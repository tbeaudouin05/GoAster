package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"goaster/internal/diagnostics"
)

func TestAgentRuntimeDiagnosticsCaptureAuthenticationAndProviderCauses(t *testing.T) {
	var output bytes.Buffer
	restore := diagnostics.SetOutput(&output)
	defer restore()

	authErr := errors.New("authentication transport failed token=AUTH_TOKEN\nprivate auth detail")
	result := postChat(t, handler(fakeProvider{}, func(context.Context) error { return authErr }), 42, "hello")
	if result.Code != 503 || !strings.Contains(result.Body.String(), "check remote authentication") {
		t.Fatalf("authentication response = %d %q", result.Code, result.Body.String())
	}
	authLog := output.String()
	if !strings.Contains(authLog, "ERROR operation=agent.chat.authentication") || strings.Contains(authLog, "AUTH_TOKEN") || strings.Contains(authLog, "private auth detail") {
		t.Fatalf("authentication diagnostic = %q", authLog)
	}

	output.Reset()
	providerErr := errors.New("dial tcp provider.example:443: connection refused token=PROVIDER_TOKEN")
	result = postChat(t, handler(fakeProvider{err: providerErr}, readyAuth), 42, "hello")
	if result.Code != 503 || !strings.Contains(result.Body.String(), "agent request failed") {
		t.Fatalf("provider response = %d %q", result.Code, result.Body.String())
	}
	providerLog := output.String()
	if !strings.Contains(providerLog, "ERROR operation=agent.chat") || !strings.Contains(providerLog, "*errors.errorString") || strings.Contains(providerLog, "PROVIDER_TOKEN") {
		t.Fatalf("provider diagnostic = %q", providerLog)
	}
}

func TestNormalServerStopDoesNotHideJoinedFailure(t *testing.T) {
	for _, err := range []error{http.ErrServerClosed, net.ErrClosed, io.EOF, context.Canceled, fmt.Errorf("closed: %w", net.ErrClosed)} {
		if !normalServerStop(err) {
			t.Fatalf("expected normal shutdown for %v", err)
		}
	}
	if normalServerStop(errors.Join(net.ErrClosed, errors.New("unexpected failure"))) {
		t.Fatal("joined failure was treated as normal shutdown")
	}
}
