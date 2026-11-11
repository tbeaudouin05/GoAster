package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/command"
	"goaster/internal/controlsecret"
)

type fakeProvider struct{ err error }

func readyAuth(context.Context) error { return nil }

const testControlSecret = "test-control-secret"

func handler(provider chat.Provider, checkAuth func(context.Context) error) http.Handler {
	return handlerWithMemory(provider, nil, checkAuth)
}

func handlerWithMemory(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error) http.Handler {
	return handlerWithMemoryAndRegistry(provider, memory, checkAuth, chat.ModelRegistry{})
}

func handlerWithMemoryAndRegistry(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error, registry chat.ModelRegistry) http.Handler {
	return handlerWithMemoryAndRegistryWithSecret(provider, memory, checkAuth, registry, testControlSecret)
}

func testAgentStore(t *testing.T) (string, chat.CredentialStore) {
	t.Helper()
	home := t.TempDir()
	store, err := agentCredentialStore(func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	return home, store
}

func (p fakeProvider) Reply(_ context.Context, _ string, message string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return "reply to " + message, nil
}

type historyProvider struct {
	mu        sync.Mutex
	histories [][]chat.ChatMessage
}

type recordingHistoryProvider func([]chat.ChatMessage) string

type recordingModelProvider func(string) (string, error)

func (p recordingModelProvider) Reply(_ context.Context, model, _ string) (string, error) {
	return p(model)
}

func (p recordingHistoryProvider) Reply(_ context.Context, _, message string) (string, error) {
	return p([]chat.ChatMessage{{Role: "user", Content: message}}), nil
}

func (p recordingHistoryProvider) ReplyWithHistory(_ context.Context, _ string, history []chat.ChatMessage) (string, error) {
	return p(history), nil
}

func (p *historyProvider) Reply(_ context.Context, _, message string) (string, error) {
	return "reply to " + message, nil
}

func (p *historyProvider) ReplyWithHistory(_ context.Context, _ string, history []chat.ChatMessage) (string, error) {
	p.mu.Lock()
	p.histories = append(p.histories, append([]chat.ChatMessage(nil), history...))
	p.mu.Unlock()
	return "reply to " + history[len(history)-1].Content, nil
}

func (p *historyProvider) snapshot() [][]chat.ChatMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	histories := make([][]chat.ChatMessage, len(p.histories))
	for i, history := range p.histories {
		histories[i] = append([]chat.ChatMessage(nil), history...)
	}
	return histories
}

type blockingHistoryProvider struct {
	mu        sync.Mutex
	active    int
	maxActive int
	callCount int
	entered   chan struct{}
	release   chan struct{}
}

func newBlockingHistoryProvider() *blockingHistoryProvider {
	return &blockingHistoryProvider{
		entered: make(chan struct{}, 4),
		release: make(chan struct{}, 4),
	}
}

func (p *blockingHistoryProvider) Reply(_ context.Context, _, message string) (string, error) {
	return "reply to " + message, nil
}

func (p *blockingHistoryProvider) ReplyWithHistory(_ context.Context, _ string, history []chat.ChatMessage) (string, error) {
	p.mu.Lock()
	p.callCount++
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	p.entered <- struct{}{}
	<-p.release
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return "reply to " + history[len(history)-1].Content, nil
}

func (p *blockingHistoryProvider) maximumActive() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

func postChat(t testing.TB, h http.Handler, chatID int64, message string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Message string `json:"message"`
		ChatID  int64  `json:"chat_id"`
	}{message, chatID})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(controlAuthorizationHeader, testControlSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postChatWithAttachments(t testing.TB, h http.Handler, chatID int64, message string, attachments []attachment.Incoming) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Message     string                `json:"message"`
		ChatID      int64                 `json:"chat_id"`
		Attachments []attachment.Incoming `json:"attachments,omitempty"`
	}{message, chatID, attachments})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(controlAuthorizationHeader, testControlSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAgentLoopbackHandler(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, host, contentType, body string
		provider                                    fakeProvider
		status                                      int
		want                                        string
	}{
		{name: "live", method: "GET", path: "/livez", host: "127.0.0.1:8765", status: 200, want: "ok"},
		{name: "health", method: "GET", path: "/healthz", host: "127.0.0.1:8765", status: 200, want: "ok"},
		{name: "chat", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","chat_id":42}`, status: 200, want: `"type":"final","reply":"reply to hello"`},
		{name: "reject host", method: "GET", path: "/healthz", host: "attacker.invalid", status: 403},
		{name: "reject content type", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "text/plain", body: `{"message":"hello"}`, status: 415},
		{name: "reject missing chat ID", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello"}`, status: 400},
		{name: "reject unknown fields", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","extra":true}`, status: 400},
		{name: "credential error redacted", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","chat_id":42}`, provider: fakeProvider{err: errors.New("fake-secret")}, status: 503},
		{name: "selected model error is direct", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","chat_id":42}`, provider: fakeProvider{err: chat.ErrModelUnavailable}, status: 503, want: "model " + chat.DefaultModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			if tc.path == "/chat" {
				r.Header.Set(controlAuthorizationHeader, testControlSecret)
			}
			w := httptest.NewRecorder()
			handler(tc.provider, readyAuth).ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("status %d, body %q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "fake-secret") {
				t.Error("provider error exposed a fake secret")
			}
		})
	}
	checks := 0
	h := handler(fakeProvider{}, func(context.Context) error { checks++; return nil })
	for _, endpoint := range []string{"/livez", "/healthz"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765"+endpoint, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", endpoint, response.Code)
		}
		if endpoint == "/livez" && checks != 0 {
			t.Fatal("liveness check called the credential check")
		}
	}
	if checks != 1 {
		t.Fatalf("readiness authentication checks = %d, want 1", checks)
	}
}

func TestServeRejectsInvalidConfiguredControlSecretBeforeStartup(t *testing.T) {
	t.Setenv(controlsecret.ValueEnv, "")
	t.Setenv(controlsecret.FileEnv, filepath.Join(t.TempDir(), "missing-control-secret"))
	if err := serve(chat.CredentialStore{}); err == nil || err.Error() != "control secret configuration is invalid" {
		t.Fatalf("serve() error = %v; want invalid control secret", err)
	}
}

func TestAgentChatRequiresDeploymentSecretBeforeUsingForgedChatID(t *testing.T) {
	providerCalls := 0
	provider := inspectingProvider(func(context.Context, string) (string, error) {
		providerCalls++
		return "unexpected", nil
	})
	h := handlerWithMemoryAndRegistryWithSecret(provider, nil, readyAuth, chat.ModelRegistry{}, "control-secret")
	post := func(secret string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", strings.NewReader(`{"chat_id":999,"message":"continue"}`))
		request.Header.Set("Content-Type", "application/json")
		if secret != "" {
			request.Header.Set(controlAuthorizationHeader, secret)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	for _, secret := range []string{"", "wrong-secret"} {
		if response := post(secret); response.Code != http.StatusUnauthorized {
			t.Fatalf("forged chat_id with secret %q status = %d, body = %q", secret, response.Code, response.Body.String())
		}
	}
	if providerCalls != 0 {
		t.Fatalf("unauthenticated forged chat_id reached provider: calls=%d", providerCalls)
	}
	if response := post("control-secret"); response.Code != http.StatusOK {
		t.Fatalf("authenticated chat status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestAgentSessionsReuseHistoryAndIsolateChats(t *testing.T) {
	provider := &historyProvider{}
	h := handler(provider, readyAuth)
	if response := postChat(t, h, 101, "first"); response.Code != http.StatusOK {
		t.Fatalf("first chat status = %d, body = %q", response.Code, response.Body.String())
	}
	if response := postChat(t, h, 202, "other"); response.Code != http.StatusOK {
		t.Fatalf("other chat status = %d, body = %q", response.Code, response.Body.String())
	}
	if response := postChat(t, h, 101, "second"); response.Code != http.StatusOK {
		t.Fatalf("second chat status = %d, body = %q", response.Code, response.Body.String())
	}

	histories := provider.snapshot()
	want := [][]chat.ChatMessage{
		{{Role: "user", Content: "first"}},
		{{Role: "user", Content: "other"}},
		{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "reply to first"},
			{Role: "user", Content: "second"},
		},
	}
	if len(histories) != len(want) {
		t.Fatalf("provider calls = %d, want %d: %#v", len(histories), len(want), histories)
	}
	for i := range want {
		if len(histories[i]) != len(want[i]) {
			t.Fatalf("history %d = %#v, want %#v", i, histories[i], want[i])
		}
		for j := range want[i] {
			if histories[i][j] != want[i][j] {
				t.Fatalf("history %d message %d = %#v, want %#v", i, j, histories[i][j], want[i][j])
			}
		}
	}
}

func TestAgentClearIsChatScopedAndWorksWithoutAuthentication(t *testing.T) {
	var histories [][]chat.ChatMessage
	provider := recordingHistoryProvider(func(history []chat.ChatMessage) string {
		snapshot := append([]chat.ChatMessage(nil), history...)
		histories = append(histories, snapshot)
		return "reply to " + history[len(history)-1].Content
	})
	authAvailable := true
	checkAuth := func(context.Context) error {
		if !authAvailable {
			return errors.New("private credential detail")
		}
		return nil
	}
	h := handler(provider, checkAuth)
	for _, request := range []struct {
		chatID  int64
		message string
	}{
		{101, "first private message"},
		{-100200, "first group message"},
	} {
		if response := postChat(t, h, request.chatID, request.message); response.Code != http.StatusOK {
			t.Fatalf("initial chat %d status = %d, body = %q", request.chatID, response.Code, response.Body.String())
		}
	}

	authAvailable = false
	malformed := postChat(t, h, 101, "/clear extra")
	if malformed.Code != http.StatusOK || !strings.Contains(malformed.Body.String(), `"reply":"Usage: /clear"`) {
		t.Fatalf("malformed clear response = %d %q", malformed.Code, malformed.Body.String())
	}
	unavailable := postChat(t, h, 101, "provider request")
	if unavailable.Code != http.StatusServiceUnavailable || strings.Contains(unavailable.Body.String(), "private credential detail") {
		t.Fatalf("normal request while unauthenticated = %d %q", unavailable.Code, unavailable.Body.String())
	}
	authAvailable = true
	if response := postChat(t, h, 101, "before clear followup"); response.Code != http.StatusOK {
		t.Fatalf("followup after malformed clear status = %d", response.Code)
	}
	if response := postChat(t, h, -100200, "group followup"); response.Code != http.StatusOK {
		t.Fatalf("group followup status = %d", response.Code)
	}

	authAvailable = false
	cleared := postChat(t, h, 101, "/clear")
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"reply":"Conversation history cleared."`) {
		t.Fatalf("clear response = %d %q", cleared.Code, cleared.Body.String())
	}
	authAvailable = true
	if response := postChat(t, h, 101, "after clear"); response.Code != http.StatusOK {
		t.Fatalf("post-clear chat status = %d", response.Code)
	}
	if response := postChat(t, h, -100200, "group after clear"); response.Code != http.StatusOK {
		t.Fatalf("other chat status = %d", response.Code)
	}

	want := [][]chat.ChatMessage{
		{{Role: "user", Content: "first private message"}},
		{{Role: "user", Content: "first group message"}},
		{
			{Role: "user", Content: "first private message"},
			{Role: "assistant", Content: "reply to first private message"},
			{Role: "user", Content: "before clear followup"},
		},
		{
			{Role: "user", Content: "first group message"},
			{Role: "assistant", Content: "reply to first group message"},
			{Role: "user", Content: "group followup"},
		},
		{{Role: "user", Content: "after clear"}},
		{
			{Role: "user", Content: "first group message"},
			{Role: "assistant", Content: "reply to first group message"},
			{Role: "user", Content: "group followup"},
			{Role: "assistant", Content: "reply to group followup"},
			{Role: "user", Content: "group after clear"},
		},
	}
	if len(histories) != len(want) {
		t.Fatalf("provider histories = %#v, want %#v", histories, want)
	}
	for index := range want {
		if !reflect.DeepEqual(histories[index], want[index]) {
			t.Fatalf("history %d = %#v, want %#v", index, histories[index], want[index])
		}
	}
}

type inspectingProvider func(context.Context, string) (string, error)

func (p inspectingProvider) Reply(ctx context.Context, _ string, message string) (string, error) {
	return p(ctx, message)
}

func attachmentPath(message string) string {
	_, path, ok := strings.Cut(message, "local_path: ")
	if !ok {
		return ""
	}
	path, _, _ = strings.Cut(path, "\n")
	return path
}

func TestAgentCreatesPrivateAttachmentForTurn(t *testing.T) {
	tempRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", tempRoot)
	} else {
		t.Setenv("TMPDIR", tempRoot)
	}
	data := []byte("attachment text")
	item := attachment.Incoming{Type: "document", Filename: "notes.txt", MIMEType: "text/plain", Size: int64(len(data)), Data: data}
	var pathName string
	provider := inspectingProvider(func(ctx context.Context, message string) (string, error) {
		pathName = attachmentPath(message)
		if filepath.Dir(filepath.Dir(pathName)) != tempRoot {
			t.Fatalf("attachment not in agent temporary directory: %q", pathName)
		}
		if got, err := os.ReadFile(pathName); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("agent attachment = %q, error = %v", got, err)
		}
		fileInfo, err := os.Stat(pathName)
		if err != nil || fileInfo.Mode().Perm() != 0600 {
			t.Fatalf("attachment file mode or stat error: %v, %v", fileInfo, err)
		}
		dirInfo, err := os.Stat(filepath.Dir(pathName))
		if err != nil || dirInfo.Mode().Perm() != 0700 {
			t.Fatalf("attachment directory mode or stat error: %v, %v", dirInfo, err)
		}
		cmd := "cat -- '" + strings.ReplaceAll(pathName, "'", "'\\''") + "'"
		if runtime.GOOS == "windows" {
			cmd = "Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(pathName, "'", "''") + "'"
		}
		result, err := command.Execute(ctx, command.Request{Command: cmd})
		if err != nil || !strings.Contains(result.Stdout, string(data)) {
			t.Fatalf("shell could not read attachment: %+v, %v", result, err)
		}
		return "document read", nil
	})
	h := handler(provider, readyAuth)
	response := postChatWithAttachments(t, h, 404, "", []attachment.Incoming{item})
	if response.Code != http.StatusOK {
		t.Fatalf("media-only request status = %d, body = %q", response.Code, response.Body.String())
	}
	if pathName == "" {
		t.Fatal("provider did not receive a local attachment path")
	}
	if _, err := os.Stat(filepath.Dir(pathName)); !os.IsNotExist(err) {
		t.Fatalf("agent did not clean up its attachment directory: %v", err)
	}
	if response := postChat(t, h, 405, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("empty request without attachment status = %d, want 400", response.Code)
	}
}

func TestAgentRecognizesImageDocumentAndMakesItReadable(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	item := attachment.Incoming{Type: "document", Filename: "image.txt", MIMEType: "text/plain", Size: int64(data.Len()), Data: data.Bytes()}
	var pathName string
	provider := inspectingProvider(func(_ context.Context, message string) (string, error) {
		pathName = attachmentPath(message)
		if !strings.Contains(message, "type: image") || !strings.Contains(message, "mime_type: image/png") {
			t.Fatalf("image document classified incorrectly: %q", message)
		}
		metadata := attachment.Metadata{Type: "image", Filename: item.Filename, Path: pathName, MIMEType: "image/png", Size: item.Size}
		dataURL, err := attachment.ReadImageDataURL(metadata)
		if err != nil || !strings.HasPrefix(dataURL, "data:image/png;base64,") {
			t.Fatalf("agent image unavailable for input_image: %q %v", dataURL, err)
		}
		return "image read", nil
	})
	response := postChatWithAttachments(t, handler(provider, readyAuth), 1, "", []attachment.Incoming{item})
	if response.Code != http.StatusOK || pathName == "" {
		t.Fatalf("image request status = %d, body = %q", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(pathName)); !os.IsNotExist(err) {
		t.Fatalf("image directory remains after the turn: %v", err)
	}
}

func TestAgentRejectsUntrustedAttachmentMetadata(t *testing.T) {
	h := handler(fakeProvider{}, readyAuth)
	for name, body := range map[string]string{
		"path authority":  `{"chat_id":1,"attachments":[{"type":"document","filename":"x","size":1,"data":"eA==","path":"/etc/passwd"}]}`,
		"unsafe filename": `{"chat_id":1,"attachments":[{"type":"document","filename":"../x","size":1,"data":"eA=="}]}`,
		"wrong size":      `{"chat_id":1,"attachments":[{"type":"document","filename":"x","size":2,"data":"eA=="}]}`,
		"missing data":    `{"chat_id":1,"attachments":[{"type":"document","filename":"x","size":0}]}`,
		"bad base64":      `{"chat_id":1,"attachments":[{"type":"document","filename":"x","size":1,"data":"%%%"}]}`,
		"invalid mime":    `{"chat_id":1,"attachments":[{"type":"document","filename":"x","mime_type":"text/plain\nextra","size":1,"data":"eA=="}]}`,
		"multiple":        `{"chat_id":1,"attachments":[{"type":"document","filename":"x","size":1,"data":"eA=="},{"type":"document","filename":"y","size":1,"data":"eQ=="}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(controlAuthorizationHeader, testControlSecret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
			}
		})
	}
	invalidImage := attachment.Incoming{Type: "image", Filename: "x.png", Size: 3, Data: []byte("bad")}
	if response := postChatWithAttachments(t, h, 1, "", []attachment.Incoming{invalidImage}); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("invalid image status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestAgentAttachmentAndEnvelopeSizeLimits(t *testing.T) {
	h := handler(inspectingProvider(func(context.Context, string) (string, error) { return "ok", nil }), readyAuth)
	data := bytes.Repeat([]byte("x"), int(attachment.MaxBytes)+1)
	item := attachment.Incoming{Type: "document", Filename: "large.bin", Size: attachment.MaxBytes, Data: data[:attachment.MaxBytes]}
	if response := postChatWithAttachments(t, h, 1, "read", []attachment.Incoming{item}); response.Code != http.StatusOK {
		t.Fatalf("20 MiB attachment status = %d, body = %q", response.Code, response.Body.String())
	}
	item.Data, item.Size = data, int64(len(data))
	if response := postChatWithAttachments(t, h, 1, "read", []attachment.Incoming{item}); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized attachment status = %d", response.Code)
	}
	// The old 1 MiB body limit remains in force for text-only requests.
	body := `{"chat_id":1,"message":"` + strings.Repeat("x", int(maxChatEnvelopeBytes)) + `"}`
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(controlAuthorizationHeader, testControlSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized text-only request status = %d", w.Code)
	}
}

func TestAgentCleansAttachmentAfterProviderError(t *testing.T) {
	for name, providerErr := range map[string]error{"error": errors.New("fake-secret"), "timeout": context.DeadlineExceeded} {
		t.Run(name, func(t *testing.T) {
			var pathName string
			h := handler(inspectingProvider(func(_ context.Context, message string) (string, error) {
				pathName = attachmentPath(message)
				return "", providerErr
			}), readyAuth)
			item := attachment.Incoming{Type: "document", Filename: "x.txt", Size: 1, Data: []byte("x")}
			response := postChatWithAttachments(t, h, 1, "read", []attachment.Incoming{item})
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "fake-secret") {
				t.Fatalf("provider error response = %d %q", response.Code, response.Body.String())
			}
			if pathName == "" {
				t.Fatal("provider did not receive the attachment")
			}
			if _, err := os.Stat(filepath.Dir(pathName)); !os.IsNotExist(err) {
				t.Fatalf("attachment directory remains after provider error: %v", err)
			}
		})
	}
}

func TestAgentKeepsAttachmentUntilCanceledTurnReturns(t *testing.T) {
	entered := make(chan string, 1)
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseProvider()
	provider := inspectingProvider(func(ctx context.Context, message string) (string, error) {
		pathName := attachmentPath(message)
		entered <- pathName
		<-ctx.Done()
		close(canceled)
		<-release
		if _, err := os.ReadFile(pathName); err != nil {
			return "", fmt.Errorf("attachment disappeared before turn finished: %w", err)
		}
		return "", ctx.Err()
	})
	item := attachment.Incoming{Type: "document", Filename: "x.txt", Size: 4, Data: []byte("data")}
	body, err := json.Marshal(struct {
		ChatID      int64                 `json:"chat_id"`
		Attachments []attachment.Incoming `json:"attachments"`
	}{1, []attachment.Incoming{item}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(controlAuthorizationHeader, testControlSecret)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		handler(provider, readyAuth).ServeHTTP(w, r)
		done <- w
	}()
	var pathName string
	select {
	case pathName = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not enter attachment turn")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not observe cancellation")
	}
	if _, err := os.ReadFile(pathName); err != nil {
		t.Fatalf("attachment disappeared before the turn returned: %v", err)
	}
	releaseProvider()
	select {
	case response := <-done:
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("canceled request status = %d", response.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled handler did not finish")
	}
	if _, err := os.Stat(filepath.Dir(pathName)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory remains after canceled turn: %v", err)
	}
}

func TestAgentSessionsSerializeSameChatAndRunDifferentChatsConcurrently(t *testing.T) {
	t.Run("same chat is serialized", func(t *testing.T) {
		provider := newBlockingHistoryProvider()
		started := make(chan struct{}, 2)
		base := handler(provider, readyAuth)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			base.ServeHTTP(w, r)
		})

		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() { firstDone <- postChat(t, h, 303, "first") }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first request did not start")
		}
		select {
		case <-provider.entered:
		case <-time.After(time.Second):
			t.Fatal("first provider call did not start")
		}

		secondDone := make(chan *httptest.ResponseRecorder, 1)
		go func() { secondDone <- postChat(t, h, 303, "second") }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("second request did not start")
		}
		select {
		case <-provider.entered:
			t.Fatal("same-chat provider calls overlapped")
		case <-time.After(100 * time.Millisecond):
		}

		provider.release <- struct{}{}
		if response := <-firstDone; response.Code != http.StatusOK {
			t.Fatalf("first response status = %d", response.Code)
		}
		select {
		case <-provider.entered:
		case <-time.After(time.Second):
			t.Fatal("second provider call did not start after the first completed")
		}
		provider.release <- struct{}{}
		if response := <-secondDone; response.Code != http.StatusOK {
			t.Fatalf("second response status = %d", response.Code)
		}
		if got := provider.maximumActive(); got != 1 {
			t.Fatalf("maximum same-chat provider concurrency = %d, want 1", got)
		}
	})

	t.Run("different chats run concurrently", func(t *testing.T) {
		provider := newBlockingHistoryProvider()
		h := handler(provider, readyAuth)
		responses := make(chan *httptest.ResponseRecorder, 2)
		go func() { responses <- postChat(t, h, 404, "one") }()
		go func() { responses <- postChat(t, h, 505, "two") }()
		for i := 0; i < 2; i++ {
			select {
			case <-provider.entered:
			case <-time.After(time.Second):
				t.Fatal("different-chat provider call did not start")
			}
		}
		if got := provider.maximumActive(); got != 2 {
			t.Fatalf("maximum different-chat provider concurrency = %d, want 2", got)
		}
		provider.release <- struct{}{}
		provider.release <- struct{}{}
		for i := 0; i < 2; i++ {
			if response := <-responses; response.Code != http.StatusOK {
				t.Fatalf("different-chat response status = %d", response.Code)
			}
		}
	})
}

func TestAgentModelCommandResolvesAliasPersistsChatSelectionAndSkipsAuth(t *testing.T) {
	registry := chat.NewModelRegistryAt(filepath.Join(t.TempDir(), "openai-models.json"))
	checkAuth := func(context.Context) error { return errors.New("authentication unavailable") }
	h := handlerWithMemoryAndRegistry(fakeProvider{}, nil, checkAuth, registry)

	current := postChat(t, h, 101, "/m")
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"reply":"Current model: gpt-6.1-terra (terra)"`) {
		t.Fatalf("current model response = %d %q", current.Code, current.Body.String())
	}
	selected := postChat(t, h, 101, "/m sol")
	if selected.Code != http.StatusOK || !strings.Contains(selected.Body.String(), `"reply":"Selected model: gpt-6.1-sol (sol)"`) {
		t.Fatalf("alias selection response = %d %q", selected.Code, selected.Body.String())
	}
	current = postChat(t, h, 101, "/m")
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"reply":"Current model: gpt-6.1-sol (sol)"`) {
		t.Fatalf("persisted model response = %d %q", current.Code, current.Body.String())
	}
	other := postChat(t, h, 202, "/m")
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), `"reply":"Current model: gpt-6.1-terra (terra)"`) {
		t.Fatalf("other chat response = %d %q", other.Code, other.Body.String())
	}

	data, err := os.ReadFile(registry.Path)
	if err != nil {
		t.Fatal(err)
	}
	var config chat.ModelConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Chats["101"] != "gpt-6.1-sol" {
		t.Fatalf("persisted model = %q, want %q", config.Chats["101"], "gpt-6.1-sol")
	}
	for message, want := range map[string]string{
		"/m gpt-5.6-luna": "Selected model: gpt-5.6-luna (luna)",
		"/m missing":      "Model is not configured: missing",
		"/m sol terra":    "Usage: /m [MODEL_OR_ALIAS]",
	} {
		response := postChat(t, h, 101, message)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Errorf("response to %q = %d %q", message, response.Code, response.Body.String())
		}
	}
}

func TestAgentReportsModelRecoveryEventOnceAndUsesEmergencyModel(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	if err := os.WriteFile(pathName, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := chat.NewModelRegistryAt(pathName)
	provider := &historyProvider{}
	h := handlerWithMemoryAndRegistry(provider, nil, readyAuth, registry)
	command := postChat(t, h, 303, "/m")
	if command.Code != http.StatusOK || !strings.Contains(command.Body.String(), chat.EmergencyModel) {
		t.Fatalf("model command during recovery = %d %q", command.Code, command.Body.String())
	}
	clear := postChat(t, h, 303, "/clear")
	if clear.Code != http.StatusOK {
		t.Fatalf("clear during recovery = %d %q", clear.Code, clear.Body.String())
	}
	first := postChat(t, h, 303, "hello")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"type":"model_recovery"`) || !strings.Contains(first.Body.String(), "parse model configuration") {
		t.Fatalf("recovery event = %d %q", first.Code, first.Body.String())
	}
	if len(provider.histories) != 1 || provider.histories[0][0].Role != "system" || !strings.Contains(provider.histories[0][0].Content, "parse model configuration") {
		t.Fatalf("recovery message was not supplied to the model: %#v", provider.histories)
	}
	second := postChat(t, h, 303, "again")
	if second.Code != http.StatusOK || strings.Contains(second.Body.String(), `"type":"model_recovery"`) {
		t.Fatalf("recovery event repeated = %d %q", second.Code, second.Body.String())
	}
	for _, message := range provider.histories[1] {
		if strings.Contains(message.Content, "model configuration recovery notice") {
			t.Fatalf("recovery notice repeated in provider input: %#v", provider.histories[1])
		}
	}
}

func TestAgentReloadsUpdatedModelForExistingChat(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	registry := chat.NewModelRegistryAt(pathName)
	provider := recordingModelProvider(func(model string) (string, error) { return model, nil })
	h := handlerWithMemoryAndRegistry(provider, nil, readyAuth, registry)
	before := postChat(t, h, 101, "before")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"reply":"`+chat.DefaultModel+`"`) {
		t.Fatalf("before update = %d %q", before.Code, before.Body.String())
	}
	temporary := filepath.Join(filepath.Dir(pathName), "updated.json")
	updated := `{"provider":"openai","models":[{"id":"gpt-6.1-sol","alias":"sol"},{"id":"gpt-new","alias":"new"}],"chats":{"101":"gpt-new"}}`
	if err := os.WriteFile(temporary, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, pathName); err != nil {
		t.Fatal(err)
	}
	after := postChat(t, h, 101, "after")
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"reply":"gpt-new"`) {
		t.Fatalf("after update = %d %q", after.Code, after.Body.String())
	}
}

func TestAgentKeepsRecoveryNoticeAfterConfigRepair(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	if err := os.WriteFile(pathName, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	var histories [][]chat.ChatMessage
	provider := recordingHistoryProvider(func(history []chat.ChatMessage) string {
		histories = append(histories, history)
		return "ok"
	})
	h := handlerWithMemoryAndRegistry(provider, nil, readyAuth, chat.NewModelRegistryAt(pathName))
	if response := postChat(t, h, 101, "/m"); response.Code != http.StatusOK {
		t.Fatalf("model command = %d %q", response.Code, response.Body.String())
	}
	valid := `{"provider":"openai","models":[{"id":"gpt-6.1-sol","alias":"sol"}],"chats":{}}`
	if err := os.WriteFile(pathName, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if response := postChat(t, h, 202, "hello"); response.Code != http.StatusOK {
		t.Fatalf("first normal request = %d %q", response.Code, response.Body.String())
	}
	if response := postChat(t, h, 101, "hello"); response.Code != http.StatusOK {
		t.Fatalf("second normal request = %d %q", response.Code, response.Body.String())
	}
	if len(histories) != 2 || len(histories[0]) == 0 || !strings.Contains(histories[0][0].Content, "parse model configuration") {
		t.Fatalf("repair lost recovery notice: %#v", histories)
	}
	for _, message := range histories[1] {
		if strings.Contains(message.Content, "model configuration recovery notice") {
			t.Fatalf("recovery notice repeated after repair: %#v", histories[1])
		}
	}
}

var _ http.Handler = handler(fakeProvider{}, readyAuth)

func TestRemoteLoginIsIndependentAndGatesReadiness(t *testing.T) {
	home, remoteStore := testAgentStore(t)
	localStore, err := chat.NewCredentialStore(filepath.Join(home, ".goaster", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	localCredential := chat.Credential{AccessToken: "local-access", RefreshToken: "local-refresh", ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}
	if err := localStore.Save(localCredential); err != nil {
		t.Fatal(err)
	}
	customStore := chat.CredentialStore{Path: filepath.Join(filepath.Dir(localStore.Path), "custom-auth.json")}
	if err := customStore.Save(chat.Credential{AccessToken: "custom-local-access"}); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(filepath.Dir(localStore.Path), "session.json")
	if err := os.WriteFile(sessionPath, []byte("local-session"), 0600); err != nil {
		t.Fatal(err)
	}
	if remoteStore.Path == localStore.Path || remoteStore.Path != filepath.Join(home, ".goaster", "agent-auth.json") {
		t.Fatalf("remote credential path is not separate: %q", remoteStore.Path)
	}
	provider := chat.NewCodexProvider(remoteStore)
	h := handler(fakeProvider{}, provider.CheckAuth)
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/healthz", nil))
		if w.Code != want {
			t.Fatalf("health status = %d, want %d", w.Code, want)
		}
		if response := postChat(t, h, 42, "hello"); response.Code != want {
			t.Fatalf("chat status = %d, want %d", response.Code, want)
		}
	}
	check(http.StatusServiceUnavailable)

	pollStarted := make(chan struct{})
	allowLogin := make(chan struct{})
	release := sync.OnceFunc(func() { close(allowLogin) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read remote login request: %v", err)
		}
		for _, value := range []string{"local-access", "local-refresh", "custom-local-access", "local-session"} {
			if strings.Contains(string(body), value) || strings.Contains(r.Header.Get("Authorization"), value) || strings.Contains(r.URL.String(), value) {
				t.Error("local authentication or session was sent during remote login")
			}
		}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"remote-device","user_code":"REMOTE-CODE","interval":1}`)
		case "/api/accounts/deviceauth/token":
			close(pollStarted)
			<-allowLogin
			fmt.Fprint(w, `{"authorization_code":"remote-code","code_verifier":"verifier"}`)
		case "/oauth/token":
			fmt.Fprint(w, `{"access_token":"remote-access","refresh_token":"remote-refresh","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer release()
	auth := chat.NewOpenAIAuthenticator(remoteStore)
	auth.AuthBaseURL, auth.HTTPClient = server.URL, server.Client()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), []string{"login"}, &output, auth, func(chat.CredentialStore) error {
			return errors.New("login unexpectedly started the server")
		})
	}()
	select {
	case <-pollStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("remote device login did not start")
	}
	if !strings.Contains(output.String(), server.URL+"/codex/device") || !strings.Contains(output.String(), "REMOTE-CODE") {
		t.Fatalf("remote login did not present its device instructions: %q", output.String())
	}
	check(http.StatusServiceUnavailable)
	if _, err := os.Lstat(remoteStore.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials existed before login completed: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("remote login: %v", err)
	}
	check(http.StatusOK)
	remoteCredential, err := remoteStore.Load()
	if err != nil || remoteCredential.AccessToken != "remote-access" || remoteCredential.RefreshToken != "remote-refresh" {
		t.Fatalf("remote credential was not created by its login: %v", err)
	}
	gotLocal, err := localStore.Load()
	if err != nil || gotLocal != localCredential {
		t.Fatalf("local credential changed: %v", err)
	}
	gotCustom, err := customStore.Load()
	if err != nil || gotCustom.AccessToken != "custom-local-access" {
		t.Fatalf("custom local credential changed: %v", err)
	}
	if session, err := os.ReadFile(sessionPath); err != nil || string(session) != "local-session" {
		t.Fatalf("local session changed: %v", err)
	}
	for path, want := range map[string]os.FileMode{remoteStore.Path: 0600, filepath.Dir(remoteStore.Path): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions for %s: %v, want %04o", path, err, want)
		}
	}
}

func TestAgentStartsUnauthenticatedAndRejectsOtherCommands(t *testing.T) {
	_, store := testAgentStore(t)
	auth := chat.NewOpenAIAuthenticator(store)
	starts := 0
	start := func(got chat.CredentialStore) error {
		starts++
		if got.Path != store.Path {
			t.Fatal("server received a different credential store")
		}
		return nil
	}
	if err := run(context.Background(), nil, io.Discard, auth, start); err != nil || starts != 1 {
		t.Fatalf("start: error=%v starts=%d", err, starts)
	}
	if _, err := os.Lstat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup created a credential: %v", err)
	}
	if err := run(context.Background(), []string{"setup-runtime"}, io.Discard, auth, start); err == nil || starts != 1 {
		t.Fatal("agent accepted a runtime credential transfer command")
	}
	if err := run(context.Background(), []string{"prompt"}, io.Discard, auth, start); err == nil || !strings.Contains(err.Error(), `usage: goaster-agent prompt|test-prompt "MESSAGE"`) || starts != 1 {
		t.Fatalf("invalid prompt invocation: error=%v starts=%d", err, starts)
	}
	promptHome := t.TempDir()
	t.Setenv("HOME", promptHome)
	t.Setenv("USERPROFILE", promptHome)
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", filepath.Join(filepath.Dir(filepath.Dir(working)), "skills"))
	for _, mode := range []string{"prompt", "test-prompt"} {
		if err := run(context.Background(), []string{mode, "message"}, io.Discard, auth, start); err == nil || starts != 1 {
			t.Fatalf("%s without credentials: error=%v starts=%d", mode, err, starts)
		}
	}
	if err := run(context.Background(), []string{"prompt", "agent auth check"}, io.Discard, auth, start); err == nil || starts != 1 {
		t.Fatalf("prompt without agent credentials: error=%v starts=%d", err, starts)
	}
	if err := run(context.Background(), []string{"test-prompt", "agent auth check"}, io.Discard, auth, start); err == nil || starts != 1 {
		t.Fatalf("test prompt without agent credentials: error=%v starts=%d", err, starts)
	}
	if _, err := os.Lstat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt created agent credentials: %v", err)
	}
	message := "direct agent prompt"
	received := ""
	provider := inspectingProvider(func(_ context.Context, got string) (string, error) {
		received = got
		return "direct agent reply", nil
	})
	if err := runAgentPrompt(context.Background(), message, io.Discard, provider); err != nil {
		t.Fatalf("direct prompt core: %v", err)
	}
	if received != message {
		t.Fatalf("direct prompt message = %q, want %q", received, message)
	}
}

func TestRemoteLoginFailureDoesNotCreateCredentialOrReportReady(t *testing.T) {
	_, store := testAgentStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"device","user_code":"CODE","interval":1}`)
		default:
			http.Error(w, "denied", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	auth := chat.NewOpenAIAuthenticator(store)
	auth.AuthBaseURL, auth.HTTPClient = server.URL, server.Client()
	if err := run(context.Background(), []string{"login"}, io.Discard, auth, nil); err == nil {
		t.Fatal("failed login was accepted")
	}
	if _, err := os.Lstat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed login created a credential: %v", err)
	}
	provider := chat.NewCodexProvider(store)
	w := httptest.NewRecorder()
	handler(fakeProvider{}, provider.CheckAuth).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed login health status = %d", w.Code)
	}
}

func TestCanceledRemoteLoginRemainsUnavailable(t *testing.T) {
	_, store := testAgentStore(t)
	pollStarted := make(chan struct{})
	endPoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"device","user_code":"CODE","interval":1}`)
		case "/api/accounts/deviceauth/token":
			close(pollStarted)
			select {
			case <-r.Context().Done():
			case <-endPoll:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(endPoll)
	auth := chat.NewOpenAIAuthenticator(store)
	auth.AuthBaseURL, auth.HTTPClient = server.URL, server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"login"}, io.Discard, auth, nil) }()
	select {
	case <-pollStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("remote device login did not start")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled login succeeded")
	}
	if _, err := os.Lstat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled login created a credential: %v", err)
	}
	provider := chat.NewCodexProvider(store)
	w := httptest.NewRecorder()
	handler(fakeProvider{}, provider.CheckAuth).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled login health status = %d", w.Code)
	}
}

func TestAgentDoesNotAcceptExpiredOrInsecureCredentials(t *testing.T) {
	_, store := testAgentStore(t)
	provider := chat.NewCodexProvider(store)
	h := handler(fakeProvider{}, provider.CheckAuth)
	checkUnavailable := func() {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/healthz", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("health status = %d, want unavailable", w.Code)
		}
	}
	if err := store.Save(chat.Credential{AccessToken: "expired", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	checkUnavailable()
	if err := store.Save(chat.Credential{AccessToken: "unexpired", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Path, 0644); err != nil {
		t.Fatal(err)
	}
	checkUnavailable()
}

func TestAgentAuthErrorsAreRedactedAndGateChat(t *testing.T) {
	check := func(context.Context) error { return errors.New("private credential detail") }
	h := handler(fakeProvider{}, check)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/healthz", nil))
	chatResponse := postChat(t, h, 42, "hello")
	for _, response := range []*httptest.ResponseRecorder{w, chatResponse} {
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private credential detail") {
			t.Fatalf("authentication response = %d %q", response.Code, response.Body.String())
		}
	}
}

func TestAgentReportsSafeSkillRecoveryInDirectAndStreamResponses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	skillDir := filepath.Join(home, ".goaster", "artefacts", "skills", "bad")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "META.md"), []byte("private-fixture-content"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := chat.NewCodexProvider(chat.CredentialStore{Path: filepath.Join(t.TempDir(), "unused")})
	_, skillErr := chat.NewSession(chat.Config{Model: "test"}, provider).Reply(context.Background(), "run")
	message := chat.SkillRecoveryMessage(skillErr)
	if message == "" {
		t.Fatalf("missing skill recovery error: %v", skillErr)
	}
	result := postChat(t, handler(fakeProvider{err: skillErr}, readyAuth), 42, "run")
	if result.Code != http.StatusServiceUnavailable || result.Header().Get("X-GoAster-Error") != "skill-recovery" || !strings.Contains(result.Body.String(), message) || strings.Contains(result.Body.String(), "private-fixture") {
		t.Fatalf("direct recovery = %d %q", result.Code, result.Body.String())
	}
	modelPath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(modelPath, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	h := handlerWithMemoryAndRegistry(fakeProvider{err: skillErr}, nil, readyAuth, chat.NewModelRegistryAt(modelPath))
	result = postChat(t, h, 42, "run")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"skill_recovery"`) || !strings.Contains(result.Body.String(), message) || strings.Contains(result.Body.String(), "private-fixture") {
		t.Fatalf("stream recovery = %d %q", result.Code, result.Body.String())
	}

	for _, postToolErr := range []error{postToolAgentError{}, postToolAgentError{cause: context.Canceled}, postToolAgentError{cause: context.DeadlineExceeded}} {
		message = chat.PostToolAuthenticationMessage(postToolErr)
		if message == "" {
			t.Fatal("missing post-tool warning")
		}
		result = postChat(t, handler(fakeProvider{err: fmt.Errorf("fake-secret: %w", postToolErr)}, readyAuth), 43, "run")
		if result.Code != http.StatusServiceUnavailable || result.Header().Get("X-GoAster-Error") != "post-tool-authentication" || strings.TrimSpace(result.Body.String()) != message {
			t.Fatalf("direct post-tool warning = %d %q", result.Code, result.Body.String())
		}
		modelPath = filepath.Join(t.TempDir(), "models.json")
		if err := os.WriteFile(modelPath, []byte("broken"), 0600); err != nil {
			t.Fatal(err)
		}
		h = handlerWithMemoryAndRegistry(fakeProvider{err: postToolErr}, nil, readyAuth, chat.NewModelRegistryAt(modelPath))
		result = postChat(t, h, 43, "run")
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"post_tool_authentication"`) || !strings.Contains(result.Body.String(), message) {
			t.Fatalf("stream post-tool warning = %d %q", result.Code, result.Body.String())
		}
	}
	result = postChat(t, handler(fakeProvider{err: errors.New(message + " fake-secret")}, readyAuth), 44, "run")
	if result.Header().Get("X-GoAster-Error") == "post-tool-authentication" || strings.Contains(result.Body.String(), "fake-secret") || strings.Contains(result.Body.String(), message) {
		t.Fatalf("forged error displayed: %d %q", result.Code, result.Body.String())
	}
}

type postToolAgentError struct{ cause error }

func (postToolAgentError) Error() string {
	return "OpenAI authentication failed after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state, then resolve OpenAI authentication before retrying."
}

func (err postToolAgentError) Is(target error) bool { return target.Error() == err.Error() }
func (err postToolAgentError) Unwrap() error        { return err.cause }
