//go:build !windows

package chat

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockSessionFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
