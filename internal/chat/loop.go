package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"goaster/internal/localenv"
)

// Run accepts one message per line and ends the session at EOF.
func Run(ctx context.Context, input io.Reader, output io.Writer, session *Session) error {
	store, err := localenv.NewStore()
	if err != nil {
		return err
	}
	if err := store.Load(); err != nil {
		return err
	}
	return RunWithEnvStore(ctx, input, output, session, store)
}

// RunWithEnvStore runs chat with a separately loaded local environment store.
func RunWithEnvStore(ctx context.Context, input io.Reader, output io.Writer, session *Session, store *localenv.Store) error {
	if store == nil {
		return fmt.Errorf("environment settings store is nil")
	}
	w := bufio.NewWriter(output)
	status := func() {
		fmt.Fprintf(w, "Active model: %s\n", session.active)
		if session.reason != "" {
			fmt.Fprintf(w, "Fallback reason: %s\n", session.reason)
		}
	}
	fmt.Fprintf(w, "GoAster — %s\n", session.description())
	fmt.Fprintln(w, "Type a message, then press Enter.")
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
		if message == "" {
			continue
		}
		if path, isEnvironmentCommand := environmentFilePath(message); isEnvironmentCommand {
			if path == "" {
				fmt.Fprintln(w, "Usage: /env <local-file-path>")
				if err := w.Flush(); err != nil {
					return err
				}
				continue
			}
			if err := runEnvironmentFileSetup(path, scanner, w, store); err != nil {
				if flushErr := w.Flush(); flushErr != nil {
					return flushErr
				}
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
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

func runEnvironmentFileSetup(path string, scanner *bufio.Scanner, output *bufio.Writer, store *localenv.Store) error {
	values, err := localenv.LoadFile(path)
	if err != nil {
		fmt.Fprintf(output, "Environment file rejected: %v\n", err)
		return nil
	}
	return reviewEnvironmentValues(scanner, output, store, values)
}

func environmentFilePath(message string) (string, bool) {
	if message == "/env" {
		return "", true
	}
	if !strings.HasPrefix(message, "/env") || len(message) == len("/env") {
		return "", false
	}
	separator := message[len("/env")]
	if separator != ' ' && separator != '\t' {
		return "", false
	}
	return strings.TrimSpace(message[len("/env"):]), true
}

func reviewEnvironmentValues(scanner *bufio.Scanner, output *bufio.Writer, store *localenv.Store, values map[string]string) error {
	fmt.Fprintln(output, "Review environment values (values are masked):")
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(output, "  %s=********\n", key)
	}
	fmt.Fprint(output, "Save these values? [y/N] ")
	if err := output.Flush(); err != nil {
		return err
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read environment confirmation: %w", err)
		}
		fmt.Fprintln(output, "\nEnvironment setup canceled.")
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
		fmt.Fprintln(output, "Environment setup canceled.")
		return nil
	}
	if err := store.Save(values); err != nil {
		return err
	}
	fmt.Fprintln(output, "Environment values saved locally.")
	return nil
}

func (s *Session) description() string {
	if described, ok := s.provider.(interface{ Description() string }); ok {
		return described.Description()
	}
	return "chat provider"
}
