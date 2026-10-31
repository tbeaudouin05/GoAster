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

func TestWrapTerminalTextPreservesIndentationAndExpandsTabs(t *testing.T) {
	lines := wrapTerminalText("  code\tvalue\n\n  next", 20)
	want := []string{"  code  value", "", "  next"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapped lines = %q, want %q", lines, want)
	}

	lines = wrapTerminalText("  alpha beta", 7)
	if strings.Join(lines, "|") != "  alpha|beta" {
		t.Fatalf("wrapped indentation = %q, want leading indentation on the first line", lines)
	}
}

func TestTerminalTextWidthUsesDisplayCellsAndCombiningMarks(t *testing.T) {
	if got, want := terminalTextWidth("a\tb"), 5; got != want {
		t.Fatalf("tab display width = %d, want %d", got, want)
	}
	if got, want := terminalTextWidth("A界😀e\u0301"), 6; got != want {
		t.Fatalf("Unicode display width = %d, want %d", got, want)
	}
	if got, want := terminalTextWidth("👩‍💻"), 2; got != want {
		t.Fatalf("joined emoji display width = %d, want %d", got, want)
	}
	chunk, rest := terminalTextChunk("e\u0301x", 1)
	if chunk != "e\u0301" || rest != "x" {
		t.Fatalf("combining-mark chunk = %q, rest %q", chunk, rest)
	}
}

func TestSanitizeTerminalTextRemovesCSIOSCAndControls(t *testing.T) {
	fixture := "before\x1b[31mred\x1b]0;hidden title\aafter\x07\x1b[2Jdone"
	if got, want := sanitizeTerminalText(fixture), "beforeredafterdone"; got != want {
		t.Fatalf("sanitized text = %q, want %q", got, want)
	}

	var output bytes.Buffer
	ui := terminalUI{interactive: true, color: false, width: 40}
	ui.renderNotice(&output, "Status", fixture)
	if strings.Contains(output.String(), "\x1b") || strings.Contains(output.String(), "\x07") {
		t.Fatalf("interactive notice retained a control sequence: %q", output.String())
	}
}

func TestTerminalUIUsesCompactASCIIAtNarrowWidths(t *testing.T) {
	fixture := "  code\tvalue\n\nA long URL https://example.test/" + strings.Repeat("segment/", 5) + "界😀e\u0301"
	for _, width := range []int{20, 31, 40, 80, 120} {
		var output bytes.Buffer
		ui := terminalUI{interactive: true, color: false, width: width}
		ui.renderIntro(&output, "description with a long name")
		ui.renderStatus(&output, strings.Repeat("model-", 5), strings.Repeat("fallback reason ", 4))
		ui.renderNotice(&output, "Notice", fixture)
		ui.renderAssistant(&output, "gpt-test", fixture)
		for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
			if terminalTextWidth(line) > width {
				t.Errorf("width %d produced line width %d: %q", width, terminalTextWidth(line), line)
			}
		}
	}

	var asciiOutput bytes.Buffer
	asciiUI := terminalUI{interactive: true, color: true, ascii: true, width: 31}
	asciiUI.renderIntro(&asciiOutput, "plain")
	asciiUI.renderStatus(&asciiOutput, "model", "reason")
	asciiUI.renderAssistant(&asciiOutput, "model", "reply")
	result := asciiOutput.String()
	if strings.Contains(result, "\x1b") || strings.ContainsAny(result, "╭╮╰╯│─●•›✓×") {
		t.Fatalf("ASCII fallback retained ANSI or Unicode markers: %q", result)
	}
}

func TestTerminalUINoColorKeepsUnicodeLayout(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	if terminalColorEnabled() {
		t.Fatal("NO_COLOR did not disable color")
	}
	ui := terminalUI{interactive: true, color: terminalColorEnabled(), width: 40}
	var output bytes.Buffer
	ui.renderPrompt(&output)
	if strings.Contains(output.String(), "\x1b") || !strings.Contains(output.String(), "›") {
		t.Fatalf("NO_COLOR changed the normal Unicode prompt layout: %q", output.String())
	}
}

func TestNewTerminalInputDoesNotTreatPipeInputAsInteractive(t *testing.T) {
	input := NewTerminalInput(strings.NewReader("hello\n"))
	if input.Interactive() {
		t.Fatal("pipe input was reported as interactive")
	}
}
