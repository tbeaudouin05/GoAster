package chat

import (
	"errors"
	"fmt"

	"github.com/ncruces/zenity"
)

// ErrEnvironmentFilePickerCanceled indicates that the user closed the file
// picker without choosing a file.
var ErrEnvironmentFilePickerCanceled = errors.New("environment file selection canceled")

// EnvironmentFilePicker selects a local environment file.
type EnvironmentFilePicker interface {
	Pick() (string, error)
}

// EnvironmentFilePickerFunc adapts a function to EnvironmentFilePicker.
type EnvironmentFilePickerFunc func() (string, error)

// Pick selects a local environment file.
func (f EnvironmentFilePickerFunc) Pick() (string, error) {
	return f()
}

type nativeEnvironmentFilePicker struct{}

func (nativeEnvironmentFilePicker) Pick() (string, error) {
	path, err := zenity.SelectFile(
		zenity.Title("Choose an environment text file"),
		zenity.FileFilters{
			{Name: "Environment text files", Patterns: []string{"*.env", "*.txt"}},
			{Name: "All files", Patterns: []string{"*"}},
		},
	)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrEnvironmentFilePickerCanceled
	}
	if err != nil {
		return "", fmt.Errorf("open environment file picker: %w", err)
	}
	return path, nil
}
