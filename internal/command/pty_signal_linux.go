package command

import (
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux TIOCSIG addresses the foreground group through the PTY itself, without
// a separate group-ID lookup and its reuse race. Linux permits only job-control
// signals here, so this is best effort: SIGINT can be ignored or caught.
func signalPTYForeground(terminal io.ReadWriteCloser) {
	file, ok := terminal.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return
	}
	_ = conn.Control(func(fd uintptr) {
		_ = unix.IoctlSetInt(int(fd), unix.TIOCSIG, int(unix.SIGINT))
	})
}
