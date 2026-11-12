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
	"time"

	"goaster/internal/command"
)

func TestFullRuntimeSessionRequestIncludesToolsSkillsAndMemory(t *testing.T) {
	setupSkillFixture(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	memory, err := NewMemoryStoreWithError("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memory.MemoryPath(), []byte("durable fact"), memoryFileMode); err != nil {
		t.Fatal(err)
	}
	store := CredentialStore{Path: filepath.Join(home, "agent-auth.json")}
	if err := store.Save(Credential{AccessToken: "offline-test-access", AccountID: "offline-test-account", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		wantTools := map[string]bool{
			"web_search":                 false,
			"web_fetch":                  false,
			"shell_execute":              false,
			"load_skill":                 false,
			"telegram_command_reference": false,
			"read_file":                  false,
			"write_file":                 false,
			"edit_file":                  false,
			"append_file":                false,
			"list_dir":                   false,
			"background_command":         false,
		}
		for _, tool := range request.Tools {
			if _, ok := wantTools[tool.Name]; !ok {
				t.Errorf("unexpected runtime tool %q", tool.Name)
				continue
			}
			wantTools[tool.Name] = true
		}
		for name, found := range wantTools {
			if !found {
				t.Errorf("runtime request omitted tool %q; tools = %+v", name, request.Tools)
			}
		}
		for _, want := range []string{
			"name: fixture-skill",
			"User-managed long-term MEMORY.md",
			"durable fact",
			"Before a skill-dependent workflow, call load_skill",
		} {
			if !strings.Contains(request.Instructions, want) {
				t.Errorf("runtime instructions omitted %q: %q", want, request.Instructions)
			}
		}
		if len(request.Input) != 1 || len(request.Input[0].Content) != 1 || request.Input[0].Content[0].Text != "Reply with exactly FULL_RUNTIME_PROMPT_OK and no other text." {
			t.Errorf("runtime input = %+v", request.Input)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output_text":"FULL_RUNTIME_PROMPT_OK"}`)
	}))
	defer backend.Close()

	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	manager := command.NewBackgroundCommandManagerAt(filepath.Join(home, "background-commands.json"))
	defer manager.Close()
	if err := RegisterBackgroundCommandTool(provider, manager); err != nil {
		t.Fatal(err)
	}
	session := NewSessionWithMemory(Config{Model: "gpt-test"}, provider, memory)
	reply, err := session.Reply(context.Background(), "Reply with exactly FULL_RUNTIME_PROMPT_OK and no other text.")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "FULL_RUNTIME_PROMPT_OK" {
		t.Fatalf("reply = %q, want exact deterministic response", reply)
	}
}
