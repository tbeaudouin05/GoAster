package localenv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	values, err := Parse("OVH_REGION=gva\nAPI_TOKEN=sample-value=with-equals\nEMPTY=")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OVH_REGION": "gva",
		"API_TOKEN":  "sample-value=with-equals",
		"EMPTY":      "",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("parsed variable names do not match: got %v, want %v", mapKeys(values), mapKeys(want))
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
	if err := os.WriteFile(path, []byte("OVH_REGION=gva\nAPI_TOKEN=sample-value=with-equals\n"), 0600); err != nil {
		t.Fatal(err)
	}

	values, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"OVH_REGION": "gva", "API_TOKEN": "sample-value=with-equals"}
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
	values := map[string]string{"OVH_REGION": "gva", "API_TOKEN": "sample-value"}
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

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
