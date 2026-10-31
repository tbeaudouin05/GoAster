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

// EnvironmentFileSavePicker chooses where /get-env writes a private copy.
type EnvironmentFileSavePicker interface {
	PickSavePath() (string, error)
}

// LocalFilePicker selects any local file for a terminal chat command.
type LocalFilePicker interface {
	PickFile() (string, error)
}

// EnvironmentFilePickerFunc adapts a function to the local picker operations.
type EnvironmentFilePickerFunc func() (string, error)

// Pick selects a local environment file.
func (f EnvironmentFilePickerFunc) Pick() (string, error) {
	return f()
}

// PickSavePath adapts the same injectable path picker for /get-env.
func (f EnvironmentFilePickerFunc) PickSavePath() (string, error) {
	return f()
}

// PickFile selects any local file.
func (f EnvironmentFilePickerFunc) PickFile() (string, error) {
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
		return "", wrapNativeFilePickerError(err)
	}
	return path, nil
}

func (nativeLocalFilePicker) PickFile() (string, error) {
	path, err := zenity.SelectFile(
		zenity.Title("Choose a local file"),
		zenity.FileFilters{
			{Name: "All files", Patterns: []string{"*"}},
		},
	)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrEnvironmentFilePickerCanceled
	}
	if err != nil {
		return "", wrapNativeFilePickerError(err)
	}
	return path, nil
}

func (nativeLocalFilePicker) PickSavePath() (string, error) {
	path, err := zenity.SelectFileSave(
		zenity.Title("Save GoAster environment file"),
		zenity.Filename("goaster.env"),
		zenity.ConfirmOverwrite(),
		zenity.FileFilters{
			{Name: "Environment files", Patterns: []string{"*.env", "*.txt"}},
			{Name: "All files", Patterns: []string{"*"}},
		},
	)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrEnvironmentFilePickerCanceled
	}
	if err != nil {
		return "", wrapNativeFilePickerError(err)
	}
	return path, nil
}

func wrapNativeFilePickerError(err error) error {
	if runtime.GOOS == "linux" && errors.Is(err, exec.ErrNotFound) {
		return errEnvironmentFilePickerUnavailable
	}
	return fmt.Errorf("open local file picker: %w", err)
}
