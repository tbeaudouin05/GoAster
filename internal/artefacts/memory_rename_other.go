//go:build !linux && !darwin && !windows

package artefacts

import "errors"

func renameMemoryNoReplace(source, destination string) error {
	return errors.New("atomic non-replacing rename is not available on this platform")
}
