package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type summaryCall struct {
	model    string
	previous string
	messages []ChatMessage
}

type summaryTestProvider struct {
	mu         sync.Mutex
	summaries  []summaryCall
	histories  [][]ChatMessage
	summarize  func() (string, error)
	replyError error
}

func (p *summaryTestProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return p.ReplyWithHistory(ctx, model, []ChatMessage{{Role: "user", Content: message}})
}

func (p *summaryTestProvider) ReplyWithHistory(_ context.Context, _ string, history []ChatMessage) (string, error) {
	p.mu.Lock()
	p.histories = append(p.histories, cloneChatMessages(history))
	err := p.replyError
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	return "answer to " + history[len(history)-1].Content, nil
}

func (p *summaryTestProvider) SummarizeConversation(_ context.Context, model, previous string, messages []ChatMessage) (string, error) {
	p.mu.Lock()
	p.summaries = append(p.summaries, summaryCall{model: model, previous: previous, messages: cloneChatMessages(messages)})
	f := p.summarize
	p.mu.Unlock()
	if f != nil {
		return f()
	}
	return "important earlier facts", nil
}

func (p *summaryTestProvider) calls() ([]summaryCall, [][]ChatMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]summaryCall(nil), p.summaries...), append([][]ChatMessage(nil), p.histories...)
}

func numberedHistory(count int) []ChatMessage {
	messages := make([]ChatMessage, count)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = ChatMessage{Role: role, Content: fmt.Sprintf("message %03d", i)}
	}
	return messages
}

func historyWithTokens(tokens int) []ChatMessage {
	var messages []ChatMessage
	for tokens > 0 {
		chunk := tokens
		if chunk > maxSessionMessageBytes/4 {
			chunk = maxSessionMessageBytes / 4
		}
		messages = append(messages, ChatMessage{Role: "user", Content: strings.Repeat("x", chunk*4)})
		tokens -= chunk
	}
	return messages
}

func TestSessionSummaryTriggerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		history       []ChatMessage
		wantSummaries int
	}{
		{"below token trigger", historyWithTokens(summarizeTokens - 2), 0},
		{"at token trigger", historyWithTokens(summarizeTokens - 1), 1},
		{"below message trigger", numberedHistory(maxSessionMessages - 2), 0},
		{"at message trigger", numberedHistory(maxSessionMessages - 1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &summaryTestProvider{}
			session := NewSession(Config{PrimaryModel: "configured-model", FallbackModel: "fallback"}, provider)
			session.history = tc.history
			if _, err := session.Reply(context.Background(), "q"); err != nil {
				t.Fatal(err)
			}
			summaries, histories := provider.calls()
			if len(summaries) != tc.wantSummaries || len(histories) != 1 {
				t.Fatalf("summary calls = %d, chat calls = %d; want %d and 1", len(summaries), len(histories), tc.wantSummaries)
			}
			if tc.wantSummaries == 1 && summaries[0].model != "configured-model" {
				t.Fatalf("summary model = %q", summaries[0].model)
			}
		})
	}
}

func TestSessionSummaryPersistsAndReplacesOlderMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	config := Config{PrimaryModel: "primary", FallbackModel: "fallback"}
	provider := &summaryTestProvider{}
	session, err := NewPersistentSessionAt(config, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	original := numberedHistory(maxSessionMessages)
	session.history = cloneChatMessages(original)
	if _, err := session.Reply(context.Background(), "new question"); err != nil {
		t.Fatal(err)
	}
	summaries, histories := provider.calls()
	if len(summaries) != 1 || len(histories) != 1 {
		t.Fatalf("summary calls = %d, chat calls = %d", len(summaries), len(histories))
	}
	cut := len(original) + 1 - recentMessagesToKeep
	if summaries[0].previous != "" || !reflect.DeepEqual(summaries[0].messages, original[:cut]) {
		t.Fatalf("summary input did not contain exactly the old messages: %+v", summaries[0])
	}
	want := []ChatMessage{summaryMessage("important earlier facts")}
	want = append(want, original[cut:]...)
	want = append(want, ChatMessage{Role: "user", Content: "new question"})
	if !reflect.DeepEqual(histories[0], want) {
		t.Fatalf("outbound context = %#v, want %#v", histories[0], want)
	}
	if session.contextNotice != "" {
		t.Fatalf("successful summary reported trimming: %q", session.contextNotice)
	}

	restartedProvider := &summaryTestProvider{}
	restarted, err := NewPersistentSessionAt(config, restartedProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.summary != "important earlier facts" {
		t.Fatalf("reloaded summary = %q", restarted.summary)
	}
	if _, err := restarted.Reply(context.Background(), "follow-up"); err != nil {
		t.Fatal(err)
	}
	_, restartedHistories := restartedProvider.calls()
	want = append(want, ChatMessage{Role: "assistant", Content: "answer to new question"}, ChatMessage{Role: "user", Content: "follow-up"})
	if len(restartedHistories) != 1 || !reflect.DeepEqual(restartedHistories[0], want) {
		t.Fatalf("restarted context = %#v, want %#v", restartedHistories, want)
	}

	// A later compaction incorporates the previous summary instead of losing it.
	remaining := maxSessionMessages - len(restarted.history) - 2 // summary and next user message
	restarted.history = append(restarted.history, numberedHistory(remaining)...)
	if _, err := restarted.Reply(context.Background(), "later question"); err != nil {
		t.Fatal(err)
	}
	laterSummaries, _ := restartedProvider.calls()
	if len(laterSummaries) != 1 || laterSummaries[0].previous != "important earlier facts" {
		t.Fatalf("subsequent summary did not incorporate the prior summary: %#v", laterSummaries)
	}
}

func TestSummaryTriggerCountsPreviousSummary(t *testing.T) {
	provider := &summaryTestProvider{}
	session := NewSession(Config{PrimaryModel: "primary"}, provider)
	session.summary = "prior context"
	priorTokens := estimateMessageTokens(summaryMessage(session.summary))
	session.history = historyWithTokens(summarizeTokens - priorTokens - 1)
	if _, err := session.Reply(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	summaries, _ := provider.calls()
	if len(summaries) != 1 || summaries[0].previous != "prior context" {
		t.Fatalf("summary trigger omitted prior summary: %#v", summaries)
	}
}

func TestSessionSummaryFailureFallsBackToBoundedTrimming(t *testing.T) {
	for _, tc := range []struct {
		name      string
		summarize func() (string, error)
	}{
		{"provider error", func() (string, error) { return "", errors.New("summary unavailable") }},
		{"empty summary", func() (string, error) { return " \n ", nil }},
		{"oversized summary", func() (string, error) { return strings.Repeat("x", maxSummaryBytes+1), nil }},
		{"invalid UTF-8", func() (string, error) { return "\xff", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &summaryTestProvider{summarize: tc.summarize}
			session := NewSession(Config{PrimaryModel: "primary"}, provider)
			session.summary = "previous summary"
			session.history = numberedHistory(maxSessionMessages)
			if _, err := session.Reply(context.Background(), "current question"); err != nil {
				t.Fatalf("chat failed after summarization failure: %v", err)
			}
			summaries, histories := provider.calls()
			if len(summaries) != 1 || len(histories) != 1 {
				t.Fatalf("summary calls = %d, chat calls = %d", len(summaries), len(histories))
			}
			history := histories[0]
			if session.summary != "previous summary" || history[0] != summaryMessage("previous summary") || !containsContextNotice(history) {
				t.Fatalf("fallback did not preserve summary and notice: %#v", history)
			}
			if len(history) > maxSessionMessages/2 || totalMessageTokens(history) > maxContextTokens/2 {
				t.Fatalf("fallback is not bounded: %d messages, %d tokens", len(history), totalMessageTokens(history))
			}
			if containsMessage(history, "user", "message 000") || !containsMessage(history, "user", "current question") || !containsMessage(history, "assistant", "message 255") {
				t.Fatalf("fallback did not retain the newest messages: %#v", history)
			}
			if session.contextNotice != contextTrimNotice {
				t.Fatalf("context notice = %q", session.contextNotice)
			}
		})
	}
}

func TestSummaryFailureTrimsTokenBudget(t *testing.T) {
	provider := &summaryTestProvider{summarize: func() (string, error) { return "", errors.New("unavailable") }}
	session := NewSession(Config{PrimaryModel: "primary"}, provider)
	session.history = historyWithTokens(summarizeTokens - 1)
	if _, err := session.Reply(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	_, histories := provider.calls()
	if len(histories) != 1 || totalMessageTokens(histories[0]) > maxContextTokens/2 || !containsContextNotice(histories[0]) || !containsMessage(histories[0], "user", "q") {
		t.Fatalf("fallback context was not bounded: %#v", histories)
	}
}

func TestCompactionIsCheckpointedBeforeReplyFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	provider := &summaryTestProvider{replyError: errors.New("chat failed")}
	session, err := NewPersistentSessionAt(Config{PrimaryModel: "primary"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	session.history = numberedHistory(maxSessionMessages)
	if _, err := session.Reply(context.Background(), "unsent question"); err == nil {
		t.Fatal("chat unexpectedly succeeded")
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != "important earlier facts" || len(loaded.Messages) != recentMessagesToKeep-1 || containsMessage(loaded.Messages, "user", "unsent question") {
		t.Fatalf("checkpoint = %+v", loaded)
	}
}

func TestConcurrentRepliesSummarizeOnlyOnce(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	provider := &summaryTestProvider{summarize: func() (string, error) {
		started <- struct{}{}
		<-release
		return "important earlier facts", nil
	}}
	session := NewSession(Config{PrimaryModel: "primary"}, provider)
	session.history = numberedHistory(maxSessionMessages - 1)
	errorsCh := make(chan error, 2)
	go func() { _, err := session.Reply(context.Background(), "first"); errorsCh <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("summary did not start")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := session.Reply(context.Background(), "second")
		errorsCh <- err
	}()
	<-secondStarted
	select {
	case <-started:
		t.Fatal("a second summary started while the first was in progress")
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	summaries, histories := provider.calls()
	if len(summaries) != 1 || len(histories) != 2 {
		t.Fatalf("summary calls = %d, chat calls = %d", len(summaries), len(histories))
	}
	if !containsMessage(histories[1], "user", "first") || !containsMessage(histories[1], "assistant", "answer to first") || !containsMessage(histories[1], "user", "second") {
		t.Fatalf("concurrent turns were not serialized: %#v", histories[1])
	}
}

func TestCanceledSummaryLeavesConversationUnchanged(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &summaryTestProvider{summarize: func() (string, error) {
		close(started)
		<-release
		return "ignored summary", nil
	}}
	session := NewSession(Config{PrimaryModel: "primary"}, provider)
	original := numberedHistory(maxSessionMessages)
	session.history = cloneChatMessages(original)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := session.Reply(ctx, "question"); result <- err }()
	<-started
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	_, histories := provider.calls()
	if session.summary != "" || !reflect.DeepEqual(session.history, original) || len(histories) != 0 {
		t.Fatal("canceled summary changed or sent the conversation")
	}
}

func TestCodexSessionSummarizesWithSubscriptionAndNoTools(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "access-token", AccountID: "account-123", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var requestsMu sync.Mutex
	var requests []codexRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-ID") != "account-123" {
			t.Error("subscription credentials were not sent")
		}
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestsMu.Lock()
		requests = append(requests, request)
		requestCount := len(requests)
		requestsMu.Unlock()
		reply := "compact summary"
		if requestCount == 2 {
			reply = "normal reply"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", reply)
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	session := NewSession(Config{PrimaryModel: "configured-model"}, provider)
	session.summary = "prior summary"
	session.history = numberedHistory(maxSessionMessages)
	if reply, err := session.Reply(context.Background(), "latest question"); err != nil || reply != "normal reply" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want summary and chat", len(requests))
	}
	summaryRequest, chatRequest := requests[0], requests[1]
	if summaryRequest.Model != "configured-model" || len(summaryRequest.Tools) != 0 {
		t.Fatalf("summary model/tools = %q/%v", summaryRequest.Model, summaryRequest.Tools)
	}
	if len(summaryRequest.Input) < 4 || !strings.Contains(summaryRequest.Input[1].Content[0].Text, "prior summary") || !strings.Contains(summaryRequest.Input[2].Content[0].Text, "message 000") {
		t.Fatalf("summary request omitted prior context: %#v", summaryRequest.Input)
	}
	if len(chatRequest.Tools) == 0 || chatRequest.Input[0].Content[0].Text != summaryMessage("compact summary").Content {
		t.Fatalf("chat request did not use the summary and normal tools: %#v", chatRequest)
	}
	for _, input := range chatRequest.Input {
		if len(input.Content) > 0 && input.Content[0].Text == "message 000" {
			t.Fatal("summarized message was sent again to chat")
		}
	}
}

func TestCodexSummarizationRejectsUnexpectedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCodexFunctionCall(t, w, "call-1", "summary_test_tool", `{}`)
	}))
	defer server.Close()
	provider := newShellTestProvider(t, server)
	var executions atomic.Int32
	if err := provider.RegisterTool(Tool{
		Name:        "summary_test_tool",
		Description: "Record an execution.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "done", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.SummarizeConversation(context.Background(), "model", "", numberedHistory(2)); err == nil {
		t.Fatal("unexpected tool call was accepted")
	}
	if executions.Load() != 0 {
		t.Fatal("summarization executed a tool")
	}
}
