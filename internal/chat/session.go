package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"goaster/internal/artefacts"
	"goaster/internal/filesync"
)

const (
	sessionDirectoryName  = ".goaster"
	sessionFilename       = "session.json"
	sessionFormatVersion  = 2
	sessionStateDirectory = "state"
	sessionDirectory      = "sessions"

	// The file limit leaves room for a full context window after JSON escaping
	// while keeping reads and writes bounded.
	maxSessionFileBytes    = 8 << 20
	maxSessionMessages     = 256
	maxSessionMessageBytes = 32 << 10
	maxSummaryBytes        = 8 << 10
	// These fixed defaults track PicoClaw's context window and summary trigger.
	contextWindowTokens   = 300_000
	summarizeTokenPercent = 75
	maxContextTokens      = contextWindowTokens
	summarizeTokens       = contextWindowTokens * summarizeTokenPercent / 100
	recentMessagesToKeep  = 16
)

const (
	contextTrimNotice = "Earlier conversation messages or long message content were trimmed to keep the context within GoAster's automatic budget."
	messageTrimSuffix = "\n[message shortened by GoAster]"
)

type conversationStateError uint8

const (
	errConversationStateChanged conversationStateError = iota + 1
	errConversationStateTooLarge
	errConversationSaveFailed
	errConversationLockBusy
)

func (err conversationStateError) Error() string {
	switch err {
	case errConversationStateChanged:
		return "conversation state changed in another instance; restart to load the latest history"
	case errConversationStateTooLarge:
		return "conversation state changed; restart to recover it"
	case errConversationSaveFailed:
		return "conversation state could not be saved"
	case errConversationLockBusy:
		return "conversation lock is busy"
	default:
		return "conversation state error"
	}
}

type conversationSaveError struct {
	operation string
	cause     error
}

func (err conversationSaveError) Error() string {
	return fmt.Sprintf("%s: %v", err.operation, err.cause)
}
func (err conversationSaveError) Unwrap() error        { return err.cause }
func (err conversationSaveError) Is(target error) bool { return target == errConversationSaveFailed }

type conversationLockBusyError struct{ cause error }

func (err conversationLockBusyError) Error() string        { return err.cause.Error() }
func (err conversationLockBusyError) Unwrap() error        { return err.cause }
func (err conversationLockBusyError) Is(target error) bool { return target == errConversationLockBusy }

// SessionStore persists the local terminal conversation separately from
// credentials and environment settings.
type SessionStore struct {
	Path string

	mu             sync.Mutex
	version        [sha256.Size]byte // Zero means the file was absent at the last load.
	selectedSkills []selectedSkill
	syncDirectory  func(string) error // Optional per-store seam for filesystem failures.
}

// SessionLoadResult describes a safely loaded conversation. Recovered means
// an invalid file was moved aside without exposing its contents.
type SessionLoadResult struct {
	Messages       []ChatMessage
	Summary        string
	Recovered      bool
	Trimmed        bool
	selectedSkills []selectedSkill
}

type sessionFile struct {
	Version        int             `json:"version"`
	Summary        string          `json:"summary,omitempty"`
	Messages       []ChatMessage   `json:"messages"`
	SelectedSkills []selectedSkill `json:"selected_skills"`
}

// NewSessionStore locates the default conversation file at
// ~/.goaster/artefacts/state/sessions/session.json when path is empty.
func NewSessionStore(path string) (*SessionStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		root, err := artefacts.Root()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(root, sessionStateDirectory, sessionDirectory, sessionFilename)
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
	memory, err := NewMemoryStoreWithError("")
	if err != nil {
		return nil, fmt.Errorf("initialize Markdown memory store: %w", err)
	}
	return NewSessionWithStoreAndMemory(config, provider, store, memory)
}

// NewPersistentSessionAt creates a persistent session at path for isolated
// local setups and tests. It does not add a user-facing configuration option.
func NewPersistentSessionAt(config Config, provider Provider, path string) (*Session, error) {
	return NewSessionWithStore(config, provider, NewSessionStoreAt(path))
}

// NewSessionWithStore loads a session from store without changing the file.
// A malformed file is quarantined by the store and starts a fresh session.
func NewSessionWithStore(config Config, provider Provider, store *SessionStore) (*Session, error) {
	return NewSessionWithStoreAndMemory(config, provider, store, nil)
}

// NewSessionWithStoreAndMemory loads a persistent conversation and attaches
// an independent Markdown memory store. Conversation compaction only saves the
// SessionStore; it never writes the memory store.
func NewSessionWithStoreAndMemory(config Config, provider Provider, store *SessionStore, memory *MemoryStore) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation store is nil")
	}
	session := NewSessionWithMemory(config, provider, memory)
	loaded, err := store.loadState(&session.storeVersion)
	if err != nil {
		return nil, err
	}
	session.store = store
	session.history = cloneChatMessages(loaded.Messages)
	session.summary = loaded.Summary
	session.selectedSkills = append([]selectedSkill(nil), loaded.selectedSkills...)
	session.recoveryPending = loaded.Recovered
	if loaded.Recovered {
		session.startupNotice = appendSessionNotice(session.startupNotice, ErrConversationRecovery.Error())
	} else if loaded.Trimmed {
		session.startupNotice = appendSessionNotice(session.startupNotice, "The saved conversation was shortened to fit GoAster's automatic context budget.")
	}
	return session, nil
}

// Load reads a private conversation file. Missing state is normal. Invalid
// state is moved to a new .corrupt file so it cannot be reused accidentally;
// the original contents are never included in an error or notice.
func (s *SessionStore) Load() (SessionLoadResult, error) {
	return s.loadState(nil)
}

// loadState captures the session token under the same lock as its history.
func (s *SessionStore) loadState(version *[sha256.Size]byte) (loaded SessionLoadResult, err error) {
	if err := s.validatePath(); err != nil {
		return SessionLoadResult{}, err
	}
	unlockTurn, err := s.lock(true)
	if err != nil {
		return SessionLoadResult{}, fmt.Errorf("lock conversation turn: %w", err)
	}
	defer unlockTurn()
	return s.loadStateUnderTurnLock(version)
}

func (s *SessionStore) loadStateUnderTurnLock(version *[sha256.Size]byte) (loaded SessionLoadResult, err error) {
	unlock, err := s.lock(false)
	if err != nil {
		return SessionLoadResult{}, err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err == nil && version != nil {
			*version = s.version
		}
	}()
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		s.version = [sha256.Size]byte{}
		s.selectedSkills = nil
		// A quarantine or missing-file marker without a replacement means no
		// interactive turn has acknowledged the loss, even after a restart.
		entries, readErr := os.ReadDir(filepath.Dir(s.Path))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return SessionLoadResult{}, fmt.Errorf("inspect conversation recovery: %w", readErr)
		}
		prefix := filepath.Base(s.Path) + ".corrupt"
		for _, entry := range entries {
			if entry.Name() == prefix || strings.HasPrefix(entry.Name(), prefix+"-") || entry.Name() == filepath.Base(s.Path)+".missing" {
				return SessionLoadResult{Recovered: true}, nil
			}
		}
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
	summary, summaryTrimmed := normalizeSummary(disk.Summary)
	bounded, budgetTrimmed := boundSessionMessages(summary, messages, false, maxContextTokens, maxSessionMessages)
	s.version = sha256.Sum256(data)
	s.selectedSkills = append([]selectedSkill(nil), disk.SelectedSkills...)
	return SessionLoadResult{
		Messages:       bounded,
		Summary:        summary,
		Trimmed:        normalized || summaryTrimmed || budgetTrimmed,
		selectedSkills: append([]selectedSkill(nil), s.selectedSkills...),
	}, nil
}

// Save atomically replaces the private conversation file. It trims the input
// before encoding, so callers cannot make persisted history grow unbounded.
// Existing state must be loaded first; stale stores fail rather than overwrite
// changes saved by another instance.
func (s *SessionStore) Save(messages []ChatMessage) error {
	if err := s.validatePath(); err != nil {
		return err
	}
	unlockTurn, err := s.lock(true)
	if err != nil {
		return fmt.Errorf("lock conversation turn: %w", err)
	}
	defer unlockTurn()
	return s.saveState("", messages)
}

func (s *SessionStore) saveState(summary string, messages []ChatMessage) error {
	_, err := s.saveStateWithVersion(summary, messages, nil, [sha256.Size]byte{}, false)
	return err
}

// Sessions pass their expected token by value so another user of the same
// store cannot advance their expected version.
func (s *SessionStore) saveStateWithVersion(summary string, messages []ChatMessage, selected []selectedSkill, expectedVersion [sha256.Size]byte, hasExpectedVersion bool) ([sha256.Size]byte, error) {
	if err := s.validatePath(); err != nil {
		return [sha256.Size]byte{}, err
	}
	normalized, _, err := normalizeChatMessages(messages, false)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("conversation contains unsupported message data")
	}
	summary, _ = normalizeSummary(summary)
	bounded, _ := boundSessionMessages(summary, normalized, false, maxContextTokens, maxSessionMessages)
	directory := filepath.Dir(s.Path)
	unlock, err := s.lock(false)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := sessionVersion(s.Path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	expected := s.version
	if hasExpectedVersion {
		expected = expectedVersion
	}
	if current != expected {
		return [sha256.Size]byte{}, errConversationStateChanged
	}
	if !hasExpectedVersion {
		selected = s.selectedSkills
	}
	if !validSelectedSkills(selected) {
		return [sha256.Size]byte{}, fmt.Errorf("conversation contains invalid skill selections")
	}
	data, err := encodeSessionFile(summary, bounded, selected)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode conversation state: %w", err)
	}
	if len(data) > maxSessionFileBytes {
		return [sha256.Size]byte{}, fmt.Errorf("conversation state exceeds the %d-byte limit", maxSessionFileBytes)
	}
	temporary, err := os.CreateTemp(directory, ".session-*.tmp")
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("create temporary conversation file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return [sha256.Size]byte{}, fmt.Errorf("restrict temporary conversation file: %w", err)
	}
	n, err := temporary.Write(data)
	if err != nil {
		_ = temporary.Close()
		return [sha256.Size]byte{}, fmt.Errorf("write conversation state: %w", err)
	}
	if n != len(data) {
		_ = temporary.Close()
		return [sha256.Size]byte{}, fmt.Errorf("write conversation state: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return [sha256.Size]byte{}, fmt.Errorf("sync conversation state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("close temporary conversation file: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("replace conversation state: %w", err)
	}
	if err := os.Chmod(s.Path, 0600); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("restrict conversation file: %w", err)
	}
	syncDirectory := s.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncSessionDirectory
	}
	if err := syncDirectory(directory); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("sync conversation directory: %w", err)
	}
	s.version = sha256.Sum256(data)
	s.selectedSkills = append([]selectedSkill(nil), selected...)
	return s.version, nil
}

func (s *SessionStore) validatePath() error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("conversation path is empty")
	}
	return nil
}

func appendSessionNotice(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" || strings.Contains(existing, next) {
		return existing
	}
	return existing + " " + next
}

func (s *SessionStore) recoverInvalid() (SessionLoadResult, error) {
	if err := quarantineSessionFile(s.Path); err != nil {
		return SessionLoadResult{}, fmt.Errorf("saved conversation is invalid and could not be recovered safely")
	}
	s.version = [sha256.Size]byte{}
	s.selectedSkills = nil
	return SessionLoadResult{Recovered: true}, nil
}

// markMissing preserves the recovery barrier across restarts when a saved
// conversation disappeared without a file to quarantine. The turn lock is held.
func (s *SessionStore) markMissing() error {
	file, err := os.OpenFile(s.Path+".missing", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return syncSessionDirectory(filepath.Dir(s.Path))
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
	if disk.SelectedSkills == nil || !validSelectedSkills(disk.SelectedSkills) {
		return sessionFile{}, errors.New("invalid conversation skill selections")
	}
	return disk, nil
}

func encodeSessionFile(summary string, messages []ChatMessage, selected []selectedSkill) ([]byte, error) {
	selections := append(make([]selectedSkill, 0, len(selected)), selected...)
	data, err := json.MarshalIndent(sessionFile{Version: sessionFormatVersion, Summary: summary, Messages: messages, SelectedSkills: selections}, "", "  ")
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
	return trimNormalizedChatMessagesToLimit(messages, budget, maxSessionMessages)
}

func trimNormalizedChatMessagesToLimit(messages []ChatMessage, budget, maxMessages int) ([]ChatMessage, bool) {
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
	if len(keptReverse) > maxMessages {
		keptReverse = keptReverse[:maxMessages]
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

func normalizeSummary(summary string) (string, bool) {
	trimmed := false
	if !utf8.ValidString(summary) {
		summary = strings.ToValidUTF8(summary, "\uFFFD")
		trimmed = true
	}
	summary = strings.TrimSpace(summary)
	summary, limited := limitMessageText(summary, maxSummaryBytes)
	return summary, trimmed || limited
}

func summaryMessage(summary string) ChatMessage {
	return ChatMessage{Role: "system", Content: "Summary of earlier conversation:\n" + summary}
}

// Reserve room for the summary and any trimming notice so neither can be
// displaced by recent messages when the provider applies its own bounds.
func boundSessionMessages(summary string, messages []ChatMessage, notice bool, budget, maxMessages int) ([]ChatMessage, bool) {
	if summary != "" {
		budget -= estimateMessageTokens(summaryMessage(summary))
		maxMessages--
	}
	if notice {
		budget -= estimateMessageTokens(ChatMessage{Role: "system", Content: contextTrimNotice})
		maxMessages--
	}
	return trimNormalizedChatMessagesToLimit(messages, budget, maxMessages)
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
	bounded, _ := trimNormalizedChatMessagesToLimit(messages, budget, maxSessionMessages-1)
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
		return fmt.Errorf("%w: %04o (want 0600)", errConversationFilePermissions, info.Mode().Perm())
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
	if create {
		root, rootErr := artefacts.Root()
		if rootErr == nil && filepath.Clean(directory) == filepath.Join(root, sessionStateDirectory, sessionDirectory) {
			if _, err := artefacts.EnsureRuntimeDirectory(sessionStateDirectory, sessionDirectory); err != nil {
				return fmt.Errorf("prepare conversation directory: %w", err)
			}
			return nil
		}
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
			return fmt.Errorf("%w: %04o (want 0700)", errConversationDirectoryPermissions, info.Mode().Perm())
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
		return syncSessionDirectory(directory)
	}
	return errors.New("could not select a quarantine path")
}

// The state lock coordinates loads and saves; the separate turn lock prevents
// another conversation writer from changing the state while tools are running.
// Keep both files in place so every opener locks the same inode. The OS releases
// each lock when the descriptor is closed, including when the process exits.
func (s *SessionStore) lock(turn bool) (func(), error) {
	if err := validateSessionDirectory(filepath.Dir(s.Path), true); err != nil {
		return nil, err
	}
	path := s.Path
	if turn {
		path += ".turn"
	}
	path += ".lock"
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open conversation lock: %w", err)
	}
	if err := filesync.LockFile(file); err != nil {
		_ = file.Close()
		if filesync.IsLockBusy(err) {
			err = conversationLockBusyError{cause: err}
		}
		return nil, fmt.Errorf("lock conversation state (another instance may be using it): %w", err)
	}
	return func() { _ = file.Close() }, nil
}

func sessionVersion(path string) ([sha256.Size]byte, error) {
	var absent [sha256.Size]byte
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return absent, nil
	}
	if err != nil {
		return absent, fmt.Errorf("inspect conversation version: %w", err)
	}
	if err := validateSessionFileInfo(info); err != nil {
		return absent, err
	}
	if info.Size() > maxSessionFileBytes {
		return absent, errConversationStateTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return absent, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxSessionFileBytes+1)); err != nil {
		return absent, err
	}
	var version [sha256.Size]byte
	copy(version[:], hash.Sum(nil))
	return version, nil
}

func syncSessionDirectory(directory string) error {
	return filesync.DirectoryPath(directory)
}

func syncSessionDirectoryFile(file filesync.Directory) error {
	return filesync.DirectoryFile(file)
}
