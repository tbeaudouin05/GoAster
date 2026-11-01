package artefacts

import (
	"fmt"
	"path/filepath"
)

// EnsureRuntimeDirectory creates one private directory below the fresh
// artefacts root. Each component is checked independently so a symbolic link
// cannot redirect runtime state outside the GoAster home.
func EnsureRuntimeDirectory(parts ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	if err := privateDir(filepath.Dir(root)); err != nil {
		return "", err
	}
	if err := privateDir(root); err != nil {
		return "", err
	}
	current := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part || filepath.VolumeName(part) != "" {
			return "", fmt.Errorf("invalid artefacts directory component")
		}
		current = filepath.Join(current, part)
		if err := privateDir(current); err != nil {
			return "", err
		}
	}
	return current, nil
}
