package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	memoryDirectoryName = "memory"
	memoryFilename      = "MEMORY.md"
	recentMemoryDays    = 3
	memoryDirectoryMode = 0700
	memoryFileMode      = 0600
)

// MemoryStore manages the Markdown memory files kept beside the GoAster
// session. MEMORY.md is deliberately read-only to the running chat session;
// durable-memory edits are an explicit, user-reviewed maintenance operation.
// Daily notes are append-only from GoAster's perspective.
//
// The layout is:
//
//	<workspace>/memory/MEMORY.md
//	<workspace>/memory/YYYYMM/YYYYMMDD.md
type MemoryStore struct {
	workspace  string
	memoryDir  string
	memoryFile string
	now        func() time.Time

	mu      sync.Mutex
	initErr error
}

// NewMemoryStore creates a Markdown memory store. An empty workspace uses
// ~/.goaster. Directory creation errors are retained by the store and are
// returned by AppendToday; read helpers treat unavailable memory as empty so
// an empty or unavailable memory store does not change normal chat behavior.
func NewMemoryStore(workspace string) *MemoryStore {
	store, _ := newMemoryStore(workspace)
	return store
}

// NewMemoryStoreWithError is the error-reporting constructor used by the
// persistent terminal session. It has no user-facing configuration surface.
func NewMemoryStoreWithError(workspace string) (*MemoryStore, error) {
	return newMemoryStore(workspace)
}

func newMemoryStore(workspace string) (*MemoryStore, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			store := &MemoryStore{initErr: fmt.Errorf("find home directory: %w", err)}
			return store, store.initErr
		}
		workspace = filepath.Join(home, sessionDirectoryName)
	}
	workspace = filepath.Clean(workspace)
	store := &MemoryStore{
		workspace:  workspace,
		memoryDir:  filepath.Join(workspace, memoryDirectoryName),
		memoryFile: filepath.Join(workspace, memoryDirectoryName, memoryFilename),
		now:        time.Now,
	}
	if err := ensureMemoryWorkspace(store.workspace); err != nil {
		store.initErr = err
		return store, err
	}
	if err := ensurePrivateDirectory(store.memoryDir, "memory directory"); err != nil {
		store.initErr = err
		return store, err
	}
	return store, nil
}

// WorkspacePath returns the root directory used by this store.
func (ms *MemoryStore) WorkspacePath() string {
	if ms == nil {
		return ""
	}
	return ms.workspace
}

// MemoryPath returns the protected long-term memory path.
func (ms *MemoryStore) MemoryPath() string {
	if ms == nil {
		return ""
	}
	return ms.memoryFile
}

// DailyNotePath returns the Markdown path for date.
func (ms *MemoryStore) DailyNotePath(date time.Time) string {
	if ms == nil {
		return ""
	}
	dateString := date.Format("20060102")
	return filepath.Join(ms.memoryDir, dateString[:6], dateString+".md")
}

func (ms *MemoryStore) today() time.Time {
	if ms == nil || ms.now == nil {
		return time.Now()
	}
	return ms.now()
}

// ReadLongTerm reads MEMORY.md. Missing, invalid, or unavailable memory is
// represented as an empty string so memory remains an additive feature.
func (ms *MemoryStore) ReadLongTerm() string {
	if ms == nil || ms.initErr != nil {
		return ""
	}
	data, err := readMemoryFile(ms.memoryFile)
	if err != nil {
		return ""
	}
	return string(data)
}

// ReadToday reads today's daily Markdown note, if present.
func (ms *MemoryStore) ReadToday() string {
	if ms == nil || ms.initErr != nil {
		return ""
	}
	data, err := readMemoryFile(ms.DailyNotePath(ms.today()))
	if err != nil {
		return ""
	}
	return string(data)
}

// AppendToday atomically appends content to today's note. A new note starts
// with a date heading. The old file remains untouched until the fully written,
// synced, restrictive temporary file is renamed into place.
func (ms *MemoryStore) AppendToday(content string) error {
	if ms == nil {
		return errors.New("memory store is nil")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.initErr != nil {
		return ms.initErr
	}
	if err := ensurePrivateDirectory(ms.memoryDir, "memory directory"); err != nil {
		return err
	}
	today := ms.today()
	todayFile := ms.DailyNotePath(today)
	monthDir := filepath.Dir(todayFile)
	if err := ensurePrivateDirectory(monthDir, "daily memory directory"); err != nil {
		return err
	}

	existing, err := readMemoryFile(todayFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read today's memory note: %w", err)
	}
	var next []byte
	if len(existing) == 0 {
		header := fmt.Sprintf("# %s\n\n", today.Format("2006-01-02"))
		next = []byte(header + content)
	} else {
		next = []byte(string(existing) + "\n" + content)
	}

	temporary, err := os.CreateTemp(monthDir, "."+filepath.Base(todayFile)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary daily memory note: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(memoryFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary daily memory note: %w", err)
	}
	n, err := temporary.Write(next)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write daily memory note: %w", err)
	}
	if n != len(next) {
		_ = temporary.Close()
		return errors.New("write daily memory note: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync daily memory note: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary daily memory note: %w", err)
	}
	if err := os.Rename(temporaryName, todayFile); err != nil {
		return fmt.Errorf("replace daily memory note: %w", err)
	}
	if err := os.Chmod(todayFile, memoryFileMode); err != nil {
		return fmt.Errorf("restrict daily memory note: %w", err)
	}
	syncMemoryDirectory(monthDir)
	return nil
}

// GetRecentDailyNotes returns up to the last days of notes, newest first.
// Notes are separated with a Markdown horizontal rule, matching PicoClaw's
// memory context format.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	if ms == nil || ms.initErr != nil || days <= 0 {
		return ""
	}
	notes := make([]string, 0, days)
	for index := 0; index < days; index++ {
		date := ms.today().AddDate(0, 0, -index)
		data, err := readMemoryFile(ms.DailyNotePath(date))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		notes = append(notes, string(data))
	}
	return strings.Join(notes, "\n\n---\n\n")
}

// GetMemoryContext returns formatted long-term and recent daily memory for a
// normal model request. It is empty when no usable memory exists.
func (ms *MemoryStore) GetMemoryContext() string {
	if ms == nil || ms.initErr != nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if longTerm := ms.ReadLongTerm(); strings.TrimSpace(longTerm) != "" {
		parts = append(parts, "## Long-term Memory\n\n"+longTerm)
	}
	if recent := ms.GetRecentDailyNotes(recentMemoryDays); strings.TrimSpace(recent) != "" {
		parts = append(parts, "## Recent Daily Notes\n\n"+recent)
	}
	if len(parts) == 0 {
		return ""
	}
	return "# Memory\n\n" + strings.Join(parts, "\n\n---\n\n")
}

// WithMemoryContext carries user-managed Markdown memory to a provider
// without adding it to conversation history or the persisted session summary.
func WithMemoryContext(ctx context.Context, memory string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(memory) == "" {
		return ctx
	}
	return context.WithValue(ctx, memoryContextKey{}, memory)
}

// MemoryContextFromContext retrieves memory carried by WithMemoryContext.
// Providers that do not understand the value can safely ignore it.
func MemoryContextFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if memory, ok := ctx.Value(memoryContextKey{}).(string); ok {
		return memory
	}
	return ""
}

type memoryContextKey struct{}

func ensureMemoryWorkspace(workspace string) error {
	if err := ensureDirectory(workspace, "memory workspace"); err != nil {
		return err
	}
	// The default workspace also contains credentials and session state. Keep
	// that known GoAster directory private without changing arbitrary custom
	// parent directories used by tests or callers.
	if filepath.Base(workspace) == sessionDirectoryName {
		if err := os.Chmod(workspace, memoryDirectoryMode); err != nil {
			return fmt.Errorf("restrict memory workspace: %w", err)
		}
	}
	return nil
}

func ensurePrivateDirectory(path, label string) error {
	if err := ensureDirectory(path, label); err != nil {
		return err
	}
	if err := os.Chmod(path, memoryDirectoryMode); err != nil {
		return fmt.Errorf("restrict %s: %w", label, err)
	}
	return nil
}

func ensureDirectory(path, label string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s path is empty", label)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, memoryDirectoryMode); err != nil {
			return fmt.Errorf("create %s: %w", label, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", label)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", label)
	}
	return nil
}

func readMemoryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("memory file must not be a symbolic link: %s", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("memory file must be a regular file: %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func syncMemoryDirectory(directory string) {
	directoryFile, err := os.Open(directory)
	if err != nil {
		return
	}
	defer directoryFile.Close()
	_ = directoryFile.Sync()
}
