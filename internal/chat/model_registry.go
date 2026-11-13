package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	appconfig "goaster/config"
	"goaster/internal/artefacts"
	"goaster/internal/diagnostics"
)

const (
	DefaultModel           = "gpt-6.1-terra"
	EmergencyModel         = "gpt-6.1-terra"
	modelConfigFilename    = "openai-models.json"
	maxModelConfigBytes    = 64 << 10
	maxModelErrorBytes     = 256
	maxRecoveryNoticeBytes = 640
)

var (
	ErrModelNotConfigured = errors.New("model is not configured")
	ErrModelConfigBroken  = errors.New("model configuration is recovering")
)

// ConfiguredModel is an OpenAI model that can be selected by exact ID or alias.
type ConfiguredModel struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// ModelConfig is the complete OpenAI-only runtime configuration. Chat values
// are always resolved exact model IDs, never aliases.
type ModelConfig struct {
	Provider string            `json:"provider"`
	Models   []ConfiguredModel `json:"models"`
	Chats    map[string]string `json:"chats"`
}

// ModelSelection is the resolved model used for one chat.
type ModelSelection struct {
	ID    string
	Alias string
}

// ModelState is the current selection and an optional one-time recovery notice.
type ModelState struct {
	Selection      ModelSelection
	RecoveryNotice string
	Recovered      bool
}

// ModelRegistry reads the real mutable model configuration. A zero Path uses
// the embedded configuration without filesystem writes, which is useful for
// callers that deliberately want a fixed test registry.
type ModelRegistry struct {
	Path string
}

type modelRecoveryState struct {
	notice   string
	reported bool
	broken   bool
}

var (
	modelRegistryMu sync.Mutex
	modelRecovery   = make(map[string]modelRecoveryState)
)

// NewModelRegistry locates the real runtime configuration at
// ~/.goaster/artefacts/config/openai-models.json when path is empty.
func NewModelRegistry(path string) (ModelRegistry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		root, err := artefacts.Root()
		if err != nil {
			return ModelRegistry{}, err
		}
		path = filepath.Join(root, "config", modelConfigFilename)
	}
	return ModelRegistry{Path: filepath.Clean(path)}, nil
}

// NewModelRegistryAt creates a registry at an explicit path for isolated
// application instances and tests.
func NewModelRegistryAt(path string) ModelRegistry {
	if strings.TrimSpace(path) == "" {
		return ModelRegistry{}
	}
	return ModelRegistry{Path: filepath.Clean(path)}
}

// Current returns the configured model for chatID. A broken file returns the
// compiled-in emergency model and a bounded one-time recovery notice.
func (r ModelRegistry) Current(chatID int64) (ModelState, error) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	config, notice, recovered, err := r.loadLocked()
	if err != nil {
		return ModelState{Selection: emergencySelection(), RecoveryNotice: notice, Recovered: true}, err
	}
	return ModelState{
		Selection:      selectionForChat(config, chatID),
		RecoveryNotice: notice,
		Recovered:      recovered,
	}, nil
}

// Select resolves reference as an exact ID or alias, then atomically persists
// the resolved exact ID for chatID. It never writes through a broken file.
func (r ModelRegistry) Select(chatID int64, reference string) (ModelSelection, error) {
	if r.Path == "" {
		return ModelSelection{}, fmt.Errorf("model configuration path is empty")
	}
	if chatID == 0 {
		return ModelSelection{}, fmt.Errorf("chat ID is invalid")
	}
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	config, _, recovered, err := r.loadLocked()
	if err != nil {
		return ModelSelection{}, err
	}
	if recovered {
		return emergencySelection(), fmt.Errorf("%w: using %s", ErrModelConfigBroken, EmergencyModel)
	}
	selection, err := resolveConfiguredModel(config, reference)
	if err != nil {
		return ModelSelection{}, err
	}
	if config.Chats == nil {
		config.Chats = make(map[string]string)
	}
	config.Chats[strconv.FormatInt(chatID, 10)] = selection.ID
	if err := writeModelConfigLocked(r.Path, config); err != nil {
		return ModelSelection{}, fmt.Errorf("save model selection: %w", err)
	}
	return selection, nil
}

// ParseModelConfig validates the strict OpenAI-only JSON format.
func ParseModelConfig(data []byte) (ModelConfig, error) {
	return decodeModelConfig(data)
}

// IsModelCommand reports whether message starts with the Telegram model
// selection command. It does not expose a catalog or administration command.
func IsModelCommand(message string) bool {
	fields := strings.Fields(strings.TrimSpace(message))
	return len(fields) > 0 && fields[0] == "/m"
}

// ModelCommandArgument returns the argument after /m. A false result means
// that message is not the model command.
func ModelCommandArgument(message string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || fields[0] != "/m" {
		return "", false
	}
	return strings.Join(fields[1:], " "), true
}

func (r ModelRegistry) loadLocked() (ModelConfig, string, bool, error) {
	if r.Path == "" {
		config, err := decodeModelConfig(appconfig.OpenAIModels)
		if err != nil {
			return emergencyConfig(), boundedRecoveryNotice(err, "embedded configuration"), true, err
		}
		return config, "", false, nil
	}

	pathName := filepath.Clean(r.Path)
	// Validate the directory before even inspecting the file or quarantining
	// it. In particular, a symlinked runtime config directory must never
	// redirect reads or recovery writes outside the artefacts tree.
	if err := prepareModelConfigDirectory(filepath.Dir(pathName)); err != nil {
		return r.recoverModelLocked(pathName, fmt.Errorf("prepare model configuration directory: %w", err), false)
	}
	if recovery := modelRecovery[pathName]; recovery.broken {
		if _, err := os.Lstat(pathName); errors.Is(err, os.ErrNotExist) {
			return emergencyConfig(), "", true, nil
		}
	}
	info, err := os.Lstat(pathName)
	if errors.Is(err, os.ErrNotExist) {
		config, decodeErr := decodeModelConfig(appconfig.OpenAIModels)
		if decodeErr != nil {
			return r.recoverLocked(pathName, fmt.Errorf("embedded model configuration: %w", decodeErr))
		}
		if writeErr := writeModelConfigLocked(pathName, config); writeErr != nil {
			// A failure after the atomic rename can still leave a usable file.
			if info, statErr := os.Lstat(pathName); statErr == nil && info.Mode().IsRegular() && info.Size() <= maxModelConfigBytes {
				if data, readErr := os.ReadFile(pathName); readErr == nil {
					if installed, parseErr := decodeModelConfig(data); parseErr == nil {
						return installed, "", false, nil
					}
				}
			}
			return r.recoverLocked(pathName, fmt.Errorf("install model configuration: %w", writeErr))
		}
		return config, "", false, nil
	}
	if err != nil {
		return r.recoverLocked(pathName, fmt.Errorf("inspect model configuration: %w", err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return r.recoverLocked(pathName, errors.New("model configuration must not be a symbolic link"))
	}
	if !info.Mode().IsRegular() {
		return r.recoverLocked(pathName, errors.New("model configuration must be a regular file"))
	}
	if info.Size() > maxModelConfigBytes {
		return r.recoverLocked(pathName, fmt.Errorf("model configuration exceeds %d bytes", maxModelConfigBytes))
	}
	data, err := os.ReadFile(pathName)
	if err != nil {
		return r.recoverLocked(pathName, fmt.Errorf("read model configuration: %w", err))
	}
	config, err := decodeModelConfig(data)
	if err != nil {
		return r.recoverLocked(pathName, err)
	}
	if recovery, ok := modelRecovery[pathName]; ok {
		if recovery.notice == "" {
			delete(modelRecovery, pathName)
		} else {
			recovery.broken = false
			modelRecovery[pathName] = recovery
		}
	}
	return config, "", false, nil
}

func (r ModelRegistry) recoverLocked(pathName string, cause error) (ModelConfig, string, bool, error) {
	return r.recoverModelLocked(pathName, cause, true)
}

func (r ModelRegistry) recoverModelLocked(pathName string, cause error, quarantine bool) (ModelConfig, string, bool, error) {
	recovery := modelRecovery[pathName]
	if recovery.broken {
		return emergencyConfig(), "", true, nil
	}
	var quarantinedPath string
	var quarantineErr error
	if quarantine {
		if _, err := os.Lstat(pathName); err == nil {
			quarantinedPath, quarantineErr = quarantineModelConfig(pathName)
		}
	}
	detail := boundedModelError(cause)
	if quarantineErr != nil {
		detail += "; preserve failed: " + boundedModelError(quarantineErr)
	}
	notice := fmt.Sprintf("OpenAI model configuration recovery: %s. ", detail)
	if quarantinedPath != "" {
		notice += "The bad file was preserved as " + filepath.Base(quarantinedPath) + ". "
	}
	notice += "Using emergency model " + EmergencyModel + " until a valid configuration is available."
	notice = boundText(notice, maxRecoveryNoticeBytes)
	if recovery.notice == "" {
		recovery.notice = notice
		recovery.reported = false
	}
	recovery.broken = true
	modelRecovery[pathName] = recovery
	return emergencyConfig(), notice, true, nil
}

// takeRecoveryNotice reserves the notice for exactly one normal model request.
func (r ModelRegistry) takeRecoveryNotice() string {
	if r.Path == "" {
		return ""
	}
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	pathName := filepath.Clean(r.Path)
	recovery := modelRecovery[pathName]
	notice := recovery.notice
	if notice != "" {
		recovery.notice = ""
		if recovery.broken {
			modelRecovery[pathName] = recovery
		} else {
			delete(modelRecovery, pathName)
		}
	}
	return notice
}

func decodeModelConfig(data []byte) (ModelConfig, error) {
	if len(data) == 0 || len(data) > maxModelConfigBytes {
		return ModelConfig{}, fmt.Errorf("model configuration size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config ModelConfig
	if err := decoder.Decode(&config); err != nil {
		return ModelConfig{}, fmt.Errorf("parse model configuration: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return ModelConfig{}, errors.New("model configuration contains trailing data")
	}
	if config.Provider != "openai" {
		return ModelConfig{}, errors.New(`model configuration provider must be "openai"`)
	}
	if len(config.Models) == 0 {
		return ModelConfig{}, errors.New("model configuration has no models")
	}
	seenIDs := make(map[string]bool, len(config.Models))
	seenAliases := make(map[string]bool, len(config.Models))
	for index := range config.Models {
		model := config.Models[index]
		if !validModelName(model.ID) {
			return ModelConfig{}, fmt.Errorf("model %d has an invalid exact ID", index+1)
		}
		if model.Alias != "" && !validModelName(model.Alias) {
			return ModelConfig{}, fmt.Errorf("model %s has an invalid alias", model.ID)
		}
		if seenIDs[model.ID] {
			return ModelConfig{}, fmt.Errorf("model ID %s is duplicated", model.ID)
		}
		seenIDs[model.ID] = true
		if model.Alias != "" {
			if seenAliases[model.Alias] {
				return ModelConfig{}, fmt.Errorf("model alias %s is duplicated", model.Alias)
			}
			seenAliases[model.Alias] = true
		}
	}
	for _, model := range config.Models {
		if model.Alias != "" && model.Alias != model.ID && seenIDs[model.Alias] {
			return ModelConfig{}, fmt.Errorf("model alias %s conflicts with an exact model ID", model.Alias)
		}
	}
	if config.Chats == nil {
		config.Chats = make(map[string]string)
	}
	for chatID, modelID := range config.Chats {
		if chatID == "" || strings.TrimSpace(chatID) != chatID {
			return ModelConfig{}, errors.New("model configuration has an invalid chat ID")
		}
		if _, err := strconv.ParseInt(chatID, 10, 64); err != nil {
			return ModelConfig{}, errors.New("model configuration has an invalid chat ID")
		}
		if !seenIDs[modelID] {
			return ModelConfig{}, fmt.Errorf("chat %s selects an unknown exact model ID", chatID)
		}
	}
	return config, nil
}

func validModelName(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) &&
		len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func selectionForChat(config ModelConfig, chatID int64) ModelSelection {
	modelID := config.Models[0].ID
	for _, model := range config.Models {
		if model.ID == DefaultModel {
			modelID = DefaultModel
			break
		}
	}
	if chatID != 0 {
		if selected := config.Chats[strconv.FormatInt(chatID, 10)]; selected != "" {
			modelID = selected
		}
	}
	for _, model := range config.Models {
		if model.ID == modelID {
			return ModelSelection{ID: model.ID, Alias: model.Alias}
		}
	}
	return ModelSelection{ID: config.Models[0].ID, Alias: config.Models[0].Alias}
}

func resolveConfiguredModel(config ModelConfig, reference string) (ModelSelection, error) {
	reference = strings.TrimSpace(reference)
	for _, model := range config.Models {
		if model.ID == reference || model.Alias != "" && model.Alias == reference {
			return ModelSelection{ID: model.ID, Alias: model.Alias}, nil
		}
	}
	return ModelSelection{}, fmt.Errorf("%w: %s", ErrModelNotConfigured, boundText(reference, maxModelErrorBytes))
}

func emergencyConfig() ModelConfig {
	return ModelConfig{
		Provider: "openai",
		Models:   []ConfiguredModel{{ID: EmergencyModel, Alias: "terra"}},
		Chats:    make(map[string]string),
	}
}

func emergencySelection() ModelSelection {
	return ModelSelection{ID: EmergencyModel, Alias: "terra"}
}

func writeModelConfigLocked(pathName string, config ModelConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model configuration: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxModelConfigBytes {
		return fmt.Errorf("model configuration exceeds %d bytes", maxModelConfigBytes)
	}
	directory := filepath.Dir(pathName)
	if err := prepareModelConfigDirectory(directory); err != nil {
		return fmt.Errorf("prepare model configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".openai-models-*.tmp")
	if err != nil {
		return fmt.Errorf("create model configuration temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict model configuration temporary file: %w", err)
	}
	if n, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write model configuration: %w", err)
	} else if n != len(data) {
		_ = temporary.Close()
		return errors.New("write model configuration: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync model configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close model configuration temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, pathName); err != nil {
		return fmt.Errorf("replace model configuration: %w", err)
	}
	if err := os.Chmod(pathName, 0600); err != nil {
		return fmt.Errorf("restrict model configuration: %w", err)
	}
	if err := syncSessionDirectory(directory); err != nil {
		return fmt.Errorf("sync model configuration directory: %w", err)
	}
	return nil
}

func prepareModelConfigDirectory(directory string) error {
	root, rootErr := artefacts.Root()
	if rootErr == nil && filepath.Clean(directory) == filepath.Join(root, "config") {
		if _, err := artefacts.EnsureRuntimeDirectory("config"); err != nil {
			return fmt.Errorf("prepare model configuration directory: %w", err)
		}
	} else {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(directory, 0700); err != nil {
				return fmt.Errorf("create model configuration directory: %w", err)
			}
			info, err = os.Lstat(directory)
		}
		if err != nil {
			return fmt.Errorf("inspect model configuration directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("model configuration directory must be a real directory")
		}
	}
	return nil
}

func quarantineModelConfig(pathName string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		suffix := time.Now().UnixNano()
		if attempt > 0 {
			suffix += int64(attempt)
		}
		quarantinePath := fmt.Sprintf("%s.corrupt-%d", pathName, suffix)
		if _, err := os.Lstat(quarantinePath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(pathName, quarantinePath); err != nil {
			return "", err
		}
		return quarantinePath, nil
	}
	return "", errors.New("could not choose a quarantine path")
}

func boundedRecoveryNotice(cause error, source string) string {
	notice := "OpenAI model configuration recovery: " + boundedModelError(cause) + ". Using emergency model " + EmergencyModel + " from " + source + "."
	return boundText(notice, maxRecoveryNoticeBytes)
}

func boundedModelError(err error) string {
	if err == nil {
		return "unknown configuration error"
	}
	return boundText(strings.Join(strings.Fields(err.Error()), " "), maxModelErrorBytes)
}

func boundText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	if max == 1 {
		return "…"
	}
	var bounded strings.Builder
	for _, character := range value {
		candidateBytes := bounded.Len() + utf8.RuneLen(character)
		if candidateBytes+len("…") > max {
			break
		}
		bounded.WriteRune(character)
	}
	return bounded.String() + "…"
}

func (s *Session) refreshModel() {
	if s.registry.Path == "" {
		return
	}
	state, _ := s.registry.Current(s.chatID)
	if state.Selection.ID != "" {
		s.active = state.Selection.ID
	}
	if state.RecoveryNotice == "" {
		return
	}
	if s.startupNotice == "" {
		s.startupNotice = state.RecoveryNotice
	} else if !strings.Contains(s.startupNotice, state.RecoveryNotice) {
		s.startupNotice += " " + state.RecoveryNotice
	}
}

// PendingModelRecoveryNotice returns the one-time recovery message that will
// be supplied to the model on the next normal turn.
func (s *Session) PendingModelRecoveryNotice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshModel()
	if s.registry.Path == "" {
		return ""
	}
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	pathName := filepath.Clean(s.registry.Path)
	recovery := modelRecovery[pathName]
	if recovery.notice == "" || recovery.reported {
		return ""
	}
	recovery.reported = true
	modelRecovery[pathName] = recovery
	return recovery.notice
}

// Resolve diagnostic model names only against the trusted configured registry.
func configuredModelDiagnostic(registry ModelRegistry, model string) []diagnostics.Field {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	config, _, _, err := registry.loadLocked()
	if err != nil {
		return nil
	}
	for _, configured := range config.Models {
		if configured.ID == model {
			return []diagnostics.Field{{Key: "configured_model", Value: configured.ID}}
		}
	}
	return nil
}
