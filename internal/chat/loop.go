package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

// Run accepts one message per line. EOF and /quit or /exit end the session.
func Run(ctx context.Context, input io.Reader, output io.Writer, session *Session) error {
	w := bufio.NewWriter(output)
	status := func() {
		fmt.Fprintf(w, "Active model: %s\n", session.active)
		if session.reason != "" {
			fmt.Fprintf(w, "Fallback reason: %s\n", session.reason)
		}
	}
	fmt.Fprintln(w, "GoAster — local echo provider (offline)")
	fmt.Fprintln(w, "Type a message, /history to view history, /clear to clear history, or /quit to exit.")
	status()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(w, "you> ")
		if err := w.Flush(); err != nil {
			return err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read message: %w", err)
			}
			fmt.Fprintln(w)
			return w.Flush()
		}
		message := strings.TrimSpace(scanner.Text())
		if message == "/quit" || message == "/exit" {
			fmt.Fprintln(w, "Goodbye.")
			return w.Flush()
		}
		if message == "" {
			continue
		}
		switch message {
		case "/history":
			history := session.History()
			if len(history) == 0 {
				fmt.Fprintln(w, "History is empty.")
			}
			for i, entry := range history {
				fmt.Fprintf(w, "%d. %s: %s\n", i+1, entry.Role, entry.Content)
			}
			continue
		case "/clear":
			session.ClearHistory()
			fmt.Fprintln(w, "History cleared.")
			continue
		}
		previousModel := session.active
		reply, err := session.Reply(ctx, message)
		if session.active != previousModel {
			status()
		}
		if err != nil {
			if flushErr := w.Flush(); flushErr != nil {
				return flushErr
			}
			return err
		}
		fmt.Fprintf(w, "assistant [%s]> %s\n", session.active, reply)
	}
}
