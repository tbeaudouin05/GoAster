//go:build windows

package artefacts

import "syscall"

// syscall.Rename uses MoveFile without MOVEFILE_REPLACE_EXISTING. Both paths
// are in the same directory, so this is a move, not a cross-volume copy.
func renameMemoryNoReplace(source, destination string) error {
	return syscall.Rename(source, destination)
}
