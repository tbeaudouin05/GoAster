package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/command"
)

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
	if err != nil || !strings.Contains(started, `"status":"started"`) {
		t.Fatalf("start result = %q, error = %v", started, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err = registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"status":"configuration_unavailable"`) || !strings.Contains(listed, `"id":"bg-000001"`) {
		t.Fatalf("list with invalid config = %q, error = %v; want existing session for recovery", listed, err)
	}
	for _, arguments := range []string{
		`{"action":"list","action":"start"}`,
		`{"action":"list","command":"echo injected"}`,
		`{"action":"list"} {}`,
		`[]`,
	} {
		if _, err := parseBackgroundCommandToolArguments(arguments); err == nil {
			t.Fatalf("parseBackgroundCommandToolArguments(%s) error = nil", arguments)
		}
	}
}
