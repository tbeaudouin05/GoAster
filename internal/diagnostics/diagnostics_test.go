package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestErrorCapturesCauseChainAndSafeContext(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	transport := errors.New("dial tcp backend.example:443: connection refused")
	Error("provider.http", errors.Join(errors.New("request failed"), transport),
		Field{Key: "model", Value: "gpt-test"},
		Field{Key: "status", Value: "502"},
		Field{Key: "request_id", Value: "req-123"},
	)

	log := output.String()
	for _, want := range []string{"ERROR operation=provider.http", `status="502"`, "*errors.joinError", "*errors.errorString"} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
}

func TestErrorRedactsCredentialsPrivateContentAndTerminalInjection(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Error("telegram.api", errors.Join(
		errors.New(`Post "https://api.telegram.org/bot123456:ABCSECRET/getUpdates?access_token=oauth-secret": private prompt content`),
		errors.New("private tool output\n\x1b[31mforged journal line"),
	), Field{Key: "status", Value: "502\nforged=true"})

	log := output.String()
	for _, unwanted := range []string{"123456:ABCSECRET", "oauth-secret", "private prompt content", "private tool output", "\x1b[31m", "forged journal line"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
	if strings.Count(log, "\n") != 1 {
		t.Fatalf("diagnostic = %q, want one bounded safe record", log)
	}
}

func TestErrorSkipsExpectedCancellationAndEOF(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Error("request", context.Canceled)
	Error("stream", fmt.Errorf("read stream: %w", io.EOF))

	if output.Len() != 0 {
		t.Fatalf("diagnostic = %q, want expected cancellation/EOF to remain quiet", output.String())
	}
}

func TestUntrustedMetadataAndErrorMessagesAreNeverCopied(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()
	Error("provider.http", fmt.Errorf("transport: %w", errors.New(`{"token":"quoted-secret"} Bearer first Bearer second https://host/private/path?query=private-value`)),
		Field{Key: "request_id", Value: "private-request-id"}, Field{Key: "model", Value: "private-model"}, Field{Key: "status", Value: "502 injected-private"}, Field{Key: "backend_code", Value: "private-code"})
	log := output.String()
	for _, secret := range []string{"quoted-secret", "first", "second", "/private/path", "private-value", "private-request-id", "private-model", "private-code", "injected-private"} {
		if strings.Contains(log, secret) {
			t.Fatalf("diagnostic leaked %q: %q", secret, log)
		}
	}
	if !strings.Contains(log, `status="502"`) {
		t.Fatalf("missing safe status: %q", log)
	}
}

func TestRemoteMetadataAllowlist(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()
	Error("provider.http", errors.New("unknown private failure"),
		Field{Key: "backend_type", Value: "invalid_request_error"}, Field{Key: "backend_code", Value: "model_not_found"},
		Field{Key: "request_id", Value: "req_abc12345"}, Field{Key: "backend_message", Value: "Unauthorized"},
		Field{Key: "backend_code", Value: "sk-private"}, Field{Key: "request_id", Value: "req-sk-private"},
		Field{Key: "request_id", Value: "req-abc\nforged"}, Field{Key: "backend_message", Value: "Unauthorized private prompt"})
	log := output.String()
	for _, want := range []string{`backend_type="invalid_request_error"`, `backend_code="model_not_found"`, `request_id="req_abc12345"`, `backend_message="Unauthorized"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %q", want, log)
		}
	}
	for _, secret := range []string{"private", "forged", "prompt"} {
		if strings.Contains(log, secret) {
			t.Fatalf("leaked %q: %q", secret, log)
		}
	}
	if strings.Count(log, "\n") != 1 {
		t.Fatalf("injected log: %q", log)
	}
}
