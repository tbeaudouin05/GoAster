package command

import (
	"errors"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux background cleanup is deliberately best effort. A private process
// group is useful when the main process is being stopped, but it is not a
// kernel-enforced descendant container and does not cover every possible
// descendant after the main process exits.
func backgroundLifecycleSupported() bool { return true }

func backgroundPTYSupported() bool { return true }

func backgroundProcessLifecycle(pid int, terminal bool, fallback func() error) (func() error, func(), func() error) {
	var mu sync.Mutex
	var cleanupErr error
	var canceled, finished bool

	cancel := func() error {
		mu.Lock()
		defer mu.Unlock()
		if canceled || finished {
			return cleanupErr
		}
		canceled = true

		// The leader remains unreaped until beginWait marks the lifecycle
		// finished, so its PID and process-group ID cannot be reused here.
		if !terminal && backgroundProcessIDHeld(pid) {
			cleanupErr = errors.Join(cleanupErr, signalBackgroundProcessGroup(pid, syscall.SIGTERM))
		}
		if fallback != nil {
			if err := fallback(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
		return cleanupErr
	}
	beginWait := func() {
		// Observe exit without reaping. This keeps the PID reserved while
		// cancellation can signal the group, including while the leader is a zombie.
		for {
			err := unix.Waitid(unix.P_PID, pid, nil, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		mu.Lock()
		finished = true
		mu.Unlock()
	}
	finish := func() error {
		mu.Lock()
		defer mu.Unlock()
		finished = true
		return cleanupErr
	}
	return cancel, beginWait, finish
}

func backgroundProcessIDHeld(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalBackgroundProcessGroup(pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
