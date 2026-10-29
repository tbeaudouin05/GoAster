package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"goaster/internal/localenv"
)

const environmentFileGuidance = `Next: choose your local KEY=VALUE file in the file picker.
Use a local file for environment values; never paste credentials or secrets into chat.
GoAster displays variable names with masked values and saves confirmed values locally.`

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
	return RunWithEnvStoreAndPicker(ctx, input, output, session, store, nativeEnvironmentFilePicker{})
}

// RunWithEnvStoreAndPicker runs chat with a separately loaded local
// environment store and an injectable local-file picker.
func RunWithEnvStoreAndPicker(ctx context.Context, input io.Reader, output io.Writer, session *Session, store *localenv.Store, picker EnvironmentFilePicker) error {
	if store == nil {
		return fmt.Errorf("environment settings store is nil")
	}
	if picker == nil {
		return fmt.Errorf("environment file picker is nil")
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
	if notice := session.consumeStartupNotice(); notice != "" {
		fmt.Fprintf(w, "Notice: %s\n", notice)
	}
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
		if message == "/env" {
			if err := runEnvironmentFileSetup(scanner, w, store, picker, ""); err != nil {
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
		if notice := session.consumeContextNotice(); notice != "" {
			fmt.Fprintf(w, "Context notice: %s\n", notice)
		}
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

func (s *Session) consumeStartupNotice() string {
	notice := s.startupNotice
	s.startupNotice = ""
	return notice
}

func (s *Session) consumeContextNotice() string {
	notice := s.contextNotice
	s.contextNotice = ""
	return notice
}

func runEnvironmentFileSetup(scanner *bufio.Scanner, output *bufio.Writer, store *localenv.Store, picker EnvironmentFilePicker, next string) error {
	fmt.Fprintln(output, environmentFileGuidance)
	if err := output.Flush(); err != nil {
		return err
	}
	path, err := picker.Pick()
	if err != nil {
		if errors.Is(err, ErrEnvironmentFilePickerCanceled) {
			fmt.Fprintln(output, "Environment setup canceled.")
			return nil
		}
		fmt.Fprintf(output, "Environment file selection failed: %v\n", err)
		return nil
	}
	if strings.TrimSpace(path) == "" {
		fmt.Fprintln(output, "Environment setup canceled.")
		return nil
	}

	values, err := localenv.LoadFile(path)
	if err != nil {
		fmt.Fprintf(output, "Environment file rejected: %v\n", err)
		return nil
	}
	return reviewEnvironmentValues(scanner, output, store, values, next)
}

func reviewEnvironmentValues(scanner *bufio.Scanner, output *bufio.Writer, store *localenv.Store, values map[string]string, next string) error {
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
	fmt.Fprintln(output, "The imported file replaces the previous saved set.")
	if next != "" {
		fmt.Fprintln(output, next)
	}
	return nil
}

func (s *Session) description() string {
	if described, ok := s.provider.(interface{ Description() string }); ok {
		return described.Description()
	}
	return "chat provider"
}
