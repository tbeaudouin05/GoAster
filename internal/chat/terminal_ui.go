package chat

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

const (
	defaultTerminalWidth = 80
	minimumTerminalWidth = 32
	maximumBoxWidth      = 100
	terminalTabStop      = 4

	ansiReset       = "\x1b[0m"
	ansiYellow      = "\x1b[33m"
	ansiRed         = "\x1b[31m"
	ansiGreen       = "\x1b[32m"
	ansiBorder      = "\x1b[38;5;39m"
	ansiAssistant   = "\x1b[38;5;117m"
	ansiPrompt      = "\x1b[38;5;150m"
	ansiStatus      = "\x1b[38;5;249m"
	ansiDescription = "\x1b[38;5;245m"

	terminalEnvHelp    = "- /env: securely import environment variables."
	terminalGetEnvHelp = "- /get-env: edit the current env file or get a blank template."
	terminalFileHelp   = "- /file: inspect non-secret files."
)

// terminalUI keeps the interactive presentation separate from the line-based
// protocol used by pipes, tests, and approval handoffs.
type terminalUI struct {
	interactive bool
	color       bool
	ascii       bool
	width       int
	output      io.Writer
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
		ascii:       interactive && terminalIsDumb(),
		width:       width,
		output:      output,
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
	if err != nil || width < 1 {
		return defaultTerminalWidth
	}
	return width
}

func terminalColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return !terminalIsDumb()
}

func terminalIsDumb() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb")
}

func (ui terminalUI) renderWidth(output io.Writer) int {
	if !ui.interactive {
		return defaultTerminalWidth
	}
	source := ui.output
	if source == nil {
		source = output
	}
	if terminalWriter(source) {
		return terminalWriterWidth(source)
	}
	if ui.width > 0 {
		return ui.width
	}
	return defaultTerminalWidth
}

func (ui terminalUI) renderIntro(output io.Writer, description string) {
	if !ui.interactive {
		fmt.Fprintf(output, "GoAster — %s\n", description)
		fmt.Fprintln(output, "Type a message, then press Enter.")
		ui.renderIntroHelp(output, defaultTerminalWidth)
		return
	}

	width := ui.renderWidth(output)
	boxWidth := terminalBoxWidth(width)
	if boxWidth == 0 {
		separator := " — "
		if ui.ascii {
			separator = " - "
		}
		ui.renderWrapped(output, "  ", "GoAster"+separator+description, width, ansiBorder)
		ui.renderIntroHelp(output, width)
		fmt.Fprintln(output)
		return
	}

	innerWidth := boxWidth - 4
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxTop("", boxWidth)))
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxLine("GoAster", boxWidth, innerWidth)))
	for _, line := range wrapTerminalText(description, innerWidth) {
		fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxLine(line, boxWidth, innerWidth)))
	}
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxBottom(boxWidth)))
	ui.renderIntroHelp(output, width)
	fmt.Fprintln(output)
}

func (ui terminalUI) renderIntroHelp(output io.Writer, width int) {
	for _, line := range [...]string{terminalEnvHelp, terminalGetEnvHelp, terminalFileHelp} {
		ui.renderWrapped(output, "  ", line, width, ansiDescription)
	}
}

func (ui terminalUI) renderStatus(output io.Writer, model, reason string) {
	if !ui.interactive {
		fmt.Fprintf(output, "Active model: %s\n", model)
		if reason != "" {
			fmt.Fprintf(output, "Fallback reason: %s\n", reason)
		}
		return
	}

	status := ui.statusMarker() + " Active model: " + model
	if reason != "" {
		separator := "  ·  "
		if ui.ascii {
			separator = "  -  "
		}
		status += separator + "fallback active"
	}
	ui.renderWrapped(output, "  ", status, ui.renderWidth(output), ansiStatus)
	if reason != "" {
		ui.renderWrapped(output, "  ", "Fallback reason: "+reason, ui.renderWidth(output), ansiYellow)
	}
	fmt.Fprintln(output)
}

func (ui terminalUI) renderNotice(output io.Writer, label, message string) {
	if !ui.interactive {
		fmt.Fprintf(output, "%s: %s\n", label, message)
		return
	}
	ui.renderSemantic(output, terminalWarning, label+": "+message)
}

func (ui terminalUI) renderPrompt(output io.Writer) {
	if !ui.interactive {
		fmt.Fprint(output, "you> ")
		return
	}
	prompt := "  You › "
	if ui.ascii {
		prompt = "  You > "
	}
	fmt.Fprint(output, ui.paint(ansiPrompt, prompt))
}

func (ui terminalUI) renderWorking(output io.Writer) {
	if !ui.interactive {
		return
	}
	ui.renderWrapped(output, "  ", "Working...", ui.renderWidth(output), ansiStatus)
}

func (ui terminalUI) renderToolCall(output io.Writer, name string) {
	if !ui.interactive {
		return
	}
	ui.renderWrapped(output, "  "+ui.statusMarker()+" ", terminalToolCallStatus(name), ui.renderWidth(output), ansiStatus)
}

func terminalToolCallStatus(name string) string {
	name = strings.TrimSpace(name)
	if !validTerminalToolName(name) {
		name = "tool"
	}
	if runes := []rune(name); len(runes) > 40 {
		name = string(runes[:37]) + "..."
	}

	activity := "running"
	switch name {
	case "web_search":
		activity = "searching the web"
	case "web_fetch":
		activity = "fetching a public page"
	case "shell_execute":
		activity = "running a command"
	case "cron":
		activity = "managing scheduled jobs"
	case "remote_login":
		activity = "starting remote login approval"
	}
	return "Tool " + name + ": " + activity
}

func validTerminalToolName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (ui terminalUI) renderRequestError(output io.Writer) {
	if !ui.interactive {
		return
	}
	ui.renderError(output, "Request failed. Please try again.")
}

func (ui terminalUI) renderAssistant(output io.Writer, model, reply string) {
	if !ui.interactive {
		fmt.Fprintf(output, "assistant [%s]> %s\n", model, reply)
		return
	}

	label := "GoAster"
	if model != "" {
		separator := " · "
		if ui.ascii {
			separator = " - "
		}
		label += separator + model
	}
	ui.renderBubble(output, label, reply)
}

func (ui terminalUI) renderBubble(output io.Writer, label, message string) {
	width := ui.renderWidth(output)
	boxWidth := terminalBoxWidth(width)
	if boxWidth == 0 {
		fmt.Fprintln(output)
		ui.renderWrapped(output, "  ", label+":", width, ansiAssistant)
		ui.renderWrapped(output, "  ", message, width, ansiAssistant)
		return
	}

	innerWidth := boxWidth - 4
	labelWidth := boxWidth - 4
	topMarker := "─"
	if ui.ascii {
		topMarker = "-"
	}
	topLabel := topMarker + " " + ui.fitTerminalText(label, labelWidth-2)

	fmt.Fprintln(output)
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxTop(topLabel, boxWidth)))
	for _, line := range wrapTerminalText(message, innerWidth) {
		content := ui.boxLine(line, boxWidth, innerWidth)
		fmt.Fprintln(output, ui.paint(ansiAssistant, content))
	}
	fmt.Fprintln(output, ui.paint(ansiBorder, ui.boxBottom(boxWidth)))
}

func (ui terminalUI) renderFinish(output io.Writer) {
	fmt.Fprintln(output)
}

func (ui terminalUI) boxWidth() int {
	width := ui.width
	if width < 1 {
		width = defaultTerminalWidth
	}
	return terminalBoxWidth(width)
}

func terminalBoxWidth(width int) int {
	if width < minimumTerminalWidth {
		return 0
	}
	width -= 4
	if width > maximumBoxWidth {
		width = maximumBoxWidth
	}
	return width
}

func (ui terminalUI) boxTop(label string, boxWidth int) string {
	left, right, horizontal := "╭", "╮", "─"
	if ui.ascii {
		left, right, horizontal = "+", "+", "-"
	}
	label = strings.ReplaceAll(sanitizeTerminalText(label), "\n", " ")
	label = strings.ReplaceAll(label, "\r", " ")
	label = expandTerminalTabs(label)
	if label == "" {
		return "  " + left + strings.Repeat(horizontal, boxWidth-2) + right
	}
	fill := boxWidth - 2 - terminalTextWidth(label)
	if fill < 0 {
		fill = 0
	}
	return "  " + left + label + strings.Repeat(horizontal, fill) + right
}

func (ui terminalUI) boxBottom(boxWidth int) string {
	left, right, horizontal := "╰", "╯", "─"
	if ui.ascii {
		left, right, horizontal = "+", "+", "-"
	}
	return "  " + left + strings.Repeat(horizontal, boxWidth-2) + right
}

func (ui terminalUI) boxLine(content string, boxWidth, innerWidth int) string {
	left, right := "│", "│"
	if ui.ascii {
		left, right = "|", "|"
	}
	return "  " + left + " " + ui.padTerminalText(content, innerWidth) + " " + right
}

func (ui terminalUI) paint(code, content string) string {
	content = sanitizeTerminalText(content)
	if !ui.color || ui.ascii || terminalIsDumb() || content == "" {
		return content
	}
	return code + content + ansiReset
}

type terminalSemantic uint8

const (
	terminalInfo terminalSemantic = iota
	terminalWarning
	terminalError
	terminalConfirmation
	terminalOutcome
)

func (ui terminalUI) renderInfo(output io.Writer, message string) {
	if !ui.interactive {
		fmt.Fprintln(output, message)
		return
	}
	ui.renderSemantic(output, terminalInfo, message)
}

func (ui terminalUI) renderWarning(output io.Writer, message string) {
	if !ui.interactive {
		fmt.Fprintln(output, message)
		return
	}
	ui.renderSemantic(output, terminalWarning, message)
}

func (ui terminalUI) renderError(output io.Writer, message string) {
	if !ui.interactive {
		fmt.Fprintln(output, message)
		return
	}
	ui.renderSemantic(output, terminalError, message)
}

func (ui terminalUI) renderOutcome(output io.Writer, message string) {
	if !ui.interactive {
		fmt.Fprintln(output, message)
		return
	}
	ui.renderSemantic(output, terminalOutcome, message)
}

func (ui terminalUI) renderConfirmation(output io.Writer, message string) {
	if !ui.interactive {
		fmt.Fprint(output, message)
		return
	}
	prefix := "  " + ui.semanticMarker(terminalConfirmation) + " "
	available := ui.renderWidth(output) - terminalTextWidth(prefix) - 1
	if available < 1 {
		available = 1
	}
	lines := wrapTerminalText(message, available)
	for index, line := range lines {
		if index == len(lines)-1 {
			fmt.Fprint(output, ui.paint(ansiYellow, prefix+line+" "))
			continue
		}
		if line == "" {
			fmt.Fprintln(output)
			continue
		}
		fmt.Fprintln(output, ui.paint(ansiYellow, prefix+line))
	}
}

func (ui terminalUI) renderEnvironmentReview(output io.Writer, keys []string) {
	if !ui.interactive {
		fmt.Fprintln(output, "Review environment values (values are masked):")
		for _, key := range keys {
			fmt.Fprintf(output, "  %s=********\n", key)
		}
		return
	}
	ui.renderInfo(output, "Review environment values (values are masked):")
	for _, key := range keys {
		ui.renderWrapped(output, "    ", key+"=********", ui.renderWidth(output), ansiDescription)
	}
}

func (ui terminalUI) renderSemantic(output io.Writer, semantic terminalSemantic, message string) {
	prefix := "  " + ui.semanticMarker(semantic) + " "
	code := ansiYellow
	switch semantic {
	case terminalError:
		code = ansiRed
	case terminalOutcome:
		code = ansiGreen
	}
	ui.renderWrapped(output, prefix, message, ui.renderWidth(output), code)
}

func (ui terminalUI) semanticMarker(semantic terminalSemantic) string {
	if ui.ascii {
		switch semantic {
		case terminalConfirmation:
			return "?"
		case terminalOutcome:
			return "OK"
		default:
			return "!"
		}
	}
	switch semantic {
	case terminalInfo:
		return "•"
	case terminalWarning:
		return "!"
	case terminalError:
		return "×"
	case terminalConfirmation:
		return "?"
	case terminalOutcome:
		return "✓"
	default:
		return "•"
	}
}

func (ui terminalUI) statusMarker() string {
	if ui.ascii {
		return "*"
	}
	return "●"
}

func (ui terminalUI) renderWrapped(output io.Writer, prefix, message string, width int, code string) {
	message = sanitizeTerminalText(message)
	prefix = sanitizeTerminalText(prefix)
	available := width - terminalTextWidth(prefix)
	if available < 1 {
		available = 1
	}
	for _, line := range wrapTerminalText(message, available) {
		if line == "" {
			fmt.Fprintln(output)
			continue
		}
		fmt.Fprintln(output, ui.paint(code, prefix+line))
	}
}

func wrapTerminalText(message string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	normalized := sanitizeTerminalText(message)
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = expandTerminalTabs(normalized)
	paragraphs := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		lines = append(lines, wrapTerminalLine(paragraph, width)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func wrapTerminalLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if terminalTextWidth(line) <= width {
		return []string{line}
	}

	lines := make([]string, 0, 2)
	current := ""
	currentWidth := 0
	for _, token := range terminalTextTokens(line) {
		tokenWidth := terminalTextWidth(token)
		if tokenWidth == 0 {
			current += token
			continue
		}
		if terminalTokenIsWhitespace(token) {
			for token != "" {
				remaining := width - currentWidth
				if remaining < 1 {
					if current != "" {
						lines = append(lines, trimWrappingSpace(current))
					}
					current = ""
					currentWidth = 0
					break
				}
				chunk, rest := terminalTextChunk(token, remaining)
				if chunk == "" {
					break
				}
				current += chunk
				currentWidth += terminalTextWidth(chunk)
				token = rest
				if token != "" && currentWidth == width {
					lines = append(lines, current)
					current = ""
					currentWidth = 0
				}
			}
			continue
		}
		if tokenWidth > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
				currentWidth = 0
			}
			for token != "" {
				chunk, rest := terminalTextChunk(token, width)
				if chunk == "" {
					break
				}
				token = rest
				if token == "" {
					current = chunk
					currentWidth = terminalTextWidth(chunk)
					break
				}
				lines = append(lines, chunk)
			}
			continue
		}
		if currentWidth > 0 && currentWidth+tokenWidth > width {
			lines = append(lines, trimWrappingSpace(current))
			current = ""
			currentWidth = 0
		}
		current += token
		currentWidth += tokenWidth
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

func trimWrappingSpace(text string) string {
	trimmed := strings.TrimRight(text, " ")
	if trimmed == "" && text != "" {
		return text
	}
	return trimmed
}

func terminalTextTokens(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(runes))
	start := 0
	whitespace := unicode.IsSpace(runes[0])
	for index := 1; index < len(runes); index++ {
		currentWhitespace := unicode.IsSpace(runes[index])
		if currentWhitespace != whitespace {
			tokens = append(tokens, string(runes[start:index]))
			start = index
			whitespace = currentWhitespace
		}
	}
	return append(tokens, string(runes[start:]))
}

func terminalTokenIsWhitespace(text string) bool {
	for _, character := range text {
		if !unicode.IsSpace(character) {
			return false
		}
	}
	return text != ""
}

func terminalTextChunk(text string, width int) (string, string) {
	if width < 1 || text == "" {
		return "", text
	}
	units := terminalTextUnits(text)
	used := 0
	cut := 0
	for _, unit := range units {
		if unit.width > width-used {
			if cut == 0 {
				return "?", text[len(unit.text):]
			}
			break
		}
		used += unit.width
		cut += len(unit.text)
		if used == width {
			break
		}
	}
	if cut == 0 {
		return "", text
	}
	return text[:cut], text[cut:]
}

func terminalTextWidth(text string) int {
	text = sanitizeTerminalText(text)
	maxWidth := 0
	for _, line := range strings.Split(text, "\n") {
		lineWidth := terminalLineWidth(line)
		if lineWidth > maxWidth {
			maxWidth = lineWidth
		}
	}
	return maxWidth
}

func terminalLineWidth(text string) int {
	text = expandTerminalTabs(text)
	width := 0
	for _, unit := range terminalTextUnits(text) {
		width += unit.width
	}
	return width
}

func fitTerminalText(text string, width int) string {
	return fitTerminalTextWithMarker(text, width, "…")
}

func fitTerminalTextWithMarker(text string, width int, marker string) string {
	if width < 1 {
		return ""
	}
	text = sanitizeTerminalText(text)
	if terminalTextWidth(text) <= width {
		return text
	}
	markerWidth := terminalTextWidth(marker)
	if markerWidth > width {
		return "?"
	}
	if markerWidth == width {
		return marker
	}
	chunk, _ := terminalTextChunk(text, width-markerWidth)
	return chunk + marker
}

func padTerminalText(text string, width int) string {
	return padTerminalTextWithMarker(text, width, "…")
}

func padTerminalTextWithMarker(text string, width int, marker string) string {
	text = fitTerminalTextWithMarker(text, width, marker)
	padding := width - terminalTextWidth(text)
	if padding < 0 {
		padding = 0
	}
	return text + strings.Repeat(" ", padding)
}

func (ui terminalUI) fitTerminalText(text string, width int) string {
	marker := "…"
	if ui.ascii {
		marker = "..."
	}
	return fitTerminalTextWithMarker(text, width, marker)
}

func (ui terminalUI) padTerminalText(text string, width int) string {
	marker := "…"
	if ui.ascii {
		marker = "..."
	}
	return padTerminalTextWithMarker(text, width, marker)
}

type terminalTextUnit struct {
	text  string
	width int
}

func terminalTextUnits(text string) []terminalTextUnit {
	runes := []rune(text)
	units := make([]terminalTextUnit, 0, len(runes))
	for index := 0; index < len(runes); {
		character := runes[index]
		if character == '\n' || character == '\r' {
			units = append(units, terminalTextUnit{text: string(character)})
			index++
			continue
		}
		if character == '\u200d' {
			if len(units) == 0 {
				units = append(units, terminalTextUnit{text: string(character)})
			} else {
				units[len(units)-1].text += string(character)
			}
			index++
			continue
		}
		if terminalMarkRune(character) {
			if len(units) == 0 {
				units = append(units, terminalTextUnit{text: string(character)})
			} else {
				last := len(units) - 1
				units[last] = terminalTextUnitWithMark(units[last], character)
			}
			index++
			continue
		}
		unit := terminalTextUnit{text: string(character), width: terminalRuneWidth(character)}
		index++
		if terminalRegionalIndicator(character) && index < len(runes) && terminalRegionalIndicator(runes[index]) {
			unit.text += string(runes[index])
			unit.width = 2
			index++
		}
		for index < len(runes) && terminalMarkRune(runes[index]) {
			unit = terminalTextUnitWithMark(unit, runes[index])
			index++
		}
		for index+1 < len(runes) && runes[index] == '\u200d' && terminalEmojiZWJRune(character) && terminalEmojiZWJRune(runes[index+1]) {
			unit.text += string(runes[index]) + string(runes[index+1])
			index += 2
			unit.width = 2
			for index < len(runes) && terminalMarkRune(runes[index]) {
				unit = terminalTextUnitWithMark(unit, runes[index])
				index++
			}
		}
		units = append(units, unit)
	}
	return units
}

func terminalTextUnitWithMark(unit terminalTextUnit, character rune) terminalTextUnit {
	if character == '\ufe0f' {
		base, _ := utf8.DecodeRuneInString(unit.text)
		if terminalEmojiRune(base) && unit.width < 2 {
			unit.width = 2
		}
	}
	if character == '\u20e3' && strings.ContainsRune(unit.text, '\ufe0f') && unit.width < 2 {
		unit.width = 2
	}
	unit.text += string(character)
	return unit
}

func terminalCombiningRune(character rune) bool {
	return character == '\u200d' || character == '\ufe0e' || character == '\ufe0f' ||
		character >= '\U000e0100' && character <= '\U000e01ef' ||
		unicode.Is(unicode.Mn, character) || unicode.Is(unicode.Me, character) ||
		character >= '\U0001f3fb' && character <= '\U0001f3ff'
}

func terminalMarkRune(character rune) bool {
	return character != '\u200d' && terminalCombiningRune(character)
}

func terminalRegionalIndicator(character rune) bool {
	return character >= '\U0001f1e6' && character <= '\U0001f1ff'
}

func terminalEmojiRune(character rune) bool {
	switch {
	case character >= '\U0001f000' && character <= '\U0001faff':
		return true
	case character >= '\u2600' && character <= '\u27bf':
		return true
	case character == '\u00a9' || character == '\u00ae' || character == '\u203c' || character == '\u2049':
		return true
	case character >= '\u2122' && character <= '\u2139':
		return true
	case character >= '\u2194' && character <= '\u21ff':
		return true
	case character >= '\u2300' && character <= '\u23ff':
		return true
	case character >= '\u25aa' && character <= '\u25ff':
		return true
	case character >= '\u2934' && character <= '\u2935':
		return true
	case character >= '\u2b00' && character <= '\u2bff':
		return true
	case character == '\u3030' || character == '\u303d':
		return true
	case character == '\u3297' || character == '\u3299':
		return true
	default:
		return false
	}
}

func terminalEmojiZWJRune(character rune) bool {
	return terminalEmojiRune(character) &&
		(character >= '\U0001f000' && character <= '\U0001faff' || !terminalWideRune(character))
}

func terminalRuneWidth(character rune) int {
	switch {
	case character == '\n' || character == '\r' || character == '\t':
		return 0
	case character == '\u200d' || terminalCombiningRune(character):
		return 0
	case character < 0x20 || character == 0x7f || character >= 0x80 && character <= 0x9f:
		return 0
	case unicode.Is(unicode.Cf, character):
		return 0
	case terminalWideRune(character):
		return 2
	case utf8.RuneLen(character) == 0:
		return 0
	default:
		return 1
	}
}

func terminalWideRune(character rune) bool {
	return character >= 0x1100 && (character <= 0x115f || character == 0x2329 || character == 0x232a ||
		character >= 0x2e80 && character <= 0xa4cf || character >= 0xac00 && character <= 0xd7a3 ||
		character >= 0xf900 && character <= 0xfaff || character >= 0xfe10 && character <= 0xfe19 ||
		character >= 0xfe30 && character <= 0xfe6f || character >= 0xff00 && character <= 0xff60 ||
		character >= 0xffe0 && character <= 0xffe6 || character >= 0x1aff0 && character <= 0x1afff ||
		character >= 0x1b000 && character <= 0x1b122 ||
		character >= 0x1f000 && character <= 0x1faff ||
		character >= 0x20000 && character <= 0x3fffd)
}

func expandTerminalTabs(text string) string {
	var expanded strings.Builder
	column := 0
	for _, unit := range terminalTextUnits(text) {
		switch unit.text {
		case "\n", "\r":
			expanded.WriteString(unit.text)
			column = 0
		case "\t":
			spaces := terminalTabStop - column%terminalTabStop
			expanded.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		default:
			expanded.WriteString(unit.text)
			column += unit.width
		}
	}
	return expanded.String()
}

func sanitizeTerminalText(text string) string {
	var sanitized strings.Builder
	for index := 0; index < len(text); {
		character := text[index]
		if character == 0x9c {
			index++
			continue
		}
		if character == 0x1b || character == 0x90 || character == 0x98 || character == 0x9b || character == 0x9d || character == 0x9e || character == 0x9f {
			index = skipTerminalControlSequence(text, index)
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(text[index:])
		if size == 0 {
			break
		}
		if runeValue == utf8.RuneError && size == 1 {
			index += size
			continue
		}
		if runeValue == '\n' || runeValue == '\r' || runeValue == '\t' ||
			(runeValue >= 0x20 && runeValue != 0x7f && !(runeValue >= 0x80 && runeValue <= 0x9f) &&
				(!unicode.Is(unicode.Cf, runeValue) || terminalCombiningRune(runeValue))) {
			sanitized.WriteRune(runeValue)
		}
		index += size
	}
	return sanitized.String()
}

func skipTerminalControlSequence(text string, index int) int {
	if index >= len(text) {
		return index
	}
	if text[index] == 0x9b {
		index++
		for index < len(text) {
			character := text[index]
			index++
			if character >= 0x40 && character <= 0x7e {
				break
			}
		}
		return index
	}
	if text[index] == 0x9d || text[index] == 0x90 || text[index] == 0x98 || text[index] == 0x9e || text[index] == 0x9f {
		index++
		for index < len(text) {
			if text[index] == '\a' {
				return index + 1
			}
			if text[index] == 0x9c {
				return index + 1
			}
			if text[index] == 0x1b && index+1 < len(text) && text[index+1] == '\\' {
				return index + 2
			}
			index++
		}
		return index
	}

	index++
	if index >= len(text) {
		return index
	}
	if text[index] == '[' {
		index++
		for index < len(text) {
			character := text[index]
			index++
			if character >= 0x40 && character <= 0x7e {
				break
			}
		}
		return index
	}
	if text[index] == ']' || text[index] == 'P' || text[index] == 'X' || text[index] == '^' || text[index] == '_' {
		index++
		for index < len(text) {
			if text[index] == '\a' {
				return index + 1
			}
			if text[index] == 0x9c {
				return index + 1
			}
			if text[index] == 0x1b && index+1 < len(text) && text[index+1] == '\\' {
				return index + 2
			}
			index++
		}
		return index
	}
	return index + 1
}
