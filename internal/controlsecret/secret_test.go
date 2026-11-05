package controlsecret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const invalidConfiguration = "control secret configuration is invalid"

func TestLoadKeepsLegacyEnvironmentCompatibility(t *testing.T) {
	unsetEnv(t, FileEnv)
	for _, tc := range []struct {
		name, value, want string
	}{
		{name: "plain", value: "legacy-secret", want: "legacy-secret"},
		{name: "surrounding whitespace", value: "  legacy-secret  ", want: "legacy-secret"},
		{name: "multiline", value: "legacy\nsecret", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ValueEnv, tc.value)
			got, err := Load()
			if err != nil || got != tc.want {
				t.Fatalf("Load() = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestLoadReadsPrivateHexFile(t *testing.T) {
	if os.Geteuid() < 0 {
		t.Skip("file ownership metadata is unavailable")
	}
	path := filepath.Join(canonicalTempDir(t), "control-secret")
	secret := strings.Repeat("ab", 32)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ValueEnv, "")
	t.Setenv(FileEnv, path)

	got, err := Load()
	if err != nil || got != secret {
		t.Fatalf("Load() = %q, %v; want the file secret", got, err)
	}
}

func TestLoadRejectsUnsafeConfiguredFile(t *testing.T) {
	base := canonicalTempDir(t)
	secret := strings.Repeat("ab", 32)
	validPath := filepath.Join(base, "control-secret")
	if err := os.WriteFile(validPath, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resetValid := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(validPath, []byte(secret+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(validPath, 0600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		path  string
		setup func(t *testing.T)
	}{
		{name: "missing", path: filepath.Join(base, "missing")},
		{name: "empty path", path: ""},
		{name: "relative path", path: "control-secret"},
		{name: "unclean path", path: validPath + "/../control-secret"},
		{name: "public file", path: validPath, setup: func(t *testing.T) {
			if err := os.Chmod(validPath, 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", path: filepath.Join(base, "directory"), setup: func(t *testing.T) {
			if err := os.Mkdir(filepath.Join(base, "directory"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed", path: validPath, setup: func(t *testing.T) {
			if err := os.WriteFile(validPath, []byte(strings.Repeat("A", 64)), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetValid(t)
			if tc.setup != nil {
				tc.setup(t)
			}
			expectInvalid(t, tc.path, "")
		})
	}

	t.Run("conflicting legacy value", func(t *testing.T) {
		resetValid(t)
		expectInvalid(t, validPath, "legacy-secret")
	})

	t.Run("symlinked file", func(t *testing.T) {
		link := filepath.Join(base, "link")
		if err := os.Symlink(validPath, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		expectInvalid(t, link, "")
	})

	t.Run("symlinked parent", func(t *testing.T) {
		parent := filepath.Join(base, "parent")
		if err := os.Symlink(base, parent); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		expectInvalid(t, filepath.Join(parent, "control-secret"), "")
	})

	t.Run("writable parent", func(t *testing.T) {
		parent := filepath.Join(base, "writable")
		if err := os.Mkdir(parent, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0777); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "control-secret")
		if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		expectInvalid(t, path, "")
	})
}

func TestLoadRejectsUntrustedOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create an untrusted-owner fixture")
	}
	path := filepath.Join(canonicalTempDir(t), "control-secret")
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, -1); err != nil {
		t.Skipf("cannot change fixture owner: %v", err)
	}
	expectInvalid(t, path, "")
}

func expectInvalid(t *testing.T, path, value string) {
	t.Helper()
	t.Setenv(ValueEnv, value)
	t.Setenv(FileEnv, path)
	got, err := Load()
	if err == nil || err.Error() != invalidConfiguration || got != "" {
		t.Fatalf("Load() = %q, %v; want empty value and %q", got, err, invalidConfiguration)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	previous, wasSet := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	// macOS may expose the system temporary directory through /var -> /private/var;
	// the production loader intentionally rejects symlinked path components.
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return directory
}
