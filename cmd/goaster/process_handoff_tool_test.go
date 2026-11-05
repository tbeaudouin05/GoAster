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

func TestProcessHandoffAcceptsOnlySafeAgentSocket(t *testing.T) {
	request, err := parseProcessHandoffToolRequest(`{"agent_transport":"socket","profile":"openai","target":"root@host.example","key":"/tmp/key","agent_socket":"/run/goaster-agent/agent.sock"}`)
	if err != nil || request.remoteLoginOptions().AgentTransport != remoteTransportSocket || request.remoteLoginOptions().AgentSocket != "/run/goaster-agent/agent.sock" {
		t.Fatalf("socket handoff request = %+v, %v", request, err)
	}
	for _, value := range []string{`""`, `null`, `"/run/a;id"`, `"/run/a/../b"`} {
		args := `{"agent_transport":"socket","profile":"openai","target":"root@host.example","key":"/tmp/key","agent_socket":` + value + `}`
		if _, err := parseProcessHandoffToolRequest(args); err == nil {
			t.Errorf("accepted invalid agent_socket %s", value)
		}
	}
	for _, arguments := range []string{
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key"}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_socket":"/run/agent.sock"}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":null}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":""}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":"auto"}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":"socket"}`,
		`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":"tcp","agent_socket":"/run/agent.sock"}`,
	} {
		calls := 0
		result, err := newProcessHandoffTool(func(context.Context, processHandoffRequest) string {
			calls++
			return processHandoffToolSucceeded
		}).Handler(context.Background(), arguments)
		if err != nil || result != processHandoffToolInvalid || calls != 0 {
			t.Errorf("ambiguous transport request %s: result %q, error %v, handoffs %d", arguments, result, err, calls)
		}
	}
	tcp, err := parseProcessHandoffToolRequest(`{"profile":"openai","target":"root@host.example","key":"/tmp/key","agent_transport":"tcp"}`)
	if err != nil || tcp.remoteLoginOptions().AgentTransport != remoteTransportTCP || tcp.AgentSocket != "" {
		t.Fatalf("explicit legacy TCP handoff = %+v, %v", tcp, err)
	}
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

func TestProcessHandoffToolUsesApprovedInteractiveFlowWithoutRelayingOutput(t *testing.T) {
	home := t.TempDir()
	sshDirectory := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDirectory, "goaster-ovh"), []byte("TEST-PRIVATE-KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDirectory, "verified_hosts"), []byte("host.example ssh-ed25519 TEST-HOST-KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	input := strings.NewReader("YES\nADMIN-INPUT\n")
	output := newChannelWriter()
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
	if err := registerProcessHandoffTool(registrar, newProtectedProcessHandoff(testTerminalOpener(input, output), runner)); err != nil {
		t.Fatalf("register process handoff tool: %v", err)
	}
	if registered.Name != processHandoffToolName || registered.Handler == nil {
		t.Fatalf("registered tool = %+v", registered)
	}
	if !strings.Contains(registered.Description, "internal profile-based process handoff") || !strings.Contains(registered.Description, "do not ask the user to type its name or the goaster remote-login command") {
		t.Fatalf("tool description does not explain the internal handoff: %q", registered.Description)
	}
	if registered.Parameters["additionalProperties"] != false {
		t.Fatalf("tool schema allows extra arguments: %+v", registered.Parameters)
	}
	required, ok := registered.Parameters["required"].([]string)
	if !ok || !strings.Contains(strings.Join(required, ","), "agent_transport") {
		t.Fatalf("tool schema does not require transport: %+v", registered.Parameters)
	}

	result, err := registered.Handler(context.Background(), `{"agent_transport":"tcp","profile":"openai","mode":"interactive","target":"goaster@host.example","key":"~/.ssh/goaster-ovh","known_hosts":"~/.ssh/verified_hosts","port":2222}`)
	if err != nil {
		t.Fatalf("process_handoff handler: %v", err)
	}
	if result != processHandoffToolSucceeded {
		t.Fatalf("tool result = %q, want %q", result, processHandoffToolSucceeded)
	}
	if strings.Contains(result, "DEVICE-CODE-MUST-STAY-LOCAL") {
		t.Fatalf("tool result relayed terminal output: %q", result)
	}
	if len(calls) != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", len(calls))
	}
	loaded, err := loadRemoteTargetRecord(home, filepath.Join(home, ".ssh", "goaster-ovh"))
	if err != nil {
		t.Fatalf("load saved connection record: %v", err)
	}
	if loaded != (remoteTargetRecord{Target: "goaster@host.example", Port: 2222, KnownHosts: "~/.ssh/verified_hosts"}) {
		t.Fatalf("saved connection record = %+v", loaded)
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

func TestProcessHandoffToolDeclineAndInvalidArgumentsDoNotConnect(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		arguments string
		want      string
	}{
		{name: "declined", input: "no\n", arguments: `{"agent_transport":"tcp","profile":"openai","target":"goaster@host.example","key":"/tmp/key"}`, want: processHandoffToolDeclined},
		{name: "missing enter", input: "YES", arguments: `{"agent_transport":"tcp","profile":"openai","target":"goaster@host.example","key":"/tmp/key"}`, want: processHandoffToolDeclined},
		{name: "unknown argument", input: "YES\n", arguments: `{"agent_transport":"tcp","profile":"openai","target":"goaster@host.example","key":"/tmp/key","command":"anything"}`, want: processHandoffToolInvalid},
		{name: "invalid port", input: "YES\n", arguments: `{"agent_transport":"tcp","profile":"openai","target":"goaster@host.example","key":"/tmp/key","port":0}`, want: processHandoffToolInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tool := newProcessHandoffTool(newProtectedProcessHandoff(testTerminalOpener(strings.NewReader(test.input), io.Discard),
				func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
					calls++
					return nil
				}))
			result, err := tool.Handler(context.Background(), test.arguments)
			if err != nil {
				t.Fatalf("process_handoff handler: %v", err)
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

func TestProcessHandoffToolReturnsOnlyGenericFailureStatus(t *testing.T) {
	secret := "DEVICE-CODE-OR-SSH-OUTPUT"
	calls := 0
	output := newChannelWriter()
	tool := newProcessHandoffTool(newProtectedProcessHandoff(testTerminalOpener(strings.NewReader("YES\n"), output),
		func(_ context.Context, _ []string, _ io.Reader, gotOutput, _ io.Writer) error {
			calls++
			if calls == 1 {
				fmt.Fprintln(gotOutput, secret)
				return nil
			}
			return errors.New(secret)
		}))
	result, err := tool.Handler(context.Background(), `{"agent_transport":"tcp","profile":"openai","target":"goaster@host.example","key":"/tmp/key"}`)
	if err != nil {
		t.Fatalf("process_handoff handler: %v", err)
	}
	if result != processHandoffToolFailed || strings.Contains(result, secret) {
		t.Fatalf("tool result = %q, want generic failure without terminal output", result)
	}
	if calls != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", calls)
	}
	localOutput := output.String()
	if strings.Count(localOutput, secret) != 1 || !strings.Contains(localOutput, "Review the local terminal output") {
		t.Fatalf("local terminal output = %q", localOutput)
	}
}

func TestProcessHandoffToolRejectsUnknownProfilesAndBackgroundMode(t *testing.T) {
	calls := 0
	tool := newProcessHandoffTool(func(context.Context, processHandoffRequest) string {
		calls++
		return processHandoffToolSucceeded
	})
	for _, arguments := range []string{
		`{"agent_transport":"tcp","profile":"unknown","mode":"interactive","target":"root@host.example","key":"/tmp/key"}`,
		`{"agent_transport":"tcp","profile":"openai","mode":"background","target":"root@host.example","key":"/tmp/key"}`,
	} {
		result, err := tool.Handler(context.Background(), arguments)
		if err != nil {
			t.Fatalf("process_handoff handler: %v", err)
		}
		if result != processHandoffToolInvalid {
			t.Fatalf("arguments %s returned %q, want %q", arguments, result, processHandoffToolInvalid)
		}
	}
	if calls != 0 {
		t.Fatalf("handoff calls = %d, want none for disallowed requests", calls)
	}
}

func TestProcessHandoffParsesVerifiedProvisionedTargetIdentity(t *testing.T) {
	request, err := parseProcessHandoffToolRequest(`{"agent_transport":"tcp","profile":"openai","target":"root@203.0.113.10","key":"/tmp/key","provisioned_target":{"instance_id":"instance-123","public_ip":"203.0.113.10"}}`)
	if err != nil {
		t.Fatalf("parse provisioned process handoff: %v", err)
	}
	if !request.HasProvisionedTarget {
		t.Fatal("provisioned target was not marked as present")
	}
	want := remoteProvisionedTarget{InstanceID: "instance-123", PublicIP: "203.0.113.10"}
	if request.ProvisionedTarget != want {
		t.Fatalf("provisioned target = %+v, want %+v", request.ProvisionedTarget, want)
	}
	if request.remoteLoginOptions().ProvisionedTarget != want {
		t.Fatalf("remote login options lost provisioned target: %+v", request.remoteLoginOptions().ProvisionedTarget)
	}

	for _, arguments := range []string{
		`{"agent_transport":"tcp","profile":"openai","target":"root@203.0.113.11","key":"/tmp/key","provisioned_target":{"instance_id":"instance-123","public_ip":"203.0.113.10"}}`,
		`{"agent_transport":"tcp","profile":"openai","target":"root@203.0.113.10","key":"/tmp/key","provisioned_target":{"instance_id":"../../other","public_ip":"203.0.113.10"}}`,
	} {
		if _, err := parseProcessHandoffToolRequest(arguments); err == nil {
			t.Fatalf("invalid provisioned target was accepted: %s", arguments)
		}
	}
}

func TestProcessHandoffRunsNewInstanceFirstContactBeforeStrictVerification(t *testing.T) {
	home := t.TempDir()
	sshDirectory := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(sshDirectory, "goaster-ovh")
	if err := os.WriteFile(identityFile, []byte("TEST-PRIVATE-KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	knownHostsFile := filepath.Join(sshDirectory, "goaster-ovh-instance-123.known_hosts")
	var calls [][]string
	runner := func(_ context.Context, arguments []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		if len(calls) == 1 {
			if err := os.WriteFile(knownHostsFile, []byte("203.0.113.10 ssh-ed25519 TEST-HOST-KEY\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	tool := newProcessHandoffTool(newProtectedProcessHandoff(testTerminalOpener(strings.NewReader("YES\n"), io.Discard), runner))
	result, err := tool.Handler(context.Background(), `{"agent_transport":"tcp","profile":"openai","target":"root@203.0.113.10","key":"~/.ssh/goaster-ovh","provisioned_target":{"instance_id":"instance-123","public_ip":"203.0.113.10"}}`)
	if err != nil || result != processHandoffToolSucceeded {
		t.Fatalf("process handoff result = %q, error = %v", result, err)
	}
	if len(calls) != 3 {
		t.Fatalf("SSH calls = %d, want first contact, login, and health check", len(calls))
	}
	if !strings.Contains(strings.Join(calls[0], " "), "StrictHostKeyChecking=accept-new") {
		t.Fatalf("first handoff SSH call did not accept the new host key: %s", strings.Join(calls[0], " "))
	}
	for _, call := range calls[1:] {
		arguments := strings.Join(call, " ")
		if !strings.Contains(arguments, "StrictHostKeyChecking=yes") || !strings.Contains(arguments, "UserKnownHostsFile="+knownHostsFile) {
			t.Fatalf("protected handoff SSH call did not use strict verification and the accepted key: %s", arguments)
		}
	}
	loaded, err := loadRemoteTargetRecord(home, identityFile)
	if err != nil {
		t.Fatalf("load handoff connection record: %v", err)
	}
	if loaded.KnownHosts != "~/.ssh/goaster-ovh-instance-123.known_hosts" {
		t.Fatalf("handoff connection record known_hosts = %q", loaded.KnownHosts)
	}
}
