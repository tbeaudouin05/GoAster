package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRemoteLoginUsesDirectSSHWithInheritedStreamsAndHealthCheck(t *testing.T) {
	input := strings.NewReader("terminal input")
	var output bytes.Buffer
	var errorOutput bytes.Buffer
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

	err := runWithSSH(context.Background(), []string{
		"remote-login",
		"--target", "goaster@example.invalid",
		"--key", "/tmp/goaster-ovh",
		"--port", "2222",
		"--known-hosts", "/tmp/known_hosts",
	}, input, &output, &errorOutput, runner)
	if err != nil {
		t.Fatalf("remote login: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("SSH calls = %d, want login and health check", len(calls))
	}
	for index := range inputs {
		if inputs[index] != input || outputs[index] != &output || errorOutputs[index] != &errorOutput {
			t.Fatalf("SSH call %d did not inherit terminal streams", index)
		}
	}

	login := strings.Join(calls[0], " ")
	for _, want := range []string{
		"-tt",
		"-i /tmp/goaster-ovh",
		"-p 2222",
		"StrictHostKeyChecking=yes",
		"IdentitiesOnly=yes",
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

func TestRemoteLoginReportsFailuresWithoutCommandDetails(t *testing.T) {
	secret := "remote-device-token"
	calls := 0
	runner := func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		calls++
		return errors.New(secret)
	}
	options := remoteLoginOptions{Target: "goaster@example.invalid", IdentityFile: "/tmp/key", Port: 22}
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
	options := remoteLoginOptions{Target: "goaster@example.invalid", IdentityFile: "/tmp/key", Port: 22}
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
