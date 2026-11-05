//go:build linux

package controltransport

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The socket's immediate directory is private to root. Each ancestor must be
// root-owned and either not writable by others or a sticky directory, so an
// unprivileged user cannot replace any checked component.
func secureSocketDirectory(path string) bool {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 || filepath.Base(path) == "." || len(path) > 107 {
		return false
	}
	dir := filepath.Dir(path)
	var ancestors []string
	for current := dir; ; current = filepath.Dir(current) {
		ancestors = append(ancestors, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		info, err := os.Lstat(ancestors[i])
		if err != nil || !info.IsDir() || !rootOwned(info) {
			return false
		}
		if ancestors[i] == dir {
			if info.Mode().Perm() != 0700 {
				return false
			}
		} else if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return false
		}
	}
	return true
}

func rootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func listenSocket(path string) (net.Listener, error) {
	if !secureSocketDirectory(path) {
		return nil, invalidSocket
	}
	// Do not unlink an existing file or a possibly live socket.
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, invalidSocket
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, invalidSocket
	}
	return listener, nil
}

func socketTransport(path string) (http.RoundTripper, error) {
	if !secureSocketDirectory(path) {
		return nil, invalidSocket
	}
	return &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if !secureSocketDirectory(path) {
				return nil, invalidSocket
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || !rootOwned(info) {
				return nil, invalidSocket
			}
			var dialer net.Dialer
			dialer.Timeout = 5 * time.Second
			conn, err := dialer.DialContext(ctx, "unix", path)
			if err != nil {
				return nil, invalidSocket
			}
			if !rootPeer(conn) {
				conn.Close()
				return nil, invalidSocket
			}
			return conn, nil
		},
	}, nil
}

// Check the kernel-reported peer before sending any HTTP request or secret.
func rootPeer(conn net.Conn) bool {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return false
	}
	valid := false
	err = raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		valid = err == nil && credentials.Uid == 0
	})
	return err == nil && valid
}
