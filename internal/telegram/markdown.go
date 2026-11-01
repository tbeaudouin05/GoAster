package telegram

import (
	"strings"
	"unicode/utf8"
)

var telegramHTMLTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// markdownToTelegramHTML converts code spans and fenced code blocks to the
// subset of Telegram HTML needed by assistant messages. All other text is
// escaped and preserved verbatim.
func markdownToTelegramHTML(text string) string {
	var converted strings.Builder
	converted.Grow(len(text))

	for offset := 0; offset < len(text); {
		if isLineStart(text, offset) {
			if code, end, ok := fencedCodeAt(text, offset); ok {
				converted.WriteString("<pre><code>")
				writeEscapedText(&converted, code)
				converted.WriteString("</code></pre>")
				offset = end
				continue
			}
		}

		if text[offset] == '`' {
			if code, end, ok := inlineCodeAt(text, offset); ok {
				converted.WriteString("<code>")
				writeEscapedText(&converted, code)
				converted.WriteString("</code>")
				offset = end
				continue
			}
		}

		start := offset
		offset++
		for offset < len(text) && text[offset] != '`' && text[offset-1] != '\n' {
			offset++
		}
		writeEscapedText(&converted, text[start:offset])
	}

	return converted.String()
}

func isLineStart(text string, offset int) bool {
	return offset == 0 || text[offset-1] == '\n'
}

func fencedCodeAt(text string, start int) (code string, end int, ok bool) {
	if !isLineStart(text, start) {
		return "", start, false
	}
	openingEnd := strings.IndexByte(text[start:], '\n')
	if openingEnd < 0 {
		return "", start, false
	}
	openingEnd += start
	markerLength, _, isOpening := parseFenceLine(text[start:openingEnd], 0)
	if !isOpening {
		return "", start, false
	}
	codeStart := openingEnd + 1

	for lineStart := codeStart; lineStart <= len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		if _, _, isClosing := parseFenceLine(text[lineStart:lineEnd], markerLength); isClosing {
			return text[codeStart:lineStart], lineEnd, true
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}

	return "", start, false
}

// parseFenceLine returns the marker length and whether the line can open or
// close a fenced block. The info string on an opening fence is intentionally
// ignored; a closing fence may contain only whitespace after its markers.
func parseFenceLine(line string, openingLength int) (int, string, bool) {
	line = strings.TrimSuffix(line, "\r")
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) || line[indent] != '`' {
		return 0, "", false
	}
	markerEnd := indent
	for markerEnd < len(line) && line[markerEnd] == '`' {
		markerEnd++
	}
	markerLength := markerEnd - indent
	if markerLength < 3 || markerLength < openingLength {
		return 0, "", false
	}
	rest := line[markerEnd:]
	if openingLength > 0 {
		return markerLength, rest, strings.TrimSpace(rest) == ""
	}
	if strings.ContainsRune(rest, '`') {
		return 0, "", false
	}
	return markerLength, rest, true
}

func inlineCodeAt(text string, start int) (code string, end int, ok bool) {
	markerLength := 1
	for start+markerLength < len(text) && text[start+markerLength] == '`' {
		markerLength++
	}
	if markerLength >= 3 {
		return "", start, false
	}
	marker := strings.Repeat("`", markerLength)
	for searchAt := start + markerLength; searchAt < len(text); {
		relative := strings.Index(text[searchAt:], marker)
		if relative < 0 {
			return "", start, false
		}
		closeAt := searchAt + relative
		closeEnd := closeAt + markerLength
		beforeMatches := closeAt == 0 || text[closeAt-1] != '`'
		afterMatches := closeEnd == len(text) || text[closeEnd] != '`'
		if beforeMatches && afterMatches {
			return text[start+markerLength : closeAt], closeEnd, true
		}
		searchAt = closeEnd
	}
	return "", start, false
}

func writeEscapedText(destination *strings.Builder, text string) {
	destination.WriteString(telegramHTMLTextEscaper.Replace(text))
}

// splitTelegramMarkdown keeps the existing rune-based Telegram chunks while
// closing and reopening fenced blocks when a chunk boundary falls inside one.
func splitTelegramMarkdown(text string, maxRunes int) []string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		if text == "" {
			return nil
		}
		return []string{text}
	}

	var chunks []string
	insideFence := false
	fenceLength := 0
	pendingLine := ""
	continuation := false
	remaining := []rune(text)

	for len(remaining) > maxRunes {
		cut := maxRunes
		piece := string(remaining[:cut])
		nextInside, nextFenceLength, nextPending := advanceFenceState(piece, insideFence, fenceLength, pendingLine)
		if !strings.HasSuffix(piece, "\n") {
			lineBreak := strings.LastIndexByte(piece, '\n')
			moveToLineStart := possibleFenceLinePrefix(nextPending)
			if nextInside && lineBreak >= 0 {
				lineBreakRunes := utf8.RuneCountInString(piece[:lineBreak+1])
				moveToLineStart = moveToLineStart || lineBreakRunes > maxRunes/2
			}
			if moveToLineStart && lineBreak >= 0 {
				cut = utf8.RuneCountInString(piece[:lineBreak+1])
				piece = string(remaining[:cut])
				nextInside, nextFenceLength, nextPending = advanceFenceState(piece, insideFence, fenceLength, pendingLine)
			}
		}

		var chunk strings.Builder
		if continuation {
			chunk.WriteString("```\n")
		}
		chunk.WriteString(piece)
		if nextInside {
			if !strings.HasSuffix(piece, "\n") {
				chunk.WriteByte('\n')
			}
			chunk.WriteString("```")
		}
		chunks = append(chunks, chunk.String())

		remaining = remaining[cut:]
		insideFence, fenceLength, pendingLine = nextInside, nextFenceLength, nextPending
		continuation = nextInside
	}

	var final strings.Builder
	if continuation {
		final.WriteString("```\n")
	}
	finalText := string(remaining)
	final.WriteString(finalText)
	insideFence, _ = finishFenceState(finalText, insideFence, fenceLength, pendingLine)
	if insideFence {
		if !strings.HasSuffix(finalText, "\n") {
			final.WriteByte('\n')
		}
		final.WriteString("```")
	}
	chunks = append(chunks, final.String())
	return chunks
}

func possibleFenceLinePrefix(line string) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	return indent <= 3 && strings.HasPrefix(line[indent:], "```")
}

func advanceFenceState(text string, inside bool, fenceLength int, pending string) (bool, int, string) {
	text = pending + text
	for {
		lineEnd := strings.IndexByte(text, '\n')
		if lineEnd < 0 {
			return inside, fenceLength, text
		}
		line := text[:lineEnd]
		if inside {
			if _, _, closes := parseFenceLine(line, fenceLength); closes {
				inside, fenceLength = false, 0
			}
		} else if markerLength, _, opens := parseFenceLine(line, 0); opens {
			inside, fenceLength = true, markerLength
		}
		text = text[lineEnd+1:]
	}
}

func finishFenceState(text string, inside bool, fenceLength int, pending string) (bool, int) {
	inside, fenceLength, pending = advanceFenceState(text, inside, fenceLength, pending)
	if pending == "" {
		return inside, fenceLength
	}
	if inside {
		if _, _, closes := parseFenceLine(pending, fenceLength); closes {
			return false, 0
		}
		return true, fenceLength
	}
	if markerLength, _, opens := parseFenceLine(pending, 0); opens {
		return true, markerLength
	}
	return false, 0
}
