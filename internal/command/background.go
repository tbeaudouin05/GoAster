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
	"time"
	"unicode/utf8"

	appconfig "goaster/config"
	"goaster/internal/artefacts"
)

const (
	backgroundCommandConfigFilename = "background-commands.json"
	maxBackgroundConfigBytes        = 64 << 10
	maxBackgroundCommands           = 32
	maxBackgroundSessions           = 32
	maxRunningBackgroundSessions    = 8
)

// BackgroundCommandConfig is the owner-managed list of named commands that
// the chat tool may start. Executables and arguments come from this local file,
// never from model-supplied tool input.
type BackgroundCommandConfig struct {
	Version  int                           `json:"version"`
	Commands []BackgroundCommandDefinition `json:"commands"`
}

// BackgroundCommandDefinition describes one locally configured executable.
type BackgroundCommandDefinition struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	CWD         string   `json:"cwd,omitempty"`
}

// BackgroundRequest contains an executable and its exact arguments.
type BackgroundRequest struct {
	Executable string
	Args       []string
	Dir        string
	Timeout    time.Duration
}

// BackgroundCommandInfo exposes only the safe name and description to chat.
type BackgroundCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
}

// BackgroundSessionOutput pairs the current captured output with its runtime
// session metadata. Output remains bounded by MaxOutputBytes per stream.
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
	Stop     func()
	Snapshot func() Result
	Done     <-chan BackgroundExecution
}

// BackgroundCommandManager owns in-memory sessions for one GoAster runtime.
// The command allowlist is reread before listing or starting a command, so an
// administrator can update it without restarting GoAster.
type BackgroundCommandManager struct {
	requests chan backgroundManagerRequest
	done     chan struct{}
}

type backgroundManagerRequest struct {
	action   string
	name     string
	id       string
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
		if !ok || !hasOnlyBackgroundFields(object, "name", "description", "executable", "args", "cwd") {
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
		definition := BackgroundCommandDefinition{Name: name, Description: description, Executable: executable, Args: args, CWD: cwd}
		if !nameOK || !descriptionOK || !executableOK || !argsOK || !cwdOK || !validBackgroundCommandDefinition(definition) || seen[name] {
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
	manager := BackgroundCommandManager{requests: requests, done: done}
	go runBackgroundCommandManager(filepath.Clean(path), requests, done)
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
		return nil
	default:
	}
	response, err := manager.call(context.Background(), backgroundManagerRequest{action: "close"})
	if err != nil {
		select {
		case <-manager.done:
			return nil
		default:
			return err
		}
	}
	return response.err
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

func runBackgroundCommandManager(path string, requests <-chan backgroundManagerRequest, done chan struct{}) {
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
					response.commands = append(response.commands, BackgroundCommandInfo{Name: entry.Name, Description: entry.Description})
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
			process, err := StartBackground(BackgroundRequest{Executable: selected.Executable, Args: selected.Args, Dir: selected.CWD})
			if err != nil {
				response.err = errors.New("background command could not be started")
				break
			}
			nextID++
			id := fmt.Sprintf("bg-%06d", nextID)
			session := BackgroundSession{ID: id, Name: selected.Name, State: "running", StartedAt: time.Now().UTC(), ExitCode: -1}
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
		case "get", "output", "stop":
			managed, exists := sessions[request.id]
			if !exists {
				response.err = errors.New("background command session was not found")
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
			if request.action == "output" {
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
			closeManagedBackgroundSessions(sessions, 5*time.Second)
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
		if managed.finished {
			continue
		}
		select {
		case execution, open := <-managed.process.Done:
			if !open {
				managed.session.State = "failed"
				managed.session.ExitCode = -1
			} else {
				managed.result = execution.Result
				managed.session.FinishedAt = execution.FinishedAt
				managed.session.ExitCode = execution.Result.ExitCode
				managed.session.StdoutTruncated = execution.Result.StdoutTruncated
				managed.session.StderrTruncated = execution.Result.StderrTruncated
				switch {
				case managed.stopRequested && errors.Is(execution.Err, context.Canceled):
					managed.session.State = "stopped"
				case errors.Is(execution.Err, context.DeadlineExceeded):
					managed.session.State = "timed_out"
				case errors.Is(execution.Err, ErrOutputLimit):
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
			if exists && !managed.finished {
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

func closeManagedBackgroundSessions(sessions map[string]managedBackgroundSession, timeout time.Duration) {
	for _, managed := range sessions {
		if !managed.finished {
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
			if open {
				managed.result = execution.Result
				managed.session.ExitCode = execution.Result.ExitCode
			}
		case <-deadline.C:
			return
		}
		managed.session.State = "stopped"
		managed.session.FinishedAt = time.Now().UTC()
		sessions[id] = managed
	}
}

// StartBackground executes the configured executable directly, using the same
// process-tree cleanup as Execute. Timeout zero means it runs until stopped, exits, or
// reaches the existing bounded output limit.
func StartBackground(req BackgroundRequest) (BackgroundProcess, error) {
	if !validBackgroundExecutable(req.Executable, req.Args) {
		return BackgroundProcess{}, errors.New("executable or arguments are invalid")
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
	cleanup := configureCommand(cmd)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	var outputMu sync.Mutex
	var stdout = newLimitedOutput(MaxOutputBytes, nil)
	var stderr = newLimitedOutput(MaxOutputBytes, nil)
	var outputLimitReached atomic.Bool
	cmd.Stdout = outputWriterFunc(func(content []byte) (int, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		before := stdout.Truncated()
		count, err := stdout.Write(content)
		if !before && stdout.Truncated() {
			outputLimitReached.Store(true)
			stop()
		}
		return count, err
	})
	cmd.Stderr = outputWriterFunc(func(content []byte) (int, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		before := stderr.Truncated()
		count, err := stderr.Write(content)
		if !before && stderr.Truncated() {
			outputLimitReached.Store(true)
			stop()
		}
		return count, err
	})
	if err := cmd.Start(); err != nil {
		cleanup()
		stop()
		return BackgroundProcess{}, errors.New("command could not be started")
	}
	done := make(chan BackgroundExecution, 1)
	snapshot := func() Result {
		outputMu.Lock()
		defer outputMu.Unlock()
		return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	}
	go func() {
		defer stop()
		runErr := cmd.Wait()
		finishedAt := time.Now().UTC()
		cleanup()
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
		done <- BackgroundExecution{Result: result, Err: runErr, FinishedAt: finishedAt}
		close(done)
	}()
	return BackgroundProcess{
		Stop:     stop,
		Snapshot: snapshot,
		Done:     done,
	}, nil
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
