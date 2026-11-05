package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type adapter struct {
	token              string
	pairing            *pairing
	pairingMu          sync.Mutex
	client             httpDoer
	agent              httpDoer
	authControlSecret  string
	apiBase            string
	attachmentMaxBytes int64 // test seam; zero uses attachment.MaxBytes.
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	Text         string      `json:"text"`
	Caption      string      `json:"caption"`
	Photo        []photoSize `json:"photo"`
	Document     *document   `json:"document"`
	MediaGroupID string      `json:"media_group_id"`
	Chat         struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	SenderChat json.RawMessage `json:"sender_chat"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type attachmentSource struct {
	kind     string
	fileID   string
	filename string
	mimeType string
	fileSize int64
}

type telegramFile struct {
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type telegramFormattingError struct{}

func (telegramFormattingError) Error() string { return "Telegram request failed" }

type agentModelError struct {
	message string
}

func (err agentModelError) Error() string { return err.message }

const (
	telegramToolFeedbackMaxChars  = 600
	telegramTypingRefreshInterval = 4 * time.Second
	telegramTypingActionTimeout   = 2 * time.Second
	telegramAgentRequestTimeout   = 20 * time.Minute
	telegramAuthControlHeader     = "X-GoAster-GitHub-Auth-Control"
)

// Run starts polling only after the token and pairing state are protected.
// ready is called after the agent process is available and Telegram accepts
// an initial getUpdates request; /clear and /m remain usable while model
// authentication is unavailable.
func Run(ctx context.Context, paths Paths, ready func() error) error {
	controlSecret, err := controlsecret.Load()
	if err != nil {
		return err
	}
	agent, err := newAgentClient()
	if err != nil {
		return err
	}
	token, err := readPrivate(paths.Token)
	if err != nil || ValidateToken(string(token)) != nil {
		return fmt.Errorf("Telegram token file is invalid")
	}
	pairing, err := loadPairing(paths)
	if err != nil {
		return fmt.Errorf("Telegram pairing state is invalid")
	}
	a := &adapter{
		token: string(token), pairing: pairing,
		authControlSecret: controlSecret,
		client:            &http.Client{Timeout: 40 * time.Second, CheckRedirect: noRedirect},
		agent:             agent,
		apiBase:           "https://api.telegram.org",
	}
	return a.run(ctx, ready)
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func newAgentClient() (httpDoer, error) {
	transport, err := controltransport.ClientTransport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: telegramAgentRequestTimeout, CheckRedirect: noRedirect, Transport: transport}, nil
}

func (a *adapter) run(ctx context.Context, ready func() error) error {
	if !a.agentEndpointAvailable(ctx, "/livez") {
		return fmt.Errorf("remote agent is unavailable")
	}
	updates, err := a.getUpdates(ctx, 0, 0)
	if err != nil {
		return fmt.Errorf("Telegram polling could not start; check the bot token, connection and webhook settings")
	}
	if !a.agentEndpointAvailable(ctx, "/livez") {
		return fmt.Errorf("remote agent is unavailable")
	}
	if ready != nil {
		if err := ready(); err != nil {
			return fmt.Errorf("could not report Telegram service readiness")
		}
	}
	var offset int64
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		for _, item := range updates {
			if ctx.Err() != nil {
				return nil
			}
			if item.ID < offset {
				continue
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				a.handle(ctx, item.Message)
			}()
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

func (a *adapter) agentEndpointAvailable(ctx context.Context, endpoint string) bool {
	if a.agent == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8765"+endpoint, nil)
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
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&envelope)
	if isTelegramFormattingRejection(envelope.Description) {
		return telegramFormattingError{}
	}
	if decodeErr != nil || !envelope.OK || resp.StatusCode != http.StatusOK {
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
	if m.MediaGroupID != "" {
		return
	}
	if m.Chat.Type != "private" && m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		return
	}
	if m.Chat.Type == "private" && m.From.ID != m.Chat.ID {
		return
	}
	hasAttachment := m.hasAttachment()
	text := strings.TrimSpace(m.Text)
	if text == "" && hasAttachment {
		text = strings.TrimSpace(m.Caption)
	}
	a.pairingMu.Lock()
	pairedUserID := a.pairing.userID
	paired := false
	if pairedUserID == 0 && m.Chat.Type == "private" && strings.HasPrefix(text, "/start ") {
		paired = a.pairing.tryPair(m.From.ID, strings.TrimSpace(strings.TrimPrefix(text, "/start ")))
	}
	a.pairingMu.Unlock()
	if pairedUserID == 0 {
		if m.Chat.Type != "private" {
			return
		}
		if text == "/start" {
			a.send(ctx, m.Chat.ID, "Send /start followed by the one-time pairing code shown in GoAster.")
			return
		}
		if paired {
			if a.agentEndpointAvailable(ctx, "/healthz") {
				a.send(ctx, m.Chat.ID, "Paired. Send text, an image, or a document to chat. Use /m to view the model, /m MODEL_OR_ALIAS to select one, and /clear to clear this chat's history.")
			} else {
				a.send(ctx, m.Chat.ID, "Paired. The remote agent is not ready; check its remote authentication. You can still use /m to view or select a model and /clear to clear this chat's history.")
			}
		}
		return
	}
	if m.From.ID != pairedUserID {
		return
	}
	if action, recognized := githubLoginCommand(text); recognized {
		if hasAttachment {
			a.send(ctx, m.Chat.ID, "Usage: /github-login, /github-login cancel, or /github-login reconcile")
			return
		}
		if m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
			a.send(ctx, m.Chat.ID, "GitHub login can only be authorized in the paired private Telegram chat.")
			return
		}
		switch action {
		case "authorize":
			if a.authorizeGitHubLogin(ctx, m.Chat.ID) {
				a.send(ctx, m.Chat.ID, "GitHub login is authorized for this private chat for a short time. Ask GoAster to continue the GitHub workflow. The verification code will be delivered only in this chat.")
			} else {
				a.send(ctx, m.Chat.ID, "GitHub login could not be authorized. No login was started.")
			}
		case "cancel":
			if a.cancelGitHubLogin(ctx, m.Chat.ID) {
				a.send(ctx, m.Chat.ID, "GitHub login cancellation was requested. Cleanup is pending; wait for the final status.")
			} else {
				a.send(ctx, m.Chat.ID, "GitHub login cancellation could not reach the agent.")
			}
		case "reconcile":
			if a.reconcileGitHubLogin(ctx, m.Chat.ID) {
				a.send(ctx, m.Chat.ID, "GitHub login reconciliation was acknowledged. Only retry after manually reconciling the GitHub credential in gh.")
			} else {
				a.send(ctx, m.Chat.ID, "GitHub login has no reconciliation acknowledgment available.")
			}
		default:
			a.send(ctx, m.Chat.ID, "Usage: /github-login, /github-login cancel, or /github-login reconcile")
		}
		return
	}
	if text == "/start" || strings.HasPrefix(text, "/start ") {
		if a.agentEndpointAvailable(ctx, "/healthz") {
			a.send(ctx, m.Chat.ID, "GoAster is ready for text messages, images, and documents. Use /m to view the model, /m MODEL_OR_ALIAS to select one, and /clear to clear this chat's history.")
		} else {
			a.send(ctx, m.Chat.ID, "The remote agent is not ready; check its remote authentication. You can still use /m to view or select a model and /clear to clear this chat's history.")
		}
		return
	}
	if chat.IsClearCommand(text) {
		if hasAttachment {
			a.send(ctx, m.Chat.ID, "Usage: /clear")
			return
		}
		reply, err := a.askAgent(ctx, text, m.Chat.ID, nil, nil)
		if err != nil {
			a.send(ctx, m.Chat.ID, "Conversation history could not be cleared.")
			return
		}
		a.send(ctx, m.Chat.ID, reply)
		return
	}
	if chat.IsModelCommand(text) {
		if hasAttachment {
			a.send(ctx, m.Chat.ID, "Usage: /m [MODEL_OR_ALIAS]")
			return
		}
		reply, err := a.askAgent(ctx, text, m.Chat.ID, nil, nil)
		if err != nil {
			a.send(ctx, m.Chat.ID, "Model selection could not be completed.")
			return
		}
		a.send(ctx, m.Chat.ID, reply)
		return
	}
	if strings.HasPrefix(text, "/") {
		return
	}
	if a.token != "" && strings.Contains(text, a.token) {
		a.send(ctx, m.Chat.ID, "That message contains the bot token and was not sent to the agent.")
		return
	}
	if text == "" && !hasAttachment {
		return
	}
	stopTyping := a.startTyping(ctx, m.Chat.ID)
	defer stopTyping()
	var progressMessageID int64
	showToolCall := func(name, arguments string) {
		text := toolFeedbackText(name, arguments)
		if name == "auth_handoff" {
			text = "🔐 GitHub sign-in handoff started"
		}
		if progressMessageID == 0 {
			progressMessageID, _ = a.sendMessage(ctx, m.Chat.ID, text)
			return
		}
		_ = a.editMessage(ctx, m.Chat.ID, progressMessageID, text)
	}
	sendFinal := func(text string) {
		stopTyping()
		chunks := splitTelegramMarkdown(text, 2000)
		if len(chunks) == 0 {
			return
		}
		if progressMessageID != 0 {
			err := a.editMessage(ctx, m.Chat.ID, progressMessageID, chunks[0])
			if err == nil || !chat.IsPostToolAuthenticationMessage(text) {
				chunks = chunks[1:]
			}
		}
		for _, chunk := range chunks {
			a.send(ctx, m.Chat.ID, chunk)
		}
	}
	attachments, err := a.downloadAttachments(ctx, m)
	if err != nil {
		if errors.Is(err, attachment.ErrUnsupportedImage) {
			sendFinal("That image format is unsupported. Supported image formats are JPEG, PNG, GIF, and WebP.")
			return
		}
		sendFinal("The Telegram attachment could not be downloaded safely.")
		return
	}
	reply, err := a.askAgent(ctx, text, m.Chat.ID, attachments, showToolCall)
	if err != nil {
		if errors.Is(err, attachment.ErrUnsupportedImage) {
			sendFinal("That image format is unsupported. Supported image formats are JPEG, PNG, GIF, and WebP.")
			return
		}
		var modelErr agentModelError
		if errors.As(err, &modelErr) {
			sendFinal(modelErr.message)
			return
		}
		sendFinal("The remote agent could not answer. Check its remote authentication.")
		return
	}
	// Telegram limits message size. Chunks also handle non-BMP characters.
	sendFinal(reply)
}

func githubLoginCommand(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/github-login") {
		return "", false
	}
	if len(fields) == 1 {
		return "authorize", true
	}
	if len(fields) == 2 {
		switch {
		case strings.EqualFold(fields[1], "cancel"):
			return "cancel", true
		case strings.EqualFold(fields[1], "reconcile"):
			return "reconcile", true
		}
	}
	return "invalid", true
}

func (a *adapter) authorizeGitHubLogin(ctx context.Context, chatID int64) bool {
	return a.githubAuthRequest(ctx, "/auth/github/authorize", chatID)
}

func (a *adapter) cancelGitHubLogin(ctx context.Context, chatID int64) bool {
	return a.githubAuthRequest(ctx, "/auth/github/cancel", chatID)
}

func (a *adapter) reconcileGitHubLogin(ctx context.Context, chatID int64) bool {
	return a.githubAuthRequest(ctx, "/auth/github/reconcile", chatID)
}

func (a *adapter) githubAuthRequest(ctx context.Context, endpoint string, chatID int64) bool {
	if a.agent == nil || chatID <= 0 {
		return false
	}
	if a.authControlSecret == "" || strings.ContainsAny(a.authControlSecret, "\r\n") {
		return false
	}
	requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		ChatID int64 `json:"chat_id"`
	}{chatID})
	if err != nil {
		return false
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://127.0.0.1:8765"+endpoint, bytes.NewReader(body))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(telegramAuthControlHeader, a.authControlSecret)
	response, err := a.agent.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
	return response.StatusCode == http.StatusOK
}

func (a *adapter) startTyping(ctx context.Context, chatID int64) func() {
	typingCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})

	a.sendChatAction(typingCtx, chatID)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(telegramTypingRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				a.sendChatAction(typingCtx, chatID)
			}
		}
	}()

	return func() {
		cancel()
		<-stopped
	}
}

func (a *adapter) sendChatAction(ctx context.Context, chatID int64) {
	actionCtx, cancel := context.WithTimeout(ctx, telegramTypingActionTimeout)
	defer cancel()
	_ = a.call(actionCtx, "sendChatAction", struct {
		ChatID int64  `json:"chat_id"`
		Action string `json:"action"`
	}{chatID, "typing"}, nil)
}

func (a *adapter) send(ctx context.Context, userID int64, text string) {
	_, _ = a.sendMessage(ctx, userID, text)
}

func (a *adapter) sendMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	payload := struct {
		ChatID    int64  `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{ChatID: chatID, Text: markdownToTelegramHTML(text), ParseMode: "HTML"}
	err := a.call(ctx, "sendMessage", payload, &result)
	if isTelegramFormattingError(err) {
		payload.Text = text
		payload.ParseMode = ""
		err = a.call(ctx, "sendMessage", payload, &result)
	}
	return result.MessageID, err
}

func (a *adapter) editMessage(ctx context.Context, chatID, messageID int64, text string) error {
	payload := struct {
		ChatID    int64  `json:"chat_id"`
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{ChatID: chatID, MessageID: messageID, Text: markdownToTelegramHTML(text), ParseMode: "HTML"}
	err := a.call(ctx, "editMessageText", payload, nil)
	if isTelegramFormattingError(err) {
		payload.Text = text
		payload.ParseMode = ""
		err = a.call(ctx, "editMessageText", payload, nil)
	}
	return err
}

func toolFeedbackText(name, arguments string) string {
	preview := truncateRunes(strings.TrimSpace(arguments), telegramToolFeedbackMaxChars)
	if preview == "" {
		return fmt.Sprintf("🔧 `%s`", name)
	}
	return fmt.Sprintf("🔧 `%s`\n```json\n%s\n```", name, preview)
}

func isTelegramFormattingRejection(description string) bool {
	description = strings.ToLower(description)
	return strings.Contains(description, "can't parse entities") ||
		strings.Contains(description, "can't find end tag") ||
		strings.Contains(description, "unsupported start tag") ||
		strings.Contains(description, "unsupported end tag") ||
		strings.Contains(description, "no start tag")
}

func isTelegramFormattingError(err error) bool {
	_, ok := err.(telegramFormattingError)
	return ok
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

type authHandoffEvent struct {
	Type            string `json:"type"`
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
	ExpiresAt       string `json:"expires_at"`
	Account         string `json:"account"`
}

func (a *adapter) handleAuthHandoffEvent(ctx context.Context, chatID int64, event authHandoffEvent) error {
	if event.Provider != "github" {
		return errors.New("unsupported authentication handoff")
	}
	var text string
	switch event.Status {
	case "started":
		if !validGitHubVerificationURLForTelegram(event.VerificationURL) || !validGitHubDeviceCodeForTelegram(event.UserCode) {
			return errors.New("invalid GitHub device instructions")
		}
		expiresAt, err := time.Parse(time.RFC3339, event.ExpiresAt)
		if err != nil {
			return errors.New("invalid GitHub device expiry")
		}
		text = "GitHub sign-in was requested. Open " + event.VerificationURL + " and enter code " + event.UserCode + ". This one-time code expires at " + expiresAt.UTC().Format(time.RFC3339Nano) + ". Do not share it."
	case "completed":
		if !validGitHubAccountForTelegram(event.Account) {
			return errors.New("invalid verified GitHub account")
		}
		text = "GitHub sign-in completed. Verified account: " + event.Account + " on github.com."
	case "cancellation_requested":
		text = "GitHub sign-in cancellation was requested. Cleanup is pending; wait for the final status."
	case "canceled":
		text = "GitHub sign-in cancellation completed after cleanup. No new GitHub account was left installed."
	case "expired":
		text = "GitHub sign-in expired. Send /github-login again for a new handoff."
	case "failed":
		text = "GitHub sign-in could not be completed. No account was verified."
	case "reconciliation_required":
		text = "GitHub sign-in could not be safely rolled back. Manually reconcile the GitHub account in gh before retrying."
	default:
		return errors.New("unknown authentication handoff status")
	}
	if _, err := a.sendMessage(ctx, chatID, text); err != nil {
		return errors.New("GitHub authentication status could not be delivered")
	}
	return nil
}

func validGitHubVerificationURLForTelegram(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" && parsed.Path == "/login/device" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}

func validGitHubDeviceCodeForTelegram(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validGitHubAccountForTelegram(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 39 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (a *adapter) askAgent(ctx context.Context, text string, chatID int64, attachments []attachment.Incoming, onToolCall func(string, string)) (string, error) {
	body, err := json.Marshal(struct {
		Message     string                `json:"message"`
		ChatID      int64                 `json:"chat_id"`
		Attachments []attachment.Incoming `json:"attachments,omitempty"`
	}{text, chatID, attachments})
	if err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8765/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(telegramAuthControlHeader, a.authControlSecret)
	resp, err := a.agent.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnsupportedMediaType {
		return "", attachment.ErrUnsupportedImage
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.Header.Get("X-GoAster-Error") == "post-tool-authentication" {
			message := strings.TrimSuffix(string(body), "\n")
			if chat.IsPostToolAuthenticationMessage(message) {
				return "", agentModelError{message: message}
			}
			return "", fmt.Errorf("agent request failed")
		}
		message := strings.TrimSpace(string(body))
		if resp.Header.Get("X-GoAster-Error") == "skill-recovery" && chat.IsSkillRecoveryMessage(message) {
			return "", agentModelError{message: message}
		}
		if strings.HasPrefix(message, "model ") {
			return "", agentModelError{message: truncateRunes(message, 512)}
		}
		return "", fmt.Errorf("agent request failed")
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4*1024*1024))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var reply string
	var modelError string
	for scanner.Scan() {
		var event authHandoffEvent
		var toolEvent struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		var replyEvent struct {
			Type  string `json:"type"`
			Reply string `json:"reply"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", fmt.Errorf("agent request failed: %w", err)
		}
		if err := json.Unmarshal(scanner.Bytes(), &toolEvent); err != nil {
			return "", fmt.Errorf("agent request failed: %w", err)
		}
		if err := json.Unmarshal(scanner.Bytes(), &replyEvent); err != nil {
			return "", fmt.Errorf("agent request failed: %w", err)
		}
		switch replyEvent.Type {
		case "tool_call":
			if onToolCall != nil {
				onToolCall(toolEvent.Name, toolEvent.Arguments)
			}
		case "final":
			reply = replyEvent.Reply
		case "error":
			if strings.HasPrefix(replyEvent.Error, "model ") {
				modelError = truncateRunes(strings.TrimSpace(replyEvent.Error), 512)
			}
		case "skill_recovery":
			if chat.IsSkillRecoveryMessage(replyEvent.Error) {
				modelError = replyEvent.Error
			}
		case "post_tool_authentication":
			if chat.IsPostToolAuthenticationMessage(replyEvent.Error) {
				// This is a terminal agent event. Deliver its validated warning
				// even if the remaining response stream is broken.
				return "", agentModelError{message: replyEvent.Error}
			}
		case "auth_handoff":
			if err := a.handleAuthHandoffEvent(ctx, chatID, event); err != nil {
				_ = a.cancelGitHubLogin(ctx, chatID)
				return "", errors.New("agent authentication handoff failed")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	if strings.TrimSpace(reply) == "" {
		if modelError != "" {
			return "", agentModelError{message: modelError}
		}
		return "", fmt.Errorf("agent request failed")
	}
	return reply, nil
}
func (m *message) hasAttachment() bool {
	return m != nil && (len(m.Photo) > 0 || m.Document != nil)
}

func (m *message) attachmentSource() (attachmentSource, error) {
	if m == nil {
		return attachmentSource{}, fmt.Errorf("message is missing")
	}
	if len(m.Photo) > 0 && m.Document != nil {
		return attachmentSource{}, fmt.Errorf("message contains unsupported media")
	}
	if len(m.Photo) > 0 {
		selected := m.Photo[0]
		for _, candidate := range m.Photo[1:] {
			selectedArea := int64(selected.Width) * int64(selected.Height)
			candidateArea := int64(candidate.Width) * int64(candidate.Height)
			if candidateArea > selectedArea || candidateArea == selectedArea && candidate.FileSize > selected.FileSize {
				selected = candidate
			}
		}
		return attachmentSource{
			kind:     "image",
			fileID:   selected.FileID,
			filename: "photo.jpg",
			mimeType: "image/jpeg",
			fileSize: selected.FileSize,
		}, nil
	}
	if m.Document != nil {
		filename := attachment.SanitizeFilename(m.Document.FileName)
		if strings.TrimSpace(m.Document.FileName) == "" {
			filename = "document"
		}
		return attachmentSource{
			kind:     "document",
			fileID:   m.Document.FileID,
			filename: filename,
			mimeType: m.Document.MIMEType,
			fileSize: m.Document.FileSize,
		}, nil
	}
	return attachmentSource{}, fmt.Errorf("message has no supported attachment")
}

func (a *adapter) downloadAttachments(ctx context.Context, m *message) ([]attachment.Incoming, error) {
	if !m.hasAttachment() {
		return nil, nil
	}
	source, err := m.attachmentSource()
	if err != nil || strings.TrimSpace(source.fileID) == "" {
		return nil, fmt.Errorf("attachment download failed")
	}
	limit := a.maxAttachmentBytes()
	if source.fileSize < 0 || source.fileSize > limit {
		return nil, fmt.Errorf("attachment exceeds the size limit")
	}

	var file telegramFile
	if err := a.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{source.fileID}, &file); err != nil || file.FilePath == "" || file.FileSize < 0 || file.FileSize > limit {
		return nil, fmt.Errorf("attachment download failed")
	}
	downloadURL, err := telegramFileURL(a.apiBase, a.token, file.FilePath)
	if err != nil {
		return nil, fmt.Errorf("attachment download failed")
	}
	data, err := a.downloadFile(ctx, downloadURL, limit)
	if err != nil {
		return nil, fmt.Errorf("attachment download failed")
	}
	item := attachment.Incoming{
		Type: source.kind, Filename: source.filename, MIMEType: source.mimeType,
		Size: int64(len(data)), Data: data,
	}
	metadata, err := item.Validate()
	if err != nil {
		if errors.Is(err, attachment.ErrUnsupportedImage) {
			return nil, attachment.ErrUnsupportedImage
		}
		return nil, fmt.Errorf("attachment download failed")
	}
	item.Type, item.MIMEType = metadata.Type, metadata.MIMEType
	return []attachment.Incoming{item}, nil
}

func (a *adapter) downloadFile(ctx context.Context, downloadURL string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("attachment size limit is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("attachment download failed")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("attachment download failed")
	}
	if resp.Body == nil {
		return nil, fmt.Errorf("attachment download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, fmt.Errorf("attachment download failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("attachment download failed")
	}
	return data, nil
}

func (a *adapter) maxAttachmentBytes() int64 {
	if a.attachmentMaxBytes > 0 && a.attachmentMaxBytes < attachment.MaxBytes {
		return a.attachmentMaxBytes
	}
	return attachment.MaxBytes
}

func telegramFileURL(apiBase, token, filePath string) (string, error) {
	if strings.TrimSpace(apiBase) == "" || strings.TrimSpace(token) == "" || filePath == "" ||
		strings.ContainsAny(filePath, "\\\x00\r\n") || strings.HasPrefix(filePath, "/") {
		return "", fmt.Errorf("invalid Telegram file path")
	}
	clean := path.Clean(filePath)
	if clean != filePath || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("invalid Telegram file path")
	}
	return strings.TrimRight(apiBase, "/") + "/file/bot" + token + "/" + filePath, nil
}
