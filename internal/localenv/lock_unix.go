//go:build !windows

package localenv

import "golang.org/x/sys/unix"

const environmentLockOpenFlags = unix.O_NOFOLLOW
