package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/chat"
)

type functionToolRegistrar struct {
	register func(chat.Tool) error
}

func (registrar functionToolRegistrar) RegisterTool(tool chat.Tool) error {
	return registrar.register(tool)
}

type channelWriter struct {
	writes chan string
}

func newChannelWriter() channelWriter {
	return channelWriter{writes: make(chan string, 64)}
}

func (writer channelWriter) Write(content []byte) (int, error) {
	writer.writes <- string(content)
	return len(content), nil
}

func (writer channelWriter) String() string {
	count := len(writer.writes)
	parts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		parts = append(parts, <-writer.writes)
	}
	return strings.Join(parts, "")
}

func TestRemoteLoginToolUsesApprovedInteractiveFlowWithoutRelayingOutput(t *testing.T) {
	input := strings.NewReader("YES\nADMIN-INPUT\n")
	output := newChannelWriter()
	errorOutput := newChannelWriter()
	var calls [][]string
	registered := chat.Tool{}
	registrar := functionToolRegistrar{register: func(tool chat.Tool) error {
		registered = tool
		return nil
	}}
	runner := func(_ context.Context, arguments []string, gotInput io.Reader, gotOutput, gotErrorOutput io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		if len(calls) == 1 {
			remaining, err := io.ReadAll(gotInput)
			if err != nil || string(remaining) != "ADMIN-INPUT\n" {
				t.Errorf("interactive input after approval = %q, error %v", remaining, err)
			}
			fmt.Fprintln(gotOutput, "DEVICE-CODE-MUST-STAY-LOCAL")
		}
		return nil
	}
	if err := registerRemoteLoginTool(registrar, chat.NewTerminalInput(input), input, &output, &errorOutput, runner); err != nil {
		t.Fatalf("register remote login tool: %v", err)
	}
	if registered.Name != remoteLoginToolName || registered.Handler == nil {
		t.Fatalf("registered tool = %+v", registered)
	}
	if registered.Parameters["additionalProperties"] != false {
		t.Fatalf("tool schema allows extra arguments: %+v", registered.Parameters)
	}

	result, err := registered.Handler(context.Background(), `{"target":"goaster@host.example","key":"~/.ssh/goaster-ovh","known_hosts":"~/.ssh/verified_hosts","port":2222}`)
	if err != nil {
		t.Fatalf("remote_login handler: %v", err)
	}
	if result != remoteLoginToolSucceeded {
		t.Fatalf("tool result = %q, want %q", result, remoteLoginToolSucceeded)
	}
	if strings.Contains(result, "DEVICE-CODE-MUST-STAY-LOCAL") {
		t.Fatalf("tool result relayed terminal output: %q", result)
	}
	if len(calls) != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", len(calls))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	localOutput := output.String()
	if !strings.Contains(localOutput, "Type YES") || !strings.Contains(localOutput, "DEVICE-CODE-MUST-STAY-LOCAL") || !strings.Contains(localOutput, "Remote agent login and health check completed.") || !strings.Contains(localOutput, filepath.Join(home, ".ssh", "verified_hosts")) {
		t.Fatalf("local terminal output = %q", localOutput)
	}
	login := strings.Join(calls[0], " ")
	for _, want := range []string{
		"-tt",
		"-i " + filepath.Join(home, ".ssh", "goaster-ovh"),
		"-p 2222",
		"StrictHostKeyChecking=yes",
		"IdentitiesOnly=yes",
		"UserKnownHostsFile=" + filepath.Join(home, ".ssh", "verified_hosts"),
		"goaster@host.example",
		remoteLoginCommand,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("login arguments missing %q: %s", want, login)
		}
	}
	health := strings.Join(calls[1], " ")
	for _, want := range []string{"-T", "StrictHostKeyChecking=yes", "goaster@host.example", remoteHealthCommand} {
		if !strings.Contains(health, want) {
			t.Errorf("health-check arguments missing %q: %s", want, health)
		}
	}
}

func TestRemoteLoginToolDeclineAndInvalidArgumentsDoNotConnect(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		arguments string
		want      string
	}{
		{name: "declined", input: "no\n", arguments: `{"target":"goaster@host.example","key":"/tmp/key"}`, want: remoteLoginToolDeclined},
		{name: "missing enter", input: "YES", arguments: `{"target":"goaster@host.example","key":"/tmp/key"}`, want: remoteLoginToolDeclined},
		{name: "unknown argument", input: "YES\n", arguments: `{"target":"goaster@host.example","key":"/tmp/key","command":"anything"}`, want: remoteLoginToolInvalid},
		{name: "invalid port", input: "YES\n", arguments: `{"target":"goaster@host.example","key":"/tmp/key","port":0}`, want: remoteLoginToolInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tool := newRemoteLoginTool(chat.NewTerminalInput(strings.NewReader(test.input)), strings.NewReader("unused"), io.Discard, io.Discard,
				func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
					calls++
					return nil
				})
			result, err := tool.Handler(context.Background(), test.arguments)
			if err != nil {
				t.Fatalf("remote_login handler: %v", err)
			}
			if result != test.want {
				t.Fatalf("tool result = %q, want %q", result, test.want)
			}
			if calls != 0 {
				t.Fatalf("SSH calls = %d, want none", calls)
			}
		})
	}
}

func TestRemoteLoginToolReturnsOnlyGenericFailureStatus(t *testing.T) {
	secret := "DEVICE-CODE-OR-SSH-OUTPUT"
	calls := 0
	output := newChannelWriter()
	errorOutput := newChannelWriter()
	tool := newRemoteLoginTool(chat.NewTerminalInput(strings.NewReader("YES\n")), strings.NewReader(""), output, errorOutput,
		func(_ context.Context, _ []string, _ io.Reader, gotOutput, _ io.Writer) error {
			calls++
			if calls == 1 {
				fmt.Fprintln(gotOutput, secret)
				return nil
			}
			return errors.New(secret)
		})
	result, err := tool.Handler(context.Background(), `{"target":"goaster@host.example","key":"/tmp/key"}`)
	if err != nil {
		t.Fatalf("remote_login handler: %v", err)
	}
	if result != remoteLoginToolFailed || strings.Contains(result, secret) {
		t.Fatalf("tool result = %q, want generic failure without terminal output", result)
	}
	if calls != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", calls)
	}
	localOutput := output.String()
	localErrorOutput := errorOutput.String()
	if !strings.Contains(localOutput, secret) || strings.Contains(localErrorOutput, secret) || !strings.Contains(localErrorOutput, "Review the local terminal output") {
		t.Fatalf("local outputs = stdout %q, stderr %q", localOutput, localErrorOutput)
	}
}
