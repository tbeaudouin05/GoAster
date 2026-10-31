package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func privateMessage(id int64, text string) *message {
	return textMessage(id, id, "private", text)
}

func textMessage(userID, chatID int64, chatType, text string) *message {
	m := &message{Text: text}
	m.Chat.ID, m.Chat.Type = chatID, chatType
	m.From = &struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}{ID: userID}
	return m
}

func TestPrivatePairingAndAuthorization(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	var sent []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	var forwarded []struct {
		Message string `json:"message"`
		ChatID  int64  `json:"chat_id"`
	}
	a := &adapter{token: testToken, pairing: p, apiBase: "https://example.invalid", client: doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			return response(200, `{"ok":true,"result":true}`), nil
		}
		var payload struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") || json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Fatal("unexpected Telegram request")
		}
		sent = append(sent, payload)
		return response(200, `{"ok":true,"result":{}}`), nil
	}), agent: doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			return response(200, "ok\n"), nil
		}
		var request struct {
			Message string `json:"message"`
			ChatID  int64  `json:"chat_id"`
		}
		if r.URL.String() != "http://127.0.0.1:8765/chat" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatal("unexpected agent request")
		}
		forwarded = append(forwarded, request)
		return response(200, `{"reply":"hello back"}`), nil
	})}

	group := privateMessage(22, "/start "+testCode)
	group.Chat.Type = "group"
	spoof := privateMessage(23, "/start "+testCode)
	spoof.Chat.ID = 24
	bot := privateMessage(25, "/start "+testCode)
	bot.From.IsBot = true
	senderChat := privateMessage(26, "/start "+testCode)
	senderChat.SenderChat = json.RawMessage(`{"id":26}`)
	for _, m := range []*message{nil, group, spoof, bot, senderChat, privateMessage(28, "hello"), privateMessage(29, "/start wrong")} {
		a.handle(context.Background(), m)
	}
	if len(forwarded) != 0 || len(sent) != 0 || p.userID != 0 {
		t.Fatal("unpaired or non-private users gained access")
	}
	a.handle(context.Background(), privateMessage(20, "/start"))
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "pairing code") || p.userID != 0 {
		t.Fatal("/start did not explain pairing without authorizing anyone")
	}
	a.handle(context.Background(), privateMessage(42, "/start "+testCode))
	if p.userID != 42 || len(sent) != 2 || !strings.Contains(sent[1].Text, "Paired") {
		t.Fatal("valid private pairing failed")
	}
	for _, m := range []*message{privateMessage(99, "hello"), privateMessage(99, "/start "+testCode), group, privateMessage(42, "/clear"), privateMessage(42, "")} {
		a.handle(context.Background(), m)
	}
	if len(forwarded) != 0 || len(sent) != 2 {
		t.Fatal("unauthorized, unsupported or non-text message reached the agent")
	}
	a.handle(context.Background(), privateMessage(42, "my token is "+testToken))
	if len(forwarded) != 0 || len(sent) != 3 || strings.Contains(sent[2].Text, testToken) {
		t.Fatal("a pasted bot token reached the agent or was echoed")
	}
	a.handle(context.Background(), privateMessage(42, "Hello"))
	if len(forwarded) != 1 || forwarded[0].Message != "Hello" || forwarded[0].ChatID != 42 || len(sent) != 4 || sent[3].ChatID != 42 || sent[3].Text != "hello back" {
		t.Fatal("paired user's private message was not answered")
	}
	reloaded, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	a.pairing = reloaded
	a.handle(context.Background(), privateMessage(99, "another user"))
	a.handle(context.Background(), privateMessage(42, "after restart"))
	if len(forwarded) != 2 || forwarded[1].Message != "after restart" || forwarded[1].ChatID != 42 {
		t.Fatal("authorization did not persist across restart")
	}
}

func TestPairedGroupMessagesForwardChatIDAndReplyToOriginatingChat(t *testing.T) {
	var sent []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	var forwarded []struct {
		Message string `json:"message"`
		ChatID  int64  `json:"chat_id"`
	}
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				return response(200, `{"ok":true,"result":true}`), nil
			}
			var payload struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") || json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Fatal("unexpected Telegram request")
			}
			sent = append(sent, payload)
			return response(200, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message string `json:"message"`
				ChatID  int64  `json:"chat_id"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Fatal("invalid agent request")
			}
			forwarded = append(forwarded, request)
			return response(200, `{"reply":"group reply"}`), nil
		}),
	}

	a.handle(context.Background(), textMessage(42, -100123, "group", "group hello"))
	a.handle(context.Background(), textMessage(42, -100456, "supergroup", "supergroup hello"))
	a.handle(context.Background(), textMessage(99, -100123, "group", "unauthorized"))

	if len(forwarded) != 2 || forwarded[0].ChatID != -100123 || forwarded[1].ChatID != -100456 {
		t.Fatalf("forwarded group requests = %#v", forwarded)
	}
	if len(sent) != 2 || sent[0].ChatID != -100123 || sent[1].ChatID != -100456 {
		t.Fatalf("group replies were not sent to their originating chats: %#v", sent)
	}
}

func TestPollingReadinessOffsetAndRedactedErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls []struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}
	ready := false
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", agent: doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != "http://127.0.0.1:8765/healthz" {
			t.Fatal("unexpected agent health request")
		}
		return response(200, "ok\n"), nil
	}), client: doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			return response(200, `{"ok":true,"result":{}}`), nil
		}
		var request struct {
			Offset         int64    `json:"offset"`
			Timeout        int      `json:"timeout"`
			AllowedUpdates []string `json:"allowed_updates"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatal("invalid polling request")
		}
		polls = append(polls, request)
		if len(polls) == 1 {
			return response(200, `{"ok":true,"result":[{"update_id":123,"message":{"text":"/start","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
		}
		cancel()
		return nil, errors.New("failure for " + r.URL.String())
	})}
	if err := a.run(ctx, func() error { ready = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ready || len(polls) != 2 || polls[0].Timeout != 0 || polls[1].Timeout != 30 || polls[1].Offset != 124 || len(polls[1].AllowedUpdates) != 1 || polls[1].AllowedUpdates[0] != "message" {
		t.Fatal("polling readiness, offset or update filter is incorrect")
	}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network error contains %s", r.URL)
	})
	ready = false
	err := a.run(context.Background(), func() error { ready = true; return nil })
	if err == nil || ready || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "example.invalid") {
		t.Fatal("startup error leaked the URL or incorrectly reported readiness")
	}
	a.client = doerFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"ok":true,"result":null}`), nil
	})
	if err := a.run(context.Background(), func() error { ready = true; return nil }); err == nil || ready {
		t.Fatal("invalid polling response was accepted as ready")
	}
}

func TestPollingDoesNotReportReadyWithoutAgentAuthentication(t *testing.T) {
	var healthRequests, polls int
	agentStatus := http.StatusServiceUnavailable
	a := &adapter{agent: doerFunc(func(r *http.Request) (*http.Response, error) {
		healthRequests++
		if agentStatus == http.StatusOK {
			return response(agentStatus, "ok\n"), nil
		}
		return response(agentStatus, "private credential detail"), nil
	}), client: doerFunc(func(*http.Request) (*http.Response, error) {
		polls++
		return response(200, `{"ok":true,"result":[]}`), nil
	})}
	ready := false
	err := a.run(context.Background(), func() error { ready = true; return nil })
	if err == nil || ready || polls != 0 || strings.Contains(err.Error(), "private credential detail") {
		t.Fatalf("unavailable agent: error=%v ready=%v polls=%d", err, ready, polls)
	}

	// The agent can become unavailable while the initial Telegram poll runs.
	healthRequests, polls = 0, 0
	agentStatus = http.StatusOK
	a.client = doerFunc(func(*http.Request) (*http.Response, error) {
		polls++
		agentStatus = http.StatusServiceUnavailable
		return response(200, `{"ok":true,"result":[]}`), nil
	})
	err = a.run(context.Background(), func() error { ready = true; return nil })
	if err == nil || ready || polls != 1 || healthRequests != 2 {
		t.Fatalf("agent became unavailable: error=%v ready=%v polls=%d checks=%d", err, ready, polls, healthRequests)
	}
}

func TestStartReportsCurrentAgentReadiness(t *testing.T) {
	available := false
	var sent []string
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			if available {
				return response(200, "ok\n"), nil
			}
			return response(503, "unavailable"), nil
		}),
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(200, `{"ok":true,"result":{}}`), nil
		}),
	}
	a.handle(context.Background(), privateMessage(42, "/start"))
	available = true
	a.handle(context.Background(), privateMessage(42, "/start"))
	if len(sent) != 2 || !strings.Contains(sent[0], "not ready") || !strings.Contains(sent[1], "ready for text messages") {
		t.Fatalf("readiness messages = %q", sent)
	}
}

func TestPairingDoesNotAnnounceChatWhenAgentIsUnavailable(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	var message string
	a := &adapter{token: testToken, pairing: p, apiBase: "https://example.invalid",
		agent: doerFunc(func(*http.Request) (*http.Response, error) { return response(503, "unavailable"), nil }),
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			message = payload.Text
			return response(200, `{"ok":true,"result":{}}`), nil
		}),
	}
	a.handle(context.Background(), privateMessage(42, "/start "+testCode))
	if p.userID != 42 || !strings.Contains(message, "Paired") || !strings.Contains(message, "not ready") {
		t.Fatalf("pairing readiness message = %q", message)
	}
}
