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

// EchoProvider is entirely local; model names are labels, not API calls.
type EchoProvider struct {
	UnavailableModel string
}

func (p EchoProvider) Reply(ctx context.Context, model, message string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if model == p.UnavailableModel {
		return "", fmt.Errorf("%w: %s (simulated by local echo provider)", ErrModelUnavailable, model)
	}
	return "Echo: " + message, nil
}

type Session struct {
	provider Provider
	config   Config
	active   string
	reason   string
	history  []Entry
}

func NewSession(config Config, provider Provider) *Session {
	return &Session{provider: provider, config: config, active: config.PrimaryModel}
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	s.history = append(s.history, Entry{Role: "user", Content: message})
	reply, err := s.reply(ctx, message)
	if err == nil {
		s.history = append(s.history, Entry{Role: "assistant", Content: reply})
	}
	return reply, err
}

func (s *Session) reply(ctx context.Context, message string) (string, error) {
	reply, err := s.provider.Reply(ctx, s.active, message)
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
	reply, err = s.provider.Reply(ctx, s.active, message)
	if err != nil {
		return "", fmt.Errorf("fallback model %s failed (%s): %w", s.active, s.reason, err)
	}
	return reply, nil
}
