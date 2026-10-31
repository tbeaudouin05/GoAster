package chat

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalUIKeepsLineProtocolOutsideInteractiveTerminals(t *testing.T) {
	var output bytes.Buffer
	ui := terminalUI{}
	ui.renderIntro(&output, "chat provider")
	ui.renderStatus(&output, "gpt-test", "")
	ui.renderPrompt(&output)
	ui.renderAssistant(&output, "gpt-test", "hello")

	result := output.String()
	for _, want := range []string{
		"GoAster — chat provider",
		"Type a message, then press Enter.",
		"Active model: gpt-test",
		"you> ",
		"assistant [gpt-test]> hello",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("output missing %q: %s", want, result)
		}
	}
	if strings.Contains(result, "\x1b[") {
		t.Fatalf("noninteractive output contains terminal escape codes: %q", result)
	}
}

func TestTerminalUIWrapsAssistantBubbleToAvailableWidth(t *testing.T) {
	var output bytes.Buffer
	ui := terminalUI{interactive: true, width: 48}
	ui.renderAssistant(&output, "gpt-test", "alpha beta gamma delta epsilon zeta eta theta https://example.test/a-long-path")

	result := output.String()
	if !strings.Contains(result, "GoAster · gpt-test") {
		t.Fatalf("bubble missing assistant label: %s", result)
	}
	if !strings.Contains(result, "│ alpha beta gamma delta epsilon zeta eta") || !strings.Contains(result, "│ theta") {
		t.Fatalf("bubble did not wrap words as expected: %s", result)
	}
	for _, line := range strings.Split(strings.TrimSuffix(result, "\n"), "\n") {
		if terminalTextWidth(line) > ui.width {
			t.Errorf("line width = %d, want at most %d: %q", terminalTextWidth(line), ui.width, line)
		}
	}
}

func TestWrapTerminalTextPreservesParagraphBreaksAndSplitsLongWords(t *testing.T) {
	lines := wrapTerminalText("alpha beta\n\n0123456789", 5)
	want := []string{"alpha", "beta", "", "01234", "56789"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapped lines = %q, want %q", lines, want)
	}
}

func TestNewTerminalInputDoesNotTreatPipeInputAsInteractive(t *testing.T) {
	input := NewTerminalInput(strings.NewReader("hello\n"))
	if input.Interactive() {
		t.Fatal("pipe input was reported as interactive")
	}
}
