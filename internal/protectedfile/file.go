// Package protectedfile provides small, owner-checked operations for files
// containing credentials or other one-shot private material.
package protectedfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"goaster/internal/filesync"
)

// Identity is the stable identity of a file on the local filesystem.
// It is deliberately value-shaped so callers can persist it without a custom
// pointer or an open file descriptor.
type Identity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// Content is a bounded private-file read together with the identity observed
// while the descriptor was open.
type Content struct {
	Data     []byte
	Identity Identity
}

// Read opens a regular mode-0600 file only through owner-controlled parents,
// verifies that the path did not change after opening, and bounds its content.
func Read(path string, maxBytes int64) (Content, error) {
	if maxBytes <= 0 || !secureParents(path) {
		return Content{}, errors.New("invalid protected file")
	}
	info, err := os.Lstat(path)
	if err != nil || !validPrivateInfo(info, maxBytes) {
		return Content{}, errors.New("invalid protected file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Content{}, errors.New("could not open protected file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !validPrivateInfo(opened, maxBytes) {
		return Content{}, errors.New("protected file changed")
	}
	identity, ok := fileIdentity(opened)
	if !ok {
		return Content{}, errors.New("protected file identity unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		clear(data)
		return Content{}, errors.New("could not read protected file")
	}
	return Content{Data: data, Identity: identity}, nil
}

// WriteExclusive writes the final path once, without replacing an existing
// file. The path is not a temporary directory and the file is synced before
// success is reported.
func WriteExclusive(path string, data []byte) (Identity, error) {
	if !secureParents(path) {
		return Identity{}, errors.New("invalid protected file path")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Identity{}, errors.New("protected file already exists or is unavailable")
	}
	identity := Identity{}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure && identity != (Identity{}) {
			_ = RemoveIfSame(path, identity)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return Identity{}, errors.New("could not inspect protected file")
	}
	identity, ok := fileIdentity(info)
	if !ok {
		return Identity{}, errors.New("protected file identity unavailable")
	}
	if err := file.Chmod(0600); err != nil {
		return Identity{}, errors.New("could not protect file")
	}
	if _, err := file.Write(data); err != nil {
		return Identity{}, errors.New("could not write protected file")
	}
	if err := file.Sync(); err != nil {
		return Identity{}, errors.New("could not sync protected file")
	}
	info, err = file.Stat()
	if err != nil {
		return Identity{}, errors.New("could not inspect protected file")
	}
	currentIdentity, ok := fileIdentity(info)
	if !ok || currentIdentity != identity || !validPrivateInfo(info, int64(len(data))) || info.Size() != int64(len(data)) {
		return Identity{}, errors.New("protected file identity unavailable")
	}
	if err := file.Close(); err != nil {
		return Identity{}, errors.New("could not close protected file")
	}
	if err := syncDirectoryPath(filepath.Dir(path)); err != nil {
		return Identity{}, errors.New("could not sync protected file directory")
	}
	removeOnFailure = false
	return identity, nil
}

// RemoveIfSame removes path only if it is still the exact regular file that
// was read or staged. Missing files are already cleaned up; replacement files
// are never removed.
func RemoveIfSame(path string, expected Identity) error {
	if expected == (Identity{}) || !secureParents(path) {
		return errors.New("protected file identity mismatch")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !validPrivateInfo(info, int64(info.Size())) {
		return errors.New("protected file identity mismatch")
	}
	actual, ok := fileIdentity(info)
	if !ok || actual != expected {
		return errors.New("protected file identity mismatch")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("could not remove protected file")
	}
	if err := syncDirectoryPath(filepath.Dir(path)); err != nil {
		return errors.New("could not sync protected file directory")
	}
	return nil
}

// RemoveUnpublished removes a reserved mode-0600 staging file. It is used only
// for a file that has not been published at its public path, so an orphan from
// an interrupted write can be cleaned without guessing at a replacement.
func RemoveUnpublished(path string) error {
	if !secureParents(path) {
		return errors.New("invalid protected file path")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !validPrivateInfo(info, int64(info.Size())) {
		return errors.New("invalid protected staging file")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("could not remove protected staging file")
	}
	if err := syncDirectoryPath(filepath.Dir(path)); err != nil {
		return errors.New("could not sync protected file directory")
	}
	return nil
}

func validPrivateInfo(info os.FileInfo, maxBytes int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 &&
		trustedOwner(info) && info.Size() >= 0 && info.Size() <= maxBytes
}

func secureParents(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	directories := make([]string, 0, 4)
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		directories = append(directories, directory)
		if filepath.Dir(directory) == directory {
			break
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		info, err := os.Lstat(directories[index])
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !trustedOwner(info) {
			return false
		}
		if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return false
		}
	}
	return true
}

func trustedOwner(info os.FileInfo) bool {
	if os.Geteuid() < 0 {
		return false
	}
	metadata := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !metadata.IsValid() || metadata.Kind() != reflect.Struct {
		return false
	}
	owner := metadata.FieldByName("Uid")
	return owner.IsValid() && owner.CanUint() && (owner.Uint() == 0 || owner.Uint() == uint64(os.Geteuid()))
}

func fileIdentity(info os.FileInfo) (Identity, bool) {
	metadata := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !metadata.IsValid() || metadata.Kind() != reflect.Struct {
		return Identity{}, false
	}
	device := metadata.FieldByName("Dev")
	inode := metadata.FieldByName("Ino")
	if !device.IsValid() || !device.CanUint() || !inode.IsValid() || !inode.CanUint() {
		return Identity{}, false
	}
	identity := Identity{Device: device.Uint(), Inode: inode.Uint()}
	return identity, identity != (Identity{})
}

var syncDirectoryPath = filesync.DirectoryPath
