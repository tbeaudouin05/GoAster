package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"goaster/internal/diagnostics"
)

func captureTelegramDiagnostics(t *testing.T, fn func()) string {
	t.Helper()
	var output bytes.Buffer
	restore := diagnostics.SetOutput(&output)
	defer restore()
	fn()
	return output.String()
}

func telegramDiagnosticResponse(status int, body string) *http.Response {
	response := response(status, body)
	response.Status = fmt.Sprintf("%d %s", status, http.StatusText(status))
	response.Header = make(http.Header)
	response.Header.Set("X-Request-ID", "req-telegram-123")
	return response
}

func TestRuntimeDiagnosticsCaptureTelegramStatusAndMethodFailures(t *testing.T) {
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		return telegramDiagnosticResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"private Telegram response"}`), nil
	})
	a := &adapter{token: testToken, apiBase: "https://api.telegram.org", client: client}
	log := captureTelegramDiagnostics(t, func() {
		_, _ = a.sendMessage(context.Background(), 42, "hello")
		_ = a.editMessage(context.Background(), 42, 9, "hello")
		_ = sendDocument(context.Background(), client, a.apiBase, a.token, 42, "notes.txt", []byte("local content"))
		_, _ = a.getUpdates(context.Background(), 0, 0)
		a.registerTelegramCommands(context.Background())
	})
	for _, method := range []string{"sendMessage", "editMessageText", "sendDocument", "getUpdates", "setMyCommands"} {
		if !strings.Contains(log, `method="`+method+`"`) {
			t.Fatalf("diagnostic = %q, missing method %q", log, method)
		}
	}
	for _, want := range []string{`status="400"`, `backend_code="400"`, `request_id="req-telegram-123"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
	for _, unwanted := range []string{testToken, "private Telegram response", "local content"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
}

func TestRuntimeDiagnosticsCaptureTelegramTransportCauseAndRedaction(t *testing.T) {
	transportCause := errors.New(`Post "https://api.telegram.org/bot123456:ABCSECRET/getUpdates": dial tcp telegram.example:443: connection refused token=TELEGRAM_TOKEN`)
	a := &adapter{token: testToken, apiBase: "https://api.telegram.org", client: doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportCause
	})}
	var requestErr error
	log := captureTelegramDiagnostics(t, func() {
		requestErr = a.call(context.Background(), "getUpdates", map[string]string{"query": "private prompt"}, nil)
	})
	if requestErr == nil || !errors.Is(requestErr, transportCause) {
		t.Fatalf("Telegram error = %v, want transport cause", requestErr)
	}
	if !strings.Contains(log, "ERROR operation=telegram.api") || !strings.Contains(log, "*errors.errorString") {
		t.Fatalf("diagnostic = %q, missing transport cause", log)
	}
	for _, unwanted := range []string{testToken, "123456:ABCSECRET", "TELEGRAM_TOKEN", "private prompt"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
}

func TestRuntimeDiagnosticsCaptureTelegramMalformedStream(t *testing.T) {
	a := &adapter{token: testToken, apiBase: "https://api.telegram.org", agent: doerFunc(func(*http.Request) (*http.Response, error) {
		return telegramDiagnosticResponse(http.StatusOK, `{"type":"final","reply":"private response"}`+"\n"+`{"type":"final"`), nil
	})}
	var requestErr error
	log := captureTelegramDiagnostics(t, func() {
		_, requestErr = askAgentWithUser(context.Background(), "hello", 42, 7, nil, nil, a.agent, "control-secret")
	})
	if requestErr == nil {
		t.Fatal("malformed agent stream unexpectedly succeeded")
	}
	if !strings.Contains(log, "ERROR operation=telegram.agent.chat") || !strings.Contains(log, "*json.SyntaxError") {
		t.Fatalf("diagnostic = %q, missing malformed-stream cause", log)
	}
	if strings.Contains(log, "private response") {
		t.Fatalf("diagnostic exposed agent response content: %q", log)
	}
}

func TestRuntimeDiagnosticsCaptureTelegramScannerCause(t *testing.T) {
	scannerCause := errors.New("scanner transport cause")
	response := telegramDiagnosticResponse(http.StatusOK, "")
	response.Body = io.NopCloser(io.MultiReader(
		strings.NewReader(`{"type":"tool_call","name":"shell_execute","arguments":"{}"}`+"\n"),
		iotest.ErrReader(scannerCause),
	))
	a := &adapter{token: testToken, apiBase: "https://api.telegram.org", agent: doerFunc(func(*http.Request) (*http.Response, error) {
		return response, nil
	})}
	var requestErr error
	log := captureTelegramDiagnostics(t, func() {
		_, requestErr = askAgentWithUser(context.Background(), "hello", 42, 7, nil, nil, a.agent, "control-secret")
	})
	if requestErr == nil || !errors.Is(requestErr, scannerCause) {
		t.Fatalf("agent error = %v, want scanner cause", requestErr)
	}
	if !strings.Contains(log, "*errors.errorString") {
		t.Fatalf("diagnostic = %q, missing scanner cause", log)
	}
}

func TestRuntimeDiagnosticsKnownTelegramRejection(t *testing.T) {
	a := &adapter{token: testToken, apiBase: "https://api.telegram.org", client: doerFunc(func(*http.Request) (*http.Response, error) {
		return telegramDiagnosticResponse(http.StatusUnauthorized, `{"ok":false,"error_code":401,"description":"Unauthorized"}`), nil
	})}
	log := captureTelegramDiagnostics(t, func() { _ = a.call(context.Background(), "getUpdates", nil, nil) })
	if !strings.Contains(log, `backend_message="Unauthorized"`) || !strings.Contains(log, `backend_code="401"`) {
		t.Fatalf("missing safe original rejection: %q", log)
	}
}
