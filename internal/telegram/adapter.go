package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"goaster/internal/attachment"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type adapter struct {
	token              string
	pairing            *pairing
	client             httpDoer
	agent              httpDoer
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

const (
	telegramTypingRefreshInterval = 4 * time.Second
	telegramTypingActionTimeout   = 2 * time.Second
)

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
				a.send(ctx, m.Chat.ID, "Paired. Send text, an image, or a document to chat.")
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
			a.send(ctx, m.Chat.ID, "GoAster is ready for text messages, images, and documents.")
		} else {
			a.send(ctx, m.Chat.ID, "The remote agent is not ready; check its remote authentication.")
		}
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
	sendFinal := func(text string) {
		stopTyping()
		a.send(ctx, m.Chat.ID, text)
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
	reply, err := a.askAgent(ctx, text, m.Chat.ID, attachments)
	if err != nil {
		if errors.Is(err, attachment.ErrUnsupportedImage) {
			sendFinal("That image format is unsupported. Supported image formats are JPEG, PNG, GIF, and WebP.")
			return
		}
		sendFinal("The remote agent could not answer. Check its remote authentication.")
		return
	}
	// Telegram limits message size. Chunks also handle non-BMP characters.
	runes := []rune(reply)
	for len(runes) > 0 {
		n := len(runes)
		if n > 2000 {
			n = 2000
		}
		sendFinal(string(runes[:n]))
		runes = runes[n:]
	}
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
	_ = a.call(ctx, "sendMessage", struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{userID, text}, nil)
}

func (a *adapter) askAgent(ctx context.Context, text string, chatID int64, attachments []attachment.Incoming) (string, error) {
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
	resp, err := a.agent.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnsupportedMediaType {
		return "", attachment.ErrUnsupportedImage
	}
	var result struct {
		Reply string `json:"reply"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent request failed")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result); err != nil {
		return "", fmt.Errorf("agent request failed: %w", err)
	}
	if strings.TrimSpace(result.Reply) == "" {
		return "", fmt.Errorf("agent request failed")
	}
	return result.Reply, nil
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
