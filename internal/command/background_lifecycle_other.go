//go:build !linux

package command

import "sync"

// Other platforms retain direct process cleanup, or the platform-specific
// fallback supplied by StartBackground. The fallback is intentionally not
// presented as complete descendant cleanup.
func backgroundLifecycleSupported() bool { return true }

func backgroundPTYSupported() bool { return false }

func backgroundProcessLifecycle(pid int, terminal bool, fallback func() error) (func() error, func(), func() error) {
	var mu sync.Mutex
	finished := false
	beginWait := func() {}
	cancel := func() error {
		mu.Lock()
		defer mu.Unlock()
		if finished {
			return nil
		}
		if fallback == nil {
			return nil
		}
		return fallback()
	}
	finish := func() error {
		mu.Lock()
		defer mu.Unlock()
		finished = true
		return nil
	}
	return cancel, beginWait, finish
}
