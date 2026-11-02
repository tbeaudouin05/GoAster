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
	if err := installDefaultMemory(root, syncDirectory); err != nil {
		return err
	}
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skills path is not a real directory: %s", destination)
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
		return fmt.Errorf("validate starter skill assets: %w", err)
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

// starterDirectory locates ordinary package assets next to the executable,
// or under its installation prefix. The explicit override supports
// development and service installations.
func starterDirectory() (string, error) {
	if path := strings.TrimSpace(os.Getenv("GOASTER_STARTER_DIR")); path != "" {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("starter assets directory is unavailable or not a real directory: %s; set GOASTER_STARTER_DIR to the matching reviewed skills directory or unset it to use package assets", path)
		}
		return path, nil
	}
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(dir, "skills"), filepath.Join(filepath.Dir(dir), "share", "goaster", "skills"))
	}
	for _, path := range candidates {
		info, err := os.Lstat(path)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
	}
	return "", errors.New("starter assets not found; place the package skills directory beside the executable or under its prefix at share/goaster/skills, or set GOASTER_STARTER_DIR")
}

func starterFiles() (map[string]map[string][]byte, error) {
	directory, err := starterDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string]map[string][]byte)
	for _, entry := range entries {
		name := entry.Name()
		if name == "MEMORY.md" && entry.Type().IsRegular() {
			continue
		}
		if !entry.IsDir() || !validName(name) || len(files) >= maxSkills {
			return nil, fmt.Errorf("invalid starter asset %q", name)
		}
		children, err := os.ReadDir(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("read starter directory %s: %w", name, err)
		}
		for _, child := range children {
			if child.Name() != "META.md" && child.Name() != "SKILL.md" && child.Name() != "AUTH.md" {
				return nil, fmt.Errorf("unexpected starter asset %s/%s", name, child.Name())
			}
		}
		contents := make(map[string][]byte)
		for _, filename := range []string{"META.md", "SKILL.md", "AUTH.md"} {
			limit := int64(skillLimit)
			if filename != "SKILL.md" {
				limit = metadataLimit
			}
			data, err := readSkillFile(filepath.Join(directory, name, filename), limit)
			if err != nil {
				return nil, fmt.Errorf("read starter %s/%s: %w", name, filename, err)
			}
			if strings.TrimSpace(string(data)) == "" || !utf8.Valid(data) {
				return nil, fmt.Errorf("starter %s has invalid %s", name, filename)
			}
			contents[filename] = data
		}
		if err := validateMetadata(name, contents["META.md"]); err != nil {
			return nil, err
		}
		files[name] = contents
	}
	if len(files) == 0 {
		return nil, errors.New("no starter skills")
	}
	return files, nil
}

// installDefaultMemory only publishes a complete template if no file exists.
// Both publication methods leave concurrently created files untouched.
func installDefaultMemory(root string, syncDirectory func(string) error) error {
	return installDefaultMemoryUsing(root, syncDirectory, os.Link, renameMemoryNoReplace)
}

func installDefaultMemoryUsing(root string, syncDirectory func(string) error, link, renameNoReplace func(string, string) error) error {
	directory := filepath.Join(root, "memory")
	if err := privateDir(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "MEMORY.md")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect default memory: %w", err)
	}
	source, err := starterDirectory()
	if err != nil {
		return err
	}
	data, err := readSkillFile(filepath.Join(source, "MEMORY.md"), metadataLimit)
	if err != nil || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("read default memory template: invalid or unavailable asset")
	}
	file, err := os.CreateTemp(directory, ".memory-starter-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary default memory: %w", err)
	}
	defer os.Remove(file.Name())
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("write default memory: %w", errors.Join(writeErr, closeErr))
	}
	if err := publishMemory(file.Name(), path, link, renameNoReplace); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("install default memory without overwriting: %w; use a filesystem supporting hard links or atomic non-replacing rename, or create MEMORY.md yourself before retrying", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync default memory: %w; inspect the file before retrying", err)
	}
	return nil
}

// publishMemory never exposes partially written content or replaces a file.
// Some filesystems prohibit hard links but support an exclusive atomic rename.
func publishMemory(source, destination string, link, renameNoReplace func(string, string) error) error {
	linkErr := link(source, destination)
	if linkErr == nil || errors.Is(linkErr, os.ErrExist) {
		return linkErr
	}
	if err := renameNoReplace(source, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
		return errors.Join(fmt.Errorf("hard link: %w", linkErr), fmt.Errorf("atomic non-replacing rename: %w", err))
	}
	return nil
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
