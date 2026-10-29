package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	sessionDirectoryName = ".goaster"
	sessionFilename      = "session.json"
	sessionFormatVersion = 1

	// The file limit is an additional guard around the bounded history. It
	// protects startup from an unexpectedly large or replaced file.
	maxSessionFileBytes    = 256 << 10
	maxSessionMessages     = 256
	maxSessionMessageBytes = 32 << 10
	maxContextTokens       = 8192
)

const (
	contextTrimNotice = "Earlier conversation messages or long message content were trimmed to keep the context within GoAster's automatic budget."
	messageTrimSuffix = "\n[message shortened by GoAster]"
)

// SessionStore persists the local terminal conversation separately from
// credentials and environment settings.
type SessionStore struct {
	Path string
}

// SessionLoadResult describes a safely loaded conversation. Recovered means
// an invalid file was moved aside without exposing its contents.
type SessionLoadResult struct {
	Messages  []ChatMessage
	Recovered bool
	Trimmed   bool
}

type sessionFile struct {
	Version  int           `json:"version"`
	Messages []ChatMessage `json:"messages"`
}

// NewSessionStore locates the default conversation file at
// ~/.goaster/session.json when path is empty.
func NewSessionStore(path string) (*SessionStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
		path = filepath.Join(home, sessionDirectoryName, sessionFilename)
	}
	return &SessionStore{Path: path}, nil
}

// NewSessionStoreAt creates a store for an isolated local path.
func NewSessionStoreAt(path string) *SessionStore {
	return &SessionStore{Path: path}
}

// NewPersistentSession creates the normal terminal session backed by the
// private default conversation file.
func NewPersistentSession(config Config, provider Provider) (*Session, error) {
	store, err := NewSessionStore("")
	if err != nil {
		return nil, err
	}
	return NewSessionWithStore(config, provider, store)
}

// NewPersistentSessionAt creates a persistent session at path for isolated
// local setups and tests. It does not add a user-facing configuration option.
func NewPersistentSessionAt(config Config, provider Provider, path string) (*Session, error) {
	return NewSessionWithStore(config, provider, NewSessionStoreAt(path))
}

// NewSessionWithStore loads a session from store without changing the file.
// A malformed file is quarantined by the store and starts a fresh session.
func NewSessionWithStore(config Config, provider Provider, store *SessionStore) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation store is nil")
	}
	loaded, err := store.Load()
	if err != nil {
		return nil, err
	}
	session := NewSession(config, provider)
	session.store = store
	session.history = cloneChatMessages(loaded.Messages)
	if loaded.Recovered {
		session.startupNotice = "The saved conversation was invalid, so it was moved aside and a new conversation was started."
	} else if loaded.Trimmed {
		session.startupNotice = "The saved conversation was shortened to fit GoAster's automatic context budget."
	}
	return session, nil
}

// Load reads a private conversation file. Missing state is normal. Invalid
// state is moved to a new .corrupt file so it cannot be reused accidentally;
// the original contents are never included in an error or notice.
func (s *SessionStore) Load() (SessionLoadResult, error) {
	if err := s.validatePath(); err != nil {
		return SessionLoadResult{}, err
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return SessionLoadResult{}, nil
	}
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("inspect conversation file: %w", err)
	}
	if err := validateSessionFileInfo(info); err != nil {
		return SessionLoadResult{}, err
	}
	if err := validateSessionDirectory(filepath.Dir(s.Path), false); err != nil {
		return SessionLoadResult{}, err
	}
	if info.Size() > maxSessionFileBytes {
		return s.recoverInvalid()
	}

	file, err := os.Open(s.Path)
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("read conversation file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("inspect opened conversation file: %w", err)
	}
	pathInfo, err := os.Lstat(s.Path)
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("inspect conversation file: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return SessionLoadResult{}, fmt.Errorf("conversation file changed while it was opened")
	}
	if err := validateSessionFileInfo(openedInfo); err != nil {
		return SessionLoadResult{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionFileBytes+1))
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("read conversation file: %w", err)
	}
	if len(data) > maxSessionFileBytes {
		return s.recoverInvalid()
	}

	disk, err := decodeSessionFile(data)
	if err != nil {
		return s.recoverInvalid()
	}
	messages, normalized, err := normalizeChatMessages(disk.Messages, false)
	if err != nil {
		return s.recoverInvalid()
	}
	bounded, budgetTrimmed := trimNormalizedChatMessages(messages, maxContextTokens)
	return SessionLoadResult{
		Messages: bounded,
		Trimmed:  normalized || budgetTrimmed,
	}, nil
}

// Save atomically replaces the private conversation file. It trims the input
// before encoding, so callers cannot make persisted history grow unbounded.
func (s *SessionStore) Save(messages []ChatMessage) error {
	if err := s.validatePath(); err != nil {
		return err
	}
	normalized, _, err := normalizeChatMessages(messages, false)
	if err != nil {
		return fmt.Errorf("conversation contains unsupported message data")
	}
	bounded, _ := trimNormalizedChatMessages(normalized, maxContextTokens)
	data, err := encodeSessionFile(bounded)
	if err != nil {
		return fmt.Errorf("encode conversation state: %w", err)
	}
	if len(data) > maxSessionFileBytes {
		return fmt.Errorf("conversation state exceeds the %d-byte limit", maxSessionFileBytes)
	}

	directory := filepath.Dir(s.Path)
	if err := validateSessionDirectory(directory, true); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary conversation file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary conversation file: %w", err)
	}
	n, err := temporary.Write(data)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write conversation state: %w", err)
	}
	if n != len(data) {
		_ = temporary.Close()
		return fmt.Errorf("write conversation state: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync conversation state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary conversation file: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace conversation state: %w", err)
	}
	if err := os.Chmod(s.Path, 0600); err != nil {
		return fmt.Errorf("restrict conversation file: %w", err)
	}
	// Directory sync is best-effort because it is unsupported by some
	// platforms, while the file and rename above remain atomic.
	syncSessionDirectory(directory)
	return nil
}

func (s *SessionStore) validatePath() error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("conversation path is empty")
	}
	return nil
}

func (s *SessionStore) recoverInvalid() (SessionLoadResult, error) {
	if err := quarantineSessionFile(s.Path); err != nil {
		return SessionLoadResult{}, fmt.Errorf("saved conversation is invalid and could not be recovered safely")
	}
	return SessionLoadResult{Recovered: true}, nil
}

func decodeSessionFile(data []byte) (sessionFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var disk sessionFile
	if err := decoder.Decode(&disk); err != nil {
		return sessionFile{}, err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return sessionFile{}, errors.New("conversation file contains trailing data")
	}
	if disk.Version != sessionFormatVersion {
		return sessionFile{}, errors.New("unsupported conversation file version")
	}
	return disk, nil
}

func encodeSessionFile(messages []ChatMessage) ([]byte, error) {
	data, err := json.MarshalIndent(sessionFile{Version: sessionFormatVersion, Messages: messages}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func normalizeChatMessages(messages []ChatMessage, allowSystem bool) ([]ChatMessage, bool, error) {
	normalized := make([]ChatMessage, 0, len(messages))
	trimmed := false
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "user" && role != "assistant" && !(allowSystem && role == "system") {
			return nil, false, errors.New("unsupported conversation role")
		}
		content := message.Content
		if !utf8.ValidString(content) {
			content = strings.ToValidUTF8(content, "\uFFFD")
			trimmed = true
		}
		limited, wasLimited := limitMessageText(content, maxSessionMessageBytes)
		if wasLimited {
			trimmed = true
		}
		normalized = append(normalized, ChatMessage{Role: role, Content: limited})
	}
	return normalized, trimmed, nil
}

func boundChatHistory(messages []ChatMessage) ([]ChatMessage, bool) {
	normalized, trimmed, err := normalizeChatMessages(messages, true)
	if err != nil {
		// Session-generated messages only use the roles above. Keep a safe,
		// bounded fallback if a custom caller supplied something else.
		normalized = nil
		for _, message := range messages {
			if message.Role != "user" && message.Role != "assistant" && message.Role != "system" {
				trimmed = true
				continue
			}
			normalized = append(normalized, message)
		}
	}
	bounded, budgetTrimmed := trimNormalizedChatMessages(normalized, maxContextTokens)
	return bounded, trimmed || budgetTrimmed
}

func trimNormalizedChatMessages(messages []ChatMessage, budget int) ([]ChatMessage, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	if budget < 1 {
		return []ChatMessage{messages[len(messages)-1]}, true
	}
	trimmed := false
	if estimateMessageTokens(messages[len(messages)-1]) > budget {
		messages = cloneChatMessages(messages)
		last := messages[len(messages)-1]
		last.Content, _ = limitMessageText(last.Content, budget*4)
		messages[len(messages)-1] = last
		trimmed = true
	}
	keptReverse := make([]ChatMessage, 0, len(messages))
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		cost := estimateMessageTokens(message)
		if used+cost > budget {
			trimmed = true
			continue
		}
		keptReverse = append(keptReverse, message)
		used += cost
	}
	if len(keptReverse) > maxSessionMessages {
		keptReverse = keptReverse[:maxSessionMessages]
		trimmed = true
	}
	if len(keptReverse) == 0 {
		last := messages[len(messages)-1]
		last.Content, _ = limitMessageText(last.Content, budget*4)
		keptReverse = append(keptReverse, last)
		trimmed = true
	}
	bounded := make([]ChatMessage, len(keptReverse))
	for index := range keptReverse {
		bounded[len(keptReverse)-1-index] = keptReverse[index]
	}
	return bounded, trimmed || len(bounded) != len(messages)
}

func estimateMessageTokens(message ChatMessage) int {
	tokens := (utf8.RuneCountInString(message.Content) + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func historyWithContextNotice(messages []ChatMessage) []ChatMessage {
	notice := ChatMessage{Role: "system", Content: contextTrimNotice}
	budget := maxContextTokens - estimateMessageTokens(notice)
	if budget < 1 {
		budget = 1
	}
	bounded, _ := trimNormalizedChatMessages(messages, budget)
	result := make([]ChatMessage, 0, len(bounded)+1)
	result = append(result, notice)
	result = append(result, bounded...)
	return result
}

func containsContextNotice(messages []ChatMessage) bool {
	for _, message := range messages {
		if message.Role == "system" && message.Content == contextTrimNotice {
			return true
		}
	}
	return false
}

func limitMessageText(content string, limit int) (string, bool) {
	if limit < 1 {
		return "", content != ""
	}
	if len(content) <= limit && utf8.ValidString(content) {
		return content, false
	}
	if !utf8.ValidString(content) {
		content = strings.ToValidUTF8(content, "\uFFFD")
	}
	suffix := messageTrimSuffix
	if len(suffix) >= limit {
		return truncateUTF8(content, limit), true
	}
	prefix := truncateUTF8(content, limit-len(suffix))
	return prefix + suffix, true
}

func truncateUTF8(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(content) <= limit {
		return content
	}
	content = content[:limit]
	for len(content) > 0 && !utf8.ValidString(content) {
		content = content[:len(content)-1]
	}
	return content
}

func validateSessionFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("conversation file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("conversation file must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("conversation file permissions are too broad: %04o (want 0600)", info.Mode().Perm())
	}
	if info.Mode().Perm()&0444 == 0 {
		return fmt.Errorf("conversation file is not readable")
	}
	return nil
}

func validateSessionDirectory(directory string, create bool) error {
	if directory == "." {
		return nil
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create conversation directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("inspect conversation directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("conversation directory must be a real directory")
	}
	if filepath.Base(directory) == sessionDirectoryName {
		if create {
			if err := os.Chmod(directory, 0700); err != nil {
				return fmt.Errorf("restrict conversation directory: %w", err)
			}
		} else if info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("conversation directory permissions are too broad: %04o (want 0700)", info.Mode().Perm())
		}
	}
	return nil
}

func quarantineSessionFile(path string) error {
	directory := filepath.Dir(path)
	for index := 0; index < 100; index++ {
		candidate := path + ".corrupt"
		if index > 0 {
			candidate = fmt.Sprintf("%s.corrupt-%d", path, index)
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(path, candidate); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return err
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return err
		}
		syncSessionDirectory(directory)
		return nil
	}
	return errors.New("could not select a quarantine path")
}

func syncSessionDirectory(directory string) {
	directoryFile, err := os.Open(directory)
	if err != nil {
		return
	}
	defer directoryFile.Close()
	_ = directoryFile.Sync()
}
