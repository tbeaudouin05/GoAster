package artefacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureRuntimeDirectoryCreatesPrivateFreshLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path, err := EnsureRuntimeDirectory("state", "sessions")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".goaster", "artefacts", "state", "sessions")
	if path != want {
		t.Fatalf("runtime directory = %q, want %q", path, want)
	}
	for _, directory := range []string{
		filepath.Join(home, ".goaster"),
		filepath.Join(home, ".goaster", "artefacts"),
		filepath.Join(home, ".goaster", "artefacts", "state"),
		path,
	} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("directory %s permissions = %04o, want 0700", directory, info.Mode().Perm())
		}
	}
}

func TestEnsureRuntimeDirectoryRejectsSymlinkedRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".goaster")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := EnsureRuntimeDirectory("config"); err == nil {
		t.Fatal("symlinked GoAster root was accepted")
	}
}
