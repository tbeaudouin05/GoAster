package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type adapter struct {
	token   string
	pairing *pairing
	client  httpDoer
	agent   httpDoer
	apiBase string
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	SenderChat json.RawMessage `json:"sender_chat"`
}

// Run starts polling only after the token and pairing state are protected.
// ready is called after the agent is authenticated and Telegram accepts an
// initial getUpdates request.
func Run(ctx context.Context, paths Paths, ready func() error) error {
	token, err := readPrivate(paths.Token)
	if err != nil || ValidateToken(string(token)) != nil {
		return fmt.Errorf("Telegram token file is invalid")
	}
	pairing, err := loadPairing(paths)
	if err != nil {
		return fmt.Errorf("Telegram pairing state is invalid")
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a := &adapter{
		token: string(token), pairing: pairing,
		client:  &http.Client{Timeout: 40 * time.Second, CheckRedirect: noRedirect},
		agent:   &http.Client{Timeout: 150 * time.Second, CheckRedirect: noRedirect},
		apiBase: "https://api.telegram.org",
	}
	return a.run(ctx, ready)
}

func (a *adapter) run(ctx context.Context, ready func() error) error {
	if !a.agentReady(ctx) {
		return fmt.Errorf("remote agent is not ready; check its OpenAI authentication")
	}
	updates, err := a.getUpdates(ctx, 0, 0)
	if err != nil {
		return fmt.Errorf("Telegram polling could not start; check the bot token, connection and webhook settings")
	}
	if !a.agentReady(ctx) {
		return fmt.Errorf("remote agent is not ready; check its OpenAI authentication")
	}
	if ready != nil {
		if err := ready(); err != nil {
			return fmt.Errorf("could not report Telegram service readiness")
		}
	}
	var offset int64
	for {
		for _, item := range updates {
			if item.ID < offset {
				continue
			}
			a.handle(ctx, item.Message)
			if item.ID < int64(^uint64(0)>>1) {
				offset = item.ID + 1
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		updates, err = a.getUpdates(ctx, offset, 30)
		if err != nil {
			// Never print HTTP errors: they may include the token-bearing URL.
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			updates = nil
		}
	}
}

func (a *adapter) agentReady(ctx context.Context) bool {
	if a.agent == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8765/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := a.agent.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 33))
	return err == nil && resp.StatusCode == http.StatusOK && string(body) == "ok\n"
}

func (a *adapter) getUpdates(ctx context.Context, offset int64, timeout int) ([]update, error) {
	var updates []update
	err := a.call(ctx, "getUpdates", struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, timeout, []string{"message"}}, &updates)
	if err == nil && updates == nil {
		return nil, fmt.Errorf("Telegram response was invalid")
	}
	return updates, err
}

func (a *adapter) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("Telegram request failed")
	}
	// This URL must never be included in an error or log because it contains the token.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBase+"/bot"+a.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Telegram request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram request failed")
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&envelope); err != nil || !envelope.OK {
		return fmt.Errorf("Telegram request failed")
	}
	if result != nil && json.Unmarshal(envelope.Result, result) != nil {
		return fmt.Errorf("Telegram response was invalid")
	}
	return nil
}

func (a *adapter) handle(ctx context.Context, m *message) {
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID <= 0 || m.Chat.ID == 0 || len(m.SenderChat) != 0 && string(m.SenderChat) != "null" {
		return
	}
	if m.Chat.Type != "private" && m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		return
	}
	if m.Chat.Type == "private" && m.From.ID != m.Chat.ID {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	if a.pairing.userID == 0 {
		if m.Chat.Type != "private" {
			return
		}
		if text == "/start" {
			a.send(ctx, m.Chat.ID, "Send /start followed by the one-time pairing code shown in GoAster.")
			return
		}
		if strings.HasPrefix(text, "/start ") && a.pairing.tryPair(m.From.ID, strings.TrimSpace(strings.TrimPrefix(text, "/start "))) {
			if a.agentReady(ctx) {
				a.send(ctx, m.Chat.ID, "Paired. Send a text message to chat.")
			} else {
				a.send(ctx, m.Chat.ID, "Paired. The remote agent is not ready; check its remote authentication.")
			}
		}
		return
	}
	if m.From.ID != a.pairing.userID {
		return
	}
	if text == "/start" || strings.HasPrefix(text, "/start ") {
		if a.agentReady(ctx) {
			a.send(ctx, m.Chat.ID, "GoAster is ready for text messages.")
		} else {
			a.send(ctx, m.Chat.ID, "The remote agent is not ready; check its remote authentication.")
		}
		return
	}
	if strings.HasPrefix(text, "/") {
		return
	}
	if strings.Contains(text, a.token) {
		a.send(ctx, m.Chat.ID, "That message contains the bot token and was not sent to the agent.")
		return
	}
	reply, err := a.askAgent(ctx, text, m.Chat.ID)
	if err != nil {
		a.send(ctx, m.Chat.ID, "The remote agent could not answer. Check its remote authentication.")
		return
	}
	// Telegram limits message size. Chunks also handle non-BMP characters.
	runes := []rune(reply)
	for len(runes) > 0 {
		n := len(runes)
		if n > 2000 {
			n = 2000
		}
		a.send(ctx, m.Chat.ID, string(runes[:n]))
		runes = runes[n:]
	}
}

func (a *adapter) send(ctx context.Context, userID int64, text string) {
	_ = a.call(ctx, "sendMessage", struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{userID, text}, nil)
}

func (a *adapter) askAgent(ctx context.Context, text string, chatID int64) (string, error) {
	body, err := json.Marshal(struct {
		Message string `json:"message"`
		ChatID  int64  `json:"chat_id"`
	}{text, chatID})
	if err != nil {
		return "", fmt.Errorf("agent request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8765/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agent request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.agent.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent request failed")
	}
	defer resp.Body.Close()
	var result struct {
		Reply string `json:"reply"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result) != nil || strings.TrimSpace(result.Reply) == "" {
		return "", fmt.Errorf("agent request failed")
	}
	return result.Reply, nil
}
