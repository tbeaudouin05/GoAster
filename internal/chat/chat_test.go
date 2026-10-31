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
	"goaster/skills"
)

func TestConfig(t *testing.T) {
	config, err := ParseConfig(nil, io.Discard)
	if err != nil || config.PrimaryModel != "gpt-6.1-sol" || config.FallbackModel != "gpt-5.6-luna" {
		t.Fatalf("defaults: %+v, %v", config, err)
	}
	config, err = ParseConfig([]string{"-primary-model", " custom-primary ", "-fallback-model", "custom-fallback"}, io.Discard)
	if err != nil || config != (Config{"custom-primary", "custom-fallback"}) {
		t.Fatalf("overrides: %+v, %v", config, err)
	}
	for _, args := range [][]string{{"-primary-model", " "}, {"-fallback-model", ""}, {"unexpected"}, {"-unknown"}, {"-primary-model"}, {"-offline"}, {"-echo-unavailable-model", "gpt-6.1-sol"}} {
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

func TestSessionSelection(t *testing.T) {
	ordinaryError := errors.New("provider failed")
	unavailable := fmt.Errorf("temporarily unavailable: %w", ErrModelUnavailable)
	for _, tc := range []struct {
		name        string
		primaryErr  error
		fallbackErr error
		sameModel   bool
		wantModels  []string
		wantErr     error
	}{
		{"primary", nil, nil, false, []string{DefaultPrimaryModel, DefaultPrimaryModel}, nil},
		{"fallback stays active", unavailable, nil, false, []string{DefaultPrimaryModel, DefaultFallbackModel, DefaultFallbackModel}, nil},
		{"ordinary error does not switch", ordinaryError, nil, false, []string{DefaultPrimaryModel}, ordinaryError},
		{"failed fallback stops", unavailable, ordinaryError, false, []string{DefaultPrimaryModel, DefaultFallbackModel}, ordinaryError},
		{"same model is not retried", unavailable, nil, true, []string{DefaultPrimaryModel}, ErrModelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultConfig(t)
			if tc.sameModel {
				config.FallbackModel = config.PrimaryModel
			}
			var models []string
			provider := providerFunc(func(ctx context.Context, model, message string) (string, error) {
				models = append(models, model)
				if message != "hello" {
					t.Errorf("message = %q", message)
				}
				if model == config.PrimaryModel && tc.primaryErr != nil {
					return "", tc.primaryErr
				}
				if model == config.FallbackModel && tc.fallbackErr != nil {
					return "", tc.fallbackErr
				}
				return "response", nil
			})
			session := NewSession(config, provider)
			reply, err := session.Reply(context.Background(), "hello")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err == nil {
				if reply != "response" {
					t.Fatalf("reply = %q", reply)
				}
				if _, err := session.Reply(context.Background(), "hello"); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(models, tc.wantModels) {
				t.Errorf("models = %v, want %v", models, tc.wantModels)
			}
			if len(models) > 1 && models[1] == DefaultFallbackModel && !strings.Contains(session.reason, unavailable.Error()) {
				t.Errorf("missing failure reason: %q", session.reason)
			}
		})
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

func TestLoopShowsFallback(t *testing.T) {
	var output bytes.Buffer
	provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
		if model == DefaultPrimaryModel {
			return "", ErrModelUnavailable
		}
		return "reply to " + message, nil
	})
	err := runWithTestEnvStore(t, context.Background(), strings.NewReader("hello\nagain"), &output,
		NewSession(defaultConfig(t), provider))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Active model: gpt-5.6-luna", "Fallback reason: gpt-6.1-sol failed: model unavailable", "assistant [gpt-5.6-luna]> reply to hello", "assistant [gpt-5.6-luna]> reply to again"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
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
	if !strings.Contains(result, "Request failed. Please try again.") || !strings.Contains(result, "retried reply") {
		t.Fatalf("missing recoverable error or retry reply: %q", result)
	}
	if strings.Contains(result, "temporary provider failure") {
		t.Fatalf("provider error leaked into the interactive request notice: %q", result)
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

func TestInteractiveToolFailureShowsSafeRequestError(t *testing.T) {
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
		!strings.Contains(result, "Request failed. Please try again.") || !strings.Contains(result, "Retry complete.") {
		t.Fatalf("tool failure and retry lifecycle was not displayed: %q", result)
	}
	if strings.Contains(result, "PRIVATE_TOKEN") || strings.Contains(result, "private tool failure details") {
		t.Fatalf("tool arguments or private failure details leaked into terminal output: %q", result)
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
	if !strings.Contains(result, "Request failed. Please try again.") || !strings.Contains(result, "retry succeeded") {
		t.Fatalf("failed attachment did not return to a successful retry: %q", result)
	}
	if strings.Contains(result, temporaryPath) || strings.Contains(result, "inspection provider failed") {
		t.Fatalf("attachment failure leaked path or provider error: %q", result)
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

func TestLoopStartsOVHOnboardingWithReadOnlyNextStep(t *testing.T) {
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
		"check whether GoAster already has a server connection saved on this computer",
		"I will not create a second server.",
		"You do not need to understand SSH, API calls, or service names.",
		"The server is ready, but Telegram still needs setup. Do not test Telegram yet.",
		"Setup is complete. Please send one normal message to the Telegram bot and check that it replies.",
		"If no server is saved, I will guide you through creating one.",
		"I will ask before anything that could cost money or change your OVH account.",
		"Starting this screen does not create or change anything.",
		"assistant [gpt-6.1-sol]> OVH skill response",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
	}
	if strings.Contains(output.String(), "POST /cloud/project") || strings.Contains(output.String(), "automatically create") {
		t.Errorf("onboarding output contains account-level project creation guidance: %s", output.String())
	}
}

func TestOVHProjectDiscoveryAcceptsNameMismatchAndWaitsForConfirmation(t *testing.T) {
	policy := strings.Join(strings.Fields(skills.OVHProvisioning), " ")
	if !strings.Contains(policy, "an active project named `Production` remains usable even though its name differs from `GoAster`") {
		t.Fatal("OVH project discovery policy does not cover a non-GoAster project name")
	}
	for _, requirement := range []string{
		"the only environment values to ask the user to provide are `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, `OVH_CONSUMER_KEY`, and `TELEGRAM_BOT_TOKEN`",
		"The OVH API endpoint is derived agent-side from the Create API Keys page URL in the conversation",
		"Derive the OVH API endpoint from the URL's hostname and use it in OVH API requests.",
	} {
		if !strings.Contains(policy, requirement) {
			t.Errorf("OVH onboarding policy is missing %q", requirement)
		}
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
		"Before asking for approval to create a server or other OVH resource, complete the Public price estimate above.",
		"Present the exact proposed resource, assumptions, SSH rule, and brief estimate.",
		"If pricing is unavailable, say the current cost is unknown",
		"get the user's acknowledgment before requesting approval.",
		"Before creating any server or other OVH resource, changing any billable resource, deleting or replacing",
		"For resource creation, the confirmation must",
		"follow the read-only assumptions and completed price report (including a clear statement when price is unknown)",
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
		"perform a fresh public-web search with GoAster's built-in `web_search`",
		"Use `web_fetch` to read the matching official OVHcloud price page",
		"https://www.ovhcloud.com/en/public-cloud/prices/",
		"search snippets alone and third-party prices are not confirmation.",
		"not an OVH billing API or account-specific pricing endpoint.",
		"after read-only inspection identifies the deployment region, smallest suitable machine, and required options",
		"OVH’s public price for this setup is approximately [currency and amount]/month",
		"official OVH price list](URL) checked [YYYY-MM-DD]",
		"This is a broad estimate, not an exact quote.",
		"When an hourly estimate is available, also include this concise cost and cleanup information:",
		"Estimated cost: ~US$0.02 for 1 hour, or ~US$0.40 for 24 hours, before tax. Delete the server after testing to stop compute charges. Retained storage or snapshots may still incur fees.",
		"Keep the `~` qualifiers; these amounts are approximate, not an exact quote.",
		"Add a short note with region, currency, VAT treatment, or material uncovered charges only when needed",
		"Do not present a numeric price as applying to the selected region unless the official source establishes both the region and currency",
		"do not guess or use third-party prices.",
		"Say the current price is unknown",
		"get the user's acknowledgment before requesting explicit creation approval.",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH provisioning price precheck missing requirement %q", want)
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

func TestOVHRemoteNewInstanceUsesScopedTrustOnFirstUse(t *testing.T) {
	policy := strings.Join(strings.Fields(skills.OVHRemote), " ")
	for _, want := range []string{
		"Existing hosts and saved targets must have an already trusted `known_hosts` entry.",
		"Never use trust-on-first-use for an existing target, an unrelated target, or a target whose provenance is unclear.",
		"query the selected OVH project read-only by the exact instance ID returned by the create operation.",
		"returned instance ID matches exactly",
		"current public IP matches the expected IP verified and reported by the provisioning workflow.",
		"Use only the approved local private key whose matching public key was registered for this instance",
		"If the API lookup fails, the instance ID or IP differs",
		"do not connect or accept a host key.",
		"unique target-specific file under the local `~/.ssh` directory",
		"-o StrictHostKeyChecking=accept-new",
		"-o UserKnownHostsFile=<target-specific-known_hosts-file>",
		"-o GlobalKnownHostsFile=none",
		"`accept-new` stores the first key presented by the verified IP in this instance's file and rejects a changed key.",
		"Do not use `ssh-keyscan` or another separately collected fingerprint as an independent trust source",
		"do not ask the user to retrieve a host fingerprint from OVHcloud Manager for this new-server flow.",
		"`StrictHostKeyChecking=yes` with `UpdateHostKeys=no` for every SSH command",
		"Set `GlobalKnownHostsFile=none` with this explicit file so no other known-hosts source can authorize a changed key.",
		"Pass the file in `remote_login`'s optional `known_hosts` field",
		"Never use `accept-new` again for this target.",
		"If the first attempt fails, inspect the OVH instance, exact IP, and known-hosts file before any retry",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("OVH remote SSH policy missing first-contact trust safeguard %q", want)
		}
	}
	if strings.Contains(policy, "For a first connection, obtain and compare the host-key fingerprint through a trusted channel") {
		t.Fatal("OVH remote SSH policy still requires manual fingerprint verification for every first connection")
	}
	instanceCheck := strings.Index(policy, "Before any SSH connection, query the selected OVH project read-only")
	acceptNew := strings.Index(policy, "-o StrictHostKeyChecking=accept-new")
	if instanceCheck < 0 || acceptNew <= instanceCheck {
		t.Fatal("OVH instance and IP verification must precede first-use host-key acceptance")
	}
}

func TestOVHRemoteUserFacingOnboardingWording(t *testing.T) {
	const heading = "#### User-facing status and next action"
	const endHeading = "#### Operator readiness and completion gates"
	start := strings.Index(skills.OVHRemote, heading)
	if start < 0 {
		t.Fatalf("OVH remote skill is missing %q", heading)
	}
	content := skills.OVHRemote[start+len(heading):]
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
		"Next: Approve the OpenAI sign-in in the terminal.",
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

	technicalDetails := regexp.MustCompile(`(?i)\b(?:ssh|port|systemd|root|healthz|adapter|shell_execute|remote_login|systemctl)\b|key path|known_hosts(?: path)?|host-key verification|api endpoint(?:s)?|raw tool command`)
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
