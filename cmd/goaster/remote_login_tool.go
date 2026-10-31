package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"goaster/internal/chat"
)

const remoteLoginToolName = "remote_login"

const (
	remoteLoginToolInvalid   = "Remote login was not started because its request was invalid."
	remoteLoginToolDeclined  = "Remote login was not started because local terminal approval was not granted."
	remoteLoginToolFailed    = "Remote login or its health check failed. Review the local terminal output before continuing."
	remoteLoginToolSucceeded = "Remote login and the remote health check completed successfully."
)

type remoteLoginToolArguments struct {
	Target     string `json:"target"`
	Key        string `json:"key"`
	KnownHosts string `json:"known_hosts,omitempty"`
	Port       int    `json:"port,omitempty"`
}

func registerRemoteLoginTool(registrar chat.ToolRegistrar, approvalInput chat.TerminalInput, interactiveInput io.Reader, output, errorOutput io.Writer, sshRun sshRunner) error {
	return registrar.RegisterTool(newRemoteLoginTool(approvalInput, interactiveInput, output, errorOutput, sshRun))
}

func newRemoteLoginTool(approvalInput chat.TerminalInput, interactiveInput io.Reader, output, errorOutput io.Writer, sshRun sshRunner) chat.Tool {
	if output == nil {
		output = io.Discard
	}
	if errorOutput == nil {
		errorOutput = io.Discard
	}
	return chat.Tool{
		Name:        remoteLoginToolName,
		Description: "After the user has explicitly approved the exact existing remote host in chat and its GoAster service account is verified ready, start the existing local interactive OpenAI login and health check. It asks for a separate YES approval in the local terminal, attaches the login directly to the terminal, and returns only a generic status. Never use this for provisioning, an unverified host, or a host that is not ready.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "The exact approved SSH target, including the remote service username, such as goaster@server.example",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Path to the approved local SSH private key; a leading ~/ is expanded locally",
				},
				"known_hosts": map[string]any{
					"type":        "string",
					"description": "Optional local known_hosts file containing the already verified host key",
				},
				"port": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     65535,
					"description": "SSH port, default 22",
				},
			},
			"required":             []string{"target", "key"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeRemoteLoginTool(ctx, arguments, approvalInput, interactiveInput, output, errorOutput, sshRun), nil
		},
	}
}

func executeRemoteLoginTool(ctx context.Context, arguments string, approvalInput chat.TerminalInput, interactiveInput io.Reader, output, errorOutput io.Writer, sshRun sshRunner) string {
	if ctx == nil || ctx.Err() != nil {
		return remoteLoginToolFailed
	}
	options, err := parseRemoteLoginToolOptions(arguments)
	if err != nil {
		fmt.Fprintln(output, "Remote login was not started because its request was invalid.")
		return remoteLoginToolInvalid
	}
	if interactiveInput == nil {
		fmt.Fprintln(output, "Remote login requires the local interactive terminal.")
		return remoteLoginToolFailed
	}
	knownHosts := "default known_hosts file"
	if options.KnownHostsFile != "" {
		knownHosts = strconv.Quote(options.KnownHostsFile)
	}

	if _, err := fmt.Fprintf(output,
		"\nRemote login approval required.\nTarget: %q\nSSH port: %d\nLocal SSH key: %s\nKnown-hosts file: %s\nHost-key verification: strict; an unknown or changed key will stop the connection.\nType YES to start the interactive login and health check: ",
		options.Target, options.Port, strconv.Quote(options.IdentityFile), knownHosts); err != nil {
		return remoteLoginToolFailed
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return remoteLoginToolFailed
		}
	}
	answer, completed, err := approvalInput.ReadLine(4096)
	if err != nil || !completed || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fmt.Fprintln(output, "\nRemote login canceled; no connection was made.")
		return remoteLoginToolDeclined
	}

	if err := runRemoteLoginWith(ctx, options, interactiveInput, output, errorOutput, sshRun); err != nil {
		fmt.Fprintf(errorOutput, "Remote login did not complete: %v. Review the local terminal output before continuing.\n", err)
		return remoteLoginToolFailed
	}
	return remoteLoginToolSucceeded
}

func parseRemoteLoginToolOptions(arguments string) (remoteLoginOptions, error) {
	trimmed := strings.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return remoteLoginOptions{}, fmt.Errorf("remote_login arguments must be a JSON object")
	}

	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var parsed remoteLoginToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return remoteLoginOptions{}, fmt.Errorf("decode remote_login arguments: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return remoteLoginOptions{}, fmt.Errorf("remote_login arguments contain trailing data")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return remoteLoginOptions{}, fmt.Errorf("read remote_login argument fields: %w", err)
	}
	if _, exists := fields["port"]; !exists {
		parsed.Port = 22
	}
	key, err := expandRemoteLoginPath(parsed.Key)
	if err != nil {
		return remoteLoginOptions{}, err
	}
	knownHosts, err := expandRemoteLoginPath(parsed.KnownHosts)
	if err != nil {
		return remoteLoginOptions{}, err
	}
	options := remoteLoginOptions{
		Target:         parsed.Target,
		IdentityFile:   key,
		KnownHostsFile: knownHosts,
		Port:           parsed.Port,
	}
	if err := validateRemoteLoginOptions(options); err != nil {
		return remoteLoginOptions{}, err
	}
	return options, nil
}

func expandRemoteLoginPath(path string) (string, error) {
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
