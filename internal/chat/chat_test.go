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
	"runtime"
	"strings"
	"testing"

	"goaster/internal/attachment"
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
		"OVH onboarding started.",
		"create your Public Cloud project yourself in OVHcloud Manager",
		"https://www.ovh.com/manager/#/hub/public-cloud",
		"After creation, Discovery mode is the restricted starting state.",
		"click OVHcloud Manager's Enable or Activate button only to start the payment-method activation flow.",
		"provide or add a payment method in that flow to deactivate and exit Discovery mode.",
		"The required final state is an active project with no Discovery mode label.",
		"GoAster cannot provision a server while the project remains in Discovery mode.",
		"https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project",
		"Return to GoAster only after the project is active and the Discovery mode label is gone.",
		"verify the user-created project with the read-only GET /cloud/project.",
		"GoAster does not create projects.",
		"Ask for a preferred Public Cloud deployment region only if it is genuinely unknown.",
		"GoAster remote-agent workload, Ubuntu LTS, the smallest suitable machine, SSH-key-only access, minimal inbound exposure, and a hard US$8/month recurring-cost ceiling.",
		"report the assumptions and current expected cost and wait for explicit approval before billable creation or other mutations.",
		"Use the local /env flow for secrets; never paste credentials into chat.",
		"does not provision or change any OVH resource.",
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
