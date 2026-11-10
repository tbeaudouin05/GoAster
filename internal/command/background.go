package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"

	appconfig "goaster/config"
	"goaster/internal/artefacts"
)

const (
	backgroundCommandConfigFilename = "background-commands.json"
	maxBackgroundConfigBytes        = 64 << 10
	maxBackgroundCommands           = 32
	maxBackgroundSessions           = 32
	maxRunningBackgroundSessions    = 8
	backgroundInputQueueSize        = 4
	backgroundPTYOutputDrainTimeout = 250 * time.Millisecond
	// MaxSessionInputBytes bounds one model-to-process write and one key list.
	MaxSessionInputBytes = 64 << 10
)

var (
	ErrBackgroundSessionNotFound         = errors.New("background command session was not found")
	ErrBackgroundSessionDone             = errors.New("background command session has finished")
	ErrBackgroundSessionNotInteractive   = errors.New("background command session does not accept input")
	ErrBackgroundSessionInputUnavailable = errors.New("background command session input is unavailable")
	ErrBackgroundSessionInputBusy        = errors.New("background command session input queue is full")
	ErrBackgroundCleanupUnavailable      = errors.New("safe background process cleanup is unavailable: kernel-enforced descendant containment is required")
	ErrPTYNotSupported                   = errors.New("PTY sessions are not supported on this platform")
)

// BackgroundCommandConfig is the owner-managed list of named commands that
// the chat tool may start. Executables and arguments come from this local file,
// never from model-supplied tool input.
type BackgroundCommandConfig struct {
	Version  int                           `json:"version"`
	Commands []BackgroundCommandDefinition `json:"commands"`
}

// BackgroundCommandDefinition describes one locally configured executable and
// its owner-approved non-secret input mode.
type BackgroundCommandDefinition struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	CWD         string   `json:"cwd,omitempty"`
	Interactive bool     `json:"interactive,omitempty"`
	PTY         bool     `json:"pty,omitempty"`
}

// BackgroundRequest contains an executable, its exact arguments, and the
// owner-approved session I/O mode.
type BackgroundRequest struct {
	Executable  string
	Args        []string
	Dir         string
	Timeout     time.Duration
	Interactive bool
	PTY         bool
}

// BackgroundCommandInfo exposes only safe command metadata to chat.
type BackgroundCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	PTY         bool   `json:"pty,omitempty"`
}

// BackgroundSession is the bounded runtime record for one started command.
type BackgroundSession struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	State           string    `json:"state"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	ExitCode        int       `json:"exit_code"`
	StdoutTruncated bool      `json:"stdout_truncated,omitempty"`
	StderrTruncated bool      `json:"stderr_truncated,omitempty"`
	Interactive     bool      `json:"interactive,omitempty"`
	PTY             bool      `json:"pty,omitempty"`
	InputFailed     bool      `json:"input_failed,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// BackgroundSessionOutput pairs the current captured output with its runtime
// session metadata. Output remains bounded by MaxOutputBytes per stream and may
// include PTY echo; it is not a secret-safe channel.
type BackgroundSessionOutput struct {
	Session BackgroundSession `json:"session"`
	Stdout  string            `json:"stdout"`
	Stderr  string            `json:"stderr"`
}

// BackgroundExecution is the final result reported by a background process.
type BackgroundExecution struct {
	Result     Result
	Err        error
	FinishedAt time.Time
}

// BackgroundProcess controls one detached execution. Its functions and
// completion channel keep operating-system process details inside this
// package.
type BackgroundProcess struct {
	Stop        func()
	Snapshot    func() Result
	Write       func(string) error
	InputFailed func() bool
	KeyMode     func() BackgroundPtyKeyMode
	Done        <-chan BackgroundExecution
}

// BackgroundCommandManager owns in-memory sessions for one GoAster runtime.
// The command allowlist is reread before listing or starting a command, so an
// administrator can update it without restarting GoAster.
type BackgroundCommandManager struct {
	requests    chan backgroundManagerRequest
	done        chan struct{}
	closeResult chan error
}

type backgroundManagerRequest struct {
	action   string
	name     string
	id       string
	data     string
	keys     string
	ctx      context.Context
	response chan backgroundManagerResponse
}

type backgroundManagerResponse struct {
	commands []BackgroundCommandInfo
	sessions []BackgroundSession
	session  BackgroundSession
	output   BackgroundSessionOutput
	err      error
}

type managedBackgroundSession struct {
	session       BackgroundSession
	process       BackgroundProcess
	result        Result
	stopRequested bool
	finished      bool
	err           error
}

// LoadBackgroundCommandConfig loads the default private runtime config when
// path is empty. A missing config is initialized from the embedded template.
func LoadBackgroundCommandConfig(path string) (BackgroundCommandConfig, error) {
	path = strings.TrimSpace(path)
	root, rootErr := artefacts.Root()
	if path == "" {
		if rootErr != nil {
			return BackgroundCommandConfig{}, rootErr
		}
		directory, err := artefacts.EnsureRuntimeDirectory("config")
		if err != nil {
			return BackgroundCommandConfig{}, err
		}
		path = filepath.Join(directory, backgroundCommandConfigFilename)
	}
	path = filepath.Clean(path)
	if rootErr == nil && path == filepath.Join(root, "config", backgroundCommandConfigFilename) {
		if _, err := artefacts.EnsureRuntimeDirectory("config"); err != nil {
			return BackgroundCommandConfig{}, err
		}
	}
	if err := validateBackgroundConfigDirectory(filepath.Dir(path)); err != nil {
		return BackgroundCommandConfig{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := installBackgroundCommandConfig(path); err != nil {
			return BackgroundCommandConfig{}, err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxBackgroundConfigBytes {
		return BackgroundCommandConfig{}, errors.New("background command configuration is unavailable or invalid")
	}
	if !privateBackgroundConfigMode(info.Mode()) {
		return BackgroundCommandConfig{}, errors.New("background command configuration must be private")
	}
	file, err := os.Open(path)
	if err != nil {
		return BackgroundCommandConfig{}, errors.New("background command configuration is unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return BackgroundCommandConfig{}, errors.New("background command configuration is unavailable")
	}
	after, err := os.Lstat(path)
	if err != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !privateBackgroundConfigMode(opened.Mode()) || !privateBackgroundConfigMode(after.Mode()) || !os.SameFile(info, opened) || !os.SameFile(opened, after) {
		return BackgroundCommandConfig{}, errors.New("background command configuration changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBackgroundConfigBytes+1))
	if err != nil || len(data) > maxBackgroundConfigBytes || !utf8.Valid(data) {
		return BackgroundCommandConfig{}, errors.New("background command configuration is unavailable or invalid")
	}
	return decodeBackgroundCommandConfig(data)
}

func privateBackgroundConfigMode(mode os.FileMode) bool {
	// Windows controls access with ACLs; its Go FileMode permission bits do not
	// represent those ACLs. Use the same user-profile protection as model config.
	return runtime.GOOS == "windows" || mode.Perm()&0077 == 0
}

func validateBackgroundConfigDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return errors.New("background command configuration directory is unavailable")
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("background command configuration directory is unavailable")
	}
	return nil
}

func installBackgroundCommandConfig(path string) error {
	if _, err := decodeBackgroundCommandConfig(appconfig.BackgroundCommands); err != nil {
		return errors.New("embedded background command configuration is invalid")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".background-commands-*.tmp")
	if err != nil {
		return errors.New("background command configuration is unavailable")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return errors.New("background command configuration could not be initialized")
	}
	written, writeErr := file.Write(appconfig.BackgroundCommands)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(appconfig.BackgroundCommands) || syncErr != nil || closeErr != nil {
		return errors.New("background command configuration could not be initialized")
	}
	// Publish a complete template without replacing a config installed by an
	// operator or another GoAster process while it was being written.
	if err := os.Link(temporary, path); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("background command configuration could not be initialized")
	}
	return nil
}

func decodeBackgroundCommandConfig(data []byte) (BackgroundCommandConfig, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var parseValue func() (any, error)
	parseValue = func() (any, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("background command configuration is malformed")
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, errors.New("background command configuration is malformed")
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("background command configuration is malformed")
				}
				if _, exists := object[key]; exists {
					return nil, errors.New("background command configuration contains a duplicate field")
				}
				value, err := parseValue()
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("background command configuration is malformed")
			}
			return object, nil
		case '[':
			values := make([]any, 0)
			for decoder.More() {
				value, err := parseValue()
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("background command configuration is malformed")
			}
			return values, nil
		default:
			return nil, errors.New("background command configuration is malformed")
		}
	}
	value, err := parseValue()
	if err != nil {
		return BackgroundCommandConfig{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return BackgroundCommandConfig{}, errors.New("background command configuration contains trailing data")
	}
	root, ok := value.(map[string]any)
	if !ok || !hasOnlyBackgroundFields(root, "version", "commands") {
		return BackgroundCommandConfig{}, errors.New("background command configuration must contain version and commands")
	}
	versionNumber, ok := root["version"].(json.Number)
	if !ok {
		return BackgroundCommandConfig{}, errors.New("background command configuration version is invalid")
	}
	version, err := versionNumber.Int64()
	if err != nil || version != 1 {
		return BackgroundCommandConfig{}, errors.New("background command configuration version is unsupported")
	}
	entries, ok := root["commands"].([]any)
	if !ok || len(entries) > maxBackgroundCommands {
		return BackgroundCommandConfig{}, errors.New("background command configuration command list is invalid")
	}
	config := BackgroundCommandConfig{Version: 1, Commands: make([]BackgroundCommandDefinition, 0, len(entries))}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok || !hasOnlyBackgroundFields(object, "name", "description", "executable", "args", "cwd", "interactive", "pty") {
			return BackgroundCommandConfig{}, errors.New("background command definition is invalid")
		}
		name, nameOK := object["name"].(string)
		description, descriptionOK := optionalBackgroundString(object, "description")
		executable, executableOK := object["executable"].(string)
		argValues, argsOK := object["args"].([]any)
		args := make([]string, 0, len(argValues))
		for _, value := range argValues {
			arg, ok := value.(string)
			if !ok {
				argsOK = false
				break
			}
			args = append(args, arg)
		}
		cwd, cwdOK := optionalBackgroundString(object, "cwd")
		interactive, interactiveOK := optionalBackgroundBool(object, "interactive")
		ptyEnabled, ptyOK := optionalBackgroundBool(object, "pty")
		definition := BackgroundCommandDefinition{
			Name:        name,
			Description: description,
			Executable:  executable,
			Args:        args,
			CWD:         cwd,
			Interactive: interactive,
			PTY:         ptyEnabled,
		}
		if !nameOK || !descriptionOK || !executableOK || !argsOK || !cwdOK || !interactiveOK || !ptyOK || !validBackgroundCommandDefinition(definition) || seen[name] {
			return BackgroundCommandConfig{}, errors.New("background command definition is invalid")
		}
		seen[name] = true
		config.Commands = append(config.Commands, definition)
	}
	return config, nil
}

func hasOnlyBackgroundFields(object map[string]any, allowed ...string) bool {
	if len(object) > len(allowed) {
		return false
	}
	for key := range object {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func optionalBackgroundString(object map[string]any, key string) (string, bool) {
	value, exists := object[key]
	if !exists {
		return "", true
	}
	text, ok := value.(string)
	return text, ok
}

func optionalBackgroundBool(object map[string]any, key string) (bool, bool) {
	value, exists := object[key]
	if !exists {
		return false, true
	}
	flag, ok := value.(bool)
	return flag, ok
}

func validBackgroundCommandDefinition(definition BackgroundCommandDefinition) bool {
	if !validBackgroundCommandName(definition.Name) || !validBackgroundExecutable(definition.Executable, definition.Args) {
		return false
	}
	if strings.TrimSpace(definition.Description) != definition.Description || len(definition.Description) > 240 || strings.ContainsAny(definition.Description, "\r\n\x00") || !utf8.ValidString(definition.Description) {
		return false
	}
	return strings.TrimSpace(definition.CWD) == definition.CWD && !strings.ContainsAny(definition.CWD, "\r\n\x00") && utf8.ValidString(definition.CWD)
}

func validBackgroundExecutable(executable string, args []string) bool {
	if !filepath.IsAbs(executable) || len(executable) > MaxCommandBytes || strings.IndexByte(executable, 0) >= 0 || !utf8.ValidString(executable) {
		return false
	}
	for _, arg := range args {
		if len(arg) > MaxCommandBytes || strings.IndexByte(arg, 0) >= 0 || !utf8.ValidString(arg) {
			return false
		}
	}
	return true
}

func validBackgroundCommandName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

// NewBackgroundCommandManager locates the default configuration under the
// private GoAster artefact directory.
func NewBackgroundCommandManager() (BackgroundCommandManager, error) {
	root, err := artefacts.Root()
	if err != nil {
		return BackgroundCommandManager{}, err
	}
	return NewBackgroundCommandManagerAt(filepath.Join(root, "config", backgroundCommandConfigFilename)), nil
}

// NewBackgroundCommandManagerAt creates a runtime session manager that reads
// an explicit allowlist file before listing or starting commands.
func NewBackgroundCommandManagerAt(path string) BackgroundCommandManager {
	requests := make(chan backgroundManagerRequest)
	done := make(chan struct{})
	closeResult := make(chan error, 1)
	manager := BackgroundCommandManager{requests: requests, done: done, closeResult: closeResult}
	go runBackgroundCommandManager(filepath.Clean(path), requests, done, closeResult)
	return manager
}

// Configured lists the currently allowlisted command names and descriptions.
func (manager BackgroundCommandManager) Configured(ctx context.Context) ([]BackgroundCommandInfo, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "configured"})
	return response.commands, err
}

// Start starts one command by its configured name and returns a runtime ID.
func (manager BackgroundCommandManager) Start(ctx context.Context, name string) (BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "start", name: name})
	return response.session, err
}

// List returns the current in-memory runtime sessions.
func (manager BackgroundCommandManager) List(ctx context.Context) ([]BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "list"})
	return response.sessions, err
}

// Get returns one in-memory runtime session by ID.
func (manager BackgroundCommandManager) Get(ctx context.Context, id string) (BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "get", id: id})
	return response.session, err
}

// Output returns bounded captured output for one in-memory runtime session.
func (manager BackgroundCommandManager) Output(ctx context.Context, id string) (BackgroundSessionOutput, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "output", id: id})
	return response.output, err
}

// Input queues bounded non-secret text for an owner-authorized interactive
// session. It is not a secret-safe channel: input arguments and PTY echo may be
// visible to the model or saved by the caller. The session's executable and
// arguments remain entirely configuration-owned.
func (manager BackgroundCommandManager) Input(ctx context.Context, id, data string) (BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "input", id: id, data: data})
	return response.session, err
}

// SendKeys encodes named non-secret terminal keys and queues them for an
// owner-authorized PTY session. It is not a secret-safe channel.
func (manager BackgroundCommandManager) SendKeys(ctx context.Context, id, keys string) (BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "send-keys", id: id, keys: keys})
	return response.session, err
}

// Stop requests cancellation of one running session.
func (manager BackgroundCommandManager) Stop(ctx context.Context, id string) (BackgroundSession, error) {
	response, err := manager.call(ctx, backgroundManagerRequest{action: "stop", id: id})
	return response.session, err
}

// Close stops all active commands and releases the runtime manager.
func (manager BackgroundCommandManager) Close() error {
	if manager.requests == nil || manager.done == nil {
		return nil
	}
	select {
	case <-manager.done:
		return manager.closedResult()
	default:
	}
	response, err := manager.call(context.Background(), backgroundManagerRequest{action: "close"})
	if response.err != nil {
		return response.err
	}
	if manager.closeResult != nil {
		<-manager.done
		return manager.closedResult()
	}
	return err
}

// Keep the result available to every concurrent or subsequent Close caller.
func (manager BackgroundCommandManager) closedResult() error {
	if manager.closeResult == nil {
		return nil
	}
	err := <-manager.closeResult
	manager.closeResult <- err
	return err
}

func (manager BackgroundCommandManager) call(ctx context.Context, request backgroundManagerRequest) (backgroundManagerResponse, error) {
	if ctx == nil {
		return backgroundManagerResponse{}, errors.New("background command manager context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return backgroundManagerResponse{}, err
	}
	if manager.requests == nil || manager.done == nil {
		return backgroundManagerResponse{}, errors.New("background command manager is unavailable")
	}
	request.ctx = ctx
	if request.action == "start" {
		// A canceled caller must not lose the ID of a newly started process.
		// The manager stops it if the response cannot be delivered.
		request.response = make(chan backgroundManagerResponse)
	} else {
		request.response = make(chan backgroundManagerResponse, 1)
	}
	select {
	case manager.requests <- request:
	case <-manager.done:
		return backgroundManagerResponse{}, errors.New("background command manager is closed")
	case <-ctx.Done():
		return backgroundManagerResponse{}, ctx.Err()
	}
	select {
	case response := <-request.response:
		return response, response.err
	case <-manager.done:
		select {
		case response := <-request.response:
			return response, response.err
		default:
			return backgroundManagerResponse{}, errors.New("background command manager is closed")
		}
	case <-ctx.Done():
		select {
		case response := <-request.response:
			return response, response.err
		default:
			return backgroundManagerResponse{}, ctx.Err()
		}
	}
}

func runBackgroundCommandManager(path string, requests <-chan backgroundManagerRequest, done chan struct{}, closeResult chan error) {
	defer close(done)
	sessions := make(map[string]managedBackgroundSession)
	order := make([]string, 0, maxBackgroundSessions)
	nextID := 0
	for request := range requests {
		refreshManagedBackgroundSessions(sessions)
		response := backgroundManagerResponse{}
		if request.action != "close" && request.ctx != nil && request.ctx.Err() != nil {
			select {
			case request.response <- backgroundManagerResponse{err: request.ctx.Err()}:
			case <-request.ctx.Done():
			}
			continue
		}
		switch request.action {
		case "configured":
			config, err := LoadBackgroundCommandConfig(path)
			if err != nil {
				response.err = err
			} else {
				response.commands = make([]BackgroundCommandInfo, 0, len(config.Commands))
				for _, entry := range config.Commands {
					response.commands = append(response.commands, BackgroundCommandInfo{
						Name:        entry.Name,
						Description: entry.Description,
						Interactive: entry.Interactive || entry.PTY,
						PTY:         entry.PTY,
					})
				}
			}
		case "start":
			config, err := LoadBackgroundCommandConfig(path)
			if err != nil {
				response.err = err
				break
			}
			var selected BackgroundCommandDefinition
			for _, entry := range config.Commands {
				if entry.Name == request.name {
					selected = entry
					break
				}
			}
			if selected.Name == "" {
				response.err = errors.New("background command name is not allowlisted")
				break
			}
			if activeBackgroundSessionCount(sessions) >= maxRunningBackgroundSessions {
				response.err = errors.New("background command session limit reached")
				break
			}
			if request.ctx != nil && request.ctx.Err() != nil {
				response.err = request.ctx.Err()
				break
			}
			order, err = makeBackgroundSessionRoom(sessions, order)
			if err != nil {
				response.err = err
				break
			}
			if request.ctx != nil && request.ctx.Err() != nil {
				response.err = request.ctx.Err()
				break
			}
			process, err := StartBackground(BackgroundRequest{
				Executable:  selected.Executable,
				Args:        selected.Args,
				Dir:         selected.CWD,
				Interactive: selected.Interactive || selected.PTY,
				PTY:         selected.PTY,
			})
			if err != nil {
				if errors.Is(err, ErrPTYNotSupported) {
					response.err = ErrPTYNotSupported
					break
				}
				response.err = fmt.Errorf("background command could not be started: %w", err)
				break
			}
			nextID++
			id := fmt.Sprintf("bg-%06d", nextID)
			session := BackgroundSession{
				ID:          id,
				Name:        selected.Name,
				State:       "running",
				StartedAt:   time.Now().UTC(),
				ExitCode:    -1,
				Interactive: selected.Interactive || selected.PTY,
				PTY:         selected.PTY,
			}
			sessions[id] = managedBackgroundSession{session: session, process: process}
			order = append(order, id)
			response.session = session
		case "list":
			response.sessions = make([]BackgroundSession, 0, len(order))
			for _, id := range order {
				if session, exists := sessions[id]; exists {
					response.sessions = append(response.sessions, session.session)
				}
			}
		case "get", "output", "input", "send-keys", "stop":
			managed, exists := sessions[request.id]
			if !exists {
				response.err = ErrBackgroundSessionNotFound
				break
			}
			if request.action == "stop" && !managed.finished {
				managed.stopRequested = true
				managed.session.State = "stopping"
				managed.process.Stop()
				sessions[request.id] = managed
				refreshManagedBackgroundSessions(sessions)
				managed = sessions[request.id]
			}
			if request.action == "input" || request.action == "send-keys" {
				if managed.finished {
					response.err = ErrBackgroundSessionDone
					break
				}
				if !managed.session.Interactive {
					response.err = ErrBackgroundSessionNotInteractive
					break
				}
				data := request.data
				if request.action == "send-keys" {
					if !managed.session.PTY {
						response.err = ErrBackgroundSessionInputUnavailable
						break
					}
					mode := BackgroundPtyKeyModeCSI
					if managed.process.KeyMode != nil {
						mode = managed.process.KeyMode()
					}
					encoded, encodeErr := EncodeBackgroundKeySequence(request.keys, mode)
					if encodeErr != nil {
						response.err = encodeErr
						break
					}
					data = encoded
				}
				if managed.process.Write == nil {
					response.err = ErrBackgroundSessionInputUnavailable
					break
				}
				if err := managed.process.Write(data); err != nil {
					response.err = err
					break
				}
				response.session = managed.session
			} else if request.action == "output" {
				result := managed.result
				if !managed.finished {
					result = managed.process.Snapshot()
				}
				managed.session.StdoutTruncated = result.StdoutTruncated
				managed.session.StderrTruncated = result.StderrTruncated
				sessions[request.id] = managed
				response.output = BackgroundSessionOutput{Session: managed.session, Stdout: result.Stdout, Stderr: result.Stderr}
			} else {
				response.session = managed.session
			}
		case "close":
			response.err = closeManagedBackgroundSessions(sessions, 5*time.Second)
			closeResult <- response.err
			request.response <- response
			return
		default:
			response.err = errors.New("unknown background command manager operation")
		}
		if request.action == "start" {
			select {
			case request.response <- response:
			case <-request.ctx.Done():
				if response.session.ID != "" {
					managed := sessions[response.session.ID]
					managed.stopRequested = true
					managed.session.State = "stopping"
					managed.process.Stop()
					sessions[response.session.ID] = managed
				}
			}
		} else {
			request.response <- response
		}
	}
}

func refreshManagedBackgroundSessions(sessions map[string]managedBackgroundSession) {
	for id, managed := range sessions {
		if managed.process.InputFailed != nil {
			managed.session.InputFailed = managed.process.InputFailed()
			sessions[id] = managed
		}
		if managed.finished {
			continue
		}
		select {
		case execution, open := <-managed.process.Done:
			if managed.process.InputFailed != nil {
				managed.session.InputFailed = managed.process.InputFailed()
			}
			if !open {
				managed.session.State = "failed"
				managed.err = errors.New("background session ended without a cleanup result")
				managed.session.ExitCode = -1
			} else {
				managed.result = execution.Result
				managed.err = execution.Err
				managed.session.FinishedAt = execution.FinishedAt
				managed.session.ExitCode = execution.Result.ExitCode
				managed.session.StdoutTruncated = execution.Result.StdoutTruncated
				managed.session.StderrTruncated = execution.Result.StderrTruncated
				switch {
				case managed.stopRequested && execution.Err == context.Canceled:
					managed.session.State = "stopped"
				case execution.Err == context.DeadlineExceeded:
					managed.session.State = "timed_out"
				case execution.Err == ErrOutputLimit:
					managed.session.State = "output_limited"
				case execution.Err != nil:
					managed.session.State = "failed"
				default:
					managed.session.State = "exited"
				}
			}
			if managed.session.FinishedAt.IsZero() {
				managed.session.FinishedAt = time.Now().UTC()
			}
			managed.finished = true
			if managed.err != nil {
				managed.session.Error = managed.err.Error()
			}
		default:
			if !managed.stopRequested {
				result := managed.process.Snapshot()
				managed.session.StdoutTruncated = result.StdoutTruncated
				managed.session.StderrTruncated = result.StderrTruncated
			}
		}
		sessions[id] = managed
	}
}

func activeBackgroundSessionCount(sessions map[string]managedBackgroundSession) int {
	count := 0
	for _, managed := range sessions {
		if !managed.finished {
			count++
		}
	}
	return count
}

func makeBackgroundSessionRoom(sessions map[string]managedBackgroundSession, order []string) ([]string, error) {
	for len(order) >= maxBackgroundSessions {
		removed := false
		for index, id := range order {
			managed, exists := sessions[id]
			if exists && (!managed.finished || (managed.err != nil && managed.err != context.Canceled)) {
				continue
			}
			delete(sessions, id)
			order = append(order[:index], order[index+1:]...)
			removed = true
			break
		}
		if !removed {
			return order, errors.New("background command session limit reached")
		}
	}
	return order, nil
}

func closeManagedBackgroundSessions(sessions map[string]managedBackgroundSession, timeout time.Duration) error {
	var closeErr error
	for _, managed := range sessions {
		if managed.finished {
			if managed.err != context.Canceled {
				closeErr = errors.Join(closeErr, managed.err)
			}
		} else {
			managed.process.Stop()
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for id, managed := range sessions {
		if managed.finished {
			continue
		}
		select {
		case execution, open := <-managed.process.Done:
			managed.session.State = "failed"
			if open {
				managed.err = execution.Err
				managed.result = execution.Result
				managed.session.ExitCode = execution.Result.ExitCode
				managed.session.FinishedAt = execution.FinishedAt
				if execution.Err == nil || execution.Err == context.Canceled {
					managed.session.State = "stopped"
				} else {
					closeErr = errors.Join(closeErr, execution.Err)
				}
			} else {
				managed.err = errors.New("background session ended without a cleanup result")
				closeErr = errors.Join(closeErr, managed.err)
			}
		case <-deadline.C:
			return errors.Join(closeErr, errors.New("background session cleanup exceeded the shutdown deadline"))
		}
		managed.finished = true
		if managed.err != nil {
			managed.session.Error = managed.err.Error()
		}
		if managed.session.FinishedAt.IsZero() {
			managed.session.FinishedAt = time.Now().UTC()
		}
		sessions[id] = managed
	}
	return closeErr
}

// StartBackground refuses execution until safe descendant containment exists.
// Timeout zero runs until stopped, exited, or the bounded output limit is reached.
func StartBackground(req BackgroundRequest) (BackgroundProcess, error) {
	if !validBackgroundExecutable(req.Executable, req.Args) {
		return BackgroundProcess{}, errors.New("executable or arguments are invalid")
	}
	lifecycleSupported := backgroundLifecycleSupported()
	if req.PTY && (!lifecycleSupported || !backgroundPTYSupported()) {
		return BackgroundProcess{}, ErrPTYNotSupported
	}
	if !lifecycleSupported {
		return BackgroundProcess{}, ErrBackgroundCleanupUnavailable
	}
	if req.Timeout < 0 {
		return BackgroundProcess{}, errors.New("command timeout must not be negative")
	}
	if req.Timeout > MaxTimeout {
		return BackgroundProcess{}, errors.New("command timeout exceeds the maximum allowed duration")
	}
	if req.Dir != "" {
		resolvedDir, err := resolveWorkingDirectory(req.Dir)
		if err != nil {
			return BackgroundProcess{}, err
		}
		req.Dir = resolvedDir
	}
	runContext, stop := context.WithCancel(context.Background())
	if req.Timeout > 0 {
		timedContext, timeout := context.WithTimeout(runContext, req.Timeout)
		originalStop := stop
		stop = func() {
			timeout()
			originalStop()
		}
		runContext = timedContext
	}
	cmd := exec.CommandContext(runContext, req.Executable, req.Args...)
	cmd.Env = EnvironmentWithoutGitHubTokenOverrides()
	// Reuse the existing process-group setup on Linux; its timer-based
	// terminator is replaced below with cleanup performed before reaping.
	if runtime.GOOS == "linux" && !req.PTY {
		configureCommand(cmd)
	}
	cmd.WaitDelay = commandWaitDelay
	ready := make(chan struct{})
	cancelProcess := cmd.Cancel
	cmd.Cancel = func() error {
		<-ready
		return cancelProcess()
	}
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	var outputMu sync.Mutex
	var stdout = newLimitedOutput(MaxOutputBytes, nil)
	var stderr = newLimitedOutput(MaxOutputBytes, nil)
	var outputLimitReached atomic.Bool
	writeStdout := func(content []byte) (int, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		before := stdout.Truncated()
		count, err := stdout.Write(content)
		if !before && stdout.Truncated() {
			outputLimitReached.Store(true)
			stop()
		}
		return count, err
	}
	writeStderr := func(content []byte) (int, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		before := stderr.Truncated()
		count, err := stderr.Write(content)
		if !before && stderr.Truncated() {
			outputLimitReached.Store(true)
			stop()
		}
		return count, err
	}

	var terminal io.ReadWriteCloser
	var inputWriter io.Writer
	if req.PTY {
		master, err := pty.Start(cmd)
		if err == nil {
			terminal = master
		}
		if err != nil {
			stop()
			close(ready)
			if errors.Is(err, pty.ErrUnsupported) {
				return BackgroundProcess{}, ErrPTYNotSupported
			}
			return BackgroundProcess{}, errors.New("PTY could not be started")
		}
		inputWriter = terminal
	} else {
		cmd.Stdout = outputWriterFunc(writeStdout)
		cmd.Stderr = outputWriterFunc(writeStderr)
		if req.Interactive {
			stdin, err := cmd.StdinPipe()
			if err != nil {
				stop()
				close(ready)
				return BackgroundProcess{}, errors.New("command input could not be initialized")
			}
			inputWriter = stdin
		}
		if err := cmd.Start(); err != nil {
			stop()
			close(ready)
			return BackgroundProcess{}, errors.New("command could not be started")
		}
	}

	cancelProcess, finishProcess := backgroundProcessLifecycle(cmd.Process.Pid, req.PTY, cancelProcess)
	close(ready)
	done := make(chan BackgroundExecution, 1)
	writeInput, inputFailed, finishInput := backgroundInputWriter(inputWriter)

	var keyModeMu sync.RWMutex
	keyMode := BackgroundPtyKeyModeCSI
	keyDetector := backgroundPtyKeyModeDetector{}
	recordPTYOutput := func(content []byte) {
		if !req.PTY {
			return
		}
		keyModeMu.Lock()
		defer keyModeMu.Unlock()
		var detected BackgroundPtyKeyMode
		keyDetector, detected = observeBackgroundPtyKeyMode(keyDetector, content)
		if detected != backgroundPtyKeyModeNotFound {
			keyMode = detected
		}
	}
	currentKeyMode := func() BackgroundPtyKeyMode {
		keyModeMu.RLock()
		defer keyModeMu.RUnlock()
		return keyMode
	}
	snapshot := func() Result {
		outputMu.Lock()
		defer outputMu.Unlock()
		return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	}

	var readDone chan error
	if req.PTY {
		readDone = make(chan error, 1)
		go func() {
			defer close(readDone)
			buffer := make([]byte, 4096)
			for {
				count, err := terminal.Read(buffer)
				if count > 0 {
					recordPTYOutput(buffer[:count])
					if _, writeErr := writeStdout(buffer[:count]); writeErr != nil {
						readDone <- writeErr
						stop()
						return
					}
				}
				if err != nil {
					if errors.Is(err, io.EOF) || (runtime.GOOS == "linux" && errors.Is(err, syscall.EIO)) {
						err = nil
					}
					readDone <- err
					if err != nil {
						stop()
					}
					return
				}
			}
		}()
	}
	go func() {
		defer stop()
		cleanupErr := finishProcess()
		runErr := cmd.Wait()
		if terminal != nil {
			cleanupErr = errors.Join(cleanupErr, waitForBackgroundPTYOutput(terminal, readDone))
		}
		finishedAt := time.Now().UTC()
		result := snapshot()
		if cmd.ProcessState != nil {
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
		if outputLimitReached.Load() {
			runErr = ErrOutputLimit
			result.ExitCode = -1
		} else if runContext.Err() != nil {
			runErr = runContext.Err()
			result.ExitCode = -1
		} else if _, exited := runErr.(interface{ ExitCode() int }); exited && result.ExitCode >= 0 {
			// A nonzero process exit is a completed session, as in Execute.
			runErr = nil
		}
		if cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("background cleanup failed: %w", cleanupErr))
		}
		finishInput()
		done <- BackgroundExecution{Result: result, Err: runErr, FinishedAt: finishedAt}
		close(done)
	}()
	return BackgroundProcess{
		Stop:        stop,
		Snapshot:    snapshot,
		Write:       writeInput,
		InputFailed: inputFailed,
		KeyMode:     currentKeyMode,
		Done:        done,
	}, nil
}

// backgroundInputWriter acknowledges queue admission only. Completion marks any
// outstanding writes as failed or unconfirmed before publishing the result.
func backgroundInputWriter(writer io.Writer) (func(string) error, func() bool, func()) {
	queue := make(chan string, backgroundInputQueueSize)
	finished := make(chan struct{})
	var mu sync.Mutex
	failed, closed, pending := false, false, 0
	if writer != nil {
		go func() {
			for {
				select {
				case data := <-queue:
					mu.Lock()
					if closed {
						mu.Unlock()
						return
					}
					mu.Unlock()
					n, err := io.WriteString(writer, data)
					mu.Lock()
					pending--
					if err != nil || n != len(data) {
						failed = true
						mu.Unlock()
						return
					}
					mu.Unlock()
				case <-finished:
					return
				}
			}
		}()
	}
	write := func(data string) error {
		if len(data) > MaxSessionInputBytes {
			return fmt.Errorf("session input exceeds the %d-byte limit", MaxSessionInputBytes)
		}
		if writer == nil {
			return ErrBackgroundSessionInputUnavailable
		}
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return ErrBackgroundSessionDone
		}
		if failed {
			return ErrBackgroundSessionInputUnavailable
		}
		select {
		case queue <- data:
			pending++
			return nil
		default:
			return ErrBackgroundSessionInputBusy
		}
	}
	failure := func() bool { mu.Lock(); defer mu.Unlock(); return failed }
	finish := func() {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		if pending > 0 {
			failed = true
		}
		close(finished)
	}
	return write, failure, finish
}

func waitForBackgroundPTYOutput(terminal io.ReadWriteCloser, readDone <-chan error) error {
	timer := time.NewTimer(backgroundPTYOutputDrainTimeout)
	defer timer.Stop()
	var readErr error
	drained := false
	select {
	case readErr = <-readDone:
		drained = true
	case <-timer.C:
		readErr = errors.New("background PTY output drain exceeded its deadline")
	}
	closeErr := terminal.Close()
	// Closing the terminal releases a blocked reader. Preserve the original
	// read failure as well as close and drain-deadline failures.
	if !drained {
		timer.Reset(backgroundPTYOutputDrainTimeout)
		select {
		case finalErr := <-readDone:
			readErr = errors.Join(readErr, finalErr)
		case <-timer.C:
			readErr = errors.Join(readErr, errors.New("background PTY output reader did not finish after close"))
		}
	}
	return errors.Join(readErr, closeErr)
}

type outputWriterFunc func([]byte) (int, error)

func (writer outputWriterFunc) Write(content []byte) (int, error) {
	return writer(content)
}

// SortBackgroundCommandInfo returns a copy sorted by configured name.
func SortBackgroundCommandInfo(commands []BackgroundCommandInfo) []BackgroundCommandInfo {
	sorted := make([]BackgroundCommandInfo, len(commands))
	copy(sorted, commands)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].Name < sorted[right].Name })
	return sorted
}
