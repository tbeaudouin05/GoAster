//go:build darwin

package artefacts

import "golang.org/x/sys/unix"

func renameMemoryNoReplace(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
