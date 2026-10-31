package chat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"goaster/internal/attachment"
)

// Providers wrap ErrModelUnavailable when switching models may help.
var ErrModelUnavailable = errors.New("model unavailable")

type requestErrorClass uint8

const (
	requestErrorFatal requestErrorClass = iota
	requestErrorRecoverable
)

var (
	errFatalRequest       = errors.New("fatal request")
	errRecoverableRequest = errors.New("recoverable request")
)

// classifiedRequestError gives the terminal loop a typed boundary without
// changing the underlying provider or persistence error returned to callers.
type classifiedRequestError struct {
	err   error
	class requestErrorClass
}

func (err classifiedRequestError) Error() string {
	return err.err.Error()
}

func (err classifiedRequestError) Unwrap() error {
	return err.err
}

func (err classifiedRequestError) Is(target error) bool {
	switch {
	case err.class == requestErrorFatal && target == errFatalRequest:
		return true
	case err.class == requestErrorRecoverable && target == errRecoverableRequest:
		return true
	default:
		return false
	}
}

func fatalRequestError(err error) error {
	if err == nil || errors.Is(err, errFatalRequest) {
		return err
	}
	return classifiedRequestError{err: err, class: requestErrorFatal}
}

func recoverableRequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errFatalRequest) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fatalRequestError(err)
	}
	if errors.Is(err, errRecoverableRequest) {
		return err
	}
	return classifiedRequestError{err: err, class: requestErrorRecoverable}
}

func classifyRequestError(err error) requestErrorClass {
	if err == nil {
		return requestErrorRecoverable
	}
	if errors.Is(err, errRecoverableRequest) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return requestErrorRecoverable
	}
	return requestErrorFatal
}

type Provider interface {
	Reply(ctx context.Context, model, message string) (string, error)
}

// ChatMessage is a conversation message that can be sent to a history-aware
// provider. Session persistence stores user and assistant messages separately
// from the summary supplied as context.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// IsClearCommand reports whether a message is a /clear command or an attempted
// /clear command with extra arguments. The latter still needs command routing
// so it can be rejected without reaching the provider.
func IsClearCommand(message string) bool {
	fields := strings.Fields(message)
	return len(fields) > 0 && fields[0] == "/clear"
}

// HistoryProvider is optional so existing providers remain compatible with
// the one-message Provider interface.
type HistoryProvider interface {
	ReplyWithHistory(ctx context.Context, model string, history []ChatMessage) (string, error)
}

type conversationSummarizer interface {
	SummarizeConversation(ctx context.Context, model, previousSummary string, messages []ChatMessage) (string, error)
}

type Session struct {
	mu            sync.Mutex
	provider      Provider
	config        Config
	active        string
	reason        string
	history       []ChatMessage
	summary       string
	store         *SessionStore
	storeVersion  [sha256.Size]byte // Expected persisted version for this session.
	memory        *MemoryStore
	startupNotice string
	contextNotice string
}

type ToolCallObserver func(name, arguments string)

type toolCallObserverKey struct{}

func WithToolCallObserver(ctx context.Context, observer ToolCallObserver) context.Context {
	return context.WithValue(ctx, toolCallObserverKey{}, observer)
}

func toolCallObserver(ctx context.Context) ToolCallObserver {
	observer, _ := ctx.Value(toolCallObserverKey{}).(ToolCallObserver)
	return observer
}

type currentTurnAttachmentsKey struct{}

func NewSession(config Config, provider Provider) *Session {
	return NewSessionWithMemory(config, provider, nil)
}

// NewSessionWithMemory adds Markdown memory to normal provider requests while
// keeping it out of conversation history and persisted summaries.
func NewSessionWithMemory(config Config, provider Provider, memory *MemoryStore) *Session {
	return &Session{provider: provider, config: config, active: config.PrimaryModel, memory: memory}
}

func withCurrentTurnAttachments(ctx context.Context, attachments []attachment.Metadata) context.Context {
	return context.WithValue(ctx, currentTurnAttachmentsKey{}, append([]attachment.Metadata(nil), attachments...))
}

func currentTurnAttachments(ctx context.Context) []attachment.Metadata {
	attachments, _ := ctx.Value(currentTurnAttachmentsKey{}).([]attachment.Metadata)
	return attachments
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	return s.replyWithAttachments(ctx, message, nil)
}

// ReplyWithAttachments sends one current-turn message with temporary local
// attachment references. The references are added only to the provider input;
// the persisted conversation keeps the original user text and never retains
// temporary paths.
func (s *Session) ReplyWithAttachments(ctx context.Context, message string, attachments []attachment.Metadata) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fatalRequestError(err)
	}
	if len(attachments) > attachment.MaxAttachments {
		return "", recoverableRequestError(fmt.Errorf("too many attachments"))
	}
	for _, item := range attachments {
		if err := item.ValidateMetadata(); err != nil {
			return "", recoverableRequestError(fmt.Errorf("invalid attachment metadata"))
		}
	}
	return s.replyWithAttachments(ctx, message, attachments)
}

func (s *Session) replyWithAttachments(ctx context.Context, message string, attachments []attachment.Metadata) (string, error) {
	// A session is a single ordered conversation. Serializing its turns also
	// ensures that only one caller summarizes a given history.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextNotice = ""
	if err := ctx.Err(); err != nil {
		return "", fatalRequestError(err)
	}
	if IsClearCommand(message) {
		if strings.TrimSpace(message) != "/clear" || len(attachments) != 0 {
			return "Usage: /clear", nil
		}
		if s.store != nil {
			version, err := s.store.saveStateWithVersion("", nil, s.storeVersion, true)
			if err != nil {
				return "", fatalRequestError(fmt.Errorf("save cleared conversation state: %w", err))
			}
			s.storeVersion = version
		}
		s.history = nil
		s.summary = ""
		s.contextNotice = ""
		return "Conversation history cleared.", nil
	}

	historyMessage := attachment.HistoryPlaceholder(message, attachments)
	candidate := append(cloneChatMessages(s.history), ChatMessage{Role: "user", Content: historyMessage})
	normalized, trimmed, err := normalizeChatMessages(candidate, false)
	if err != nil {
		normalized, trimmed = boundChatHistory(candidate)
	}
	candidate = normalized
	summary := s.summary
	if shouldSummarize(summary, candidate) {
		keepFrom := len(candidate) - recentMessagesToKeep
		provider, canSummarize := s.provider.(conversationSummarizer)
		_, canUseHistory := s.provider.(HistoryProvider)
		var compact string
		if canSummarize && canUseHistory {
			compact, err = provider.SummarizeConversation(ctx, s.active, summary, cloneChatMessages(candidate[:keepFrom]))
		} else {
			err = errors.New("provider cannot summarize conversation history")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fatalRequestError(ctxErr)
		}
		compact = strings.TrimSpace(compact)
		if err == nil && compact != "" && utf8.ValidString(compact) && len(compact) <= maxSummaryBytes {
			summary = compact
			candidate = cloneChatMessages(candidate[keepFrom:])
			// Checkpoint the compacted old conversation before the next model
			// request, without recording a user turn that might fail.
			previousTurns := cloneChatMessages(candidate[:len(candidate)-1])
			if s.store != nil {
				version, saveErr := s.store.saveStateWithVersion(summary, previousTurns, s.storeVersion, true)
				if saveErr != nil {
					return "", fatalRequestError(fmt.Errorf("save conversation state: %w", saveErr))
				}
				s.storeVersion = version
			}
			s.summary, s.history = summary, previousTurns
		} else {
			// Leave room for the next turns if compaction is unavailable. The
			// prior summary remains useful even when newer history is trimmed.
			var fallbackTrimmed bool
			candidate, fallbackTrimmed = boundSessionMessages(summary, candidate, true, maxContextTokens/2, maxSessionMessages/2)
			trimmed = trimmed || fallbackTrimmed
		}
	}

	bounded, budgetTrimmed := boundSessionMessages(summary, candidate, trimmed, maxContextTokens, maxSessionMessages)
	trimmed = trimmed || budgetTrimmed
	if trimmed {
		bounded, _ = boundSessionMessages(summary, candidate, true, maxContextTokens, maxSessionMessages)
		s.contextNotice = contextTrimNotice
	}
	requestHistory := make([]ChatMessage, 0, len(bounded)+2)
	if summary != "" {
		requestHistory = append(requestHistory, summaryMessage(summary))
	}
	if trimmed {
		requestHistory = append(requestHistory, ChatMessage{Role: "system", Content: contextTrimNotice})
	}
	requestHistory = append(requestHistory, bounded...)
	requestMessage := message
	if len(bounded) > 0 {
		requestMessage = bounded[len(bounded)-1].Content
	}
	if len(attachments) > 0 {
		requestMessage = attachment.AddReferences(requestMessage, attachments)
		if len(requestHistory) > 0 && requestHistory[len(requestHistory)-1].Role == "user" {
			requestHistory[len(requestHistory)-1].Content = attachment.AddReferences(requestHistory[len(requestHistory)-1].Content, attachments)
		}
	}

	replyContext := withCurrentTurnAttachments(ctx, attachments)
	if s.memory != nil {
		if memoryContext := s.memory.GetMemoryContext(); memoryContext != "" {
			replyContext = WithMemoryContext(replyContext, memoryContext)
		}
	}
	reply, err := s.replyWithHistory(replyContext, requestMessage, requestHistory)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fatalRequestError(ctxErr)
		}
		return "", recoverableRequestError(err)
	}

	// A provider can quote the current-turn reference or image data in its
	// answer. Do not return or retain either after this turn.
	historyReply := reply
	for _, item := range attachments {
		historyReply = strings.ReplaceAll(historyReply, item.Path, "[temporary attachment]")
		directory := filepath.Dir(item.Path)
		if strings.HasPrefix(filepath.Base(directory), attachment.AgentTemporaryDirectoryPrefix) {
			historyReply = strings.ReplaceAll(historyReply, directory, "[temporary attachment directory]")
		}
		if item.Type == "image" && len(historyReply) >= base64.StdEncoding.EncodedLen(int(item.Size)) {
			if dataURL, err := attachment.ReadImageDataURL(item); err == nil {
				historyReply = strings.ReplaceAll(historyReply, dataURL, "[image data]")
				_, encoded, _ := strings.Cut(dataURL, ",")
				historyReply = strings.ReplaceAll(historyReply, encoded, "[image data]")
			}
		}
	}
	completedCandidate := append(cloneChatMessages(bounded), ChatMessage{Role: "assistant", Content: historyReply})
	completed, completedTrimmed, err := normalizeChatMessages(completedCandidate, false)
	if err != nil {
		completed, completedTrimmed = boundChatHistory(completedCandidate)
	}
	completed, completedBudgetTrimmed := boundSessionMessages(summary, completed, false, maxContextTokens, maxSessionMessages)
	if completedTrimmed || completedBudgetTrimmed {
		s.contextNotice = contextTrimNotice
	}
	if s.store != nil {
		version, saveErr := s.store.saveStateWithVersion(summary, completed, s.storeVersion, true)
		if saveErr != nil {
			return "", fatalRequestError(fmt.Errorf("save conversation state: %w", saveErr))
		}
		s.storeVersion = version
	}
	s.history = completed
	s.summary = summary
	return historyReply, nil
}

func shouldSummarize(summary string, messages []ChatMessage) bool {
	if len(messages) <= recentMessagesToKeep {
		return false
	}
	count := len(messages)
	tokens := 0
	if summary != "" {
		count++
		tokens += estimateMessageTokens(summaryMessage(summary))
	}
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	return count >= maxSessionMessages || tokens >= summarizeTokens
}

func (s *Session) reply(ctx context.Context, message string) (string, error) {
	return s.replyWithHistory(ctx, message, []ChatMessage{{Role: "user", Content: message}})
}

func (s *Session) replyWithHistory(ctx context.Context, message string, history []ChatMessage) (string, error) {
	reply, err := s.providerReply(ctx, s.active, message, history)
	if err == nil {
		return reply, nil
	}
	if !errors.Is(err, ErrModelUnavailable) || s.active == s.config.FallbackModel {
		return "", fmt.Errorf("model %s: %w", s.active, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	// Keep the fallback active for the remainder of this terminal session.
	s.reason = fmt.Sprintf("%s failed: %v", s.active, err)
	s.active = s.config.FallbackModel
	reply, err = s.providerReply(ctx, s.active, message, history)
	if err != nil {
		return "", fmt.Errorf("fallback model %s failed (%s): %w", s.active, s.reason, err)
	}
	return reply, nil
}

func (s *Session) providerReply(ctx context.Context, model, message string, history []ChatMessage) (string, error) {
	if provider, ok := s.provider.(HistoryProvider); ok {
		return provider.ReplyWithHistory(ctx, model, cloneChatMessages(history))
	}
	return s.provider.Reply(ctx, model, message)
}

func cloneChatMessages(messages []ChatMessage) []ChatMessage {
	return append([]ChatMessage(nil), messages...)
}
