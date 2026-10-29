package chat

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"goaster/internal/localenv"
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

func TestLoop(t *testing.T) {
	var output bytes.Buffer
	var messages []string
	provider := providerFunc(func(_ context.Context, model, message string) (string, error) {
		messages = append(messages, message)
		return "received " + message, nil
	})
	err := runWithTestEnvStore(t, context.Background(), strings.NewReader("\n hello \n/history\n/clear\n/quit\n/exit\nsecond\n"), &output, NewSession(defaultConfig(t), provider))
	if err != nil || !reflect.DeepEqual(messages, []string{"hello", "/history", "/clear", "/quit", "/exit", "second"}) {
		t.Fatalf("messages = %q, error = %v", messages, err)
	}
	for _, want := range []string{
		"chat provider", "Type a message, then press Enter.",
		"Active model: gpt-6.1-sol", "you> ", "assistant [gpt-6.1-sol]> received hello",
		"assistant [gpt-6.1-sol]> received /history", "assistant [gpt-6.1-sol]> received /clear",
		"assistant [gpt-6.1-sol]> received /quit", "assistant [gpt-6.1-sol]> received /exit",
		"assistant [gpt-6.1-sol]> received second",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, &output)
		}
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

func TestEnvPickerCancellationPreservesSavedValues(t *testing.T) {
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

func TestEnvPickerUnavailableShowsInstallInstructionsAndContinues(t *testing.T) {
	missingPicker := &exec.Error{Name: "zenity", Err: exec.ErrNotFound}
	err := wrapNativeEnvironmentFilePickerError(missingPicker)
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
