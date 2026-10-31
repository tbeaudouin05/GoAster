package chat

import (
	"io"
	"strings"
	"testing"
)

func TestTerminalInputReadsOnlyThroughTheCurrentLine(t *testing.T) {
	reader := strings.NewReader("chat message\r\napproval\n")
	input := NewTerminalInput(reader)

	line, completed, err := input.ReadLine(maxTerminalInputLineBytes)
	if err != nil || !completed || line != "chat message" {
		t.Fatalf("first line = %q, completed %v, error %v", line, completed, err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remaining terminal input: %v", err)
	}
	if string(remaining) != "approval\n" {
		t.Fatalf("remaining terminal input = %q, want approval line intact", remaining)
	}
}

func TestTerminalInputReportsPartialEOFAndLineLimit(t *testing.T) {
	input := NewTerminalInput(strings.NewReader("final message"))
	line, completed, err := input.ReadLine(20)
	if err != nil || completed || line != "final message" {
		t.Fatalf("partial line = %q, completed %v, error %v", line, completed, err)
	}
	if _, _, err := input.ReadLine(20); err != io.EOF {
		t.Fatalf("next line error = %v, want EOF", err)
	}

	longLine := NewTerminalInput(strings.NewReader("too long\n"))
	if _, _, err := longLine.ReadLine(3); err == nil || !strings.Contains(err.Error(), "exceeds 3 bytes") {
		t.Fatalf("long line error = %v, want line limit error", err)
	}
}
