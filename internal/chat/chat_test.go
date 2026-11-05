package chat

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/localenv"
)

func TestConfig(t *testing.T) {
	config, err := ParseConfig(nil, io.Discard)
	if err != nil || config.Model != DefaultModel {
		t.Fatalf("defaults: %+v, %v", config, err)
	}
	for _, args := range [][]string{{"unexpected"}, {"-unknown"}, {"-auth-file"}, {"-offline"}, {"-echo-unavailable-model", "gpt-6.1-sol"}} {
		if _, err := ParseConfig(args, io.Discard); err == nil {
			t.Errorf("expected invalid configuration: %q", args)
		}
	}
	if _, err := ParseConfig([]string{"-help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
}

type providerFunc func(context.Context, string, string) (string, error)

func (f providerFunc) Reply(ctx context.Context, model, message string) (string, error) {
	return f(ctx, model, message)
}

func defaultConfig(t *testing.T) Config {
	t.Helper()
	config, err := ParseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestSessionReportsUnavailableModelWithoutRetryingAnotherModel(t *testing.T) {
	var models []string
	unavailable := fmt.Errorf("temporarily unavailable: %w", ErrModelUnavailable)
	provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
		models = append(models, model)
		if message != "hello" {
			t.Errorf("message = %q", message)
		}
		return "", unavailable
	})
	registry := NewModelRegistryAt(filepath.Join(t.TempDir(), "openai-models.json"))
	session := NewSession(Config{Registry: registry}, provider)
	_, err := session.Reply(context.Background(), "hello")
	if !errors.Is(err, unavailable) || !strings.Contains(err.Error(), DefaultModel) {
		t.Fatalf("error = %v, want direct error for %s", err, DefaultModel)
	}
	_, err = session.Reply(context.Background(), "hello")
	if !errors.Is(err, unavailable) || !reflect.DeepEqual(models, []string{DefaultModel, DefaultModel}) {
		t.Fatalf("models = %v, error = %v; want the configured model on both requests", models, err)
	}
}

func TestTerminalClearPersistsWithoutProviderAndPreservesMarkdownMemory(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(sessionPath)
	priorMessages := []ChatMessage{
		{Role: "user", Content: "keep no longer"},
		{Role: "assistant", Content: "old response"},
	}
	if err := store.saveState("old conversation summary", priorMessages); err != nil {
		t.Fatal(err)
	}

	memory := NewMemoryStore(filepath.Join(t.TempDir(), "workspace"))
	if err := os.MkdirAll(filepath.Dir(memory.MemoryPath()), memoryDirectoryMode); err != nil {
		t.Fatal(err)
	}
	protectedMemory := []byte("verified durable fact\n")
	if err := os.WriteFile(memory.MemoryPath(), protectedMemory, memoryFileMode); err != nil {
		t.Fatal(err)
	}

	providerCalls := 0
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		providerCalls++
		return "", errors.New("provider unavailable")
	})
	session, err := NewSessionWithStoreAndMemory(defaultConfig(t), provider, store, memory)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWithTestEnvStore(t, context.Background(), strings.NewReader("/clear\n"), &output, session); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Conversation history cleared.") {
		t.Fatalf("terminal did not confirm the clear: %q", output.String())
	}
	if providerCalls != 0 {
		t.Fatalf("clear called the unavailable provider %d times", providerCalls)
	}
	loaded, err := NewSessionStoreAt(sessionPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 0 || loaded.Summary != "" {
		t.Fatalf("persisted conversation after clear = %+v, want empty history and summary", loaded)
	}
	conversationInfo, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if conversationInfo.Mode().Perm() != 0600 {
		t.Fatalf("cleared conversation permissions = %04o, want 0600", conversationInfo.Mode().Perm())
	}
	gotMemory, err := os.ReadFile(memory.MemoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotMemory, protectedMemory) {
		t.Fatalf("protected Markdown memory changed: %q", gotMemory)
	}

	_, err = NewSessionWithStoreAndMemory(defaultConfig(t), provider, NewSessionStoreAt(sessionPath), memory)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalClearRejectsExtraArgumentsWithoutChangingSavedHistory(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(sessionPath)
	wantMessages := []ChatMessage{{Role: "user", Content: "retain this"}}
	if err := store.saveState("retained summary", wantMessages); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		providerCalls++
		return "", errors.New("provider unavailable")
	})
	session, err := NewSessionWithStore(defaultConfig(t), provider, NewSessionStoreAt(sessionPath))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWithTestEnvStore(t, context.Background(), strings.NewReader("/clear extra\n"), &output, session); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Usage: /clear") {
		t.Fatalf("terminal did not explain malformed usage: %q", output.String())
	}
	if providerCalls != 0 {
		t.Fatalf("malformed clear called the provider %d times", providerCalls)
	}
	loaded, err := NewSessionStoreAt(sessionPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != "retained summary" || !reflect.DeepEqual(loaded.Messages, wantMessages) {
		t.Fatalf("malformed clear changed saved history: %+v", loaded)
	}
}

func TestRequestErrorClassificationIsTypedAndPreservesCause(t *testing.T) {
	failure := errors.New("provider failed")
	recoverable := recoverableRequestError(failure)
	if classifyRequestError(recoverable) != requestErrorRecoverable || !errors.Is(recoverable, failure) || !errors.Is(recoverable, errRecoverableRequest) {
		t.Fatalf("recoverable classification = %v", recoverable)
	}
	fatal := fatalRequestError(failure)
	if classifyRequestError(fatal) != requestErrorFatal || !errors.Is(fatal, failure) || !errors.Is(fatal, errFatalRequest) {
		t.Fatalf("fatal classification = %v", fatal)
	}
	if classifyRequestError(failure) != requestErrorFatal {
		t.Fatal("unclassified errors did not default to the fatal boundary")
	}
	if classifyRequestError(recoverableRequestError(context.Canceled)) != requestErrorFatal {
		t.Fatal("context cancellation crossed the recoverable boundary")
	}
}

func TestLoop(t *testing.T) {
	var output bytes.Buffer
	var messages []string
	requests := []string{
		"Provision a new OVH server.",
		"Create a new OVH server and install GoAster on it.",
		"Install GoAster on my existing OVH server.",
		"Find and explain this public document.",
		"Help my Telegram bot receive messages in a group.",
		"Review my daily memory notes and propose durable facts to keep.",
	}
	provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
		messages = append(messages, message)
		return "received " + message, nil
	})
	err := runWithTestEnvStore(t, context.Background(), strings.NewReader("\n hello \n"+strings.Join(requests, "\n")+"\n/history\n/clear\n/quit\n/exit\nsecond\n"), &output, NewSession(defaultConfig(t), provider))
	wantMessages := append([]string{"hello"}, requests...)
	wantMessages = append(wantMessages, "/history", "/quit", "/exit", "second")
	if err != nil || !reflect.DeepEqual(messages, wantMessages) {
		t.Fatalf("messages = %q, error = %v", messages, err)
	}
	for _, want := range []string{
		"chat provider", "Type a message, then press Enter.",
		"- /env: securely import environment variables.",
		"- /get-env: edit the current env file or get a blank template.",
		"- /file: inspect non-secret files.",
		"- /clear: clear this conversation's history.",
		"Active model: gpt-6.1-sol", "you> ", "assistant [gpt-6.1-sol]> received hello",
		"assistant [gpt-6.1-sol]> received /history", "Conversation history cleared.",
		"assistant [gpt-6.1-sol]> received /quit", "assistant [gpt-6.1-sol]> received /exit",
		"assistant [gpt-6.1-sol]> received second",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	if strings.Contains(output.String(), "Ctrl-D") {
		t.Errorf("startup help unexpectedly contains Ctrl-D: %s", &output)
	}
	if strings.Contains(output.String(), "OVH setup has started.") {
		t.Errorf("ordinary-language requests unexpectedly entered the legacy /ovh display: %s", output.String())
	}
	if strings.Contains(output.String(), "Working...") {
		t.Errorf("redirected line protocol unexpectedly included an interactive working state: %s", output.String())
	}
}

func TestLoopReportsUnavailableModelDirectly(t *testing.T) {
	var output bytes.Buffer
	provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
		if model == DefaultModel {
			return "", ErrModelUnavailable
		}
		return "reply to " + message, nil
	})
	err := runWithTestEnvStore(t, context.Background(), strings.NewReader("hello"), &output, NewSession(defaultConfig(t), provider))
	if !errors.Is(err, ErrModelUnavailable) || !strings.Contains(err.Error(), DefaultModel) {
		t.Fatalf("loop error = %v, want direct model error", err)
	}
	if strings.Contains(output.String(), "gpt-5.6-luna") || strings.Contains(output.String(), "Fallback") {
		t.Fatalf("loop reported a fallback: %s", output.String())
	}
}

func TestInteractiveRequestLifecycleShowsWorkingAndRetries(t *testing.T) {
	var output bytes.Buffer
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	provider := providerFunc(func(ctx context.Context, _ string, message string) (string, error) {
		calls++
		if calls == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "first reply to " + message, nil
		}
		if calls == 2 {
			return "", errors.New("temporary provider failure")
		}
		return "retried reply", nil
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runChatLoopWithUI(
			context.Background(),
			TerminalInput{reader: strings.NewReader("first\nsecond\nthird\n"), interactive: true},
			&output,
			NewSession(defaultConfig(t), provider),
			store,
			EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 80},
		)
	}()
	<-started
	if !strings.Contains(output.String(), "Working...") {
		t.Fatalf("working state was not flushed before the delayed request: %q", output.String())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result := output.String()
	if calls != 3 || strings.Count(result, "Working...") != 3 {
		t.Fatalf("request lifecycle calls/output = %d/%q", calls, result)
	}
	if !strings.Contains(result, "If an earlier attempt may have failed or been interrupted") || !strings.Contains(result, "check whether any actions completed") || !strings.Contains(result, "retried reply") {
		t.Fatalf("missing recoverable error or retry reply: %q", result)
	}
	if !strings.Contains(result, "temporary provider failure") {
		t.Fatalf("provider error was omitted from the interactive request notice: %q", result)
	}
}

func TestInteractiveToolCallProgressShowsWhileToolRunsAndAfterCompletion(t *testing.T) {
	var output bytes.Buffer
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTool := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTool()
	provider := providerFunc(func(ctx context.Context, _ string, _ string) (string, error) {
		observer := toolCallObserver(ctx)
		if observer == nil {
			close(started)
			return "", errors.New("tool progress observer missing")
		}
		observer("shell_execute", `{"command":"printf PRIVATE_TOKEN","cwd":"/private/path"}`)
		close(started)
		select {
		case <-release:
			return "Command finished.", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runChatLoopWithUI(
			context.Background(),
			TerminalInput{reader: strings.NewReader("run command\n"), interactive: true},
			&output,
			NewSession(defaultConfig(t), provider),
			store,
			EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 80},
		)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("slow tool did not start")
	}
	progress := output.String()
	if !strings.Contains(progress, "Tool shell_execute: running a command") || !strings.Contains(progress, "Working...") {
		t.Fatalf("slow tool progress was not visible: %q", progress)
	}
	if strings.Contains(progress, "PRIVATE_TOKEN") || strings.Contains(progress, "/private/path") {
		t.Fatalf("tool arguments leaked into terminal progress: %q", progress)
	}
	releaseTool()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Command finished.") {
		t.Fatalf("completed tool reply was not displayed: %q", output.String())
	}
}

func TestInteractiveToolFailureShowsUnderlyingRequestError(t *testing.T) {
	var output bytes.Buffer
	calls := 0
	provider := providerFunc(func(ctx context.Context, _ string, _ string) (string, error) {
		calls++
		if calls == 1 {
			if observer := toolCallObserver(ctx); observer != nil {
				observer("web_fetch", `{"url":"https://example.test/?token=PRIVATE_TOKEN"}`)
			}
			return "", errors.New("private tool failure details")
		}
		return "Retry complete.", nil
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	err := runChatLoopWithUI(
		context.Background(),
		TerminalInput{reader: strings.NewReader("fetch\nretry\n"), interactive: true},
		&output,
		NewSession(defaultConfig(t), provider),
		store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
		terminalUI{interactive: true, width: 80},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := output.String()
	if !strings.Contains(result, "Tool web_fetch: fetching a public page") ||
		!strings.Contains(result, "If an earlier attempt may have failed or been interrupted") || !strings.Contains(result, "check whether any actions completed") || !strings.Contains(result, "Retry complete.") {
		t.Fatalf("tool failure and retry lifecycle was not displayed: %q", result)
	}
	if strings.Contains(result, "PRIVATE_TOKEN") || !strings.Contains(result, "private tool failure details") {
		t.Fatalf("tool arguments were exposed or provider details were omitted from terminal output: %q", result)
	}
}

func TestLineProtocolDoesNotAttachToolCallObserver(t *testing.T) {
	var output bytes.Buffer
	provider := providerFunc(func(ctx context.Context, _ string, _ string) (string, error) {
		if toolCallObserver(ctx) != nil {
			t.Error("line protocol unexpectedly attached a terminal tool progress observer")
		}
		return "done", nil
	})
	if err := runWithTestEnvStore(t, context.Background(), strings.NewReader("run\n"), &output, NewSession(defaultConfig(t), provider)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Tool ") || strings.Contains(output.String(), "Working...") {
		t.Fatalf("line protocol output changed: %q", output.String())
	}
}

func TestInteractiveAttachmentWorkingStateCleansUpAfterInspection(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(filePath, []byte("inspect this"), 0600); err != nil {
		t.Fatal(err)
	}
	var temporaryPath string
	started := make(chan struct{})
	release := make(chan struct{})
	provider := providerFunc(func(ctx context.Context, _ string, message string) (string, error) {
		if !strings.Contains(message, fileInspectionMessage) {
			return "ordinary reply", nil
		}
		_, temporaryPath, _ = strings.Cut(message, "local_path: ")
		temporaryPath = strings.TrimSpace(temporaryPath)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "File inspected.", nil
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runChatLoopWithUI(
			context.Background(),
			TerminalInput{reader: strings.NewReader("/file\n"), interactive: true},
			&output,
			NewSession(defaultConfig(t), provider),
			store,
			EnvironmentFilePickerFunc(func() (string, error) { return filePath, nil }),
			terminalUI{interactive: true, width: 80},
		)
	}()
	<-started
	if !strings.Contains(output.String(), "Working...") {
		t.Fatalf("working state was not flushed before attachment inspection: %q", output.String())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if temporaryPath == "" {
		t.Fatal("provider did not receive the temporary attachment path")
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary attachment remained after inspection: %v", err)
	}
	result := output.String()
	if !strings.Contains(result, "File inspection complete.") || !strings.Contains(result, "File inspected.") {
		t.Fatalf("interactive inspection did not report completion: %q", result)
	}
	if strings.Contains(result, temporaryPath) {
		t.Fatalf("temporary attachment path leaked into interactive output: %q", result)
	}
}

func TestInteractiveAttachmentFailureCleansUpBeforeRetry(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(filePath, []byte("inspect this"), 0600); err != nil {
		t.Fatal(err)
	}
	var temporaryPath string
	calls := 0
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		calls++
		if calls == 1 {
			_, temporaryPath, _ = strings.Cut(message, "local_path: ")
			temporaryPath = strings.TrimSpace(temporaryPath)
			return "", errors.New("inspection provider failed")
		}
		return "retry succeeded", nil
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runChatLoopWithUI(
		context.Background(),
		TerminalInput{reader: strings.NewReader("/file\nretry\n"), interactive: true},
		&output,
		NewSession(defaultConfig(t), provider),
		store,
		EnvironmentFilePickerFunc(func() (string, error) { return filePath, nil }),
		terminalUI{interactive: true, width: 80},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || temporaryPath == "" {
		t.Fatalf("attachment failure calls/path = %d/%q", calls, temporaryPath)
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary attachment remained after a failed inspection: %v", err)
	}
	result := output.String()
	if !strings.Contains(result, "If an earlier attempt may have failed or been interrupted") || !strings.Contains(result, "check whether any actions completed") || !strings.Contains(result, "retry succeeded") {
		t.Fatalf("failed attachment did not return to a successful retry: %q", result)
	}
	if strings.Contains(result, temporaryPath) || !strings.Contains(result, "inspection provider failed") {
		t.Fatalf("attachment failure leaked path or omitted provider error: %q", result)
	}
}

func TestInteractiveMalformedEnvironmentPathReturnsToPrompt(t *testing.T) {
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	err := runChatLoopWithUI(
		context.Background(),
		TerminalInput{reader: strings.NewReader("/env-file\nhello\n"), interactive: true},
		&output,
		NewSession(defaultConfig(t), provider),
		store,
		EnvironmentFilePickerFunc(func() (string, error) {
			return "", errors.New("picker must not run for malformed usage")
		}),
		terminalUI{interactive: true, width: 80},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want chat to continue after malformed path", messages)
	}
	if !strings.Contains(output.String(), "Expected exactly one non-empty file path.") || !strings.Contains(output.String(), "received") {
		t.Fatalf("interactive malformed path did not return to chat: %q", output.String())
	}
}

func TestLoopStartsOVHOnboardingWithBoundedRoutineApprovalGuidance(t *testing.T) {
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "OVH skill response", nil
	})

	if err := runWithTestEnvStore(t, context.Background(), strings.NewReader("/ovh\n"), &output, NewSession(defaultConfig(t), provider)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, []string{"/ovh"}) {
		t.Fatalf("provider messages = %q, want /ovh", messages)
	}
	for _, want := range []string{
		"OVH setup has started.",
		"I can list your existing OVH servers using read-only checks.",
		"A missing GoAster connection record on this computer does not mean that no server exists.",
		"Would you like to connect to an existing server or create a new one?",
		"If you choose an existing server, I will list servers read-only and ask you to select the exact one before preparing a connection.",
		"If you choose a new server, I will show its exact configuration and cost before asking for your explicit approval to create it.",
		"Starting this screen does not create or change anything.",
		"For the server you select, I will include its exact address in the routine checklist and ask once for approval before contacting it.",
		"Selecting or checking an existing server never creates one.",
		"Approval applies only to the listed actions and that server.",
		"If anything unexpected or outside the plan comes up, I will pause and ask only for the needed input or approval.",
		"Approval does not guarantee setup will succeed.",
		"I will report progress as I go.",
		"If the selected server is ready, I will say one of these:",
		"The server is ready, but Telegram still needs setup. Do not test Telegram yet.",
		"Setup is complete. Please send one normal message to the Telegram bot and check that it replies.",
		"Creating an OVH resource, making a change that could cost money, or making another high-impact change always needs separate explicit approval.",
		"assistant [gpt-6.1-sol]> OVH skill response",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	orderedKickoffGuidance := []string{
		"I can list your existing OVH servers using read-only checks.",
		"Would you like to connect to an existing server or create a new one?",
		"If you choose a new server, I will show its exact configuration and cost before asking for your explicit approval to create it.",
	}
	previousPosition := -1
	for _, guidance := range orderedKickoffGuidance {
		position := strings.Index(output.String(), guidance)
		if position <= previousPosition {
			t.Errorf("OVH kickoff guidance %q is out of order", guidance)
		}
		previousPosition = position
	}
	for _, outdated := range []string{
		"If no server is saved, I will guide you through creating one.",
		"one simple next step at a time",
		"one approval at a time",
		"approval before each routine step",
	} {
		if strings.Contains(output.String(), outdated) {
			t.Errorf("onboarding start still implies per-step routine approval %q: %s", outdated, output.String())
		}
	}
	if strings.Contains(output.String(), "POST /cloud/project") || strings.Contains(output.String(), "automatically create") {
		t.Errorf("onboarding output contains account-level project creation guidance: %s", output.String())
	}
}

func TestOVHRemoteConsolidatedRoutineApprovalIsExplicitAndBounded(t *testing.T) {
	remoteSkill := readRepositorySkill(t, "ovh-remote")
	policy := strings.Join(strings.Fields(remoteSkill), " ")
	checkStart := strings.Index(remoteSkill, "After the target-specific routine checklist is approved")
	checkEnd := strings.Index(remoteSkill, "After a newly created host")
	if checkStart < 0 || checkEnd <= checkStart {
		t.Fatal("saved-server check guidance missing")
	}
	check := strings.Join(strings.Fields(remoteSkill[checkStart:checkEnd]), " ")
	for _, want := range []string{"http://127.0.0.1:8765/livez", "does not check whether its separate OpenAI login is usable", "can refresh and write credentials", "obtain separate approval"} {
		if !strings.Contains(check, want) {
			t.Errorf("saved-server check missing %q", want)
		}
	}
	if strings.Contains(check, "http://127.0.0.1:8765/healthz") {
		t.Error("read-only saved-server command invokes healthz")
	}
	for _, want := range []string{
		"## Consolidated routine approval for onboarding",
		"Show one checklist for that exact target before the first remote connection or mutation.",
		"Ask for one unambiguous approval of the listed routine plan.",
		"Target: [exact OVH host name or address]",
		"After the target-specific routine checklist is approved, perform the read-only liveness check",
		"No server check runs merely because GoAster starts.",
		"StrictHostKeyChecking=yes",
		"UpdateHostKeys=no",
		"BatchMode=yes",
		"/usr/local/share/goaster/skills",
		"Read-only inspection and verification:",
		"Local connection record (if needed): Save or refresh the verified server connection on this computer for future checks.",
		"Installation and build actions (if required):",
		"Starting or restarting the already-reviewed GoAster services (if required):",
		"Protected sign-in process handoff (if required):",
		"Telegram token and pairing setup (include only if the token was already supplied through the local `/env` flow):",
		"this approval covers exactly the listed actions and that target.",
		"It does not authorize another server, creating or changing OVH resources or costs, destructive changes or unexpected file replacement, unrelated commands or any other unlisted action, new service definitions, Telegram privacy changes, or changes to firewall, SSH, or user configuration.",
		"If a step is unexpected, changes scope, needs input not yet supplied, requires a new service definition, has financial or destructive impact, or cannot be safely predicted, stop before that step and ask only for the specific input or approval needed.",
		"Do not repeat the chat approval request before each listed step",
		"Approval of the routine bundle does not mean onboarding will succeed",
		"Count business steps, never individual shell commands.",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH remote approval policy missing %q", want)
		}
	}

	for _, outdated := range []string{
		"Before each stage, show the exact script",
		"Apply approval separately to:",
		"obtain fresh explicit approval immediately before running it",
		"Starting or restarting either unit also needs its own approval.",
		"Immediately before each, present the exact temporary script",
		"show the exact target and local key to the user in chat",
	} {
		if strings.Contains(policy, outdated) {
			t.Errorf("OVH remote approval policy still requires repeated approval: %q", outdated)
		}
	}

	checklistStart := strings.Index(remoteSkill, "Target: [exact OVH host name or address]")
	if checklistStart < 0 {
		t.Fatal("OVH remote approval policy is missing the target-specific checklist")
	}
	checklistTail := remoteSkill[checklistStart:]
	checklistEnd := strings.Index(checklistTail, "\n\nAsk the user to approve")
	if checklistEnd < 0 {
		t.Fatal("OVH remote approval checklist has no clear end")
	}
	checklist := checklistTail[:checklistEnd]
	technicalDetails := regexp.MustCompile(`(?i)\b(?:ssh|port|systemd|root|healthz|shell_execute|process_handoff)\b|api endpoint|known_hosts|key path|raw commands?`)
	if detail := technicalDetails.FindString(checklist); detail != "" {
		t.Errorf("user-facing OVH routine checklist exposes internal detail %q", detail)
	}
}

func TestOVHAccountRegionIsAutoDetectedAfterLocalEnv(t *testing.T) {
	provisioningSkill := readRepositorySkill(t, "ovh")
	policy := strings.Join(strings.Fields(provisioningSkill), " ")
	for _, requirement := range []string{
		"Before probing any account/API endpoint, verify that all three required OVH credential values (`OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, and `OVH_CONSUMER_KEY`) are available through the local `/env` flow.",
		"After the OVH credentials are available through the local `/env` flow, proactively probe every supported account/API endpoint with the same credentials and the existing read-only `GET /cloud/project` discovery request.",
		"Do not ask the user to identify the account/API region.",
		"Record every successful probe",
		"select a successful endpoint using table order (Europe, Canada, United States)",
		"Report the detected account/API region and ask only for a simple confirmation",
		"If no supported region works, stop with a clear setup error",
	} {
		if !strings.Contains(policy, requirement) {
			t.Errorf("OVH account-region auto-detection policy is missing %q", requirement)
		}
	}
	for _, endpoint := range []string{
		"https://eu.api.ovh.com",
		"https://ca.api.ovh.com",
		"https://api.us.ovhcloud.com",
	} {
		if !strings.Contains(policy, endpoint) {
			t.Errorf("OVH account-region auto-detection policy is missing supported endpoint %q", endpoint)
		}
	}
}

func TestOVHProjectDiscoveryAcceptsNameMismatchAndWaitsForConfirmation(t *testing.T) {
	provisioningSkill := readRepositorySkill(t, "ovh")
	policy := strings.Join(strings.Fields(provisioningSkill), " ")
	for _, requirement := range []string{
		"At the start of OVH onboarding, explain that GoAster can list existing OVH servers with read-only checks, then ask whether the user wants to connect to an existing server or create a new one.",
		"A missing GoAster connection record on this computer only means that no local record is available; it does not mean that no OVH server exists, express intent to create one, or authorize creation.",
		"list that project's existing instances using read-only checks.",
		"Present the candidates and ask the user to select the exact server before following the OVH remote SSH skill.",
		"If no usable project or server is found, report that result and ask what the user wants to do next; do not switch to creating a server.",
		"That choice does not approve creation",
		"before any server or other OVH resource is created, present its exact configuration and price estimate and obtain the user's explicit approval for that creation.",
	} {
		if !strings.Contains(policy, requirement) {
			t.Errorf("OVH onboarding path policy is missing %q", requirement)
		}
	}
	if strings.Contains(policy, "If no server is saved, I will guide you through creating one.") {
		t.Fatal("OVH onboarding policy still treats a missing saved server as a request to create one")
	}
	if !strings.Contains(policy, "an active project named `Production` remains usable even though its name differs from `GoAster`") {
		t.Fatal("OVH project discovery policy does not cover a non-GoAster project name")
	}
	workflowStart := strings.Index(policy, "2. Use `GET /cloud/project` to list Public Cloud projects.")
	if workflowStart < 0 {
		t.Fatal("OVH provisioning workflow is missing its project-discovery step")
	}
	workflow := policy[workflowStart:]
	steps := []string{
		"Do not check the project name against `GoAster` or any other expected name.",
		"Present each discovered project's name, ID, and status.",
		"ask whether the user wants to use it",
		"Wait for confirmation or selection before continuing.",
		"That answer permits only read-only inspection.",
		"Use the preferred deployment region if known; otherwise ask for it.",
		"Continue with read-only checks for its regional quota, available regions, images, flavors,",
		"public network, stored SSH keys, matching local SSH identities, and existing instances.",
		"Select the smallest available machine suitable for the workload.",
		"If no usable project key has a matching local private identity, generate a",
		"dedicated local Ed25519 keypair automatically.",
		"Before asking for approval to create a server or other OVH resource, use the applicable Public price estimate above.",
		"Present the exact proposed resource, assumptions, SSH rule, and brief estimate.",
		"If applicable pricing is unknown, say so",
		"get the user's acknowledgment before requesting approval.",
		"Before creating any server or other OVH resource, changing any billable resource, deleting or replacing",
		"For resource creation, the confirmation must",
		"follow the read-only assumptions and price report (including a clear statement when price is unknown)",
		"configuration.",
	}
	previous := -1
	for _, step := range steps {
		position := strings.Index(workflow, step)
		if position < 0 {
			t.Errorf("OVH project workflow is missing %q", step)
			continue
		}
		if position <= previous {
			t.Errorf("OVH project workflow step %q is out of order", step)
		}
		previous = position
	}
	for _, want := range []string{
		"For the standard configuration of one d2-2 instance with one public IPv4 on pay-as-you-go pricing",
		"use this rough illustrative baseline without a web search",
		"official OVH US Public Cloud price list](https://us.ovhcloud.com/public-cloud/prices/), dated 2026-11-05",
		"d2-2 at US$0.0129/hour and public IPv4 at US$0.00319/hour",
		"US$0.01609/hour; a 20% buffer gives US$0.019308/hour, shown as roughly **US$0.02/hour**",
		"At 730 hours per month, that is US$14.095/month, rounded up to roughly **US$15/month**",
		"before tax",
		"rather than an exact quote or guaranteed ceiling",
		"assumes no Savings Plan discount",
		"OVH prices vary by account and region",
		"other resources (including storage) and tax are extra",
		"For a test lasting less than one hour, explain that it may cost roughly US$0.02 if deleted promptly; this is not a guarantee or maximum.",
		"The IPv4 address and other retained resources may continue to cost after the instance is deleted.",
		"Warn the user to clean up the address and any other billable resources as well as the server.",
		"Do not apply this baseline to a different instance type, address option, billing option, or additional resource.",
		"say its cost is unknown unless applicable official pricing has otherwise been verified",
		"get the user's acknowledgment of that unknown cost before requesting exact creation approval.",
		"No web search is mandatory.",
		"label the US figure as an illustrative reference and make the actual regional/account cost uncertainty clear.",
		"this roughly US$15/month estimate exceeds it, so get the user's explicit budget override before creation.",
		"Even with an override, obtain separate explicit approval for the exact billable configuration",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH provisioning price guidance missing requirement %q", want)
		}
	}
	if !strings.Contains(policy, "Confirmation for one action does not authorize a different action.") {
		t.Fatal("OVH project selection confirmation must remain distinct from resource-creation approval")
	}
	for _, want := range []string{
		"Never ask the user to provide a key.",
		"Generate it non-interactively with an empty passphrase (`ssh-keygen -N \"\"`)",
		"never print, upload, or report it; only the public `.pub` key may be sent to OVH.",
		"from unrestricted sources by default (`0.0.0.0/0`, and `::/0` when IPv6 rules apply).",
		"Do not require or ask for a trusted source IP.",
		"Do not guess a price or stop solely because pricing could not be retrieved.",
		"explicit approval of that exact configuration and, when pricing is unavailable, their acknowledgment that the current cost could not be verified.",
		"data or resources, registering the generated public SSH key, or changing access or network exposure",
		"Do not exceed the US$8/month default ceiling when a known estimate is available without an explicit budget override.",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH provisioning policy missing onboarding requirement %q", want)
		}
	}
}

func TestOVHInstanceCreationUsesVerifiedDirectRequestAndReconcilesAmbiguity(t *testing.T) {
	provisioningSkill := readRepositorySkill(t, "ovh")
	policy := strings.Join(strings.Fields(provisioningSkill), " ")
	for _, requirement := range []string{
		"Do not download the complete OVH API specification, schema bundle, SDK, or CLI as a prerequisite for ordinary onboarding.",
		"POST /1.0/cloud/project/{serviceName}/instance",
		"\"name\": \"<selected instance name>\"",
		"\"flavorId\": \"<selected flavor id>\"",
		"\"imageId\": \"<selected image id>\"",
		"\"region\": \"<selected deployment region>\"",
		"\"sshKeyId\": \"<registered project SSH key id>\"",
		"\"monthlyBilling\": false",
		"The schema marks `name`, `flavorId`, and `region` as required",
		"GET /1.0/cloud/project/{serviceName}/instance/{instanceId}",
		"GET /1.0/cloud/project/{serviceName}/instance?region=<selected deployment region>",
		"A name alone is never enough",
		"Never blindly repeat the POST.",
		"If the POST times out, the transport fails, returns a non-success result, or returns no usable instance ID, treat the outcome as ambiguous.",
		"If reconciliation finds exactly one attributable instance, verify that exact ID",
		"If it finds none, more than one, a mismatch, or the read-only verification is unavailable, stop and report the missing or ambiguous evidence",
	} {
		if !strings.Contains(policy, requirement) {
			t.Errorf("OVH instance creation policy is missing %q", requirement)
		}
	}
	for _, unsafe := range []string{
		"Download the complete OVH API specification",
		"blindly retry the POST",
		"retry the POST immediately",
	} {
		if strings.Contains(policy, unsafe) {
			t.Errorf("OVH instance creation policy contains unsafe prerequisite or retry guidance %q", unsafe)
		}
	}
}

func TestOVHRemoteNewInstanceUsesScopedTrustOnFirstUse(t *testing.T) {
	remoteSkill := readRepositorySkill(t, "ovh-remote")
	policy := strings.Join(strings.Fields(remoteSkill), " ")
	for _, want := range []string{
		"Pre-existing hosts, saved targets outside the same successful provisioning flow, and hosts whose provenance is unclear must have an already trusted `known_hosts` entry.",
		"If such a host has no trusted entry, obtain and compare its host-key fingerprint through a trusted channel and get explicit approval before connecting.",
		"Never use trust-on-first-use for a pre-existing target, an unrelated target, or a target whose provenance is unclear.",
		"provisioning skill has just successfully created and verified it in this same provisioning flow.",
		"exact instance ID returned by the successful create operation, current public IP returned and verified during provisioning, and approved local SSH identity.",
		"query the selected OVH project read-only by the exact instance ID returned by the create operation.",
		"returned instance ID matches exactly",
		"the instance is ready",
		"current public IP matches the expected IP verified and reported by the provisioning workflow.",
		"Use only the approved local private key whose matching public key was registered for this instance",
		"never ask the user to retrieve, compare, approve, or paste an SSH host-key fingerprint for this newly created server.",
		"If the API lookup fails, the instance ID or IP differs",
		"do not connect or accept a host key.",
		"unique target-specific file under the local `~/.ssh` directory",
		"make sure this file does not already exist or resolve through a symlink.",
		"-o BatchMode=yes",
		"-o StrictHostKeyChecking=accept-new",
		"-o UserKnownHostsFile=<target-specific-known_hosts-file>",
		"-o GlobalKnownHostsFile=none",
		"`accept-new` stores the first key presented by the verified IP in this instance's file and rejects a changed key.",
		"This is trust-on-first-use: the OVH lookup confirms the target, not the SSH host key itself.",
		"Do not use `ssh-keyscan` or another separately collected fingerprint as an independent trust source",
		"After that first SSH attempt, whether it succeeds or fails, use the same target-specific file",
		"`StrictHostKeyChecking=yes` with `UpdateHostKeys=no` for every SSH command",
		"Set `GlobalKnownHostsFile=none` with this explicit file so no other known-hosts source can authorize a changed key.",
		"Pass the file in `process_handoff`'s optional `known_hosts` field",
		"Never use `accept-new` again for this target.",
		"If the first attempt fails, inspect the OVH instance, exact IP, and known-hosts file before any retry",
		"If no key was saved, stop; do not ask the user to supply or approve a fingerprint for this newly created server.",
		"the first preflight automatically records that entry in its target-specific file; pass that file to the handoff without asking the user to retrieve, compare, approve, or paste a fingerprint.",
		"For a pre-existing host or a host whose provenance is unclear and which has no trusted entry, obtain and compare the fingerprint through a trusted channel and get explicit approval before connecting.",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH remote SSH policy missing first-contact trust safeguard %q", want)
		}
	}
	for _, outdated := range []string{
		"For a first connection, obtain and compare the host-key fingerprint through a trusted channel",
		"obtain and compare a first-connection fingerprint through a trusted channel before running it",
	} {
		if strings.Contains(policy, outdated) {
			t.Errorf("OVH remote SSH policy still requires manual fingerprint verification for every first connection: %q", outdated)
		}
	}
	instanceCheck := strings.Index(policy, "Before any SSH connection, query the selected OVH project read-only")
	acceptNew := strings.Index(policy, "-o StrictHostKeyChecking=accept-new")
	if instanceCheck < 0 || acceptNew <= instanceCheck {
		t.Fatal("OVH instance and IP verification must precede first-use host-key acceptance")
	}
	for _, prerequisite := range []string{
		"returned instance ID matches exactly",
		"the instance is ready",
		"current public IP matches the expected IP verified and reported by the provisioning workflow.",
		"Use only the approved local private key whose matching public key was registered for this instance",
		"do not connect or accept a host key.",
		"make sure this file does not already exist or resolve through a symlink.",
	} {
		position := strings.Index(policy, prerequisite)
		if position <= instanceCheck || position >= acceptNew {
			t.Errorf("first-use host-key acceptance precedes prerequisite %q", prerequisite)
		}
	}
	if strings.Count(policy, "-o StrictHostKeyChecking=accept-new") != 1 {
		t.Error("OVH remote SSH policy must specify only one first-use acceptance command")
	}

	for path, requirements := range map[string][]string{
		"skills/ovh/SKILL.md": {
			"exact instance ID returned by the successful create operation, current public IP returned and verified during provisioning, and approved local SSH identity",
			"Whenever SSH contact is part of the requested provisioning flow for a newly created instance",
			"the OVH remote SSH workflow in this same provisioning flow.",
			"automatically records the first host key in a unique target-specific `known_hosts` file with `StrictHostKeyChecking=accept-new`; all later SSH connections use that same file with `StrictHostKeyChecking=yes`.",
			"Never ask the user to retrieve, compare, approve, or paste an SSH host-key fingerprint for this newly created server",
			"Trusted-channel fingerprint verification is reserved for pre-existing hosts or hosts whose provenance is unclear when they lack an already trusted entry.",
		},
		"skills/ovh-remote/AUTH.md": {
			"exact instance created and verified in the same provisioning flow",
			"unique target-specific `known_hosts` file using `StrictHostKeyChecking=accept-new`; use that file with `StrictHostKeyChecking=yes` for all later connections.",
			"Never ask the user to retrieve, compare, approve, or paste its SSH host-key fingerprint.",
			"For pre-existing or provenance-unclear hosts, require an already trusted entry or trusted-channel fingerprint verification and approval before connecting.",
		},
		"README.md": {
			"exact instance just created and verified in the same provisioning flow",
			"unique target-specific `known_hosts` file using `StrictHostKeyChecking=accept-new`.",
			"All later connections, including the protected login, use that same file with `StrictHostKeyChecking=yes`.",
			"You will never be asked to retrieve, compare, approve, or paste an SSH host-key fingerprint for this newly created server.",
			"Pre-existing hosts and hosts of unclear provenance need an already trusted entry or fingerprint verification through a trusted channel and approval before connecting.",
		},
		"docs/USER-JOURNEY.md": {
			"automatically establishes trust for that newly created and verified instance during the same provisioning flow",
			"`StrictHostKeyChecking=accept-new`",
			"unique target-specific `known_hosts` file",
			"This is trust on first use: the OVH lookup confirms the target, not the SSH host key itself.",
			"`StrictHostKeyChecking=yes`",
			"You are never asked to retrieve, compare, approve, or paste an SSH host-key fingerprint for this newly created server.",
			"Pre-existing hosts and hosts of unclear provenance need a trusted `known_hosts` entry or fingerprint verification through a trusted channel and approval before connection.",
		},
	} {
		content := readFixture(t, filepath.Join("..", "..", filepath.FromSlash(path)))
		document := strings.Join(strings.Fields(content), " ")
		for _, want := range requirements {
			if !strings.Contains(document, want) {
				t.Errorf("%s is missing the new-instance trust requirement %q", path, want)
			}
		}
	}
}

func TestOVHRootSSHRedirectDocumentationContract(t *testing.T) {
	const contract = "Every GoAster installation and bootstrap operation must run as `root`. The SSH target must be `root@SERVER`, and `root@SERVER` must execute as `root`. Before any installation or bootstrap action, perform an explicit read-only root identity/privilege check in that session: run `id -u` and `id -un`, and require `id -u` to return `0` and `id -un` to return `root`. If root SSH is redirected to `ubuntu` by the OVH forced-command/account policy, stop all installation and bootstrap work immediately. Do not continue as `ubuntu`, use `sudo` from that session, or silently substitute another account. GoAster must use its approved programmatic OVH administrative/recovery path to inspect and repair the root account's SSH authorization and forced-command/account policy, including removing or correcting the redirect to `ubuntu` so root connections execute as `root`; the user must not be asked to perform this remediation manually. If that approved programmatic remediation path is unavailable or fails, stop and report the failure clearly. After a successful repair, reconnect specifically to `root@SERVER` and repeat the explicit `id -u`/`id -un` check; continue only after it confirms effective UID `0` and user `root`."
	const outdatedManualRemediation = "use OVH Manager/console/recovery access or another approved administrative path to inspect"
	for _, path := range []string{
		"skills/ovh-remote/SKILL.md",
		"docs/USER-JOURNEY.md",
	} {
		document := strings.Join(strings.Fields(readFixture(t, filepath.Join("..", "..", filepath.FromSlash(path)))), " ")
		if !strings.Contains(document, contract) {
			t.Errorf("%s is missing the OVH root SSH redirect guidance", path)
		}
		if strings.Contains(document, outdatedManualRemediation) {
			t.Errorf("%s still assigns root SSH remediation to a manual administrative path", path)
		}
	}
}

func TestOVHRemoteNewInstanceTrustInstructionCanonicalContract(t *testing.T) {
	remoteSkill := readRepositorySkill(t, "ovh-remote")
	// § stands in for a Markdown backtick inside these Go raw-string fixtures.
	const trustScopeTemplate = `Pre-existing hosts, saved targets outside the same successful provisioning flow, and hosts whose provenance is unclear must have an already trusted §known_hosts§ entry. If such a host has no trusted entry, obtain and compare its host-key fingerprint through a trusted channel and get explicit approval before connecting. Never use trust-on-first-use for a pre-existing target, an unrelated target, or a target whose provenance is unclear. The exact instance created and verified in the same provisioning flow uses the automatic first-contact procedure below.`
	trustScope := strings.ReplaceAll(trustScopeTemplate, "§", "`")
	if !strings.Contains(strings.Join(strings.Fields(remoteSkill), " "), trustScope) {
		t.Fatal("manual trusted-channel fingerprint verification must be scoped to pre-existing or provenance-unclear targets")
	}

	const heading = "### First SSH contact for a newly provisioned OVH instance"
	start := strings.Index(remoteSkill, heading)
	if start < 0 {
		t.Fatalf("OVH remote skill is missing %q", heading)
	}
	sectionStart := start + len(heading)
	sectionEnd := strings.Index(remoteSkill[sectionStart:], "\nIf setup needs environment variables,")
	if sectionEnd < 0 {
		t.Fatal("OVH remote skill's first-contact section has no clear end")
	}
	got := strings.Join(strings.Fields(remoteSkill[start:sectionStart+sectionEnd]), " ")
	const wantTemplate = `### First SSH contact for a newly provisioned OVH instance

Use this one-time trust-on-first-use path only when the user requested a new
OVH host and the provisioning skill has just successfully created and verified
it in this same provisioning flow. The handoff carries the selected project,
exact instance ID returned by the successful create operation, current public
IP returned and verified during provisioning, and approved local SSH identity.
Do not substitute a saved or unrelated target. Before any SSH connection,
query the selected OVH project read-only by the exact instance ID returned by
the create operation.
Verify that the returned instance ID matches exactly, the instance is ready,
and its current public IP matches the expected IP verified and reported by the
provisioning workflow. Use that verified IP as the SSH destination. Use only
the approved local private key whose matching public key was registered for
this instance in the approved provisioning configuration; do not ask for or
substitute another key. Automatically establish host trust as part of this
flow; never ask the user to retrieve, compare, approve, or paste an SSH
host-key fingerprint for this newly created server.

If the API lookup fails, the instance ID or IP differs, the instance is not
ready, the approved key cannot be matched, or any value is ambiguous, do not
connect or accept a host key. Follow the provisioning mismatch/error fallback:
stop, report the discrepancy or uncertainty, and ask what the user wants to do
next. A display name, region, or saved connection is not an instance-ID/IP
match.

For an exact match, use a unique target-specific file under the local
§~/.ssh§ directory, with a safe filename derived from the instance ID (for
example, §goaster-ovh-<instance-id>.known_hosts§). Keep §~/.ssh§ owner-only,
and make sure this file does not already exist or resolve through a symlink.
If it exists, stop and inspect it; do not clear or replace it. For the first
read-only SSH preflight only, connect to the verified IP with the approved key
and OpenSSH options equivalent to:

§§§text
-o BatchMode=yes
-o IdentitiesOnly=yes
-o PreferredAuthentications=publickey
-o PasswordAuthentication=no
-o KbdInteractiveAuthentication=no
-o GlobalKnownHostsFile=none
-o UserKnownHostsFile=<target-specific-known_hosts-file>
-o StrictHostKeyChecking=accept-new
-o UpdateHostKeys=no
§§§

Run the client with a restrictive umask so OpenSSH creates the file with
owner-only permissions. §accept-new§ stores the first key presented by the
verified IP in this instance's file and rejects a changed key. This is
trust-on-first-use: the OVH lookup confirms the target, not the SSH host key
itself. This exception applies only to that first preflight for this exact new
instance. Do not use §ssh-keyscan§ or another separately collected fingerprint
as an independent trust source, and do not request a fingerprint from the user
or any manual fingerprint approval for this new-server flow.

After that first SSH attempt, whether it succeeds or fails, use the same
target-specific file and §StrictHostKeyChecking=yes§ with §UpdateHostKeys=no§
for every SSH command, including bootstrap stages and the §process_handoff§
handoff. Set
§GlobalKnownHostsFile=none§ with this explicit file so no other known-hosts
source can authorize a changed key. Pass the file in §process_handoff§'s optional
§known_hosts§ field so the protected login uses the same entry and the saved
connection record retains its path. Never use §accept-new§ again for this
target. If the first attempt fails, inspect the OVH instance, exact IP, and
known-hosts file before any retry; do not delete the entry or repeat first-use
acceptance. If no key was saved, stop; do not ask the user to supply or approve
a fingerprint for this newly created server. Stop on a changed-key warning or
any unresolved mismatch.`
	want := strings.Join(strings.Fields(strings.ReplaceAll(wantTemplate, "§", "`")), " ")
	if got != want {
		t.Fatal("OVH remote skill's new-instance trust instructions drifted from the canonical contract")
	}
}

func TestInternetFetchNetworkPolicyContract(t *testing.T) {
	internetFetch := strings.Join(strings.Fields(readRepositorySkill(t, "internet-fetch")), " ")
	remoteSkillDocument := readRepositorySkill(t, "ovh-remote")
	for _, requirement := range []string{
		"### Apply an explicit address-family requirement first",
		"from the first request and on every request in its approved redirect chain; this takes precedence over normal-first retrieval and diagnostic fallback.",
		"For the official pinned Go archive bootstrap in the OVH remote skill, use per-request IPv4 from the initial archive request through each validated HTTPS redirect and any separately retrieved official checksum.",
		"Never make a dual-stack first request or switch a redirected request to a default client for this workaround.",
		"Keep the existing timeout, retry, redirect, destination, credential, and integrity safeguards.",
		"### Make the normal request first when no address-family requirement applies",
		"Do not force an address family for this generic first attempt.",
		"### Use targeted IPv4 fallback only for a suspicious endpoint when no requirement applies",
		"at most twice after the initial attempt",
		"After the normal request and any allowed transient retries, make at most one IPv4-only probe for the same exact hostname and URL.",
		"Use a per-request option such as `curl -4` when an authorized, suitable client is available.",
		"IPv4 selection must apply only to this one request.",
		"Never change system or application network settings, resolver configuration, environment defaults, or client-wide defaults to force IPv4.",
		"Limit redirects to five and allow only HTTP or HTTPS.",
		"validate a new host as a public endpoint before requesting it",
		"never forward credentials, cookies, or other authorization across hosts",
	} {
		if !strings.Contains(internetFetch, requirement) {
			t.Errorf("internet-fetch skill missing network policy requirement %q", requirement)
		}
	}

	const downloadStart = "Inside the reviewed host script, map `uname -m`"
	downloadStartIndex := strings.Index(remoteSkillDocument, downloadStart)
	if downloadStartIndex < 0 {
		t.Fatalf("OVH remote skill is missing %q", downloadStart)
	}
	downloadEndIndex := strings.Index(remoteSkillDocument[downloadStartIndex:], "\n\nWhen installing Go,")
	if downloadEndIndex < 0 {
		t.Fatal("OVH remote skill's Go download instruction has no clear end")
	}
	download := strings.Join(strings.Fields(remoteSkillDocument[downloadStartIndex:downloadStartIndex+downloadEndIndex]), " ")
	for _, requirement := range []string{
		"GoAster must use the explicit per-request IPv4 option (`curl --ipv4`) from the first archive request through every request in its approved redirect chain.",
		"Use the same option for any separately retrieved official checksum and each request in its redirect chain.",
		"Never make a dual-stack first request or switch a redirected archive or checksum request to a default client or normal-first probe.",
		"Keep the `go.dev` hostname rather than replacing it with an IP so TLS SNI, certificate validation, and virtual hosting remain intact.",
		"Validate each redirect destination as public before requesting it; allow only HTTPS, never downgrade, and limit each chain to five redirects.",
		"Never forward credentials to a different host.",
		"sha256sum --check --status",
		"before extraction",
		"stop without extracting or building",
		"get approval for any retry",
		"Keep the internet-fetch skill's finite timeout and retry limits; each approved retry must again use per-request IPv4 from its first request through each validated redirect.",
		"do not change system or application network settings, resolver configuration, or client-wide defaults.",
	} {
		if !strings.Contains(download, requirement) {
			t.Errorf("OVH remote Go download instruction missing requirement %q", requirement)
		}
	}
}

func TestOVHPriceGuidanceDocsMatchProvisioningPolicy(t *testing.T) {
	for _, path := range []string{"README.md", "docs/USER-JOURNEY.md"} {
		document := strings.Join(strings.Fields(readFixture(t, filepath.Join("..", "..", filepath.FromSlash(path)))), " ")
		for _, want := range []string{
			"US$0.02/hour",
			"US$15/month",
			"2026-11-05",
			"No web search is mandatory.",
			"explicit budget override",
			"exact billable configuration",
			"other configurations",
		} {
			if !strings.Contains(document, want) {
				t.Errorf("%s is missing OVH price guidance %q", path, want)
			}
		}
		for _, outdated := range []string{
			"must use `web_search` and `web_fetch`",
			"checks current official OVH prices with `web_search` and `web_fetch`",
		} {
			if strings.Contains(document, outdated) {
				t.Errorf("%s still mandates a web search for OVH pricing: %q", path, outdated)
			}
		}
	}
}

func TestOVHRemoteUserFacingOnboardingWording(t *testing.T) {
	remoteSkill := readRepositorySkill(t, "ovh-remote")
	const heading = "#### User-facing status and next action"
	const endHeading = "#### Operator readiness and completion gates"
	start := strings.Index(remoteSkill, heading)
	if start < 0 {
		t.Fatalf("OVH remote skill is missing %q", heading)
	}
	content := remoteSkill[start+len(heading):]
	end := strings.Index(content, endHeading)
	if end < 0 {
		t.Fatalf("OVH remote skill is missing %q", endHeading)
	}
	wording := strings.TrimSpace(content[:end])
	plain := strings.Join(strings.Fields(wording), " ")
	for _, want := range []string{
		"Telegram setup is still in progress.",
		"I found your server.",
		"Telegram is ready for pairing.",
		"Telegram is ready to test.",
		"Setup is complete. Telegram replied successfully.",
		"Open a private chat with the Telegram bot you created or selected using @BotFather, using the same bot whose token you entered through GoAster's local /env flow.",
		"Send /start, then send /start followed by the exact pairing string GoAster showed you, unchanged.",
		"Example only: /start EXAMPLE_CODE_ONLY.",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("user-facing OVH onboarding wording missing %q", want)
		}
	}

	paragraphs := strings.Split(wording, "\n\n")
	for _, action := range []string{
		"Next: Wait while I finish setup. Do not test Telegram yet.",
		"Next: Approve the sign-in in the terminal.",
		"Open a private chat with the Telegram bot you created or selected using @BotFather, using the same bot whose token you entered through GoAster's local /env flow. Send /start, then send /start followed by the exact pairing string GoAster showed you, unchanged. Example only: /start EXAMPLE_CODE_ONLY.",
		"Next: Send one normal message to the bot and check for a reply.",
	} {
		paragraphIndex := -1
		for i, paragraph := range paragraphs {
			if strings.Join(strings.Fields(paragraph), " ") == action {
				paragraphIndex = i
				break
			}
		}
		if paragraphIndex <= 0 || paragraphIndex == len(paragraphs)-1 {
			t.Errorf("user action must be separated by blank lines before and after it: %q", action)
		}
	}

	technicalDetails := regexp.MustCompile(`(?i)\b(?:ssh|port|systemd|root|healthz|adapter|shell_execute|process_handoff|systemctl)\b|key path|known_hosts(?: path)?|host-key verification|api endpoint(?:s)?|raw tool command`)
	if detail := technicalDetails.FindString(wording); detail != "" {
		t.Errorf("user-facing OVH status wording exposes internal detail %q", detail)
	}
}

func TestEnvUsesPickerAndCancellationPreservesSavedValues(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "env")
	store := localenv.NewStoreAt(storePath)
	if err := store.Save(map[string]string{"EXISTING": "before"}); err != nil {
		t.Fatal(err)
	}
	originalFile, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return "", ErrEnvironmentFilePickerCanceled
	})
	input := strings.NewReader("/env\nhello\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 1 {
		t.Fatalf("picker calls = %d, want 1", pickerCalls)
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want only the ordinary chat message", messages)
	}
	if !reflect.DeepEqual(store.Values(), map[string]string{"EXISTING": "before"}) {
		t.Fatalf("bare /env changed saved variable names: %v", localenvNames(store.Values()))
	}
	afterFile, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterFile, originalFile) {
		t.Errorf("bare /env changed saved env file: before %q, after %q", originalFile, afterFile)
	}
	if !strings.Contains(output.String(), "Environment setup canceled.") {
		t.Errorf("output did not report cancellation: %s", &output)
	}
	for _, unwanted := range []string{"Usage: /env <local-file-path>", "Review environment values", "Save these values?"} {
		if strings.Contains(output.String(), unwanted) {
			t.Errorf("canceled /env entered an unexpected flow: %s", &output)
		}
	}
}

func TestGetEnvSavesExistingEnvironmentWithoutPrintingValues(t *testing.T) {
	secretValues := map[string]string{
		"OVH_APPLICATION_KEY":    "private-ovh-application-key",
		"OVH_APPLICATION_SECRET": "private-ovh-application-secret",
		"OVH_CONSUMER_KEY":       "private-ovh-consumer-key",
		"TELEGRAM_BOT_TOKEN":     "private-telegram-token",
	}
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Save(secretValues); err != nil {
		t.Fatal(err)
	}
	downloadPath := filepath.Join(t.TempDir(), "goaster.env")
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return downloadPath, nil
	})
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	var output bytes.Buffer
	input := strings.NewReader("/get-env\ncontinue chatting\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 1 {
		t.Fatalf("save picker calls = %d, want 1", pickerCalls)
	}
	if !reflect.DeepEqual(messages, []string{"continue chatting"}) {
		t.Fatalf("provider messages = %q, want only ordinary chat", messages)
	}
	data, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal("environment file was not saved")
	}
	wantFile := `OVH_APPLICATION_KEY=private-ovh-application-key
OVH_APPLICATION_SECRET=private-ovh-application-secret
OVH_CONSUMER_KEY=private-ovh-consumer-key
TELEGRAM_BOT_TOKEN=private-telegram-token
`
	if !bytes.Equal(data, []byte(wantFile)) {
		t.Fatal("saved environment file did not match the stored settings")
	}
	info, err := os.Stat(downloadPath)
	if err != nil {
		t.Fatal("could not inspect saved environment file")
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("saved environment permissions = %04o, want 0600", info.Mode().Perm())
	}
	for _, secret := range []string{
		"private-ovh-application-key",
		"private-ovh-application-secret",
		"private-ovh-consumer-key",
		"private-telegram-token",
	} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("terminal output exposed a saved environment value")
		}
	}
	if !strings.Contains(output.String(), "Environment file saved to: "+downloadPath) {
		t.Errorf("terminal output did not report the download path: %s", &output)
	}
}

func TestGetEnvCreatesExactBlankTemplateAndGuidance(t *testing.T) {
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	downloadPath := filepath.Join(t.TempDir(), "goaster.env")
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return downloadPath, nil
	})
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	var output bytes.Buffer
	input := strings.NewReader("/get-env\ncontinue chatting\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 1 {
		t.Fatalf("save picker calls = %d, want 1", pickerCalls)
	}
	if !reflect.DeepEqual(messages, []string{"continue chatting"}) {
		t.Fatalf("provider messages = %q, want only ordinary chat", messages)
	}
	data, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal("blank environment template was not saved")
	}
	wantTemplate := `OVH_APPLICATION_KEY=
OVH_APPLICATION_SECRET=
OVH_CONSUMER_KEY=
TELEGRAM_BOT_TOKEN=
`
	if !bytes.Equal(data, []byte(wantTemplate)) {
		t.Fatal("template variable names or KEY= format did not match the required settings")
	}
	info, err := os.Stat(downloadPath)
	if err != nil {
		t.Fatal("could not inspect saved environment template")
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("template permissions = %04o, want 0600", info.Mode().Perm())
	}
	for _, want := range []string{
		"Blank environment template saved to: " + downloadPath,
		"Fill in the values, then re-upload the completed file with /env.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("terminal output missing %q: %s", want, &output)
		}
	}
}

func TestEnvPickerUnavailableShowsInstallInstructionsAndContinues(t *testing.T) {
	missingPicker := &exec.Error{Name: "zenity", Err: exec.ErrNotFound}
	err := wrapNativeFilePickerError(missingPicker)
	if runtime.GOOS != "linux" {
		if errors.Is(err, errEnvironmentFilePickerUnavailable) {
			t.Fatalf("non-Linux picker error was classified as unavailable: %v", err)
		}
		if !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("wrapped picker error = %v, want exec.ErrNotFound", err)
		}
		return
	}
	if !errors.Is(err, errEnvironmentFilePickerUnavailable) {
		t.Fatalf("Linux missing-picker error = %v, want unavailable sentinel", err)
	}

	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	picker := EnvironmentFilePickerFunc(func() (string, error) { return "", err })
	input := strings.NewReader("/env\nhello\n")
	if runErr := runWithTestEnvStoreAndPicker(t, context.Background(), input, &output, NewSession(defaultConfig(t), provider), picker); runErr != nil {
		t.Fatal(runErr)
	}
	for _, want := range []string{"File picker unavailable", "Install zenity", "qarma", "matedialog", "sudo apt install zenity"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want chat to continue after /env", messages)
	}
}

func TestFileCommandForwardsSelectedImageAndCleansTemporaryCopy(t *testing.T) {
	imageData := bytes.NewBuffer(nil)
	if err := png.Encode(imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	screenPath := filepath.Join(t.TempDir(), "dashboard.png")
	if err := os.WriteFile(screenPath, imageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	pickerCalls := 0
	var temporaryPath string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		if !strings.Contains(message, fileInspectionMessage) {
			t.Errorf("generic file inspection prompt missing from model request: %q", message)
		}
		for _, want := range []string{"type: image; filename: dashboard.png; mime_type: image/png", "local_path:"} {
			if !strings.Contains(message, want) {
				t.Errorf("attachment reference missing %q: %q", want, message)
			}
		}
		_, temporaryPath, _ = strings.Cut(message, "local_path: ")
		temporaryPath = strings.TrimSpace(temporaryPath)
		metadata := attachment.Metadata{
			Type:     "image",
			Filename: "dashboard.png",
			Path:     temporaryPath,
			MIMEType: "image/png",
			Size:     int64(imageData.Len()),
		}
		dataURL, err := attachment.ReadImageDataURL(metadata)
		if err != nil || !strings.HasPrefix(dataURL, "data:image/png;base64,") {
			t.Errorf("selected image was not available to the attachment flow: %v", err)
		}
		return "File inspected.", nil
	})
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return screenPath, nil
	})
	output := bytes.NewBuffer(nil)
	if err := runWithTestEnvStoreAndPicker(t, context.Background(), strings.NewReader("/file\n"), output, NewSession(defaultConfig(t), provider), picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 1 {
		t.Fatalf("image picker calls = %d, want 1", pickerCalls)
	}
	if temporaryPath == "" {
		t.Fatal("provider did not receive an attachment path")
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary attachment still exists after the turn: %v", err)
	}
	for _, want := range []string{"Choose a local file to inspect.", "assistant [gpt-6.1-sol]> File inspected."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, output)
		}
	}
}

func TestFileCommandCancellationAndPickerUnavailableContinue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pickerErr  error
		wantOutput string
	}{
		{name: "canceled", pickerErr: ErrEnvironmentFilePickerCanceled, wantOutput: "File inspection canceled."},
		{name: "unavailable", pickerErr: errEnvironmentFilePickerUnavailable, wantOutput: "File picker unavailable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messages []string
			provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
				messages = append(messages, message)
				return "received", nil
			})
			picker := EnvironmentFilePickerFunc(func() (string, error) { return "", tc.pickerErr })
			output := bytes.NewBuffer(nil)
			input := strings.NewReader("/file\ncontinue chatting\n")
			if err := runWithTestEnvStoreAndPicker(t, context.Background(), input, output, NewSession(defaultConfig(t), provider), picker); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(messages, []string{"continue chatting"}) {
				t.Fatalf("provider messages = %q, want ordinary chat to continue", messages)
			}
			if !strings.Contains(output.String(), tc.wantOutput) {
				t.Errorf("output missing %q: %s", tc.wantOutput, output)
			}
		})
	}
}

func TestFileCommandForwardsDocumentAndContinues(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "notes.txt")
	documentData := []byte("plain text to inspect")
	if err := os.WriteFile(filePath, documentData, 0600); err != nil {
		t.Fatal(err)
	}
	malformedImagePath := filepath.Join(t.TempDir(), "broken.png")
	if err := os.WriteFile(malformedImagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0600); err != nil {
		t.Fatal(err)
	}
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		if strings.Contains(message, "type: document; filename: notes.txt") {
			_, pathName, ok := strings.Cut(message, "local_path: ")
			if !ok {
				t.Errorf("document attachment path is missing: %q", message)
			} else {
				pathName = strings.TrimSpace(pathName)
				data, err := os.ReadFile(pathName)
				if err != nil || !bytes.Equal(data, documentData) {
					t.Errorf("document was unavailable for local inspection: %v", err)
				}
			}
		}
		return "received", nil
	})
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		selectedPaths := []string{filePath, malformedImagePath}
		selectedPath := selectedPaths[pickerCalls]
		pickerCalls++
		return selectedPath, nil
	})
	output := bytes.NewBuffer(nil)
	input := strings.NewReader("/upload\n/file\nordinary message\n")
	if err := runWithTestEnvStoreAndPicker(t, context.Background(), input, output, NewSession(defaultConfig(t), provider), picker); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[0], fileInspectionMessage) || !strings.Contains(messages[0], "type: document; filename: notes.txt") || messages[1] != "ordinary message" {
		t.Fatalf("provider messages = %q, want document inspection and ordinary message", messages)
	}
	if !strings.Contains(output.String(), "File rejected: choose a regular file") {
		t.Errorf("malformed image was not rejected: %s", output)
	}
	_, temporaryPath, ok := strings.Cut(messages[0], "local_path: ")
	if !ok {
		t.Fatal("provider did not receive a document attachment path")
	}
	temporaryPath = strings.TrimSpace(temporaryPath)
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary document still exists after the turn: %v", err)
	}

	directoryPath := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(directoryPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareSelectedFile(directoryPath); err == nil {
		t.Fatal("accepted a directory as an attachment")
	}

	oversizedPath := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(oversizedPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(oversizedPath, attachment.MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareSelectedFile(oversizedPath); err == nil {
		t.Fatal("accepted an attachment above the size limit")
	}
}

func TestEnvPathSyntaxIsNotACommand(t *testing.T) {
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Save(map[string]string{"EXISTING": "before"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return "", errors.New("the picker must not be called for path syntax")
	})
	message := "/env /path/to/settings.txt"
	if err := RunWithEnvStoreAndPicker(context.Background(), strings.NewReader(message+"\n"), &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 0 {
		t.Fatalf("picker calls = %d, want 0", pickerCalls)
	}
	if !reflect.DeepEqual(messages, []string{message}) {
		t.Fatalf("provider messages = %q, want %q", messages, []string{message})
	}
	if !reflect.DeepEqual(store.Values(), map[string]string{"EXISTING": "before"}) {
		t.Fatalf("path syntax changed saved values: %v", localenvNames(store.Values()))
	}
	for _, unwanted := range []string{"Environment file rejected:", "Review environment values", "Environment setup canceled."} {
		if strings.Contains(output.String(), unwanted) {
			t.Errorf("path syntax entered environment setup: %s", &output)
		}
	}
}

func TestEnvFileCommandLoadsReviewsAndSavesWithoutPicker(t *testing.T) {
	directory := t.TempDir()
	filePath := filepath.Join(directory, "settings to import.txt")
	secret := "test-api-token"
	if err := os.WriteFile(filePath, []byte("API_TOKEN="+secret+"\nREMOTE_HOST=example.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(directory, "saved", "env")
	store := localenv.NewStoreAt(storePath)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	pickerCalls := 0
	picker := EnvironmentFilePickerFunc(func() (string, error) {
		pickerCalls++
		return "", errors.New("the picker must not be called for an explicit path")
	})
	input := strings.NewReader("/env-file '" + filePath + "'\ny\nhello\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 0 {
		t.Fatalf("picker calls = %d, want 0", pickerCalls)
	}
	wantValues := map[string]string{"API_TOKEN": secret, "REMOTE_HOST": "example.invalid"}
	if !reflect.DeepEqual(store.Values(), wantValues) {
		t.Fatalf("saved variable names = %v, want %v", localenvNames(store.Values()), localenvNames(wantValues))
	}
	loaded := localenv.NewStoreAt(storePath)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Values(), wantValues) {
		t.Fatalf("persisted variable names = %v, want %v", localenvNames(loaded.Values()), localenvNames(wantValues))
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want only the ordinary chat message", messages)
	}
	for _, want := range []string{"API_TOKEN=********", "REMOTE_HOST=********", "Save these values? [y/N]", "Environment values saved locally."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	for _, value := range []string{secret, "example.invalid"} {
		if strings.Contains(output.String(), value) {
			t.Errorf("output exposed a file value %q", value)
		}
	}
}

func TestEnvFileCommandMalformedUsageReturnsErrorWithoutPicker(t *testing.T) {
	for _, command := range []string{"/env-file", `/env-file ""`, "/env-file one two", "/env-file 'two words' extra"} {
		t.Run(command, func(t *testing.T) {
			store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
			var output bytes.Buffer
			providerCalls := 0
			provider := providerFunc(func(context.Context, string, string) (string, error) {
				providerCalls++
				return "received", nil
			})
			pickerCalls := 0
			picker := EnvironmentFilePickerFunc(func() (string, error) {
				pickerCalls++
				return "", errors.New("the picker must not be called for malformed usage")
			})
			err := RunWithEnvStoreAndPicker(context.Background(), strings.NewReader(command+"\n"), &output, NewSession(defaultConfig(t), provider), store, picker)
			if err == nil || err.Error() != "expected exactly one non-empty file path" {
				t.Fatalf("error = %v, want concise path-usage error", err)
			}
			if strings.Contains(err.Error(), "/env-file") {
				t.Errorf("error advertises the command: %v", err)
			}
			if pickerCalls != 0 {
				t.Errorf("picker calls = %d, want 0", pickerCalls)
			}
			if providerCalls != 0 {
				t.Errorf("provider calls = %d, want 0", providerCalls)
			}
		})
	}
}

func TestEnvFileCancellationPreservesSavedValues(t *testing.T) {
	directory := t.TempDir()
	inputFile := filepath.Join(directory, "settings.txt")
	if err := os.WriteFile(inputFile, []byte("NEW_VALUE=after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "env")
	store := localenv.NewStoreAt(path)
	if err := store.Save(map[string]string{"EXISTING": "before"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	picker := EnvironmentFilePickerFunc(func() (string, error) { return inputFile, nil })
	input := strings.NewReader("/env\nn\nhello\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.Values(), map[string]string{"EXISTING": "before"}) {
		t.Fatalf("canceled setup changed saved variable names: %v", localenvNames(store.Values()))
	}
	loaded := localenv.NewStoreAt(path)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Values(), map[string]string{"EXISTING": "before"}) {
		t.Fatalf("canceled setup changed persisted variable names: %v", localenvNames(loaded.Values()))
	}
	if !strings.Contains(output.String(), "Environment setup canceled.") {
		t.Errorf("output did not report cancellation: %s", &output)
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want only the ordinary chat message", messages)
	}
}

func TestEnvFileCommandReviewsAndSavesLocally(t *testing.T) {
	directory := t.TempDir()
	filePath := filepath.Join(directory, "settings to import.txt")
	if err := os.WriteFile(filePath, []byte("GOASTER_MODE=remote\nAPI_TOKEN=test-api-token\nREMOTE_HOST=example.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := localenv.NewStoreAt(filepath.Join(directory, "saved", "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		messages = append(messages, message)
		return "received", nil
	})
	picker := EnvironmentFilePickerFunc(func() (string, error) { return filePath, nil })
	input := strings.NewReader("/env\ny\nhello\n")
	if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
		t.Fatal(err)
	}

	wantValues := map[string]string{
		"GOASTER_MODE": "remote",
		"API_TOKEN":    "test-api-token",
		"REMOTE_HOST":  "example.invalid",
	}
	if !reflect.DeepEqual(store.Values(), wantValues) {
		t.Fatalf("saved variable names = %v, want %v", localenvNames(store.Values()), localenvNames(wantValues))
	}
	if !reflect.DeepEqual(messages, []string{"hello"}) {
		t.Fatalf("provider messages = %q, want only the ordinary chat message", messages)
	}
	for _, want := range []string{
		"GOASTER_MODE=********",
		"API_TOKEN=********",
		"REMOTE_HOST=********",
		"Save these values? [y/N]",
		"Environment values saved locally.",
		"Next: choose your local KEY=VALUE file in the file picker.",
		"where to find its value; ask me to explain any variable",
		"Enter secret values only through this local environment-file flow; never paste them into chat.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	for _, secret := range []string{"test-api-token", "example.invalid", "remote"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("output exposed a file value %q", secret)
		}
	}
}

func TestEnvFileCommandErrorsPreserveSavedValuesAndContinue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		prepare func(t *testing.T, path string)
		wantErr string
	}{
		{name: "missing", wantErr: "inspect environment file"},
		{name: "malformed", content: []byte("VALID=secret-value\nbad line\n"), wantErr: "line 2 must be KEY=VALUE"},
		{name: "duplicate", content: []byte("DUPLICATE=first\nDUPLICATE=second\n"), wantErr: "duplicates key DUPLICATE"},
		{name: "oversized", content: []byte(strings.Repeat("A", 1024*1024+1)), wantErr: "exceeds the 1 MiB limit"},
		{name: "unreadable", prepare: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("KEY=value\n"), 0000); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "not readable"},
		{name: "symbolic link", prepare: func(t *testing.T, path string) {
			t.Helper()
			target := path + ".target"
			if err := os.WriteFile(target, []byte("KEY=value\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("cannot create symbolic link: %v", err)
			}
		}, wantErr: "must not be a symbolic link"},
		{name: "non-regular", prepare: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "must be a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			filePath := filepath.Join(directory, "settings.txt")
			if tc.prepare != nil {
				tc.prepare(t, filePath)
			} else if tc.name != "missing" {
				if err := os.WriteFile(filePath, tc.content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			storePath := filepath.Join(directory, "saved", "env")
			store := localenv.NewStoreAt(storePath)
			if err := store.Save(map[string]string{"EXISTING": "before"}); err != nil {
				t.Fatal(err)
			}
			originalFile, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}

			var output bytes.Buffer
			var messages []string
			provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
				messages = append(messages, message)
				return "received", nil
			})
			picker := EnvironmentFilePickerFunc(func() (string, error) { return filePath, nil })
			input := strings.NewReader("/env\nhello\n")
			if err := RunWithEnvStoreAndPicker(context.Background(), input, &output, NewSession(defaultConfig(t), provider), store, picker); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(output.String(), "Environment file rejected:") || !strings.Contains(output.String(), tc.wantErr) {
				t.Errorf("output did not explain the rejected file: %s", &output)
			}
			if strings.Contains(output.String(), "Review environment values") {
				t.Errorf("rejected file was sent to review: %s", &output)
			}
			if !reflect.DeepEqual(messages, []string{"hello"}) {
				t.Errorf("provider messages = %q, want only the ordinary chat message", messages)
			}
			if !reflect.DeepEqual(store.Values(), map[string]string{"EXISTING": "before"}) {
				t.Errorf("rejected file changed saved values: %v", localenvNames(store.Values()))
			}
			afterFile, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(afterFile, originalFile) {
				t.Errorf("rejected file changed saved env file: before %q, after %q", originalFile, afterFile)
			}
		})
	}
}

func localenvNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	return names
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLoopErrors(t *testing.T) {
	failure := errors.New("broken stream or provider")
	provider := providerFunc(func(context.Context, string, string) (string, error) { return "response", nil })
	for _, tc := range []struct {
		name     string
		input    io.Reader
		output   io.Writer
		provider Provider
	}{
		{"input", failingReader{failure}, io.Discard, provider},
		{"output", strings.NewReader("hello\n"), failingWriter{failure}, provider},
		{"provider", strings.NewReader("hello\n"), io.Discard, providerFunc(func(context.Context, string, string) (string, error) { return "", failure })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runWithTestEnvStore(t, context.Background(), tc.input, tc.output, NewSession(defaultConfig(t), tc.provider)); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func runWithTestEnvStore(t *testing.T, ctx context.Context, input io.Reader, output io.Writer, session *Session) error {
	t.Helper()
	return runWithTestEnvStoreAndPicker(t, ctx, input, output, session, EnvironmentFilePickerFunc(func() (string, error) {
		return "", ErrEnvironmentFilePickerCanceled
	}))
}

func runWithTestEnvStoreAndPicker(t *testing.T, ctx context.Context, input io.Reader, output io.Writer, session *Session, picker EnvironmentFilePicker) error {
	t.Helper()
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		return err
	}
	return RunWithEnvStoreAndPicker(ctx, input, output, session, store, picker)
}

func readRepositorySkill(t testing.TB, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", name, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
