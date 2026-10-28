//go:build windows

package command

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func configureCommand(cmd *exec.Cmd) func() {
	cmd.Cancel = func() error { return terminateProcessTree(cmd) }
	cmd.WaitDelay = commandWaitDelay
	return func() { _ = terminateProcessTree(cmd) }
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// PowerShell commonly starts a second process for a command. Ask the
	// platform to terminate the process tree, then fall back to the direct
	// process handle if taskkill is unavailable or the process already left.
	if err := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
