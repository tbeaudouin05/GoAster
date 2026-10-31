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
)

const (
	directoryName            = ".goaster"
	fileName                 = "env"
	maxLineSize              = 1024 * 1024
	maxFileSize              = 1024 * 1024
	ovhEndpointName          = "OVH_ENDPOINT"
	ovhApplicationKeyName    = "OVH_APPLICATION_KEY"
	ovhApplicationSecretName = "OVH_APPLICATION_SECRET"
	ovhConsumerKeyName       = "OVH_CONSUMER_KEY"
	telegramBotTokenName     = "TELEGRAM_BOT_TOKEN"
)

// Store keeps environment settings separate from chat messages and providers.
type Store struct {
	path      string
	values    map[string]string
	savedFile bool
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
	return &Store{path: path, values: make(map[string]string)}
}

// Values returns a copy of the values loaded or saved by this store.
func (s *Store) Values() map[string]string {
	return clone(s.values)
}

// HasSavedFile reports whether the most recent Load or Save found a saved env file.
func (s Store) HasSavedFile() bool {
	return s.savedFile
}

// RequiredTemplateValues returns the blank integration settings GoAster asks
// users to configure through the local environment-file flow.
func RequiredTemplateValues() map[string]string {
	values := make(map[string]string, len(requiredTemplateNames()))
	for _, name := range requiredTemplateNames() {
		values[name] = ""
	}
	return values
}

func requiredTemplateNames() [5]string {
	return [5]string{
		ovhEndpointName,
		ovhApplicationKeyName,
		ovhApplicationSecretName,
		ovhConsumerKeyName,
		telegramBotTokenName,
	}
}

func isBlankRequiredTemplate(values map[string]string) bool {
	names := requiredTemplateNames()
	if len(values) != len(names) {
		return false
	}
	for _, name := range names {
		value, exists := values[name]
		if !exists || value != "" {
			return false
		}
	}
	return true
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

	values, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse environment file: %w", err)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("environment file contains no settings")
	}
	return values, nil
}

// Load reads settings without creating the file. A missing file means no
// settings have been saved yet.
func (s *Store) Load() error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("environment settings path is empty")
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.values = make(map[string]string)
		s.savedFile = false
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect environment settings: %w", err)
	}
	directory := filepath.Dir(s.path)
	if filepath.Base(directory) == directoryName {
		directoryInfo, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect environment settings directory: %w", err)
		}
		if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
			return fmt.Errorf("environment settings directory must be a real directory")
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("environment settings file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("environment settings file must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("environment settings permissions are too broad: %04o (want 0600)", info.Mode().Perm())
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read environment settings: %w", err)
	}
	values, err := Parse(string(data))
	if err != nil {
		return fmt.Errorf("parse environment settings: %w", err)
	}
	s.values = values
	s.savedFile = true
	return nil
}

// Save atomically replaces the settings file and keeps it private to the user.
func (s *Store) Save(values map[string]string) error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("environment settings path is empty")
	}
	if err := validateValues(values); err != nil {
		return err
	}

	directory := filepath.Dir(s.path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create environment settings directory: %w", err)
		}
		// The default .goaster directory belongs to GoAster. A test or future
		// custom path may live under an application-managed directory.
		if filepath.Base(directory) == directoryName {
			info, err := os.Lstat(directory)
			if err != nil {
				return fmt.Errorf("inspect environment settings directory: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("environment settings directory must be a real directory")
			}
			if err := os.Chmod(directory, 0700); err != nil {
				return fmt.Errorf("restrict environment settings directory: %w", err)
			}
		}
	}

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
	s.values = clone(values)
	s.savedFile = true
	return nil
}

// Parse validates newline-separated KEY=VALUE settings. Errors identify a
// line and problem without including any entered values.
func Parse(input string) (map[string]string, error) {
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
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("environment setting %s has an invalid value", key)
		}
	}
	return nil
}

func encode(values map[string]string) []byte {
	var keys []string
	if isBlankRequiredTemplate(values) {
		templateNames := requiredTemplateNames()
		keys = templateNames[:]
	} else {
		keys = make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
	}
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
