package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

	if err := Run(context.Background(), strings.NewReader("/exit\n"), &output, NewSession(Config{
		PrimaryModel:  DefaultPrimaryModel,
		FallbackModel: DefaultFallbackModel,
	}, provider)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Goodbye.") {
		t.Fatalf("chat output = %q", output.String())
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

func TestCodexProviderUsesSubscriptionBackendAndStreamsText(t *testing.T) {
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
		if request.Model != "gpt-test" || request.Instructions == "" || !request.Stream || request.Store || len(request.Input) != 1 || request.Input[0].Type != "message" || request.Input[0].Content[0].Text != "hello" {
			t.Errorf("Codex request = %+v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" from Codex\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL + "/responses"
	provider.httpClient = server.Client()
	reply, err := provider.Reply(context.Background(), "gpt-test", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hello from Codex" {
		t.Fatalf("reply = %q", reply)
	}
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

func TestCodexChatKeepsExitCommandBeforeLogin(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "missing-auth.json")}
	provider := NewCodexProvider(store)
	var output bytes.Buffer
	config := Config{PrimaryModel: DefaultPrimaryModel, FallbackModel: DefaultFallbackModel}
	if err := Run(context.Background(), strings.NewReader("/exit\n"), &output, NewSession(config, provider)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Goodbye.") {
		t.Fatalf("exit output = %q", output.String())
	}
}

func testJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
