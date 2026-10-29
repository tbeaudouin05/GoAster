package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/chat"
)

type fakeProvider struct{ err error }

func readyAuth(context.Context) error { return nil }

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
		{name: "health", method: "GET", path: "/healthz", host: "127.0.0.1:8765", status: 200, want: "ok"},
		{name: "chat", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","chat_id":42}`, status: 200, want: `"reply":"reply to hello"`},
		{name: "reject host", method: "GET", path: "/healthz", host: "attacker.invalid", status: 403},
		{name: "reject content type", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "text/plain", body: `{"message":"hello"}`, status: 415},
		{name: "reject missing chat ID", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello"}`, status: 400},
		{name: "reject unknown fields", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","extra":true}`, status: 400},
		{name: "credential error redacted", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","chat_id":42}`, provider: fakeProvider{err: errors.New("fake-secret")}, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
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
