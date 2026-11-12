package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/localenv"
)

func TestTelegramGetEnvironmentSendsFileWithoutAgentContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{
		"API_TOKEN":      "synthetic-secret",
		"SERVICE_REGION": "test-region",
	}); err != nil {
		t.Fatal(err)
	}

	var documentBody []byte
	messageCount := 0
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendDocument"):
				var err error
				documentBody, err = io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
					t.Fatalf("sendDocument content type = %q", r.Header.Get("Content-Type"))
				}
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				messageCount++
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte("synthetic-secret")) || bytes.Contains(body, []byte("test-region")) {
				t.Fatal("environment contents entered agent context")
			}
			t.Fatalf("/get_env made an agent request: %s", r.URL.Path)
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if messageCount != 0 {
		t.Fatalf("/get_env sent %d text messages, want only the document", messageCount)
	}
	if len(documentBody) == 0 || !bytes.Contains(documentBody, []byte(`name="chat_id"`)) || !bytes.Contains(documentBody, []byte("\r\n\r\n42\r\n")) {
		t.Fatalf("sendDocument did not target the authorized private chat; body length = %d", len(documentBody))
	}
	if !bytes.Contains(documentBody, []byte(`filename="goaster.env"`)) {
		t.Fatal("sendDocument did not use the goaster.env filename")
	}
	if !bytes.Contains(documentBody, []byte("API_TOKEN=synthetic-secret\nSERVICE_REGION=test-region\n")) {
		t.Fatal("sendDocument did not contain the saved file bytes")
	}
}

func TestTelegramGetEnvironmentCommandNamesAndBotMentions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{"KEY": "synthetic-value"}); err != nil {
		t.Fatal(err)
	}

	commands := []string{
		"/get_env",
		"/get_env@goaster_bot",
		"/get-env",
		"/get-env@goaster_bot",
	}
	documentCount := 0
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			documentCount++
			return response(http.StatusOK, `{"ok":true,"result":true}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("get environment command reached the agent")
			return nil, nil
		}),
	}

	for _, command := range commands {
		a.handle(context.Background(), privateMessage(42, command))
	}
	for _, malformed := range []string{"/get_env@", "/get_env@goaster-bot", "/get-env@goaster bot"} {
		a.handle(context.Background(), privateMessage(42, malformed))
	}
	if documentCount != len(commands) {
		t.Fatalf("sendDocument calls = %d, want one for each valid command form: %#v", documentCount, commands)
	}
}

func TestTelegramGetEnvironmentRequiresAuthorizedPrivateConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{"KEY": "synthetic-value"}); err != nil {
		t.Fatal(err)
	}

	documentCount := 0
	var messages []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendDocument"):
				documentCount++
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var payload struct {
					ChatID int64  `json:"chat_id"`
					Text   string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				messages = append(messages, payload)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("/get_env reached the agent")
			return nil, nil
		}),
	}

	unauthorized := privateMessage(99, telegramGetEnvironmentCommand)
	a.handle(context.Background(), unauthorized)
	spoofed := privateMessage(42, telegramGetEnvironmentCommand)
	spoofed.Chat.ID = 99
	a.handle(context.Background(), spoofed)
	a.handle(context.Background(), textMessage(42, -100123, "group", telegramGetEnvironmentCommand))
	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if documentCount != 1 {
		t.Fatalf("sendDocument calls = %d, want one authorized private delivery", documentCount)
	}
	if len(messages) != 1 || messages[0].ChatID != -100123 || !strings.Contains(messages[0].Text, "private chat") || strings.Contains(messages[0].Text, "synthetic-value") {
		t.Fatalf("group restriction messages = %#v", messages)
	}
}

func TestTelegramGetEnvironmentDoesNotEchoUnavailableFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".goaster", "env")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("BROKEN=synthetic-secret\nnot-a-setting\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var sent string
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = payload.Text
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unavailable environment reached the agent")
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if sent != "The environment file is unavailable." {
		t.Fatalf("unavailable file response = %q", sent)
	}
	if strings.Contains(sent, "synthetic-secret") {
		t.Fatal("unavailable file response exposed file contents")
	}
}
