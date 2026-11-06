package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRemoteLoginUsesProtectedTerminalAndHealthCheck(t *testing.T) {
	input := strings.NewReader("terminal input")
	var output bytes.Buffer
	var calls [][]string
	var inputs []io.Reader
	var outputs []io.Writer
	var errorOutputs []io.Writer
	runner := func(_ context.Context, arguments []string, gotInput io.Reader, gotOutput, gotErrorOutput io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		inputs = append(inputs, gotInput)
		outputs = append(outputs, gotOutput)
		errorOutputs = append(errorOutputs, gotErrorOutput)
		return nil
	}

	options, err := parseRemoteLogin([]string{
		"--target", "goaster@example.invalid",
		"--key", "/tmp/goaster-remote",
		"--port", "2222",
		"--known-hosts", "/tmp/known_hosts",
		"--agent-transport", "tcp",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	terminal := testTerminal{Reader: input, Writer: &output}
	err = runRemoteLoginOnTerminal(context.Background(), options, func() (io.ReadWriteCloser, error) {
		return terminal, nil
	}, runner)
	if err != nil {
		t.Fatalf("remote login: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", len(calls))
	}
	for index := range inputs {
		if inputs[index] != terminal || outputs[index] != terminal || errorOutputs[index] != terminal {
			t.Fatalf("SSH call %d did not use protected terminal streams", index)
		}
	}

	login := strings.Join(calls[0], " ")
	for _, want := range []string{
		"-tt",
		"-i /tmp/goaster-remote",
		"-p 2222",
		"StrictHostKeyChecking=yes",
		"UpdateHostKeys=no",
		"IdentitiesOnly=yes",
		"GlobalKnownHostsFile=none",
		"UserKnownHostsFile=/tmp/known_hosts",
		"goaster@example.invalid",
		remoteLoginCommand,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("login SSH arguments missing %q: %s", want, login)
		}
	}
	for _, forbidden := range []string{" sh ", " -c ", "auth.json", "token"} {
		if strings.Contains(login, forbidden) {
			t.Errorf("login SSH arguments contain forbidden %q: %s", forbidden, login)
		}
	}

	health := strings.Join(calls[1], " ")
	for _, want := range []string{"-T", "goaster@example.invalid", remoteHealthCommand} {
		if !strings.Contains(health, want) {
			t.Errorf("health SSH arguments missing %q: %s", want, health)
		}
	}
	if !strings.Contains(output.String(), "Remote agent login and health check completed.") {
		t.Errorf("completion output = %q", output.String())
	}
}

func TestRemoteLoginRequiresExplicitSafeInputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		options remoteLoginOptions
		want    string
	}{
		{name: "missing target", options: remoteLoginOptions{IdentityFile: "/tmp/key"}, want: "requires --target"},
		{name: "missing key", options: remoteLoginOptions{Target: "goaster@example.invalid"}, want: "requires --key"},
		{name: "target whitespace", options: remoteLoginOptions{Target: "goaster@example invalid", IdentityFile: "/tmp/key"}, want: "single SSH destination"},
		{name: "target option", options: remoteLoginOptions{Target: "-oProxyCommand=bad", IdentityFile: "/tmp/key"}, want: "single SSH destination"},
		{name: "invalid port", options: remoteLoginOptions{Target: "goaster@example.invalid", IdentityFile: "/tmp/key", Port: 0}, want: "between 1 and 65535"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			runner := func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				called = true
				return nil
			}
			err := runRemoteLoginWith(context.Background(), test.options, nil, nil, nil, runner)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if called {
				t.Fatal("SSH runner was called for invalid options")
			}
		})
	}
}

func TestRemoteLoginUsesOnlyConfiguredSocketForHealth(t *testing.T) {
	options := remoteLoginOptions{
		Target: "root@example.invalid", IdentityFile: "/tmp/key", Port: 22,
		AgentTransport: remoteTransportSocket,
		AgentSocket:    "/run/goaster-agent/agent.sock",
	}
	var calls [][]string
	runner := func(_ context.Context, arguments []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		if len(calls) == 2 && arguments[len(arguments)-1] != remoteHealthCommand {
			return errors.New("socket unavailable")
		}
		// A TCP impersonator would answer successfully; it must not be used.
		return nil
	}
	err := runRemoteLoginWith(context.Background(), options, nil, io.Discard, io.Discard, runner)
	if err == nil || !strings.Contains(err.Error(), "remote agent health check failed") || len(calls) != 2 {
		t.Fatalf("socket health failure = %v, calls = %d", err, len(calls))
	}
	health := strings.Join(calls[1], " ")
	if !strings.Contains(health, "/usr/bin/env GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock /usr/local/bin/goaster-agent health") {
		t.Fatalf("health check did not use the configured Unix socket: %s", health)
	}
	if strings.Contains(health, remoteHealthCommand) {
		t.Fatalf("socket health check included the TCP fallback: %s", health)
	}
}

func TestRemoteLoginRejectsUnsafeSocketArguments(t *testing.T) {
	for _, socket := range []string{"", "relative", "/run/a/../b", "/run/a;id", "/run/a b", "/run/a'bad", "/"} {
		_, err := parseRemoteLogin([]string{"--target", "root@example.invalid", "--key", "/tmp/key", "--agent-transport", "socket", "--agent-socket", socket}, io.Discard)
		if err == nil {
			t.Errorf("accepted socket path %q", socket)
		}
	}
	for _, arguments := range [][]string{
		{},
		{"--agent-socket", "/run/agent.sock"},
		{"--agent-transport", "socket"},
		{"--agent-transport", ""},
		{"--agent-transport", "auto"},
		{"--agent-transport", "tcp", "--agent-socket", "/run/agent.sock"},
	} {
		args := append([]string{"--target", "root@example.invalid", "--key", "/tmp/key"}, arguments...)
		calls := 0
		err := runRemoteLogin(context.Background(), args, nil, io.Discard, io.Discard, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			calls++
			return nil
		})
		if err == nil || calls != 0 {
			t.Errorf("ambiguous CLI transport %q: error %v, SSH calls %d", arguments, err, calls)
		}
	}
	for _, options := range []remoteLoginOptions{
		{},
		{AgentTransport: remoteTransportSocket},
		{AgentTransport: remoteTransportTCP, AgentSocket: "/run/agent.sock"},
	} {
		if command, err := remoteHealthCommandFor(options); err == nil || command != "" {
			t.Errorf("ambiguous transport generated health command %q, error %v", command, err)
		}
	}
}

func TestRemoteLoginReportsFailuresWithoutCommandDetails(t *testing.T) {
	secret := "remote-device-token"
	calls := 0
	runner := func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		calls++
		return errors.New(secret)
	}
	options := remoteLoginOptions{Target: "goaster@example.invalid", IdentityFile: "/tmp/key", Port: 22, AgentTransport: remoteTransportTCP}
	err := runRemoteLoginWith(context.Background(), options, nil, io.Discard, io.Discard, runner)
	if err == nil || !strings.Contains(err.Error(), "remote agent login failed") {
		t.Fatalf("error = %v, want bounded login failure", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed command detail: %v", err)
	}
	if calls != 1 {
		t.Fatalf("SSH calls = %d, want 1 after login failure", calls)
	}
}

func TestRemoteLoginReportsHealthFailureAfterSuccessfulLogin(t *testing.T) {
	calls := 0
	runner := func(_ context.Context, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		calls++
		if calls == 2 {
			return errors.New("exit status 7")
		}
		return nil
	}
	options := remoteLoginOptions{Target: "goaster@example.invalid", IdentityFile: "/tmp/key", Port: 22, AgentTransport: remoteTransportTCP}
	err := runRemoteLoginWith(context.Background(), options, nil, io.Discard, io.Discard, runner)
	if err == nil || !strings.Contains(err.Error(), "remote agent health check failed") {
		t.Fatalf("error = %v, want health-check failure", err)
	}
	if !strings.Contains(err.Error(), "SSH exit status 7") {
		t.Fatalf("error = %v, want SSH exit status", err)
	}
	if calls != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", calls)
	}
}

func TestRemoteLoginExistingTargetUsesOnlyPreTrustedKnownHosts(t *testing.T) {
	home, identityFile, knownHostsFile := prepareRemoteTargetFiles(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	options := remoteLoginOptions{
		Target:         "root@host.example",
		IdentityFile:   identityFile,
		KnownHostsFile: knownHostsFile,
		AgentTransport: remoteTransportTCP,
		Port:           22,
	}
	var calls [][]string
	if err := runRemoteLoginWith(context.Background(), options, strings.NewReader(""), io.Discard, io.Discard, func(_ context.Context, arguments []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		return nil
	}); err != nil {
		t.Fatalf("existing remote login: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("SSH calls = %d, want login and health check without preflight", len(calls))
	}
	for index, call := range calls {
		arguments := strings.Join(call, " ")
		if !strings.Contains(arguments, "StrictHostKeyChecking=yes") {
			t.Fatalf("existing-host SSH call %d did not require strict pre-trusted checking: %s", index+1, arguments)
		}
		if !strings.Contains(arguments, "UserKnownHostsFile="+knownHostsFile) || !strings.Contains(arguments, "GlobalKnownHostsFile=none") {
			t.Fatalf("existing-host SSH call %d did not use the pre-trusted known_hosts file: %s", index+1, arguments)
		}
	}
}
