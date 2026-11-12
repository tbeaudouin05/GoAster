package localenv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestParse(t *testing.T) {
	values, err := Parse("SERVICE_REGION=west\nAPI_TOKEN=sample-value=with-equals\nEMPTY=")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SERVICE_REGION": "west",
		"API_TOKEN":      "sample-value=with-equals",
		"EMPTY":          "",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("parsed variable names do not match: got %v, want %v", mapKeys(values), mapKeys(want))
	}
	uploaded, err := ParseFileData([]byte("ADDED=one\nMODIFIED=two\n"))
	if err != nil {
		t.Fatal(err)
	}
	changes := Changes(map[string]string{"MODIFIED": "before", "UNRELATED": "keep"}, uploaded)
	if !reflect.DeepEqual(changes, ChangeSet{Added: []string{"ADDED"}, Modified: []string{"MODIFIED"}}) {
		t.Fatalf("changes = %+v, want only added and modified names", changes)
	}
	merged := Merge(map[string]string{"MODIFIED": "before", "UNRELATED": "keep"}, uploaded)
	if !reflect.DeepEqual(merged, map[string]string{"ADDED": "one", "MODIFIED": "two", "UNRELATED": "keep"}) {
		t.Fatalf("merge did not preserve unrelated settings: names = %v", mapKeys(merged))
	}
	if _, err := ParseFileData([]byte(strings.Repeat("A", MaxFileBytes+1))); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized uploaded data error = %v", err)
	}
}

func TestParseRejectsInvalidInputWithoutEchoingValues(t *testing.T) {
	for index, input := range []string{
		"missing-separator sample-value",
		"9INVALID=sample-value",
		"VALID-NAME=sample-value",
		"DUPLICATE=first\nDUPLICATE=second",
		"VALID=one\n\nNEXT=two",
	} {
		_, err := Parse(input)
		if err == nil {
			t.Errorf("invalid input case %d was accepted", index+1)
			continue
		}
		if strings.Contains(err.Error(), "sample-value") || strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
			t.Errorf("parse error included an entered value: %v", err)
		}
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.txt")
	if err := os.WriteFile(path, []byte("SERVICE_REGION=west\nAPI_TOKEN=sample-value=with-equals\n"), 0600); err != nil {
		t.Fatal(err)
	}

	values, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"SERVICE_REGION": "west", "API_TOKEN": "sample-value=with-equals"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("loaded variable names do not match: got %v, want %v", mapKeys(values), mapKeys(want))
	}
}

func TestLoadFileRejectsMalformedAndUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		prepare func(t *testing.T, path string)
		wantErr string
	}{
		{name: "missing", wantErr: "inspect environment file"},
		{name: "malformed", content: []byte("GOOD=secret-value\nbad line\n"), wantErr: "line 2 must be KEY=VALUE"},
		{name: "duplicate", content: []byte("DUPLICATE=first\nDUPLICATE=second\n"), wantErr: "duplicates key DUPLICATE"},
		{name: "invalid UTF-8", content: []byte("KEY=\xff\n"), wantErr: "valid UTF-8"},
		{name: "empty", wantErr: "contains no settings"},
		{name: "oversized", content: []byte(strings.Repeat("A", maxFileSize+1)), wantErr: "exceeds the 1 MiB limit"},
		{name: "unreadable", prepare: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("KEY=value\n"), 0000); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "not readable"},
		{name: "directory", prepare: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "must be a regular file"},
		{name: "symbolic link", prepare: func(t *testing.T, path string) {
			t.Helper()
			target := path + ".target"
			if err := os.WriteFile(target, []byte("KEY=value"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("cannot create symbolic link: %v", err)
			}
		}, wantErr: "must not be a symbolic link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.txt")
			if tc.prepare != nil {
				tc.prepare(t, path)
			} else if tc.name != "missing" {
				if err := os.WriteFile(path, tc.content, 0600); err != nil {
					t.Fatal(err)
				}
			}

			values, err := LoadFile(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadFile() = %v, %v; want an error containing %q", values, err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
				t.Errorf("load error exposed an entered value: %v", err)
			}
		})
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), directoryName)
	store := NewStoreAt(filepath.Join(directory, fileName))
	values := map[string]string{"SERVICE_REGION": "west", "API_TOKEN": "sample-value"}
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0700 {
		t.Errorf("directory permissions = %04o, want 0700", got)
	}

	fileInfo, err := os.Stat(filepath.Join(directory, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("file permissions = %04o, want 0600", got)
	}

	loaded := NewStoreAt(filepath.Join(directory, fileName))
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Values(), values) {
		t.Fatalf("round trip variable names do not match: got %v, want %v", mapKeys(loaded.Values()), mapKeys(values))
	}
}

func TestStoreRejectsSymlinkedLockWithoutChangingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	target := filepath.Join(filepath.Dir(path), "unrelated")
	if err := os.WriteFile(target, []byte("unrelated"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Skipf("cannot create symbolic link: %v", err)
	}

	err := NewStoreAt(path).Save(map[string]string{"KEY": "value"})
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("symlinked lock save error = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("unrelated target permissions = %04o, want 0644", info.Mode().Perm())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unrelated" {
		t.Fatalf("unrelated target contents changed: %q", data)
	}
}

func TestStoreRejectsNonRegularLockAndRestrictsRegularLock(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		if err := os.Mkdir(path+".lock", 0755); err != nil {
			t.Fatal(err)
		}
		err := NewStoreAt(path).Save(map[string]string{"KEY": "value"})
		if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("directory lock save error = %v", err)
		}
	})

	t.Run("regular", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		if err := os.WriteFile(path+".lock", nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := NewStoreAt(path).Save(map[string]string{"KEY": "value"}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("regular lock permissions = %04o, want 0600", info.Mode().Perm())
		}
	})
}

func TestStoreConcurrentUpsertIfUnchangedRejectsStaleReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	initialValues := map[string]string{"EXISTING": "keep"}
	initial := NewStoreAt(path)
	if err := initial.Save(initialValues); err != nil {
		t.Fatal(err)
	}
	staleReplacement := NewStoreAt(path)
	if err := staleReplacement.Load(); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type result struct {
		err error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, values := range []map[string]string{{"FIRST": "one"}, {"SECOND": "two"}} {
		wait.Add(1)
		go func(values map[string]string) {
			defer wait.Done()
			<-start
			_, err := NewStoreAt(path).UpsertIfUnchanged(Digest(initialValues), values)
			results <- result{err: err}
		}(values)
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	stale := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, ErrChanged):
			stale++
		default:
			t.Fatalf("concurrent upsert failed: %v", result.err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent upsert results = %d successes, %d stale; want one of each", successes, stale)
	}

	loaded := NewStoreAt(path)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	values := loaded.Values()
	wantFirst := map[string]string{"EXISTING": "keep", "FIRST": "one"}
	wantSecond := map[string]string{"EXISTING": "keep", "SECOND": "two"}
	if !reflect.DeepEqual(values, wantFirst) && !reflect.DeepEqual(values, wantSecond) {
		t.Fatalf("concurrent upserts produced unsafe settings: got %v, want %v or %v", values, wantFirst, wantSecond)
	}
	if err := staleReplacement.Save(map[string]string{"REPLACED": "value"}); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale replacement error = %v, want ErrChanged", err)
	}
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Values(), values) {
		t.Fatalf("stale replacement changed settings: got %v, want %v", loaded.Values(), values)
	}
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
