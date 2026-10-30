package chat

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/ncruces/zenity"
)

// ErrEnvironmentFilePickerCanceled indicates that the user closed the file
// picker without choosing a file.
var ErrEnvironmentFilePickerCanceled = errors.New("environment file selection canceled")

var errEnvironmentFilePickerUnavailable = errors.New("environment file picker unavailable")

// EnvironmentFilePicker selects the local environment file used by /env.
type EnvironmentFilePicker interface {
	Pick() (string, error)
}

// ImageFilePicker selects an image for a local terminal chat command.
type ImageFilePicker interface {
	PickImage() (string, error)
}

// EnvironmentFilePickerFunc adapts a function to the local picker operations.
type EnvironmentFilePickerFunc func() (string, error)

// Pick selects a local environment file.
func (f EnvironmentFilePickerFunc) Pick() (string, error) {
	return f()
}

// PickImage selects a local image file.
func (f EnvironmentFilePickerFunc) PickImage() (string, error) {
	return f()
}

type nativeLocalFilePicker struct{}

func (nativeLocalFilePicker) Pick() (string, error) {
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
		return "", wrapNativeEnvironmentFilePickerError(err)
	}
	return path, nil
}

func (nativeLocalFilePicker) PickImage() (string, error) {
	path, err := zenity.SelectFile(
		zenity.Title("Choose an OVH screenshot"),
		zenity.FileFilters{
			{Name: "Image files", Patterns: []string{"*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp"}},
			{Name: "All files", Patterns: []string{"*"}},
		},
	)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrEnvironmentFilePickerCanceled
	}
	if err != nil {
		return "", wrapNativeEnvironmentFilePickerError(err)
	}
	return path, nil
}

func wrapNativeEnvironmentFilePickerError(err error) error {
	if runtime.GOOS == "linux" && errors.Is(err, exec.ErrNotFound) {
		return errEnvironmentFilePickerUnavailable
	}
	return fmt.Errorf("open environment file picker: %w", err)
}
