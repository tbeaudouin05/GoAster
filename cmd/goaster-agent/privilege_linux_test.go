//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"goaster/internal/attachment"
	"goaster/internal/command"
)

// The root test process stands in for the adapter. The child runs the actual
// handler as an unprivileged user with a separate private temporary root.
func TestRootAdapterCanSendBytesToUnprivilegedAgent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to start an unprivileged child")
	}
	root, err := os.MkdirTemp("", "goaster-privilege-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	agentTemp := filepath.Join(root, "agent")
	adapterTemp := filepath.Join(root, "adapter")
	for _, dir := range []string{agentTemp, adapterTemp} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const agentUID, agentGID = 65534, 65534
	if err := os.Chown(agentTemp, agentUID, agentGID); err != nil {
		t.Fatal(err)
	}
	data := []byte("private document from the root adapter")
	body, err := json.Marshal(struct {
		ChatID      int64                 `json:"chat_id"`
		Attachments []attachment.Incoming `json:"attachments"`
	}{1, []attachment.Incoming{{Type: "document", Filename: "notes.txt", Size: int64(len(data)), Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/proc/self/exe", "-test.run=^TestUnprivilegedAgentHelper$")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: agentUID, Gid: agentGID}}
	child.Env = append(os.Environ(), "TMPDIR="+agentTemp, "GOASTER_PRIVILEGE_TEST=1")
	child.Stdin = bytes.NewReader(body)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged agent failed: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(agentTemp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("agent temporary directory not cleaned: %v, %v", entries, err)
	}
}

func TestUnprivilegedAgentHelper(t *testing.T) {
	if os.Getenv("GOASTER_PRIVILEGE_TEST") != "1" {
		t.Skip("child helper")
	}
	if os.Geteuid() != 65534 {
		t.Fatalf("agent has unexpected uid %d", os.Geteuid())
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	var pathName string
	provider := inspectingProvider(func(ctx context.Context, message string) (string, error) {
		pathName = attachmentPath(message)
		if filepath.Dir(filepath.Dir(pathName)) != os.TempDir() {
			return "", fmt.Errorf("attachment is not in agent's temporary directory")
		}
		for _, path := range []string{pathName, filepath.Dir(pathName)} {
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 65534 {
				return "", fmt.Errorf("attachment is not agent-owned")
			}
		}
		cmd := "cat -- '" + strings.ReplaceAll(pathName, "'", "'\\''") + "'"
		result, err := command.Execute(ctx, command.Request{Command: cmd})
		if err != nil || result.ExitCode != 0 || result.Stdout != "private document from the root adapter" {
			return "", fmt.Errorf("shell failed to read attachment: %+v %v", result, err)
		}
		return "ok", nil
	})
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/chat", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(provider, readyAuth).ServeHTTP(w, r)
	if w.Code != http.StatusOK || pathName == "" {
		t.Fatalf("agent response = %d %q, attachment = %q", w.Code, w.Body.String(), pathName)
	}
	if _, err := os.Stat(filepath.Dir(pathName)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory remains after the turn: %v", err)
	}
}
