package chat

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	config, err := ParseConfig(nil, io.Discard)
	if err != nil || config.PrimaryModel != "gpt-6.1-sol" || config.FallbackModel != "gpt-5.6-luna" || config.EchoUnavailableModel != "" {
		t.Fatalf("defaults: %+v, %v", config, err)
	}
	config, err = ParseConfig([]string{"-primary-model", " custom-primary ", "-fallback-model", "custom-fallback", "-echo-unavailable-model", "custom-primary"}, io.Discard)
	if err != nil || config != (Config{"custom-primary", "custom-fallback", "custom-primary"}) {
		t.Fatalf("overrides: %+v, %v", config, err)
	}
	for _, args := range [][]string{{"-primary-model", " "}, {"-fallback-model", ""}, {"unexpected"}, {"-unknown"}, {"-primary-model"}} {
		if _, err := ParseConfig(args, io.Discard); err == nil {
			t.Errorf("expected invalid configuration: %q", args)
		}
	}
	if _, err := ParseConfig([]string{"-help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
}

type providerFunc func(context.Context, string, string) (string, error)

func (f providerFunc) Reply(ctx context.Context, model, message string) (string, error) {
	return f(ctx, model, message)
}

func defaultConfig(t *testing.T) Config {
	t.Helper()
	config, err := ParseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestSessionSelection(t *testing.T) {
	ordinaryError := errors.New("provider failed")
	unavailable := fmt.Errorf("temporarily offline: %w", ErrModelUnavailable)
	for _, tc := range []struct {
		name        string
		primaryErr  error
		fallbackErr error
		sameModel   bool
		wantModels  []string
		wantErr     error
	}{
		{"primary", nil, nil, false, []string{DefaultPrimaryModel, DefaultPrimaryModel}, nil},
		{"fallback stays active", unavailable, nil, false, []string{DefaultPrimaryModel, DefaultFallbackModel, DefaultFallbackModel}, nil},
		{"ordinary error does not switch", ordinaryError, nil, false, []string{DefaultPrimaryModel}, ordinaryError},
		{"failed fallback stops", unavailable, ordinaryError, false, []string{DefaultPrimaryModel, DefaultFallbackModel}, ordinaryError},
		{"same model is not retried", unavailable, nil, true, []string{DefaultPrimaryModel}, ErrModelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultConfig(t)
			if tc.sameModel {
				config.FallbackModel = config.PrimaryModel
			}
			var models []string
			provider := providerFunc(func(ctx context.Context, model, message string) (string, error) {
				models = append(models, model)
				if message != "hello" {
					t.Errorf("message = %q", message)
				}
				if model == config.PrimaryModel && tc.primaryErr != nil {
					return "", tc.primaryErr
				}
				if model == config.FallbackModel && tc.fallbackErr != nil {
					return "", tc.fallbackErr
				}
				return "response", nil
			})
			session := NewSession(config, provider)
			reply, err := session.Reply(context.Background(), "hello")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err == nil {
				if reply != "response" {
					t.Fatalf("reply = %q", reply)
				}
				if _, err := session.Reply(context.Background(), "hello"); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(models, tc.wantModels) {
				t.Errorf("models = %v, want %v", models, tc.wantModels)
			}
			if len(models) > 1 && models[1] == DefaultFallbackModel && !strings.Contains(session.reason, unavailable.Error()) {
				t.Errorf("missing failure reason: %q", session.reason)
			}
		})
	}
}

func TestEchoProvider(t *testing.T) {
	provider := EchoProvider{UnavailableModel: "offline"}
	for i := 0; i < 2; i++ {
		reply, err := provider.Reply(context.Background(), "local", "hello")
		if err != nil || reply != "Echo: hello" {
			t.Fatalf("reply = %q, %v", reply, err)
		}
	}
	if _, err := provider.Reply(context.Background(), "offline", "hello"); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Reply(ctx, "local", "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestLoop(t *testing.T) {
	for _, ending := range []string{"/quit\nignored\n", "/exit\nignored\n", ""} {
		t.Run(fmt.Sprintf("ending=%q", ending), func(t *testing.T) {
			var output bytes.Buffer
			var messages []string
			provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
				messages = append(messages, message)
				return "received " + message, nil
			})
			err := Run(context.Background(), strings.NewReader("\n hello \n/history\n/clear\nsecond\n"+ending), &output, NewSession(defaultConfig(t), provider))
			if err != nil || !reflect.DeepEqual(messages, []string{"hello", "/history", "/clear", "second"}) {
				t.Fatalf("messages = %q, error = %v", messages, err)
			}
			for _, want := range []string{"local echo provider (offline)", "Type a message, /quit or /exit to exit.", "Active model: gpt-6.1-sol", "you> ", "assistant [gpt-6.1-sol]> received hello", "assistant [gpt-6.1-sol]> received /history", "assistant [gpt-6.1-sol]> received /clear", "received second"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("output missing %q: %s", want, &output)
				}
			}
		})
	}
}

func TestLoopShowsFallback(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), strings.NewReader("hello\nagain"), &output,
		NewSession(defaultConfig(t), EchoProvider{UnavailableModel: DefaultPrimaryModel}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Active model: gpt-5.6-luna", "Fallback reason: gpt-6.1-sol failed: model unavailable", "assistant [gpt-5.6-luna]> Echo: hello", "assistant [gpt-5.6-luna]> Echo: again"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLoopErrors(t *testing.T) {
	failure := errors.New("broken stream or provider")
	for _, tc := range []struct {
		name     string
		input    io.Reader
		output   io.Writer
		provider Provider
	}{
		{"input", failingReader{failure}, io.Discard, EchoProvider{}},
		{"output", strings.NewReader("hello\n"), failingWriter{failure}, EchoProvider{}},
		{"provider", strings.NewReader("hello\n"), io.Discard, providerFunc(func(context.Context, string, string) (string, error) { return "", failure })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(context.Background(), tc.input, tc.output, NewSession(defaultConfig(t), tc.provider)); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
