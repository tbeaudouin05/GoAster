package command

import (
	"context"
	"errors"
	"os"
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

func samePath(got, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(got, want)
	}
	gotAbs, gotErr := os.Stat(got)
	wantAbs, wantErr := os.Stat(want)
	return gotErr == nil && wantErr == nil && os.SameFile(gotAbs, wantAbs)
}
