package protectedfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadAndRemoveExactIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff")
	want := []byte("private handoff")
	identity, err := WriteExclusive(path, want)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Read(path, 1024)
	if err != nil || string(content.Data) != string(want) || content.Identity != identity {
		t.Fatalf("Read() = %#v, %v; want %#v and identity %#v", content, err, want, identity)
	}
	if _, err := WriteExclusive(path, []byte("replacement")); err == nil {
		t.Fatal("WriteExclusive replaced an existing handoff")
	}
	if err := RemoveIfSame(path, Identity{Device: identity.Device, Inode: identity.Inode + 1}); err == nil {
		t.Fatal("RemoveIfSame accepted a replacement identity")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement-protection check removed the file: %v", err)
	}
	if err := RemoveIfSame(path, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed handoff stat = %v", err)
	}
}

func TestReadRejectsUnsafeParentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 1024); err == nil {
		t.Fatal("Read accepted a non-private file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(parent, "secret")
	if err := os.WriteFile(unsafePath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(unsafePath, 1024); err == nil {
		t.Fatal("Read accepted a writable parent")
	}
}

func TestWriteExclusiveCleansFileWhenDirectorySyncFails(t *testing.T) {
	oldSync := syncDirectoryPath
	syncDirectoryPath = func(string) error { return errors.New("directory sync failed") }
	defer func() { syncDirectoryPath = oldSync }()

	path := filepath.Join(t.TempDir(), "handoff")
	if _, err := WriteExclusive(path, []byte("private handoff")); err == nil {
		t.Fatal("WriteExclusive succeeded after a directory sync failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed staging left a protected file: %v", err)
	}
}
