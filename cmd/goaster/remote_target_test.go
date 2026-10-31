package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prepareRemoteTargetFiles(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	sshDirectory := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(sshDirectory, "goaster-ovh")
	if err := os.WriteFile(identityFile, []byte("TEST-IDENTITY-CONTENT-MUST-NOT-BE-SAVED"), 0600); err != nil {
		t.Fatal(err)
	}
	knownHostsFile := filepath.Join(sshDirectory, "known_hosts")
	if err := os.WriteFile(knownHostsFile, []byte("host.example ssh-ed25519 TEST-HOST-KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, identityFile, knownHostsFile
}

func TestSaveAndLoadRemoteTargetRecord(t *testing.T) {
	home, identityFile, _ := prepareRemoteTargetFiles(t)
	record := remoteTargetRecord{
		Target:     "root@host.example",
		Port:       2222,
		KnownHosts: "~/.ssh/known_hosts",
	}
	if err := saveRemoteTargetRecord(home, identityFile, record); err != nil {
		t.Fatalf("save remote target: %v", err)
	}
	loaded, err := loadRemoteTargetRecord(home, identityFile)
	if err != nil {
		t.Fatalf("load remote target: %v", err)
	}
	if loaded != record {
		t.Fatalf("loaded record = %+v, want %+v", loaded, record)
	}

	path, eligible, err := remoteTargetRecordPath(home, identityFile)
	if err != nil || !eligible {
		t.Fatalf("record path eligible = %v, error = %v", eligible, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "{\"target\":\"root@host.example\",\"port\":2222,\"known_hosts\":\"~/.ssh/known_hosts\"}\n" {
		t.Fatalf("record content = %q", content)
	}
	if strings.Contains(string(content), "TEST-IDENTITY-CONTENT-MUST-NOT-BE-SAVED") {
		t.Fatal("record contains identity-file data")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("record mode = %04o, want 0600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0700 {
		t.Fatalf(".ssh mode = %04o, want 0700", directoryInfo.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".goaster-target-") {
			t.Fatalf("temporary file remains after save: %s", entry.Name())
		}
	}
}

func TestRemoteTargetRecordValidation(t *testing.T) {
	home, _, knownHosts := prepareRemoteTargetFiles(t)
	for _, test := range []struct {
		name   string
		record remoteTargetRecord
	}{
		{name: "empty target", record: remoteTargetRecord{Port: 22}},
		{name: "whitespace target", record: remoteTargetRecord{Target: "root@host example", Port: 22}},
		{name: "control target", record: remoteTargetRecord{Target: "root@host\nexample", Port: 22}},
		{name: "option target", record: remoteTargetRecord{Target: "-oProxyCommand=bad", Port: 22}},
		{name: "zero port", record: remoteTargetRecord{Target: "root@host.example", Port: 0}},
		{name: "large port", record: remoteTargetRecord{Target: "root@host.example", Port: 65536}},
		{name: "relative known_hosts", record: remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: "known_hosts"}},
		{name: "outside known_hosts", record: remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: filepath.Join(filepath.Dir(home), "known_hosts")}},
		{name: "valid", record: remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: knownHosts}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRemoteTargetRecord(test.record, home)
			if test.name == "valid" {
				if err != nil {
					t.Fatalf("valid record rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid record was accepted")
			}
		})
	}

	symlink := filepath.Join(home, ".ssh", "linked_known_hosts")
	if err := os.Symlink(knownHosts, symlink); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := validateRemoteTargetRecord(remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: symlink}, home); err == nil {
		t.Fatal("symlink known_hosts path was accepted")
	}
}

func TestLoadRemoteTargetRecordRejectsMalformedSymlinkAndUnknownFields(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "malformed JSON", content: `{"target":`},
		{name: "unknown field", content: `{"target":"root@host.example","port":22,"secret":"value"}`},
		{name: "duplicate field", content: `{"target":"root@host.example","target":"root@other.example","port":22}`},
		{name: "trailing data", content: `{"target":"root@host.example","port":22} {}`},
		{name: "missing port", content: `{"target":"root@host.example"}`},
		{name: "non-integer port", content: `{"target":"root@host.example","port":22.0}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, identityFile, _ := prepareRemoteTargetFiles(t)
			if err := os.Chmod(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			path := identityFile + remoteTargetRecordSuffix
			if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRemoteTargetRecord(home, identityFile); err == nil {
				t.Fatal("invalid connection record was accepted")
			}
		})
	}

	t.Run("record symlink", func(t *testing.T) {
		home, identityFile, knownHosts := prepareRemoteTargetFiles(t)
		if err := os.Chmod(filepath.Join(home, ".ssh"), 0700); err != nil {
			t.Fatal(err)
		}
		recordPath := identityFile + remoteTargetRecordSuffix
		elsewhere := filepath.Join(home, "record.json")
		if err := os.WriteFile(elsewhere, []byte(`{"target":"root@host.example","port":22}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, recordPath); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
		if _, err := loadRemoteTargetRecord(home, identityFile); err == nil {
			t.Fatal("symbolic-link connection record was accepted")
		}
		if err := saveRemoteTargetRecord(home, identityFile, remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: knownHosts}); err == nil {
			t.Fatal("save replaced a symbolic-link connection record")
		}
	})
}

func TestMalformedSavedTargetIsRejectedBeforeSSH(t *testing.T) {
	home, identityFile, _ := prepareRemoteTargetFiles(t)
	if err := os.Chmod(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityFile+remoteTargetRecordSuffix, []byte(`{"target":"root@host.example","port":22,"extra":"value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, found, err := checkSavedRemoteTarget(context.Background(), home, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		calls++
		return nil
	}); err == nil || found {
		t.Fatalf("malformed saved target found = %v, error = %v", found, err)
	}
	if calls != 0 {
		t.Fatalf("SSH calls = %d, want no connection for malformed record", calls)
	}
}

func TestSavedRemoteTargetReusePerformsReadOnlyStrictHostKeyCheck(t *testing.T) {
	home, identityFile, knownHosts := prepareRemoteTargetFiles(t)
	record := remoteTargetRecord{Target: "root@host.example", Port: 2222, KnownHosts: "~/.ssh/known_hosts"}
	if err := saveRemoteTargetRecord(home, identityFile, record); err != nil {
		t.Fatal(err)
	}
	path := identityFile + remoteTargetRecordSuffix
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var captured []string
	checked, found, err := checkSavedRemoteTarget(context.Background(), home, func(ctx context.Context, arguments []string, input io.Reader, output, errorOutput io.Writer) error {
		calls++
		captured = append([]string(nil), arguments...)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("read-only check has no timeout")
		}
		if output != io.Discard || errorOutput != io.Discard {
			t.Error("remote check output is not discarded")
		}
		if remaining, readErr := io.ReadAll(input); readErr != nil || len(remaining) != 0 {
			t.Errorf("read-only check input = %q, error = %v", remaining, readErr)
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("saved target check found = %v, error = %v", found, err)
	}
	if checked.Record != record || checked.IdentityFile != identityFile {
		t.Fatalf("reused target = %+v, want record %+v and key %q", checked, record, identityFile)
	}
	if calls != 1 {
		t.Fatalf("SSH calls = %d, want one read-only check", calls)
	}
	joined := strings.Join(captured, " ")
	for _, want := range []string{
		"-T",
		"-i " + identityFile,
		"-p 2222",
		"BatchMode=yes",
		"StrictHostKeyChecking=yes",
		"UpdateHostKeys=no",
		"UserKnownHostsFile=" + knownHosts,
		"root@host.example",
		remoteHealthCommand,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("read-only SSH arguments missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, remoteLoginCommand) {
		t.Fatalf("read-only SSH arguments included interactive login: %s", joined)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("read-only reuse changed the saved connection record")
	}

	failedCalls := 0
	_, found, err = checkSavedRemoteTarget(context.Background(), home, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		failedCalls++
		return errors.New("private remote output")
	})
	if err == nil || !found || failedCalls != 1 {
		t.Fatalf("failed read-only check found = %v, calls = %d, error = %v", found, failedCalls, err)
	}
	if strings.Contains(err.Error(), "private remote output") {
		t.Fatalf("read-only check exposed remote output: %v", err)
	}
}

func TestRemoteLoginFailureDoesNotCreateConnectionRecord(t *testing.T) {
	for _, hasExistingRecord := range []bool{false, true} {
		name := "does not create a record"
		if hasExistingRecord {
			name = "preserves the prior record"
		}
		t.Run(name, func(t *testing.T) {
			home, identityFile, _ := prepareRemoteTargetFiles(t)
			t.Setenv("HOME", home)
			oldRecord := remoteTargetRecord{Target: "root@old.example", Port: 2222, KnownHosts: "~/.ssh/known_hosts"}
			if hasExistingRecord {
				if err := saveRemoteTargetRecord(home, identityFile, oldRecord); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			options := remoteLoginOptions{Target: "root@new.example", IdentityFile: identityFile, Port: 22}
			err := runRemoteLoginWith(context.Background(), options, strings.NewReader(""), io.Discard, io.Discard, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				calls++
				if calls == 2 {
					return errors.New("exit status 7")
				}
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "remote agent health check failed") {
				t.Fatalf("remote login error = %v, want health-check failure", err)
			}
			if calls != 2 {
				t.Fatalf("SSH calls = %d, want login and health check", calls)
			}
			if hasExistingRecord {
				loaded, err := loadRemoteTargetRecord(home, identityFile)
				if err != nil || loaded != oldRecord {
					t.Fatalf("prior connection record = %+v, error = %v", loaded, err)
				}
			} else if _, err := os.Lstat(identityFile + remoteTargetRecordSuffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("connection record exists after failed verification: %v", err)
			}
		})
	}
}

func TestRemoteTargetSaveFailurePreservesExistingRecord(t *testing.T) {
	home, identityFile, knownHosts := prepareRemoteTargetFiles(t)
	path := identityFile + remoteTargetRecordSuffix
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRemoteTargetRecord(home, identityFile, remoteTargetRecord{Target: "root@host.example", Port: 22, KnownHosts: knownHosts}); err == nil {
		t.Fatal("save replaced a non-file connection record")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("failed save changed the existing connection record path")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".goaster-target-") {
			t.Fatalf("temporary record remains after failed save: %s", entry.Name())
		}
	}
}
