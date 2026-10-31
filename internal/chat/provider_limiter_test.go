package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderCallLimiterCapacityAndWait(t *testing.T) {
	if providerCallLimit != 5 || providerSlotWait != 45*time.Second {
		t.Fatalf("production defaults = %d, %s", providerCallLimit, providerSlotWait)
	}
	wait := 30 * time.Millisecond
	limiter := newProviderCallLimiter(wait)
	for i := 0; i < 5; i++ {
		release, err := limiter.acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire slot %d: %v", i+1, err)
		}
		defer release()
	}
	started := time.Now()
	if _, err := limiter.acquire(context.Background()); !errors.Is(err, ErrProviderCapacity) {
		t.Fatalf("sixth call error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < wait {
		t.Fatalf("sixth call waited %s, want at least %s", elapsed, wait)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition error = %v", err)
	}
}

func TestCodexProvidersShareSlotsThroughStreamingBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 6)
	finish := make(chan struct{}, 6)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush stream headers: %v", err)
			return
		}
		entered <- struct{}{}
		select {
		case <-finish:
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: [DONE]\n\n")
		case <-r.Context().Done():
		}
	}))
	defer backend.Close()
	defer close(finish)
	done := make(chan error, 6)
	call := func() {
		// Separate instances must still compete for the same process-wide slots.
		provider := CodexProvider{backendURL: backend.URL}
		_, _, err := provider.requestCodex(ctx, Credential{}, "test", nil, false)
		done <- err
	}
	for i := 0; i < 5; i++ {
		go call()
	}
	for i := 0; i < 5; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("five provider HTTP calls did not run concurrently")
		}
	}
	go call()
	select {
	case <-entered:
		t.Fatal("sixth HTTP call started while five streaming bodies were open")
	case <-time.After(30 * time.Millisecond):
	}
	finish <- struct{}{}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("sixth HTTP call did not start after a slot was released")
	}
	for i := 0; i < 5; i++ {
		finish <- struct{}{}
	}
	for i := 0; i < 6; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("provider request: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("provider requests did not finish")
		}
	}
}

func TestCodexProviderReleasesSlotBeforeToolHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Leave only one free slot. The handler must be able to acquire that slot.
	for i := 0; i < 4; i++ {
		release, err := providerCalls.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	requests := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			writeCodexFunctionCall(t, w, "call-slot", "check_slot", `{}`)
			return
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := CodexProvider{backendURL: backend.URL}
	handled := false
	if err := provider.RegisterTool(Tool{
		Name: "check_slot", Description: "Check that the provider slot is free.",
		Parameters: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			release, err := providerCalls.acquire(ctx)
			if err != nil {
				return "", fmt.Errorf("slot still held during tool execution: %w", err)
			}
			defer release()
			handled = true
			return "slot available", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	reply, _, err := provider.requestReply(ctx, Credential{}, "test", nil, true)
	if err != nil || reply != "done" || !handled {
		t.Fatalf("reply = %q, handled = %v, error = %v", reply, handled, err)
	}
	if requests != 2 {
		t.Fatalf("provider requests = %d, want tool request and follow-up", requests)
	}
}
