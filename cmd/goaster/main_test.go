package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunDoesNotAcceptLoginCommand(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"login"}, strings.NewReader(""), &output, &output); err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("login command error = %v", err)
	}
}
