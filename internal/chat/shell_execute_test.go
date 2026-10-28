package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/command"
)

func TestCodexProviderAdvertisesShellExecute(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		var shellTool *codexTool
		for i := range request.Tools {
			if request.Tools[i].Name == shellExecuteCodexTool.Name {
				shellTool = &request.Tools[i]
				break
			}
		}
		if shellTool == nil {
			t.Errorf("Codex tools = %+v, missing shell_execute", request.Tools)
		} else {
			if shellTool.Type != "function" {
				t.Errorf("shell_execute type = %q, want function", shellTool.Type)
			}
			properties, ok := shellTool.Parameters["properties"].(map[string]any)
			if !ok {
				t.Errorf("shell_execute properties = %#v", shellTool.Parameters["properties"])
			} else {
				for _, name := range []string{"command", "cwd", "timeout_ms"} {
					if _, ok := properties[name]; !ok {
						t.Errorf("shell_execute properties missing %q: %#v", name, properties)
					}
				}
				commandProperties, ok := properties["command"].(map[string]any)
				if !ok || commandProperties["maxLength"] != float64(command.MaxCommandBytes) {
					t.Errorf("shell_execute command safety schema = %#v, want maxLength %d", properties["command"], command.MaxCommandBytes)
				}
				timeoutProperties, ok := properties["timeout_ms"].(map[string]any)
				if !ok || timeoutProperties["minimum"] != float64(0) || timeoutProperties["maximum"] != float64(command.MaxTimeout/time.Millisecond) {
					t.Errorf("shell_execute timeout safety schema = %#v", properties["timeout_ms"])
				}
			}
			required, ok := shellTool.Parameters["required"].([]any)
			if !ok || len(required) != 1 || required[0] != "command" {
				t.Errorf("shell_execute required = %#v, want [command]", shellTool.Parameters["required"])
			}
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	if _, err := provider.Reply(context.Background(), "gpt-test", "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProviderExecutesShellExecuteAndSendsFollowUp(t *testing.T) {
	workDir := t.TempDir()
	args, err := json.Marshal(map[string]any{
		"command":    shellOutputCommand(),
		"cwd":        workDir,
		"timeout_ms": 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := string(args)

	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			if len(request.Input) != 1 || request.Input[0].Content[0].Text != "run command" {
				t.Errorf("initial input = %+v", request.Input)
			}
			writeCodexFunctionCall(t, w, "call-shell", "shell_execute", arguments)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			}
			if len(request.Input) >= 3 {
				call := request.Input[1]
				if call.Type != "function_call" || call.CallID != "call-shell" || call.Name != "shell_execute" || call.Arguments != arguments {
					t.Errorf("function call input = %+v", call)
				}
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "call-shell" {
					t.Errorf("function output input = %+v", output)
				}
				for _, want := range []string{"stdout", "stderr", "cwd=" + workDir, "exit_code: 7"} {
					if !strings.Contains(output.Output, want) {
						t.Errorf("shell output missing %q: %q", want, output.Output)
					}
				}
			}
			fmt.Fprint(w, `{"output_text":"Final answer after shell execution"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	reply, err := provider.Reply(context.Background(), "gpt-test", "run command")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Final answer after shell execution" {
		t.Fatalf("reply = %q", reply)
	}
	if got := backendRequests.Load(); got != 2 {
		t.Fatalf("backend requests = %d, want 2", got)
	}
}

func TestCodexProviderReturnsMalformedShellArgumentsAsToolOutput(t *testing.T) {
	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			writeCodexFunctionCall(t, w, "call-bad-shell", "shell_execute", `{not valid JSON`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			}
			if len(request.Input) >= 3 {
				call := request.Input[1]
				if call.Type != "function_call" || call.CallID != "call-bad-shell" || call.Arguments != `{not valid JSON` {
					t.Errorf("function call input = %+v", call)
				}
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "call-bad-shell" {
					t.Errorf("function output input = %+v", output)
				}
				for _, want := range []string{"execution_error: malformed shell_execute arguments", "stdout:", "stderr:", "exit_code: -1"} {
					if !strings.Contains(output.Output, want) {
						t.Errorf("malformed-argument output missing %q: %q", want, output.Output)
					}
				}
			}
			fmt.Fprint(w, `{"output_text":"Recovered after malformed arguments"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	reply, err := provider.Reply(context.Background(), "gpt-test", "run a command")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Recovered after malformed arguments" {
		t.Fatalf("reply = %q", reply)
	}
	if got := backendRequests.Load(); got != 2 {
		t.Fatalf("backend requests = %d, want 2", got)
	}
}

func TestShellExecuteBoundsTimeoutArguments(t *testing.T) {
	maxTimeoutMS := int64(command.MaxTimeout / time.Millisecond)
	for _, test := range []struct {
		name string
		args string
		want string
	}{
		{name: "negative", args: `{"command":"printf ok","timeout_ms":-1}`, want: "must not be negative"},
		{name: "too large", args: fmt.Sprintf(`{"command":"printf ok","timeout_ms":%d}`, maxTimeoutMS+1), want: "exceeds the maximum"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := parseShellExecuteArguments(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseShellExecuteArguments() error = %v, want %q", err, test.want)
			}
			if request.Command != "" || request.Dir != "" || request.Timeout != 0 {
				t.Errorf("request on parse failure = %+v, want zero value", request)
			}
		})
	}
}

func TestFormatShellExecuteResultIsExplicitAndSecretSafe(t *testing.T) {
	secret := "shell-result-secret"
	result := command.Result{
		Stdout:          "captured stdout",
		Stderr:          "captured stderr",
		ExitCode:        -1,
		StdoutTruncated: true,
		StderrTruncated: true,
	}
	formatted := formatShellExecuteResult(result, errors.New("internal detail: "+secret))
	for _, want := range []string{"execution_error: command execution failed", "stdout_truncated: true", "stderr_truncated: true", "exit_code: -1"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted result missing %q: %q", want, formatted)
		}
	}
	if strings.Contains(formatted, secret) {
		t.Errorf("formatted result exposed execution error detail: %q", formatted)
	}
	if got := formatShellExecuteResult(result, command.ErrOutputLimit); !strings.Contains(got, "execution_error: command output exceeded the limit") {
		t.Errorf("output-limit result = %q", got)
	}
}

func TestExecuteShellReportsTimeoutAsUncertainResult(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"command":    shellLoopCommand(),
		"timeout_ms": 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := executeShell(context.Background(), string(arguments))
	if err != nil {
		t.Fatalf("executeShell() error = %v", err)
	}
	for _, want := range []string{"execution_error: context deadline exceeded", "exit_code: -1"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("timeout result missing %q: %q", want, formatted)
		}
	}
}

func newShellTestProvider(t *testing.T, backend *httptest.Server) *CodexProvider {
	t.Helper()
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{
		AccessToken: "access-token",
		AccountID:   "account-123",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	return provider
}

func writeCodexFunctionCall(t *testing.T, w http.ResponseWriter, callID, name, arguments string) {
	t.Helper()
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		t.Errorf("encode function arguments: %v", err)
		return
	}
	body, err := json.Marshal(codexResponse{Output: []codexOutputItem{{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: encodedArguments,
	}}})
	if err != nil {
		t.Errorf("encode function call response: %v", err)
		return
	}
	_, _ = w.Write(body)
}

func shellOutputCommand() string {
	if runtime.GOOS == "windows" {
		return `Write-Output "stdout"; [Console]::Error.WriteLine("stderr"); Write-Output ("cwd=" + (Get-Location).Path); exit 7`
	}
	return `printf 'stdout\n'; printf 'stderr\n' >&2; printf 'cwd='; pwd; exit 7`
}

func shellLoopCommand() string {
	if runtime.GOOS == "windows" {
		return "while ($true) {}"
	}
	return "while :; do :; done"
}
