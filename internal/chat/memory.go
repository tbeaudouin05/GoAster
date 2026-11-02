package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"goaster/internal/filesync"
)

const (
	artefactsDirectoryName     = "artefacts"
	memoryDirectoryName        = "memory"
	memoryFilename             = "MEMORY.md"
	projectMemoryDirectoryName = "projects"
	projectMemoryExtension     = ".md"
	recentMemoryDays           = 3
	memoryDirectoryMode        = 0700
	memoryFileMode             = 0600
	maxProjectMemoryBytes      = 128 << 10
	maxProjectMemories         = 64
)

var projectMemoryMu sync.Mutex

// MemoryStore manages the Markdown memory files kept under GoAster's artefacts
// root. MEMORY.md is deliberately read-only to the running chat session;
// shared durable-memory edits are an explicit, user-reviewed maintenance
// operation. Daily notes are append-only from GoAster's perspective. Project
// memories are managed separately by the project-memory workflow.
//
// The layout is:
//
//	<workspace>/artefacts/memory/MEMORY.md
//	<workspace>/artefacts/memory/YYYYMM/YYYYMMDD.md
//	<workspace>/artefacts/memory/projects/<project>.md
type MemoryStore struct {
	workspace  string
	memoryDir  string
	memoryFile string
	now        func() time.Time

	mu            sync.Mutex
	initErr       error
	syncDirectory func(string) error
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
		memoryDir:  filepath.Join(workspace, artefactsDirectoryName, memoryDirectoryName),
		memoryFile: filepath.Join(workspace, artefactsDirectoryName, memoryDirectoryName, memoryFilename),
		now:        time.Now,
	}
	if err := ensureMemoryWorkspace(store.workspace); err != nil {
		store.initErr = err
		return store, err
	}
	if err := ensurePrivateDirectory(filepath.Join(store.workspace, artefactsDirectoryName), "artefacts directory"); err != nil {
		store.initErr = err
		return store, err
	}
	if err := ensurePrivateDirectory(store.memoryDir, "memory directory"); err != nil {
		store.initErr = err
		return store, err
	}
	if err := ensurePrivateDirectory(projectMemoryDirectory(store.memoryDir), "project memory directory"); err != nil {
		store.initErr = err
		return store, err
	}
	return store, nil
}

// ProjectMemoryDirectory returns the project-memory directory for a GoAster
// workspace. Project memories stay below the same memory folder as MEMORY.md
// and daily notes so exporting that folder includes the complete memory set.
func ProjectMemoryDirectory(workspace string) string {
	memoryDir := projectMemoryWorkspaceDirectory(workspace)
	if memoryDir == "" {
		return ""
	}
	return projectMemoryDirectory(memoryDir)
}

func projectMemoryWorkspaceDirectory(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		workspace = filepath.Join(home, sessionDirectoryName)
	}
	return filepath.Join(filepath.Clean(workspace), artefactsDirectoryName, memoryDirectoryName)
}

// ProjectMemoryPath returns the private Markdown path for a valid project
// memory name. An empty result means the workspace or name is invalid.
func ProjectMemoryPath(workspace, name string) string {
	directory := ProjectMemoryDirectory(workspace)
	if directory == "" {
		return ""
	}
	return projectMemoryPath(directory, name)
}

// DiscoverProjectMemories returns valid project-memory names in stable order.
// Missing project-memory directories are treated as an empty collection.
func DiscoverProjectMemories(workspace string) ([]string, error) {
	directory := projectMemoryWorkspaceDirectory(workspace)
	if directory == "" {
		return nil, errors.New("project memory directory is unavailable")
	}
	return discoverProjectMemories(directory)
}

// ReadProjectMemory reads one bounded project-memory file.
func ReadProjectMemory(workspace, name string) (string, error) {
	directory := projectMemoryWorkspaceDirectory(workspace)
	if directory == "" {
		return "", errors.New("project memory directory is unavailable")
	}
	data, err := readProjectMemory(directory, name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CreateProjectMemory creates a new bounded project-memory file. Empty
// content receives the standard project-memory template.
func CreateProjectMemory(workspace, name, content string) error {
	directory := projectMemoryWorkspaceDirectory(workspace)
	if directory == "" {
		return errors.New("project memory directory is unavailable")
	}
	return writeProjectMemory(directory, name, content, true, nil)
}

// WriteProjectMemory atomically replaces one existing bounded project-memory
// file and keeps the file private.
func WriteProjectMemory(workspace, name, content string) error {
	directory := projectMemoryWorkspaceDirectory(workspace)
	if directory == "" {
		return errors.New("project memory directory is unavailable")
	}
	return writeProjectMemory(directory, name, content, false, nil)
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
	syncDirectory := ms.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncMemoryDirectory
	}
	if err := syncDirectory(monthDir); err != nil {
		return fmt.Errorf("sync daily memory directory: %w; the note was replaced, so check its contents before retrying", err)
	}
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

func projectMemoryDirectory(memoryDir string) string {
	if strings.TrimSpace(memoryDir) == "" {
		return ""
	}
	return filepath.Join(memoryDir, projectMemoryDirectoryName)
}

func validProjectMemoryName(name string) bool {
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 80 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

func projectMemoryPath(directory, name string) string {
	name = strings.TrimSpace(name)
	if directory == "" || !validProjectMemoryName(name) {
		return ""
	}
	return filepath.Join(directory, name+projectMemoryExtension)
}

func discoverProjectMemories(memoryDir string) ([]string, error) {
	directory := projectMemoryDirectory(memoryDir)
	if directory == "" {
		return nil, errors.New("project memory directory is empty")
	}
	if err := validateProjectMemoryParents(memoryDir); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect project memory directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("project memory directory must be a real directory: %s", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read project memory directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect project memory entry: %w", err)
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("project memory entry must not be a symbolic link: %s", entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if !entryInfo.IsDir() && !entryInfo.Mode().IsRegular() {
				return nil, fmt.Errorf("project memory temporary entry is not regular: %s", entry.Name())
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("project memory entry must be a regular file: %s", entry.Name())
		}
		if !strings.HasSuffix(entry.Name(), projectMemoryExtension) {
			return nil, fmt.Errorf("project memory file must use the %s extension: %s", projectMemoryExtension, entry.Name())
		}
		name := strings.TrimSuffix(entry.Name(), projectMemoryExtension)
		if !validProjectMemoryName(name) {
			return nil, fmt.Errorf("invalid project memory name: %s", name)
		}
		if len(names) >= maxProjectMemories {
			return nil, fmt.Errorf("too many project memories")
		}
		if _, err := readProjectMemoryFile(path); err != nil {
			return nil, fmt.Errorf("read project memory %s: %w", name, err)
		}
		names = append(names, name)
	}
	return names, nil
}

func readProjectMemory(memoryDir, name string) ([]byte, error) {
	if err := validateProjectMemoryParents(memoryDir); err != nil {
		return nil, err
	}
	path := projectMemoryPath(projectMemoryDirectory(memoryDir), name)
	if path == "" {
		return nil, errors.New("invalid project memory name")
	}
	return readProjectMemoryFile(path)
}

func validateProjectMemoryParents(memoryDir string) error {
	if strings.TrimSpace(memoryDir) == "" {
		return errors.New("project memory directory is empty")
	}
	for _, directory := range []string{filepath.Dir(filepath.Dir(memoryDir)), filepath.Dir(memoryDir), memoryDir} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect project memory parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("project memory parent must be a real directory: %s", directory)
		}
	}
	return nil
}

func readProjectMemoryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("project memory file must not be a symbolic link: %s", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("project memory file must be a regular file: %s", filepath.Base(path))
	}
	if info.Size() > maxProjectMemoryBytes {
		return nil, fmt.Errorf("project memory file exceeds %d bytes", maxProjectMemoryBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened project memory file: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect project memory file: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return nil, errors.New("project memory file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxProjectMemoryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read project memory file: %w", err)
	}
	if len(data) > maxProjectMemoryBytes {
		return nil, fmt.Errorf("project memory file exceeds %d bytes", maxProjectMemoryBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("project memory file must contain valid UTF-8")
	}
	return data, nil
}

func writeProjectMemory(memoryDir, name, content string, createOnly bool, syncDirectory func(string) error) error {
	path := projectMemoryPath(projectMemoryDirectory(memoryDir), name)
	if path == "" {
		return errors.New("invalid project memory name")
	}
	if !utf8.ValidString(content) {
		return errors.New("project memory content must contain valid UTF-8")
	}
	if createOnly && strings.TrimSpace(content) == "" {
		content = projectMemoryTemplate(name)
	}
	if len(content) > maxProjectMemoryBytes {
		return fmt.Errorf("project memory content exceeds %d bytes", maxProjectMemoryBytes)
	}
	if err := ensureMemoryWorkspace(filepath.Dir(filepath.Dir(memoryDir))); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(memoryDir), "artefacts directory"); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(memoryDir, "memory directory"); err != nil {
		return err
	}
	directory := projectMemoryDirectory(memoryDir)
	if err := ensurePrivateDirectory(directory, "project memory directory"); err != nil {
		return err
	}

	projectMemoryMu.Lock()
	defer projectMemoryMu.Unlock()
	info, err := os.Lstat(path)
	if createOnly {
		if err == nil {
			return fmt.Errorf("project memory %q already exists", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect project memory %q: %w", name, err)
		}
	} else {
		if err != nil {
			return fmt.Errorf("inspect project memory %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("project memory file must be a regular file: %s", filepath.Base(path))
		}
	}

	temporary, err := os.CreateTemp(directory, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary project memory: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(memoryFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary project memory: %w", err)
	}
	data := []byte(content)
	n, err := temporary.Write(data)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write project memory: %w", err)
	}
	if n != len(data) {
		_ = temporary.Close()
		return errors.New("write project memory: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync project memory: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary project memory: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace project memory: %w", err)
	}
	if err := os.Chmod(path, memoryFileMode); err != nil {
		return fmt.Errorf("restrict project memory: %w", err)
	}
	if syncDirectory == nil {
		syncDirectory = syncMemoryDirectory
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync project memory directory: %w; the project memory was replaced, so check its contents before retrying", err)
	}
	return nil
}

func projectMemoryTemplate(name string) string {
	return "# Project Memory: " + name + "\n\n" +
		"## Project\n\n" +
		"Describe the project, its purpose, and stable boundaries.\n\n" +
		"## Current State\n\n" +
		"Record verified status, active work, and important unfinished items.\n\n" +
		"## Decisions and Constraints\n\n" +
		"Record durable decisions, conventions, and constraints.\n\n" +
		"## Lessons\n\n" +
		"Record verified lessons that will help with future work.\n"
}

func projectMemoryStatus(memoryDir, selected string) (string, error) {
	if selected != "" {
		if _, err := readProjectMemory(memoryDir, selected); err != nil {
			return "", fmt.Errorf("read selected project memory %q: %w", selected, err)
		}
		return "A project memory is selected: " + selected + ". Use its bounded content as context and update it when the task is complete.", nil
	}
	names, err := discoverProjectMemories(memoryDir)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "No project memory is selected and none exists. Before any task action, create a relevant project memory with project_memory, then use it.", nil
	}
	return "No project memory is selected. Before any task action, choose the relevant name with project_memory select; if none matches, create a new project memory. Available names: " + strings.Join(names, ", "), nil
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

func syncMemoryDirectory(directory string) error {
	return filesync.DirectoryPath(directory)
}
