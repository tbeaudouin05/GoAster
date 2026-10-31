package chat

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

const (
	defaultTerminalWidth = 80
	minimumTerminalWidth = 32
	maximumBoxWidth      = 100

	ansiReset       = "\x1b[0m"
	ansiYellow      = "\x1b[33m"
	ansiBorder      = "\x1b[38;5;39m"
	ansiAssistant   = "\x1b[38;5;117m"
	ansiPrompt      = "\x1b[38;5;150m"
	ansiStatus      = "\x1b[38;5;249m"
	ansiDescription = "\x1b[38;5;245m"
)

// terminalUI keeps the interactive presentation separate from the line-based
// protocol used by pipes, tests, and approval handoffs.
type terminalUI struct {
	interactive bool
	color       bool
	width       int
}

func newTerminalUI(input TerminalInput, output io.Writer) terminalUI {
	interactive := input.Interactive() && terminalWriter(output)
	width := defaultTerminalWidth
	if interactive {
		width = terminalWriterWidth(output)
	}
	return terminalUI{
		interactive: interactive,
		color:       interactive && terminalColorEnabled(),
		width:       width,
	}
}

func terminalWriter(output io.Writer) bool {
	device, ok := output.(terminalFileDescriptor)
	return ok && term.IsTerminal(int(device.Fd()))
}

func terminalWriterWidth(output io.Writer) int {
	device, ok := output.(terminalFileDescriptor)
	if !ok {
		return defaultTerminalWidth
	}
	width, _, err := term.GetSize(int(device.Fd()))
	if err != nil || width < minimumTerminalWidth {
		return defaultTerminalWidth
	}
	return width
}

func terminalColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return strings.ToLower(strings.TrimSpace(os.Getenv("TERM"))) != "dumb"
}

func (ui terminalUI) renderIntro(output io.Writer, description string) {
	if !ui.interactive {
		fmt.Fprintf(output, "GoAster — %s\n", description)
		fmt.Fprintln(output, "Type a message, then press Enter.")
		fmt.Fprintln(output, "Describe what you want, such as a new OVH server or GoAster installed on an existing one.")
		fmt.Fprintln(output, "Use /env for local secret setup and /file to inspect non-secret files.")
		return
	}

	boxWidth := ui.boxWidth()
	innerWidth := boxWidth - 4
	fmt.Fprintln(output, ui.paint(ansiBorder, "  ╭"+strings.Repeat("─", boxWidth-2)+"╮"))
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxLine("GoAster", boxWidth, innerWidth)))
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxLine(description, boxWidth, innerWidth)))
	fmt.Fprintln(output, ui.paint(ansiBorder, "  ╰"+strings.Repeat("─", boxWidth-2)+"╯"))
	fmt.Fprintln(output, "  "+ui.paint(ansiDescription, "Describe what you want in plain language."))
	fmt.Fprintln(output, "  "+ui.paint(ansiDescription, "/env  local secrets  ·  /file  inspect files  ·  Ctrl-D  quit"))
	fmt.Fprintln(output)
}

func (ui terminalUI) renderStatus(output io.Writer, model, reason string) {
	if !ui.interactive {
		fmt.Fprintf(output, "Active model: %s\n", model)
		if reason != "" {
			fmt.Fprintf(output, "Fallback reason: %s\n", reason)
		}
		return
	}

	status := "  ● Active model: " + model
	if reason != "" {
		status += "  ·  fallback active"
	}
	fmt.Fprintln(output, ui.paint(ansiStatus, status))
	if reason != "" {
		fmt.Fprintln(output, "  "+ui.paint(ansiYellow, "Fallback reason: "+reason))
	}
	fmt.Fprintln(output)
}

func (ui terminalUI) renderNotice(output io.Writer, label, message string) {
	if !ui.interactive {
		fmt.Fprintf(output, "%s: %s\n", label, message)
		return
	}
	fmt.Fprintln(output, "  "+ui.paint(ansiYellow, "• "+label+": "+message))
}

func (ui terminalUI) renderPrompt(output io.Writer) {
	if !ui.interactive {
		fmt.Fprint(output, "you> ")
		return
	}
	fmt.Fprint(output, ui.paint(ansiPrompt, "  You › "))
}

func (ui terminalUI) renderAssistant(output io.Writer, model, reply string) {
	if !ui.interactive {
		fmt.Fprintf(output, "assistant [%s]> %s\n", model, reply)
		return
	}

	label := "GoAster"
	if model != "" {
		label += " · " + model
	}
	ui.renderBubble(output, label, reply)
}

func (ui terminalUI) renderBubble(output io.Writer, label, message string) {
	boxWidth := ui.boxWidth()
	innerWidth := boxWidth - 4
	labelWidth := boxWidth - 4
	topLabel := "─ " + fitTerminalText(label, labelWidth-2)
	topFill := boxWidth - 2 - terminalTextWidth(topLabel)
	if topFill < 0 {
		topFill = 0
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, ui.paint(ansiBorder, "  ╭"+topLabel+strings.Repeat("─", topFill)+"╮"))
	for _, line := range wrapTerminalText(message, innerWidth) {
		content := "  │ " + padTerminalText(line, innerWidth) + " │"
		fmt.Fprintln(output, ui.paint(ansiAssistant, content))
	}
	fmt.Fprintln(output, ui.paint(ansiBorder, "  ╰"+strings.Repeat("─", boxWidth-2)+"╯"))
}

func (ui terminalUI) renderFinish(output io.Writer) {
	fmt.Fprintln(output)
}

func (ui terminalUI) boxWidth() int {
	width := ui.width
	if width < minimumTerminalWidth {
		width = defaultTerminalWidth
	}
	width -= 4
	if width > maximumBoxWidth {
		width = maximumBoxWidth
	}
	return width
}

func (ui terminalUI) boxLine(content string, boxWidth, innerWidth int) string {
	return "  │ " + padTerminalText(fitTerminalText(content, innerWidth), innerWidth) + " │"
}

func (ui terminalUI) paint(code, content string) string {
	if !ui.color || content == "" {
		return content
	}
	return code + content + ansiReset
}

func wrapTerminalText(message string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	normalized := strings.ReplaceAll(message, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	paragraphs := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := ""
		for _, word := range words {
			for terminalTextWidth(word) > width {
				chunk, rest := terminalTextChunk(word, width)
				if current != "" {
					lines = append(lines, current)
					current = ""
				}
				lines = append(lines, chunk)
				word = rest
			}
			if word == "" {
				continue
			}
			candidate := word
			if current != "" {
				candidate = current + " " + word
			}
			if terminalTextWidth(candidate) > width && current != "" {
				lines = append(lines, current)
				current = word
				continue
			}
			current = candidate
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func terminalTextChunk(text string, width int) (string, string) {
	runes := []rune(text)
	if len(runes) <= width {
		return text, ""
	}
	return string(runes[:width]), string(runes[width:])
}

func terminalTextWidth(text string) int {
	width := 0
	for _, character := range text {
		switch {
		case character == '\t':
			width += 4
		case character == '\n' || character == '\r':
			continue
		case utf8.RuneLen(character) == 0:
			continue
		default:
			width++
		}
	}
	return width
}

func fitTerminalText(text string, width int) string {
	if width < 1 {
		return ""
	}
	if terminalTextWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	if len(runes) > width-1 {
		runes = runes[:width-1]
	}
	return string(runes) + "…"
}

func padTerminalText(text string, width int) string {
	text = fitTerminalText(text, width)
	return text + strings.Repeat(" ", width-terminalTextWidth(text))
}
