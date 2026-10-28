//go:build !windows

package command

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const commandTerminationGrace = 100 * time.Millisecond

type processGroupTerminator struct {
	cmd        *exec.Cmd
	cleaned    atomic.Bool
	timerMu    sync.Mutex
	forceTimer *time.Timer
}

func configureCommand(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	terminator := &processGroupTerminator{cmd: cmd}
	cmd.Cancel = terminator.cancel
	cmd.WaitDelay = commandWaitDelay
	return terminator.cleanup
}

func (terminator *processGroupTerminator) cancel() error {
	terminator.timerMu.Lock()
	defer terminator.timerMu.Unlock()
	if terminator.cleaned.Load() {
		return nil
	}
	if err := signalProcessGroup(terminator.cmd, syscall.SIGTERM); err != nil {
		return err
	}
	if !terminator.cleaned.Load() && terminator.forceTimer == nil {
		terminator.forceTimer = time.AfterFunc(commandTerminationGrace, func() {
			if !terminator.cleaned.Load() {
				_ = terminateProcessGroup(terminator.cmd)
			}
		})
	}
	return nil
}

func (terminator *processGroupTerminator) cleanup() {
	terminator.timerMu.Lock()
	if terminator.cleaned.Swap(true) {
		terminator.timerMu.Unlock()
		return
	}
	if terminator.forceTimer != nil {
		terminator.forceTimer.Stop()
	}
	terminator.timerMu.Unlock()
	_ = terminateProcessGroup(terminator.cmd)
}

func terminateProcessGroup(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err := cmd.Process.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
