package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectMemoryMustBeSelectedBeforeTaskActions(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	memory := NewMemoryStore(workspace)
	step := 0
	actions := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		step++
		switch step {
		case 1:
			if !strings.Contains(request.Instructions, "No project memory is selected") || !strings.Contains(request.Instructions, "project_memory") {
				t.Errorf("initial request omitted project-memory requirement: %s", request.Instructions)
			}
			writeCodexFunctionCall(t, w, "blocked-action", shellExecuteCodexTool.Name, `{"command":"echo should-not-run"}`)
		case 2:
			payload := fmt.Sprint(request.Input)
			if !strings.Contains(payload, "select or create a relevant project memory") {
				t.Errorf("blocked action result missing from follow-up: %s", payload)
			}
			writeCodexFunctionCall(t, w, "create-memory", projectMemoryCodexTool.Name, `{"action":"create","name":"demo","content":"# Demo\n\nKeep the verified constraint."}`)
		case 3:
			if !strings.Contains(request.Instructions, "A project memory is selected: demo") || !strings.Contains(fmt.Sprint(request.Input), "Keep the verified constraint") {
				t.Errorf("selected project memory was not available: instructions=%s input=%v", request.Instructions, request.Input)
			}
			fmt.Fprint(w, `{"output_text":"completed"}`)
		default:
			t.Errorf("unexpected backend request %d", step)
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	provider.tools.byName[shellExecuteCodexTool.Name] = func(context.Context, string) (string, error) {
		actions++
		return "executed", nil
	}
	session, err := NewSessionWithStoreAndMemory(
		Config{Model: "gpt-test"},
		provider,
		NewSessionStoreAt(filepath.Join(t.TempDir(), "session.json")),
		memory,
	)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := session.Reply(context.Background(), "perform the task")
	if err != nil || reply != "completed" {
		t.Fatalf("project-memory task reply = %q, error = %v", reply, err)
	}
	if actions != 0 {
		t.Fatalf("task action ran before project-memory selection: %d", actions)
	}
	if session.selectedProjectMemory != "demo" {
		t.Fatalf("selected project memory = %q, want demo", session.selectedProjectMemory)
	}
	content, err := ReadProjectMemory(workspace, "demo")
	if err != nil || !strings.Contains(content, "Keep the verified constraint") {
		t.Fatalf("created project memory = %q, error = %v", content, err)
	}
	if _, err := os.Stat(ProjectMemoryPath(workspace, "demo")); err != nil {
		t.Fatal(err)
	}
}
