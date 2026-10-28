// Package command runs commands through the platform's native shell.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Request describes a command to execute.
type Request struct {
	Command string
	Dir     string
	Timeout time.Duration
}

// Result contains the command's captured output and exit status.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Execute runs req.Command through the platform's native shell.
// A nonzero command exit is reported in Result.ExitCode without an error.
func Execute(ctx context.Context, req Request) (Result, error) {
	result := Result{ExitCode: -1}
	if strings.TrimSpace(req.Command) == "" {
		return result, errors.New("command must not be empty")
	}
	if ctx == nil {
		return result, errors.New("context must not be nil")
	}
	if req.Timeout < 0 {
		return result, errors.New("timeout must not be negative")
	}

	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	cmd := shellCommand(runCtx, req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	if ctxErr := runCtx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if exitErr != nil {
		return result, nil
	}
	return result, fmt.Errorf("run command: %w", err)
}
