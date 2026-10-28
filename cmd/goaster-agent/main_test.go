package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeProvider struct{ err error }

func (p fakeProvider) Reply(_ context.Context, _ string, message string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return "reply to " + message, nil
}

func TestAgentLoopbackHandler(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, host, contentType, body string
		provider                                    fakeProvider
		status                                      int
		want                                        string
	}{
		{name: "health", method: "GET", path: "/healthz", host: "127.0.0.1:8765", status: 200, want: "ok"},
		{name: "chat", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello"}`, status: 200, want: `"reply":"reply to hello"`},
		{name: "reject host", method: "GET", path: "/healthz", host: "attacker.invalid", status: 403},
		{name: "reject content type", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "text/plain", body: `{"message":"hello"}`, status: 415},
		{name: "reject unknown fields", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello","extra":true}`, status: 400},
		{name: "credential error redacted", method: "POST", path: "/chat", host: "127.0.0.1:8765", contentType: "application/json", body: `{"message":"hello"}`, provider: fakeProvider{err: errors.New("fake-secret")}, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			handler(tc.provider).ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("status %d, body %q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "fake-secret") {
				t.Error("provider error exposed a fake secret")
			}
		})
	}
}

var _ http.Handler = handler(fakeProvider{})
