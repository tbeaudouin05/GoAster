package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/chat"
)

type fakeProvider struct{ err error }

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

func TestAgentSessionsReuseHistoryAndIsolateChats(t *testing.T) {
	provider := &historyProvider{}
	h := handler(provider)
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
		base := handler(provider)
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
		h := handler(provider)
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

var _ http.Handler = handler(fakeProvider{})
