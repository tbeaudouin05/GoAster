package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goaster/internal/attachment"
	"goaster/internal/localenv"
)

const environmentFileGuidance = `Next: choose your local KEY=VALUE file in the file picker.
Check the relevant service docs or dashboard for each variable's purpose and where to find its value; ask me to explain any variable.
Enter secret values only through this local environment-file flow; never paste them into chat.
GoAster masks values and saves confirmed settings locally.`

const ovhOnboardingStart = `OVH setup has started.

I will first check whether GoAster already has a server connection saved on this computer. If it does, I will check that same server. I will not create a second server.

You do not need to understand SSH, API calls, or service names. I will tell you one simple next step at a time.

If a server is already found, I will say one of these:
- “The server is ready. Please approve the OpenAI sign-in in the terminal.”
- “The server is ready, but Telegram still needs setup. Do not test Telegram yet.”
- “Setup is complete. Please send one normal message to the Telegram bot and check that it replies.”

If no server is saved, I will guide you through creating one. I will ask before anything that could cost money or change your OVH account. Starting this screen does not create or change anything.`

const environmentFileCommand = "/env-file"
const fileCommand = "/file"
const uploadCommand = "/upload"
const fileInspectionMessage = "Please inspect the attached file and explain what it shows, what I should do next, and any risks. Do not ask for, transcribe, or reveal secrets found in the file."

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
	return RunWithEnvStoreAndPicker(ctx, input, output, session, store, nativeLocalFilePicker{})
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
		model, reason := session.modelStatus()
		fmt.Fprintf(w, "Active model: %s\n", model)
		if reason != "" {
			fmt.Fprintf(w, "Fallback reason: %s\n", reason)
		}
	}
	fmt.Fprintf(w, "GoAster — %s\n", session.description())
	fmt.Fprintln(w, "Type a message, then press Enter.")
	fmt.Fprintln(w, "Describe what you want, such as a new OVH server or GoAster installed on an existing one.")
	fmt.Fprintln(w, "Use /env for local secret setup and /file to inspect non-secret files.")
	status()
	if notice := session.consumeStartupNotice(); notice != "" {
		fmt.Fprintf(w, "Notice: %s\n", notice)
	}
	terminalInput, ok := input.(TerminalInput)
	if !ok {
		terminalInput = NewTerminalInput(input)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(w, "you> ")
		if err := w.Flush(); err != nil {
			return err
		}
		line, _, readErr := terminalInput.ReadLine(maxTerminalInputLineBytes)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("read message: %w", readErr)
			}
			fmt.Fprintln(w)
			return w.Flush()
		}
		message := strings.TrimSpace(line)
		if message == "" {
			continue
		}
		if message == "/env" {
			if err := runEnvironmentFileSetup(terminalInput, w, store, picker, "", ""); err != nil {
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
		if isEnvironmentFileCommand(message) {
			path, ok := environmentFileCommandPath(message)
			if !ok {
				return fmt.Errorf("expected exactly one non-empty file path")
			}
			if err := runEnvironmentFileSetup(terminalInput, w, store, picker, path, ""); err != nil {
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
		if message == "/ovh" {
			fmt.Fprintln(w, ovhOnboardingStart)
			if err := w.Flush(); err != nil {
				return err
			}
		}
		userMessage := message
		var attachments []attachment.Metadata
		var attachmentDirectory string
		if message == fileCommand || message == uploadCommand {
			fmt.Fprintln(w, "Choose a local file to inspect.")
			if err := w.Flush(); err != nil {
				return err
			}
			filePicker, ok := picker.(LocalFilePicker)
			if !ok {
				fmt.Fprintln(w, "File picker unavailable.")
				if err := w.Flush(); err != nil {
					return err
				}
				continue
			}
			selectedPath, err := filePicker.PickFile()
			if errors.Is(err, ErrEnvironmentFilePickerCanceled) || err == nil && strings.TrimSpace(selectedPath) == "" {
				fmt.Fprintln(w, "File inspection canceled.")
				if err := w.Flush(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				if errors.Is(err, errEnvironmentFilePickerUnavailable) {
					fmt.Fprintln(w, "File picker unavailable.")
				} else {
					fmt.Fprintln(w, "File selection failed.")
				}
				if err := w.Flush(); err != nil {
					return err
				}
				continue
			}
			metadata, directory, err := prepareSelectedFile(selectedPath)
			if err != nil {
				fmt.Fprintln(w, "File rejected: choose a regular file up to 20 MiB. Supported images include PNG, JPEG, GIF, and WebP.")
				if flushErr := w.Flush(); flushErr != nil {
					return flushErr
				}
				continue
			}
			userMessage = fileInspectionMessage
			attachments = []attachment.Metadata{metadata}
			attachmentDirectory = directory
		}
		previousModel, _ := session.modelStatus()
		var reply string
		var err error
		if len(attachments) == 0 {
			reply, err = session.Reply(ctx, userMessage)
		} else {
			reply, err = session.ReplyWithAttachments(ctx, userMessage, attachments)
			if cleanupErr := attachment.CleanupTemporaryDir(attachmentDirectory); cleanupErr != nil {
				fmt.Fprintln(w, "Temporary attachment cleanup failed.")
			}
		}
		if notice := session.consumeContextNotice(); notice != "" {
			fmt.Fprintf(w, "Context notice: %s\n", notice)
		}
		model, _ := session.modelStatus()
		if model != previousModel {
			status()
		}
		if err != nil {
			if flushErr := w.Flush(); flushErr != nil {
				return flushErr
			}
			return err
		}
		fmt.Fprintf(w, "assistant [%s]> %s\n", model, reply)
	}
}

func prepareSelectedFile(pathName string) (attachment.Metadata, string, error) {
	file, err := os.Open(pathName)
	if err != nil {
		return attachment.Metadata{}, "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > attachment.MaxBytes {
		return attachment.Metadata{}, "", fmt.Errorf("selected file is not a readable regular file within the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, attachment.MaxBytes+1))
	if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > attachment.MaxBytes {
		return attachment.Metadata{}, "", fmt.Errorf("selected file changed or exceeds the size limit")
	}
	inspection, err := attachment.InspectBytes(data)
	if err != nil {
		return attachment.Metadata{}, "", err
	}

	filename := attachment.SanitizeFilename(filepath.Base(pathName))
	temporaryRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return attachment.Metadata{}, "", fmt.Errorf("locate temporary attachment directory")
	}
	directory, err := os.MkdirTemp(temporaryRoot, attachment.AgentTemporaryDirectoryPrefix)
	if err != nil {
		return attachment.Metadata{}, "", fmt.Errorf("create temporary attachment directory")
	}
	pathName = filepath.Join(directory, filename)
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		attachment.CleanupTemporaryDir(directory)
		return attachment.Metadata{}, "", fmt.Errorf("store temporary attachment")
	}
	kind := "document"
	if inspection.MIMEType != "" {
		kind = "image"
	}
	metadata := attachment.Metadata{
		Type:     kind,
		Filename: filename,
		Path:     pathName,
		MIMEType: inspection.MIMEType,
		Size:     inspection.Size,
	}
	if err := metadata.Validate(); err != nil {
		attachment.CleanupTemporaryDir(directory)
		return attachment.Metadata{}, "", fmt.Errorf("validate temporary attachment")
	}
	return metadata, directory, nil
}

func (s *Session) modelStatus() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.reason
}

func (s *Session) consumeStartupNotice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	notice := s.startupNotice
	s.startupNotice = ""
	return notice
}

func (s *Session) consumeContextNotice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	notice := s.contextNotice
	s.contextNotice = ""
	return notice
}

func isEnvironmentFileCommand(message string) bool {
	if message == environmentFileCommand {
		return true
	}
	if !strings.HasPrefix(message, environmentFileCommand) {
		return false
	}
	rest := message[len(environmentFileCommand):]
	return len(strings.TrimSpace(rest)) < len(rest)
}

func environmentFileCommandPath(message string) (string, bool) {
	arguments := strings.TrimSpace(strings.TrimPrefix(message, environmentFileCommand))
	if arguments == "" {
		return "", false
	}
	if arguments[0] == '\'' || arguments[0] == '"' {
		quote := arguments[0]
		if len(arguments) < 2 || arguments[len(arguments)-1] != quote {
			return "", false
		}
		path := arguments[1 : len(arguments)-1]
		if strings.TrimSpace(path) == "" || strings.ContainsRune(path, rune(quote)) {
			return "", false
		}
		return path, true
	}
	fields := strings.Fields(arguments)
	if len(fields) != 1 || fields[0] == "" {
		return "", false
	}
	return fields[0], true
}

func runEnvironmentFileSetup(input TerminalInput, output *bufio.Writer, store *localenv.Store, picker EnvironmentFilePicker, path string, next string) error {
	if path == "" {
		fmt.Fprintln(output, environmentFileGuidance)
		if err := output.Flush(); err != nil {
			return err
		}
		var err error
		path, err = picker.Pick()
		if err != nil {
			if errors.Is(err, ErrEnvironmentFilePickerCanceled) {
				fmt.Fprintln(output, "Environment setup canceled.")
				return nil
			}
			if errors.Is(err, errEnvironmentFilePickerUnavailable) {
				fmt.Fprintln(output, "File picker unavailable. Install zenity (or a compatible package such as qarma or matedialog); for example: sudo apt install zenity")
				return nil
			}
			fmt.Fprintf(output, "Environment file selection failed: %v\n", err)
			return nil
		}
		if strings.TrimSpace(path) == "" {
			fmt.Fprintln(output, "Environment setup canceled.")
			return nil
		}
	}

	values, err := localenv.LoadFile(path)
	if err != nil {
		fmt.Fprintf(output, "Environment file rejected: %v\n", err)
		return nil
	}
	return reviewEnvironmentValues(input, output, store, values, next)
}

func reviewEnvironmentValues(input TerminalInput, output *bufio.Writer, store *localenv.Store, values map[string]string, next string) error {
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
	line, _, err := input.ReadLine(maxTerminalInputLineBytes)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return fmt.Errorf("read environment confirmation: %w", err)
		}
		fmt.Fprintln(output, "\nEnvironment setup canceled.")
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(line), "y") {
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
