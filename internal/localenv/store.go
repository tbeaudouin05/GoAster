// Package localenv parses and stores GoAster's local integration settings.
package localenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"goaster/internal/filesync"
)

const (
	directoryName = ".goaster"
	fileName      = "env"
	maxLineSize   = 1024 * 1024
	// MaxFileBytes bounds both imported environment documents and the saved
	// settings file.
	MaxFileBytes = 1024 * 1024
	maxFileSize  = MaxFileBytes
)

// ErrChanged means that a store loaded an older version of the settings file.
// Callers must load and review the current settings before replacing them.
var ErrChanged = errors.New("environment settings changed in another instance")

var environmentProcessMu sync.Mutex

// Store keeps environment settings separate from chat messages and providers.
type Store struct {
	path   string
	values map[string]string
	state  map[storeStateKey]storeState
}

type storeStateKey struct{}

type storeState struct {
	version string
	loaded  bool
}

// NewStore locates the default settings file at ~/.goaster/env.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	return NewStoreAt(filepath.Join(home, directoryName, fileName)), nil
}

// NewStoreAt creates a store for path. It is useful for isolated local setups.
func NewStoreAt(path string) *Store {
	state := make(map[storeStateKey]storeState)
	state[storeStateKey{}] = storeState{version: Digest(nil), loaded: true}
	return &Store{path: path, values: make(map[string]string), state: state}
}

// Values returns a copy of the values loaded or saved by this store.
func (s *Store) Values() map[string]string {
	return clone(s.values)
}

// LoadFile reads and validates KEY=VALUE settings from a local regular file.
// It never changes a Store; callers can review the returned values first.
func LoadFile(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("environment file path is empty")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect environment file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("environment file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("environment file must be a regular file")
	}
	if info.Mode().Perm()&0444 == 0 {
		return nil, fmt.Errorf("environment file is not readable")
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("environment file exceeds the 1 MiB limit")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open environment file: %w", err)
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened environment file: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect environment file: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return nil, fmt.Errorf("environment file changed while it was opened")
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("environment file must be a regular file")
	}
	if openedInfo.Mode().Perm()&0444 == 0 {
		return nil, fmt.Errorf("environment file is not readable")
	}
	if openedInfo.Size() > maxFileSize {
		return nil, fmt.Errorf("environment file exceeds the 1 MiB limit")
	}

	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read environment file: %w", err)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("environment file exceeds the 1 MiB limit")
	}

	values, err := ParseFileData(data)
	if err != nil {
		return nil, fmt.Errorf("parse environment file: %w", err)
	}
	return values, nil
}

// ParseFileData parses a complete uploaded environment document and rejects
// empty or oversized files. It does not include entered values in errors.
func ParseFileData(data []byte) (map[string]string, error) {
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("environment file exceeds the 1 MiB limit")
	}
	values, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("environment file contains no settings")
	}
	return values, nil
}

// Load reads settings without creating the file. A missing file means no
// settings have been saved yet.
func (s *Store) Load() error {
	if s == nil {
		return fmt.Errorf("environment settings path is empty")
	}
	if err := s.validatePath(); err != nil {
		return err
	}
	if s.state == nil {
		s.state = make(map[storeStateKey]storeState)
	}
	unlock, err := s.lock()
	if err != nil {
		return fmt.Errorf("lock environment settings: %w", err)
	}
	defer unlock()
	values, version, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	s.values = values
	s.setState(version, true)
	return nil
}

// Save atomically replaces the settings file and keeps it private to the user.
// A store that loaded an older file cannot silently replace newer settings.
func (s *Store) Save(values map[string]string) error {
	if err := validateValues(values); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("environment settings path is empty")
	}
	if err := s.validatePath(); err != nil {
		return err
	}
	if s.state == nil {
		s.state = make(map[storeStateKey]storeState)
	}
	unlock, err := s.lock()
	if err != nil {
		return fmt.Errorf("lock environment settings: %w", err)
	}
	defer unlock()
	_, currentVersion, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	state := s.currentState()
	if state.loaded && currentVersion != state.version {
		return ErrChanged
	}
	if err := s.saveUnlocked(values); err != nil {
		return err
	}
	s.values = clone(values)
	s.setState(Digest(values), true)
	return nil
}

// UpsertIfUnchanged applies incoming settings only when the current on-disk
// settings still match expectedDigest. It is used after a preview so changes
// made after that preview cannot be hidden by the accepted proposal.
func (s Store) UpsertIfUnchanged(expectedDigest string, incoming map[string]string) (ChangeSet, error) {
	return s.upsert(expectedDigest, incoming)
}

func (s Store) upsert(expectedDigest string, incoming map[string]string) (ChangeSet, error) {
	if err := validateValues(incoming); err != nil {
		return ChangeSet{}, err
	}
	if err := s.validatePath(); err != nil {
		return ChangeSet{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return ChangeSet{}, fmt.Errorf("lock environment settings: %w", err)
	}
	defer unlock()
	current, currentVersion, err := s.loadUnlocked()
	if err != nil {
		return ChangeSet{}, err
	}
	replaceValues(s.values, current)
	s.setState(currentVersion, true)
	if currentVersion != expectedDigest {
		return ChangeSet{}, ErrChanged
	}
	changes := Changes(current, incoming)
	if len(changes.Added) == 0 && len(changes.Modified) == 0 {
		return changes, nil
	}
	merged := Merge(current, incoming)
	if err := validateValues(merged); err != nil {
		return ChangeSet{}, err
	}
	if err := s.saveUnlocked(merged); err != nil {
		return ChangeSet{}, err
	}
	replaceValues(s.values, merged)
	s.setState(Digest(merged), true)
	return changes, nil
}

func (s Store) currentState() storeState {
	if s.state == nil {
		return storeState{}
	}
	return s.state[storeStateKey{}]
}

func (s Store) setState(version string, loaded bool) {
	if s.state != nil {
		s.state[storeStateKey{}] = storeState{version: version, loaded: loaded}
	}
}

func replaceValues(target, source map[string]string) {
	for name := range target {
		delete(target, name)
	}
	for name, value := range source {
		target[name] = value
	}
}

func (s Store) validatePath() error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("environment settings path is empty")
	}
	return nil
}

func (s Store) lock() (func(), error) {
	directory, err := s.prepareDirectory()
	if err != nil {
		return nil, err
	}
	environmentProcessMu.Lock()
	lockPath := s.path + ".lock"
	lockInfo, err := os.Lstat(lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("inspect environment lock: %w", err)
	}
	if err == nil {
		if lockInfo.Mode()&os.ModeSymlink != 0 {
			environmentProcessMu.Unlock()
			return nil, fmt.Errorf("environment lock file must not be a symbolic link")
		}
		if !lockInfo.Mode().IsRegular() {
			environmentProcessMu.Unlock()
			return nil, fmt.Errorf("environment lock file must be a regular file")
		}
	}
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|environmentLockOpenFlags, 0600)
	if err != nil {
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("open environment lock: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("inspect opened environment lock: %w", err)
	}
	pathInfo, err := os.Lstat(lockPath)
	if err != nil {
		_ = file.Close()
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("inspect environment lock: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("environment lock file changed while it was opened")
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("restrict environment lock: %w", err)
	}
	if err := filesync.LockFile(file); err != nil {
		_ = file.Close()
		environmentProcessMu.Unlock()
		return nil, fmt.Errorf("lock environment settings in %s: %w", directory, err)
	}
	return func() {
		_ = file.Close()
		environmentProcessMu.Unlock()
	}, nil
}

func (s Store) prepareDirectory() (string, error) {
	directory := filepath.Dir(s.path)
	if directory == "." {
		return directory, nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create environment settings directory: %w", err)
	}
	// The default .goaster directory belongs to GoAster. A test or future
	// custom path may live under an application-managed directory.
	if filepath.Base(directory) == directoryName {
		info, err := os.Lstat(directory)
		if err != nil {
			return "", fmt.Errorf("inspect environment settings directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("environment settings directory must be a real directory")
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return "", fmt.Errorf("restrict environment settings directory: %w", err)
		}
	}
	return directory, nil
}

func (s Store) loadUnlocked() (map[string]string, string, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		values := make(map[string]string)
		return values, Digest(values), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("inspect environment settings: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("environment settings file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("environment settings file must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, "", fmt.Errorf("environment settings permissions are too broad: %04o (want 0600)", info.Mode().Perm())
	}
	if info.Size() > maxFileSize {
		return nil, "", fmt.Errorf("environment settings exceed the 1 MiB limit")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, "", fmt.Errorf("read environment settings: %w", err)
	}
	values, err := Parse(string(data))
	if err != nil {
		return nil, "", fmt.Errorf("parse environment settings: %w", err)
	}
	return values, Digest(values), nil
}

func (s Store) saveUnlocked(values map[string]string) error {
	directory := filepath.Dir(s.path)
	data := encode(values)
	temporary, err := os.CreateTemp(directory, ".env-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary environment settings file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary environment settings file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write environment settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync environment settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close environment settings: %w", err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return fmt.Errorf("replace environment settings: %w", err)
	}
	return nil
}

// Parse validates newline-separated KEY=VALUE settings. Errors identify a
// line and problem without including any entered values.
func Parse(input string) (map[string]string, error) {
	if len(input) > maxFileSize {
		return nil, fmt.Errorf("environment settings exceed the 1 MiB limit")
	}
	if !utf8.ValidString(input) {
		return nil, fmt.Errorf("environment settings are not valid UTF-8")
	}
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 4096), maxLineSize)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			return nil, fmt.Errorf("line %d is blank", lineNumber)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d must be KEY=VALUE", lineNumber)
		}
		if !validName(key) {
			return nil, fmt.Errorf("line %d has an invalid environment variable name", lineNumber)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("line %d duplicates key %s", lineNumber, key)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("line %d has an invalid value", lineNumber)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment settings: %w", err)
	}
	return values, nil
}

func validName(name string) bool {
	if name == "" || !isNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isNameStart(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func isNameStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func validateValues(values map[string]string) error {
	for key, value := range values {
		if !validName(key) {
			return fmt.Errorf("invalid environment variable name")
		}
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("environment setting %s has an invalid value", key)
		}
	}
	if len(encode(values)) > maxFileSize {
		return fmt.Errorf("environment settings exceed the 1 MiB limit")
	}
	return nil
}

func encode(values map[string]string) []byte {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var data strings.Builder
	for _, key := range keys {
		data.WriteString(key)
		data.WriteByte('=')
		data.WriteString(values[key])
		data.WriteByte('\n')
	}
	return []byte(data.String())
}

func clone(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
