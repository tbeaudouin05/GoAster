package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/migration"
)

func TestMigrationHandoffUsesOnlyStoredPairedUser(t *testing.T) {
	runtime := t.TempDir()
	paths := Paths{Token: filepath.Join(runtime, "token"), UserID: filepath.Join(runtime, "user-id")}
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
	data := []byte("GoAster migration setup: https://web.example/migration#attempt=abc&owner=secret\nprivate key material")
	if err := sendMigrationHandoffWith(t.Context(), paths, client, "https://telegram.synthetic", data); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/bot"+testToken+"/sendDocument" {
		t.Fatalf("Telegram endpoint = %q", requestPath)
	}
	bodyText := string(requestBody)
	if !strings.Contains(bodyText, "name=\"chat_id\"") || !strings.Contains(bodyText, "42") || !strings.Contains(bodyText, "private key material") {
		t.Fatalf("paired-user document body = %q", bodyText)
	}
	if strings.Contains(bodyText, "-100") || strings.Contains(bodyText, "model-supplied") {
		t.Fatalf("document delivery leaked a group or model destination: %q", bodyText)
	}
}

func TestMigrationApprovalDeliveryUsesStoredPairedPrivateUser(t *testing.T) {
	runtime := t.TempDir()
	paths := Paths{Token: filepath.Join(runtime, "token"), UserID: filepath.Join(runtime, "user-id")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	var requestBody struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot"+testToken+"/sendMessage" {
			t.Fatalf("Telegram approval endpoint = %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			return nil, err
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":1}}`), nil
	})
	notice := migration.ApprovalNotice{
		Code:    "migration-approval-0123456789abcdef0123456789abcdef",
		Request: migration.PriceRequest{ServerType: "cx22", Locations: []string{"fsn1"}},
		Estimate: migration.PriceEstimate{
			Status:            "quoted",
			ServerType:        "cx22",
			Currency:          "EUR",
			LowestHourlyGross: "0.10",
			Locations:         []migration.PriceLocation{{Location: "fsn1", HourlyGross: "0.10"}},
		},
	}
	if err := sendMigrationApprovalWith(context.Background(), paths, client, "https://telegram.synthetic", notice); err != nil {
		t.Fatal(err)
	}
	if requestBody.ChatID != 42 || !strings.Contains(requestBody.Text, "Server type: cx22") || !strings.Contains(requestBody.Text, migration.ApprovalCommandPrefix+notice.Code) {
		t.Fatalf("stored-user approval request = %#v", requestBody)
	}
	if strings.Contains(requestBody.Text, "-100") || strings.Contains(requestBody.Text, "model") {
		t.Fatalf("approval delivery contained a group or model destination: %q", requestBody.Text)
	}
}

func TestMigrationApprovalPhraseReachesOnlyTrustedPrivateApprovalEndpoint(t *testing.T) {
	code := "migration-approval-0123456789abcdef0123456789abcdef"
	var approvalBody struct {
		Code    string `json:"code"`
		ChatID  int64  `json:"chat_id"`
		UserID  int64  `json:"user_id"`
		Private bool   `json:"private"`
	}
	var approvalCalls int
	var sent []string
	a := adapter{
		token:         testToken,
		pairing:       &pairing{userID: 42},
		controlSecret: "control-secret",
		apiBase:       "https://telegram.synthetic",
		client: doerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/bot"+testToken+"/sendMessage" {
				return nil, errors.New("unexpected Telegram method")
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				return nil, err
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/migration/approve" {
				return nil, errors.New("unexpected agent method")
			}
			approvalCalls++
			if err := json.NewDecoder(request.Body).Decode(&approvalBody); err != nil {
				return nil, err
			}
			return response(http.StatusOK, `{"status":"approved"}`), nil
		}),
	}
	group := textMessage(42, -10042, "group", migration.ApprovalCommandPrefix+code)
	a.handle(context.Background(), group)
	if approvalCalls != 0 || len(sent) != 0 {
		t.Fatalf("group approval reached trusted path: calls=%d sent=%#v", approvalCalls, sent)
	}
	a.handle(context.Background(), privateMessage(42, migration.ApprovalCommandPrefix+code))
	if approvalCalls != 1 || approvalBody.Code != code || approvalBody.ChatID != 42 || approvalBody.UserID != 42 || !approvalBody.Private {
		t.Fatalf("private approval request = %#v; calls=%d", approvalBody, approvalCalls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "exact terms shown above") {
		t.Fatalf("private approval response = %#v", sent)
	}
}
