package chat

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestListDirRejectsFIFOWithoutBlocking(t *testing.T) {
	root := setupFileToolWorkspace(t)
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { result, _ := executeListDir(context.Background(), `{"path":"pipe"}`); done <- result }()
	select {
	case result := <-done:
		if !strings.Contains(result, "error") {
			t.Fatalf("result = %q", result)
		}
	case <-time.After(time.Second):
		t.Fatal("list_dir blocked on FIFO")
	}
}

func TestCheckedRegularOpenRejectsReplacementFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := root.Lstat("file")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		file, err := openFileToolCheckedRegular(before, func(flags int) (fs.File, error) {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			if err := unix.Mkfifo(path, 0600); err != nil {
				return nil, err
			}
			return root.OpenFile("file", flags, 0)
		})
		if file != nil {
			file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "file changed") {
			t.Fatalf("expected changed-file error, got %v", err)
		}
	case <-time.After(time.Second):
		// Release a blocking FIFO open before failing the test.
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			defer unix.Close(fd)
			<-done
		}
		t.Fatal("opening replacement FIFO blocked")
	}
}
