package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"goaster/internal/chat"
)

const (
	processHandoffToolName      = "process_handoff"
	processHandoffProfileOpenAI = "openai"
)

type processHandoffMode string

const (
	processHandoffModeInteractive processHandoffMode = "interactive"
	processHandoffModeBackground  processHandoffMode = "background"
)

const (
	processHandoffToolNoTerminal = "Process handoff was not started because no protected terminal is available. Run GoAster from an interactive terminal."
	processHandoffToolInvalid    = "Process handoff was not started because its request was invalid."
	processHandoffToolDeclined   = "Process handoff was not started because local terminal approval was not granted."
	processHandoffToolFailed     = "Process handoff or its verification failed. Review the local terminal output before continuing."
	processHandoffToolSucceeded  = "Process handoff and its verification completed successfully."
)

type processHandoffToolArguments struct {
	Profile        string `json:"profile"`
	Mode           string `json:"mode,omitempty"`
	Target         string `json:"target"`
	Key            string `json:"key"`
	KnownHosts     string `json:"known_hosts,omitempty"`
	AgentTransport string `json:"agent_transport"`
	AgentSocket    string `json:"agent_socket,omitempty"`
	Port           int    `json:"port,omitempty"`
}

// processHandoffRequest is deliberately a profile request, not a command
// request. The profile selects fixed local behavior; the model cannot provide
// an executable, command, or argument list.
type processHandoffRequest struct {
	Profile        string
	Mode           processHandoffMode
	Target         string
	IdentityFile   string
	KnownHostsFile string
	AgentTransport string
	AgentSocket    string
	Port           int
}

// processHandoff is owned by the local CLI. Only a generic status crosses this
// boundary; interactive streams are never supplied by the model tool.
type processHandoff func(context.Context, processHandoffRequest) string

type processHandoffInteractiveRunner func(context.Context, processHandoffRequest, io.ReadWriter, sshRunner) error
type processHandoffBackgroundRunner func(context.Context, processHandoffRequest) error

// processHandoffProfile contains fixed behavior for one allowlisted profile.
// Background is a supervised extension point; no current profile enables it.
type processHandoffProfile struct {
	interactive processHandoffInteractiveRunner
	background  processHandoffBackgroundRunner
}

func processHandoffProfileFor(name string) (processHandoffProfile, bool) {
	switch strings.TrimSpace(name) {
	case processHandoffProfileOpenAI:
		return processHandoffProfile{
			interactive: runOpenAIInteractiveProcessHandoff,
		}, true
	default:
		return processHandoffProfile{}, false
	}
}

func registerProcessHandoffTool(registrar chat.ToolRegistrar, handoff processHandoff) error {
	return registrar.RegisterTool(newProcessHandoffTool(handoff))
}

func newProcessHandoffTool(handoff processHandoff) chat.Tool {
	return chat.Tool{
		Name:        processHandoffToolName,
		Description: "This is an internal profile-based process handoff; invoke it yourself and do not ask the user to type its name or the goaster remote-login command. Only the fixed openai profile is currently available, and only interactive mode can run through this chat tool; background mode is not supported by process_handoff. Use the separate background_command tool only for names in the local background-command allowlist. For the openai profile, after the user has explicitly approved the exact remote host in chat and its GoAster service account is verified ready, start the protected local interactive login and health check using a pre-trusted known_hosts entry. It asks for a separate YES approval in the local terminal, attaches the login directly to the terminal, and returns only a generic status. It uses the controlling terminal when available; otherwise it uses inherited stdin and stdout only when both are the same actual terminal device. If no protected terminal is available, it stops without connecting. Never use this for an unverified host or a host that is not ready. The profile fixes the executable and remote operations; this tool never accepts a command or executable string.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"profile": map[string]any{
					"type":        "string",
					"enum":        []string{processHandoffProfileOpenAI},
					"description": "Fixed handoff profile; currently only openai is allowlisted",
				},
				"mode": map[string]any{
					"type":        []string{"string", "null"},
					"enum":        []any{string(processHandoffModeInteractive), string(processHandoffModeBackground), nil},
					"default":     string(processHandoffModeInteractive),
					"description": "Handoff mode; interactive is the only mode this chat tool can invoke, while background is reserved and rejected",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "The exact approved SSH target, including the remote service username, such as goaster@server.example",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Path to the approved local SSH private key; a leading ~/ is expanded locally",
				},
				"known_hosts": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Optional local known_hosts file containing the already verified host key",
				},
				"agent_socket": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Required with socket transport: exact absolute Unix socket path from the reviewed running agent configuration; omit for tcp",
				},
				"agent_transport": map[string]any{
					"type":        "string",
					"enum":        []string{remoteTransportSocket, remoteTransportTCP},
					"description": "Required: inspect the running remote agent configuration and select socket or tcp; never infer tcp from a missing socket path",
				},
				"port": map[string]any{
					"type":        []string{"integer", "null"},
					"minimum":     1,
					"maximum":     65535,
					"description": "SSH port, default 22",
				},
			},
			"required":             []string{"profile", "target", "key", "agent_transport"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeProcessHandoffTool(ctx, arguments, handoff), nil
		},
	}
}

func executeProcessHandoffTool(ctx context.Context, arguments string, handoff processHandoff) string {
	if ctx == nil || ctx.Err() != nil || handoff == nil {
		return processHandoffToolFailed
	}
	request, err := parseProcessHandoffToolRequest(arguments)
	if err != nil {
		return processHandoffToolInvalid
	}
	// The chat-facing surface is intentionally narrower than the shared
	// foundation. A future supervised caller may use background mode, but this
	// tool cannot start a background process.
	if request.Mode != processHandoffModeInteractive {
		return processHandoffToolInvalid
	}
	if _, ok := processHandoffProfileFor(request.Profile); !ok {
		return processHandoffToolInvalid
	}
	return handoff(ctx, request)
}

func parseProcessHandoffToolRequest(arguments string) (processHandoffRequest, error) {
	trimmed := strings.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return processHandoffRequest{}, fmt.Errorf("process_handoff arguments must be a JSON object")
	}
	if err := rejectDuplicateProcessHandoffKeys(trimmed); err != nil {
		return processHandoffRequest{}, fmt.Errorf("decode process_handoff arguments: %w", err)
	}

	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var parsed processHandoffToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return processHandoffRequest{}, fmt.Errorf("decode process_handoff arguments: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return processHandoffRequest{}, fmt.Errorf("process_handoff arguments contain trailing data")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return processHandoffRequest{}, fmt.Errorf("read process_handoff argument fields: %w", err)
	}
	for name := range fields {
		switch name {
		case "profile", "mode", "target", "key", "known_hosts", "agent_transport", "agent_socket", "port":
		default:
			return processHandoffRequest{}, fmt.Errorf("invalid process_handoff argument field")
		}
	}
	if !processHandoffFieldPresent(fields, "mode") {
		parsed.Mode = string(processHandoffModeInteractive)
	}
	if !processHandoffFieldPresent(fields, "port") {
		parsed.Port = 22
	}
	if processHandoffFieldPresent(fields, "agent_socket") && !validRemoteAgentSocket(parsed.AgentSocket) {
		return processHandoffRequest{}, fmt.Errorf("remote agent socket path is invalid")
	}
	key, err := expandProcessHandoffPath(parsed.Key)
	if err != nil {
		return processHandoffRequest{}, err
	}
	knownHosts, err := expandProcessHandoffPath(parsed.KnownHosts)
	if err != nil {
		return processHandoffRequest{}, err
	}
	request := processHandoffRequest{
		Profile:        strings.TrimSpace(parsed.Profile),
		Mode:           processHandoffMode(strings.TrimSpace(parsed.Mode)),
		Target:         parsed.Target,
		IdentityFile:   key,
		KnownHostsFile: knownHosts,
		AgentTransport: parsed.AgentTransport,
		AgentSocket:    parsed.AgentSocket,
		Port:           parsed.Port,
	}
	if err := validateProcessHandoffRequest(request); err != nil {
		return processHandoffRequest{}, err
	}
	return request, nil
}

func rejectDuplicateProcessHandoffKeys(arguments string) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			var seen []string
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid object key")
				}
				for _, previous := range seen {
					if strings.EqualFold(previous, key) {
						return fmt.Errorf("duplicate object key %q", key)
					}
				}
				seen = append(seen, key)
				if err := readValue(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	return readValue()
}

func processHandoffFieldPresent(fields map[string]json.RawMessage, name string) bool {
	value, exists := fields[name]
	return exists && strings.TrimSpace(string(value)) != "null"
}

func validateProcessHandoffRequest(request processHandoffRequest) error {
	profile, ok := processHandoffProfileFor(request.Profile)
	if !ok {
		return fmt.Errorf("unknown process handoff profile %q", request.Profile)
	}
	switch request.Mode {
	case processHandoffModeInteractive:
		if profile.interactive == nil {
			return fmt.Errorf("process handoff profile %q has no interactive runner", request.Profile)
		}
		return validateRemoteLoginOptions(request.remoteLoginOptions())
	case processHandoffModeBackground:
		if profile.background == nil {
			return fmt.Errorf("process handoff profile %q has no supervised background runner", request.Profile)
		}
		return nil
	default:
		return fmt.Errorf("unknown process handoff mode %q", request.Mode)
	}
}

func (request processHandoffRequest) remoteLoginOptions() remoteLoginOptions {
	return remoteLoginOptions{
		Target:         request.Target,
		IdentityFile:   request.IdentityFile,
		KnownHostsFile: request.KnownHostsFile,
		AgentTransport: request.AgentTransport,
		AgentSocket:    request.AgentSocket,
		Port:           request.Port,
	}
}

// runOpenAIInteractiveProcessHandoff is the fixed implementation behind the
// allowlisted openai profile. It supplies the existing SSH-backed login and
// health-check sequence; no model-provided process details reach it.
func runOpenAIInteractiveProcessHandoff(ctx context.Context, request processHandoffRequest, terminal io.ReadWriter, sshRun sshRunner) error {
	return runRemoteLoginWith(ctx, request.remoteLoginOptions(), terminal, terminal, terminal, sshRun)
}

func expandProcessHandoffPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find local home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	relative := strings.TrimLeft(path[1:], `/\`)
	return filepath.Join(home, filepath.FromSlash(relative)), nil
}
