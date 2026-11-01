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

// Providers wrap ErrModelUnavailable when the selected model is unavailable.
var ErrModelUnavailable = errors.New("model unavailable")

// ErrSessionBusy is returned before a scheduled turn starts when it must be
// deferred, such as lock contention or an unresolved recovery. Retrying cannot
// repeat a tool action.
var ErrSessionBusy = errors.New("conversation is busy")

// ErrConversationRecovery requires an explicit acknowledgement before a
// missing or quarantined conversation can be replaced by an interactive turn.
var ErrConversationRecovery = errors.New("The saved conversation is missing or invalid and may have been moved aside. Check the saved or quarantined file and whether any actions already completed. Use /clear to start a new conversation.")

// ModelRequestError reports the exact model that returned a provider error.
// The caller must not retry the request with another model.
type ModelRequestError struct {
	Model string
	Err   error
}

func (err ModelRequestError) Error() string {
	return fmt.Sprintf("model %s: %v", err.Model, err.Err)
}

func (err ModelRequestError) Unwrap() error { return err.Err }

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
	mu                    sync.Mutex
	provider              Provider
	registry              ModelRegistry
	chatID                int64
	active                string
	history               []ChatMessage
	summary               string
	selectedSkills        []selectedSkill
	skillCheckpointFailed bool
	recoveryPending       bool
	store                 *SessionStore
	storeVersion          [sha256.Size]byte // Expected persisted version for this session.
	memory                *MemoryStore
	startupNotice         string
	contextNotice         string
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
	active := strings.TrimSpace(config.Model)
	if active == "" {
		active = DefaultModel
	}
	session := &Session{
		provider: provider,
		registry: config.Registry,
		chatID:   config.ChatID,
		active:   active,
		memory:   memory,
	}
	session.refreshModel()
	return session
}

func withCurrentTurnAttachments(ctx context.Context, attachments []attachment.Metadata) context.Context {
	return context.WithValue(ctx, currentTurnAttachmentsKey{}, append([]attachment.Metadata(nil), attachments...))
}

func currentTurnAttachments(ctx context.Context) []attachment.Metadata {
	attachments, _ := ctx.Value(currentTurnAttachmentsKey{}).([]attachment.Metadata)
	return attachments
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	return s.replyWithAttachments(ctx, message, nil, false)
}

// ReplyScheduled refreshes a stale on-disk conversation before running a job.
func (s *Session) ReplyScheduled(ctx context.Context, message string) (string, error) {
	return s.replyWithAttachments(ctx, message, nil, true)
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
	return s.replyWithAttachments(ctx, message, attachments, false)
}

func (s *Session) replyWithAttachments(ctx context.Context, message string, attachments []attachment.Metadata, scheduled bool) (string, error) {
	// A session is a single ordered conversation. Serializing its turns also
	// ensures that only one caller summarizes a given history.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextNotice = ""
	if err := ctx.Err(); err != nil {
		if scheduled {
			// No work has started, including when cancellation occurred while
			// this turn waited for the session mutex.
			return "", ErrSessionBusy
		}
		return "", fatalRequestError(err)
	}
	if s.store != nil {
		// Hold a separate turn lock while model calls and tools are running.
		// The ordinary state lock is still available for our checkpoints.
		unlockTurn, err := s.store.lock(true)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(fmt.Errorf("lock conversation turn: %w", err))
		}
		defer unlockTurn()
		unlockState, err := s.store.lock(false)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(err)
		}
		current, err := sessionVersion(s.store.Path)
		unlockState()
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(err)
		}
		changed := current != s.storeVersion
		if changed || (scheduled && current == [sha256.Size]byte{}) {
			if !scheduled {
				return "", fatalRequestError(errors.New("conversation state changed in another instance; restart to load the latest history"))
			}
			loaded, err := s.store.loadStateUnderTurnLock(&s.storeVersion)
			if err != nil {
				return "", errors.Join(ErrSessionBusy, fmt.Errorf("reload conversation for scheduled job: %w", err))
			}
			if loaded.Recovered {
				// Do not replace the in-memory history while recovery is awaiting
				// the user's acknowledgement, even if the scheduler found it.
				s.recoveryPending = true
			} else if changed && current == [sha256.Size]byte{} {
				s.recoveryPending = true
				if err := s.store.markMissing(); err != nil {
					return "", errors.Join(ErrSessionBusy, fmt.Errorf("record missing conversation: %w", err))
				}
			} else if changed {
				s.history = cloneChatMessages(loaded.Messages)
				s.summary = loaded.Summary
				s.selectedSkills = append([]selectedSkill(nil), loaded.selectedSkills...)
				s.skillCheckpointFailed = false
				s.recoveryPending = false
			}
		}
	}
	if scheduled && s.recoveryPending {
		return "", errors.Join(ErrSessionBusy, errors.New("conversation recovery needs an interactive turn"))
	}
	if IsClearCommand(message) {
		if strings.TrimSpace(message) != "/clear" || len(attachments) != 0 {
			return "Usage: /clear", nil
		}
		if s.store != nil {
			version, err := s.store.saveStateWithVersion("", nil, nil, s.storeVersion, true)
			if err != nil {
				return "", fatalRequestError(fmt.Errorf("save cleared conversation state: %w", err))
			}
			s.storeVersion = version
		}
		s.history = nil
		s.summary = ""
		s.selectedSkills = nil
		s.skillCheckpointFailed = false
		s.recoveryPending = false
		s.contextNotice = ""
		return "Conversation history cleared.", nil
	}
	if s.recoveryPending {
		return "", recoverableRequestError(ErrConversationRecovery)
	}
	if s.skillCheckpointFailed {
		err := fatalRequestError(skillRecovery(errors.New("skill selection could not be saved; restart to reload the conversation"), skillSaveMessage))
		if scheduled {
			return "", errors.Join(ErrSessionBusy, err)
		}
		return "", err
	}
	if s.memory != nil {
		if s.memory.initErr != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, s.memory.initErr)
			}
			return "", recoverableRequestError(s.memory.initErr)
		}
	}
	s.refreshModel()
	// Build the same memory context used by the provider before calculating
	// the budget, so instructions and restored skills have room alongside
	// both recent history and any compaction summary.
	replyContext := withCurrentTurnAttachments(ctx, attachments)
	if s.memory != nil {
		if memoryContext := s.memory.GetMemoryContext(); memoryContext != "" {
			replyContext = WithMemoryContext(replyContext, memoryContext)
		}
	}
	overhead := 0
	tracksTools := false
	if provider, ok := s.provider.(interface {
		skillContextOverhead(context.Context, []selectedSkill) (int, error)
	}); ok {
		tracksTools = true
		var err error
		overhead, err = provider.skillContextOverhead(replyContext, s.selectedSkills)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", recoverableRequestError(err)
		}
	}

	historyMessage := attachment.HistoryPlaceholder(message, attachments)
	candidate := append(cloneChatMessages(s.history), ChatMessage{Role: "user", Content: historyMessage})
	normalized, trimmed, err := normalizeChatMessages(candidate, false)
	if err != nil {
		normalized, trimmed = boundChatHistory(candidate)
	}
	candidate = normalized
	summary := s.summary
	if shouldSummarize(summary, candidate, overhead) {
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
				version, saveErr := s.store.saveStateWithVersion(summary, previousTurns, s.selectedSkills, s.storeVersion, true)
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
			fallbackBudget := (maxContextTokens - overhead) / 2
			candidate, fallbackTrimmed = boundSessionMessages(summary, candidate, true, fallbackBudget, maxSessionMessages/2)
			trimmed = trimmed || fallbackTrimmed
		}
	}

	recoveryNotice := s.registry.takeRecoveryNotice()
	modelNotice := ChatMessage{Role: "system", Content: "Pending GoAster model configuration recovery notice: " + recoveryNotice}
	budget, maxMessages := maxContextTokens-overhead, maxSessionMessages
	if recoveryNotice != "" {
		budget -= estimateMessageTokens(modelNotice)
		maxMessages--
	}
	bounded, budgetTrimmed := boundSessionMessages(summary, candidate, trimmed, budget, maxMessages)
	trimmed = trimmed || budgetTrimmed
	if trimmed {
		bounded, _ = boundSessionMessages(summary, candidate, true, budget, maxMessages)
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
	if recoveryNotice != "" {
		requestHistory = append([]ChatMessage{modelNotice}, requestHistory...)
	}
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

	// The session owns selections, even when several sessions share a provider.
	// Checkpoint before a selected skill can lead to another tool call.
	replyContext = context.WithValue(replyContext, skillSessionKey{}, skillSessionContext{
		selected: func() []selectedSkill {
			return append([]selectedSkill(nil), s.selectedSkills...)
		},
		remember: func(next selectedSkill) error {
			selected := append([]selectedSkill(nil), s.selectedSkills...)
			found := false
			for i, current := range selected {
				if current.Name == next.Name {
					if current.Digest != "" && current.Digest != next.Digest {
						return errors.New("selected skill changed while loading; use /clear to start a new conversation")
					}
					selected[i], found = next, true
					break
				}
			}
			if !found {
				selected = append(selected, next)
			}
			if !validSelectedSkills(selected) {
				return errors.New("invalid skill selection")
			}
			if s.store != nil {
				version, err := s.store.saveStateWithVersion(s.summary, s.history, selected, s.storeVersion, true)
				if err != nil {
					s.skillCheckpointFailed = true
					return fatalRequestError(skillRecovery(fmt.Errorf("save selected skill: %w", err), skillSaveMessage))
				}
				s.storeVersion = version
			}
			s.selectedSkills = selected
			if len(selected) > maxSessionSkills {
				return errors.New("too many selected skills; use /clear to start a new conversation")
			}
			return nil
		},
	})
	actionStarted := false
	if scheduled && tracksTools {
		observer := toolCallObserver(replyContext)
		replyContext = WithToolCallObserver(replyContext, func(name, arguments string) {
			if name != loadSkillCodexTool.Name {
				actionStarted = true
			}
			if observer != nil {
				observer(name, arguments)
			}
		})
	}
	reply, err := s.replyWithHistory(replyContext, requestMessage, requestHistory)
	if err != nil {
		if scheduled && tracksTools && !actionStarted && SkillRecoveryMessage(err) != "" {
			return "", errors.Join(ErrSessionBusy, err)
		}
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
	completed, completedBudgetTrimmed := boundSessionMessages(summary, completed, false, maxContextTokens-overhead, maxSessionMessages)
	if completedTrimmed || completedBudgetTrimmed {
		s.contextNotice = contextTrimNotice
	}
	if s.store != nil {
		version, saveErr := s.store.saveStateWithVersion(summary, completed, s.selectedSkills, s.storeVersion, true)
		if saveErr != nil {
			return "", fatalRequestError(fmt.Errorf("save conversation state: %w", saveErr))
		}
		s.storeVersion = version
	}
	s.history = completed
	s.summary = summary
	s.recoveryPending = false
	return historyReply, nil
}

func shouldSummarize(summary string, messages []ChatMessage, overhead int) bool {
	if len(messages) <= recentMessagesToKeep {
		return false
	}
	count := len(messages)
	tokens := overhead
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
	return "", ModelRequestError{Model: s.active, Err: err}
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
