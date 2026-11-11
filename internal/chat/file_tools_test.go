package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/artefacts"
	"goaster/internal/filesync"
)

func TestCodexProviderRegistersFileTools(t *testing.T) {
	provider := NewCodexProvider(CredentialStore{})
	want := []string{"read_file", "write_file", "edit_file", "append_file", "list_dir"}
	definitions := provider.tools.definitions()
	seen := make(map[string]bool)
	for _, definition := range definitions {
		seen[definition.Name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("runtime definitions omitted %q: %+v", name, definitions)
		}
		if _, ok := provider.tools.handler(name); !ok {
			t.Errorf("runtime registry omitted handler %q", name)
		}
	}
}

func TestCodexProviderRunsFileToolThroughModelLoop(t *testing.T) {
	root := setupFileToolWorkspace(t)
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "access-token", AccountID: "account-123", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if requests.Load() == 0 {
			for _, name := range []string{"read_file", "write_file", "edit_file", "append_file", "list_dir"} {
				found := false
				for _, tool := range request.Tools {
					if tool.Name == name {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("initial model request omitted %q", name)
				}
			}
			writeCodexFunctionCall(t, w, "write", writeFileCodexTool.Name, `{"path":"runtime.txt","content":"runtime data"}`)
		} else if requests.Load() == 1 {
			if len(request.Input) < 3 || !strings.Contains(request.Input[len(request.Input)-1].Output, "File written") {
				t.Errorf("write tool output was not returned to the model: %+v", request.Input)
			}
			writeCodexFunctionCall(t, w, "read", readFileCodexTool.Name, `{"path":"runtime.txt"}`)
		} else {
			if len(request.Input) < 5 || !strings.Contains(request.Input[len(request.Input)-1].Output, "runtime data") {
				t.Errorf("read tool output was not returned to the model: %+v", request.Input)
			}
			_, _ = w.Write([]byte(`{"output_text":"file tools completed"}`))
		}
		requests.Add(1)
	}))
	defer backend.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	reply, err := provider.Reply(context.Background(), "gpt-test", "create then read the file")
	if err != nil || reply != "file tools completed" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("backend requests = %d, want 3", requests.Load())
	}
	if got, err := os.ReadFile(filepath.Join(root, "runtime.txt")); err != nil || string(got) != "runtime data" {
		t.Fatalf("runtime file = %q, error = %v", got, err)
	}
}

func TestFileToolsPerformWorkspaceOperations(t *testing.T) {
	root := setupFileToolWorkspace(t)
	write := executeWriteFile
	read := executeReadFile
	edit := executeEditFile
	appendFile := executeAppendFile
	list := executeListDir

	if result := runFileTool(t, write, `{"path":"notes/demo.txt","content":"alpha\nbeta"}`); !strings.Contains(result, "File written") {
		t.Fatalf("write result = %q", result)
	}
	demo := filepath.Join(root, "notes", "demo.txt")
	if got, err := os.ReadFile(demo); err != nil || string(got) != "alpha\nbeta" {
		t.Fatalf("written content = %q, error = %v", got, err)
	}
	if info, err := os.Stat(demo); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("written file mode = %v, error = %v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(demo)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("created parent mode = %v, error = %v", info, err)
	}

	if result := runFileTool(t, read, `{"path":"notes/demo.txt","offset":6,"length":4}`); !strings.Contains(result, "beta") || !strings.Contains(result, "bytes 6-9") {
		t.Fatalf("partial read result = %q", result)
	}
	if result := runFileTool(t, read, `{"path":"notes/demo.txt","offset":100}`); !strings.Contains(result, "no content at this offset") {
		t.Fatalf("past-end read result = %q", result)
	}

	if result := runFileTool(t, edit, `{"path":"notes/demo.txt","old_text":"beta","new_text":"gamma"}`); !strings.Contains(result, "Edited one occurrence") {
		t.Fatalf("edit result = %q", result)
	}
	if result := runFileTool(t, appendFile, `{"path":"notes/demo.txt","content":"\ndelta"}`); !strings.Contains(result, "Appended") {
		t.Fatalf("append result = %q", result)
	}
	if got, err := os.ReadFile(demo); err != nil || string(got) != "alpha\ngamma\ndelta" {
		t.Fatalf("edited and appended content = %q, error = %v", got, err)
	}

	if result := runFileTool(t, write, `{"path":"notes/demo.txt","content":"discarded"}`); !strings.Contains(result, "already exists") {
		t.Fatalf("exclusive write result = %q", result)
	}
	if result := runFileTool(t, write, `{"path":"notes/demo.txt","content":"replacement","overwrite":true}`); !strings.Contains(result, "File written") {
		t.Fatalf("overwrite result = %q", result)
	}
	if got, err := os.ReadFile(demo); err != nil || string(got) != "replacement" {
		t.Fatalf("overwritten content = %q, error = %v", got, err)
	}

	if result := runFileTool(t, appendFile, `{"path":"empty.txt","content":""}`); !strings.Contains(result, "Appended") {
		t.Fatalf("append-create result = %q", result)
	}
	if result := runFileTool(t, write, `{"path":"empty.txt","content":""}`); !strings.Contains(result, "already exists") {
		t.Fatalf("empty exclusive write result = %q", result)
	}
	if result := runFileTool(t, list, `{"path":"."}`); !strings.Contains(result, "DIR:  notes") || !strings.Contains(result, "FILE: empty.txt") {
		t.Fatalf("root listing = %q", result)
	}
	if result := runFileTool(t, list, `{"path":"notes"}`); !strings.Contains(result, "FILE: demo.txt") {
		t.Fatalf("nested listing = %q", result)
	}
}

func TestFileToolsRejectUnsafeAndInvalidOperations(t *testing.T) {
	root := setupFileToolWorkspace(t)
	write := executeWriteFile
	read := executeReadFile
	edit := executeEditFile
	appendFile := executeAppendFile
	list := executeListDir

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	escape := `{"path":"../outside.txt"}`
	if result := runFileTool(t, read, escape); !strings.Contains(result, "escapes the GoAster artefact workspace") {
		t.Fatalf("escape read result = %q", result)
	}
	absoluteOutside, err := json.Marshal(map[string]any{"path": outside, "content": "changed", "overwrite": true})
	if err != nil {
		t.Fatal(err)
	}
	if result := runFileTool(t, write, string(absoluteOutside)); !strings.Contains(result, "escapes the GoAster artefact workspace") {
		t.Fatalf("absolute escape write result = %q", result)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
		t.Fatalf("outside file = %q, error = %v", got, err)
	}

	if result := runFileTool(t, read, `{"path":"missing.txt"}`); !strings.Contains(result, "file not found") {
		t.Fatalf("missing read result = %q", result)
	}
	if result := runFileTool(t, list, `{"path":"missing"}`); !strings.Contains(result, "file not found") {
		t.Fatalf("missing list result = %q", result)
	}
	if result := runFileTool(t, read, `{"path":"."}`); !strings.Contains(result, "not a regular file") {
		t.Fatalf("directory read result = %q", result)
	}
	if result := runFileTool(t, read, `{"path":"missing","unknown":true}`); !strings.Contains(result, "unknown field") {
		t.Fatalf("unknown argument result = %q", result)
	}
	if result := runFileTool(t, read, `{"path":"missing","offset":-1}`); !strings.Contains(result, "offset must be >= 0") {
		t.Fatalf("negative offset result = %q", result)
	}
	if result := runFileTool(t, read, `{not-json`); !strings.Contains(result, "not valid JSON") {
		t.Fatalf("malformed argument result = %q", result)
	}

	if result := runFileTool(t, write, `{"path":"ambiguous.txt","content":"same same"}`); !strings.Contains(result, "File written") {
		t.Fatalf("ambiguous fixture write result = %q", result)
	}
	if result := runFileTool(t, edit, `{"path":"ambiguous.txt","old_text":"same","new_text":"new"}`); !strings.Contains(result, "appears 2 times") {
		t.Fatalf("ambiguous edit result = %q", result)
	}
	if got, err := os.ReadFile(filepath.Join(root, "ambiguous.txt")); err != nil || string(got) != "same same" {
		t.Fatalf("ambiguous file = %q, error = %v", got, err)
	}
	if result := runFileTool(t, edit, `{"path":"ambiguous.txt","old_text":"absent","new_text":"new"}`); !strings.Contains(result, "not found") {
		t.Fatalf("missing edit result = %q", result)
	}
	if result := runFileTool(t, appendFile, `{"path":"ambiguous.txt","content":"!"}`); !strings.Contains(result, "Appended") {
		t.Fatalf("valid append result = %q", result)
	}

	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if result := runFileTool(t, appendFile, `{"path":"directory","content":"x"}`); !strings.Contains(result, "not a regular file") {
		t.Fatalf("directory append result = %q", result)
	}

	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err == nil {
		if result := runFileTool(t, read, `{"path":"linked.txt"}`); !strings.Contains(result, "symbolic link") {
			t.Fatalf("symlink read result = %q", result)
		}
		if result := runFileTool(t, write, `{"path":"linked.txt","content":"changed","overwrite":true}`); !strings.Contains(result, "symbolic link") {
			t.Fatalf("symlink write result = %q", result)
		}
		if result := runFileTool(t, list, `{"path":"linked.txt"}`); !strings.Contains(result, "symbolic link") {
			t.Fatalf("symlink list result = %q", result)
		}
		if result := runFileTool(t, list, `{"path":"."}`); !strings.Contains(result, "LINK: linked.txt") {
			t.Fatalf("symlink entry listing = %q", result)
		}
	}
}

func setupFileToolWorkspace(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root, err := artefacts.EnsureRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runFileTool(t testing.TB, handler ToolHandler, arguments string) string {
	t.Helper()
	result, err := handler(context.Background(), arguments)
	if err != nil {
		t.Fatalf("file tool returned handler error: %v", err)
	}
	return result
}

func TestFileToolRegressions(t *testing.T) {
	root := setupFileToolWorkspace(t)
	if result := runFileTool(t, executeWriteFile, `{"path":"new.txt","content":"hello","overwrite":true}`); !strings.Contains(result, "File written") {
		t.Fatal(result)
	}
	path := filepath.Join(root, "new.txt")
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if result := runFileTool(t, executeWriteFile, `{"path":"new.txt","content":"ababa","overwrite":true}`); !strings.Contains(result, "File written") {
		t.Fatal(result)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	if result := runFileTool(t, executeEditFile, `{"path":"new.txt","old_text":"aba","new_text":"x"}`); !strings.Contains(result, "appears 2 times") {
		t.Fatal(result)
	}
	if result := runFileTool(t, executeAppendFile, `{"path":"new.txt","content":"!"}`); !strings.Contains(result, "Appended") {
		t.Fatal(result)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(root, "utf8.txt"), []byte("a€b"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := runFileTool(t, executeReadFile, `{"path":"utf8.txt","length":2}`); !strings.Contains(result, "offset=1") {
		t.Fatal(result)
	}
	if result := runFileTool(t, executeReadFile, `{"path":"utf8.txt","offset":1,"length":3}`); !strings.Contains(result, "€") || !strings.Contains(result, "offset=4") {
		t.Fatal(result)
	}
	if result := runFileTool(t, executeReadFile, `{"path":"utf8.txt","offset":2}`); !strings.Contains(result, "character boundary") {
		t.Fatal(result)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.txt"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if result := runFileTool(t, executeReadFile, `{"path":"bad.txt"}`); !strings.Contains(result, "invalid UTF-8") {
		t.Fatal(result)
	}
}

func TestFileToolConcurrentAppends(t *testing.T) {
	root := setupFileToolWorkspace(t)
	var group sync.WaitGroup
	for i := 0; i < 40; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := executeAppendFile(context.Background(), `{"path":"parallel.txt","content":"x"}`)
			if err != nil || !strings.Contains(result, "Appended") {
				t.Errorf("append: %q %v", result, err)
			}
		}()
	}
	group.Wait()
	data, err := os.ReadFile(filepath.Join(root, "parallel.txt"))
	if err != nil || string(data) != strings.Repeat("x", 40) {
		t.Fatalf("content: %q %v", data, err)
	}
}

func TestFileToolListingBound(t *testing.T) {
	root := setupFileToolWorkspace(t)
	for i := 0; i < fileToolMaxEntries+1; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if result := runFileTool(t, executeListDir, `{}`); !strings.Contains(result, "TRUNCATED") {
		t.Fatal("listing should be truncated")
	}
}

func TestFileToolRejectsParentSymlinks(t *testing.T) {
	root := setupFileToolWorkspace(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, call := range []struct {
		handler ToolHandler
		args    string
	}{
		{executeWriteFile, `{"path":"link/file","content":"x"}`},
		{executeAppendFile, `{"path":"link/file","content":"x"}`},
		{executeReadFile, `{"path":"link/file"}`},
		{executeListDir, `{"path":"link"}`},
	} {
		if result := runFileTool(t, call.handler, call.args); !strings.Contains(result, "symbolic link") {
			t.Errorf("%s: %s", call.args, result)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); !os.IsNotExist(err) {
		t.Fatalf("outside file was created: %v", err)
	}
}

func TestFileToolDetectsStaleUpdate(t *testing.T) {
	root := setupFileToolWorkspace(t)
	path := filepath.Join(root, "stale.txt")
	if err := os.WriteFile(path, []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceFileTool(context.Background(), path, []byte("replacement"), true, []byte("older")); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "newer" {
		t.Fatalf("content %q: %v", data, err)
	}
}

func TestFileToolCommitCancellation(t *testing.T) {
	root := setupFileToolWorkspace(t)
	path := filepath.Join(root, "existing")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := createFileToolExclusive(ctx, filepath.Join(root, "new"), []byte("new")); !errors.Is(err, context.Canceled) {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("created despite cancellation: %v", err)
	}
	if err := atomicReplaceFileTool(ctx, path, []byte("new"), true, []byte("original")); !errors.Is(err, context.Canceled) {
		t.Fatalf("replace: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "original" {
		t.Fatalf("content %q: %v", got, err)
	}
}

func TestFileToolPostCommitSyncFailure(t *testing.T) {
	root := setupFileToolWorkspace(t)
	original := syncFileToolParent
	syncFileToolParent = func(func() (filesync.Directory, error)) error { return errors.New("sync failed") }
	defer func() { syncFileToolParent = original }()
	for _, tc := range []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{"create", func() (string, error) {
			return executeWriteFile(context.Background(), `{"path":"new","content":"created"}`)
		}, "created"},
		{"replace", func() (string, error) {
			return executeAppendFile(context.Background(), `{"path":"new","content":" plus"}`)
		}, "created plus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.run()
			if err != nil || !strings.Contains(result, "committed") || !strings.Contains(result, "durability is uncertain") {
				t.Fatalf("result %q: %v", result, err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "new")); err != nil || string(got) != tc.want {
				t.Fatalf("content %q: %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fileToolFailure(ctx, "write_file", fileToolCommittedError{errors.New("sync failed")})
	if err != nil || !strings.Contains(result, "committed") {
		t.Fatalf("cancel after commit: %q %v", result, err)
	}
}
