package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/attachment"
)

func TestCredentialStoreSaveAndLoadIsPrivate(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "nested", "auth.json")}
	want := Credential{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-123",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}

	fileInfo, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("credential file permissions = %04o, want 0600", got)
	}
	directoryInfo, err := os.Stat(filepath.Dir(store.Path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("credential directory permissions = %04o, want 0700", got)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("loaded credentials = %+v, want %+v", got, want)
	}

	if err := os.Chmod(store.Path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("insecure credential file load error = %v", err)
	}
}

func TestOpenAIAuthenticatorDeviceLogin(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	var pollCount atomic.Int32
	var exchangeForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("device code request = %s %s, content type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
			}
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode device code request: %v", err)
			}
			if request["client_id"] != openAIClientID {
				t.Errorf("client id = %q, want %q", request["client_id"], openAIClientID)
			}
			fmt.Fprint(w, `{"device_auth_id":"device-123","usercode":"ABCD-EFGH","interval":"0"}`)
		case "/api/accounts/deviceauth/token":
			if pollCount.Add(1) == 1 {
				http.Error(w, "pending", http.StatusForbidden)
				return
			}
			fmt.Fprint(w, `{"authorization_code":"authorization-code","code_verifier":"verifier"}`)
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			exchangeForm = r.PostForm
			fmt.Fprintf(w, `{"access_token":"access-token","refresh_token":"refresh-token","expires_in":3600,"id_token":%q}`, testJWT(map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-123"},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator := NewOpenAIAuthenticator(store)
	authenticator.AuthBaseURL = server.URL
	authenticator.HTTPClient = server.Client()
	authenticator.Sleep = func(context.Context, time.Duration) error { return nil }
	var output bytes.Buffer
	if err := authenticator.Login(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), server.URL+"/codex/device") || !strings.Contains(output.String(), "ABCD-EFGH") {
		t.Fatalf("login output = %q", output.String())
	}
	if got := pollCount.Load(); got != 2 {
		t.Fatalf("poll count = %d, want 2", got)
	}
	if exchangeForm.Get("grant_type") != "authorization_code" || exchangeForm.Get("code") != "authorization-code" || exchangeForm.Get("code_verifier") != "verifier" {
		t.Fatalf("authorization exchange form = %v", exchangeForm)
	}
	credential, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "access-token" || credential.RefreshToken != "refresh-token" || credential.AccountID != "account-123" {
		t.Fatalf("saved credential = %+v", credential)
	}
}

func TestCodexProviderEnsureAuthStartsDeviceLoginBeforeChat(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"device-123","user_code":"ABCD-EFGH","interval":1}`)
		case "/api/accounts/deviceauth/token":
			pollCount.Add(1)
			fmt.Fprint(w, `{"authorization_code":"authorization-code","code_verifier":"verifier"}`)
		case "/oauth/token":
			fmt.Fprint(w, `{"access_token":"access-token","refresh_token":"refresh-token","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.auth.AuthBaseURL = server.URL
	provider.auth.HTTPClient = server.Client()
	provider.auth.Sleep = func(context.Context, time.Duration) error { return nil }
	var output bytes.Buffer
	if err := provider.EnsureAuth(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if got := pollCount.Load(); got != 1 {
		t.Fatalf("poll count = %d, want 1", got)
	}
	if !strings.Contains(output.String(), "ABCD-EFGH") || !strings.Contains(output.String(), "authentication complete") {
		t.Fatalf("authentication output = %q", output.String())
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), strings.NewReader(""), &output, NewSession(Config{
		Model: DefaultModel,
	}, provider)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GoAster — OpenAI subscription via ChatGPT/Codex backend") {
		t.Fatalf("chat did not start after authentication: %q", output.String())
	}
}

func TestCodexProviderEnsureAuthUsesValidCredential(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.auth.AuthBaseURL = server.URL
	provider.auth.HTTPClient = server.Client()
	var output bytes.Buffer
	if err := provider.EnsureAuth(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("authentication requests = %d, want 0", got)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected authentication output = %q", output.String())
	}
}

func TestCodexProviderEnsureAuthRefreshesTokensAndKeepsAccount(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		AccountID:    "account-123",
		ExpiresAt:    time.Unix(100, 0),
	}); err != nil {
		t.Fatal(err)
	}
	var refreshCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		refreshCount.Add(1)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("refresh content type = %q", r.Header.Get("Content-Type"))
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode refresh request: %v", err)
		}
		if request["grant_type"] != "refresh_token" || request["refresh_token"] != "old-refresh" || request["client_id"] != openAIClientID {
			t.Errorf("refresh request = %v", request)
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.auth.AuthBaseURL = server.URL
	provider.auth.HTTPClient = server.Client()
	provider.auth.Now = func() time.Time { return time.Unix(200, 0) }
	if err := provider.EnsureAuth(context.Background()); err != nil {
		t.Fatal(err)
	}
	credential, err := provider.auth.Credential(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "new-access" || credential.RefreshToken != "new-refresh" || credential.AccountID != "account-123" {
		t.Fatalf("refreshed credential = %+v", credential)
	}
	if _, err := provider.auth.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := refreshCount.Load(); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
}

func TestConcurrentAuthChecksShareOneRefresh(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected authentication request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		refreshes.Add(1)
		fmt.Fprint(w, `{"access_token":"refreshed","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	provider := NewCodexProvider(store)
	provider.auth.AuthBaseURL = server.URL
	provider.auth.HTTPClient = server.Client()
	start := make(chan struct{})
	errs := make(chan error, 16)
	var workers sync.WaitGroup
	for i := 0; i < cap(errs); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs <- provider.CheckAuth(context.Background())
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("authentication check: %v", err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want 1", got)
	}
}

func TestOpenAIInstructionsGuideAccurateMultiStepProgress(t *testing.T) {
	for _, want := range []string{
		"For genuinely multi-step workflows, first present a concise numbered plan and a total based on the known scope",
		"do not invent a total when discovery is still needed",
		"After each meaningful completed business step or approval-gated stage, report actual completed, total, and remaining counts in plain text",
		"Progress: 5/10 steps done, 5 more to go.",
		"If remaining scope is unknown, say what is complete and that the rest is still being assessed, without a numeric total or remaining count.",
		"If discovery changes the plan, briefly explain the finding, revise the numbered plan and total, and report subsequent progress against that revision.",
		"Do not count individual shell commands as business steps.",
		"Keep single-step questions and tasks concise, without a plan or progress banner.",
		"Progress reporting does not change or replace existing plain-text, approval, safety, secret-handling, or partial-onboarding rules.",
	} {
		if !strings.Contains(openAIInstructions, want) {
			t.Errorf("OpenAI instructions missing multi-step progress guidance %q", want)
		}
	}
}

func TestCodexProviderUsesSubscriptionBackendAndStreamsText(t *testing.T) {
	verifyStarterPolicies(t)
	setupSkillFixture(t)
	for _, test := range []struct {
		name    string
		event   string
		wantErr string
	}{
		{
			name:  "assistant message item",
			event: `{"type":"response.output_item.added","item":{"id":"msg-1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		},
		{
			name:  "reasoning item",
			event: `{"type":"response.output_item.added","item":{"id":"reasoning-1","type":"reasoning","summary":[]}}`,
		},
		{
			name:  "item without a type",
			event: `{"type":"response.output_item.added","item":{}}`,
		},
		{
			name:    "missing item",
			event:   `{"type":"response.output_item.added"}`,
			wantErr: "missing its item",
		},
		{
			name:    "null item",
			event:   `{"type":"response.output_item.added","item":null}`,
			wantErr: "missing its item",
		},
		{
			name:    "unsupported tool call",
			event:   `{"type":"response.output_item.added","item":{"type":"web_search_call"}}`,
			wantErr: `unsupported tool call type "web_search_call"`,
		},
	} {
		stream := "data: " + test.event + "\n\ndata: " + `{"type":"response.output_text.delta","delta":"Hello"}` + "\n\n"
		parsed, err := parseCodexStreamResponse([]byte(stream))
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("%s: error = %v, want %q", test.name, err, test.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", test.name, err)
			continue
		}
		if parsed.Text != "Hello" || len(parsed.ToolCalls) != 0 {
			t.Errorf("%s: response = %+v, want only text Hello", test.name, parsed)
		}
	}

	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-ID") != "account-123" {
			t.Errorf("auth headers: authorization=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"))
		}
		if r.Header.Get("originator") != openAIOriginator || r.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Errorf("Codex headers: originator=%q beta=%q", r.Header.Get("originator"), r.Header.Get("OpenAI-Beta"))
		}
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
		}
		if request.Model != "gpt-test" || request.Instructions == "" || !request.Stream || request.Store || len(request.Input) != 1 || request.Input[0].Type != "message" || request.Input[0].Content[0].Text != "Install GoAster on my existing OVH server." {
			t.Errorf("Codex request = %+v", request)
		}
		for _, want := range []string{
			"All user-facing assistant replies, whether shown in the terminal UI or sent through Telegram, must use plain text only.",
			"Before performing a skill-dependent workflow, call load_skill",
			"name: fixture-skill",
			"description: A fixture for progressive skill loading.",
		} {
			if !strings.Contains(request.Instructions, want) {
				t.Errorf("Codex instructions missing %q", want)
			}
		}
		if !strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") || !strings.Contains(request.Instructions, "FIXTURE AUTHORIZATION") {
			t.Error("installed workflow or authorization was omitted before a direct tool call")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" from Codex\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL + "/responses"
	provider.httpClient = server.Client()
	reply, err := provider.Reply(context.Background(), "gpt-test", "Install GoAster on my existing OVH server.")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hello from Codex" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestCodexProviderInjectsMemoryIntoNormalInstructions(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
		}
		if !strings.Contains(request.Instructions, "# Memory") || !strings.Contains(request.Instructions, "durable fact") {
			t.Errorf("memory was not included in model instructions: %q", request.Instructions)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	ctx := WithMemoryContext(context.Background(), "# Memory\n\n## Long-term Memory\n\ndurable fact")
	if reply, err := provider.Reply(ctx, "gpt-test", "hello"); err != nil || reply != "ok" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
}

func TestCodexProviderSendsConversationHistory(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var recoveryRequests, unavailableRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
		}
		if request.Model == DefaultModel {
			unavailableRequests.Add(1)
			http.Error(w, "model unavailable", http.StatusNotFound)
			return
		}
		if request.Model == EmergencyModel {
			count := recoveryRequests.Add(1)
			found := false
			for _, item := range request.Input {
				for _, content := range item.Content {
					if item.Role == "system" && strings.Contains(content.Text, "parse model configuration") {
						found = true
					}
				}
			}
			if found != (count == 1) {
				t.Errorf("recovery notice present on request %d = %v", count, found)
			}
		} else if len(request.Input) != 3 {
			t.Errorf("history input length = %d, want 3: %+v", len(request.Input), request.Input)
		} else {
			if request.Input[0].Role != "user" || request.Input[0].Content[0].Type != "input_text" || request.Input[0].Content[0].Text != "first" {
				t.Errorf("first history item = %+v", request.Input[0])
			}
			if request.Input[1].Role != "assistant" || request.Input[1].Content[0].Type != "output_text" || request.Input[1].Content[0].Text != "answer" {
				t.Errorf("assistant history item = %+v", request.Input[1])
			}
			if request.Input[2].Role != "user" || request.Input[2].Content[0].Text != "second" {
				t.Errorf("last history item = %+v", request.Input[2])
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL + "/responses"
	provider.httpClient = server.Client()
	reply, err := provider.ReplyWithHistory(context.Background(), "gpt-test", []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}

	brokenPath := filepath.Join(t.TempDir(), "openai-models.json")
	if err := os.WriteFile(brokenPath, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	session := NewSession(Config{Registry: NewModelRegistryAt(brokenPath)}, provider)
	for _, message := range []string{"first request", "second request"} {
		if _, err := session.Reply(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if count := recoveryRequests.Load(); count != 2 {
		t.Fatalf("emergency model requests = %d, want 2", count)
	}

	validPath := filepath.Join(t.TempDir(), "openai-models.json")
	validSession := NewSession(Config{Registry: NewModelRegistryAt(validPath)}, provider)
	for _, message := range []string{"first request", "second request"} {
		if _, err := validSession.Reply(context.Background(), message); !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("unavailable model error = %v", err)
		}
	}
	if count := unavailableRequests.Load(); count != 2 || recoveryRequests.Load() != 2 {
		t.Fatalf("valid config requests: unavailable = %d, emergency = %d", count, recoveryRequests.Load())
	}
}

func TestCodexProviderSendsCurrentTurnImageAndNotLaterTextTurns(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	imageBytes := testOpenAIImageBytes(t)
	pathName := filepath.Join(directory, "claimed.jpg")
	if err := os.WriteFile(pathName, imageBytes, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := attachment.Metadata{
		Type:     "image",
		Filename: "claimed.jpg",
		Path:     pathName,
		MIMEType: "image/jpeg",
		Size:     int64(len(imageBytes)),
	}
	var requests []codexRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			if len(request.Input) != 1 || request.Input[0].Role != "user" {
				t.Errorf("image request input = %+v", request.Input)
			} else {
				if len(request.Input[0].Content) != 2 || request.Input[0].Content[0].Type != "input_text" || request.Input[0].Content[1].Type != "input_image" {
					t.Errorf("image request content = %+v", request.Input[0].Content)
				}
				imageURL := request.Input[0].Content[1].ImageURL
				if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
					t.Errorf("image URL = %q, want byte-derived PNG data URL", imageURL)
				} else if decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(imageURL, "data:image/png;base64,")); err != nil || !bytes.Equal(decoded, imageBytes) {
					t.Errorf("image payload decode error = %v or bytes changed", err)
				}
			}
			fmt.Fprint(w, `{"output_text":"image received"}`)
		case 2:
			if strings.Contains(string(mustMarshalJSON(t, request)), pathName) {
				t.Errorf("temporary image path persisted into later request: %+v", request.Input)
			}
			for _, item := range request.Input {
				for _, content := range item.Content {
					if content.Type == "input_image" || content.ImageURL != "" {
						t.Errorf("stale image content in later request: %+v", request.Input)
					}
				}
			}
			fmt.Fprint(w, `{"output_text":"text received"}`)
		default:
			t.Errorf("unexpected backend request %d", len(requests))
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	session := NewSession(Config{Model: "gpt-test"}, provider)
	if reply, err := session.ReplyWithAttachments(context.Background(), "inspect this image", []attachment.Metadata{metadata}); err != nil || reply != "image received" {
		t.Fatalf("image reply = %q, error = %v", reply, err)
	}
	if reply, err := session.Reply(context.Background(), "now answer in text"); err != nil || reply != "text received" {
		t.Fatalf("text reply = %q, error = %v", reply, err)
	}
	if len(requests) != 2 {
		t.Fatalf("backend requests = %d, want 2", len(requests))
	}
}

func TestCodexProviderShellReadsCurrentTurnDocumentBytes(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	content := "real downloaded text bytes\n"
	pathName := filepath.Join(directory, "downloaded.txt")
	if err := os.WriteFile(pathName, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := attachment.Metadata{Type: "document", Filename: "downloaded.txt", Path: pathName, MIMEType: "text/plain", Size: int64(len(content))}
	arguments, err := json.Marshal(map[string]string{"command": readDocumentCommand(pathName)})
	if err != nil {
		t.Fatal(err)
	}
	var requests []codexRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			writeCodexFunctionCall(t, w, "read-document", "shell_execute", string(arguments))
		case 2:
			if len(request.Input) != 3 || !strings.Contains(request.Input[2].Output, content) {
				t.Errorf("shell output did not contain downloaded document bytes: %+v", request.Input)
			}
			fmt.Fprint(w, `{"output_text":"document read"}`)
		default:
			t.Errorf("unexpected backend request %d", len(requests))
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	session := NewSession(Config{Model: "gpt-test"}, provider)
	reply, err := session.ReplyWithAttachments(context.Background(), "read this document", []attachment.Metadata{metadata})
	if err != nil || reply != "document read" {
		t.Fatalf("document reply = %q, error = %v", reply, err)
	}
	if len(requests) != 2 {
		t.Fatalf("backend requests = %d, want 2", len(requests))
	}
}

func testOpenAIImageBytes(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, color.RGBA{R: 0x22, G: 0x66, B: 0xaa, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func readDocumentCommand(pathName string) string {
	if runtime.GOOS == "windows" {
		return "Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(pathName, "'", "''") + "'"
	}
	return "cat -- '" + strings.ReplaceAll(pathName, "'", "'\\''") + "'"
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCodexProviderRequiresStartupAuthenticationBeforeBackendRequest(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "missing-auth.json")}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL + "/responses"
	_, err := provider.Reply(context.Background(), "gpt-test", "hello")
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "restart GoAster") {
		t.Fatalf("authentication error = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("backend requests = %d, want 0", got)
	}
}

func TestCodexChatSendsExitTextToProviderAndRequiresAuth(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "missing-auth.json")}
	provider := NewCodexProvider(store)
	var output bytes.Buffer
	config := Config{Model: DefaultModel}
	err := Run(context.Background(), strings.NewReader("/exit\n"), &output, NewSession(config, provider))
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("chat error = %v, want missing authentication", err)
	}
	if !strings.Contains(output.String(), "Type a message, then press Enter.") {
		t.Fatalf("chat prompt missing: %q", output.String())
	}
}

func testJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
