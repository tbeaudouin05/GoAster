package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prepareProvisionedRemoteLoginFiles(t *testing.T) (string, string) {
	t.Helper()
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
	return home, identityFile
}

func provisionedRemoteLoginOptions(identityFile string) remoteLoginOptions {
	return remoteLoginOptions{
		Target:       "root@203.0.113.10",
		IdentityFile: identityFile,
		Port:         22,
		ProvisionedTarget: remoteProvisionedTarget{
			InstanceID: "instance-123",
			PublicIP:   "203.0.113.10",
		},
	}
}

func targetSpecificKnownHostsPath(home string) string {
	return filepath.Join(home, ".ssh", "goaster-ovh-instance-123.known_hosts")
}

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
		"--key", "/tmp/goaster-ovh",
		"--port", "2222",
		"--known-hosts", "/tmp/known_hosts",
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
		"-i /tmp/goaster-ovh",
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

func TestRemoteLoginNewProvisionedTargetAcceptsOnceThenVerifiesAndPersists(t *testing.T) {
	home, identityFile := prepareProvisionedRemoteLoginFiles(t)
	knownHostsFile := targetSpecificKnownHostsPath(home)
	options := provisionedRemoteLoginOptions(identityFile)
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

	if err := runRemoteLoginWith(context.Background(), options, strings.NewReader(""), io.Discard, io.Discard, runner); err != nil {
		t.Fatalf("new remote login: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("SSH calls = %d, want first contact, login, and health check", len(calls))
	}
	firstContact := strings.Join(calls[0], " ")
	for _, want := range []string{
		"-T",
		"BatchMode=yes",
		"StrictHostKeyChecking=accept-new",
		"GlobalKnownHostsFile=none",
		"UserKnownHostsFile=" + knownHostsFile,
		"root@203.0.113.10",
		remoteFirstContactCommand,
	} {
		if !strings.Contains(firstContact, want) {
			t.Errorf("first-contact SSH arguments missing %q: %s", want, firstContact)
		}
	}
	if strings.Contains(firstContact, "StrictHostKeyChecking=yes") {
		t.Fatalf("first-contact SSH arguments used strict checking instead of accept-new: %s", firstContact)
	}
	for index, call := range calls[1:] {
		arguments := strings.Join(call, " ")
		if !strings.Contains(arguments, "StrictHostKeyChecking=yes") || strings.Contains(arguments, "StrictHostKeyChecking=accept-new") {
			t.Fatalf("SSH call %d did not use strict host-key checking: %s", index+2, arguments)
		}
		if !strings.Contains(arguments, "UserKnownHostsFile="+knownHostsFile) {
			t.Fatalf("SSH call %d did not reuse target-specific known_hosts: %s", index+2, arguments)
		}
	}
	info, err := os.Stat(knownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("target-specific known_hosts mode = %04o, want 0600", info.Mode().Perm())
	}
	loaded, err := loadRemoteTargetRecord(home, identityFile)
	if err != nil {
		t.Fatalf("load saved connection record: %v", err)
	}
	wantRecord := remoteTargetRecord{Target: options.Target, Port: options.Port, KnownHosts: "~/.ssh/goaster-ovh-instance-123.known_hosts"}
	if loaded != wantRecord {
		t.Fatalf("saved connection record = %+v, want %+v", loaded, wantRecord)
	}
}

func TestRemoteLoginNewProvisionedTargetChangedHostKeyFailsStrictLogin(t *testing.T) {
	home, identityFile := prepareProvisionedRemoteLoginFiles(t)
	knownHostsFile := targetSpecificKnownHostsPath(home)
	options := provisionedRemoteLoginOptions(identityFile)
	calls := 0
	err := runRemoteLoginWith(context.Background(), options, strings.NewReader(""), io.Discard, io.Discard, func(_ context.Context, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		calls++
		if calls == 1 {
			if writeErr := os.WriteFile(knownHostsFile, []byte("203.0.113.10 ssh-ed25519 ORIGINAL-HOST-KEY\n"), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
			return nil
		}
		if calls == 2 {
			return errors.New("host key verification failed: changed key")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "remote agent login failed") {
		t.Fatalf("changed-host-key error = %v, want strict login failure", err)
	}
	if calls != 2 {
		t.Fatalf("SSH calls = %d, want first contact and failed strict login", calls)
	}
	if _, statErr := os.Lstat(identityFile + remoteTargetRecordSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("connection record exists after changed-host-key failure: %v", statErr)
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
		if !strings.Contains(arguments, "StrictHostKeyChecking=yes") || strings.Contains(arguments, "StrictHostKeyChecking=accept-new") {
			t.Fatalf("existing-host SSH call %d did not require strict pre-trusted checking: %s", index+1, arguments)
		}
		if !strings.Contains(arguments, "UserKnownHostsFile="+knownHostsFile) || !strings.Contains(arguments, "GlobalKnownHostsFile=none") {
			t.Fatalf("existing-host SSH call %d did not use the pre-trusted known_hosts file: %s", index+1, arguments)
		}
	}
}

func TestRemoteLoginNewProvisionedTargetDoesNotPersistOnLoginOrHealthFailure(t *testing.T) {
	for _, failureCall := range []int{2, 3} {
		t.Run(map[int]string{2: "login", 3: "health"}[failureCall], func(t *testing.T) {
			home, identityFile := prepareProvisionedRemoteLoginFiles(t)
			knownHostsFile := targetSpecificKnownHostsPath(home)
			calls := 0
			err := runRemoteLoginWith(context.Background(), provisionedRemoteLoginOptions(identityFile), strings.NewReader(""), io.Discard, io.Discard, func(_ context.Context, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
				calls++
				if calls == 1 {
					if writeErr := os.WriteFile(knownHostsFile, []byte("203.0.113.10 ssh-ed25519 TEST-HOST-KEY\n"), 0600); writeErr != nil {
						t.Fatal(writeErr)
					}
				}
				if calls == failureCall {
					return errors.New("exit status 7")
				}
				return nil
			})
			if err == nil {
				t.Fatalf("failure at SSH call %d returned success", failureCall)
			}
			if calls != failureCall {
				t.Fatalf("SSH calls = %d, want %d", calls, failureCall)
			}
			if _, statErr := os.Lstat(identityFile + remoteTargetRecordSuffix); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("connection record exists after %s failure: %v", map[int]string{2: "login", 3: "health"}[failureCall], statErr)
			}
		})
	}
}
