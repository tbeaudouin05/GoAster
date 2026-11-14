package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
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
		ChatID    int64  `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
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
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
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
		return response(200, `{"type":"final","reply":"hello back"}`+"\n"), nil
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
	a.handle(context.Background(), privateMessage(20, telegramHelpCommand+"@other_bot"))
	a.handle(context.Background(), privateMessage(20, telegramHelpCommand))
	a.handle(context.Background(), textMessage(20, -100123, "group", telegramHelpCommand))
	if len(sent) != 1 || sent[0].ChatID != 20 || sent[0].Text != telegramHelpText ||
		!strings.Contains(sent[0].Text, "/stop") || !strings.Contains(sent[0].Text, "/get-env") || !strings.Contains(sent[0].Text, "ACCEPT") ||
		strings.Contains(sent[0].Text, "paired account") || strings.Contains(sent[0].Text, "private chat") || strings.Contains(sent[0].Text, "Available Telegram commands") {
		t.Fatal("help was unavailable in a private chat before pairing or visible in an unpaired group")
	}
	a.handle(context.Background(), privateMessage(20, "/start"))
	if len(sent) != 2 || !strings.Contains(sent[1].Text, "pairing code") || sent[1].ParseMode != "HTML" || p.userID != 0 {
		t.Fatal("/start did not explain pairing without authorizing anyone")
	}
	a.handle(context.Background(), privateMessage(42, "/pair "+testCode))
	if p.userID != 42 || len(sent) != 3 || !strings.Contains(sent[2].Text, "Paired") || sent[2].ParseMode != "HTML" {
		t.Fatal("valid private pairing failed")
	}
	a.handle(context.Background(), privateMessage(99, telegramHelpCommand))
	a.handle(context.Background(), privateMessage(42, telegramHelpCommand+"@goaster_bot"))
	a.handle(context.Background(), textMessage(42, -100123, "group", telegramHelpCommand+"@other_bot"))
	a.handle(context.Background(), privateMessage(42, telegramHelpCommand+"@"))
	a.handle(context.Background(), privateMessage(42, telegramHelpCommand))
	a.handle(context.Background(), textMessage(42, -100123, "group", telegramHelpCommand))
	if len(sent) != 5 || sent[3].ChatID != 42 || sent[3].Text != telegramHelpText ||
		sent[4].ChatID != -100123 || sent[4].Text != telegramHelpText {
		t.Fatal("bare help failed, or an addressed help or unpaired account received help")
	}
	for _, m := range []*message{privateMessage(99, "hello"), privateMessage(99, "/start "+testCode), group, privateMessage(42, "")} {
		a.handle(context.Background(), m)
	}
	if len(forwarded) != 0 || len(sent) != 5 {
		t.Fatal("unauthorized, unsupported or non-text message reached the agent")
	}
	a.handle(context.Background(), privateMessage(42, "my token is "+testToken))
	if len(forwarded) != 0 || len(sent) != 6 || sent[5].ParseMode != "HTML" || strings.Contains(sent[5].Text, testToken) {
		t.Fatal("a pasted bot token reached the agent or was echoed")
	}
	a.handle(context.Background(), privateMessage(42, "Hello"))
	if len(forwarded) != 1 || forwarded[0].Message != "Hello" || forwarded[0].ChatID != 42 || len(sent) != 7 || sent[6].ChatID != 42 || sent[6].Text != "hello back" || sent[6].ParseMode != "HTML" {
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

func TestStopIsIdleScopedAndAuthorized(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	sent := make([]string, 0, 2)
	a := adapter{
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
	}

	// An unpaired user cannot use /stop or learn whether a turn exists.
	a.handle(context.Background(), privateMessage(42, telegramStopCommand))
	if len(sent) != 0 {
		t.Fatalf("unpaired /stop response = %q", sent)
	}

	a.pairing.userID = 42
	a.handle(context.Background(), privateMessage(42, telegramStopCommand))
	a.handle(context.Background(), privateMessage(99, telegramStopCommand))
	a.handle(context.Background(), privateMessage(42, telegramStopCommand+"@goaster_bot"))
	a.handle(context.Background(), privateMessage(42, telegramStopCommand+"@other_bot"))
	a.handle(context.Background(), privateMessage(42, telegramStopCommand+"@"))
	if !reflect.DeepEqual(sent, []string{telegramNoActiveTurnMessage}) {
		t.Fatalf("idle or unauthorized /stop responses = %q", sent)
	}
}

func TestStopCancelsOnlyTheAuthorizedCurrentThread(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	started := make(chan string, 2)
	canceled := make(chan string, 2)
	release := make(chan string, 1)
	done := make(chan struct{}, 2)
	sent := make(chan string, 4)
	a := adapter{
		pairing: paired,
		token:   testToken,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var payload struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, err
				}
				sent <- payload.Text
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				return nil, fmt.Errorf("unexpected Telegram request %s", r.URL.Path)
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/chat" {
				return nil, fmt.Errorf("unexpected agent request %s", r.URL.Path)
			}
			var request struct {
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			if request.Message == "/clear" {
				return response(http.StatusOK, `{"type":"final","reply":"Conversation history cleared."}`+"\n"), nil
			}
			started <- request.Message
			select {
			case <-r.Context().Done():
				canceled <- request.Message
				return nil, r.Context().Err()
			case message := <-release:
				if message != request.Message {
					return nil, fmt.Errorf("released message %q while handling %q", message, request.Message)
				}
				return response(http.StatusOK, `{"type":"final","reply":"completed"}`+"\n"), nil
			}
		}),
	}

	first := textMessage(42, -100123, "supergroup", "long request in topic one")
	first.MessageThreadID = 11
	second := textMessage(42, -100123, "supergroup", "long request in topic two")
	second.MessageThreadID = 22
	go func() {
		a.handle(context.Background(), first)
		done <- struct{}{}
	}()
	go func() {
		a.handle(context.Background(), second)
		done <- struct{}{}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("agent turns did not start")
		}
	}

	// A different Telegram user cannot stop the paired user's turn.
	unauthorized := textMessage(99, -100123, "supergroup", telegramStopCommand)
	unauthorized.MessageThreadID = 11
	a.handle(context.Background(), unauthorized)
	select {
	case message := <-canceled:
		t.Fatalf("unauthorized /stop canceled %q", message)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case message := <-sent:
		t.Fatalf("unauthorized /stop sent a response: %q", message)
	default:
	}

	stop := textMessage(42, -100123, "supergroup", telegramStopCommand)
	stop.MessageThreadID = 11
	a.handle(context.Background(), stop)
	select {
	case message := <-canceled:
		if message != first.Text {
			t.Fatalf("canceled message = %q, want %q", message, first.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized /stop did not cancel the active request")
	}
	select {
	case message := <-sent:
		if message != telegramStopMessage {
			t.Fatalf("stop response = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized /stop did not acknowledge cancellation")
	}
	select {
	case message := <-canceled:
		t.Fatalf("stopping topic one also canceled %q", message)
	case <-time.After(100 * time.Millisecond):
	}

	a.handle(context.Background(), textMessage(42, -100123, "supergroup", "/clear"))
	select {
	case message := <-canceled:
		if message != second.Text {
			t.Fatalf("clear canceled %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("clear did not cancel the active chat turn")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("agent turn handler did not finish")
		}
	}
}

func TestPairCommandIsPrimaryAndRejectsInvalidPairingMessages(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	a := &adapter{
		token: testToken, pairing: p, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
	}

	group := textMessage(22, -100123, "group", "/pair "+testCode)
	startWithCodeGroup := textMessage(23, -100123, "group", "/start "+testCode)
	spoof := privateMessage(24, "/pair "+testCode)
	spoof.Chat.ID = 25
	bot := privateMessage(26, "/pair "+testCode)
	bot.From.IsBot = true
	senderChat := privateMessage(27, "/start "+testCode)
	senderChat.SenderChat = json.RawMessage(`{"id":27}`)
	a.handle(context.Background(), group)
	a.handle(context.Background(), startWithCodeGroup)
	a.handle(context.Background(), spoof)
	a.handle(context.Background(), bot)
	a.handle(context.Background(), senderChat)
	a.handle(context.Background(), privateMessage(28, "hello"))
	a.handle(context.Background(), privateMessage(29, "/pair wrong"))
	a.handle(context.Background(), privateMessage(30, "/pair "+testCode+" extra"))
	a.handle(context.Background(), privateMessage(31, "/pair\t"+testCode))
	a.handle(context.Background(), privateMessage(32, "/pair@goaster "+testCode))
	a.handle(context.Background(), privateMessage(33, "/start "+testCode))
	if p.userID != 0 || len(sent) != 0 {
		t.Fatalf("invalid or non-private pairing message changed authorization: user=%d sent=%q", p.userID, sent)
	}

	a.handle(context.Background(), privateMessage(20, "/start"))
	a.handle(context.Background(), privateMessage(20, "/pair"))
	if len(sent) != 2 || !strings.Contains(sent[0], "/pair") || !strings.Contains(sent[1], "/pair") ||
		strings.Contains(sent[0], testCode) || strings.Contains(sent[1], testCode) {
		t.Fatalf("pairing instructions were not primary and code-free: %q", sent)
	}

	a.handle(context.Background(), privateMessage(42, "/pair "+testCode))
	if p.userID != 42 || len(sent) != 3 || !strings.Contains(sent[2], "Paired") {
		t.Fatalf("valid /pair did not authorize the private user: user=%d sent=%q", p.userID, sent)
	}
	a.handle(context.Background(), privateMessage(99, "/start "+testCode))
	if p.userID != 42 || len(sent) != 3 {
		t.Fatal("a consumed code or legacy command authorized another user")
	}
	a.handle(context.Background(), privateMessage(42, "/start"))
	if len(sent) != 4 || !strings.Contains(sent[3], "not ready") {
		t.Fatalf("normal /start did not retain paired-user behavior: %q", sent)
	}
}

func TestStartCodeIsIgnoredBeforeAndGivesReadinessAfterPairing(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	var forwarded []string
	a := adapter{
		token: testToken, pairing: p, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
				return response(http.StatusOK, "ok\n"), nil
			}
			if r.Method != http.MethodPost || r.URL.Path != "/chat" {
				t.Fatalf("unexpected agent request %s %s", r.Method, r.URL)
			}
			var request struct {
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			forwarded = append(forwarded, request.Message)
			return response(http.StatusOK, `{"type":"final","reply":"unexpected"}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "/start "+testCode))
	if p.userID != 0 || len(sent) != 0 || len(forwarded) != 0 {
		t.Fatalf("/start CODE was accepted or forwarded before pairing: user=%d sent=%q forwarded=%q", p.userID, sent, forwarded)
	}
	a.handle(context.Background(), privateMessage(42, "/start"))
	if len(sent) != 1 || !strings.Contains(sent[0], "/pair") || strings.Contains(sent[0], testCode) || p.userID != 0 {
		t.Fatalf("bare /start did not retain its code-free welcome: user=%d sent=%q", p.userID, sent)
	}
	a.handle(context.Background(), privateMessage(42, "/pair "+testCode))
	if p.userID != 42 || len(sent) != 2 || len(forwarded) != 0 || strings.Contains(sent[1], testCode) {
		t.Fatalf("/pair CODE did not authorize without exposing the code: user=%d sent=%q forwarded=%q", p.userID, sent, forwarded)
	}
	a.handle(context.Background(), privateMessage(42, "/start "+testCode))
	if p.userID != 42 || len(forwarded) != 0 || len(sent) != 3 || !strings.Contains(sent[2], "ready for text messages") || strings.Contains(sent[2], testCode) {
		t.Fatalf("/start CODE did not give code-free readiness guidance after pairing: user=%d sent=%q forwarded=%q", p.userID, sent, forwarded)
	}
}

func TestPairedPairCommandIsNotForwardedOrEchoed(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	p.userID = 42
	var sent []string
	var forwarded []string
	a := adapter{
		token: testToken, pairing: p, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var payload struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, payload.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
				return response(http.StatusOK, "ok\n"), nil
			}
			if r.Method != http.MethodPost || r.URL.Path != "/chat" {
				t.Fatalf("unexpected agent request %s %s", r.Method, r.URL)
			}
			var request struct {
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			forwarded = append(forwarded, request.Message)
			return response(http.StatusOK, `{"type":"final","reply":"agent response"}`+"\n"), nil
		}),
	}

	for _, text := range []string{"/start", "/pair", "/pair " + testCode} {
		a.handle(context.Background(), privateMessage(42, text))
	}
	if p.userID != 42 {
		t.Fatalf("pairing command changed authorization: user=%d", p.userID)
	}
	if len(forwarded) != 0 {
		t.Fatalf("pairing commands reached the agent: %#v", forwarded)
	}
	const readyMessage = "GoAster is ready for text messages, images, and documents. Use /m to view the model, /m MODEL_OR_ALIAS to select one, and /clear to clear this chat's history."
	if len(sent) != 3 {
		t.Fatalf("expected a safe command response for each command, got %#v", sent)
	}
	for _, reply := range sent {
		if reply != readyMessage || strings.Contains(reply, testCode) {
			t.Fatalf("pairing command response was not safe: %q", reply)
		}
	}
}

func TestClearCommandUsesOnlyItsTelegramChatWithoutProviderReadiness(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	var sent []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	var forwarded []struct {
		Message string `json:"message"`
		ChatID  int64  `json:"chat_id"`
	}
	a := adapter{
		token: testToken, pairing: paired, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost || r.URL.String() != "http://127.0.0.1:8765/chat" {
				t.Fatalf("clear command made an unexpected agent request: %s %s", r.Method, r.URL)
			}
			var request struct {
				Message string `json:"message"`
				ChatID  int64  `json:"chat_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			forwarded = append(forwarded, request)
			reply := "Conversation history cleared."
			if request.Message != "/clear" {
				reply = "Usage: /clear"
			}
			return response(http.StatusOK, `{"type":"final","reply":"`+reply+`"}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "/clear"))
	a.handle(context.Background(), textMessage(42, -100123, "group", "/clear extra"))
	withAttachment := privateMessage(42, "")
	withAttachment.Caption = "/clear"
	withAttachment.Document = &document{FileID: "file"}
	a.handle(context.Background(), withAttachment)

	if len(forwarded) != 2 || forwarded[0].ChatID != 42 || forwarded[0].Message != "/clear" || forwarded[1].ChatID != -100123 || forwarded[1].Message != "/clear extra" {
		t.Fatalf("clear requests = %#v", forwarded)
	}
	if len(sent) != 3 || sent[0].ChatID != 42 || sent[0].Text != "Conversation history cleared." || sent[1].ChatID != -100123 || sent[1].Text != "Usage: /clear" || sent[2].ChatID != 42 || sent[2].Text != "Usage: /clear" {
		t.Fatalf("clear replies = %#v", sent)
	}
}

func TestModelCommandsReachAgentAndReturnSelectionReply(t *testing.T) {
	var sent []string
	var forwarded []string
	a := adapter{
		token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message string `json:"message"`
				ChatID  int64  `json:"chat_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.ChatID != 42 {
				t.Fatalf("model command chat ID = %d", request.ChatID)
			}
			forwarded = append(forwarded, request.Message)
			reply := "Current model: gpt-6.1-terra (terra)"
			if request.Message == "/m terra" {
				reply = "Selected model: gpt-6.1-terra (terra)"
			}
			return response(http.StatusOK, `{"type":"final","reply":"`+reply+`"}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "/m"))
	a.handle(context.Background(), privateMessage(42, "/m terra"))
	a.handle(context.Background(), privateMessage(42, "/model terra"))
	if len(forwarded) != 2 || forwarded[0] != "/m" || forwarded[1] != "/m terra" {
		t.Fatalf("forwarded model commands = %#v", forwarded)
	}
	if len(sent) != 2 || sent[0] != "Current model: gpt-6.1-terra (terra)" || sent[1] != "Selected model: gpt-6.1-terra (terra)" {
		t.Fatalf("model command replies = %#v", sent)
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
	a := adapter{
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
			return response(200, `{"type":"final","reply":"group reply"}`+"\n"), nil
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
		if r.Method != http.MethodGet || r.URL.Host != "127.0.0.1:8765" || r.URL.Path != "/livez" && r.URL.Path != "/healthz" {
			t.Fatal("unexpected agent health request")
		}
		return response(200, "ok\n"), nil
	}), client: doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(struct {
				Commands []telegramBotCommand `json:"commands"`
			}{Commands: telegramBotCommands})
			if err != nil || string(body) != string(want) {
				t.Fatalf("setMyCommands payload = %s, want %s", body, want)
			}
			return response(http.StatusBadGateway, `{"ok":false,"description":"temporary failure"}`), nil
		}
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

func TestTelegramCommandMenuUsesSupportedHelpCommands(t *testing.T) {
	want := []string{"help", "stop", "start", "pair", "clear", "m", "env"}
	if len(telegramBotCommands) != len(want) {
		t.Fatalf("Telegram command count = %d, want %d", len(telegramBotCommands), len(want))
	}
	for index, name := range want {
		command := telegramBotCommands[index]
		if command.Command != name || strings.Contains(command.Command, "/") || strings.Contains(command.Command, "-") {
			t.Fatalf("Telegram command %q at index %d is not a valid expected identifier", command.Command, index)
		}
		if command.Description == "" || len(command.Description) > 256 {
			t.Fatalf("Telegram command %q has an invalid description", command.Command)
		}
		if !strings.Contains(telegramHelpText, "/"+command.Command) {
			t.Fatalf("Telegram help does not describe /%s", command.Command)
		}
	}
	for _, command := range telegramBotCommands {
		if command.Command == "get-env" || command.Command == "get_env" {
			t.Fatalf("environment export command %q is invalid for Telegram's native menu", command.Command)
		}
	}
}

func TestPollingDispatchesUpdatesConcurrentlyAndCancelsHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan int64, 2)
	canceled := make(chan struct{}, 2)
	polled := make(chan int64, 1)
	a := adapter{
		token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/livez" {
				return response(200, "ok\n"), nil
			}
			if r.URL.Path == "/healthz" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			var payload struct {
				ChatID  int64  `json:"chat_id"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			if payload.Message == "/m" {
				polled <- -1
				return response(200, `{"type":"final","reply":"model"}`+"\n"), nil
			}
			entered <- payload.ChatID
			<-r.Context().Done()
			canceled <- struct{}{}
			return nil, r.Context().Err()
		}),
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
				return response(200, `{"ok":true,"result":true}`), nil
			}
			var payload struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			if payload.Offset == 0 {
				return response(200, `{"ok":true,"result":[
					{"update_id":9,"message":{"text":"/start","from":{"id":42},"chat":{"id":-300,"type":"group"}}},
					{"update_id":10,"message":{"text":"first","from":{"id":42},"chat":{"id":-100,"type":"group"}}},
					{"update_id":11,"message":{"text":"second","from":{"id":42},"chat":{"id":-200,"type":"group"}}},
					{"update_id":12,"message":{"text":"/m","from":{"id":42},"chat":{"id":-100,"type":"group"}}}
				]}`), nil
			}
			polled <- payload.Offset
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	chats := make(map[int64]bool)
	for i := 0; i < 2; i++ {
		select {
		case id := <-entered:
			chats[id] = true
		case <-ctx.Done():
			t.Fatal("distinct updates did not reach the agent concurrently")
		}
	}
	if !chats[-100] || !chats[-200] {
		t.Fatalf("dispatched chats = %v", chats)
	}
	seenModel, seenPoll := false, false
	for !seenModel || !seenPoll {
		select {
		case offset := <-polled:
			if offset == -1 {
				seenModel = true
			} else if offset == 13 {
				seenPoll = true
			} else {
				t.Fatalf("poll offset = %d", offset)
			}
		case <-ctx.Done():
			t.Fatal("polling or /m blocked behind active handlers")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("polling did not stop after cancellation")
	}
	if len(canceled) != 2 {
		t.Fatalf("canceled handlers = %d, want 2", len(canceled))
	}
}

func TestPollingProcessesEnvironmentUpdatesInTelegramOrder(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer timeoutCancel()
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	var stateMu sync.Mutex
	var agentMessages []string
	var sent []string
	pending := false
	var sentDone sync.Once
	sentComplete := make(chan struct{})
	a := adapter{
		token: testToken, pairing: paired, apiBase: "https://example.invalid",
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/livez" {
				return response(200, "ok\n"), nil
			}
			var request struct {
				Message     string                `json:"message"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			stateMu.Lock()
			agentMessages = append(agentMessages, request.Message)
			switch request.Message {
			case telegramEnvironmentCommand:
				pending = len(request.Attachments) == 1
			case telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword:
				if pending {
					pending = false
				}
			}
			currentPending := pending
			stateMu.Unlock()
			switch request.Message {
			case telegramEnvironmentCommand:
				if len(request.Attachments) == 1 {
					return response(200, `{"type":"final","reply":"preview A=one"}`+"\n"), nil
				}
				return response(200, `{"type":"final","reply":"cleared"}`+"\n"), nil
			case telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword:
				if currentPending {
					return response(200, `{"type":"final","reply":"saved"}`+"\n"), nil
				}
				return response(200, `{"type":"final","reply":"no pending"}`+"\n"), nil
			default:
				return nil, fmt.Errorf("unexpected agent message %q", request.Message)
			}
		}),
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/getUpdates"):
				var request struct {
					Offset int64 `json:"offset"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					return nil, err
				}
				if request.Offset == 0 {
					return response(200, `{"ok":true,"result":[
						{"update_id":10,"message":{"text":"/env","document":{"file_id":"env-file","file_name":"goaster.env","mime_type":"text/plain","file_size":7},"from":{"id":42},"chat":{"id":42,"type":"private"}}},
						{"update_id":11,"message":{"text":"/env\nrejected","from":{"id":42},"chat":{"id":42,"type":"private"}}},
						{"update_id":12,"message":{"text":"ACCEPT","from":{"id":42},"chat":{"id":42,"type":"private"}}}
					]}`), nil
				}
				select {
				case <-sentComplete:
					runCancel()
					return response(200, `{"ok":true,"result":[]}`), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-time.After(2 * time.Second):
					return nil, errors.New("environment replies did not complete")
				}
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(200, `{"ok":true,"result":{"file_size":7,"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(200, "A=one\n")
				fileResponse.ContentLength = 6
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(200, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					return nil, err
				}
				stateMu.Lock()
				sent = append(sent, request.Text)
				if len(sent) == 3 {
					sentDone.Do(func() { close(sentComplete) })
				}
				stateMu.Unlock()
				return response(200, `{"ok":true,"result":{}}`), nil
			default:
				return nil, fmt.Errorf("unexpected Telegram request %s", r.URL.Path)
			}
		}),
	}
	done := make(chan error, 1)
	go func() { done <- a.run(runCtx, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-timeoutCtx.Done():
		stateMu.Lock()
		t.Fatalf("ordered environment polling did not finish: agent=%q sent=%q", agentMessages, sent)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if !reflect.DeepEqual(agentMessages, []string{telegramEnvironmentCommand, telegramEnvironmentCommand, telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword}) {
		t.Fatalf("agent environment request order = %q", agentMessages)
	}
	if len(sent) != 3 || !strings.Contains(sent[0], "preview A=one") || !strings.Contains(sent[1], "same message") || !strings.Contains(sent[2], "no pending") {
		t.Fatalf("environment replies = %q", sent)
	}
}

func TestPollingRequiresAgentProcessAvailability(t *testing.T) {
	var livenessChecks, polls int
	agentStatus := http.StatusServiceUnavailable
	a := &adapter{agent: doerFunc(func(r *http.Request) (*http.Response, error) {
		livenessChecks++
		if r.URL.Path != "/livez" {
			t.Fatalf("unexpected readiness request %s", r.URL.Path)
		}
		if agentStatus == http.StatusOK {
			return response(agentStatus, "ok\n"), nil
		}
		return response(agentStatus, "private process detail"), nil
	}), client: doerFunc(func(*http.Request) (*http.Response, error) {
		polls++
		return response(200, `{"ok":true,"result":[]}`), nil
	})}
	ready := false
	err := a.run(context.Background(), func() error { ready = true; return nil })
	if err == nil || ready || polls != 0 || strings.Contains(err.Error(), "private process detail") {
		t.Fatalf("unavailable agent process: error=%v ready=%v polls=%d", err, ready, polls)
	}

	// The process can exit while the initial Telegram poll runs.
	livenessChecks, polls = 0, 0
	agentStatus = http.StatusOK
	a.client = doerFunc(func(*http.Request) (*http.Response, error) {
		polls++
		agentStatus = http.StatusServiceUnavailable
		return response(200, `{"ok":true,"result":[]}`), nil
	})
	err = a.run(context.Background(), func() error { ready = true; return nil })
	if err == nil || ready || polls != 1 || livenessChecks != 2 {
		t.Fatalf("agent process exited: error=%v ready=%v polls=%d checks=%d", err, ready, polls, livenessChecks)
	}
}

func TestPollingAndClearWorkWhenProviderAuthenticationIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	replied := make(chan string, 1)
	var polls, authenticationChecks int
	a := adapter{
		token: testToken, pairing: paired, apiBase: "https://example.invalid",
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/livez":
				return response(http.StatusOK, "ok\n"), nil
			case "/healthz":
				authenticationChecks++
				return response(http.StatusServiceUnavailable, "private credential detail"), nil
			case "/chat":
				var request struct {
					Message string `json:"message"`
					ChatID  int64  `json:"chat_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ChatID != 42 || request.Message != "/clear" {
					t.Fatalf("clear request = %+v, error = %v", request, err)
				}
				return response(http.StatusOK, `{"type":"final","reply":"Conversation history cleared."}`+"\n"), nil
			default:
				t.Fatalf("unexpected agent request %s", r.URL.Path)
				return nil, nil
			}
		}),
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/setMyCommands") {
				return response(http.StatusBadGateway, `{"ok":false,"description":"temporary failure"}`), nil
			}
			if strings.HasSuffix(r.URL.Path, "/getUpdates") {
				polls++
				if polls == 1 {
					return response(http.StatusOK, `{"ok":true,"result":[{"update_id":8,"message":{"text":"/clear","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			var payload struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			replied <- payload.Text
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
	}
	ready := false
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, func() error { ready = true; return nil }) }()
	select {
	case text := <-replied:
		if text != "Conversation history cleared." {
			t.Fatalf("clear reply = %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("clear command did not receive a reply")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !ready || polls < 1 || authenticationChecks != 0 {
		t.Fatalf("polling state ready=%v polls=%d authentication checks=%d", ready, polls, authenticationChecks)
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
	if len(sent) != 2 || !strings.Contains(sent[0], "not ready") || !strings.Contains(sent[0], "/clear") ||
		!strings.Contains(sent[1], "ready for text messages") || !strings.Contains(sent[1], "/clear") {
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
	a.handle(context.Background(), privateMessage(42, "/pair "+testCode))
	if p.userID != 42 || !strings.Contains(message, "Paired") || !strings.Contains(message, "not ready") {
		t.Fatalf("pairing readiness message = %q", message)
	}
}

func TestToolFeedbackTruncatesArgumentsByRune(t *testing.T) {
	arguments := strings.Repeat("界", telegramToolFeedbackMaxChars+5)
	text := toolFeedbackText("shell_execute", arguments)
	if !strings.HasPrefix(text, "🔧 `shell_execute`\n```json\n") {
		t.Fatalf("tool feedback prefix = %q", text)
	}
	preview := strings.TrimSuffix(strings.TrimPrefix(text, "🔧 `shell_execute`\n```json\n"), "\n```")
	if len([]rune(preview)) != telegramToolFeedbackMaxChars+1 || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview length/suffix = %d/%q", len([]rune(preview)), preview[len(preview)-3:])
	}
}

func TestAgentRejectsUntypedLegacyResponse(t *testing.T) {
	a := &adapter{agent: doerFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil || !strings.Contains(string(body), `"chat_type":"group"`) {
			t.Fatalf("conversation type missing from request: %v", err)
		}
		return response(http.StatusOK, `{"reply":"legacy reply"}`), nil
	})}

	reply, err := a.askAgent(context.WithValue(context.Background(), agentChatTypeKey{}, "group"), "hello", 42, nil, nil)
	if err == nil || reply != "" {
		t.Fatalf("untyped legacy response = %q, %v; want an error with no reply", reply, err)
	}
}

func TestRunRejectsInvalidConfiguredControlSecretBeforeTelegramState(t *testing.T) {
	t.Setenv(controlsecret.ValueEnv, "")
	t.Setenv(controlsecret.FileEnv, filepath.Join(t.TempDir(), "missing-control-secret"))
	if err := Run(context.Background(), Paths{}, nil); err == nil || err.Error() != "control secret configuration is invalid" {
		t.Fatalf("Run() error = %v; want invalid control secret", err)
	}
}

func TestRunRejectsInvalidSocketBeforeTelegramState(t *testing.T) {
	t.Setenv(controlsecret.FileEnv, "")
	// An unset file setting retains legacy behavior; an explicitly configured
	// empty socket must fail before loading Telegram state.
	if err := os.Unsetenv(controlsecret.FileEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv(controlsecret.ValueEnv, "")
	t.Setenv(controltransport.SocketEnv, "")
	if err := Run(context.Background(), Paths{}, nil); err == nil || err.Error() != "agent socket configuration is invalid or unavailable" {
		t.Fatalf("Run() error = %v; want invalid socket", err)
	}

	if os.Geteuid() >= 0 {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "control-secret")
		if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(controlsecret.FileEnv, path)
		if err := os.Unsetenv(controltransport.SocketEnv); err != nil {
			t.Fatal(err)
		}
		if err := Run(context.Background(), Paths{}, nil); err == nil || err.Error() != "agent socket configuration is invalid or unavailable" {
			t.Fatalf("Run() accepted file secret without socket: %v", err)
		}
	}
}

func TestTelegramToolProgressUsesOneMessageAndEditsFinal(t *testing.T) {
	var calls []struct {
		method    string
		text      string
		parseMode string
	}
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var payload struct {
					Text      string `json:"text"`
					ParseMode string `json:"parse_mode"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, struct {
					method    string
					text      string
					parseMode string
				}{"send", payload.Text, payload.ParseMode})
				return response(http.StatusOK, `{"ok":true,"result":{"message_id":7}}`), nil
			case strings.HasSuffix(r.URL.Path, "/editMessageText"):
				var payload struct {
					MessageID int64  `json:"message_id"`
					Text      string `json:"text"`
					ParseMode string `json:"parse_mode"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, struct {
					method    string
					text      string
					parseMode string
				}{"edit", fmt.Sprintf("%d:%s", payload.MessageID, payload.Text), payload.ParseMode})
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			default:
				t.Fatalf("unexpected Telegram request: %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
				return response(http.StatusOK, "ok\n"), nil
			}
			return response(http.StatusOK, "{"+
				`"type":"tool_call","name":"shell_execute","arguments":"{\"command\":\"git status\"}"}`+"\n"+
				`{"type":"tool_call","name":"web_search","arguments":"{\"query\":\"go\"}"}`+"\n"+
				`{"type":"final","reply":"Done."}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "inspect the repo"))
	want := []struct {
		method    string
		text      string
		parseMode string
	}{
		{"send", "🔧 <code>shell_execute</code>\n<pre><code>{\"command\":\"git status\"}\n</code></pre>", "HTML"},
		{"edit", "7:🔧 <code>web_search</code>\n<pre><code>{\"query\":\"go\"}\n</code></pre>", "HTML"},
		{"edit", "7:Done.", "HTML"},
	}
	if len(calls) != len(want) {
		t.Fatalf("Telegram calls = %#v, want %#v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("Telegram call %d = %+v, want %+v", index, calls[index], want[index])
		}
	}
}

func TestTelegramSendAndEditFallbackToPlainTextAfterFormattingRejection(t *testing.T) {
	type payload struct {
		ChatID    int64   `json:"chat_id"`
		MessageID int64   `json:"message_id"`
		Text      string  `json:"text"`
		ParseMode *string `json:"parse_mode"`
	}
	var requests []payload
	a := &adapter{client: doerFunc(func(r *http.Request) (*http.Response, error) {
		var got payload
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, got)
		if got.ParseMode != nil && *got.ParseMode == "HTML" {
			return response(http.StatusBadRequest, `{"ok":false,"description":"Bad Request: can't parse entities"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			return response(http.StatusOK, `{"ok":true,"result":{"message_id":9}}`), nil
		}
		return response(http.StatusOK, `{"ok":true,"result":true}`), nil
	})}

	message := "Run `echo <ok>`"
	if id, err := a.sendMessage(context.Background(), 42, message); err != nil || id != 9 {
		t.Fatalf("sendMessage = %d, %v", id, err)
	}
	if err := a.editMessage(context.Background(), 42, 9, message); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 {
		t.Fatalf("Telegram requests = %#v, want formatted and plain retries for send/edit", requests)
	}
	for index := range requests {
		if index%2 == 0 {
			if requests[index].ParseMode == nil || *requests[index].ParseMode != "HTML" || requests[index].Text != "Run <code>echo &lt;ok&gt;</code>" {
				t.Errorf("formatted request %d = %+v", index, requests[index])
			}
		} else if requests[index].ParseMode != nil || requests[index].Text != message {
			t.Errorf("plain fallback request %d = %+v", index, requests[index])
		}
	}
}

func TestTelegramDisplaysSafeSkillRecoveryForDirectAndStreamErrors(t *testing.T) {
	const guidance = "Some actions may already have completed. Check their results before continuing or repeating any action. Skill guidance could not be loaded or restored. Check the installed skill files; use /clear if the selection cannot be restored."
	const warning = "OpenAI authentication failed after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state, then resolve OpenAI authentication before retrying."
	const fallback = "The remote agent could not answer"
	for _, tc := range []struct {
		name, body, header, want string
		status                   int
		failEdit, breakStream    bool
	}{
		{"skill direct", guidance, "skill-recovery", guidance, http.StatusServiceUnavailable, false, false},
		{"skill stream", `{"type":"skill_recovery","error":"` + guidance + `"}` + "\n", "", guidance, http.StatusOK, false, false},
		{"skill forged", `{"type":"skill_recovery","error":"private fake-secret"}` + "\n", "", fallback, http.StatusOK, false, false},
		{"auth direct", warning + "\n", "post-tool-authentication", warning, http.StatusServiceUnavailable, false, false},
		{"auth stream", `{"type":"tool_call","name":"shell_execute","arguments":"{}"}` + "\n" + `{"type":"post_tool_authentication","error":"` + warning + `"}` + "\n", "", warning, http.StatusOK, false, false},
		{"auth failed edit", `{"type":"tool_call","name":"shell_execute","arguments":"{}"}` + "\n" + `{"type":"post_tool_authentication","error":"` + warning + `"}` + "\n", "", warning, http.StatusOK, true, false},
		{"auth stream failure and failed edit", `{"type":"tool_call","name":"shell_execute","arguments":"{}"}` + "\n" + `{"type":"post_tool_authentication","error":"` + warning + `"}` + "\n", "", warning, http.StatusOK, true, true},
		{"auth forged direct", "model fake-secret", "post-tool-authentication", fallback, http.StatusServiceUnavailable, false, false},
		{"auth extended direct", warning + " fake-secret", "post-tool-authentication", fallback, http.StatusServiceUnavailable, false, false},
		{"auth padded direct", " " + warning + "\n", "post-tool-authentication", fallback, http.StatusServiceUnavailable, false, false},
		{"auth extra newline", warning + "\n\n", "post-tool-authentication", fallback, http.StatusServiceUnavailable, false, false},
		{"auth missing header", warning, "", fallback, http.StatusServiceUnavailable, false, false},
		{"auth forged stream", `{"type":"post_tool_authentication","error":"model fake-secret"}` + "\n", "", fallback, http.StatusOK, false, false},
		{"auth forged stream failure", `{"type":"post_tool_authentication","error":"model fake-secret"}` + "\n", "", fallback, http.StatusOK, false, true},
		{"auth extended stream", `{"type":"post_tool_authentication","error":"` + warning + ` fake-secret"}` + "\n", "", fallback, http.StatusOK, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent string
			var delivered []string
			failedEdits := 0
			a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
				client: doerFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
						return response(200, `{"ok":true,"result":true}`), nil
					}
					var payload struct {
						Text string `json:"text"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if tc.failEdit && strings.HasSuffix(r.URL.Path, "/editMessageText") {
						failedEdits++
						return response(http.StatusBadRequest, `{"ok":false,"description":"message cannot be edited"}`), nil
					}
					sent = payload.Text
					delivered = append(delivered, sent)
					return response(200, `{"ok":true,"result":{"message_id":1}}`), nil
				}),
				agent: doerFunc(func(*http.Request) (*http.Response, error) {
					resp := response(tc.status, tc.body)
					if tc.breakStream {
						resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(tc.body), iotest.ErrReader(errors.New("stream read failed"))))
					}
					resp.Header = make(http.Header)
					if tc.header != "" {
						resp.Header.Set("X-GoAster-Error", tc.header)
					}
					return resp, nil
				}),
			}
			a.handle(context.Background(), privateMessage(42, "run"))
			if !strings.Contains(sent, tc.want) || strings.Contains(sent, "fake-secret") || len(sent) > 512 {
				t.Fatalf("Telegram recovery message = %q", sent)
			}
			if tc.failEdit && (failedEdits != 1 || len(delivered) != 2 || delivered[1] != warning) {
				t.Fatalf("failed progress edit: edits=%d delivered=%q", failedEdits, delivered)
			}
		})
	}
}

func TestPollingStopTargetsExecutingTopicAndKeepsQueuedRequest(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan string, 2)
	canceled := make(chan string, 2)
	replies := make(chan struct {
		Text   string
		Thread int64
	}, 8)
	a := adapter{token: testToken, pairing: paired, apiBase: "https://example.invalid"}
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/livez" {
			return response(200, "ok\n"), nil
		}
		var p struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return nil, err
		}
		entered <- p.Message
		<-r.Context().Done()
		canceled <- p.Message
		return nil, r.Context().Err()
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Offset == 0 {
				return response(200, `{"ok":true,"result":[{"update_id":1,"message":{"text":"first","message_thread_id":11,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}}]}`), nil
			}
			if p.Offset == 2 {
				select {
				case <-entered:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return response(200, `{"ok":true,"result":[{"update_id":2,"message":{"text":"second","message_thread_id":22,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}},{"update_id":3,"message":{"text":"/stop","message_thread_id":22,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}},{"update_id":4,"message":{"text":"/stop@other_bot","message_thread_id":11,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}},{"update_id":5,"message":{"caption":"/stop","document":{"file_id":"x"},"message_thread_id":11,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}}]}`), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var p struct {
				Text   string `json:"text"`
				Thread int64  `json:"message_thread_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			replies <- struct {
				Text   string
				Thread int64
			}{p.Text, p.Thread}
		}
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	select {
	case got := <-canceled:
		if got != "first" {
			t.Fatalf("canceled %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first turn was not canceled")
	}
	select {
	case got := <-entered:
		if got != "second" {
			t.Fatalf("started %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued turn did not start")
	}
	seen := map[string]int64{}
	for len(seen) < 2 {
		select {
		case p := <-replies:
			seen[p.Text] = p.Thread
		case <-time.After(3 * time.Second):
			t.Fatalf("replies: %v", seen)
		}
	}
	if seen[telegramNoActiveTurnMessage] != 22 || seen[telegramStopMessage] != 11 {
		t.Fatalf("topic replies: %v", seen)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not exit")
	}
}

func TestPollingStopInSameBatchCancelsTurn(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := make(chan string, 2)
	a := adapter{token: testToken, pairing: paired, apiBase: "https://example.invalid"}
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/livez" {
			return response(200, "ok\n"), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Offset == 0 {
				return response(200, `{"ok":true,"result":[{"update_id":1,"message":{"text":"work","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":2,"message":{"text":"/stop","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var p struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			replies <- p.Text
		}
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	select {
	case got := <-replies:
		if got != telegramStopMessage {
			t.Fatalf("reply %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no stop acknowledgement")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not exit")
	}
}

func TestPollingClearOrdersAndDiscardsEarlierQueuedRequests(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan string, 8)
	canceled := make(chan struct{})
	release := make(chan struct{})
	a := adapter{token: testToken, pairing: paired, apiBase: "https://example.invalid"}
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/livez" {
			return response(200, "ok\n"), nil
		}
		var p struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return nil, err
		}
		entered <- p.Message
		if p.Message == "first" {
			<-r.Context().Done()
			close(canceled)
			<-release
			return nil, r.Context().Err()
		}
		return response(200, `{"type":"final","reply":"ok"}`+"\n"), nil
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Offset == 0 {
				return response(200, `{"ok":true,"result":[{"update_id":1,"message":{"text":"first","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":2,"message":{"text":"stale","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":3,"message":{"text":"/clear","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":4,"message":{"text":"new","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	select {
	case got := <-entered:
		if got != "first" {
			t.Fatalf("first request = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first did not start")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("clear did not cancel first")
	}
	close(release)
	for _, want := range []string{"/clear", "new"} {
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("request = %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not start", want)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not exit")
	}
}

func TestPollingUnknownUsernameDoesNotStopAddressedToOtherBot(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polling := make(chan struct{})
	proceed := make(chan struct{})
	canceled := make(chan struct{}, 1)
	a := adapter{token: testToken, pairing: paired, apiBase: "https://example.invalid"}
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/livez" {
			return response(200, "ok\n"), nil
		}
		<-r.Context().Done()
		canceled <- struct{}{}
		return nil, r.Context().Err()
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Offset == 0 {
				return response(200, `{"ok":true,"result":[{"update_id":1,"message":{"text":"work","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":2,"message":{"text":"/stop@other_bot","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
			}
			if p.Offset == 3 {
				close(polling)
				select {
				case <-proceed:
					return response(200, `{"ok":true,"result":[{"update_id":3,"message":{"text":"/stop","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	select {
	case <-polling:
	case <-time.After(3 * time.Second):
		t.Fatal("polling stalled")
	}
	select {
	case <-canceled:
		t.Fatal("addressed stop canceled the turn with unknown username")
	default:
	}
	close(proceed)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("bare stop did not cancel turn")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not exit")
	}
}
