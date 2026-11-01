// Package filesync provides portable directory durability checks.
package filesync

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

type Directory interface {
	Sync() error
	Close() error
}

// DirectoryFile syncs and closes a directory, tolerating only operations that
// the platform or filesystem does not support. Close failures remain errors.
func DirectoryFile(file Directory) error {
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) || errors.Is(syncErr, syscall.ENOSYS) ||
		(runtime.GOOS == "windows" && errors.Is(syncErr, os.ErrPermission)) {
		syncErr = nil
	}
	return errors.Join(syncErr, closeErr)
}

func DirectoryPath(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	return DirectoryFile(file)
}
