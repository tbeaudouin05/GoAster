package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreUsesDefaultWorkspaceLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := NewMemoryStoreWithError("")
	if err != nil {
		t.Fatal(err)
	}

	wantWorkspace := filepath.Join(home, sessionDirectoryName)
	if store.WorkspacePath() != wantWorkspace {
		t.Fatalf("workspace = %q, want %q", store.WorkspacePath(), wantWorkspace)
	}
	if store.MemoryPath() != filepath.Join(wantWorkspace, memoryDirectoryName, memoryFilename) {
		t.Fatalf("memory path = %q", store.MemoryPath())
	}
	date := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	wantDaily := filepath.Join(wantWorkspace, memoryDirectoryName, "202610", "20261029.md")
	if got := store.DailyNotePath(date); got != wantDaily {
		t.Fatalf("daily path = %q, want %q", got, wantDaily)
	}

	for _, path := range []string{wantWorkspace, filepath.Dir(store.MemoryPath())} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != memoryDirectoryMode {
			t.Fatalf("directory %s permissions = %04o, want %04o", path, got, memoryDirectoryMode)
		}
	}
}

func TestMemoryStoreMissingFilesAreEmpty(t *testing.T) {
	store := NewMemoryStore(filepath.Join(t.TempDir(), "workspace"))
	store.now = func() time.Time { return time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC) }

	if got := store.ReadLongTerm(); got != "" {
		t.Fatalf("missing long-term memory = %q, want empty", got)
	}
	if got := store.ReadToday(); got != "" {
		t.Fatalf("missing daily memory = %q, want empty", got)
	}
	if got := store.GetRecentDailyNotes(3); got != "" {
		t.Fatalf("missing recent notes = %q, want empty", got)
	}
	if got := store.GetMemoryContext(); got != "" {
		t.Fatalf("empty memory context = %q, want empty", got)
	}
}

func TestMemoryStoreRecentNotesAreNewestFirstAndSeparated(t *testing.T) {
	store := NewMemoryStore(filepath.Join(t.TempDir(), "workspace"))
	base := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	if err := os.WriteFile(store.MemoryPath(), []byte("durable fact"), memoryFileMode); err != nil {
		t.Fatal(err)
	}
	for _, note := range []struct {
		date time.Time
		text string
	}{
		{base, "# 2026-10-29\n\nnewest"},
		{base.AddDate(0, 0, -1), "# 2026-10-28\n\nprevious"},
	} {
		path := store.DailyNotePath(note.date)
		if err := os.MkdirAll(filepath.Dir(path), memoryDirectoryMode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(note.text), memoryFileMode); err != nil {
			t.Fatal(err)
		}
	}

	wantNotes := "# 2026-10-29\n\nnewest\n\n---\n\n# 2026-10-28\n\nprevious"
	if got := store.GetRecentDailyNotes(3); got != wantNotes {
		t.Fatalf("recent notes = %q, want %q", got, wantNotes)
	}
	wantContext := "# Memory\n\n## Long-term Memory\n\ndurable fact\n\n---\n\n## Recent Daily Notes\n\n" + wantNotes
	if got := store.GetMemoryContext(); got != wantContext {
		t.Fatalf("memory context = %q, want %q", got, wantContext)
	}
}

func TestMemoryStoreAppendTodayIsAtomicAndPrivate(t *testing.T) {
	store := NewMemoryStore(filepath.Join(t.TempDir(), "workspace"))
	date := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return date }
	if err := store.AppendToday("first entry"); err != nil {
		t.Fatal(err)
	}
	path := store.DailyNotePath(date)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "# 2026-10-29\n\nfirst entry" {
		t.Fatalf("first note = %q", first)
	}
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AppendToday("second entry"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# 2026-10-29\n\nfirst entry\nsecond entry" {
		t.Fatalf("appended note = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != memoryFileMode {
		t.Fatalf("daily note permissions = %04o, want %04o", got, memoryFileMode)
	}
	if got := firstInfo.Mode().Perm(); got != memoryFileMode {
		t.Fatalf("first daily note permissions = %04o, want %04o", got, memoryFileMode)
	}
	monthInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := monthInfo.Mode().Perm(); got != memoryDirectoryMode {
		t.Fatalf("month directory permissions = %04o, want %04o", got, memoryDirectoryMode)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".20261029.md-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files left after atomic append: %v", temporary)
	}

	const concurrentAppends = 8
	var wait sync.WaitGroup
	for index := 0; index < concurrentAppends; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if err := store.AppendToday(fmt.Sprintf("concurrent-%d", index)); err != nil {
				t.Errorf("append %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < concurrentAppends; index++ {
		if !strings.Contains(string(final), fmt.Sprintf("concurrent-%d", index)) {
			t.Fatalf("concurrent append %d was lost: %q", index, final)
		}
	}
}

type memoryContextProvider struct {
	mu        sync.Mutex
	memory    string
	histories [][]ChatMessage
}

func (p *memoryContextProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return p.ReplyWithHistory(ctx, model, []ChatMessage{{Role: "user", Content: message}})
}

func (p *memoryContextProvider) ReplyWithHistory(ctx context.Context, _ string, history []ChatMessage) (string, error) {
	p.mu.Lock()
	p.memory = MemoryContextFromContext(ctx)
	p.histories = append(p.histories, cloneChatMessages(history))
	p.mu.Unlock()
	return "reply", nil
}

func (p *memoryContextProvider) snapshot() (string, [][]ChatMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.memory, append([][]ChatMessage(nil), p.histories...)
}

func TestSessionInjectsMemoryContextWithoutChangingHistory(t *testing.T) {
	store := NewMemoryStore(filepath.Join(t.TempDir(), "workspace"))
	if err := os.WriteFile(store.MemoryPath(), []byte("remember this fact"), memoryFileMode); err != nil {
		t.Fatal(err)
	}
	provider := &memoryContextProvider{}
	session := NewSessionWithMemory(Config{PrimaryModel: "primary"}, provider, store)
	if _, err := session.Reply(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	memory, histories := provider.snapshot()
	if memory != store.GetMemoryContext() {
		t.Fatalf("provider memory = %q, want %q", memory, store.GetMemoryContext())
	}
	if len(histories) != 1 || !reflect.DeepEqual(histories[0], []ChatMessage{{Role: "user", Content: "hello"}}) {
		t.Fatalf("provider history = %#v, want only the user turn", histories)
	}
}

type memorySummaryProvider struct {
	mu             sync.Mutex
	summaryContext string
	replyContext   string
}

func (p *memorySummaryProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return p.ReplyWithHistory(ctx, model, []ChatMessage{{Role: "user", Content: message}})
}

func (p *memorySummaryProvider) ReplyWithHistory(ctx context.Context, _ string, _ []ChatMessage) (string, error) {
	p.mu.Lock()
	p.replyContext = MemoryContextFromContext(ctx)
	p.mu.Unlock()
	return "reply", nil
}

func (p *memorySummaryProvider) SummarizeConversation(ctx context.Context, _ string, _ string, _ []ChatMessage) (string, error) {
	p.mu.Lock()
	p.summaryContext = MemoryContextFromContext(ctx)
	p.mu.Unlock()
	return "compacted conversation", nil
}

func (p *memorySummaryProvider) contexts() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summaryContext, p.replyContext
}

func TestConversationSummarizationDoesNotWriteProtectedMemory(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	memory := NewMemoryStore(workspace)
	if err := os.WriteFile(memory.MemoryPath(), []byte("protected durable knowledge"), memoryFileMode); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(memory.MemoryPath())
	if err != nil {
		t.Fatal(err)
	}

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	provider := &memorySummaryProvider{}
	session, err := NewSessionWithStoreAndMemory(
		Config{PrimaryModel: "primary"},
		provider,
		NewSessionStoreAt(sessionPath),
		memory,
	)
	if err != nil {
		t.Fatal(err)
	}
	session.history = numberedHistory(maxSessionMessages)
	if _, err := session.Reply(context.Background(), "latest question"); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(memory.MemoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("protected memory changed during summarization: before %q, after %q", before, after)
	}
	summaryContext, replyContext := provider.contexts()
	if summaryContext != "" {
		t.Fatalf("memory was injected into summarization context: %q", summaryContext)
	}
	if replyContext != memory.GetMemoryContext() {
		t.Fatalf("memory was not injected into normal reply context: %q", replyContext)
	}
	loaded, err := NewSessionStoreAt(sessionPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != "compacted conversation" {
		t.Fatalf("session summary = %q, want conversation summary", loaded.Summary)
	}
	if filepath.Clean(memory.MemoryPath()) == filepath.Clean(sessionPath) {
		t.Fatal("protected memory and session paths overlap")
	}
}
