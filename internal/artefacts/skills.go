// Package artefacts manages user-owned runtime skill files.
package artefacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"goaster/internal/filesync"
	"goaster/skills"
)

const (
	metadataLimit = 8 << 10
	skillLimit    = 256 << 10
	maxSkills     = 100
)

// Root returns the runtime artefact directory, without creating it.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".goaster", "artefacts"), nil
}

// InstallStarters installs official starters once, when no skills directory
// exists. Existing directories are never merged, replaced, or updated.
func InstallStarters(ctx context.Context) error {
	root, err := Root()
	if err != nil {
		return err
	}
	return installStartersAt(ctx, root, syncStarterDirectory)
}

func installStartersAt(ctx context.Context, root string, syncDirectory func(string) error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privateDir(filepath.Dir(root)); err != nil {
		return err
	}
	if err := privateDir(root); err != nil {
		return err
	}
	// The process lock serializes live installers. The directory below is a
	// separate durable marker that survives an interrupted installation.
	lockPath := filepath.Join(root, ".skills-install.lock")
	info, err := os.Lstat(lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect starter installation lock: %w", err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("starter installation lock must be a regular file: %s", lockPath)
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open starter installation lock: %w", err)
	}
	defer lockFile.Close()
	opened, err := lockFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened starter installation lock: %w", err)
	}
	info, err = os.Lstat(lockPath)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(opened, info) {
		return fmt.Errorf("starter installation lock changed or is not a regular file: %s", lockPath)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := filesync.LockFile(lockFile); err == nil {
			break
		} else if !filesync.IsLockBusy(err) {
			return fmt.Errorf("lock starter installation: %w", err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock := filepath.Join(root, ".skills-installing")
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("starter installation may be incomplete (%s): %w; inspect the skills directory and marker before removing the marker", lock, err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			if err := os.Remove(lock); err != nil {
				result = errors.Join(result, fmt.Errorf("remove starter installation lock %s: %w", lock, err))
			} else if err := syncDirectory(root); err != nil {
				result = errors.Join(result, fmt.Errorf("sync starter installation lock removal: %w", err))
			}
		}
	}()
	if err := syncDirectory(root); err != nil {
		return fmt.Errorf("sync starter installation lock: %w", err)
	}
	destination := filepath.Join(root, "skills")
	info, err = os.Lstat(destination)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skills path is not a real directory: %s", destination)
		}
		metadata, err := discoverAt(root)
		if err != nil {
			return fmt.Errorf("validate existing skills: %w", err)
		}
		if metadata == "" {
			return fmt.Errorf("existing skills directory %s contains no usable skills; add a skill or move the directory aside before retrying", destination)
		}
		entries, err := os.ReadDir(destination)
		if err != nil {
			return fmt.Errorf("read existing skills: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if _, err := loadAt(root, entry.Name()); err != nil {
					return fmt.Errorf("validate existing skill %s: %w", entry.Name(), err)
				}
			}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect skills directory: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	files, err := starterFiles()
	if err != nil {
		return fmt.Errorf("validate bundled starter skills: %w", err)
	}
	// Reserve the destination exclusively. Files are also created exclusively;
	// a failed partial installation is never treated as complete on restart.
	if err := os.Mkdir(destination, 0700); err != nil {
		return fmt.Errorf("create skills directory without overwriting existing data: %w", err)
	}
	keepLock = true
	if err := syncDirectory(root); err != nil {
		return fmt.Errorf("sync starter destination: %w; inspect %s and %s before retrying", err, destination, lock)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("install starter skills: %w; inspect %s and %s before retrying", err, destination, lock)
		}
		dir := filepath.Join(destination, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			return fmt.Errorf("install skill %s: %w; inspect %s and %s before retrying", name, err, destination, lock)
		}
		for _, filename := range []string{"META.md", "SKILL.md", "AUTH.md"} {
			path := filepath.Join(dir, filename)
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return fmt.Errorf("install %s: %w; inspect %s and %s before retrying", path, err, destination, lock)
			}
			data := files[name][filename]
			written, writeErr := file.Write(data)
			if writeErr == nil && written != len(data) {
				writeErr = io.ErrShortWrite
			}
			if writeErr == nil {
				writeErr = file.Sync()
			}
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				return fmt.Errorf("write %s: %v %v; inspect %s and %s before retrying", path, writeErr, closeErr, destination, lock)
			}
		}
		if err := syncDirectory(dir); err != nil {
			return fmt.Errorf("sync skill directory %s: %w; inspect %s and %s before retrying", dir, err, destination, lock)
		}
	}
	if err := syncDirectory(destination); err != nil {
		return fmt.Errorf("sync skills directory: %w; inspect %s and %s before retrying", err, destination, lock)
	}
	if err := os.Remove(lock); err != nil {
		return fmt.Errorf("finish starter installation: remove %s: %w", lock, err)
	}
	if err := syncDirectory(root); err != nil {
		return fmt.Errorf("sync completed starter installation: %w; installation durability is uncertain, inspect %s before retrying", err, destination)
	}
	return nil
}

func syncStarterDirectory(directory string) error {
	return filesync.DirectoryPath(directory)
}

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create directory %s: %w", path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", path)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("restrict directory %s: %w", path, err)
	}
	return nil
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 80 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

// ValidName reports whether name can identify a runtime skill.
func ValidName(name string) bool {
	return validName(name)
}

func starterFiles() (map[string]map[string][]byte, error) {
	entries, err := skills.Starters.ReadDir(".")
	if err != nil {
		return nil, err
	}
	files := make(map[string]map[string][]byte)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !validName(name) || len(files) >= maxSkills {
			return nil, fmt.Errorf("invalid bundled starter %q", name)
		}
		contents := make(map[string][]byte)
		for _, filename := range []string{"META.md", "SKILL.md", "AUTH.md"} {
			data, err := skills.Starters.ReadFile(name + "/" + filename)
			if err != nil {
				return nil, fmt.Errorf("read bundled starter %s: %w", name, err)
			}
			limit := skillLimit
			if filename != "SKILL.md" {
				limit = metadataLimit
			}
			if len(data) > limit || strings.TrimSpace(string(data)) == "" || !utf8.Valid(data) {
				return nil, fmt.Errorf("bundled starter %s has invalid %s", name, filename)
			}
			contents[filename] = data
		}
		if err := validateMetadata(name, contents["META.md"]); err != nil {
			return nil, err
		}
		files[name] = contents
	}
	if len(files) == 0 {
		return nil, errors.New("no bundled starter skills")
	}
	return files, nil
}

func validateMetadata(name string, data []byte) error {
	if len(data) > metadataLimit || !utf8.Valid(data) {
		return fmt.Errorf("%s metadata is invalid or exceeds the size limit", name)
	}
	var foundName, description string
	seenName, seenDescription := false, false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case strings.HasPrefix(line, "name: "):
			if seenName {
				return fmt.Errorf("%s metadata has duplicate name", name)
			}
			seenName = true
			foundName = strings.TrimSpace(strings.TrimPrefix(line, "name: "))
		case strings.HasPrefix(line, "description: "):
			if seenDescription {
				return fmt.Errorf("%s metadata has duplicate description", name)
			}
			seenDescription = true
			description = strings.TrimSpace(strings.TrimPrefix(line, "description: "))
		default:
			return fmt.Errorf("%s metadata may contain only name and description lines", name)
		}
	}
	if foundName != name || description == "" || len(description) > 1024 || strings.IndexFunc(description, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s metadata must contain its name and a single-line description", name)
	}
	return nil
}
