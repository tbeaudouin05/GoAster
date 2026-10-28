package chat

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryRecording(t *testing.T) {
	failure := errors.New("reply failed")
	for _, tc := range []struct {
		name       string
		primaryErr error
		replyErr   error
	}{
		{name: "primary"},
		{name: "fallback", primaryErr: ErrModelUnavailable},
		{name: "primary failure", primaryErr: failure, replyErr: failure},
		{name: "fallback failure", primaryErr: ErrModelUnavailable, replyErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
				if model == DefaultPrimaryModel && tc.primaryErr != nil {
					return "", tc.primaryErr
				}
				return "reply to " + message, tc.replyErr
			})
			session := NewSession(defaultConfig(t), provider)
			var want []Entry
			for _, message := range []string{"hello", "second"} {
				_, err := session.Reply(context.Background(), message)
				if !errors.Is(err, tc.replyErr) {
					t.Fatalf("Reply error = %v, want %v", err, tc.replyErr)
				}
				want = append(want, Entry{Role: "user", Content: message})
				if err == nil {
					want = append(want, Entry{Role: "assistant", Content: "reply to " + message})
				}
			}
			if got := session.History(); !reflect.DeepEqual(got, want) {
				t.Fatalf("History = %#v, want %#v", got, want)
			}
			copy := session.History()
			copy[0].Content = "changed"
			if !reflect.DeepEqual(session.History(), want) {
				t.Fatal("mutating History result changed session history")
			}
			other := NewSession(defaultConfig(t), provider)
			if len(other.History()) != 0 {
				t.Fatal("new session inherited history")
			}
		})
	}
}

func TestClearHistoryPreservesFallback(t *testing.T) {
	session := NewSession(defaultConfig(t), EchoProvider{UnavailableModel: DefaultPrimaryModel})
	if _, err := session.Reply(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	reason := session.reason
	for i := 0; i < 2; i++ {
		session.ClearHistory()
		if len(session.History()) != 0 {
			t.Fatal("history was not cleared")
		}
		if session.active != DefaultFallbackModel || session.reason != reason {
			t.Fatal("clearing history changed fallback state")
		}
	}
	if _, err := session.Reply(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	want := []Entry{{Role: "user", Content: "new"}, {Role: "assistant", Content: "Echo: new"}}
	if got := session.History(); !reflect.DeepEqual(got, want) {
		t.Fatalf("History after clear = %#v, want %#v", got, want)
	}
}

func TestLoopHistoryCommands(t *testing.T) {
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _, message string) (string, error) {
		messages = append(messages, message)
		return "reply to " + message, nil
	})
	session := NewSession(defaultConfig(t), provider)
	input := "/history\n\n hello \nsecond\n /history \n/clear\n/history\n/clear\nnew\n/history\n/quit\n"
	if err := Run(context.Background(), strings.NewReader(input), &output, session); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, []string{"hello", "second", "new"}) {
		t.Fatalf("provider messages = %q", messages)
	}
	for _, want := range []string{
		"you> 1. user: hello\n2. assistant: reply to hello\n3. user: second\n4. assistant: reply to second\n",
		"you> History cleared.\nyou> History is empty.\nyou> History cleared.\n",
		"you> 1. user: new\n2. assistant: reply to new\nyou> Goodbye.\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, &output)
		}
	}
	if count := strings.Count(output.String(), "History is empty."); count != 2 {
		t.Errorf("empty history displayed %d times, want 2", count)
	}
	want := []Entry{{Role: "user", Content: "new"}, {Role: "assistant", Content: "reply to new"}}
	if got := session.History(); !reflect.DeepEqual(got, want) {
		t.Fatalf("final history = %#v, want %#v", got, want)
	}
}
