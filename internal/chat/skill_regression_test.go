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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/artefacts"
	"goaster/internal/localenv"
	"goaster/skills"
)

func TestDirectSensitiveToolsReceiveMandatoryRules(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	step := 0
	phase := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, want := range []string{
			"FIXTURE AUTHORIZATION",
			"Never use trust-on-first-use for an",
			"Never replace the hostname with a guessed IP",
			"Obtain explicit user approval for billable creation",
		} {
			if !strings.Contains(request.Instructions, want) {
				t.Errorf("request before tool %d omitted %q", step, want)
			}
		}
		if !strings.Contains(request.Instructions, skills.MandatoryGuidance) || !strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") {
			t.Error("request omitted shipped rules or installed workflow")
		}
		if phase == "change" {
			writeFixture(t, workflow, "CHANGED WORKFLOW")
			writeCodexFunctionCall(t, w, "action", "shell_execute", `{}`)
			return
		}
		if phase == "batch" {
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"first","name":"shell_execute","arguments":"{}"},{"type":"function_call","call_id":"second","name":"shell_execute","arguments":"{}"}]}`)
			return
		}
		step++
		switch step {
		case 1:
			writeCodexFunctionCall(t, w, "shell", "shell_execute", `{}`)
		case 2:
			writeCodexFunctionCall(t, w, "fetch", "web_fetch", `{}`)
		default:
			fmt.Fprint(w, `{"output_text":"done"}`)
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	executions := 0
	for _, name := range []string{"shell_execute", "web_fetch"} {
		provider.tools.byName[name] = func(context.Context, string) (string, error) { executions++; return "ok", nil }
	}
	if _, err := provider.Reply(context.Background(), "gpt-test", "perform a task"); err != nil || executions != 2 {
		t.Fatalf("direct tools: executions=%d error=%v", executions, err)
	}

	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	phase = "change"
	if _, err := session.ReplyScheduled(context.Background(), "perform task"); !errors.Is(err, ErrSessionBusy) || SkillRecoveryMessage(err) != skillChangedMessage || executions != 2 {
		t.Fatalf("changed selected skill reached tool: executions=%d error=%v", executions, err)
	}
	writeFixture(t, workflow, "PRIVATE WORKFLOW TEXT")
	if _, err := provider.Reply(context.Background(), "gpt-test", "perform task"); SkillRecoveryMessage(err) != skillChangedMessage || executions != 2 {
		t.Fatalf("changed unselected skill reached tool: executions=%d error=%v", executions, err)
	}

	writeFixture(t, workflow, "PRIVATE WORKFLOW TEXT")
	phase = "batch"
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		executions++
		writeFixture(t, workflow, "CHANGED DURING FIRST ACTION")
		return "ok", nil
	}
	if _, err := session.ReplyScheduled(context.Background(), "perform two tasks"); err == nil || errors.Is(err, ErrSessionBusy) || SkillRecoveryMessage(err) != skillChangedMessage || !strings.Contains(SkillRecoveryMessage(err), "Some actions may already have completed") || executions != 3 {
		t.Fatalf("changed guidance did not stop second action safely: executions=%d error=%v", executions, err)
	}
}

func TestInvalidUnselectedAuthorizationStopsBeforeModelOrTools(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "skills", "fixture-skill", "AUTH.md"), "bad UTF-8: \xff")
	backendCalls := 0
	var received string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received = string(data)
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	_, err = provider.Reply(context.Background(), "gpt-test", "run a tool")
	if SkillRecoveryMessage(err) != skillResetMessage || backendCalls != 0 {
		t.Fatalf("invalid authorization reached backend: calls=%d, error=%v", backendCalls, err)
	}

	writeFixture(t, filepath.Join(root, "skills", "fixture-skill", "AUTH.md"), "FIXTURE AUTHORIZATION")
	for _, name := range []string{"large-a", "large-b", "large-c"} {
		dir := filepath.Join(root, "skills", name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: large fixture\n")
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "required authorization for "+name)
		writeFixture(t, filepath.Join(dir, "SKILL.md"), strings.Repeat(name, (256<<10)/len(name)))
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := session.Reply(context.Background(), "hello")
		if !errors.Is(err, artefacts.ErrGuidanceTooLarge) || SkillRecoveryMessage(err) != skillSizeMessage || backendCalls != 0 {
			t.Fatalf("oversized unselected skills reached backend: calls=%d, error=%v", backendCalls, err)
		}
		if _, err := session.Reply(context.Background(), "/clear"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "skills", "large-c")); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "hello"); err != nil || backendCalls != 1 {
		t.Fatalf("bounded guidance did not resume: calls=%d, error=%v", backendCalls, err)
	}
	for _, name := range []string{"large-a", "large-b"} {
		workflow := strings.Repeat(name, (256<<10)/len(name))
		if !strings.Contains(received, workflow) || !strings.Contains(received, "required authorization for "+name) {
			t.Fatalf("request omitted or shortened %s guidance", name)
		}
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

func TestTerminalShowsSafeSkillRecovery(t *testing.T) {
	for _, message := range []string{skillResetMessage, skillChangedMessage, skillBudgetMessage, skillSizeMessage, skillSaveMessage} {
		if !IsSkillRecoveryMessage(message) || !strings.Contains(message, skillActionNotice) || strings.Contains(message, "restart the workflow") {
			t.Fatalf("unsafe or unrecognized recovery message: %q", message)
		}
	}
	private := strings.Repeat("private-detail-", 1000)
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		return "", skillRecovery(errors.New(private), skillResetMessage)
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n/clear\n"), interactive: true}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{interactive: true, width: 200})
	if err != nil || !strings.Contains(output.String(), "/clear") || !strings.Contains(output.String(), "Conversation history cleared") || strings.Contains(output.String(), "private-detail") {
		t.Fatalf("terminal output = %q, error=%v", output.String(), err)
	}
	output.Reset()
	err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n")}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{})
	if err == nil || err.Error() != skillResetMessage || strings.Contains(err.Error(), "private-detail") {
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
