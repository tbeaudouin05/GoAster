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
	"sync"
	"testing"
	"time"

	"goaster/internal/filesync"
	"goaster/internal/localenv"
)

func TestRequestFailureDiagnosticIncludesUnderlyingErrorAndContext(t *testing.T) {
	secret := "PRIVATE_TOKEN https://example.test/private rm -rf"
	wantWarning := requestUncertaintyWarning
	saveFailure := errors.New(secret)
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "authentication", err: fmt.Errorf("%s: %w", secret, ErrLoginRequired), want: requestFailureAuthentication + "\n" + wantWarning},
		{name: "missing credentials", err: fmt.Errorf("%s: %w", secret, errCredentialsMissing), want: requestFailureCredentialsMissing + "\n" + wantWarning},
		{name: "authentication failure", err: authenticationRequestError{cause: errors.New(secret)}, want: requestFailureAuthentication + "\n" + wantWarning},
		{name: "authentication timeout", err: authenticationRequestError{cause: fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)}, want: requestFailureTimeout + "\n" + wantWarning},
		{name: "post-tool authentication", err: postToolAuthenticationError{cause: errors.New(secret)}, want: postToolAuthenticationWarning + "\n" + wantWarning},
		{name: "timeout", err: fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), want: requestFailureTimeout + "\n" + wantWarning},
		{name: "model unavailable", err: fmt.Errorf("%s: %w", secret, ErrModelUnavailable), want: requestFailureModelUnavailable + "\n" + wantWarning},
		{name: "interrupted", err: fmt.Errorf("%s: %w", secret, context.Canceled), want: requestFailureInterrupted + "\n" + wantWarning},
		{name: "concurrent conversation", err: fmt.Errorf("%s: %w", secret, errConversationStateChanged), want: errConversationStateChanged.Error() + "\n" + wantWarning},
		{name: "oversized conversation", err: fmt.Errorf("%s: %w", secret, errConversationStateTooLarge), want: errConversationStateTooLarge.Error() + "\n" + wantWarning},
		{name: "conversation save", err: conversationSaveError{operation: "save conversation state", cause: saveFailure}, want: requestFailureStateSave + "\n" + wantWarning},
		{name: "conversation lock busy", err: conversationLockBusyError{cause: errors.New(secret)}, want: requestFailureLockBusy + "\n" + wantWarning},
		{name: "save with lock contention", err: conversationSaveError{operation: "save conversation state", cause: conversationLockBusyError{cause: errors.New(secret)}}, want: requestFailureStateSave + "\n" + requestFailureLockBusy + "\n" + wantWarning},
		{name: "credential permissions", err: fmt.Errorf("%s: %w", secret, errCredentialPermissions), want: requestFailureCredentialMode + "\n" + wantWarning},
		{name: "conversation permissions", err: fmt.Errorf("%s: %w", secret, errConversationFilePermissions), want: requestFailureConversationMode + "\n" + wantWarning},
		{name: "directory permissions", err: fmt.Errorf("%s: %w", secret, errConversationDirectoryPermissions), want: requestFailureDirectoryMode + "\n" + wantWarning},
		{name: "save with insecure permissions", err: conversationSaveError{operation: "save conversation state", cause: fmt.Errorf("%s: %w", secret, errConversationFilePermissions)}, want: requestFailureStateSave + "\n" + requestFailureConversationMode + "\n" + wantWarning},
		{name: "save with conflict", err: conversationSaveError{operation: "save conversation state", cause: fmt.Errorf("%s: %w", secret, errConversationStateChanged)}, want: requestFailureStateSave + "\n" + errConversationStateChanged.Error() + "\n" + wantWarning},
		{name: "untyped conflict text", err: errors.New(errConversationStateChanged.Error()), want: requestFailureUnknown + "\n" + wantWarning},
		{name: "untyped permission text", err: errors.New(errCredentialPermissions.Error()), want: requestFailureUnknown + "\n" + wantWarning},
		{name: "untyped lock text", err: errors.New("lock conversation state (another instance may be using it)"), want: requestFailureUnknown + "\n" + wantWarning},
		{name: "unknown", err: errors.New(secret), want: requestFailureUnknown + "\n" + wantWarning},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := requestFailureDiagnostic(test.err)
			contextMessage := strings.Split(test.want, "\n")[0]
			if !strings.Contains(got, contextMessage) || !strings.Contains(got, wantWarning) {
				t.Fatalf("diagnostic = %q, want context %q and warning %q", got, contextMessage, wantWarning)
			}
			if !strings.Contains(got, "Underlying error: ") || !strings.Contains(got, test.err.Error()) {
				t.Fatalf("diagnostic omitted underlying error details: %q", got)
			}
			switch test.name {
			case "conversation permissions":
				requirePrivateSessionPermissions(t)
				filePath := filepath.Join(t.TempDir(), "PRIVATE_PATH", "session.json")
				if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filePath, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filePath, 0644); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(filePath)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0644 {
					t.Skip("filesystem cannot represent broad file permissions")
				}
				_, err = sessionVersion(filePath)
				if !errors.Is(err, errConversationFilePermissions) || !strings.Contains(err.Error(), "conversation file permissions are too broad: 0644 (want 0600)") {
					t.Fatalf("insecure conversation file error = %v", err)
				}
				if diagnostic := requestFailureDiagnostic(err); !strings.Contains(diagnostic, "conversation file permissions are too broad") || !strings.Contains(diagnostic, "mode 0600") || strings.Contains(diagnostic, filePath) {
					t.Fatalf("insecure conversation diagnostic = %q", diagnostic)
				}
			case "directory permissions":
				requirePrivateSessionPermissions(t)
				directory := filepath.Join(t.TempDir(), ".goaster")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(directory)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0755 {
					t.Skip("filesystem cannot represent broad directory permissions")
				}
				err = validateSessionDirectory(directory, false)
				if !errors.Is(err, errConversationDirectoryPermissions) || !strings.Contains(err.Error(), "conversation directory permissions are too broad: 0755 (want 0700)") {
					t.Fatalf("insecure conversation directory error = %v", err)
				}
				if diagnostic := requestFailureDiagnostic(err); !strings.Contains(diagnostic, "conversation directory permissions are too broad") || !strings.Contains(diagnostic, "mode 0700") || strings.Contains(diagnostic, directory) {
					t.Fatalf("insecure conversation directory diagnostic = %q", diagnostic)
				}
			}
		})
	}
	if requestFailureAuthentication != "Authentication is required to continue. Check your OpenAI sign-in and account status." {
		t.Fatalf("authentication advice is not neutral: %q", requestFailureAuthentication)
	}
	if !strings.Contains(requestUncertaintyWarning, "Before continuing or retrying, check whether any actions completed and verify external state.") {
		t.Fatalf("warning does not cover continuing and retrying: %q", requestUncertaintyWarning)
	}
	wrapped := safeRequestDiagnosticError(conversationSaveError{operation: "save conversation state", cause: saveFailure})
	if !errors.Is(wrapped, errConversationSaveFailed) || !errors.Is(wrapped, saveFailure) {
		t.Fatalf("safe diagnostic lost a save failure cause: %v", wrapped)
	}
}

func TestRequestFailureDiagnosticDoesNotBoundUnderlyingError(t *testing.T) {
	detail := strings.Repeat("PRIVATE_PROVIDER_DETAIL ", 128)
	diagnostic := requestFailureDiagnostic(errors.New(detail))
	if !strings.Contains(diagnostic, detail) {
		t.Fatalf("diagnostic truncated the underlying error: got %d detail bytes, want %d", len(diagnostic), len(detail))
	}
	cyclicDiagnostic := requestFailureDiagnostic(cyclicRequestError{})
	if !strings.Contains(cyclicDiagnostic, "cyclic request error") || !strings.Contains(cyclicDiagnostic, requestUncertaintyWarning) {
		t.Fatalf("cyclic diagnostic = %q", cyclicDiagnostic)
	}
}

type cyclicRequestError struct{}

func (cyclicRequestError) Error() string { return "cyclic request error" }
func (cyclicRequestError) Unwrap() error { return cyclicRequestError{} }

func TestInteractiveRequestDiagnosticsExposeRecoverableAndFatalDetails(t *testing.T) {
	requirePrivateSessionPermissions(t)
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	const secret = "PRIVATE_PROVIDER_DETAIL"
	const warningFragment = "tool results may be unavailable"

	var recoverableOutput bytes.Buffer
	recoverableCalls := 0
	recoverableProvider := providerFunc(func(context.Context, string, string) (string, error) {
		recoverableCalls++
		if recoverableCalls == 1 {
			return "", errors.New(secret)
		}
		return "retry succeeded", nil
	})
	err := runChatLoopWithUI(
		context.Background(),
		TerminalInput{reader: strings.NewReader("first\nsecond\n"), interactive: true},
		&recoverableOutput,
		NewSession(defaultConfig(t), recoverableProvider),
		store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
		terminalUI{interactive: true, width: 80},
	)
	if err != nil || recoverableCalls != 2 {
		t.Fatalf("recoverable path = calls %d, error %v", recoverableCalls, err)
	}
	if !strings.Contains(recoverableOutput.String(), requestFailureUnknown) || !strings.Contains(recoverableOutput.String(), warningFragment) {
		t.Fatalf("recoverable diagnostic missing: %q", recoverableOutput.String())
	}
	if !strings.Contains(recoverableOutput.String(), secret) {
		t.Fatalf("recoverable diagnostic omitted provider detail: %q", recoverableOutput.String())
	}

	conversationCause := errors.New("PRIVATE_CONVERSATION_RECOVERY")
	conversationProvider := providerFunc(func(context.Context, string, string) (string, error) {
		return "", errors.Join(ErrConversationRecovery, conversationCause)
	})
	var conversationOutput bytes.Buffer
	err = runChatLoopWithUI(
		context.Background(), TerminalInput{reader: strings.NewReader("continue\n"), interactive: true}, &conversationOutput,
		NewSession(defaultConfig(t), conversationProvider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
		terminalUI{interactive: true, width: 100},
	)
	if err != nil || !strings.Contains(conversationOutput.String(), "saved conversation is missing or invalid") || !strings.Contains(conversationOutput.String(), conversationCause.Error()) {
		t.Fatalf("interactive recovery = %v, output %q", err, conversationOutput.String())
	}
	err = runChatLoopWithUI(
		context.Background(), TerminalInput{reader: strings.NewReader("continue\n")}, io.Discard,
		NewSession(defaultConfig(t), conversationProvider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
		terminalUI{},
	)
	if err == nil || !errors.Is(err, ErrConversationRecovery) || !errors.Is(err, conversationCause) || !strings.Contains(err.Error(), conversationCause.Error()) {
		t.Fatalf("line recovery = %v", err)
	}

	var fatalOutput bytes.Buffer
	fatalProvider := providerFunc(func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)
	})
	err = runChatLoopWithUI(
		context.Background(),
		TerminalInput{reader: strings.NewReader("fatal\n"), interactive: true},
		&fatalOutput,
		NewSession(defaultConfig(t), fatalProvider),
		store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
		terminalUI{interactive: true, width: 80},
	)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), requestFailureTimeout) || !strings.Contains(err.Error(), warningFragment) {
		t.Fatalf("fatal path = error %v, output %q", err, fatalOutput.String())
	}
	if strings.Contains(fatalOutput.String(), secret) || !strings.Contains(err.Error(), secret) {
		t.Fatalf("fatal diagnostic did not expose provider detail in the returned error: error=%v output=%q", err, fatalOutput.String())
	}
	// The CLI prints the returned error. A fatal diagnostic must appear only
	// there, not in both the loop output and the caller's error report.
	combined := fatalOutput.String() + fmt.Sprint("goaster: ", err)
	if strings.Count(combined, requestFailureTimeout) != 1 || strings.Count(combined, "If an earlier attempt may have failed or been interrupted") != 1 {
		t.Fatalf("fatal diagnostic displayed more than once: %q", combined)
	}

	lineError := errors.New(secret)
	err = runChatLoopWithUI(
		context.Background(), TerminalInput{reader: strings.NewReader("line\n")}, io.Discard,
		NewSession(defaultConfig(t), providerFunc(func(context.Context, string, string) (string, error) {
			return "", lineError
		})), store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{},
	)
	if !errors.Is(err, lineError) || !strings.Contains(err.Error(), secret) {
		t.Fatalf("line-mode error changed: %v", err)
	}
	for _, failurePoint := range []string{"progress", "diagnostic", "skill diagnostic"} {
		started := false
		writeFailure := errors.New("PRIVATE_OUTPUT_FAILURE")
		requestFailure := errors.New("PRIVATE_REQUEST_FAILURE")
		writer := diagnosticWriter(func(data []byte) (int, error) {
			if started {
				return 0, writeFailure
			}
			return len(data), nil
		})
		provider := providerFunc(func(ctx context.Context, _, _ string) (string, error) {
			started = true
			if failurePoint == "progress" {
				toolCallObserver(ctx)("test_tool", "{}")
			}
			if failurePoint == "skill diagnostic" {
				return "", skillRecovery(requestFailure, skillSaveMessage)
			}
			return "", requestFailure
		})
		err := runChatLoopWithUI(
			context.Background(), TerminalInput{reader: strings.NewReader("run\n"), interactive: true}, writer,
			NewSession(defaultConfig(t), provider), store,
			EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 80},
		)
		if !errors.Is(err, requestFailure) || !errors.Is(err, writeFailure) {
			t.Fatalf("%s write failure lost a cause: %v", failurePoint, err)
		}
		if failurePoint == "skill diagnostic" && !strings.Contains(err.Error(), "Restart to reload the conversation") {
			t.Fatalf("skill recovery guidance was lost: %v", err)
		}
		if !strings.Contains(err.Error(), "PRIVATE_REQUEST_FAILURE") {
			t.Fatalf("%s write failure omitted request details: %v", failurePoint, err)
		}
		if failurePoint != "skill diagnostic" {
			if !strings.Contains(err.Error(), requestUncertaintyWarning) {
				t.Fatalf("%s write failure diagnostic = %v", failurePoint, err)
			}
		}
		if !strings.Contains(err.Error(), "PRIVATE_OUTPUT_FAILURE") {
			t.Fatalf("%s write failure omitted output details: %v", failurePoint, err)
		}
	}
	{
		store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
		if err := store.Load(); err != nil {
			t.Fatal(err)
		}
		started := false
		writeFailure := errors.New("output write failure")
		requestFailure := errors.New("request failure")
		writer := diagnosticWriter(func(data []byte) (int, error) {
			if started {
				return 0, writeFailure
			}
			return len(data), nil
		})
		provider := providerFunc(func(context.Context, string, string) (string, error) {
			started = true
			return "", requestFailure
		})
		err := runChatLoopWithUI(
			context.Background(), TerminalInput{reader: strings.NewReader("run\n")}, writer,
			noticeLoopSession{NewSession(defaultConfig(t), provider)}, store,
			EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{},
		)
		if !errors.Is(err, requestFailure) || !errors.Is(err, writeFailure) || !strings.Contains(err.Error(), requestFailure.Error()) || !strings.Contains(err.Error(), writeFailure.Error()) {
			t.Fatalf("line-mode error = %v, want request and write failures", err)
		}
	}
	{
		store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
		if err := store.Load(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "session.json")
		providerCalls := 0
		provider := providerFunc(func(context.Context, string, string) (string, error) {
			providerCalls++
			return "ok", nil
		})
		first, err := NewPersistentSessionAt(defaultConfig(t), provider, path)
		if err != nil {
			t.Fatal(err)
		}
		stale, err := NewPersistentSessionAt(defaultConfig(t), provider, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.Reply(context.Background(), "update history"); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err = runChatLoopWithUI(
			context.Background(), TerminalInput{reader: strings.NewReader("stale request\n"), interactive: true},
			&output, stale, store,
			EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 80},
		)
		if !errors.Is(err, errConversationStateChanged) || !strings.Contains(err.Error(), "restart to load the latest history") || !strings.Contains(err.Error(), requestUncertaintyWarning) {
			t.Fatalf("stale session diagnostic = %v", err)
		}
		if providerCalls != 1 {
			t.Fatalf("stale request reached provider: %d calls", providerCalls)
		}
	}
	for _, command := range []string{"ordinary request", "/clear"} {
		path := filepath.Join(t.TempDir(), "PRIVATE_SESSION_PATH", "session.json")
		provider := providerFunc(func(context.Context, string, string) (string, error) { return "ok", nil })
		session, err := NewPersistentSessionAt(defaultConfig(t), provider, path)
		if err != nil {
			t.Fatal(err)
		}
		saveCause := errors.New("PRIVATE_SAVE_CAUSE")
		session.store.syncDirectory = func(string) error { return fmt.Errorf("%s: %w", path, saveCause) }
		output := ""
		writer := diagnosticWriter(func(data []byte) (int, error) {
			output += string(data)
			return len(data), nil
		})
		err = runChatLoopWithUI(
			context.Background(), TerminalInput{reader: strings.NewReader(command + "\n"), interactive: true}, writer,
			session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 100},
		)
		if command == "/clear" {
			if err != nil || !strings.Contains(output, "Conversation state could not be saved or confirmed.") {
				t.Fatalf("clear save diagnostic = %v, output %q", err, output)
			}
		} else if !errors.Is(err, errConversationSaveFailed) || !errors.Is(err, saveCause) || !strings.Contains(err.Error(), requestFailureStateSave) {
			t.Fatalf("request save diagnostic = %v", err)
		}
		if !strings.Contains(output, "PRIVATE_SAVE_CAUSE") && (err == nil || !strings.Contains(err.Error(), "PRIVATE_SAVE_CAUSE")) {
			t.Fatalf("save diagnostic omitted the underlying cause: error %v, output %q", err, output)
		}
	}
	{
		path := filepath.Join(t.TempDir(), "PRIVATE_SESSION_PATH", "session.json")
		provider := providerFunc(func(context.Context, string, string) (string, error) { return "ok", nil })
		session, err := NewPersistentSessionAt(defaultConfig(t), provider, path)
		if err != nil {
			t.Fatal(err)
		}
		saveCause, outputCause := errors.New("PRIVATE_SAVE_CAUSE"), errors.New("PRIVATE_OUTPUT_CAUSE")
		started := false
		session.store.syncDirectory = func(string) error { started = true; return saveCause }
		writer := diagnosticWriter(func(data []byte) (int, error) {
			if started {
				return 0, outputCause
			}
			return len(data), nil
		})
		err = runChatLoopWithUI(
			context.Background(), TerminalInput{reader: strings.NewReader("/clear\n"), interactive: true}, writer,
			session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
			terminalUI{interactive: true, width: 100},
		)
		if !errors.Is(err, errConversationSaveFailed) || !errors.Is(err, saveCause) || !errors.Is(err, outputCause) || !strings.Contains(err.Error(), requestFailureStateSave) || !strings.Contains(err.Error(), "PRIVATE_SAVE_CAUSE") || !strings.Contains(err.Error(), "PRIVATE_OUTPUT_CAUSE") {
			t.Fatalf("clear output failure lost a cause or request details: %v", err)
		}
	}
	{
		path := filepath.Join(t.TempDir(), "PRIVATE_LOCK_PATH", "session.json")
		providerCalls := 0
		provider := providerFunc(func(context.Context, string, string) (string, error) {
			providerCalls++
			return "ok", nil
		})
		session, err := NewPersistentSessionAt(defaultConfig(t), provider, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, turnLock := range []bool{true, false} {
			unlock, err := session.store.lock(turnLock)
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.Reply(context.Background(), "blocked request")
			if !errors.Is(err, errConversationLockBusy) || !filesync.IsLockBusy(err) || !strings.Contains(err.Error(), "lock conversation state (another instance may be using it)") {
				t.Fatalf("typed lock contention = %v", err)
			}
			if diagnostic := requestFailureDiagnostic(err); !strings.Contains(diagnostic, requestFailureLockBusy) || !strings.Contains(diagnostic, err.Error()) || !strings.Contains(diagnostic, requestUncertaintyWarning) || strings.Contains(diagnostic, path) {
				t.Fatalf("lock contention diagnostic = %q", diagnostic)
			}
			_, err = session.ReplyScheduled(context.Background(), "scheduled")
			if !errors.Is(err, ErrSessionBusy) || !errors.Is(err, errConversationLockBusy) || !filesync.IsLockBusy(err) {
				t.Fatalf("scheduled lock contention = %v", err)
			}
			var output bytes.Buffer
			err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("blocked request\n"), interactive: true}, &output,
				session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
				terminalUI{interactive: true, width: 120})
			if !errors.Is(err, errConversationLockBusy) || !strings.Contains(err.Error(), requestFailureLockBusy) || strings.Contains(err.Error(), path) {
				t.Fatalf("interactive lock contention = %v, output %q", err, output.String())
			}
			output.Reset()
			err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("/clear\n"), interactive: true}, &output,
				session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }),
				terminalUI{interactive: true, width: 120})
			if err != nil || !strings.Contains(output.String(), "Another instance may be using the conversation.") || !strings.Contains(output.String(), "Wait for it to finish") || !strings.Contains(output.String(), "Before continuing or retrying") || strings.Contains(output.String(), path) {
				t.Fatalf("clear lock contention = %v, output %q", err, output.String())
			}
			unlock()
		}
		if providerCalls != 0 {
			t.Fatalf("lock contention reached provider %d times", providerCalls)
		}
	}
}

type diagnosticWriter func([]byte) (int, error)

func (writer diagnosticWriter) Write(data []byte) (int, error) { return writer(data) }

type noticeLoopSession struct{ chatLoopSession }

func (noticeLoopSession) consumeContextNotice() string { return "Context notice" }

func TestModelUncertaintyInstructionRemainsAfterSessionReload(t *testing.T) {
	requirePrivateSessionPermissions(t)
	setupSkillFixture(t)
	credentialStore := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := credentialStore.Save(Credential{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		AccountID:    "test-account",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var requestsMu sync.Mutex
	var requests []codexRequest
	phase := "seed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		requestsMu.Lock()
		defer requestsMu.Unlock()
		requests = append(requests, request)
		if phase == "failed" {
			for _, item := range request.Input {
				if item.Type == "function_call_output" && item.CallID == "action-call" {
					if item.Output != "TOOL_OUTPUT_NOT_PERSISTED" {
						t.Errorf("unexpected tool result: %q", item.Output)
					}
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"action-call","name":"record_action","arguments":"{}"}]}`)
			return
		}
		fmt.Fprint(w, `{"output_text":"ok"}`)
	}))
	defer server.Close()
	setPhase := func(next string) {
		requestsMu.Lock()
		phase = next
		requestsMu.Unlock()
	}

	provider := NewCodexProvider(credentialStore)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	actions := 0
	if err := provider.RegisterTool(Tool{
		Name: "record_action", Description: "Record a test action.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(context.Context, string) (string, error) {
			actions++
			return "TOOL_OUTPUT_NOT_PERSISTED", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	first, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Reply(context.Background(), "first question"); err != nil {
		t.Fatal(err)
	}
	setPhase("failed")
	if _, err := first.Reply(context.Background(), "failed action"); err == nil || actions != 1 {
		t.Fatalf("failed attempt = %v; actions = %d", err, actions)
	}

	restartedProvider := NewCodexProvider(credentialStore)
	restartedProvider.backendURL = server.URL
	restartedProvider.httpClient = server.Client()
	restarted, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, restartedProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	setPhase("after-restart")
	if _, err := restarted.Reply(context.Background(), "second question"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := restarted.ReplyScheduled(context.Background(), "regular scheduled repeat"); err != nil {
			t.Fatalf("scheduled repeat %d: %v", i+1, err)
		}
	}

	requestsMu.Lock()
	gotRequests := append([]codexRequest(nil), requests...)
	requestsMu.Unlock()
	if len(gotRequests) != 6 {
		t.Fatalf("model requests = %d, want 6", len(gotRequests))
	}
	for _, request := range gotRequests {
		if !strings.Contains(request.Instructions, "If an earlier attempt may have failed or been interrupted") || !strings.Contains(request.Instructions, "Before continuing or retrying, check whether any actions completed and verify external state.") || !strings.Contains(request.Instructions, "Conversation history may omit failed or interrupted attempts, even after a restart.") || !strings.Contains(request.Instructions, "do not suppress them because previous scheduled runs occurred") || strings.Contains(request.Instructions, "Verify external state before repeating.") {
			t.Fatalf("model instructions omit uncertainty or history warning: %q", request.Instructions)
		}
		for _, item := range request.Input {
			for _, content := range item.Content {
				if strings.Contains(content.Text, requestUncertaintyWarning) || strings.Contains(content.Text, requestHistoryWarning) || strings.Contains(content.Text, requestScheduledRepeatWarning) {
					t.Fatalf("standing warning was included in conversation history: %+v", request.Input)
				}
			}
		}
	}
	resumedInput, err := json.Marshal(gotRequests[3].Input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resumedInput), "first question") || !strings.Contains(string(resumedInput), "second question") || strings.Contains(string(resumedInput), "failed action") || strings.Contains(string(resumedInput), "TOOL_OUTPUT_NOT_PERSISTED") {
		t.Fatalf("reloaded history did not reflect the failed attempt: %s", resumedInput)
	}
	for _, request := range gotRequests[4:] {
		input, err := json.Marshal(request.Input)
		if err != nil || !strings.Contains(string(input), "regular scheduled repeat") {
			t.Fatalf("scheduled repeat was not sent: %s, %v", input, err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), requestUncertaintyWarning) || strings.Contains(string(saved), requestHistoryWarning) || strings.Contains(string(saved), requestScheduledRepeatWarning) || strings.Contains(string(saved), "failed action") || strings.Contains(string(saved), "TOOL_OUTPUT_NOT_PERSISTED") {
		t.Fatalf("unexpected data in saved conversation: %s", saved)
	}
}

func TestLegacyProviderReceivesTheOriginalSingleMessage(t *testing.T) {
	var got string
	provider := providerFunc(func(_ context.Context, _ string, message string) (string, error) {
		got = message
		return "ok", nil
	})
	session := NewSession(defaultConfig(t), provider)
	if _, err := session.Reply(context.Background(), "exact single message"); err != nil {
		t.Fatal(err)
	}
	if got != "exact single message" {
		t.Fatalf("single-message provider input = %q", got)
	}
}
