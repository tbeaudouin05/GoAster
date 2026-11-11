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
	"sync/atomic"
	"testing"
)

func TestCodexProviderRegistersAndExecutesSkillTool(t *testing.T) {
	setupSkillFixture(t)
	verifySkillLoading(t)
	var backendRequests atomic.Int32
	var gotArguments string
	scenario := "normal"
	selectedSessionPath := ""
	var overlapCheck func()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		if scenario != "normal" {
			step := backendRequests.Add(1)
			call := func(id, name, arguments string) codexOutputItem {
				encoded, err := json.Marshal(arguments)
				if err != nil {
					t.Error(err)
				}
				return codexOutputItem{Type: "function_call", CallID: id, Name: name, Arguments: encoded}
			}
			switch scenario {
			case "failed-load":
				if step == 1 {
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
						call("action", "skill_echo", `{}`),
						call("bad-load", "load_skill", `{"name":"missing-skill"}`),
					}})
				} else {
					writeCodexFunctionCall(t, w, "later-action", "skill_echo", `{}`)
				}
			case "deduplicate":
				if step == 1 {
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
						call("load-1", "load_skill", `{"name":"fixture-skill"}`),
						call("load-2", "load_skill", `{"name":"fixture-skill"}`),
					}})
				} else {
					payload, err := json.Marshal(request.Input)
					if err != nil || strings.Count(string(payload), "PRIVATE WORKFLOW TEXT") != 1 || strings.Count(string(payload), "FIXTURE AUTHENTICATION") != 1 {
						t.Errorf("skill content was duplicated: %s, %v", payload, err)
					}
					if step == 2 {
						writeCodexFunctionCall(t, w, "load-3", "load_skill", `{"name":"fixture-skill"}`)
					} else {
						fmt.Fprint(w, `{"output_text":"done"}`)
					}
				}
			case "web-budget":
				if step <= 2 {
					calls := []codexOutputItem{}
					searches, fetches := 3, 5
					if step == 1 {
						searches, fetches = 4, 6
						calls = append(calls, call("load", "load_skill", `{"name":"fixture-skill"}`))
					}
					for i := 0; i < searches; i++ {
						calls = append(calls, call(fmt.Sprintf("search-%d-%d", step, i), "web_search", `{}`))
					}
					for i := 0; i < fetches; i++ {
						calls = append(calls, call(fmt.Sprintf("fetch-%d-%d", step, i), "web_fetch", `{}`))
					}
					json.NewEncoder(w).Encode(codexResponse{Output: calls})
				} else {
					writeCodexFunctionCall(t, w, "one-too-many", "web_search", `{}`)
				}
			case "skill-budget":
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					call("big-a", "load_skill", `{"name":"big-a"}`),
					call("big-b", "load_skill", `{"name":"big-b"}`),
					call("action", "skill_echo", `{}`),
				}})
			case "context-budget":
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					call("big-a", "load_skill", `{"name":"big-a"}`),
					call("action", "skill_echo", `{}`),
				}})
			case "session-continuation":
				payload, err := json.Marshal(request.Input)
				if err != nil {
					t.Error(err)
					return
				}
				text := string(payload)
				switch step {
				case 1:
					if overlapCheck != nil {
						overlapCheck()
					}
					if strings.Contains(text, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "FIXTURE AUTHENTICATION") {
						t.Error("installed guidance or selection state was incorrect")
					}
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
						call("load", "load_skill", `{"name":"fixture-skill"}`),
						call("premature", "shell_execute", `{"command":"premature"}`),
					}})
				case 2:
					if !strings.Contains(text, "PRIVATE WORKFLOW TEXT") || !strings.Contains(text, "FIXTURE AUTHENTICATION") || !strings.Contains(text, "Tool not executed") {
						t.Errorf("initial selection did not provide full guidance or suppress shell: %s", text)
					}
					state, err := os.ReadFile(selectedSessionPath)
					if err != nil || !strings.Contains(string(state), `"name": "fixture-skill"`) || !strings.Contains(string(state), `"digest":`) {
						t.Errorf("skill was not checkpointed before the next model request: %s, %v", state, err)
					}
					fmt.Fprint(w, `{"output_text":"Approve showing the status of host A?"}`)
				case 3, 7, 8:
					if strings.Contains(text, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "FIXTURE AUTHENTICATION") {
						t.Errorf("another or cleared session received selected skill guidance: %s", text)
					}
					fmt.Fprint(w, `{"output_text":"No selected skill."}`)
				case 4, 5, 6:
					if strings.Count(text, "PRIVATE WORKFLOW TEXT") != 1 || strings.Count(text, "FIXTURE AUTHENTICATION") != 1 || !strings.Contains(text, "Reloading a skill grants no authorization") {
						t.Errorf("continuation omitted or duplicated guidance: %s", text)
					}
					foundCall, foundOutput := false, false
					for _, item := range request.Input {
						if item.CallID == "call_goaster_restored_skill_0" && item.Type == "function_call" && item.Name == "load_skill" {
							foundCall = true
						}
						if strings.Contains(item.Output, "FIXTURE AUTHENTICATION") && item.Type == "function_call_output" && item.CallID == "call_goaster_restored_skill_0" {
							foundOutput = true
						}
					}
					if !foundCall || !foundOutput {
						t.Errorf("restored skill did not retain tool-result trust: %s", text)
					}
					if !strings.Contains(text, "Approve showing the status of host A?") {
						t.Errorf("continuation omitted the approval request: %s", text)
					}
					if step == 4 {
						if !strings.Contains(text, "Yes, show host A status") {
							t.Errorf("continuation omitted the user's scoped approval: %s", text)
						}
						writeCodexFunctionCall(t, w, "approved", "shell_execute", `{"command":"show-host-a-status"}`)
					} else {
						fmt.Fprint(w, `{"output_text":"Status checked."}`)
					}
				default:
					t.Errorf("unexpected session continuation request %d", step)
				}
			case "selection-checkpoint":
				writeCodexFunctionCall(t, w, "load", "load_skill", `{"name":"fixture-skill"}`)
			case "malformed-selection":
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					call("bad", "load_skill", `{"name":null}`),
					call("action", "skill_echo", `{}`),
				}})
			case "unsupported-before-selection":
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					call("bad", "unknown_tool", `{}`),
					call("load", "load_skill", `{"name":"fixture-skill"}`),
				}})
			case "excessive-selection":
				calls := make([]codexOutputItem, 0, maxCodexSkillLoads+1)
				for i := 0; i <= maxCodexSkillLoads; i++ {
					calls = append(calls, call(fmt.Sprintf("load-%d", i), "load_skill", `{"name":"fixture-skill"}`))
				}
				json.NewEncoder(w).Encode(codexResponse{Output: calls})
			case "selected-compaction":
				payload, err := json.Marshal(request.Input)
				if err != nil {
					t.Error(err)
					return
				}
				text := string(payload)
				if step == 1 {
					if len(request.Tools) != 0 || strings.Contains(text, "PRIVATE WORKFLOW TEXT") || strings.Contains(text, "FIXTURE AUTHENTICATION") {
						t.Errorf("summarizer unexpectedly received skill guidance or tools: %s", text)
					}
					fmt.Fprint(w, `{"output_text":"Earlier conversation summarized."}`)
				} else {
					if strings.Count(text, "PRIVATE WORKFLOW TEXT") != 1 || strings.Count(text, "FIXTURE AUTHENTICATION") != 1 {
						t.Errorf("compacted session lost skill guidance: %s", text)
					}
					fmt.Fprint(w, `{"output_text":"done"}`)
				}
			case "simple":
				fmt.Fprint(w, `{"output_text":"done"}`)
			}
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			if len(request.Tools) != 10 {
				t.Errorf("tools = %+v, want nine built-ins and one optional tool", request.Tools)
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
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"call-load","name":"load_skill","arguments":"{\"name\":\"fixture-skill\"}"},{"type":"function_call","call_id":"call-shell","name":"shell_execute","arguments":"{\"command\":\"echo should-not-run\"}"}]}`)
		case 2:
			if len(request.Input) != 5 || request.Input[3].CallID != "call-load" || !strings.Contains(request.Input[3].Output, "FIXTURE AUTHENTICATION") || !strings.Contains(request.Input[3].Output, "PRIVATE WORKFLOW TEXT") || request.Input[4].CallID != "call-shell" || !strings.Contains(request.Input[4].Output, "Tool not executed") {
				t.Errorf("loaded skill output = %+v", request.Input)
			}
			writeCodexFunctionCall(t, w, "call-skill", "skill_echo", `{"value":"from skill"}`)
		case 3:
			if len(request.Input) != 7 {
				t.Errorf("follow-up input length = %d, want 7: %+v", len(request.Input), request.Input)
			}
			if len(request.Input) >= 7 {
				call := request.Input[5]
				if call.Type != "function_call" || call.CallID != "call-skill" || call.Name != "skill_echo" {
					t.Errorf("function call input = %+v", call)
				}
				output := request.Input[6]
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
	if got := backendRequests.Load(); got != 3 {
		t.Fatalf("backend requests = %d, want 3", got)
	}

	scenario = "failed-load"
	backendRequests.Store(0)
	gotArguments = ""
	if reply, err := provider.Reply(context.Background(), "gpt-test", "use missing skill"); err == nil || !strings.Contains(err.Error(), "skill load failed") || reply != "" {
		t.Fatalf("failed load reply = %q, %v", reply, err)
	}
	if gotArguments != "" || backendRequests.Load() != 1 {
		t.Fatalf("action or follow-up ran after failed load: %q, %d", gotArguments, backendRequests.Load())
	}

	scenario = "deduplicate"
	backendRequests.Store(0)
	if reply, err := provider.Reply(context.Background(), "gpt-test", "load repeatedly"); err != nil || reply != "done" || backendRequests.Load() != 3 {
		t.Fatalf("repeated load reply = %q, %v, requests %d", reply, err, backendRequests.Load())
	}

	var searches, fetches int
	provider.tools.byName["web_search"] = func(context.Context, string) (string, error) { searches++; return "search", nil }
	provider.tools.byName["web_fetch"] = func(context.Context, string) (string, error) { fetches++; return "fetch", nil }
	scenario = "web-budget"
	backendRequests.Store(0)
	if reply, err := provider.Reply(context.Background(), "gpt-test", "search after loading"); err == nil || !strings.Contains(err.Error(), "too many web_search") || reply != "" {
		t.Fatalf("web budget reply = %q, %v", reply, err)
	}
	if searches != 3 || fetches != 5 || backendRequests.Load() != 3 {
		t.Fatalf("executed web calls = %d searches, %d fetches; requests = %d", searches, fetches, backendRequests.Load())
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"big-a", "big-b"} {
		dir := filepath.Join(home, ".goaster", "artefacts", "skills", name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: large skill\n")
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "authentication")
		writeFixture(t, filepath.Join(dir, "SKILL.md"), strings.Repeat("x", 256<<10))
	}
	scenario = "skill-budget"
	backendRequests.Store(0)
	gotArguments = ""
	if reply, err := provider.Reply(context.Background(), "gpt-test", "load large skills"); err == nil || !strings.Contains(err.Error(), "combined skill content") || reply != "" {
		t.Fatalf("skill budget reply = %q, %v", reply, err)
	}
	if gotArguments != "" || backendRequests.Load() != 1 {
		t.Fatal("action or follow-up ran after exceeding the skill budget")
	}

	scenario = "context-budget"
	backendRequests.Store(0)
	history := make([]ChatMessage, 0, 37)
	for i := 0; i < 32; i++ {
		history = append(history, ChatMessage{Role: "user", Content: strings.Repeat("h", maxSessionMessageBytes)})
	}
	history = append(history, ChatMessage{Role: "user", Content: "load a large skill"})
	if reply, err := provider.ReplyWithHistory(context.Background(), "gpt-test", history); err == nil || !strings.Contains(err.Error(), "insufficient room") || reply != "" {
		t.Fatalf("context budget reply = %q, %v", reply, err)
	}
	if gotArguments != "" || backendRequests.Load() != 1 {
		t.Fatal("action or follow-up ran after exceeding the context budget")
	}

	// The same provider is shared by sessions, but selected skills and approval
	// context belong only to the conversation that selected them.
	scenario = "session-continuation"
	backendRequests.Store(0)
	var shellCalls []string
	provider.tools.byName["shell_execute"] = func(_ context.Context, arguments string) (string, error) {
		shellCalls = append(shellCalls, arguments)
		return "test shell result", nil
	}
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	selectedSessionPath = sessionPath
	first, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	overlapCheck = func() {
		if _, err := stale.Reply(context.Background(), "Overlapping approval"); err == nil || !strings.Contains(err.Error(), "lock conversation turn") {
			t.Errorf("overlapping session was not blocked: %v", err)
		}
		if err := NewSessionStoreAt(sessionPath).Save(nil); err == nil || !strings.Contains(err.Error(), "lock conversation turn") {
			t.Errorf("overlapping history save was not blocked: %v", err)
		}
		if _, err := NewSessionStoreAt(sessionPath).Load(); err == nil || !strings.Contains(err.Error(), "lock conversation turn") {
			t.Errorf("overlapping history load was not blocked: %v", err)
		}
	}
	other := NewSession(Config{Model: "gpt-test"}, provider)
	if reply, err := first.Reply(context.Background(), "Check host A"); err != nil || reply != "Approve showing the status of host A?" {
		t.Fatalf("initial session reply = %q, %v", reply, err)
	}
	if len(shellCalls) != 0 {
		t.Fatalf("shell ran before approval: %v", shellCalls)
	}
	if _, err := stale.Reply(context.Background(), "Yes, show host A status"); err == nil || !strings.Contains(err.Error(), "changed in another instance") {
		t.Fatalf("stale session continued without the selected skill: %v", err)
	}
	if backendRequests.Load() != 2 || len(shellCalls) != 0 {
		t.Fatal("stale session reached the backend or shell")
	}
	saved := readFixture(t, sessionPath)
	if !strings.Contains(saved, `"name": "fixture-skill"`) || !strings.Contains(saved, `"digest":`) || strings.Contains(saved, "PRIVATE WORKFLOW TEXT") || strings.Contains(saved, "FIXTURE AUTHENTICATION") {
		t.Fatalf("session saved the wrong skill state: %s", saved)
	}
	if _, err := other.Reply(context.Background(), "An unrelated session"); err != nil {
		t.Fatal(err)
	}

	// If current guidance changes or becomes unavailable, an old approval
	// must never reach the model or an action before the workflow is restarted.
	authPath := filepath.Join(home, ".goaster", "artefacts", "skills", "fixture-skill", "AUTH.md")
	writeFixture(t, authPath, "CHANGED AUTHENTICATION")
	if reply, err := first.Reply(context.Background(), "Yes, show host A status"); err == nil || !strings.Contains(err.Error(), "changed since it was loaded") || reply != "" {
		t.Fatalf("changed authentication continuation = %q, %v", reply, err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if reply, err := first.Reply(context.Background(), "Yes, show host A status"); err == nil || !strings.Contains(err.Error(), "reload selected skill") || reply != "" {
		t.Fatalf("missing authentication continuation = %q, %v", reply, err)
	}
	if backendRequests.Load() != 3 || len(shellCalls) != 0 {
		t.Fatal("backend or shell ran with changed or missing authentication guidance")
	}
	writeFixture(t, authPath, "FIXTURE AUTHENTICATION")
	workflowPath := filepath.Join(home, ".goaster", "artefacts", "skills", "fixture-skill", "SKILL.md")
	writeFixture(t, workflowPath, "CHANGED WORKFLOW")
	if _, err := first.Reply(context.Background(), "Yes, show host A status"); err == nil || !strings.Contains(err.Error(), "changed since it was loaded") {
		t.Fatalf("changed workflow continuation = %v", err)
	}
	if backendRequests.Load() != 3 || len(shellCalls) != 0 {
		t.Fatal("backend or shell ran with changed workflow")
	}
	writeFixture(t, workflowPath, "PRIVATE WORKFLOW TEXT")
	if reply, err := first.Reply(context.Background(), "Yes, show host A status"); err != nil || reply != "Status checked." {
		t.Fatalf("approved continuation = %q, %v", reply, err)
	}
	if len(shellCalls) != 1 || shellCalls[0] != `{"command":"show-host-a-status"}` {
		t.Fatalf("approved shell calls = %v", shellCalls)
	}
	first, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Reply(context.Background(), "What next?"); err != nil {
		t.Fatalf("restarted session: %v", err)
	}
	if _, err := other.Reply(context.Background(), "Still unrelated"); err != nil {
		t.Fatal(err)
	}
	if reply, err := first.Reply(context.Background(), "/clear"); err != nil || reply != "Conversation history cleared." {
		t.Fatalf("clear selected skills = %q, %v", reply, err)
	}
	if _, err := first.Reply(context.Background(), "A new request"); err != nil {
		t.Fatal(err)
	}
	if backendRequests.Load() != 8 || len(shellCalls) != 1 {
		t.Fatalf("continuation requests = %d, shell calls = %v", backendRequests.Load(), shellCalls)
	}

	// A selected skill whose load failed is checkpointed as pending. Neither a
	// later turn nor a restart can silently continue without that guidance.
	scenario = "failed-load"
	backendRequests.Store(0)
	pendingPath := filepath.Join(t.TempDir(), "session.json")
	pending, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Reply(context.Background(), "Use the missing skill"); err == nil {
		t.Fatal("missing skill load succeeded")
	}
	pending, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Reply(context.Background(), "Yes"); err == nil || !strings.Contains(err.Error(), "did not finish loading") {
		t.Fatalf("pending skill continuation error = %v", err)
	}
	if backendRequests.Load() != 1 || len(shellCalls) != 1 {
		t.Fatal("pending skill allowed a backend request or action")
	}
	if _, err := pending.Reply(context.Background(), "/clear"); err != nil {
		t.Fatal(err)
	}
	scenario = "simple"
	if _, err := pending.Reply(context.Background(), "Start over"); err != nil {
		t.Fatalf("cleared pending skill: %v", err)
	}
	for _, failure := range []string{"malformed-selection", "unsupported-before-selection", "excessive-selection"} {
		scenario = failure
		backendRequests.Store(0)
		gotArguments = ""
		path := filepath.Join(t.TempDir(), "session.json")
		blocked, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocked.Reply(context.Background(), "Select a skill"); err == nil {
			t.Fatalf("%s: invalid selection unexpectedly succeeded", failure)
		}
		blocked, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocked.Reply(context.Background(), "Yes"); err == nil || (!strings.Contains(err.Error(), "did not finish loading") && !strings.Contains(err.Error(), "skill selection was invalid or excessive")) {
			t.Fatalf("%s: failed selection was forgotten: %v", failure, err)
		}
		if backendRequests.Load() != 1 || gotArguments != "" {
			t.Fatalf("%s: failed selection allowed another request or action", failure)
		}
	}

	// Checkpoint failures stop before any later model request, and the live
	// session refuses retries even if the filesystem starts working again.
	scenario = "selection-checkpoint"
	backendRequests.Store(0)
	checkpointPath := filepath.Join(t.TempDir(), "session.json")
	checkpoint, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.store.syncDirectory = func(string) error { return errors.New("checkpoint failure") }
	if _, err := checkpoint.Reply(context.Background(), "Use the skill"); err == nil || !strings.Contains(err.Error(), "save selected skill") {
		t.Fatalf("checkpoint failure = %v", err)
	}
	checkpoint.store.syncDirectory = nil
	if _, err := checkpoint.Reply(context.Background(), "Yes"); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("checkpoint retry = %v", err)
	}
	checkpoint, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Reply(context.Background(), "Yes"); err == nil || !strings.Contains(err.Error(), "did not finish loading") {
		t.Fatalf("uncertain checkpoint after restart = %v", err)
	}
	if backendRequests.Load() != 1 || len(shellCalls) != 1 {
		t.Fatal("failed checkpoint allowed a later backend request or action")
	}
	checkpointPath = filepath.Join(t.TempDir(), "session.json")
	checkpoint, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	backendRequests.Store(0)
	checkpointWrites := 0
	checkpoint.store.syncDirectory = func(string) error {
		checkpointWrites++
		if checkpointWrites == 2 {
			return errors.New("completed selection checkpoint failure")
		}
		return nil
	}
	if _, err := checkpoint.Reply(context.Background(), "Use the skill"); err == nil || !strings.Contains(err.Error(), "save selected skill") {
		t.Fatalf("completed selection checkpoint failure = %v", err)
	}
	checkpoint.store.syncDirectory = nil
	if _, err := checkpoint.Reply(context.Background(), "Yes"); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("completed selection checkpoint retry = %v", err)
	}
	checkpoint, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, checkpointPath)
	if err != nil || len(checkpoint.selectedSkills) != 1 || checkpoint.selectedSkills[0].Digest == "" {
		t.Fatalf("completed selection after restart = %+v, %v", checkpoint, err)
	}
	scenario = "selected-compaction"
	if _, err := checkpoint.Reply(context.Background(), "Continue after reload"); err != nil {
		t.Fatalf("completed selection reload = %v", err)
	}
	if backendRequests.Load() != 2 || len(shellCalls) != 1 {
		t.Fatal("failed completed checkpoint allowed an unexpected request or action")
	}

	// Conversation compaction and the store's history-only Save API must
	// preserve selections through their checkpoints and subsequent restarts.
	fixtureContent, err := executeLoadSkill(context.Background(), `{"name":"fixture-skill"}`)
	if err != nil {
		t.Fatal(err)
	}
	compactedPath := filepath.Join(t.TempDir(), "session.json")
	compacted, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, compactedPath)
	if err != nil {
		t.Fatal(err)
	}
	compacted.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(fixtureContent)}}
	compacted.history = numberedHistory(maxSessionMessages - 1)
	version, err := compacted.store.saveStateWithVersion("", compacted.history, compacted.selectedSkills, compacted.storeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	compacted.storeVersion = version
	scenario = "selected-compaction"
	backendRequests.Store(0)
	if _, err := compacted.Reply(context.Background(), "Continue after compaction"); err != nil {
		t.Fatal(err)
	}
	compacted, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, compactedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compacted.Reply(context.Background(), "Continue after restart"); err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreAt(compactedPath)
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(loaded.Messages); err != nil {
		t.Fatal(err)
	}
	compacted, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, compactedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compacted.Reply(context.Background(), "Continue after history save"); err != nil {
		t.Fatal(err)
	}
	if backendRequests.Load() != 4 {
		t.Fatalf("compacted session requests = %d", backendRequests.Load())
	}

	// A selection beyond the session limit is itself checkpointed as pending,
	// so retrying cannot discard the additional requested workflow.
	limitedPath := filepath.Join(t.TempDir(), "session.json")
	limited, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, limitedPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxSessionSkills; i++ {
		name := fmt.Sprintf("limited-%d", i)
		dir := filepath.Join(home, ".goaster", "artefacts", "skills", name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: limited fixture\n")
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "authentication")
		writeFixture(t, filepath.Join(dir, "SKILL.md"), "workflow")
		arguments := fmt.Sprintf(`{"name":%q}`, name)
		content, err := executeLoadSkill(context.Background(), arguments)
		if err != nil {
			t.Fatal(err)
		}
		limited.selectedSkills = append(limited.selectedSkills, selectedSkill{Name: name, Digest: skillDigest(content)})
	}
	version, err = limited.store.saveStateWithVersion("", nil, limited.selectedSkills, limited.storeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	limited.storeVersion = version
	scenario = "failed-load"
	backendRequests.Store(0)
	gotArguments = ""
	if _, err := limited.Reply(context.Background(), "Use another skill"); err == nil || !strings.Contains(err.Error(), "too many selected skills") {
		t.Fatalf("skill selection limit = %v", err)
	}
	limited, err = NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, limitedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Reply(context.Background(), "Yes"); err == nil || !strings.Contains(err.Error(), "too many selected skills") {
		t.Fatalf("skill selection limit after restart = %v", err)
	}
	if backendRequests.Load() != 1 || gotArguments != "" {
		t.Fatal("skill selection limit allowed a later model request or action")
	}

	// A current-format file missing its explicit selection state is invalid,
	// rather than an invitation to continue an approval without its skill.
	invalidPath := filepath.Join(t.TempDir(), "session.json")
	writeFixture(t, invalidPath, `{"version":2,"messages":[{"role":"assistant","content":"Approve?"}]}`)
	invalid, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(invalid.startupNotice, "invalid") || len(invalid.history) != 0 {
		t.Fatalf("invalid selection state was reused: %q, %+v", invalid.startupNotice, invalid.history)
	}

	// Restored skills must fit the same context budget as newly loaded skills.
	largeContent, err := executeLoadSkill(context.Background(), `{"name":"big-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	selected := []selectedSkill{{Name: "big-a", Digest: skillDigest(largeContent)}}
	selectedContext := context.WithValue(context.Background(), skillSessionKey{}, skillSessionContext{selected: func() []selectedSkill { return selected }})
	backendRequests.Store(0)
	if _, err := provider.ReplyWithHistory(selectedContext, "gpt-test", history); err == nil || !strings.Contains(err.Error(), "insufficient room") {
		t.Fatalf("restored skill context budget = %v", err)
	}
	if backendRequests.Load() != 0 {
		t.Fatal("backend request ran without enough room for restored skill")
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
