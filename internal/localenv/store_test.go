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
