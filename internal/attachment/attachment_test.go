package attachment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilenameRemovesPathAndUnsafeCharacters(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "../../secret.txt", want: "secret.txt"},
		{input: `..\secret name?.txt`, want: "secret_name_.txt"},
		{input: "...", want: "attachment"},
		{input: " report.txt ", want: "report.txt"},
	} {
		if got := SanitizeFilename(test.input); got != test.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", test.input, got, test.want)
		}
		if strings.ContainsAny(SanitizeFilename(test.input), "/\\\x00\r\n") {
			t.Errorf("sanitized filename %q still contains path or control characters", SanitizeFilename(test.input))
		}
	}
}

func TestMetadataValidateRequiresPrivatePerMessageFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "report.txt")
	data := []byte("test")
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "document", Filename: "report.txt", Path: pathName, MIMEType: "text/plain", Size: int64(len(data))}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	for name, invalid := range map[string]Metadata{
		"traversal filename":  {Type: "document", Filename: "../report.txt", Path: pathName, Size: int64(len(data))},
		"normalized path":     {Type: "document", Filename: "report.txt", Path: directory + string(os.PathSeparator) + "." + string(os.PathSeparator) + "report.txt", Size: int64(len(data))},
		"size mismatch":       {Type: "document", Filename: "report.txt", Path: pathName, Size: 99},
		"unsupported type":    {Type: "video", Filename: "report.txt", Path: pathName, Size: int64(len(data))},
		"world readable file": {Type: "document", Filename: "report.txt", Path: pathName, Size: int64(len(data))},
	} {
		if name == "world readable file" {
			if err := os.Chmod(pathName, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := invalid.Validate(); err == nil {
			t.Errorf("%s metadata was accepted", name)
		}
		if name == "world readable file" {
			if err := os.Chmod(pathName, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMetadataValidateRejectsOversizedAndSymlinkFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "large.bin")
	data := []byte("large")
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "document", Filename: "large.bin", Path: pathName, Size: MaxBytes + 1}
	if err := metadata.Validate(); err == nil {
		t.Fatal("oversized metadata was accepted")
	}

	symlink := filepath.Join(directory, "link.bin")
	if err := os.Symlink(pathName, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	metadata = Metadata{Type: "document", Filename: "link.bin", Path: symlink, Size: int64(len(data))}
	if err := metadata.Validate(); err == nil {
		t.Fatal("symlink metadata was accepted")
	}
}

func TestCleanupTemporaryDirIsScoped(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupTemporaryDir(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary directory still exists, stat error = %v", err)
	}
	if err := CleanupTemporaryDir(os.TempDir()); err == nil {
		t.Fatal("accepted the process temporary directory as a cleanup target")
	}
}

func TestAddReferencesKeepsMessageAndIncludesUsefulMetadata(t *testing.T) {
	metadata := Metadata{Type: "image", Filename: "photo.jpg", Path: "/tmp/goaster-attachment/photo.jpg", MIMEType: "image/jpeg", Size: 123}
	got := AddReferences("look at this", []Metadata{metadata})
	for _, want := range []string{"look at this", "photo.jpg", "/tmp/goaster-attachment/photo.jpg", "image/jpeg", "123"} {
		if !strings.Contains(got, want) {
			t.Errorf("attachment reference %q missing from %q", want, got)
		}
	}
}
