package chat

import (
	"context"
	"errors"
	"fmt"
)

// Providers wrap ErrModelUnavailable when switching models may help.
var ErrModelUnavailable = errors.New("model unavailable")

type Provider interface {
	Reply(ctx context.Context, model, message string) (string, error)
}

// ChatMessage is a conversation message that can be sent to a history-aware
// provider. Session persistence stores only user and assistant messages.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HistoryProvider is optional so existing providers remain compatible with
// the one-message Provider interface.
type HistoryProvider interface {
	ReplyWithHistory(ctx context.Context, model string, history []ChatMessage) (string, error)
}

type Session struct {
	provider      Provider
	config        Config
	active        string
	reason        string
	history       []ChatMessage
	store         *SessionStore
	startupNotice string
	contextNotice string
}

func NewSession(config Config, provider Provider) *Session {
	return &Session{provider: provider, config: config, active: config.PrimaryModel}
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	s.contextNotice = ""
	candidate := append(cloneChatMessages(s.history), ChatMessage{Role: "user", Content: message})
	bounded, trimmed := boundChatHistory(candidate)
	requestHistory := bounded
	if trimmed {
		requestHistory = historyWithContextNotice(bounded)
		s.contextNotice = contextTrimNotice
	}
	requestMessage := message
	if len(bounded) > 0 {
		requestMessage = bounded[len(bounded)-1].Content
	}

	reply, err := s.replyWithHistory(ctx, requestMessage, requestHistory)
	if err != nil {
		return "", err
	}

	completed, completedTrimmed := boundChatHistory(append(cloneChatMessages(bounded), ChatMessage{Role: "assistant", Content: reply}))
	s.history = completed
	if completedTrimmed {
		s.contextNotice = contextTrimNotice
	}
	if s.store != nil {
		if err := s.store.Save(s.history); err != nil {
			return "", fmt.Errorf("save conversation state: %w", err)
		}
	}
	return reply, nil
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
