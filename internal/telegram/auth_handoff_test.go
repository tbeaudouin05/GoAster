package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubLoginAuthorizationIsPrivateAndPairingScoped(t *testing.T) {
	var authorizationRequests []int64
	var sent []string
	a := &adapter{
		token:             testToken,
		pairing:           &pairing{userID: 42},
		authControlSecret: "control-secret",
		apiBase:           "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/auth/github/authorize" {
				t.Fatalf("unexpected agent path %s", r.URL.Path)
			}
			if r.Header.Get("X-GoAster-GitHub-Auth-Control") != "control-secret" {
				t.Fatal("Telegram adapter did not authenticate its control request")
			}
			var payload struct {
				ChatID int64 `json:"chat_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			authorizationRequests = append(authorizationRequests, payload.ChatID)
			return response(http.StatusOK, "authorized\n"), nil
		}),
	}

	a.handle(context.Background(), textMessage(42, -100123, "group", "/github-login"))
	if len(authorizationRequests) != 0 || len(sent) != 1 || !strings.Contains(sent[0], "private") {
		t.Fatalf("group authorization requests=%v messages=%v", authorizationRequests, sent)
	}
	a.handle(context.Background(), privateMessage(42, "/github-login"))
	if len(authorizationRequests) != 1 || authorizationRequests[0] != 42 || len(sent) != 2 || !strings.Contains(sent[1], "authorized") {
		t.Fatalf("private authorization requests=%v messages=%v", authorizationRequests, sent)
	}
}

func TestGitHubLoginCancellationReportsPendingUntilFinalEvent(t *testing.T) {
	var sent []string
	a := &adapter{
		token:             testToken,
		pairing:           &pairing{userID: 42},
		authControlSecret: "control-secret",
		apiBase:           "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/auth/github/cancel" || r.Header.Get(telegramAuthControlHeader) != "control-secret" {
				t.Fatalf("unexpected cancellation request: %s", r.URL)
			}
			return response(http.StatusOK, "GitHub authentication cancellation was requested. Cleanup is pending.\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "/github-login cancel"))
	if len(sent) != 1 || !strings.Contains(sent[0], "cancellation was requested") || !strings.Contains(sent[0], "pending") || strings.Contains(sent[0], "was canceled") {
		t.Fatalf("cancellation message = %v", sent)
	}
}

func TestTelegramAuthHandoffSendsDeviceCodeOutOfBandAndRedactsToolArguments(t *testing.T) {
	const rawCredential = "RAW-LOGIN-OUTPUT-AND-TOKEN"
	var sent []string
	var edited []string
	a := &adapter{
		token:             testToken,
		pairing:           &pairing{userID: 42},
		authControlSecret: "control-secret",
		apiBase:           "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			var payload struct {
				MessageID int64  `json:"message_id"`
				Text      string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				sent = append(sent, payload.Text)
				return response(http.StatusOK, `{"ok":true,"result":{"message_id":7}}`), nil
			case strings.HasSuffix(r.URL.Path, "/editMessageText"):
				edited = append(edited, fmt.Sprintf("%d:%s", payload.MessageID, payload.Text))
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get(telegramAuthControlHeader) != "control-secret" {
				t.Fatal("Telegram adapter did not authenticate its chat request")
			}
			stream := strings.Join([]string{
				`{"type":"tool_call","name":"auth_handoff","arguments":"{\"provider\":\"github\",\"token\":\"` + rawCredential + `\"}"}`,
				`{"type":"auth_handoff","provider":"github","status":"started","verification_url":"https://github.com/login/device","user_code":"ABCD-EFGH","expires_at":"2026-11-02T07:00:00.123Z"}`,
				`{"type":"auth_handoff","provider":"github","status":"completed","account":"alice"}`,
				`{"type":"final","reply":"GitHub authentication completed and the account was verified."}`,
			}, "\n") + "\n"
			return response(http.StatusOK, stream), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, "continue"))
	if len(sent) != 3 || !strings.Contains(sent[0], "GitHub sign-in handoff started") || !strings.Contains(sent[1], "ABCD-EFGH") || !strings.Contains(sent[1], "2026-11-02T07:00:00.123Z") || !strings.Contains(sent[2], "alice") {
		t.Fatalf("production Telegram messages = %v", sent)
	}
	if len(edited) != 1 || !strings.Contains(edited[0], "authentication completed") {
		t.Fatalf("production Telegram edits = %v", edited)
	}
	for _, message := range append(sent, edited...) {
		if strings.Contains(message, rawCredential) {
			t.Fatalf("raw tool argument reached Telegram through production callback: %q", message)
		}
	}
}

func TestTelegramAuthHandoffRejectsUntrustedCompletionOutput(t *testing.T) {
	const rawOutput = "raw-login-output\nsecret"
	a := &adapter{
		token:   testToken,
		apiBase: "https://example.invalid",
		client: doerFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			stream := `{"type":"auth_handoff","provider":"github","status":"completed","account":"` + rawOutput + `"}` + "\n"
			return response(http.StatusOK, stream), nil
		}),
	}

	_, err := a.askAgent(context.Background(), "continue", 42, nil, nil)
	if err == nil || strings.Contains(err.Error(), rawOutput) {
		t.Fatalf("untrusted completion result = %v", err)
	}
}
