package chat

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxTerminalInputLineBytes = 1024 * 1024

// TerminalInput reads chat and approval lines without buffering past a line,
// leaving later terminal input available to an interactive child process.
type TerminalInput struct {
	reader io.Reader
}

func NewTerminalInput(reader io.Reader) TerminalInput {
	if reader == nil {
		reader = strings.NewReader("")
	}
	return TerminalInput{reader: reader}
}

func (input TerminalInput) Read(buffer []byte) (int, error) {
	if input.reader == nil {
		return 0, errors.New("terminal input is unavailable")
	}
	return input.reader.Read(buffer)
}

// ReadLine reads through one newline at most and reports whether that newline
// was present. A final partial line is returned at EOF, matching Scanner's
// usual chat behavior while allowing approval prompts to require Enter.
func (input TerminalInput) ReadLine(maxBytes int) (string, bool, error) {
	if input.reader == nil {
		return "", false, errors.New("terminal input is unavailable")
	}
	if maxBytes < 1 {
		return "", false, errors.New("terminal input line limit must be positive")
	}
	line := make([]byte, 0, 128)
	var next [1]byte
	for {
		read, err := input.reader.Read(next[:])
		if read > 0 {
			if next[0] == '\n' {
				return strings.TrimSuffix(string(line), "\r"), true, nil
			}
			if len(line) >= maxBytes {
				return "", false, fmt.Errorf("terminal input line exceeds %d bytes", maxBytes)
			}
			line = append(line, next[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return "", false, io.EOF
				}
				return strings.TrimSuffix(string(line), "\r"), false, nil
			}
			return "", false, err
		}
		if read == 0 {
			return "", false, io.ErrNoProgress
		}
	}
}
