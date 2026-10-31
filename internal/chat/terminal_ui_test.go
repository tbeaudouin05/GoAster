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

func TestTerminalUIIntroExplainsSlashCommandsAndQuit(t *testing.T) {
	for _, test := range []struct {
		name string
		ui   terminalUI
	}{
		{name: "line protocol", ui: terminalUI{}},
		{name: "interactive", ui: terminalUI{interactive: true, width: 40}},
		{name: "interactive ASCII", ui: terminalUI{interactive: true, ascii: true, width: 40}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			test.ui.renderIntro(&output, "chat provider")

			result := strings.Join(strings.Fields(output.String()), " ")
			want := "Use slash commands: /env to securely upload environment variables, /file to upload screenshots and other types of files. Ctrl-D to quit."
			if !strings.Contains(result, want) {
				t.Errorf("intro missing %q: %s", want, output.String())
			}
			for _, stale := range []string{
				"Describe what you want in plain language.",
				"/env  local secrets",
				"/file  inspect files",
			} {
				if strings.Contains(output.String(), stale) {
					t.Errorf("intro still contains stale help %q: %s", stale, output.String())
				}
			}
		})
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

func TestTerminalTextWidthUsesIndependentUnicodeCellFixtures(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{name: "ordinary ZWJ text", text: "a\u200db", want: 2},
		{name: "non-emoji ZWJ text", text: "\u3031\u200d\u3031", want: 4},
		{name: "wide non-emoji ZWJ text", text: "\u2329\u200d\u2329", want: 4},
		{name: "supplementary wide Kana Extended-B character", text: "\U0001aff0", want: 2},
		{name: "supplementary wide character", text: "\U0001b001", want: 2},
		{name: "supplementary wide character at range end", text: "\U0001b11f", want: 2},
		{name: "emoji presentation", text: "\u2600\ufe0f", want: 2},
		{name: "emoji ZWJ sequence", text: "👩‍💻", want: 2},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			if got := terminalTextWidth(fixture.text); got != fixture.want {
				t.Fatalf("display width of %q = %d, want %d", fixture.text, got, fixture.want)
			}
		})
	}
}

func TestWrapTerminalTextKeepsArbitraryZWJTextWithinWidth(t *testing.T) {
	text := strings.Repeat("a\u200db", 18)
	lines := wrapTerminalText(text, 12)
	want := []string{
		strings.Repeat("a\u200db", 6),
		strings.Repeat("a\u200db", 6),
		strings.Repeat("a\u200db", 6),
	}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapped ZWJ text = %q, want %q", lines, want)
	}
}

func TestTerminalUIWrapsWidthSensitiveUnicodeAtNarrowWidths(t *testing.T) {
	const prefix = "  • "
	cases := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "non-emoji ZWJ text",
			message: strings.Repeat("\u3031\u200d\u3031", 2),
			want: []string{
				prefix + "\u3031\u200d\u3031",
				prefix + "\u3031\u200d\u3031",
			},
		},
		{
			name:    "wide non-emoji ZWJ text",
			message: strings.Repeat("\u2329\u200d\u2329", 2),
			want: []string{
				prefix + "\u2329\u200d\u2329",
				prefix + "\u2329\u200d\u2329",
			},
		},
		{
			name:    "supplementary wide text",
			message: strings.Repeat("\U0001b11f", 3),
			want: []string{
				prefix + "\U0001b11f\U0001b11f",
				prefix + "\U0001b11f",
			},
		},
		{
			name:    "supplementary Kana Extended-B text",
			message: strings.Repeat("\U0001aff0", 3),
			want: []string{
				prefix + "\U0001aff0\U0001aff0",
				prefix + "\U0001aff0",
			},
		},
	}

	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			var output bytes.Buffer
			ui := terminalUI{interactive: true, color: false, width: 8}
			ui.renderInfo(&output, fixture.message)

			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			if strings.Join(lines, "|") != strings.Join(fixture.want, "|") {
				t.Fatalf("rendered lines = %q, want %q", lines, fixture.want)
			}
			for _, line := range lines {
				if terminalTextWidth(line) > ui.width {
					t.Errorf("rendered line width = %d, want at most %d: %q", terminalTextWidth(line), ui.width, line)
				}
			}
		})
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

func TestTerminalUIASCIIUsesASCIITruncationMarker(t *testing.T) {
	t.Setenv("TERM", "dumb")
	var output bytes.Buffer
	ui := terminalUI{interactive: true, ascii: true, width: 40}
	ui.renderAssistant(&output, strings.Repeat("model", 20), "reply")

	result := output.String()
	if strings.Contains(result, "…") {
		t.Fatalf("ASCII output retained a Unicode truncation marker: %q", result)
	}
	if !strings.Contains(result, "...") {
		t.Fatalf("ASCII output did not use an ASCII truncation marker: %q", result)
	}
	for _, line := range strings.Split(strings.TrimSuffix(result, "\n"), "\n") {
		if terminalTextWidth(line) > ui.width {
			t.Errorf("ASCII output line width = %d, want at most %d: %q", terminalTextWidth(line), ui.width, line)
		}
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
