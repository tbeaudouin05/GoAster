// Package attachment contains the local contract used to pass inbound files
// from a channel adapter to the agent.
package attachment

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxBytes is both the Telegram download limit and the maximum size the
	// loopback agent will accept as an attachment reference.
	MaxBytes int64 = 20 << 20

	// MaxAttachments keeps the first implementation deliberately scoped to a
	// single photo or document per message. Telegram photo sizes are not a
	// media album and are reduced to one selected photo by the adapter.
	MaxAttachments = 1

	maxFilenameBytes = 180
)

// Metadata identifies a file that is available locally for the current chat
// turn. The path is temporary and must not be persisted as conversation data.
type Metadata struct {
	Type     string `json:"type"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
}

// SanitizeFilename returns a portable basename that cannot add path
// components or shell metacharacters when it is used as a local filename.
func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == ".." || name == "" {
		return "attachment"
	}

	var safe strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), strings.ContainsRune("._-+()[]@", r):
			safe.WriteRune(r)
		default:
			safe.WriteByte('_')
		}
	}
	name = strings.TrimRight(safe.String(), ". ")
	name = truncateUTF8(name, maxFilenameBytes)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

// ValidateMetadata checks fields that are safe to carry through the model
// request without inspecting the file system.
func (a Metadata) ValidateMetadata() error {
	if a.Type != "image" && a.Type != "document" {
		return fmt.Errorf("unsupported attachment type")
	}
	if a.Filename == "" || a.Filename != SanitizeFilename(a.Filename) || filepath.Base(a.Filename) != a.Filename {
		return fmt.Errorf("unsafe attachment filename")
	}
	if len(a.Filename) > maxFilenameBytes || !utf8.ValidString(a.Filename) {
		return fmt.Errorf("attachment filename is too long or invalid")
	}
	if a.Path == "" || !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path || strings.IndexFunc(a.Path, unicode.IsControl) >= 0 {
		return fmt.Errorf("unsafe attachment path")
	}
	if a.Size < 0 || a.Size > MaxBytes {
		return fmt.Errorf("attachment size is out of range")
	}
	if len(a.MIMEType) > 128 || strings.IndexFunc(a.MIMEType, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid attachment MIME type")
	}
	return nil
}

// Validate checks that metadata refers to a private, regular file in a
// per-message child of the process temporary directory. This prevents the
// loopback API from turning an attachment reference into an arbitrary local
// file reference for model tools.
func (a Metadata) Validate() error {
	if err := a.ValidateMetadata(); err != nil {
		return err
	}
	root, err := filepath.Abs(filepath.Clean(os.TempDir()))
	if err != nil {
		return fmt.Errorf("inspect temporary directory")
	}
	pathName, err := filepath.Abs(a.Path)
	if err != nil || pathName != a.Path {
		return fmt.Errorf("unsafe attachment path")
	}
	parent := filepath.Dir(pathName)
	if parent == root || !within(root, parent) || filepath.Base(pathName) != a.Filename {
		return fmt.Errorf("attachment path is not a per-message temporary file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("inspect temporary directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !within(resolvedRoot, resolvedParent) {
		return fmt.Errorf("attachment path is not a per-message temporary file")
	}

	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("attachment directory is not private")
	}
	info, err := os.Lstat(pathName)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("attachment file is not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 {
		return fmt.Errorf("attachment file is not private")
	}
	if info.Size() != a.Size {
		return fmt.Errorf("attachment size changed")
	}
	return nil
}

// CleanupTemporaryDir removes one private temporary attachment directory.
// It refuses paths outside a direct child of the process temporary directory
// before calling RemoveAll.
func CleanupTemporaryDir(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("attachment directory is empty")
	}
	root, err := filepath.Abs(filepath.Clean(os.TempDir()))
	if err != nil {
		return errors.New("inspect temporary directory")
	}
	clean, err := filepath.Abs(directory)
	if err != nil || clean != directory || clean == root || !within(root, clean) {
		return errors.New("unsafe attachment directory")
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedDirectory, directoryErr := filepath.EvalSymlinks(clean)
	if err != nil || rootErr != nil || directoryErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !within(resolvedRoot, resolvedDirectory) {
		return errors.New("attachment directory is not private")
	}
	return os.RemoveAll(clean)
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// AddReferences adds current-turn attachment metadata to the model-facing
// message. Callers should keep the original message for conversation history
// because these paths are intentionally temporary.
func AddReferences(message string, attachments []Metadata) string {
	if len(attachments) == 0 {
		return message
	}
	var result strings.Builder
	if message != "" {
		result.WriteString(message)
		result.WriteString("\n\n")
	}
	result.WriteString("Attachments available for this message as temporary local files. Use local tools to inspect them when needed:\n")
	for _, a := range attachments {
		fmt.Fprintf(&result, "- type: %s; filename: %s; mime_type: %s; size_bytes: %d; local_path: %s\n", a.Type, a.Filename, a.MIMEType, a.Size, a.Path)
	}
	return result.String()
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
