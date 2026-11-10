package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/command"
)

func TestBackgroundCommandToolHelper(t *testing.T) {
	marker := false
	for _, argument := range os.Args {
		if argument == "--goaster-background-tool-helper" {
			marker = true
			break
		}
	}
	if !marker {
		return
	}
	fmt.Print("tool-ready")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return
	}
	fmt.Printf("tool-received:%s", strings.TrimSuffix(line, "\n"))
}

type backgroundCommandToolRegistrar struct {
	register func(Tool) error
}

func (registrar backgroundCommandToolRegistrar) RegisterTool(tool Tool) error {
	return registrar.register(tool)
}

func exerciseBackgroundCommandTool(t testing.TB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{{
		Name: "preview", Description: "Local preview", Executable: executable, Args: []string{"-test.run=^$"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	registered := Tool{}
	registrar := backgroundCommandToolRegistrar{register: func(tool Tool) error {
		registered = tool
		return nil
	}}
	if err := RegisterBackgroundCommandTool(registrar, manager); err != nil {
		t.Fatalf("RegisterBackgroundCommandTool() error = %v", err)
	}
	if registered.Name != backgroundCommandToolName || registered.Handler == nil || registered.Parameters["additionalProperties"] != false {
		t.Fatalf("registered tool = %+v", registered)
	}
	if !strings.Contains(registered.Description, "not secret-safe") ||
		!strings.Contains(registered.Description, "never use input") ||
		!strings.Contains(registered.Description, "PTY echo") {
		t.Fatalf("background tool description omits the non-secret boundary: %q", registered.Description)
	}
	properties, ok := registered.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool properties = %#v", registered.Parameters["properties"])
	}
	for _, field := range []string{"command", "executable", "args", "cwd", "shell"} {
		if _, exists := properties[field]; exists {
			t.Fatalf("background tool accepts %s: %+v", field, properties)
		}
	}
	listed, err := registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"name":"preview"`) || !strings.Contains(listed, `"description":"Local preview"`) || strings.Contains(listed, executable) || strings.Contains(listed, "-test.run") {
		t.Fatalf("list result = %q, error = %v", listed, err)
	}
	for _, arguments := range []string{
		`{"action":"start","name":"preview","command":"echo injected"}`,
		`{"action":"start","name":"preview","executable":"/bin/echo"}`,
		`{"action":"start","name":"preview","args":["injected"]}`,
		`{"action":"start","name":"preview","cwd":"/tmp"}`,
		`{"action":"start","name":"preview","shell":"echo injected"}`,
		`{"action":"start","name":"preview","id":"bg-000001"}`,
		`{"action":"start","name":"missing"}`,
		`{"action":"unknown"}`,
	} {
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"`) || strings.Contains(result, "started") {
			t.Fatalf("arguments %s returned %q, error = %v; want a rejected request", arguments, result, err)
		}
	}
	started, err := registered.Handler(context.Background(), `{"action":"start","name":"preview"}`)
	if err != nil || !strings.Contains(started, `"status":"start_failed"`) {
		t.Fatalf("unsafe background start was accepted: %q, %v", started, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err = registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"status":"configuration_unavailable"`) || strings.Contains(listed, `"id":"bg-000001"`) {
		t.Fatalf("list with invalid config = %q, error = %v; want configuration error without a session", listed, err)
	}
	for _, arguments := range []string{
		`{"action":"list","action":"start"}`,
		`{"action":"list","command":"echo injected"}`,
		`{"action":"start","name":"preview","pty":true}`,
		`{"action":"list"} {}`,
		`[]`,
	} {
		if _, err := parseBackgroundCommandToolArguments(arguments); err == nil {
			t.Fatalf("parseBackgroundCommandToolArguments(%s) error = nil", arguments)
		}
	}
}

func TestBackgroundCommandToolSupportsAllowlistedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{
		{
			Name: "interactive", Executable: executable,
			Args: []string{"-test.run=^TestBackgroundCommandToolHelper$", "--", "--goaster-background-tool-helper"}, Interactive: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	registered := Tool{}
	registrar := backgroundCommandToolRegistrar{register: func(tool Tool) error {
		registered = tool
		return nil
	}}
	if err := RegisterBackgroundCommandTool(registrar, manager); err != nil {
		t.Fatal(err)
	}

	listed, err := registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"interactive":true`) {
		t.Fatalf("interactive list = %q, error = %v", listed, err)
	}
	started, err := registered.Handler(context.Background(), `{"action":"start","name":"interactive"}`)
	if err != nil || !strings.Contains(started, `"status":"start_failed"`) {
		t.Fatalf("interactive command bypassed containment refusal: %q, %v", started, err)
	}
	for _, action := range []string{"input", "output", "stop"} {
		arguments := fmt.Sprintf(`{"action":%q,"id":"bg-000001"}`, action)
		if action == "input" {
			arguments = `{"action":"input","id":"bg-000001","data":"hello"}`
		}
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"not_found"`) {
			t.Fatalf("refused session remained accessible: %q, %v", result, err)
		}
	}

	for _, arguments := range []string{
		`{"action":"input","id":"bg-999999","data":"x"}`,
		`{"action":"start","name":"interactive","pty":true}`,
		`{"action":"input","id":"bg-000001","data":"x","keys":"enter"}`,
	} {
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"`) || strings.Contains(result, "input_queued") {
			t.Fatalf("authorization/input boundary for %s returned %q, error = %v", arguments, result, err)
		}
	}
}
