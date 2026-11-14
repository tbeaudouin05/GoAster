package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
	"goaster/internal/localenv"
	"goaster/internal/migration"
	"goaster/internal/telegramcommands"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type adapter struct {
	token              string
	pairing            *pairing
	pairingMu          sync.Mutex
	turnMu             sync.Mutex
	activeTurns        map[telegramTurnKey][]telegramActiveTurn
	nextTurnID         uint64
	environmentMu      sync.Mutex
	environmentBlocked map[environmentProposalKey]bool
	client             httpDoer
	agent              httpDoer
	controlSecret      string
	apiBase            string
	attachmentMaxBytes int64 // test seam; zero uses attachment.MaxBytes.
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type telegramBotCommand = telegramcommands.MenuCommand

type message struct {
	Text            string      `json:"text"`
	Caption         string      `json:"caption"`
	Photo           []photoSize `json:"photo"`
	Document        *document   `json:"document"`
	MediaGroupID    string      `json:"media_group_id"`
	MessageThreadID int64       `json:"message_thread_id"`
	Chat            struct {
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

type environmentProposalKey struct {
	userID int64
	chatID int64
}

type telegramTurnKey struct {
	chatID   int64
	threadID int64
}

type telegramActiveTurn struct {
	id     uint64
	userID int64
	cancel context.CancelFunc
}

type telegramFile struct {
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type telegramFormattingError struct{}

func (telegramFormattingError) Error() string { return "Telegram rejected message formatting" }

type telegramAPIError struct {
	method      string
	status      string
	errorCode   int
	description string
}

func (err telegramAPIError) Error() string {
	status := strings.Join(strings.Fields(err.status), " ")
	if status == "" {
		status = "unknown status"
	}
	if err.errorCode != 0 {
		return fmt.Sprintf("Telegram %s failed with status %s (error_code=%d)", err.method, status, err.errorCode)
	}
	return fmt.Sprintf("Telegram %s failed with status %s", err.method, status)
}

func (err telegramAPIError) DiagnosticFields() []diagnostics.Field {
	return []diagnostics.Field{{Key: "backend_message", Value: diagnostics.RemoteMessage(err.description)}}
}

// Keep startup messages safe for callers while preserving errors.Is/As causes.
type telegramStartupError struct {
	message string
	cause   error
}

func (err telegramStartupError) Error() string { return err.message }
func (err telegramStartupError) Unwrap() error { return err.cause }

func telegramFailure(method string, err error, status string, errorCode int, requestID string) error {
	if err == nil {
		return nil
	}
	fields := []diagnostics.Field{{Key: "method", Value: method}}
	if strings.TrimSpace(status) != "" {
		fields = append(fields, diagnostics.Field{Key: "status", Value: status})
	}
	if errorCode != 0 {
		fields = append(fields, diagnostics.Field{Key: "backend_code", Value: strconv.Itoa(errorCode)})
	}
	if strings.TrimSpace(requestID) != "" {
		fields = append(fields, diagnostics.Field{Key: "request_id", Value: requestID})
	}
	diagnostics.Error("telegram.api", err, fields...)
	return err
}

type agentModelError struct {
	message string
}

func (err agentModelError) Error() string { return err.message }

const (
	telegramToolFeedbackMaxChars       = 600
	telegramTypingRefreshInterval      = 4 * time.Second
	telegramTypingActionTimeout        = 2 * time.Second
	telegramAgentRequestTimeout        = 20 * time.Minute
	telegramControlAuthorizationHeader = "X-GoAster-GitHub-Auth-Control"
	telegramPairCommand                = telegramcommands.PairCommand
	telegramStartCommand               = telegramcommands.StartCommand
	telegramHelpCommand                = telegramcommands.HelpCommand
	telegramStopCommand                = telegramcommands.StopCommand
	telegramEnvironmentCommand         = telegramcommands.EnvironmentCommand
	telegramGetEnvironmentCommand      = telegramcommands.GetEnvironmentCommand
	telegramEnvironmentAcceptKeyword   = "ACCEPT"
	telegramEnvironmentFilename        = "goaster.env"
)

const telegramEnvironmentInstructions = "To update settings, attach a document named goaster.env to /env in the same message. Never paste secret values into chat."

const telegramCommandRegistrationTimeout = 5 * time.Second

var telegramBotCommands = telegramcommands.Menu()
var telegramHelpText = telegramcommands.HelpText()

const telegramStopMessage = "Stopped the current agent turn."
const telegramNoActiveTurnMessage = "No agent turn is running."

// Run starts polling only after the token and pairing state are protected.
// ready is called after the agent process is available and Telegram accepts
// an initial getUpdates request; /clear and /m remain usable while model
// authentication is unavailable.
func Run(ctx context.Context, paths Paths, ready func() error) error {
	controlSecret, err := controlsecret.Load()
	if err != nil {
		diagnostics.Error("telegram.startup.control_secret", err)
		return err
	}
	agent, err := newAgentClient()
	if err != nil {
		diagnostics.Error("telegram.startup.agent_client", err)
		return err
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		diagnostics.Error("telegram.startup.token", err)
		return telegramStartupError{message: "Telegram token file is invalid", cause: err}
	}
	if err := ValidateToken(string(token)); err != nil {
		diagnostics.Error("telegram.startup.token", err)
		return telegramStartupError{message: "Telegram token file is invalid", cause: err}
	}
	pairing, err := loadPairing(paths)
	if err != nil {
		diagnostics.Error("telegram.startup.pairing", err)
		return telegramStartupError{message: "Telegram pairing state is invalid", cause: err}
	}
	a := &adapter{
		token: string(token), pairing: pairing,
		controlSecret: controlSecret,
		client:        &http.Client{Timeout: 40 * time.Second, CheckRedirect: noRedirect},
		agent:         agent,
		apiBase:       "https://api.telegram.org",
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
		return telegramStartupError{message: "Telegram polling could not start; check the bot token, connection and webhook settings", cause: err}
	}
	if !a.agentEndpointAvailable(ctx, "/livez") {
		return fmt.Errorf("remote agent is unavailable")
	}
	a.registerTelegramCommands(ctx)
	if ready != nil {
		if err := ready(); err != nil {
			diagnostics.Error("telegram.ready", err)
			return telegramStartupError{message: "could not report Telegram service readiness", cause: err}
		}
	}
	var offset int64
	var handlers sync.WaitGroup
	tails := make(map[int64]chan struct{})
	epochs := make(map[int64]context.Context)
	epochCancels := make(map[int64]context.CancelFunc)
	defer func() {
		for _, cancel := range epochCancels {
			cancel()
		}
	}()
	defer handlers.Wait()
	for {
		for _, item := range updates {
			if ctx.Err() != nil {
				return nil
			}
			if item.ID < offset {
				continue
			}
			if item.Message != nil {
				m := item.Message
				started := make(chan struct{})
				handlerCtx := context.WithValue(ctx, telegramStartedKey{}, started)
				text := m.Text
				if text == "" && m.hasAttachment() {
					text = m.Caption
				}
				text = strings.TrimSpace(text)
				isStop := text == telegramStopCommand
				isClear := text == "/clear" && !m.hasAttachment() && a.authorizedClear(m)
				if isClear {
					if cancel := epochCancels[m.Chat.ID]; cancel != nil {
						cancel()
					}
					epochs[m.Chat.ID], epochCancels[m.Chat.ID] = context.WithCancel(ctx)
					handlerCtx = context.WithValue(handlerCtx, telegramOrderedClearKey{}, true)
				}
				isControl := isStop || !isClear && (chat.IsClearCommand(text) || chat.IsModelCommand(text))
				if !isControl && !isClear {
					if epochs[m.Chat.ID] == nil {
						epochs[m.Chat.ID], epochCancels[m.Chat.ID] = context.WithCancel(ctx)
					}
					handlerCtx = context.WithValue(epochs[m.Chat.ID], telegramStartedKey{}, started)
				}
				var previous chan struct{}
				var done chan struct{}
				if !isControl {
					previous = tails[m.Chat.ID]
					done = make(chan struct{})
					tails[m.Chat.ID] = done
				}
				waitStarted := isStop || !isControl && previous == nil
				if previous != nil {
					select {
					case <-previous:
						waitStarted = true
					default:
					}
				}
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					if done != nil {
						defer close(done)
					}
					if previous != nil {
						select {
						case <-previous:
						case <-ctx.Done():
							return
						}
					}
					if handlerCtx.Err() != nil {
						close(started)
						return
					}
					a.handle(handlerCtx, m)
				}()
				if waitStarted {
					select {
					case <-started:
					case <-ctx.Done():
						return nil
					}
				}
			}
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
	if a == nil || a.agent == nil {
		telegramFailure("agent probe", errors.New("remote agent client is unavailable"), "", 0, "")
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8765"+endpoint, nil)
	if err != nil {
		telegramFailure("agent probe", fmt.Errorf("build request for %s: %w", endpoint, err), "", 0, "")
		return false
	}
	resp, err := a.agent.Do(req)
	if err != nil {
		telegramFailure("agent probe", fmt.Errorf("request %s: %w", endpoint, err), "", 0, "")
		return false
	}
	if resp == nil || resp.Body == nil {
		telegramFailure("agent probe", fmt.Errorf("request %s returned no response body", endpoint), "", 0, "")
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 33))
	if err != nil {
		telegramFailure("agent probe", fmt.Errorf("read response from %s: %w", endpoint, err), resp.Status, 0, resp.Header.Get("x-request-id"))
		return false
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		telegramFailure("agent probe", fmt.Errorf("request %s returned an unavailable response", endpoint), resp.Status, 0, resp.Header.Get("x-request-id"))
		return false
	}
	return true
}

func (a *adapter) getUpdates(ctx context.Context, offset int64, timeout int) ([]update, error) {
	var updates []update
	err := a.call(ctx, "getUpdates", struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, timeout, []string{"message"}}, &updates)
	if err == nil && updates == nil {
		return nil, telegramFailure("getUpdates", errors.New("Telegram response contained no update list"), "200 OK", 0, "")
	}
	return updates, err
}

func (a *adapter) registerTelegramCommands(ctx context.Context) {
	registrationCtx, cancel := context.WithTimeout(ctx, telegramCommandRegistrationTimeout)
	defer cancel()
	if err := a.call(registrationCtx, "setMyCommands", struct {
		Commands []telegramBotCommand `json:"commands"`
	}{Commands: telegramBotCommands}, nil); err != nil {
		return
	}
}

func (a *adapter) call(ctx context.Context, method string, payload any, result any) error {
	if a == nil || a.client == nil {
		return telegramFailure(method, errors.New("Telegram client is unavailable"), "", 0, "")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return telegramFailure(method, fmt.Errorf("encode Telegram request: %w", err), "", 0, "")
	}
	// This URL must never be included in an error or log because it contains the token.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBase+"/bot"+a.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return telegramFailure(method, fmt.Errorf("build Telegram request: %w", err), "", 0, "")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return telegramFailure(method, fmt.Errorf("Telegram transport: %w", err), "", 0, "")
	}
	if resp == nil || resp.Body == nil {
		return telegramFailure(method, errors.New("Telegram response body is missing"), "", 0, "")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if readErr != nil {
		return telegramFailure(method, fmt.Errorf("read Telegram response: %w", readErr), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	if len(body) > 4*1024*1024 {
		return telegramFailure(method, errors.New("Telegram response body exceeds the limit"), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	decodeErr := json.Unmarshal(body, &envelope)
	if isTelegramFormattingRejection(envelope.Description) {
		return telegramFailure(method, telegramFormattingError{}, resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	if decodeErr != nil || !envelope.OK || resp.StatusCode != http.StatusOK {
		if decodeErr != nil {
			return telegramFailure(method, fmt.Errorf("decode Telegram response: %w", decodeErr), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
		}
		return telegramFailure(method, telegramAPIError{method: method, status: resp.Status, errorCode: envelope.ErrorCode, description: envelope.Description}, resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return telegramFailure(method, fmt.Errorf("decode Telegram result: %w", err), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
		}
	}
	return nil
}

func pairingCommandCode(text string) (string, bool) {
	prefix := telegramPairCommand + " "
	if strings.HasPrefix(text, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(text, prefix)), true
	}
	return "", false
}

func telegramTurnKeyForMessage(m message) telegramTurnKey {
	return telegramTurnKey{chatID: m.Chat.ID, threadID: m.MessageThreadID}
}

func (a *adapter) authorizedClear(m *message) bool {
	if m.From == nil || m.From.IsBot || m.From.ID <= 0 || m.Chat.ID == 0 || m.MediaGroupID != "" || len(m.SenderChat) != 0 && string(m.SenderChat) != "null" {
		return false
	}
	if m.Chat.Type != "private" && m.Chat.Type != "group" && m.Chat.Type != "supergroup" || m.Chat.Type == "private" && m.From.ID != m.Chat.ID {
		return false
	}
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	return a.pairing.userID != 0 && a.pairing.userID == m.From.ID
}

type telegramOrderedClearKey struct{}
type telegramStartedKey struct{}
type telegramThreadKey struct{}

func (a *adapter) handle(ctx context.Context, m *message) {
	started, _ := ctx.Value(telegramStartedKey{}).(chan struct{})
	signalStarted := func() {
		if started != nil {
			close(started)
			started = nil
		}
	}
	defer signalStarted()
	if m != nil {
		ctx = context.WithValue(ctx, telegramThreadKey{}, m.MessageThreadID)
	}
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID <= 0 || m.Chat.ID == 0 || len(m.SenderChat) != 0 && string(m.SenderChat) != "null" {
		return
	}
	if m.MediaGroupID != "" && !isTelegramEnvironmentUpdate(*m) {
		return
	}
	if m.Chat.Type != "private" && m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		return
	}
	if m.Chat.Type == "private" && m.From.ID != m.Chat.ID {
		return
	}
	hasAttachment := m.hasAttachment()
	rawText := m.Text
	if rawText == "" && hasAttachment {
		rawText = m.Caption
	}
	text := strings.TrimSpace(rawText)
	if isTelegramEnvironmentCommandMention(text) {
		text = telegramEnvironmentCommand
	}
	if isTelegramGetEnvironmentCommand(text) {
		text = telegramGetEnvironmentCommand
	}
	a.pairingMu.Lock()
	pairedUserID := a.pairing.userID
	paired := false
	if pairedUserID == 0 && m.Chat.Type == "private" {
		if code, recognized := pairingCommandCode(text); recognized {
			paired = a.pairing.tryPair(m.From.ID, code)
		}
	}
	a.pairingMu.Unlock()
	if pairedUserID == 0 {
		signalStarted()
		if m.Chat.Type != "private" {
			return
		}
		if isTelegramCatalogCommand(text, telegramHelpCommand) {
			a.send(ctx, m.Chat.ID, telegramHelpText)
			return
		}
		if text == telegramPairCommand || text == telegramStartCommand {
			a.send(ctx, m.Chat.ID, "Send /pair followed by the one-time pairing code shown in GoAster.")
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
	if !hasAttachment && rawText == text {
		if code, approved := migration.ApprovalCode(text); approved {
			signalStarted()
			if m.Chat.Type != "private" {
				return
			}
			status, err := submitMigrationApproval(ctx, code, m.Chat.ID, m.From.ID, a.agent, a.controlSecret)
			if err != nil {
				if ctx.Err() == nil {
					a.send(ctx, m.Chat.ID, "The migration approval could not be recorded.")
				}
				return
			}
			if status == "approved" {
				a.send(ctx, m.Chat.ID, "Migration approval recorded for the exact terms shown above.")
			} else {
				a.send(ctx, m.Chat.ID, "No matching migration approval is pending.")
			}
			return
		}
	}
	if chat.IsClearCommand(text) && text == "/clear" && !hasAttachment && ctx.Value(telegramOrderedClearKey{}) == nil {
		a.turnMu.Lock()
		for key, turns := range a.activeTurns {
			if key.chatID == m.Chat.ID {
				for _, turn := range turns {
					turn.cancel()
				}
			}
		}
		a.turnMu.Unlock()
	}
	if isTelegramCatalogCommand(text, telegramHelpCommand) {
		signalStarted()
		a.send(ctx, m.Chat.ID, telegramHelpText)
		return
	}
	if strings.HasPrefix(text, telegramStopCommand+"@") || strings.HasPrefix(text, telegramHelpCommand+"@") {
		return
	}
	if isTelegramCatalogCommand(text, telegramStopCommand) {
		key := telegramTurnKeyForMessage(*m)
		stopped := false
		a.turnMu.Lock()
		turns := a.activeTurns[key]
		for index, turn := range turns {
			if turn.userID == m.From.ID {
				turns = append(turns[:index], turns[index+1:]...)
				if len(turns) == 0 {
					delete(a.activeTurns, key)
				} else {
					a.activeTurns[key] = turns
				}
				turn.cancel()
				stopped = true
				break
			}
		}
		a.turnMu.Unlock()
		signalStarted()
		if stopped {
			a.send(ctx, m.Chat.ID, telegramStopMessage)
		} else {
			a.send(ctx, m.Chat.ID, telegramNoActiveTurnMessage)
		}
		return
	}
	turnCtx, cancelTurn := context.WithCancel(ctx)
	key := telegramTurnKeyForMessage(*m)
	a.turnMu.Lock()
	if a.activeTurns == nil {
		a.activeTurns = make(map[telegramTurnKey][]telegramActiveTurn)
	}
	a.nextTurnID++
	turnID := a.nextTurnID
	a.activeTurns[key] = append(a.activeTurns[key], telegramActiveTurn{id: turnID, userID: m.From.ID, cancel: cancelTurn})
	a.turnMu.Unlock()
	defer func() {
		a.turnMu.Lock()
		turns := a.activeTurns[key]
		for index, turn := range turns {
			if turn.id == turnID {
				turns = append(turns[:index], turns[index+1:]...)
				if len(turns) == 0 {
					delete(a.activeTurns, key)
				} else {
					a.activeTurns[key] = turns
				}
				break
			}
		}
		a.turnMu.Unlock()
		cancelTurn()
	}()
	signalStarted()
	ctx = turnCtx
	if m.MediaGroupID != "" {
		if m.Chat.Type == "private" && isTelegramEnvironmentCommandVariant(text) {
			proposalKey := environmentProposalKey{userID: m.From.ID, chatID: m.Chat.ID}
			a.environmentMu.Lock()
			a.environmentBlocked = invalidateEnvironmentProposalLocked(ctx, proposalKey, a.agent, a.controlSecret, a.environmentBlocked)
			a.environmentMu.Unlock()
		}
		return
	}
	if text == telegramPairCommand || strings.HasPrefix(text, telegramPairCommand+" ") ||
		text == telegramStartCommand || strings.HasPrefix(text, telegramStartCommand+" ") {
		if a.agentEndpointAvailable(ctx, "/healthz") {
			a.send(ctx, m.Chat.ID, "GoAster is ready for text messages, images, and documents. Use /m to view the model, /m MODEL_OR_ALIAS to select one, and /clear to clear this chat's history.")
		} else {
			a.send(ctx, m.Chat.ID, "The remote agent is not ready; check its remote authentication. You can still use /m to view or select a model and /clear to clear this chat's history.")
		}
		return
	}
	if text == telegramGetEnvironmentCommand {
		if m.Chat.Type != "private" {
			a.send(ctx, m.Chat.ID, "Environment files are available only in a private chat.")
			return
		}
		data, err := localenv.ReadSavedFile()
		if err != nil {
			diagnostics.Error("telegram.environment.read", err)
			a.send(ctx, m.Chat.ID, "The environment file is unavailable.")
			return
		}
		if err := sendDocument(ctx, a.client, a.apiBase, a.token, m.Chat.ID, telegramEnvironmentFilename, data); err != nil {
			a.send(ctx, m.Chat.ID, "The environment file could not be sent.")
		}
		return
	}
	if isTelegramEnvironmentDocument(*m) && !isTelegramEnvironmentMessage(text) {
		if m.Chat.Type != "private" {
			a.send(ctx, m.Chat.ID, "Environment updates are available only in a private chat.")
			return
		}
		if !isTelegramEnvironmentUploadCommand(*m, text) {
			a.send(ctx, m.Chat.ID, telegramEnvironmentInstructions)
			return
		}
	}
	if isTelegramEnvironmentMessage(text) {
		if m.Chat.Type != "private" {
			a.send(ctx, m.Chat.ID, "Environment updates are available only in a private chat.")
			return
		}
		a.environmentMu.Lock()
		defer a.environmentMu.Unlock()
		proposalKey := environmentProposalKey{userID: m.From.ID, chatID: m.Chat.ID}
		invalidateProposal := func() {
			a.environmentBlocked = invalidateEnvironmentProposalLocked(ctx, proposalKey, a.agent, a.controlSecret, a.environmentBlocked)
		}
		if text == telegramEnvironmentAcceptKeyword {
			if rawText != telegramEnvironmentAcceptKeyword {
				a.send(ctx, m.Chat.ID, "Reply exactly ACCEPT as a text message after reviewing the environment preview.")
				return
			}
			if hasAttachment {
				a.send(ctx, m.Chat.ID, "Reply exactly ACCEPT as a text message after reviewing the environment preview.")
				return
			}
			if a.environmentBlocked[proposalKey] {
				invalidateProposal()
				a.send(ctx, m.Chat.ID, "No environment proposal is pending. Send /env with goaster.env attached in the same message.")
				return
			}
			reply, err := askAgentWithUser(ctx, telegramEnvironmentCommand+" "+telegramEnvironmentAcceptKeyword, m.Chat.ID, m.From.ID, nil, nil, a.agent, a.controlSecret)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				a.send(ctx, m.Chat.ID, "The environment update could not be completed.")
				return
			}
			if ctx.Err() != nil {
				return
			}
			for _, chunk := range splitTelegramMarkdown(reply, 2000) {
				a.send(ctx, m.Chat.ID, chunk)
			}
			return
		}
		if isTelegramEnvironmentAcceptanceVariant(text) {
			a.send(ctx, m.Chat.ID, "Reply exactly ACCEPT as a text message after reviewing the environment preview.")
			return
		}
		if text != telegramEnvironmentCommand {
			invalidateProposal()
			a.send(ctx, m.Chat.ID, telegramEnvironmentInstructions)
			return
		}
		if len(m.Photo) > 0 || m.Document == nil {
			invalidateProposal()
			a.send(ctx, m.Chat.ID, telegramEnvironmentInstructions)
			return
		}
		if strings.TrimSpace(m.Document.FileName) != telegramEnvironmentFilename {
			invalidateProposal()
			a.send(ctx, m.Chat.ID, "Attach a document named goaster.env with /env in the same message.")
			return
		}
		if m.Document.FileSize < 0 || m.Document.FileSize > int64(localenv.MaxFileBytes) {
			invalidateProposal()
			a.send(ctx, m.Chat.ID, "The goaster.env document exceeds the 1 MiB upload limit.")
			return
		}
		stopTyping := a.startTyping(ctx, m.Chat.ID)
		attachments, err := a.downloadAttachments(ctx, m)
		if err != nil {
			stopTyping()
			invalidateProposal()
			if errors.Is(err, attachment.ErrUnsupportedImage) {
				a.send(ctx, m.Chat.ID, "The goaster.env document must be a text document, not an image.")
				return
			}
			a.send(ctx, m.Chat.ID, "The goaster.env document could not be downloaded safely.")
			return
		}
		if len(attachments) != 1 || int64(len(attachments[0].Data)) > int64(localenv.MaxFileBytes) {
			stopTyping()
			invalidateProposal()
			a.send(ctx, m.Chat.ID, "The goaster.env document exceeds the 1 MiB upload limit.")
			return
		}
		reply, err := askAgentWithUser(ctx, telegramEnvironmentCommand, m.Chat.ID, m.From.ID, attachments, nil, a.agent, a.controlSecret)
		stopTyping()
		if err != nil {
			invalidateProposal()
			if ctx.Err() != nil {
				return
			}
			a.send(ctx, m.Chat.ID, "The environment file could not be reviewed safely.")
			return
		}
		if ctx.Err() != nil {
			invalidateProposal()
			return
		}
		chunks := splitTelegramMarkdown(reply, 2000)
		if len(chunks) == 0 {
			invalidateProposal()
			return
		}
		for _, chunk := range chunks {
			if err := a.send(ctx, m.Chat.ID, chunk); err != nil {
				// A proposal is actionable in the agent as soon as the preview
				// response is produced. If Telegram does not deliver every
				// preview chunk, remove it before a later ACCEPT can apply
				// changes the user may not have seen.
				invalidateProposal()
				return
			}
		}
		if a.environmentBlocked != nil {
			delete(a.environmentBlocked, proposalKey)
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
			if ctx.Err() != nil {
				return
			}
			a.send(ctx, m.Chat.ID, "Conversation history could not be cleared.")
			return
		}
		if ctx.Err() != nil {
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
			if ctx.Err() != nil {
				return
			}
			a.send(ctx, m.Chat.ID, "Model selection could not be completed.")
			return
		}
		if ctx.Err() != nil {
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
		if ctx.Err() != nil {
			return
		}
		text := toolFeedbackText(name, arguments)
		if progressMessageID == 0 {
			progressMessageID, _ = a.sendMessage(ctx, m.Chat.ID, text)
			return
		}
		_ = a.editMessage(ctx, m.Chat.ID, progressMessageID, text)
	}
	sendFinal := func(text string) {
		stopTyping()
		if ctx.Err() != nil {
			return
		}
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
	reply, err := askAgentWithIdentity(ctx, text, m.Chat.ID, m.From.ID, m.Chat.Type == "private", attachments, showToolCall, a.agent, a.controlSecret)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
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

func invalidateEnvironmentProposalLocked(ctx context.Context, proposalKey environmentProposalKey, agent httpDoer, controlSecret string, environmentBlocked map[environmentProposalKey]bool) map[environmentProposalKey]bool {
	_, err := askAgentWithUser(ctx, telegramEnvironmentCommand, proposalKey.chatID, proposalKey.userID, nil, nil, agent, controlSecret)
	if environmentBlocked == nil {
		environmentBlocked = make(map[environmentProposalKey]bool)
	}
	environmentBlocked[proposalKey] = err != nil
	return environmentBlocked
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

func telegramThreadID(ctx context.Context) int64 {
	id, _ := ctx.Value(telegramThreadKey{}).(int64)
	return id
}

func (a *adapter) sendChatAction(ctx context.Context, chatID int64) {
	actionCtx, cancel := context.WithTimeout(ctx, telegramTypingActionTimeout)
	defer cancel()
	_ = a.call(actionCtx, "sendChatAction", struct {
		ChatID   int64  `json:"chat_id"`
		ThreadID int64  `json:"message_thread_id,omitempty"`
		Action   string `json:"action"`
	}{chatID, telegramThreadID(ctx), "typing"}, nil)
}

func (a *adapter) send(ctx context.Context, userID int64, text string) error {
	_, err := a.sendMessage(ctx, userID, text)
	return err
}

func (a *adapter) sendMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	payload := struct {
		ChatID    int64  `json:"chat_id"`
		ThreadID  int64  `json:"message_thread_id,omitempty"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{ChatID: chatID, ThreadID: telegramThreadID(ctx), Text: markdownToTelegramHTML(text), ParseMode: "HTML"}
	err := a.call(ctx, "sendMessage", payload, &result)
	if isTelegramFormattingError(err) {
		payload.Text = text
		payload.ParseMode = ""
		err = a.call(ctx, "sendMessage", payload, &result)
	}
	return result.MessageID, err
}

func sendDocument(ctx context.Context, client httpDoer, apiBase, token string, chatID int64, filename string, data []byte) error {
	if client == nil {
		return telegramFailure("sendDocument", errors.New("Telegram client is unavailable"), "", 0, "")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return telegramFailure("sendDocument", fmt.Errorf("encode chat ID: %w", err), "", 0, "")
	}
	if threadID := telegramThreadID(ctx); threadID != 0 {
		if err := writer.WriteField("message_thread_id", strconv.FormatInt(threadID, 10)); err != nil {
			return telegramFailure("sendDocument", fmt.Errorf("encode thread ID: %w", err), "", 0, "")
		}
	}
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return telegramFailure("sendDocument", fmt.Errorf("create document part: %w", err), "", 0, "")
	}
	if _, err := part.Write(data); err != nil {
		return telegramFailure("sendDocument", fmt.Errorf("write document part: %w", err), "", 0, "")
	}
	if err := writer.Close(); err != nil {
		return telegramFailure("sendDocument", fmt.Errorf("close multipart request: %w", err), "", 0, "")
	}
	return callMultipart(ctx, client, apiBase, token, "sendDocument", writer.FormDataContentType(), body.Bytes())
}

func callMultipart(ctx context.Context, client httpDoer, apiBase, token, method, contentType string, body []byte) error {
	if client == nil {
		return telegramFailure(method, errors.New("Telegram client is unavailable"), "", 0, "")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return telegramFailure(method, fmt.Errorf("build Telegram request: %w", err), "", 0, "")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		return telegramFailure(method, fmt.Errorf("Telegram transport: %w", err), "", 0, "")
	}
	if resp == nil || resp.Body == nil {
		return telegramFailure(method, errors.New("Telegram response body is missing"), "", 0, "")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if readErr != nil {
		return telegramFailure(method, fmt.Errorf("read Telegram response: %w", readErr), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	if len(body) > 4*1024*1024 {
		return telegramFailure(method, errors.New("Telegram response body exceeds the limit"), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	decodeErr := json.Unmarshal(body, &envelope)
	if isTelegramFormattingRejection(envelope.Description) {
		return telegramFailure(method, telegramFormattingError{}, resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	if decodeErr != nil || !envelope.OK || resp.StatusCode != http.StatusOK {
		if decodeErr != nil {
			return telegramFailure(method, fmt.Errorf("decode Telegram response: %w", decodeErr), resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
		}
		return telegramFailure(method, telegramAPIError{method: method, status: resp.Status, errorCode: envelope.ErrorCode, description: envelope.Description}, resp.Status, envelope.ErrorCode, resp.Header.Get("x-request-id"))
	}
	return nil
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

func (a *adapter) askAgent(ctx context.Context, text string, chatID int64, attachments []attachment.Incoming, onToolCall func(string, string)) (string, error) {
	return askAgentWithIdentity(ctx, text, chatID, 0, false, attachments, onToolCall, a.agent, a.controlSecret)
}

func askAgentWithUser(ctx context.Context, text string, chatID, userID int64, attachments []attachment.Incoming, onToolCall func(string, string), agent httpDoer, controlSecret string) (string, error) {
	return askAgentWithIdentity(ctx, text, chatID, userID, false, attachments, onToolCall, agent, controlSecret)
}

func askAgentWithIdentity(ctx context.Context, text string, chatID, userID int64, private bool, attachments []attachment.Incoming, onToolCall func(string, string), agent httpDoer, controlSecret string) (string, error) {
	if agent == nil {
		return "", telegramAgentFailure(errors.New("remote agent client is unavailable"), "", 0, "")
	}
	body, err := json.Marshal(struct {
		Message     string                `json:"message"`
		ChatID      int64                 `json:"chat_id"`
		UserID      int64                 `json:"user_id,omitempty"`
		Private     bool                  `json:"private,omitempty"`
		Attachments []attachment.Incoming `json:"attachments,omitempty"`
	}{Message: text, ChatID: chatID, UserID: userID, Private: private, Attachments: attachments})
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("encode agent request: %w", err), "", 0, "")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8765/chat", bytes.NewReader(body))
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("build agent request: %w", err), "", 0, "")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(telegramControlAuthorizationHeader, controlSecret)
	resp, err := agent.Do(req)
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("agent transport: %w", err), "", 0, "")
	}
	if resp == nil || resp.Body == nil {
		return "", telegramAgentFailure(errors.New("agent response body is missing"), "", 0, "")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnsupportedMediaType {
		return "", attachment.ErrUnsupportedImage
	}
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return "", telegramAgentFailure(fmt.Errorf("read agent error response: %w", readErr), resp.Status, 0, resp.Header.Get("x-request-id"))
		}
		if resp.Header.Get("X-GoAster-Error") == "post-tool-authentication" {
			message := strings.TrimSuffix(string(body), "\n")
			if chat.IsPostToolAuthenticationMessage(message) {
				return "", agentModelError{message: message}
			}
			return "", telegramAgentFailure(fmt.Errorf("agent returned an invalid post-tool authentication error"), resp.Status, 0, resp.Header.Get("x-request-id"))
		}
		message := strings.TrimSpace(string(body))
		if resp.Header.Get("X-GoAster-Error") == "skill-recovery" && chat.IsSkillRecoveryMessage(message) {
			return "", agentModelError{message: message}
		}
		if strings.HasPrefix(message, "model ") {
			return "", agentModelError{message: truncateRunes(message, 512)}
		}
		return "", telegramAgentFailure(fmt.Errorf("agent returned status %s", resp.Status), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4*1024*1024))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var reply string
	var modelError string
	for scanner.Scan() {
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
		if err := json.Unmarshal(scanner.Bytes(), &toolEvent); err != nil {
			return "", telegramAgentFailure(fmt.Errorf("decode agent stream event: %w", err), resp.Status, 0, resp.Header.Get("x-request-id"))
		}
		if err := json.Unmarshal(scanner.Bytes(), &replyEvent); err != nil {
			return "", telegramAgentFailure(fmt.Errorf("decode agent stream event: %w", err), resp.Status, 0, resp.Header.Get("x-request-id"))
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
		}
	}
	if err := scanner.Err(); err != nil {
		return "", telegramAgentFailure(fmt.Errorf("scan agent response: %w", err), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	if strings.TrimSpace(reply) == "" {
		if modelError != "" {
			return "", agentModelError{message: modelError}
		}
		return "", telegramAgentFailure(errors.New("agent response contained no final reply"), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	return reply, nil
}

func submitMigrationApproval(ctx context.Context, code string, chatID, userID int64, agent httpDoer, controlSecret string) (string, error) {
	if agent == nil {
		return "", telegramAgentFailure(errors.New("remote agent client is unavailable"), "", 0, "")
	}
	body, err := json.Marshal(struct {
		Code    string `json:"code"`
		ChatID  int64  `json:"chat_id"`
		UserID  int64  `json:"user_id"`
		Private bool   `json:"private"`
	}{Code: code, ChatID: chatID, UserID: userID, Private: true})
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("encode migration approval request: %w", err), "", 0, "")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8765/migration/approve", bytes.NewReader(body))
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("build migration approval request: %w", err), "", 0, "")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(telegramControlAuthorizationHeader, controlSecret)
	response, err := agent.Do(request)
	if err != nil {
		return "", telegramAgentFailure(fmt.Errorf("migration approval transport: %w", err), "", 0, "")
	}
	if response == nil || response.Body == nil {
		return "", telegramAgentFailure(errors.New("migration approval response body is missing"), "", 0, "")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", telegramAgentFailure(fmt.Errorf("migration approval returned status %s", response.Status), response.Status, 0, response.Header.Get("x-request-id"))
	}
	var result struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return "", telegramAgentFailure(fmt.Errorf("decode migration approval response: %w", err), response.Status, 0, response.Header.Get("x-request-id"))
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", telegramAgentFailure(errors.New("migration approval response contains trailing data"), response.Status, 0, response.Header.Get("x-request-id"))
	}
	if result.Status != "approved" && result.Status != "rejected" {
		return "", telegramAgentFailure(errors.New("migration approval response is invalid"), response.Status, 0, response.Header.Get("x-request-id"))
	}
	return result.Status, nil
}

func telegramAgentFailure(err error, status string, errorCode int, requestID string) error {
	if err == nil {
		return nil
	}
	fields := []diagnostics.Field{}
	if strings.TrimSpace(status) != "" {
		fields = append(fields, diagnostics.Field{Key: "status", Value: status})
	}
	if errorCode != 0 {
		fields = append(fields, diagnostics.Field{Key: "backend_code", Value: strconv.Itoa(errorCode)})
	}
	if strings.TrimSpace(requestID) != "" {
		fields = append(fields, diagnostics.Field{Key: "request_id", Value: requestID})
	}
	diagnostics.Error("telegram.agent.chat", err, fields...)
	return err
}

func isTelegramEnvironmentMessage(text string) bool {
	return isTelegramEnvironmentCommandVariant(text) || isTelegramEnvironmentAcceptanceVariant(text)
}

func isTelegramGetEnvironmentCommand(text string) bool {
	return isTelegramCommandOrMention(text, telegramGetEnvironmentCommand)
}

func isTelegramCatalogCommand(text, spelling string) bool {
	command, ok := telegramcommands.Dispatch(text)
	if !ok || text != spelling {
		return false
	}
	if "/"+command.Name == spelling {
		return true
	}
	for _, alias := range command.Aliases {
		if "/"+alias == spelling {
			return true
		}
	}
	return false
}

func isTelegramCommandOrMention(text, command string) bool {
	return text == command || isTelegramCommandMention(command, text)
}

func isTelegramEnvironmentDocument(m message) bool {
	return m.Document != nil && attachment.SanitizeFilename(m.Document.FileName) == telegramEnvironmentFilename
}

func isTelegramEnvironmentUploadCommand(m message, text string) bool {
	if !isTelegramEnvironmentCommandExact(text) {
		return false
	}
	if strings.TrimSpace(m.Text) != "" && !isTelegramEnvironmentCommandExact(m.Text) {
		return false
	}
	if strings.TrimSpace(m.Caption) != "" && !isTelegramEnvironmentCommandExact(m.Caption) {
		return false
	}
	return true
}

func isTelegramEnvironmentCommandExact(text string) bool {
	text = strings.TrimSpace(text)
	return text == telegramEnvironmentCommand || isTelegramEnvironmentCommandMention(text)
}

func isTelegramEnvironmentCommandVariant(text string) bool {
	if text == telegramEnvironmentCommand || isTelegramEnvironmentCommandMention(text) {
		return true
	}
	if !strings.HasPrefix(text, telegramEnvironmentCommand) {
		return false
	}
	if strings.HasPrefix(text, telegramEnvironmentCommand+"@") {
		// Treat malformed command mentions as environment-shaped input too,
		// so an attached document cannot enter ordinary chat handling.
		return true
	}
	rest := text[len(telegramEnvironmentCommand):]
	for _, character := range rest {
		return unicode.IsSpace(character)
	}
	return false
}

func isTelegramEnvironmentCommandMention(text string) bool {
	return isTelegramCommandMention(telegramEnvironmentCommand, text)
}

func isTelegramCommandMention(command, text string) bool {
	prefix := command + "@"
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	username := text[len(prefix):]
	if username == "" {
		return false
	}
	for _, character := range username {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func isTelegramEnvironmentAcceptanceVariant(text string) bool {
	if text == telegramEnvironmentAcceptKeyword {
		return true
	}
	if !strings.HasPrefix(text, telegramEnvironmentAcceptKeyword) {
		return false
	}
	rest := text[len(telegramEnvironmentAcceptKeyword):]
	for _, character := range rest {
		return unicode.IsSpace(character)
	}
	return false
}

func isTelegramEnvironmentUpdate(m message) bool {
	rawText := m.Text
	if rawText == "" && m.hasAttachment() {
		rawText = m.Caption
	}
	text := strings.TrimSpace(rawText)
	return isTelegramEnvironmentMessage(text) || isTelegramGetEnvironmentCommand(text)
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
	if err != nil {
		return nil, telegramFailure("downloadAttachment", fmt.Errorf("select attachment: %w", err), "", 0, "")
	}
	if strings.TrimSpace(source.fileID) == "" {
		return nil, telegramFailure("downloadAttachment", errors.New("attachment has no Telegram file ID"), "", 0, "")
	}
	limit := a.maxAttachmentBytes()
	if source.fileSize < 0 || source.fileSize > limit {
		return nil, telegramFailure("downloadAttachment", errors.New("attachment exceeds the size limit"), "", 0, "")
	}

	var file telegramFile
	if err := a.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{source.fileID}, &file); err != nil {
		return nil, fmt.Errorf("get Telegram file: %w", err)
	}
	if file.FilePath == "" || file.FileSize < 0 || file.FileSize > limit {
		return nil, telegramFailure("downloadAttachment", errors.New("Telegram file metadata is invalid or exceeds the size limit"), "", 0, "")
	}
	downloadURL, err := telegramFileURL(a.apiBase, a.token, file.FilePath)
	if err != nil {
		return nil, telegramFailure("downloadAttachment", fmt.Errorf("build Telegram file URL: %w", err), "", 0, "")
	}
	data, err := a.downloadFile(ctx, downloadURL, limit)
	if err != nil {
		return nil, fmt.Errorf("download Telegram attachment: %w", err)
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
		return nil, telegramFailure("downloadAttachment", fmt.Errorf("validate downloaded attachment: %w", err), "", 0, "")
	}
	item.Type, item.MIMEType = metadata.Type, metadata.MIMEType
	return []attachment.Incoming{item}, nil
}

func (a *adapter) downloadFile(ctx context.Context, downloadURL string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, telegramFailure("downloadFile", errors.New("attachment size limit is invalid"), "", 0, "")
	}
	if a == nil || a.client == nil {
		return nil, telegramFailure("downloadFile", errors.New("Telegram client is unavailable"), "", 0, "")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, telegramFailure("downloadFile", fmt.Errorf("build Telegram file request: %w", err), "", 0, "")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, telegramFailure("downloadFile", fmt.Errorf("Telegram file transport: %w", err), "", 0, "")
	}
	if resp == nil {
		return nil, telegramFailure("downloadFile", errors.New("Telegram file response is missing"), "", 0, "")
	}
	if resp.Body == nil {
		return nil, telegramFailure("downloadFile", errors.New("Telegram file response body is missing"), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, telegramFailure("downloadFile", errors.New("Telegram file download returned an unacceptable response"), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, telegramFailure("downloadFile", fmt.Errorf("read Telegram file: %w", err), resp.Status, 0, resp.Header.Get("x-request-id"))
	}
	if int64(len(data)) > limit {
		return nil, telegramFailure("downloadFile", errors.New("Telegram file exceeds the size limit"), resp.Status, 0, resp.Header.Get("x-request-id"))
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
