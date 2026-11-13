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
	"time"
)

const testMigrationHandoff = "owner URL: https://migration.example/migration#attempt=attempt&owner=owner-secret\n-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key\n-----END OPENSSH PRIVATE KEY-----\n"

func migrationHandoffEnvelope(t *testing.T, expires time.Time, document string) string {
	t.Helper()
	envelope, err := json.Marshal(struct {
		Expires  int64  `json:"expires"`
		Document string `json:"document"`
	}{Expires: expires.Unix(), Document: document})
	if err != nil {
		t.Fatal(err)
	}
	return string(envelope)
}

func migrationHandoffFile(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	pathName := filepath.Join(t.TempDir(), "migration-handoff")
	if err := os.WriteFile(pathName, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	return pathName
}

func TestTelegramMigrationHandoffIsSentDirectlyToAuthorizedPrivateOwner(t *testing.T) {
	pathName := migrationHandoffFile(t, migrationHandoffEnvelope(t, time.Now().Add(time.Hour), testMigrationHandoff), 0600)
	var documentBody []byte
	messageCount := 0
	a := adapter{
		token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		migrationHandoffPath: pathName,
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
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("migration handoff reached the agent/model stream")
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramGetMigrationCommand))

	if messageCount != 0 {
		t.Fatalf("migration handoff sent %d text messages, want only a document", messageCount)
	}
	if !bytes.Contains(documentBody, []byte(`name="chat_id"`)) || !bytes.Contains(documentBody, []byte("\r\n\r\n42\r\n")) {
		t.Fatalf("migration handoff targeted the wrong chat; body length = %d", len(documentBody))
	}
	if !bytes.Contains(documentBody, []byte(`filename="`+migrationHandoffFilename+`"`)) || !bytes.Contains(documentBody, []byte(testMigrationHandoff)) {
		t.Fatal("migration handoff document did not contain the protected owner handoff")
	}
}

func TestReadMigrationHandoffRejectsInvalidEnvelopes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name     string
		contents string
	}{
		{
			name:     "expired",
			contents: migrationHandoffEnvelope(t, now.Add(-time.Second), "expired-secret"),
		},
		{
			name:     "missing expiry",
			contents: `{"document":"missing-expiry-secret"}`,
		},
		{
			name:     "malformed",
			contents: `{"expires":1800000000,"document":`,
		},
		{
			name:     "more than seven days future",
			contents: migrationHandoffEnvelope(t, now.Add(migrationHandoffMaxAge+time.Second), "future-secret"),
		},
		{
			name:     "oversized",
			contents: strings.Repeat("x", migrationHandoffMaxBytes+1),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			pathName := migrationHandoffFile(t, test.contents, 0600)
			if _, err := readMigrationHandoffAt(pathName, now); err == nil {
				t.Fatal("readMigrationHandoffAt accepted an invalid envelope")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid envelope error exposed secret data: %q", err)
			}
		})
	}
}

func TestTelegramMigrationHandoffRejectsUnauthorizedGroupsAndAttachments(t *testing.T) {
	pathName := migrationHandoffFile(t, migrationHandoffEnvelope(t, time.Now().Add(time.Hour), testMigrationHandoff), 0600)
	documentCount := 0
	var messages []string
	a := adapter{
		token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		migrationHandoffPath: pathName,
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendDocument") {
				documentCount++
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("migration handoff request reached the agent/model stream")
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(99, telegramGetMigrationCommand))
	spoofed := privateMessage(42, telegramGetMigrationCommand)
	spoofed.Chat.ID = 99
	a.handle(context.Background(), spoofed)
	a.handle(context.Background(), textMessage(42, -100123, "group", telegramGetMigrationCommand))
	withAttachment := privateMessage(42, telegramGetMigrationCommand)
	withAttachment.Document = &document{FileID: "not-downloaded", FileName: "anything.txt", FileSize: 1}
	a.handle(context.Background(), withAttachment)

	if documentCount != 0 {
		t.Fatalf("unauthorized migration handoff document calls = %d", documentCount)
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "private chat") || !strings.Contains(messages[1], "without an attachment") {
		t.Fatalf("safe migration handoff rejection messages = %#v", messages)
	}
	for _, message := range messages {
		if strings.Contains(message, "owner-secret") || strings.Contains(message, "private-key") {
			t.Fatalf("rejection exposed handoff data: %q", message)
		}
	}
}

func TestTelegramMigrationHandoffFailureDoesNotEchoData(t *testing.T) {
	pathName := migrationHandoffFile(t, migrationHandoffEnvelope(t, time.Now().Add(time.Hour), testMigrationHandoff), 0600)
	var messages []string
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendDocument") {
			return response(http.StatusBadGateway, `{"ok":false,"description":"sensitive upstream error"}`), nil
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, payload.Text)
		return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
	})
	a := adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", migrationHandoffPath: pathName, client: client}
	a.handle(context.Background(), privateMessage(42, telegramGetMigrationCommand))
	if len(messages) != 1 || messages[0] != "The migration handoff could not be sent." {
		t.Fatalf("delivery failure response = %#v", messages)
	}
	if strings.Contains(messages[0], "sensitive") || strings.Contains(messages[0], "owner-secret") {
		t.Fatal("delivery failure exposed secret data")
	}

	missing := adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", migrationHandoffPath: filepath.Join(t.TempDir(), "missing"), client: client}
	messages = nil
	missing.handle(context.Background(), privateMessage(42, telegramGetMigrationCommand))
	if len(messages) != 1 || messages[0] != "The migration handoff could not be sent." {
		t.Fatalf("missing handoff response = %#v", messages)
	}

	if err := os.Chmod(pathName, 0644); err != nil {
		t.Fatal(err)
	}
	unsafe := adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", migrationHandoffPath: pathName, client: client}
	messages = nil
	unsafe.handle(context.Background(), privateMessage(42, telegramGetMigrationCommand))
	if len(messages) != 1 || messages[0] != "The migration handoff could not be sent." {
		t.Fatalf("unsafe handoff response = %#v", messages)
	}
}
