package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodexProviderRegistersAndExecutesSkillTool(t *testing.T) {
	var backendRequests atomic.Int32
	var gotArguments string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			if len(request.Tools) != 4 {
				t.Errorf("tools = %+v, want three built-ins and one skill tool", request.Tools)
			}
			var skillTool *codexTool
			for i := range request.Tools {
				if request.Tools[i].Name == "skill_echo" {
					skillTool = &request.Tools[i]
					break
				}
			}
			if skillTool == nil {
				t.Errorf("Codex tools = %+v, missing skill_echo", request.Tools)
			} else {
				if skillTool.Type != "function" || skillTool.Description != "Return a value from a test skill." {
					t.Errorf("skill tool metadata = %+v", skillTool)
				}
				properties, ok := skillTool.Parameters["properties"].(map[string]any)
				if !ok {
					t.Errorf("skill tool properties = %#v", skillTool.Parameters["properties"])
				} else if _, ok := properties["value"]; !ok {
					t.Errorf("skill tool properties missing value: %#v", properties)
				}
			}
			writeCodexFunctionCall(t, w, "call-skill", "skill_echo", `{"value":"from skill"}`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			}
			if len(request.Input) >= 3 {
				call := request.Input[1]
				if call.Type != "function_call" || call.CallID != "call-skill" || call.Name != "skill_echo" {
					t.Errorf("function call input = %+v", call)
				}
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "call-skill" || output.Output != "skill result" {
					t.Errorf("function output input = %+v", output)
				}
			}
			fmt.Fprint(w, `{"output_text":"Final answer after skill"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	var registrar ToolRegistrar = provider
	if registrar == nil {
		t.Fatal("CodexProvider does not implement ToolRegistrar")
	}
	if err := registrar.RegisterTool(Tool{
		Name:        "skill_echo",
		Description: "Return a value from a test skill.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []string{"value"},
		},
		Handler: func(_ context.Context, arguments string) (string, error) {
			gotArguments = arguments
			return "skill result", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := provider.Reply(context.Background(), "gpt-test", "use the skill")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Final answer after skill" {
		t.Fatalf("reply = %q", reply)
	}
	if gotArguments != `{"value":"from skill"}` {
		t.Fatalf("skill arguments = %q", gotArguments)
	}
	if got := backendRequests.Load(); got != 2 {
		t.Fatalf("backend requests = %d, want 2", got)
	}
}

func TestCodexProviderRejectsInvalidOrDuplicateTools(t *testing.T) {
	provider := NewCodexProvider(CredentialStore{})
	valid := Tool{
		Name:        "skill_echo",
		Description: "Return a value from a test skill.",
		Parameters:  map[string]any{"type": "object"},
		Handler:     func(context.Context, string) (string, error) { return "", nil },
	}

	for _, tool := range []Tool{
		valid,
		{Name: "", Description: valid.Description, Parameters: valid.Parameters, Handler: valid.Handler},
		{Name: "missing-description", Parameters: valid.Parameters, Handler: valid.Handler},
		{Name: "missing-parameters", Description: valid.Description, Handler: valid.Handler},
		{Name: "missing-handler", Description: valid.Description, Parameters: valid.Parameters},
	} {
		if tool.Name == valid.Name {
			if err := provider.RegisterTool(tool); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := provider.RegisterTool(tool); err == nil {
			t.Fatalf("RegisterTool(%q) succeeded", tool.Name)
		}
	}

	if err := provider.RegisterTool(valid); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate tool error = %v", err)
	}
	if err := provider.RegisterTool(Tool{
		Name:        shellExecuteCodexTool.Name,
		Description: "replacement",
		Parameters:  map[string]any{"type": "object"},
		Handler:     valid.Handler,
	}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("built-in duplicate error = %v", err)
	}
}
