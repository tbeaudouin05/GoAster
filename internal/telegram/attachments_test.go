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
			return response(http.StatusOK, `{"reply":"processed"}`), nil
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
			return response(http.StatusOK, `{"reply":"image received"}`), nil
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
			return response(http.StatusOK, `{"reply":"image received"}`), nil
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
