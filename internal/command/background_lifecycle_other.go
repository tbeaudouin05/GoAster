//go:build !linux

package command

// Process groups, sessions, pidfds and /proc scans do not contain descendants:
// a member may fork and exit between enumeration and inspection, and children
// may leave a group or session. Refuse launch until a kernel-enforced container
// can be established before exec and its emptiness verified after termination.
func backgroundLifecycleSupported() bool { return false }

func backgroundPTYSupported() bool { return false }

func backgroundProcessLifecycle(pid int, terminal bool, fallback func() error) (func() error, func() error) {
	unavailable := func() error { return ErrBackgroundCleanupUnavailable }
	return unavailable, unavailable
}
