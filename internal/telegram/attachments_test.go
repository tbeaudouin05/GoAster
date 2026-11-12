package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"goaster/internal/attachment"
)

func TestTelegramAttachmentMessageDecodingSelectsLargestPhoto(t *testing.T) {
	var decoded update
	if err := json.Unmarshal([]byte(`{
		"update_id": 7,
		"message": {
			"caption": "please inspect",
			"photo": [
				{"file_id":"small","width":90,"height":90,"file_size":1000},
				{"file_id":"large","width":1200,"height":800,"file_size":5000}
			],
			"chat":{"id":42,"type":"private"},
			"from":{"id":42}
		}
	}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Message == nil || decoded.Message.Caption != "please inspect" || len(decoded.Message.Photo) != 2 {
		t.Fatalf("decoded message = %#v", decoded.Message)
	}
	source, err := decoded.Message.attachmentSource()
	if err != nil {
		t.Fatal(err)
	}
	if source.kind != "image" || source.fileID != "large" || source.filename != "photo.jpg" {
		t.Fatalf("photo source = %+v", source)
	}

	var documentUpdate update
	if err := json.Unmarshal([]byte(`{
		"message": {
			"caption":"read this",
			"document":{"file_id":"doc-1","file_name":"../../notes.txt","mime_type":"text/plain","file_size":12},
			"chat":{"id":42,"type":"private"},
			"from":{"id":42}
		}
	}`), &documentUpdate); err != nil {
		t.Fatal(err)
	}
	source, err = documentUpdate.Message.attachmentSource()
	if err != nil {
		t.Fatal(err)
	}
	if source.kind != "document" || source.fileID != "doc-1" || source.filename != "notes.txt" || source.mimeType != "text/plain" || source.fileSize != 12 {
		t.Fatalf("document source = %+v", source)
	}
}

func TestPairedAttachmentDownloadsAndForwardsBytesAndCaption(t *testing.T) {
	payload := []byte("document payload")
	var forwarded struct {
		Message     string                `json:"message"`
		ChatID      int64                 `json:"chat_id"`
		Attachments []attachment.Incoming `json:"attachments"`
	}
	var sent []string
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				var request struct {
					FileID string `json:"file_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.FileID != "doc-1" {
					t.Fatalf("getFile request = %+v, err = %v", request, err)
				}
				return response(http.StatusOK, `{"ok":true,"result":{"file_size":15,"file_path":"documents/file_1.txt"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(payload))), ContentLength: int64(len(payload))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "http://127.0.0.1:8765/chat" {
				t.Fatalf("unexpected agent request %s", r.URL)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || bytes.Contains(body, []byte(`"path"`)) {
				t.Fatalf("wire request contained a path or could not be read: %v", err)
			}
			if err := json.Unmarshal(body, &forwarded); err != nil {
				t.Fatal(err)
			}
			if len(forwarded.Attachments) != 1 {
				t.Fatalf("forwarded attachments = %#v", forwarded.Attachments)
			}
			item := forwarded.Attachments[0]
			if item.Filename != "notes.txt" || item.Type != "document" || item.MIMEType != "text/plain" || item.Size != int64(len(payload)) {
				t.Fatalf("forwarded metadata = %+v", item)
			}
			if !bytes.Equal(item.Data, payload) {
				t.Fatalf("forwarded bytes = %q, want %q", item.Data, payload)
			}
			return response(http.StatusOK, `{"type":"final","reply":"processed"}`+"\n"), nil
		}),
	}
	m := textMessage(42, 42, "private", "")
	m.Caption = "please inspect"
	m.Document = &document{FileID: "doc-1", FileName: "../notes.txt", MIMEType: "text/plain", FileSize: int64(len(payload))}
	a.handle(context.Background(), m)

	if len(forwarded.Attachments) != 1 || forwarded.Message != "please inspect" || forwarded.ChatID != 42 || len(sent) != 1 || sent[0] != "processed" {
		t.Fatalf("forwarded message = %+v, sent = %q", forwarded, sent)
	}
}

func TestTelegramEnvironmentDocumentNeverEntersOrdinaryChat(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	sent := 0
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("reserved environment document reached Telegram download: %s", r.URL.Path)
			}
			sent++
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("reserved environment document reached the model")
			return nil, nil
		}),
	}
	for _, tc := range []struct {
		caption  string
		chatType string
		chatID   int64
	}{
		{chatType: "private", chatID: 42},
		{caption: "please inspect", chatType: "private", chatID: 42},
		{chatType: "group", chatID: -100123},
	} {
		m := privateMessage(42, "")
		m.Caption = tc.caption
		m.Chat.Type = tc.chatType
		m.Chat.ID = tc.chatID
		if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","mime_type":"text/plain","file_size":17}}`), m); err != nil {
			t.Fatal(err)
		}
		a.handle(context.Background(), m)
	}
	if sent != 3 {
		t.Fatalf("reserved environment document responses = %d, want 3", sent)
	}
}

func TestTelegramEnvironmentRequiresExactAttachmentAndBindsAcceptanceIdentity(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	payload := []byte("EXISTING=updated-secret\nADDED=new-secret\n")
	var sent []string
	var forwarded []struct {
		Message     string                `json:"message"`
		ChatID      int64                 `json:"chat_id"`
		UserID      int64                 `json:"user_id"`
		Attachments []attachment.Incoming `json:"attachments"`
	}
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				var request struct {
					FileID string `json:"file_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.FileID != "env-file" {
					t.Fatalf("environment getFile request = %+v, err = %v", request, err)
				}
				return response(http.StatusOK, `{"ok":true,"result":{"file_size":40,"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(http.StatusOK, string(payload))
				fileResponse.ContentLength = int64(len(payload))
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				if strings.Contains(request.Text, "updated-secret") || strings.Contains(request.Text, "new-secret") {
					t.Fatalf("Telegram message exposed an environment value: %q", request.Text)
				}
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
			var request struct {
				Message     string                `json:"message"`
				ChatID      int64                 `json:"chat_id"`
				UserID      int64                 `json:"user_id"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"path"`) {
				t.Fatal("environment request contained a client-controlled path")
			}
			if request.Message == telegramEnvironmentCommand {
				if len(request.Attachments) == 0 {
					return response(http.StatusOK, `{"type":"final","reply":"Environment proposal cleared."}`+"\n"), nil
				}
				if request.ChatID != 42 || request.UserID != 42 || len(request.Attachments) != 1 || request.Attachments[0].Filename != telegramEnvironmentFilename {
					t.Fatalf("environment preview identity or attachment = %+v", request)
				}
				forwarded = append(forwarded, request)
				return response(http.StatusOK, `{"type":"final","reply":"Environment proposal (values hidden). Added: ADDED. Modified: EXISTING. Reply exactly ACCEPT."}`+"\n"), nil
			}
			if request.Message != "/env ACCEPT" || request.ChatID != 42 || request.UserID != 42 || len(request.Attachments) != 0 {
				t.Fatalf("environment acceptance request = %+v", request)
			}
			forwarded = append(forwarded, request)
			return response(http.StatusOK, `{"type":"final","reply":"Environment settings saved locally."}`+"\n"), nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramEnvironmentCommand))
	if len(sent) != 1 || !strings.Contains(sent[0], "goaster.env") || !strings.Contains(sent[0], "same message") {
		t.Fatalf("missing-file instructions = %q", sent)
	}

	wrongName := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"wrong-file","file_name":"settings.env"}}`), wrongName); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), wrongName)
	if len(sent) != 2 || !strings.Contains(sent[1], "named goaster.env") {
		t.Fatalf("wrong-name response = %q", sent)
	}

	tooLarge := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"large-file","file_name":"goaster.env","file_size":1048577}}`), tooLarge); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), tooLarge)
	if len(sent) != 3 || !strings.Contains(sent[2], "1 MiB") {
		t.Fatalf("oversized environment response = %q", sent)
	}

	unauthorized := privateMessage(99, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env"}}`), unauthorized); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), unauthorized)
	if len(forwarded) != 0 {
		t.Fatal("unauthorized environment attachment reached the agent")
	}

	preview := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","mime_type":"text/plain","file_size":1}}`), preview); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), preview)
	if len(forwarded) != 1 || len(sent) != 4 || !strings.Contains(sent[3], "values hidden") || strings.Contains(sent[3], "updated-secret") || strings.Contains(sent[3], "new-secret") {
		t.Fatalf("environment preview forwarding = %+v, sent = %q", forwarded, sent)
	}
	a.handle(context.Background(), privateMessage(42, " ACCEPT "))
	if len(forwarded) != 1 || len(sent) != 5 || !strings.Contains(sent[4], "Reply exactly ACCEPT") {
		t.Fatalf("non-exact acceptance changed proposal state: forwarded = %+v, sent = %q", forwarded, sent)
	}
	a.handle(context.Background(), privateMessage(42, telegramEnvironmentAcceptKeyword))
	if len(forwarded) != 2 || len(sent) != 6 || sent[5] != "Environment settings saved locally." {
		t.Fatalf("environment acceptance forwarding = %+v, sent = %q", forwarded, sent)
	}
}

func TestTelegramMentionedEnvironmentCommandStaysOutOfNormalChat(t *testing.T) {
	payload := []byte("MENTIONED=fake-secret\n")
	var sent []string
	var agentMessage string
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(http.StatusOK, string(payload))
				fileResponse.ContentLength = int64(len(payload))
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message     string                `json:"message"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Message != telegramEnvironmentCommand || len(request.Attachments) != 1 {
				t.Fatalf("mentioned environment request fell through or was malformed: %+v", request)
			}
			agentMessage = request.Message
			return response(http.StatusOK, `{"type":"final","reply":"Environment proposal (values hidden). Reply exactly ACCEPT."}`+"\n"), nil
		}),
	}
	m := privateMessage(42, "/env@botname")
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","mime_type":"text/plain","file_size":1}}`), m); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), m)

	if agentMessage != telegramEnvironmentCommand || len(sent) != 1 || !strings.Contains(sent[0], "values hidden") || strings.Contains(sent[0], "fake-secret") {
		t.Fatalf("mentioned environment handling = agent %q, sent %q", agentMessage, sent)
	}
}

func TestTelegramEnvironmentPreviewDeliveryFailureInvalidatesProposal(t *testing.T) {
	payload := []byte("KEY=fake-secret\n")
	var sent []string
	var agentMessages []string
	proposalPending := false
	sendMessages := 0
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(http.StatusOK, string(payload))
				fileResponse.ContentLength = int64(len(payload))
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				sendMessages++
				if sendMessages == 2 {
					return response(http.StatusBadGateway, `{"ok":false}`), nil
				}
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message     string                `json:"message"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			agentMessages = append(agentMessages, request.Message)
			switch request.Message {
			case telegramEnvironmentCommand:
				if len(request.Attachments) == 0 {
					proposalPending = false
					return response(http.StatusOK, `{"type":"final","reply":"proposal cleared"}`+"\n"), nil
				}
				proposalPending = true
				return response(http.StatusOK, `{"type":"final","reply":"`+strings.Repeat("P", 2001)+`"}`+"\n"), nil
			case telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword:
				if proposalPending {
					return response(http.StatusOK, `{"type":"final","reply":"unexpectedly saved"}`+"\n"), nil
				}
				return response(http.StatusOK, `{"type":"final","reply":"No environment proposal is pending"}`+"\n"), nil
			default:
				t.Fatalf("unexpected agent message %q", request.Message)
				return nil, nil
			}
		}),
	}
	preview := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","mime_type":"text/plain","file_size":1}}`), preview); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), preview)
	a.handle(context.Background(), privateMessage(42, telegramEnvironmentAcceptKeyword))

	if !reflect.DeepEqual(agentMessages, []string{
		telegramEnvironmentCommand,
		telegramEnvironmentCommand,
		telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword,
	}) {
		t.Fatalf("preview delivery invalidation sequence = %q", agentMessages)
	}
	if proposalPending {
		t.Fatal("preview delivery failure left a proposal pending")
	}
	if len(sent) != 2 || !strings.Contains(sent[1], "No environment proposal is pending") {
		t.Fatalf("acceptance after preview delivery failure = %q", sent)
	}
}

func TestTelegramEnvironmentSecretFlowIsPrivateOnly(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	var sent []string
	getFileCalls := 0
	agentCalls := 0
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				getFileCalls++
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/goaster.env"}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			agentCalls++
			return response(http.StatusOK, `{"type":"final","reply":"unexpected"}`+"\n"), nil
		}),
	}
	group := textMessage(42, -100123, "group", telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"secret-file","file_name":"goaster.env","file_size":32}}`), group); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), group)
	if getFileCalls != 0 || agentCalls != 0 {
		t.Fatalf("group environment upload reached a secret flow: getFile=%d agent=%d", getFileCalls, agentCalls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "only in a private chat") {
		t.Fatalf("group restriction response = %q", sent)
	}
}

func TestTelegramMalformedEnvironmentCommandsCannotBypassGroupRestriction(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	var sent []string
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var request struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, request.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("malformed group environment command reached the agent")
			return nil, nil
		}),
	}
	for _, text := range []string{"/env\nwith extra", "/env\twith extra", "ACCEPT now"} {
		group := textMessage(42, -100123, "group", text)
		if err := json.Unmarshal([]byte(`{"document":{"file_id":"secret-file","file_name":"goaster.env","file_size":32}}`), group); err != nil {
			t.Fatal(err)
		}
		a.handle(context.Background(), group)
	}
	if len(sent) != 3 {
		t.Fatalf("group malformed environment responses = %q", sent)
	}
	for _, reply := range sent {
		if !strings.Contains(reply, "only in a private chat") {
			t.Fatalf("group malformed environment response = %q", reply)
		}
	}
}

func TestTelegramRejectedReplacementInvalidatesPendingProposal(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	payload := []byte("NEW_VALUE=new-secret\n")
	var sent []string
	proposalPending := false
	var agentCalls []string
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(http.StatusOK, string(payload))
				fileResponse.ContentLength = int64(len(payload))
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message     string                `json:"message"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			agentCalls = append(agentCalls, request.Message)
			switch request.Message {
			case telegramEnvironmentCommand:
				if len(request.Attachments) == 0 {
					proposalPending = false
					return response(http.StatusOK, `{"type":"final","reply":"proposal cleared"}`+"\n"), nil
				}
				proposalPending = true
				return response(http.StatusOK, `{"type":"final","reply":"preview"}`+"\n"), nil
			case telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword:
				if proposalPending {
					proposalPending = false
					return response(http.StatusOK, `{"type":"final","reply":"saved"}`+"\n"), nil
				}
				return response(http.StatusOK, `{"type":"final","reply":"No environment proposal is pending"}`+"\n"), nil
			default:
				t.Fatalf("unexpected agent message %q", request.Message)
				return nil, nil
			}
		}),
	}
	preview := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","file_size":1}}`), preview); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), preview)
	rejected := privateMessage(42, telegramEnvironmentCommand+" extra")
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","file_size":1}}`), rejected); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), rejected)
	a.handle(context.Background(), privateMessage(42, telegramEnvironmentAcceptKeyword))
	if !reflect.DeepEqual(agentCalls, []string{telegramEnvironmentCommand, telegramEnvironmentCommand, telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword}) {
		t.Fatalf("agent invalidation sequence = %q", agentCalls)
	}
	if len(sent) != 3 || !strings.Contains(sent[1], "To update settings") || !strings.Contains(sent[2], "No environment proposal is pending") {
		t.Fatalf("rejected replacement acceptance = %q", sent)
	}
}

func TestTelegramMediaGroupEnvironmentReplacementInvalidatesPendingProposal(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	payload := []byte("NEW_VALUE=new-secret\n")
	var sent []string
	proposalPending := false
	var agentCalls []string
	a := adapter{
		token:   testToken,
		pairing: paired,
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/goaster.env"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				fileResponse := response(http.StatusOK, string(payload))
				fileResponse.ContentLength = int64(len(payload))
				return fileResponse, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, request.Text)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Message     string                `json:"message"`
				Attachments []attachment.Incoming `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			agentCalls = append(agentCalls, request.Message)
			switch request.Message {
			case telegramEnvironmentCommand:
				if len(request.Attachments) == 0 {
					proposalPending = false
					return response(http.StatusOK, `{"type":"final","reply":"proposal cleared"}`+"\n"), nil
				}
				proposalPending = true
				return response(http.StatusOK, `{"type":"final","reply":"preview"}`+"\n"), nil
			case telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword:
				if proposalPending {
					proposalPending = false
					return response(http.StatusOK, `{"type":"final","reply":"saved"}`+"\n"), nil
				}
				return response(http.StatusOK, `{"type":"final","reply":"No environment proposal is pending"}`+"\n"), nil
			default:
				t.Fatalf("unexpected agent message %q", request.Message)
				return nil, nil
			}
		}),
	}
	preview := privateMessage(42, telegramEnvironmentCommand)
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","file_size":1}}`), preview); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), preview)
	mediaGroupReplacement := privateMessage(42, telegramEnvironmentCommand)
	mediaGroupReplacement.MediaGroupID = "album-1"
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"env-file","file_name":"goaster.env","file_size":1}}`), mediaGroupReplacement); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), mediaGroupReplacement)
	if proposalPending {
		t.Fatal("media-group replacement left the fake proposal pending before acceptance")
	}
	a.handle(context.Background(), privateMessage(42, telegramEnvironmentAcceptKeyword))

	if !reflect.DeepEqual(agentCalls, []string{
		telegramEnvironmentCommand,
		telegramEnvironmentCommand,
		telegramEnvironmentCommand + " " + telegramEnvironmentAcceptKeyword,
	}) {
		t.Fatalf("media-group invalidation sequence = %q", agentCalls)
	}
	if proposalPending {
		t.Fatal("media-group replacement left a proposal pending")
	}
	if len(sent) != 2 || sent[0] != "preview" || !strings.Contains(sent[1], "No environment proposal is pending") {
		t.Fatalf("media-group replacement responses = %q", sent)
	}
}

func TestTelegramDocumentImageIsClassifiedFromBytes(t *testing.T) {
	payload := testPNGBytes(t)
	var forwarded struct {
		Attachments []attachment.Incoming `json:"attachments"`
	}
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/photo.bin"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
				t.Fatal(err)
			}
			return response(http.StatusOK, `{"type":"final","reply":"image received"}`+"\n"), nil
		}),
	}
	m := textMessage(42, 42, "private", "inspect")
	m.Document = &document{FileID: "photo-document", FileName: "photo.txt", MIMEType: "text/plain", FileSize: int64(len(payload))}
	a.handle(context.Background(), m)
	if len(forwarded.Attachments) != 1 || forwarded.Attachments[0].Type != "image" || forwarded.Attachments[0].MIMEType != "image/png" {
		t.Fatalf("document image metadata = %+v", forwarded.Attachments)
	}
}

func TestUnsupportedImageIsReportedExplicitly(t *testing.T) {
	payload := []byte("not an image")
	var sent string
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/bad.png"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = request.Text
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unsupported image reached the agent")
			return nil, nil
		}),
	}
	m := textMessage(42, 42, "private", "inspect")
	m.Document = &document{FileID: "bad-image", FileName: "bad.png", MIMEType: "image/png", FileSize: int64(len(payload))}
	a.handle(context.Background(), m)
	if !strings.Contains(sent, "unsupported") || !strings.Contains(sent, "JPEG") || !strings.Contains(sent, "WebP") {
		t.Fatalf("unsupported-image response = %q", sent)
	}
}

func TestPairedMediaOnlyMessageIsForwarded(t *testing.T) {
	payload := testPNGBytes(t)
	var forwarded struct {
		Message     string                `json:"message"`
		Attachments []attachment.Incoming `json:"attachments"`
	}
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			if strings.HasSuffix(r.URL.Path, "/getFile") {
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"photos/file.jpg"}}`), nil
			}
			if strings.HasPrefix(r.URL.Path, "/file/bot") {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload))}, nil
			}
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			}
			t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			return nil, nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
				t.Fatal(err)
			}
			return response(http.StatusOK, `{"type":"final","reply":"image received"}`+"\n"), nil
		}),
	}
	m := textMessage(42, 42, "private", "")
	m.Photo = []photoSize{{FileID: "photo-1", Width: 100, Height: 100}}
	a.handle(context.Background(), m)
	if forwarded.Message != "" || len(forwarded.Attachments) != 1 || forwarded.Attachments[0].Type != "image" {
		t.Fatalf("media-only forwarding = %+v", forwarded)
	}
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, color.RGBA{R: 0x22, G: 0x66, B: 0xaa, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestUnauthorizedAttachmentIsNotDownloaded(t *testing.T) {
	called := false
	a := &adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			t.Fatalf("unauthorized message reached Telegram API: %s", r.URL.Path)
			return nil, nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unauthorized message reached agent")
			return nil, nil
		}),
	}
	m := textMessage(99, 99, "private", "")
	m.Document = &document{FileID: "secret-file", FileName: "secret.txt"}
	a.handle(context.Background(), m)
	if called {
		t.Fatal("unauthorized attachment was downloaded")
	}
}

func TestMediaAlbumIsIgnored(t *testing.T) {
	a := &adapter{
		pairing: &pairing{userID: 42},
		client: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("album reached Telegram API")
			return nil, nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("album reached agent")
			return nil, nil
		}),
	}
	m := textMessage(42, 42, "private", "")
	m.MediaGroupID = "album-1"
	m.Photo = []photoSize{{FileID: "photo-1"}}
	a.handle(context.Background(), m)
}

func TestAttachmentDownloadEnforcesBoundedSizeAndRedactsErrors(t *testing.T) {
	var sent string
	a := &adapter{
		token:              testToken,
		pairing:            &pairing{userID: 42},
		apiBase:            "https://example.invalid",
		attachmentMaxBytes: 4,
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			if strings.HasSuffix(r.URL.Path, "/getFile") {
				return response(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/large.bin"}}`), nil
			}
			if strings.HasPrefix(r.URL.Path, "/file/bot") {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("12345")), ContentLength: -1}, nil
			}
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				var request struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				sent = request.Text
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			}
			t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			return nil, nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("oversized attachment reached the agent")
			return nil, nil
		}),
	}
	m := textMessage(42, 42, "private", "")
	m.Document = &document{FileID: "large-file", FileName: "large.bin"}
	a.handle(context.Background(), m)
	if sent == "" || strings.Contains(sent, testToken) || !strings.Contains(sent, "could not be downloaded") {
		t.Fatalf("size-limit response = %q", sent)
	}
}

func TestTelegramFileURLRejectsUnsafePathsWithoutLeakingToken(t *testing.T) {
	for _, filePath := range []string{"../secret", "/absolute", `..\secret`, "photos/../secret"} {
		_, err := telegramFileURL("https://example.invalid", testToken, filePath)
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatalf("file path %q produced unsafe error %v", filePath, err)
		}
	}
}
