package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// Providers wrap ErrModelUnavailable when switching models may help.
var ErrModelUnavailable = errors.New("model unavailable")

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
	memory        *MemoryStore
	startupNotice string
	contextNotice string
}

func NewSession(config Config, provider Provider) *Session {
	return NewSessionWithMemory(config, provider, nil)
}

// NewSessionWithMemory adds Markdown memory to normal provider requests while
// keeping it out of conversation history and persisted summaries.
func NewSessionWithMemory(config Config, provider Provider, memory *MemoryStore) *Session {
	return &Session{provider: provider, config: config, active: config.PrimaryModel, memory: memory}
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	// A session is a single ordered conversation. Serializing its turns also
	// ensures that only one caller summarizes a given history.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextNotice = ""
	if err := ctx.Err(); err != nil {
		return "", err
	}

	candidate := append(cloneChatMessages(s.history), ChatMessage{Role: "user", Content: message})
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
			return "", ctxErr
		}
		compact = strings.TrimSpace(compact)
		if err == nil && compact != "" && utf8.ValidString(compact) && len(compact) <= maxSummaryBytes {
			summary = compact
			candidate = cloneChatMessages(candidate[keepFrom:])
			// Checkpoint the compacted old conversation before the next model
			// request, without recording a user turn that might fail.
			previousTurns := cloneChatMessages(candidate[:len(candidate)-1])
			if s.store != nil {
				if err := s.store.saveState(summary, previousTurns); err != nil {
					return "", fmt.Errorf("save conversation state: %w", err)
				}
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

	replyContext := ctx
	if s.memory != nil {
		if memoryContext := s.memory.GetMemoryContext(); memoryContext != "" {
			replyContext = WithMemoryContext(ctx, memoryContext)
		}
	}
	reply, err := s.replyWithHistory(replyContext, requestMessage, requestHistory)
	if err != nil {
		return "", err
	}

	completedCandidate := append(cloneChatMessages(bounded), ChatMessage{Role: "assistant", Content: reply})
	completed, completedTrimmed, err := normalizeChatMessages(completedCandidate, false)
	if err != nil {
		completed, completedTrimmed = boundChatHistory(completedCandidate)
	}
	completed, completedBudgetTrimmed := boundSessionMessages(summary, completed, false, maxContextTokens, maxSessionMessages)
	s.history = completed
	s.summary = summary
	if completedTrimmed || completedBudgetTrimmed {
		s.contextNotice = contextTrimNotice
	}
	if s.store != nil {
		if err := s.store.saveState(s.summary, s.history); err != nil {
			return "", fmt.Errorf("save conversation state: %w", err)
		}
	}
	return reply, nil
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
