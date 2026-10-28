// Package command runs commands through the platform's native shell.
package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// MaxCommandBytes bounds the shell command passed to the operating system.
	MaxCommandBytes = 1 << 20

	// MaxOutputBytes is the maximum number of bytes retained for each output
	// stream. Execution stops when a stream exceeds this limit.
	MaxOutputBytes = 1 << 20

	// DefaultTimeout is used when a request does not specify a timeout.
	DefaultTimeout = 2 * time.Minute

	// MaxTimeout is the largest timeout accepted for one command execution.
	MaxTimeout = 10 * time.Minute

	commandWaitDelay = time.Second
)

// ErrOutputLimit reports that a command was stopped after exceeding the
// retained output limit.
var ErrOutputLimit = errors.New("command output exceeded the limit")

// Request describes a command to execute.
type Request struct {
	Command string
	Dir     string
	// Timeout controls one command execution. Zero uses DefaultTimeout.
	Timeout time.Duration
}

// Result contains the command's captured output and exit status.
type Result struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	StdoutTruncated bool
	StderrTruncated bool
}

// Execute runs req.Command through the platform's native shell.
// A nonzero command exit is reported in Result.ExitCode without an error.
// Context cancellation and timeout expiration are returned as errors with any
// output captured before interruption preserved in the result.
func Execute(ctx context.Context, req Request) (Result, error) {
	result := Result{ExitCode: -1}
	if strings.TrimSpace(req.Command) == "" {
		return result, errors.New("command must not be empty")
	}
	if ctx == nil {
		return result, errors.New("context must not be nil")
	}
	if len(req.Command) > MaxCommandBytes {
		return result, errors.New("command exceeds the 1 MiB limit")
	}
	if strings.IndexByte(req.Command, 0) >= 0 {
		return result, errors.New("command contains an invalid NUL byte")
	}
	if req.Timeout < 0 {
		return result, errors.New("timeout must not be negative")
	}
	if req.Timeout > MaxTimeout {
		return result, errors.New("timeout exceeds the maximum allowed duration")
	}

	if req.Dir != "" {
		resolvedDir, err := resolveWorkingDirectory(req.Dir)
		if err != nil {
			return result, err
		}
		req.Dir = resolvedDir
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	executionCtx, stopExecution := context.WithCancel(runCtx)
	defer stopExecution()
	var outputLimitReached atomic.Bool
	stopForOutputLimit := func() {
		outputLimitReached.Store(true)
		stopExecution()
	}

	cmd := shellCommand(executionCtx, req.Command)
	cleanup := configureCommand(cmd)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	stdout := newLimitedOutput(MaxOutputBytes, stopForOutputLimit)
	stderr := newLimitedOutput(MaxOutputBytes, stopForOutputLimit)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	cleanup()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()

	if ctxErr := runCtx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if outputLimitReached.Load() {
		return result, ErrOutputLimit
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if exitErr != nil {
		return result, nil
	}
	return result, errors.New("command execution failed")
}

func resolveWorkingDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("working directory is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("working directory is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.New("working directory is unavailable")
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return resolved, nil
}

type limitedOutput struct {
	buffer    bytes.Buffer
	limit     int
	onLimit   func()
	truncated bool
}

func newLimitedOutput(limit int, onLimit func()) limitedOutput {
	return limitedOutput{limit: limit, onLimit: onLimit}
}

func (output *limitedOutput) Write(p []byte) (int, error) {
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = output.buffer.Write(p[:remaining])
			output.truncated = true
			if output.onLimit != nil {
				output.onLimit()
			}
		} else {
			_, _ = output.buffer.Write(p)
		}
	} else if len(p) > 0 {
		output.truncated = true
		if output.onLimit != nil {
			output.onLimit()
		}
	}
	return len(p), nil
}

func (output *limitedOutput) String() string {
	return output.buffer.String()
}

func (output *limitedOutput) Truncated() bool {
	return output.truncated
}
