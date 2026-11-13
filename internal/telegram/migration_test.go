package telegram

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationHandoffUsesOnlyStoredPrivateUserAndDocumentDelivery(t *testing.T) {
	runtime := t.TempDir()
	paths := Paths{
		Token:  filepath.Join(runtime, "token"),
		UserID: filepath.Join(runtime, "user-id"),
	}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	var requestPath string
	var requestBody []byte
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.Path
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})
	data := []byte("owner-link\nServer ID: 9001\nprivate key material")
	if err := sendMigrationHandoffWith(t.Context(), paths, client, "https://telegram.synthetic", data); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/bot"+testToken+"/sendDocument" {
		t.Fatalf("Telegram endpoint = %q", requestPath)
	}
	bodyText := string(requestBody)
	if !strings.Contains(bodyText, "name=\"chat_id\"") || !strings.Contains(bodyText, "42") || !strings.Contains(bodyText, "private key material") {
		t.Fatalf("private-user document body = %q", bodyText)
	}
	if strings.Contains(bodyText, "-100") || strings.Contains(bodyText, "model-supplied") {
		t.Fatalf("document delivery leaked a group or model destination: %q", bodyText)
	}
}
