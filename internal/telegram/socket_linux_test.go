//go:build linux

package telegram

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"goaster/internal/controltransport"
)

func TestAdapterUsesSocketForReadinessChatAndAuthorization(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the protected socket requires root")
	}
	dir, err := os.MkdirTemp("", "gt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	if len(path) > 107 {
		t.Fatal("temporary path is too long for a Unix socket; set TMPDIR to a shorter private directory")
	}
	t.Setenv(controltransport.SocketEnv, path)
	listener, err := controltransport.Listen()
	if err != nil {
		t.Fatal(err)
	}
	server := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez", "/healthz":
			fmt.Fprintln(w, "ok")
		case "/chat":
			if r.Header.Get(telegramAuthControlHeader) != "test-secret" {
				t.Error("chat request lacked the control secret")
			}
			fmt.Fprintln(w, `{"type":"final","reply":"socket reply"}`)
		case "/auth/github/authorize", "/auth/github/cancel", "/auth/github/reconcile":
			if r.Header.Get(telegramAuthControlHeader) != "test-secret" {
				t.Error("authorization request lacked the control secret")
			}
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	agent, err := newAgentClient()
	if err != nil {
		t.Fatal(err)
	}
	a := adapter{agent: agent, authControlSecret: "test-secret"}
	for _, endpoint := range []string{"/livez", "/healthz"} {
		if !a.agentEndpointAvailable(context.Background(), endpoint) {
			t.Fatalf("%s unavailable over socket", endpoint)
		}
	}
	reply, err := a.askAgent(context.Background(), "hello", 42, nil, nil)
	if err != nil || reply != "socket reply" {
		t.Fatalf("chat reply = %q, %v", reply, err)
	}
	for _, endpoint := range []string{"/auth/github/authorize", "/auth/github/cancel", "/auth/github/reconcile"} {
		if !a.githubAuthRequest(context.Background(), endpoint, 42) {
			t.Fatalf("%s failed over socket", endpoint)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if a.agentEndpointAvailable(context.Background(), "/livez") {
		t.Fatal("adapter reported a stopped socket as available")
	}
	if _, err := a.askAgent(context.Background(), "hello", 42, nil, nil); err == nil {
		t.Fatal("chat succeeded after the socket stopped")
	}
}
