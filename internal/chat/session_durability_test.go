package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// Both child processes load the same version before either is allowed to save.
func TestSessionStoreRejectsStaleProcess(t *testing.T) {
	requirePrivateSessionPermissions(t)
	path := filepath.Join(t.TempDir(), "session.json")
	type writer struct {
		cmd   *exec.Cmd
		input *bufio.Writer
	}
	start := func() writer {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionStoreProcessWriter$")
		cmd.Env = append(os.Environ(), "GOASTER_SESSION_TEST_PATH="+path)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill() })
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("child ready: %q, %v", line, err)
		}
		return writer{cmd, bufio.NewWriter(input)}
	}
	first, second := start(), start()
	for i, child := range []writer{first, second} {
		fmt.Fprintln(child.input, "save")
		if err := child.input.Flush(); err != nil {
			t.Fatal(err)
		}
		err := child.cmd.Wait()
		if i == 0 && err != nil {
			t.Fatalf("first writer: %v", err)
		}
		if i == 1 {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("stale writer: %v, want exit 3", err)
			}
		}
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Messages, []ChatMessage{{Role: "user", Content: "winning history"}}) {
		t.Fatalf("history overwritten: %+v", loaded)
	}
}

func TestSessionStoreProcessWriter(t *testing.T) {
	path := os.Getenv("GOASTER_SESSION_TEST_PATH")
	if path == "" {
		return
	}
	store := NewSessionStoreAt(path)
	if _, err := store.Load(); err != nil {
		os.Exit(4)
	}
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		os.Exit(4)
	}
	if err := store.Save([]ChatMessage{{Role: "user", Content: "winning history"}}); err != nil {
		if strings.Contains(err.Error(), "changed in another instance") {
			os.Exit(3)
		}
		os.Exit(4)
	}
	os.Exit(0)
}

func TestSessionStoreRejectsStaleLoadedHistory(t *testing.T) {
	requirePrivateSessionPermissions(t)
	path := filepath.Join(t.TempDir(), "session.json")
	first, stale := NewSessionStoreAt(path), NewSessionStoreAt(path)
	if err := first.Save([]ChatMessage{{Role: "user", Content: "original"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Load(); err != nil {
		t.Fatal(err)
	}
	latest := []ChatMessage{{Role: "user", Content: "newer history"}}
	if err := first.Save(latest); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save([]ChatMessage{{Role: "user", Content: "stale history"}}); err == nil {
		t.Fatal("stale history overwrote newer history")
	}
	if err := NewSessionStoreAt(path).Save(nil); err == nil {
		t.Fatal("unloaded store overwrote existing history")
	}
	loaded, err := stale.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Messages, latest) {
		t.Fatalf("latest history = %+v", loaded.Messages)
	}
	if err := stale.Save(latest); err != nil {
		t.Fatalf("save after reload: %v", err)
	}
}

// Each session must compare against its own loaded version, even when a shared
// store advances through another session's save or a public Load.
func TestSessionsSharingStoreRejectStaleHistory(t *testing.T) {
	requirePrivateSessionPermissions(t)
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compaction=%t", compact), func(t *testing.T) {
			store := NewSessionStoreAt(filepath.Join(t.TempDir(), "session.json"))
			count := 2
			if compact {
				count = maxSessionMessages
			}
			if err := store.Save(numberedHistory(count)); err != nil {
				t.Fatal(err)
			}
			config := Config{PrimaryModel: "primary"}
			first, err := NewSessionWithStore(config, &summaryTestProvider{}, store)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := NewSessionWithStore(config, &summaryTestProvider{}, store)
			if err != nil {
				t.Fatal(err)
			}
			oldVersion := stale.storeVersion
			oldHistory := cloneChatMessages(stale.history)
			for _, message := range []string{"winning turn", "another winning turn"} {
				if _, err := first.Reply(context.Background(), message); err != nil {
					t.Fatalf("winning session: %v", err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				reply, err := stale.Reply(context.Background(), "stale turn")
				if err == nil || !strings.Contains(err.Error(), "changed in another instance") || reply != "" {
					t.Fatalf("stale reply = %q, %v; want version conflict", reply, err)
				}
				if stale.storeVersion != oldVersion || !reflect.DeepEqual(stale.history, oldHistory) || stale.summary != "" {
					t.Fatal("failed save changed stale session state or version")
				}
				loaded, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(loaded.Messages, first.history) || loaded.Summary != first.summary {
					t.Fatalf("winning history overwritten: %+v", loaded)
				}
			}
		})
	}
}

func TestSessionStoreLockPreventsLoadAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(path)
	unlock, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other := NewSessionStoreAt(path)
	if _, err := other.Load(); err == nil {
		t.Fatal("load ignored held lock")
	}
	if err := other.Save(nil); err == nil {
		t.Fatal("save ignored held lock")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked save changed state: %v", err)
	}
}

func TestSessionStoreConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	stores := []*SessionStore{NewSessionStoreAt(path), NewSessionStoreAt(path)}
	for _, store := range stores {
		if _, err := store.Load(); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(stores))
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *SessionStore) {
			defer wg.Done()
			results <- store.Save([]ChatMessage{{Role: "user", Content: fmt.Sprint(i)}})
		}(i, store)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful writers = %d, want 1", successes)
	}
}

type failingSaveProvider struct {
	summaryTestProvider
	beforeReply func()
}

func (p *failingSaveProvider) ReplyWithHistory(ctx context.Context, model string, history []ChatMessage) (string, error) {
	if p.beforeReply != nil {
		p.beforeReply()
	}
	return p.summaryTestProvider.ReplyWithHistory(ctx, model, history)
}

func TestSessionFailedSaveDoesNotRetainUndisplayedTurn(t *testing.T) {
	requirePrivateSessionPermissions(t)
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compaction=%t", compact), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			provider := &failingSaveProvider{}
			session, err := NewPersistentSessionAt(Config{PrimaryModel: "primary"}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			session.summary = "prior summary"
			session.history = numberedHistory(4)
			if compact {
				session.history = numberedHistory(maxSessionMessages)
			}
			if err := session.store.saveStateWithVersion(session.summary, session.history, &session.storeVersion); err != nil {
				t.Fatal(err)
			}
			wantHistory, wantSummary := cloneChatMessages(session.history), session.summary
			if compact {
				wantHistory = cloneChatMessages(session.history[maxSessionMessages-recentMessagesToKeep+1:])
				wantSummary = "important earlier facts"
			}
			provider.beforeReply = func() {
				if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reply, err := session.Reply(context.Background(), "undisplayed question")
			if err == nil || reply != "" {
				t.Fatalf("reply = %q, %v; want save failure", reply, err)
			}
			if !reflect.DeepEqual(session.history, wantHistory) || session.summary != wantSummary {
				t.Fatalf("failed save retained turn or lost checkpoint: %q, %+v", session.summary, session.history)
			}
			if err := os.Remove(path + ".lock"); err != nil {
				t.Fatal(err)
			}
			provider.beforeReply = nil
			if _, err := session.Reply(context.Background(), "retry question"); err != nil {
				t.Fatal(err)
			}
			_, histories := provider.calls()
			if containsMessage(histories[len(histories)-1], "user", "undisplayed question") {
				t.Fatal("retry included undisplayed turn")
			}
		})
	}
}

type testSessionDirectory struct {
	syncErr, closeErr error
	closed            bool
}

func (d *testSessionDirectory) Sync() error  { return d.syncErr }
func (d *testSessionDirectory) Close() error { d.closed = true; return d.closeErr }

func TestSessionDirectorySyncErrors(t *testing.T) {
	for _, tc := range []struct {
		name              string
		syncErr, closeErr error
		wantFailure       bool
	}{
		{"success", nil, nil, false},
		{"io failure", syscall.EIO, nil, true},
		{"unsupported", syscall.ENOTSUP, nil, false},
		{"invalid operation", syscall.EINVAL, nil, false},
		{"not implemented", syscall.ENOSYS, nil, false},
		{"access denied", syscall.EACCES, nil, runtime.GOOS != "windows"},
		{"close failure", syscall.EINVAL, syscall.EIO, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := &testSessionDirectory{syncErr: tc.syncErr, closeErr: tc.closeErr}
			err := syncSessionDirectoryFile(dir)
			if (err != nil) != tc.wantFailure || !dir.closed {
				t.Fatalf("sync = %v, closed = %t", err, dir.closed)
			}
		})
	}
	if err := syncSessionDirectory(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("directory open failure ignored")
	}
}

func TestSessionSaveReportsDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	provider := &historyTestProvider{}
	session, err := NewPersistentSessionAt(Config{PrimaryModel: "primary"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	session.store.syncDirectory = func(string) error { return syscall.EIO }
	reply, err := session.Reply(context.Background(), "undisplayed")
	if !errors.Is(err, syscall.EIO) || reply != "" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	if len(session.history) != 0 || session.summary != "" {
		t.Fatal("directory sync failure retained undisplayed turn")
	}
	// Rename has already happened, so a failed sync has an uncertain durable
	// outcome. The store must reject a later write until it is reloaded.
	session.store.syncDirectory = nil
	if err := session.store.Save(nil); err == nil {
		t.Fatal("uncertain replacement allowed another write without reload")
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock leaked: %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".session-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files = %v, %v", temps, err)
	}
}

// The existing session loader requires Unix-style private mode bits. Skip
// round-trip tests on filesystems (including Windows) that cannot supply them.
func requirePrivateSessionPermissions(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "permissions")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Skip("filesystem cannot provide the private permissions required by SessionStore.Load")
	}
}
