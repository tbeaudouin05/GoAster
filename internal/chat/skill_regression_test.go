package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/artefacts"
	"goaster/internal/localenv"
)

func TestUnselectedSkillsAreNotInjectedAndSelectedChangesStopTools(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	phase, executions := "direct", 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(request.Instructions, "fixture-skill") || strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "FIXTURE AUTHENTICATION") {
			t.Error("request did not restrict unselected skills to metadata")
		}
		if phase == "change" {
			writeFixture(t, workflow, "CHANGED WORKFLOW")
		}
		if phase == "batch" {
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"first","name":"shell_execute","arguments":"{}"},{"type":"function_call","call_id":"second","name":"shell_execute","arguments":"{}"}]}`)
			return
		}
		if phase == "done" {
			fmt.Fprint(w, `{"output_text":"done"}`)
			return
		}
		writeCodexFunctionCall(t, w, "action", "shell_execute", `{}`)
		if phase == "direct" {
			phase = "done"
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { executions++; return "ok", nil }
	if _, err := provider.Reply(context.Background(), "gpt-test", "perform a task"); err != nil || executions != 1 {
		t.Fatalf("direct tool: executions=%d error=%v", executions, err)
	}
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	phase = "change"
	if _, err := session.ReplyScheduled(context.Background(), "perform task"); !errors.Is(err, ErrSessionBusy) || SkillRecoveryMessage(err) != skillChangedMessage || executions != 1 {
		t.Fatalf("changed selected skill reached tool: executions=%d error=%v", executions, err)
	}
	writeFixture(t, workflow, "PRIVATE WORKFLOW TEXT")
	phase = "batch"
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		executions++
		writeFixture(t, workflow, "CHANGED DURING FIRST ACTION")
		return "ok", nil
	}
	if _, err := session.ReplyScheduled(context.Background(), "perform two tasks"); err == nil || errors.Is(err, ErrSessionBusy) || SkillRecoveryMessage(err) != skillChangedMessage || executions != 2 {
		t.Fatalf("changed guidance did not stop second action: executions=%d error=%v", executions, err)
	}

}

func TestUnselectedSkillContentsAreLoadedOnlyOnDemand(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "skills", "fixture-skill", "AUTH.md"), "bad UTF-8: \xff")
	for _, name := range []string{"large-a", "large-b", "large-c"} {
		dir := filepath.Join(root, "skills", name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: large fixture\n")
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "required authentication for "+name)
		writeFixture(t, filepath.Join(dir, "SKILL.md"), strings.Repeat(name, (256<<10)/len(name)))
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(data), "required authentication for large-a") || strings.Contains(string(data), "bad UTF-8") {
			t.Error("unselected skill contents were injected")
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	if _, err := provider.Reply(context.Background(), "gpt-test", "hello"); err != nil || backendCalls != 1 {
		t.Fatalf("unselected skills blocked request: calls=%d error=%v", backendCalls, err)
	}
	if _, err := executeLoadSkill(context.Background(), `{"name":"fixture-skill"}`); SkillRecoveryMessage(err) != skillResetMessage {
		t.Fatalf("invalid selected authentication accepted: %v", err)
	}
}

func TestSelectedSkillCountsTowardCompaction(t *testing.T) {
	setupSkillFixture(t)
	home, _ := artefacts.Root()
	writeFixture(t, filepath.Join(home, "skills", "fixture-skill", "SKILL.md"), strings.Repeat("x", 192<<10))
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	selected := []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	summaryRequests, normalRequests := 0, 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) == 0 {
			summaryRequests++
			fmt.Fprint(w, `{"output_text":"compacted history"}`)
			return
		}
		normalRequests++
		if !strings.Contains(fmt.Sprint(request.Input), content) {
			t.Error("restored skill omitted after compaction")
		}
		if tokens := 4096 + estimateMessageTokens(ChatMessage{Content: request.Instructions}) + codexItemTokens(request.Input); tokens > maxContextTokens {
			t.Errorf("compacted request exceeds budget: %d", tokens)
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	base, err := provider.skillContextOverhead(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	memoryContext := WithMemoryContext(context.Background(), MemoryContext{DailyNotes: strings.Repeat("daily note ", 4096)})
	withMemory, err := provider.skillContextOverhead(memoryContext, nil)
	if err != nil || withMemory <= base {
		t.Fatalf("daily note overhead base=%d with notes=%d error=%v", base, withMemory, err)
	}
	withSkill, err := provider.skillContextOverhead(context.Background(), selected)
	if err != nil || withSkill <= base {
		t.Fatalf("skill overhead base=%d selected=%d error=%v", base, withSkill, err)
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = selected
	historyTokens := summarizeTokens - withSkill
	session.history = append(historyWithTokens(historyTokens), numberedHistory(5)...)
	if _, err := session.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if summaryRequests != 1 || normalRequests != 1 {
		t.Fatalf("summary=%d normal=%d", summaryRequests, normalRequests)
	}
}

func TestTerminalShowsSkillRecoveryDetails(t *testing.T) {
	for _, message := range []string{skillResetMessage, skillChangedMessage, skillBudgetMessage, skillSaveMessage} {
		if !IsSkillRecoveryMessage(message) || !strings.Contains(message, skillActionNotice) || strings.Contains(message, "restart the workflow") {
			t.Fatalf("unsafe or unrecognized recovery message: %q", message)
		}
	}
	private := strings.Repeat("private-detail-", 1000)
	privateError := errors.New(private)
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		return "", skillRecovery(privateError, skillResetMessage)
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n/clear\n"), interactive: true}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{interactive: true, width: 200})
	if err != nil || !strings.Contains(output.String(), "/clear") || !strings.Contains(output.String(), "Conversation history cleared") || !strings.Contains(output.String(), "private-detail") {
		t.Fatalf("terminal output = %q, error=%v", output.String(), err)
	}
	output.Reset()
	err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n")}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{})
	if err == nil || !errors.Is(err, privateError) || !strings.Contains(err.Error(), skillResetMessage) || !strings.Contains(err.Error(), "private-detail") {
		t.Fatalf("line mode error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "session.json")
	calls := 0
	recoveredProvider := providerFunc(func(context.Context, string, string) (string, error) {
		calls++
		return "unexpected", nil
	})
	session, err := NewPersistentSessionAt(Config{Model: "test"}, recoveredProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, "invalid saved conversation")
	if _, err := session.ReplyScheduled(context.Background(), "scheduled"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("scheduler recovery = %v", err)
	}
	output.Reset()
	err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("continue\n/clear\n"), interactive: true}, &output,
		session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{interactive: true, width: 200})
	if err != nil || calls != 0 || !strings.Contains(output.String(), "saved conversation is missing or invalid") || !strings.Contains(output.String(), "Conversation history cleared") {
		t.Fatalf("scheduled recovery output = %q, calls=%d, error=%v", output.String(), calls, err)
	}

}
