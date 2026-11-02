package chat

import (
	"context"
	"errors"
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
	t.Setenv("USERPROFILE", home)
	store, err := NewMemoryStoreWithError("")
	if err != nil {
		t.Fatal(err)
	}

	wantWorkspace := filepath.Join(home, sessionDirectoryName)
	if store.WorkspacePath() != wantWorkspace {
		t.Fatalf("workspace = %q, want %q", store.WorkspacePath(), wantWorkspace)
	}
	if store.MemoryPath() != filepath.Join(wantWorkspace, artefactsDirectoryName, memoryDirectoryName, memoryFilename) {
		t.Fatalf("memory path = %q", store.MemoryPath())
	}
	date := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	wantDaily := filepath.Join(wantWorkspace, artefactsDirectoryName, memoryDirectoryName, "202610", "20261029.md")
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

	syncFailure := errors.New("simulated directory sync failure")
	store.syncDirectory = func(string) error { return syncFailure }
	if err := store.AppendToday("entry with uncertain durability"); !errors.Is(err, syncFailure) || !strings.Contains(err.Error(), "check its contents before retrying") {
		t.Fatalf("directory sync failure = %v", err)
	}
	if got := readFixture(t, path); !strings.Contains(got, "entry with uncertain durability") {
		t.Fatalf("replacement outcome was not preserved: %q", got)
	}
	if err := syncMemoryDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("memory directory open failure was ignored")
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
	session := NewSessionWithMemory(Config{Model: "primary"}, provider, store)
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
		Config{Model: "primary"},
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

func TestProjectMemoryUsesExportablePrivateLayout(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	store, err := NewMemoryStoreWithError(workspace)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(workspace, artefactsDirectoryName, memoryDirectoryName, projectMemoryDirectoryName)
	if got := ProjectMemoryDirectory(workspace); got != wantDirectory {
		t.Fatalf("project memory directory = %q, want %q", got, wantDirectory)
	}
	wantPath := filepath.Join(wantDirectory, "goaster.md")
	if got := ProjectMemoryPath(workspace, "goaster"); got != wantPath {
		t.Fatalf("project memory path = %q, want %q", got, wantPath)
	}
	if got := ProjectMemoryPath(workspace, "../outside"); got != "" {
		t.Fatalf("invalid project memory path = %q", got)
	}
	if filepath.Clean(wantPath) == filepath.Clean(store.MemoryPath()) || filepath.Dir(wantPath) == filepath.Dir(store.DailyNotePath(time.Now())) {
		t.Fatal("project memory overlaps protected or daily memory")
	}
	info, err := os.Stat(wantDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != memoryDirectoryMode {
		t.Fatalf("project memory directory permissions = %04o, want %04o", info.Mode().Perm(), memoryDirectoryMode)
	}

	if err := CreateProjectMemory(workspace, "goaster", "# GoAster\n\nKeep this context."); err != nil {
		t.Fatal(err)
	}
	names, err := DiscoverProjectMemories(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"goaster"}) {
		t.Fatalf("project memories = %#v, want [goaster]", names)
	}
	content, err := ReadProjectMemory(workspace, "goaster")
	if err != nil || content != "# GoAster\n\nKeep this context." {
		t.Fatalf("project memory content = %q, error = %v", content, err)
	}
	fileInfo, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != memoryFileMode {
		t.Fatalf("project memory permissions = %04o, want %04o", fileInfo.Mode().Perm(), memoryFileMode)
	}
	if temporary, err := filepath.Glob(filepath.Join(wantDirectory, ".*.tmp")); err != nil || len(temporary) != 0 {
		t.Fatalf("temporary project memory files = %v, error = %v", temporary, err)
	}
}

func TestProjectMemoryCreationAndPersistenceErrorsAreBounded(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	store := NewMemoryStore(workspace)
	if err := CreateProjectMemory(workspace, "demo", "initial"); err != nil {
		t.Fatal(err)
	}
	if err := CreateProjectMemory(workspace, "demo", "replacement"); err == nil {
		t.Fatal("duplicate project memory creation succeeded")
	}
	if err := CreateProjectMemory(workspace, "too-large", strings.Repeat("x", maxProjectMemoryBytes+1)); err == nil {
		t.Fatal("oversized project memory creation succeeded")
	}
	if err := writeProjectMemory(store.memoryDir, "missing", "content", false, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing project memory update = %v", err)
	}

	syncFailure := errors.New("simulated project directory sync failure")
	store.syncDirectory = func(string) error { return syncFailure }
	if err := writeProjectMemory(store.memoryDir, "demo", "updated", false, store.syncDirectory); !errors.Is(err, syncFailure) || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("project memory sync failure = %v", err)
	}
	content, err := ReadProjectMemory(workspace, "demo")
	if err != nil || content != "updated" {
		t.Fatalf("replacement after sync failure = %q, error = %v", content, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), memoryFileMode); err != nil {
		t.Fatal(err)
	}
	link := ProjectMemoryPath(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := ReadProjectMemory(workspace, "link"); err == nil {
		t.Fatal("symlinked project memory was read")
	}
	if _, err := DiscoverProjectMemories(workspace); err == nil {
		t.Fatal("discovery accepted a symlinked project memory")
	}
}

func TestSelectedProjectMemoryPersistsAndReachesTheProvider(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	memory := NewMemoryStore(workspace)
	if err := CreateProjectMemory(workspace, "demo", "durable project constraint"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	provider := &memoryContextProvider{}
	session, err := NewSessionWithStoreAndMemory(Config{Model: "primary"}, provider, NewSessionStoreAt(path), memory)
	if err != nil {
		t.Fatal(err)
	}
	version, err := session.store.saveStateWithProjectMemory("", nil, nil, "demo", session.storeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	session.storeVersion = version
	session.selectedProjectMemory = "demo"

	restarted, err := NewSessionWithStoreAndMemory(Config{Model: "primary"}, provider, NewSessionStoreAt(path), memory)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.selectedProjectMemory != "demo" {
		t.Fatalf("selected project memory after restart = %q", restarted.selectedProjectMemory)
	}
	if _, err := restarted.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	got, _ := provider.snapshot()
	if !strings.Contains(got, "Selected Project Memory") || !strings.Contains(got, "durable project constraint") {
		t.Fatalf("provider project memory context = %q", got)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), `"selected_project_memory": "demo"`) {
		t.Fatalf("saved selection missing: %s", saved)
	}
}
