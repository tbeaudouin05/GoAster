package artefacts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/skills"
)

func TestInstallBundledStartersOnce(t *testing.T) {
	if os.Getenv("GOASTER_STARTER_LOCK_TEST") == "holder" {
		root := os.Getenv("GOASTER_STARTER_LOCK_ROOT")
		first := true
		err := installStartersAt(context.Background(), root, func(dir string) error {
			if dir == root && first {
				first = false
				if err := os.WriteFile(filepath.Join(root, "test-ready"), []byte("ready"), 0600); err != nil {
					return err
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(filepath.Join(root, "test-release")); err == nil {
						return syncStarterDirectory(dir)
					}
					time.Sleep(10 * time.Millisecond)
				}
				return errors.New("timed out waiting to release installer")
			}
			return syncStarterDirectory(dir)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".goaster", "artefacts")
	files, err := starterFiles()
	if err != nil || len(files) == 0 {
		t.Fatalf("bundled starters: %v", err)
	}
	for name, contents := range files {
		for filename, want := range contents {
			got, err := os.ReadFile(filepath.Join(root, "skills", name, filename))
			if err != nil || string(got) != string(want) {
				t.Fatalf("installed %s/%s differs from bundled build: %v", name, filename, err)
			}
		}
	}
	if _, err := skills.Starters.ReadFile("ovh/AUTH.md"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "skills", "ovh", "SKILL.md")
	if err := os.WriteFile(path, []byte("custom workflow"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "custom workflow" {
		t.Fatalf("existing skill was updated: %q", got)
	}
	checkConcurrentStarterInstallation(t)
}

func checkConcurrentStarterInstallation(t interface {
	TempDir() string
	Cleanup(func())
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	child := exec.Command(os.Args[0], "-test.run=^TestInstallBundledStartersOnce$")
	child.Env = append(os.Environ(), "GOASTER_STARTER_LOCK_TEST=holder", "GOASTER_STARTER_LOCK_ROOT="+root)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "test-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first installer did not acquire the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := installStartersAt(ctx, root, syncStarterDirectory)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting installer did not honor cancellation: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- installStartersAt(ctx, root, syncStarterDirectory) }()
	select {
	case err := <-done:
		t.Fatalf("concurrent installer did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "test-release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent installer failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent installer did not finish")
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "ovh", "SKILL.md")); err != nil {
		t.Fatalf("starter installation missing after concurrent startups: %v", err)
	}
}

func TestStarterInstallationPreservesIncompleteState(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	failure := errors.New("directory sync failed")
	err := installStartersAt(context.Background(), root, func(dir string) error {
		if filepath.Base(dir) == "internet-fetch" {
			return failure
		}
		return syncStarterDirectory(dir)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("install error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".skills-installing")); err != nil {
		t.Fatalf("partial installation lock: %v", err)
	}
	if err := installStartersAt(context.Background(), root, syncStarterDirectory); err == nil || !strings.Contains(err.Error(), "may be incomplete") {
		t.Fatalf("partial installation was accepted: %v", err)
	}
}

func TestStarterInstallationLeavesExistingEmptyDirectoryUntouched(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	directory := filepath.Join(root, "skills")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := installStartersAt(context.Background(), root, syncStarterDirectory); err == nil || !strings.Contains(err.Error(), "no usable skills") {
		t.Fatalf("empty existing directory = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("existing directory was modified: %v, %v", entries, err)
	}
	checkInvalidExistingSkillText(t)
}

func checkInvalidExistingSkillText(t interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	for _, filename := range []string{"SKILL.md", "AUTH.md"} {
		for _, content := range []string{" \n\t", "invalid \xff"} {
			root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
			dir := filepath.Join(root, "skills", "custom")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, text := range map[string]string{
				"META.md":  "name: custom\ndescription: existing skill\n",
				"SKILL.md": "workflow", "AUTH.md": "authorization",
				filename: content,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := installStartersAt(context.Background(), root, syncStarterDirectory); err == nil {
				t.Fatal("installation accepted invalid existing skill text")
			}
			data, err := os.ReadFile(filepath.Join(dir, filename))
			if err != nil || string(data) != content {
				t.Fatalf("existing file changed: %q, %v", data, err)
			}
		}
	}
}

func TestStarterInstallationReportsFinalDirectorySyncFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	failure := errors.New("final sync failed")
	rootSyncs := 0
	err := installStartersAt(context.Background(), root, func(directory string) error {
		if directory == root {
			rootSyncs++
			if rootSyncs == 3 {
				return failure
			}
		}
		return syncStarterDirectory(directory)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("final sync error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "ovh", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".skills-installing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed installation lock: %v", err)
	}
}
