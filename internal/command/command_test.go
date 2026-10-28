package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecuteSuccess(t *testing.T) {
	result, err := Execute(context.Background(), Request{Command: successCommand()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "success" {
		t.Errorf("stdout = %q, want %q", got, "success")
	}
	if result.Stderr != "" {
		t.Errorf("stderr = %q, want empty", result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestExecuteCapturesStderrAndNonzeroExit(t *testing.T) {
	result, err := Execute(context.Background(), Request{Command: failingCommand()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stderr); got != "problem" {
		t.Errorf("stderr = %q, want %q", got, "problem")
	}
	if result.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", result.ExitCode)
	}
}

func TestExecuteWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	result, err := Execute(context.Background(), Request{Command: workingDirectoryCommand(), Dir: dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); !samePath(got, dir) {
		t.Errorf("stdout working directory = %q, want %q", got, dir)
	}
}

func TestExecuteWorkingDirectoryValidation(t *testing.T) {
	t.Run("relative directory is resolved", func(t *testing.T) {
		result, err := Execute(context.Background(), Request{Command: workingDirectoryCommand(), Dir: "."})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		current, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(result.Stdout); !samePath(got, current) {
			t.Errorf("stdout working directory = %q, want %q", got, current)
		}
	})

	t.Run("directory symlink is resolved", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic-link creation may require elevated Windows privileges")
		}
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "working-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		result, err := Execute(context.Background(), Request{Command: workingDirectoryCommand(), Dir: link})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if got := strings.TrimSpace(result.Stdout); !samePath(got, target) {
			t.Errorf("stdout working directory = %q, want %q", got, target)
		}
	})

	t.Run("missing directory does not expose path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "secret-working-directory")
		result, err := Execute(context.Background(), Request{
			Command: "printf should-not-run",
			Dir:     missing,
		})
		if err == nil {
			t.Fatal("Execute() error = nil, want an error")
		}
		if strings.Contains(err.Error(), missing) {
			t.Errorf("working-directory error exposed path: %q", err)
		}
		if result.ExitCode != -1 || result.Stdout != "" || result.Stderr != "" {
			t.Errorf("result for invalid directory = %+v, want untouched result", result)
		}
	})

	t.Run("file is not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "working-file")
		if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), Request{Command: "printf should-not-run", Dir: file})
		if err == nil || !strings.Contains(err.Error(), "working directory") {
			t.Fatalf("Execute() error = %v, want working-directory error", err)
		}
	})
}

func TestExecuteCancellationAndTimeout(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := Execute(ctx, Request{Command: loopCommand()})
			done <- err
		}()

		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Execute() error = %v, want context.Canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Execute() did not stop after cancellation")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		result, err := Execute(context.Background(), Request{
			Command: loopCommand(),
			Timeout: 100 * time.Millisecond,
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute() error = %v, want context.DeadlineExceeded", err)
		}
		if result.ExitCode != -1 {
			t.Errorf("exit code = %d, want -1 for interrupted process", result.ExitCode)
		}
	})
}

func TestExecuteHonorsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Execute(ctx, Request{Command: loopCommand()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if result.ExitCode != -1 || result.Stdout != "" || result.Stderr != "" {
		t.Errorf("result for canceled context = %+v, want untouched result", result)
	}
}

func TestExecuteCancellationCleansUpChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup uses POSIX process groups")
	}

	directory := t.TempDir()
	started := filepath.Join(directory, "started")
	survived := filepath.Join(directory, "survived")
	command := fmt.Sprintf(
		"printf started > %s; (sleep 0.3; printf survived > %s) & while :; do :; done",
		shellQuote(started), shellQuote(survived),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, Request{Command: command})
		done <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute() did not stop after child-process cancellation")
	}

	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(survived); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("background child artifact stat error = %v, want os.ErrNotExist", err)
	}
}

func TestExecuteCancellationAllowsCleanupTrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell trap cleanup")
	}

	directory := t.TempDir()
	artifact := filepath.Join(directory, "temporary-artifact")
	quotedArtifact := shellQuote(artifact)
	command := fmt.Sprintf(
		"touch %s; trap \"rm -f -- %s\" TERM EXIT; while :; do :; done",
		quotedArtifact, quotedArtifact,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, Request{Command: command})
		done <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(artifact); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("command did not create temporary artifact")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute() did not stop after cleanup-trap cancellation")
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary artifact stat error = %v, want os.ErrNotExist", err)
	}
}

func TestExecuteCleansUpBackgroundChildAfterNormalExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup uses POSIX process groups")
	}

	directory := t.TempDir()
	artifact := filepath.Join(directory, "background-artifact")
	command := fmt.Sprintf(
		"(sleep 0.3; printf survived > %s) >/dev/null 2>&1 & exit 0",
		shellQuote(artifact),
	)
	result, err := Execute(context.Background(), Request{Command: command})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("background child artifact stat error = %v, want os.ErrNotExist", err)
	}
}

func TestExecuteLimitsCapturedOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		command    string
		outputSize func(Result) int
		truncated  func(Result) bool
	}{
		{
			name:       "stdout",
			command:    largeStdoutCommand(MaxOutputBytes + 1024),
			outputSize: func(result Result) int { return len(result.Stdout) },
			truncated:  func(result Result) bool { return result.StdoutTruncated },
		},
		{
			name:       "stderr",
			command:    largeStderrCommand(MaxOutputBytes + 1024),
			outputSize: func(result Result) int { return len(result.Stderr) },
			truncated:  func(result Result) bool { return result.StderrTruncated },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(context.Background(), Request{Command: test.command})
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("Execute() error = %v, want ErrOutputLimit", err)
			}
			if got := test.outputSize(result); got != MaxOutputBytes {
				t.Errorf("captured output length = %d, want %d", got, MaxOutputBytes)
			}
			if !test.truncated(result) {
				t.Error("output truncation flag = false, want true")
			}
			if result.ExitCode != -1 {
				t.Errorf("exit code = %d, want -1 for output-limit interruption", result.ExitCode)
			}
		})
	}
}

func TestExecuteRejectsUnsafeRequestBounds(t *testing.T) {
	secret := "secret-command-value"
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "command too large",
			req:  Request{Command: strings.Repeat("x", MaxCommandBytes+1)},
			want: "1 MiB limit",
		},
		{
			name: "negative timeout",
			req:  Request{Command: secret, Timeout: -time.Millisecond},
			want: "timeout must not be negative",
		},
		{
			name: "timeout too large",
			req:  Request{Command: secret, Timeout: MaxTimeout + time.Nanosecond},
			want: "timeout exceeds",
		},
		{
			name: "NUL command",
			req:  Request{Command: secret + "\x00suffix"},
			want: "NUL",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(context.Background(), test.req)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Execute() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error exposed command content: %q", err)
			}
			if result.ExitCode != -1 {
				t.Errorf("exit code = %d, want -1", result.ExitCode)
			}
		})
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestExecuteEmptyCommand(t *testing.T) {
	result, err := Execute(context.Background(), Request{Command: " \t\n"})
	if err == nil {
		t.Fatal("Execute() error = nil, want an error")
	}
	if result.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", result.ExitCode)
	}
}

func successCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output 'success'"
	}
	return "printf success"
}

func failingCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Error.WriteLine('problem'); exit 7"
	}
	return "printf problem >&2; exit 7"
}

func workingDirectoryCommand() string {
	if runtime.GOOS == "windows" {
		return "(Get-Location).Path"
	}
	return "pwd"
}

func loopCommand() string {
	if runtime.GOOS == "windows" {
		return "while ($true) {}"
	}
	return "while :; do :; done"
}

func largeStdoutCommand(size int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("[Console]::Out.Write('A' * %d)", size)
	}
	return fmt.Sprintf("yes A | head -c %d", size)
}

func largeStderrCommand(size int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("[Console]::Error.Write('B' * %d)", size)
	}
	return fmt.Sprintf("yes B | head -c %d >&2", size)
}

func samePath(got, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(got, want)
	}
	gotAbs, gotErr := os.Stat(got)
	wantAbs, wantErr := os.Stat(want)
	return gotErr == nil && wantErr == nil && os.SameFile(gotAbs, wantAbs)
}
