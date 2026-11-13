package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/diagnostics"
)

type errorLoggingRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper errorLoggingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func captureChatDiagnostics(t *testing.T, fn func()) string {
	t.Helper()
	var output bytes.Buffer
	restore := diagnostics.SetOutput(&output)
	defer restore()
	fn()
	return output.String()
}

func runtimeTestProvider(t *testing.T, client *http.Client, endpoint string) *CodexProvider {
	t.Helper()
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "access-token-should-never-be-logged", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(store)
	provider.backendURL = endpoint
	provider.httpClient = client
	return provider
}

func TestRuntimeDiagnosticsCaptureAuthenticationCause(t *testing.T) {
	provider := NewCodexProvider(CredentialStore{Path: filepath.Join(t.TempDir(), "missing-auth.json")})
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), "gpt-test", "private prompt content must not enter diagnostics")
	})
	if requestErr == nil || !errors.Is(requestErr, ErrLoginRequired) {
		t.Fatalf("authentication error = %v, want ErrLoginRequired", requestErr)
	}
	for _, want := range []string{"ERROR operation=chat.provider", "chat.authenticationError", "credentials were not found"} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
	for _, unwanted := range []string{"private prompt content", "access-token-should-never-be-logged"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
}

func TestRuntimeDiagnosticsCaptureHTTPStatusAndSafeBackendDetails(t *testing.T) {
	const body = `{"error":{"type":"provider_error","code":"gateway_rejected","message":"private provider response"}}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Request-ID", "req-safe-123")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), DefaultModel, "hello")
	})
	if requestErr == nil {
		t.Fatal("provider request unexpectedly succeeded")
	}
	for _, want := range []string{
		"ERROR operation=chat.provider",
		`phase="pre-stream"`,
		`status="502"`, `backend_type="provider_error"`, `backend_code="gateway_rejected"`, `request_id="req-safe-123"`, `configured_model="` + DefaultModel + `"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
	for _, unwanted := range []string{"private provider response", "access-token-should-never-be-logged", "Authorization"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
}

func TestRuntimeDiagnosticsCaptureTransportCause(t *testing.T) {
	transportCause := errors.New("dial tcp provider.example:443: connection refused")
	provider := runtimeTestProvider(t, &http.Client{Transport: errorLoggingRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, transportCause
	})}, "https://provider.example/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), "gpt-test", "hello")
	})
	if requestErr == nil || !errors.Is(requestErr, transportCause) {
		t.Fatalf("provider error = %v, want transport cause", requestErr)
	}
	if !strings.Contains(log, "ERROR operation=chat.provider") || !strings.Contains(log, "*url.Error") {
		t.Fatalf("diagnostic = %q, missing transport cause", log)
	}
}

func TestRuntimeDiagnosticsCaptureMalformedSSEWithoutBody(t *testing.T) {
	const privateStreamBody = "data: {not-valid-private-response}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(privateStreamBody))
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), "gpt-test", "hello")
	})
	if requestErr == nil {
		t.Fatal("malformed stream unexpectedly succeeded")
	}
	for _, want := range []string{"ERROR operation=chat.provider", `phase="stream"`, `status="200"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
	if strings.Contains(log, privateStreamBody) || strings.Contains(log, "private-response") {
		t.Fatalf("diagnostic exposed malformed stream body: %q", log)
	}
}

func TestSessionDiagnosticOwnsProviderFailureBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(writer, `{"error":{"type":"backend_error","code":"single_boundary"}}`)
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		requestContext := WithRuntimeFailureOperation(context.Background(), "agent.chat")
		_, requestErr = NewSession(Config{Model: "gpt-test"}, provider).Reply(requestContext, "hello")
	})
	if requestErr == nil {
		t.Fatal("session request unexpectedly succeeded")
	}
	if strings.Count(log, "ERROR operation=") != 1 || !strings.Contains(log, "ERROR operation=agent.chat") || strings.Contains(log, "ERROR operation=chat.provider") {
		t.Fatalf("diagnostics = %q, want one outer boundary record", log)
	}
}

func TestRuntimeDiagnosticsTypedDNSAndCredentialJSON(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		cause := &net.DNSError{Err: "secret arbitrary DNS detail", Name: "private-host", Server: "private-resolver", IsNotFound: true}
		provider := runtimeTestProvider(t, &http.Client{Transport: errorLoggingRoundTripper(func(*http.Request) (*http.Response, error) { return nil, cause })}, "https://provider.example/responses")
		log := captureChatDiagnostics(t, func() {
			_, err := provider.Reply(context.Background(), DefaultModel, "private prompt")
			if !errors.Is(err, cause) {
				t.Fatal("DNS cause was lost")
			}
		})
		if !strings.Contains(log, "DNS name not found") {
			t.Fatalf("missing DNS reason: %q", log)
		}
		for _, secret := range []string{"secret arbitrary", "private-host", "private-resolver", "private prompt"} {
			if strings.Contains(log, secret) {
				t.Fatalf("leaked %q", secret)
			}
		}
	})
	t.Run("credential JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte(`{"access_token":"private-token",`), 0600); err != nil {
			t.Fatal(err)
		}
		provider := NewCodexProvider(CredentialStore{Path: path})
		log := captureChatDiagnostics(t, func() {
			_, err := provider.Reply(context.Background(), DefaultModel, "hello")
			var syntax *json.SyntaxError
			if !errors.Is(err, ErrLoginRequired) || !errors.As(err, &syntax) {
				t.Fatal("JSON/authentication cause was lost")
			}
		})
		if !strings.Contains(log, "invalid JSON at byte") || strings.Contains(log, "private-token") {
			t.Fatalf("unsafe or missing JSON reason: %q", log)
		}
	})
}

func TestRuntimeDiagnosticsSSEKnownRejection(t *testing.T) {
	provider := runtimeTestProvider(t, &http.Client{Transport: errorLoggingRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"req-sse-123"}}, Body: io.NopCloser(strings.NewReader("data: " + `{"type":"response.failed","response":{"error":{"type":"server_error","code":"rate_limit_exceeded","message":"private echoed request"}}}` + "\n\n"))}, nil
	})}, "https://provider.example/responses")
	log := captureChatDiagnostics(t, func() { _, _ = NewSession(Config{Model: DefaultModel}, provider).Reply(context.Background(), "hello") })
	for _, want := range []string{`backend_type="server_error"`, `backend_code="rate_limit_exceeded"`, `request_id="req-sse-123"`, "backend returned an error"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %q", want, log)
		}
	}
	if strings.Count(log, "ERROR operation=") != 1 || strings.Contains(log, "private echoed") {
		t.Fatalf("unsafe or duplicate log: %q", log)
	}
}
