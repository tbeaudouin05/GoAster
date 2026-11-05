package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunDoesNotAcceptLoginCommand(t *testing.T) {
	var output bytes.Buffer
	writeFatalError(&output, "goaster:", errors.New("provider \x1b[31mfailed\x1b[0m"))
	if got, want := output.String(), "goaster: provider failed\n"; got != want {
		t.Fatalf("fatal error output = %q, want %q", got, want)
	}
	output.Reset()
	if err := run(context.Background(), []string{"login"}, strings.NewReader(""), &output, &output); err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("login command error = %v", err)
	}
	helpOutput := newChannelWriter()
	if err := run(context.Background(), []string{"-help"}, strings.NewReader(""), helpOutput, helpOutput); err != nil {
		t.Fatalf("chat help error = %v", err)
	}
	help := helpOutput.String()
	for _, want := range []string{
		"Describe what you want in chat",
		"/env: securely import environment variables.",
		"/get-env: edit the current env file or get a blank template.",
		"/file: inspect non-secret files.",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("chat help missing %q: %s", want, help)
		}
	}
	remoteHelpOutput := newChannelWriter()
	if err := run(context.Background(), []string{"remote-login", "-help"}, strings.NewReader(""), remoteHelpOutput, remoteHelpOutput); err != nil {
		t.Fatalf("remote-login help error = %v", err)
	}
	remoteHelp := remoteHelpOutput.String()
	if !strings.Contains(remoteHelp, "internal command for exceptional use") || !strings.Contains(remoteHelp, "ask GoAster in chat") {
		t.Errorf("remote-login help does not explain the normal chat flow: %s", remoteHelp)
	}
}
