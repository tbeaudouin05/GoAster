package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func typingTestHandler(t *testing.T, events chan<- string, actionStatus int) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Telegram method = %s, want POST", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
			var payload struct {
				ChatID int64  `json:"chat_id"`
				Action string `json:"action"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode sendChatAction payload: %v", err)
				return
			}
			if payload.ChatID != 42 || payload.Action != "typing" {
				t.Errorf("sendChatAction payload = %+v", payload)
			}
			events <- "typing"
			if actionStatus != http.StatusOK {
				w.WriteHeader(actionStatus)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			events <- "reply"
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			t.Errorf("unexpected Telegram request path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func nextTypingTestEvent(t *testing.T, events <-chan string, timeout time.Duration) string {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event := <-events:
		return event
	case <-timer.C:
		t.Fatalf("timed out waiting for Telegram test event")
		return ""
	}
}

func TestAcceptedTurnSendsTypingImmediatelyBeforeAgentWork(t *testing.T) {
	events := make(chan string, 8)
	server := httptest.NewServer(typingTestHandler(t, events, http.StatusOK))
	defer server.Close()
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: server.URL,
		client:  server.Client(),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			events <- "agent"
			return response(http.StatusOK, `{"type":"final","reply":"hello"}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "hello"))
	if event := nextTypingTestEvent(t, events, time.Second); event != "typing" {
		t.Fatalf("first accepted-turn event = %q, want typing", event)
	}
	if event := nextTypingTestEvent(t, events, time.Second); event != "agent" {
		t.Fatalf("second accepted-turn event = %q, want agent", event)
	}
}

func TestTypingFailureDoesNotFailAcceptedTurn(t *testing.T) {
	events := make(chan string, 8)
	server := httptest.NewServer(typingTestHandler(t, events, http.StatusBadGateway))
	defer server.Close()
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: server.URL,
		client:  server.Client(),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			events <- "agent"
			return response(http.StatusOK, `{"type":"final","reply":"hello"}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "hello"))
	for _, want := range []string{"typing", "agent", "reply"} {
		if event := nextTypingTestEvent(t, events, time.Second); event != want {
			t.Fatalf("accepted-turn event = %q, want %q", event, want)
		}
	}
}

func TestTypingCancellationDuringInFlightRequestStopsHandlePromptly(t *testing.T) {
	typingStarted := make(chan struct{})
	typingCanceled := make(chan struct{})
	extraTyping := make(chan struct{}, 1)
	var typingCalls atomic.Int32
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				if typingCalls.Add(1) == 1 {
					close(typingStarted)
					<-r.Context().Done()
					close(typingCanceled)
					return nil, r.Context().Err()
				}
				select {
				case extraTyping <- struct{}{}:
				default:
				}
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			return response(http.StatusOK, `{"type":"final","reply":"hello"}`+"\n"), nil
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		a.handle(ctx, privateMessage(42, "hello"))
		close(done)
	}()
	select {
	case <-typingStarted:
	case <-time.After(time.Second):
		t.Fatal("typing request did not start")
	}
	cancel()
	select {
	case <-typingCanceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight typing request was not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handle did not return promptly after cancellation")
	}
	if typingCalls.Load() != 1 {
		t.Fatalf("typing calls after cancellation = %d, want 1", typingCalls.Load())
	}

	timer := time.NewTimer(telegramTypingRefreshInterval + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case <-extraTyping:
		t.Fatal("typing refresher leaked after cancellation")
	case <-timer.C:
	}
}

func TestTypingRefreshesWhileAgentIsBlockedAndStopsAfterReturn(t *testing.T) {
	events := make(chan string, 8)
	server := httptest.NewServer(typingTestHandler(t, events, http.StatusOK))
	defer server.Close()
	agentStarted := make(chan struct{})
	releaseAgent := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseAgent) })
	}
	defer release()
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: server.URL,
		client:  server.Client(),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			close(agentStarted)
			<-releaseAgent
			return response(http.StatusOK, `{"type":"final","reply":"hello"}`+"\n"), nil
		}),
	}

	done := make(chan struct{})
	go func() {
		a.handle(context.Background(), privateMessage(42, "hello"))
		close(done)
	}()
	if event := nextTypingTestEvent(t, events, time.Second); event != "typing" {
		t.Fatalf("first blocked-turn event = %q, want typing", event)
	}
	select {
	case <-agentStarted:
	case <-time.After(time.Second):
		t.Fatal("agent did not block")
	}
	if event := nextTypingTestEvent(t, events, telegramTypingRefreshInterval+2*time.Second); event != "typing" {
		t.Fatalf("refresh event = %q, want typing", event)
	}

	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accepted turn did not return")
	}
	replySeen := false
	for {
		select {
		case event := <-events:
			if event == "reply" {
				replySeen = true
			}
			if event == "typing" && replySeen {
				t.Fatal("typing refresh arrived after the reply")
			}
		default:
			if !replySeen {
				t.Fatal("accepted turn returned without sending a reply")
			}
			goto drained
		}
	}

drained:
	timer := time.NewTimer(telegramTypingRefreshInterval + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case event := <-events:
		t.Fatalf("event after accepted turn returned = %q", event)
	case <-timer.C:
	}
}

func TestRejectedAndInvalidMessagesDoNotStartTyping(t *testing.T) {
	events := make(chan string, 16)
	server := httptest.NewServer(typingTestHandler(t, events, http.StatusOK))
	defer server.Close()
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: server.URL,
		client:  server.Client(),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("rejected message reached the agent")
			return nil, nil
		}),
	}

	bot := privateMessage(42, "hello")
	bot.From.IsBot = true
	missingFrom := privateMessage(42, "hello")
	missingFrom.From = nil
	zeroUser := privateMessage(42, "hello")
	zeroUser.From.ID = 0
	zeroChat := privateMessage(42, "hello")
	zeroChat.Chat.ID = 0
	senderChat := privateMessage(42, "hello")
	senderChat.SenderChat = json.RawMessage(`{"id":42}`)
	album := privateMessage(42, "hello")
	album.MediaGroupID = "album-1"
	mismatchedPrivateChat := privateMessage(42, "hello")
	mismatchedPrivateChat.Chat.ID = 43
	invalidChat := textMessage(42, 42, "channel", "hello")
	for _, message := range []*message{
		nil,
		bot,
		missingFrom,
		zeroUser,
		zeroChat,
		senderChat,
		privateMessage(99, "hello"),
		mismatchedPrivateChat,
		invalidChat,
		album,
		privateMessage(42, "/clear"),
		privateMessage(42, ""),
		privateMessage(42, "contains "+testToken),
	} {
		a.handle(context.Background(), message)
	}

	for {
		select {
		case event := <-events:
			if event == "typing" {
				t.Fatal("rejected or invalid message started typing")
			}
		default:
			return
		}
	}
}
