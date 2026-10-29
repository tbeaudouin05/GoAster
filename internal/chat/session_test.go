package chat

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type historyTestProvider struct {
	histories [][]ChatMessage
}

func (p *historyTestProvider) Reply(_ context.Context, _ string, message string) (string, error) {
	return "reply to " + message, nil
}

func (p *historyTestProvider) ReplyWithHistory(_ context.Context, _ string, history []ChatMessage) (string, error) {
	p.histories = append(p.histories, cloneChatMessages(history))
	if len(history) == 0 {
		return "reply", nil
	}
	return "reply to " + history[len(history)-1].Content, nil
}

func TestSessionStorePersistsAndLoadsConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "session.json")
	store := NewSessionStoreAt(path)
	want := []ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hello back"},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("conversation permissions = %04o, want 0600", got)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("conversation directory permissions = %04o, want 0700", got)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Recovered || loaded.Trimmed {
		t.Fatalf("load status = %+v, want an unchanged conversation", loaded)
	}
	if !reflect.DeepEqual(loaded.Messages, want) {
		t.Fatalf("loaded messages = %#v, want %#v", loaded.Messages, want)
	}
}

func TestPersistentSessionLoadsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	config := Config{PrimaryModel: DefaultPrimaryModel, FallbackModel: DefaultFallbackModel}
	firstProvider := &historyTestProvider{}
	first, err := NewPersistentSessionAt(config, firstProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Reply(context.Background(), "first question"); err != nil {
		t.Fatal(err)
	}

	restartedProvider := &historyTestProvider{}
	restarted, err := NewPersistentSessionAt(config, restartedProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reply(context.Background(), "second question"); err != nil {
		t.Fatal(err)
	}
	if len(restartedProvider.histories) != 1 {
		t.Fatalf("history calls = %d, want 1", len(restartedProvider.histories))
	}
	want := []ChatMessage{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "reply to first question"},
		{Role: "user", Content: "second question"},
	}
	if !reflect.DeepEqual(restartedProvider.histories[0], want) {
		t.Fatalf("restarted history = %#v, want %#v", restartedProvider.histories[0], want)
	}
}

func TestSessionStoreQuarantinesMalformedConversationSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	secretLikeText := "malformed access-token should not appear in a notice"
	if err := os.WriteFile(path, []byte(secretLikeText), 0600); err != nil {
		t.Fatal(err)
	}

	session, err := NewPersistentSessionAt(Config{PrimaryModel: "primary", FallbackModel: "fallback"}, &historyTestProvider{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if session.startupNotice == "" || !strings.Contains(session.startupNotice, "invalid") {
		t.Fatalf("startup notice = %q, want explicit recovery notice", session.startupNotice)
	}
	if strings.Contains(session.startupNotice, secretLikeText) || strings.Contains(session.startupNotice, "access-token") {
		t.Fatalf("startup notice exposed malformed file contents: %q", session.startupNotice)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid conversation path still exists, stat error = %v", err)
	}
	backups, err := filepath.Glob(path + ".corrupt*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("quarantine files = %v, want one", backups)
	}
	backupInfo, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := backupInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("quarantine permissions = %04o, want 0600", got)
	}
}

func TestSessionStoreFailedSaveLeavesPreviousStateAndNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(path)
	if err := store.Save([]ChatMessage{{Role: "user", Content: "safe state"}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]ChatMessage{{Role: "system", Content: "must not be persisted"}}); err == nil {
		t.Fatal("invalid role save unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed save changed previous state: before %q, after %q", before, after)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".session-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files left after failed save: %v", temporary)
	}
}

func TestSessionStoreBoundsPersistedSizeAndKeepsNewestMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(path)
	messages := make([]ChatMessage, 0, maxSessionMessages+20)
	for index := 0; index < maxSessionMessages+20; index++ {
		messages = append(messages, ChatMessage{Role: "user", Content: "message " + string(rune('a'+index%26))})
	}
	if err := store.Save(messages); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxSessionFileBytes {
		t.Fatalf("conversation file size = %d, exceeds %d", info.Size(), maxSessionFileBytes)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != maxSessionMessages {
		t.Fatalf("persisted message count = %d, want %d", len(loaded.Messages), maxSessionMessages)
	}
	if loaded.Messages[0].Content != messages[len(messages)-maxSessionMessages].Content || loaded.Messages[len(loaded.Messages)-1].Content != messages[len(messages)-1].Content {
		t.Fatalf("persisted messages did not retain newest range: first=%q last=%q", loaded.Messages[0].Content, loaded.Messages[len(loaded.Messages)-1].Content)
	}
}

func TestSessionTrimsOutboundContextAndExplainsIt(t *testing.T) {
	provider := &historyTestProvider{}
	session := NewSession(Config{PrimaryModel: "primary", FallbackModel: "fallback"}, provider)
	session.history = []ChatMessage{
		{Role: "user", Content: strings.Repeat("oldest context ", 2500)},
		{Role: "assistant", Content: "recent useful answer"},
	}
	if _, err := session.Reply(context.Background(), "new question"); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 1 {
		t.Fatalf("history calls = %d, want 1", len(provider.histories))
	}
	history := provider.histories[0]
	if !containsContextNotice(history) {
		t.Fatalf("outbound history has no context notice: %#v", history)
	}
	if history[len(history)-1] != (ChatMessage{Role: "user", Content: "new question"}) {
		t.Fatalf("newest user message = %#v", history[len(history)-1])
	}
	if !containsMessage(history, "assistant", "recent useful answer") {
		t.Fatalf("newest useful assistant message was not retained: %#v", history)
	}
	if containsMessagePrefix(history, "oldest context ") {
		t.Fatal("oldest context was not trimmed")
	}
	if totalMessageTokens(history) > maxContextTokens {
		t.Fatalf("outbound history token estimate = %d, exceeds %d", totalMessageTokens(history), maxContextTokens)
	}
	if session.contextNotice != contextTrimNotice {
		t.Fatalf("session context notice = %q, want %q", session.contextNotice, contextTrimNotice)
	}
}

func containsMessage(history []ChatMessage, role, content string) bool {
	for _, message := range history {
		if message.Role == role && message.Content == content {
			return true
		}
	}
	return false
}

func containsMessagePrefix(history []ChatMessage, prefix string) bool {
	for _, message := range history {
		if strings.HasPrefix(message.Content, prefix) {
			return true
		}
	}
	return false
}

func totalMessageTokens(history []ChatMessage) int {
	total := 0
	for _, message := range history {
		total += estimateMessageTokens(message)
	}
	return total
}
