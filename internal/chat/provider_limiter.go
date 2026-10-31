package chat

import (
	"context"
	"errors"
	"time"
)

const (
	providerCallLimit = 5
	providerSlotWait  = 45 * time.Second
)

// ErrProviderCapacity means all provider slots remained occupied for the maximum wait.
var ErrProviderCapacity = errors.New("provider capacity exhausted: timed out waiting for a request slot")

// All providers, including terminal and Telegram sessions, share these slots.
var providerCalls = newProviderCallLimiter(providerSlotWait)

type providerCallLimiter struct {
	slots chan struct{}
	wait  time.Duration
}

func newProviderCallLimiter(wait time.Duration) providerCallLimiter {
	return providerCallLimiter{slots: make(chan struct{}, providerCallLimit), wait: wait}
}

func (l providerCallLimiter) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrProviderCapacity
	}
}
