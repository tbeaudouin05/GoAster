package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunOfflineEchoRemainsAvailable(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-offline"}, strings.NewReader("hello\n/exit\n"), &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"assistant [gpt-6.1-sol]> Echo: hello", "Goodbye."} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q: %s", want, output.String())
		}
	}
}

func TestRunDoesNotAcceptLoginCommand(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"login"}, strings.NewReader(""), &output, &output); err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("login command error = %v", err)
	}
}
