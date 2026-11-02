package chat

import "context"

// AuthHandoffNotice is an out-of-band authentication event. It is delivered
// to the channel adapter, never to the model conversation or its history.
type AuthHandoffNotice struct {
	Provider        string
	Status          string
	VerificationURL string
	UserCode        string
	ExpiresAt       string
	VerifiedAccount string
}

// AuthHandoffObserver receives secret-bearing device instructions outside the
// model request. Implementations must not add notices to model history.
type AuthHandoffObserver func(AuthHandoffNotice)

type authHandoffObserverKey struct{}

// WithAuthHandoffObserver attaches an out-of-band authentication event sink to
// one request.
func WithAuthHandoffObserver(ctx context.Context, observer AuthHandoffObserver) context.Context {
	return context.WithValue(ctx, authHandoffObserverKey{}, observer)
}

// AuthHandoffObserverFromContext returns the out-of-band event sink, if any.
func AuthHandoffObserverFromContext(ctx context.Context) AuthHandoffObserver {
	observer, _ := ctx.Value(authHandoffObserverKey{}).(AuthHandoffObserver)
	return observer
}

type chatIDKey struct{}

// WithChatID binds the authenticated channel's conversation ID to a model
// request. Tools use this value instead of accepting a model-supplied ID.
func WithChatID(ctx context.Context, chatID int64) context.Context {
	return context.WithValue(ctx, chatIDKey{}, chatID)
}

// ChatIDFromContext returns the channel conversation ID bound by the caller.
func ChatIDFromContext(ctx context.Context) int64 {
	chatID, _ := ctx.Value(chatIDKey{}).(int64)
	return chatID
}
