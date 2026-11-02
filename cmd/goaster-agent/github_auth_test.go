package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/chat"
)

func TestGitHubAuthToolRequiresAuthorizationAndAllowlistedProvider(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
	})
	tool := newGitHubAuthTool(manager)
	ctx := chat.WithChatID(context.Background(), 42)

	result, err := tool.Handler(ctx, `{"provider":"github"}`)
	if err != nil || result != githubAuthAuthorizationNeeded {
		t.Fatalf("unauthorized tool result = %q, error = %v", result, err)
	}
	if requests != 0 {
		t.Fatalf("unauthorized tool made %d network requests", requests)
	}

	for _, arguments := range []string{
		`{"provider":"openai"}`,
		`{"provider":"shell"}`,
		`{"provider":"github","command":"gh auth login"}`,
		`{"provider":"github","chat_id":999}`,
		`{"provider":"github"} trailing`,
	} {
		result, err = tool.Handler(ctx, arguments)
		if err != nil || result != githubAuthProviderNotAllowed {
			t.Errorf("arguments %s result = %q, error = %v", arguments, result, err)
		}
	}
}

func TestGitHubDeviceFlowUsesGhCompatibleOAuthScopeAndForm(t *testing.T) {
	const clientID = "public-client-id"
	var deviceForm url.Values
	var tokenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("GitHub request = %s %s content-type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse GitHub form: %v", err)
		}
		switch r.URL.Path {
		case githubDevicePath:
			deviceForm = r.PostForm
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			tokenForm = r.PostForm
			_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := *server.Client()
	startedAt := time.Unix(100, 0)
	device, err := requestGitHubDeviceCode(context.Background(), client, server.URL, clientID, startedAt, time.Minute)
	if err != nil {
		t.Fatalf("request device code: %v", err)
	}
	if device.ExpiresAt != startedAt.Add(time.Minute) {
		t.Fatalf("device expiry = %v, want capped expiry %v", device.ExpiresAt, startedAt.Add(time.Minute))
	}
	token, status := pollGitHubDeviceCode(context.Background(), client, server.URL, clientID, device, func() time.Time { return startedAt }, func(context.Context, time.Duration) error { return nil })
	if status != "completed" || token != "access-token" {
		t.Fatalf("device polling = token %q status %q", token, status)
	}
	if got := deviceForm.Get("client_id"); got != clientID {
		t.Fatalf("device client_id = %q, want %q", got, clientID)
	}
	if got := deviceForm.Get("scope"); got != "repo read:org gist" {
		t.Fatalf("device scope = %q, want gh-compatible scope", got)
	}
	if got := tokenForm.Get("client_id"); got != clientID {
		t.Fatalf("token client_id = %q, want %q", got, clientID)
	}
	if got := tokenForm.Get("device_code"); got != "device" {
		t.Fatalf("token device_code = %q, want device", got)
	}
	if got := tokenForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("token grant_type = %q", got)
	}
}

func TestGitHubAuthRedactsTokenAndLoginOutputFromModel(t *testing.T) {
	const (
		deviceSecret = "DEVICE-SECRET-MUST-NOT-LEAK"
		accessSecret = "ACCESS-TOKEN-MUST-NOT-LEAK"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"` + deviceSecret + `","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"` + accessSecret + `"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var installed string
	var notices []chat.AuthHandoffNotice
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		Sleep:       func(context.Context, time.Duration) error { return nil },
		InstallToken: func(_ context.Context, token string) error {
			installed = token
			return nil
		},
		VerifyAccount: func(context.Context) (string, error) {
			return "alice\n", nil
		},
	})
	if got := manager.authorize(context.Background(), 42); got != githubAuthAuthorizationGranted {
		t.Fatalf("authorization result = %q", got)
	}
	ctx := chat.WithChatID(context.Background(), 42)
	ctx = chat.WithAuthHandoffObserver(ctx, func(notice chat.AuthHandoffNotice) {
		if notice.UserCode != "" && strings.Contains(notice.UserCode, accessSecret) {
			t.Errorf("device notice contained access token: %+v", notice)
		}
		notices = append(notices, notice)
	})
	result, err := newGitHubAuthTool(manager).Handler(ctx, `{"provider":"github"}`)
	if err != nil || result != githubAuthCompleted {
		t.Fatalf("completed result = %q, error = %v", result, err)
	}
	for _, value := range []string{deviceSecret, accessSecret} {
		if strings.Contains(result, value) {
			t.Fatalf("model result exposed %q: %q", value, result)
		}
	}
	if installed != accessSecret {
		t.Fatalf("installed token = %q, want internal token", installed)
	}
	if len(notices) != 2 || notices[0].Status != "started" || notices[0].UserCode != "ABCD-EFGH" || notices[1].Status != "completed" || notices[1].VerifiedAccount != "alice" {
		t.Fatalf("handoff notices = %+v", notices)
	}
	for _, notice := range notices {
		if strings.Contains(notice.VerificationURL, accessSecret) || strings.Contains(notice.VerifiedAccount, accessSecret) {
			t.Fatalf("handoff notice exposed token: %+v", notice)
		}
	}
}

func TestGitHubAuthRequiresPostLoginAccountMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	rolledBack := false
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		Sleep:       func(context.Context, time.Duration) error { return nil },
		InstallToken: func(context.Context, string) error {
			installed = true
			return nil
		},
		RollbackCredential: func(_ context.Context, account string) error {
			if account != "alice" {
				t.Errorf("rollback account = %q, want token-verified account", account)
			}
			installed = false
			rolledBack = true
			return nil
		},
		VerifyAccount: func(context.Context) (string, error) {
			return "different-account", nil
		},
	})
	manager.authorize(context.Background(), 42)
	result := manager.start(context.Background(), 42, nil)
	if result != githubAuthFailed {
		t.Fatalf("account mismatch result = %q, want failure", result)
	}
	if installed || !rolledBack {
		t.Fatalf("mismatched post-install verification left credential active: installed=%v rolled_back=%v", installed, rolledBack)
	}
}

func TestGitHubAuthRollsBackOnPostLoginVerificationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	rolledBack := false
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		InstallToken: func(context.Context, string) error {
			installed = true
			return nil
		},
		RollbackCredential: func(context.Context, string) error {
			installed = false
			rolledBack = true
			return nil
		},
		VerifyAccount: func(context.Context) (string, error) {
			return "", errors.New("post-install verification failed")
		},
	})
	manager.authorize(context.Background(), 42)
	if result := manager.start(context.Background(), 42, nil); result != githubAuthFailed {
		t.Fatalf("post-install verification result = %q, want failure", result)
	}
	if installed || !rolledBack {
		t.Fatalf("failed post-install verification left credential active: installed=%v rolled_back=%v", installed, rolledBack)
	}
}

func TestGitHubAuthDoesNotInstallAfterDisplayedExpiry(t *testing.T) {
	current := time.Unix(100, 0)
	installed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":1,"interval":1}`))
		case githubTokenPath:
			current = current.Add(time.Second)
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		Now:         func() time.Time { return current },
		InstallToken: func(context.Context, string) error {
			installed = true
			return nil
		},
	})
	if got := manager.authorize(context.Background(), 42); got != githubAuthAuthorizationGranted {
		t.Fatalf("authorization result = %q", got)
	}
	if got := manager.start(context.Background(), 42, nil); got != githubAuthExpired {
		t.Fatalf("expired device result = %q, want expiry", got)
	}
	if installed {
		t.Fatal("credential was installed after the displayed device expiry")
	}
}

func TestGitHubAuthCancellationBeforeInstallDoesNotInstall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	var manager githubAuthManager
	manager = newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		VerifyToken: func(context.Context, string) (string, error) {
			if got := manager.cancel(context.Background(), 42); got != githubAuthCancellationPending {
				t.Errorf("install-time cancellation result = %q, want pending", got)
			}
			return "alice", nil
		},
		InstallToken: func(context.Context, string) error {
			installed = true
			return nil
		},
	})
	manager.authorize(context.Background(), 42)
	if got := manager.start(context.Background(), 42, nil); got != githubAuthCanceled {
		t.Fatalf("canceled handoff result = %q, want cancellation", got)
	}
	if installed {
		t.Fatal("credential was installed after cancellation")
	}
}

func TestGitHubAuthCancellationDuringInstallRollsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	rolledBack := false
	var manager githubAuthManager
	manager = newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		VerifyToken: func(context.Context, string) (string, error) { return "alice", nil },
		InstallCredential: func(_ context.Context, _ string, _ string, beforeInstall func() error) (githubCredentialRollback, error) {
			if err := beforeInstall(); err != nil {
				return nil, err
			}
			installed = true
			if got := manager.cancel(context.Background(), 42); got != githubAuthCancellationPending {
				t.Errorf("install-time cancellation result = %q, want pending", got)
			}
			return func(context.Context, string) error {
				installed = false
				rolledBack = true
				return nil
			}, nil
		},
		VerifyAccount: func(context.Context) (string, error) { return "alice", nil },
	})
	manager.authorize(context.Background(), 42)
	if got := manager.start(context.Background(), 42, nil); got != githubAuthCanceled {
		t.Fatalf("canceled install result = %q, want cancellation", got)
	}
	if installed || !rolledBack {
		t.Fatalf("canceled install state = installed %v rolled_back %v", installed, rolledBack)
	}
}

func TestGitHubCredentialRollbackRemovesInactiveNewAccountBeforeRestoringSnapshot(t *testing.T) {
	accounts := map[string]bool{"previous": true, "new-account": true}
	active := "previous"
	var order []string
	logout := func(_ context.Context, account string) error {
		if account != "new-account" {
			return errors.New("rollback targeted the wrong account")
		}
		order = append(order, "logout:"+account)
		delete(accounts, account)
		return nil
	}
	install := func(_ context.Context, token string) error {
		if token != "previous-token" {
			return errors.New("rollback restored the wrong token")
		}
		order = append(order, "install:previous-token")
		active = "previous"
		return nil
	}
	verify := func(context.Context) (string, error) { return active, nil }
	err := restoreGitHubCredentialWithCallbacks(context.Background(), githubCredentialSnapshot{
		present: true,
		token:   "previous-token",
		account: "previous",
	}, "new-account", install, logout, verify)
	if err != nil {
		t.Fatalf("multi-account rollback: %v", err)
	}
	if accounts["new-account"] {
		t.Fatal("rollback left the newly-installed inactive account stored")
	}
	if got, want := strings.Join(order, ","), "logout:new-account,install:previous-token"; got != want {
		t.Fatalf("rollback order = %q, want %q", got, want)
	}
}

func TestGitHubCredentialRollbackFailsClosedWhenAccountRemovalIsUncertain(t *testing.T) {
	installCalls := 0
	err := restoreGitHubCredentialWithCallbacks(context.Background(), githubCredentialSnapshot{
		present: true,
		token:   "previous-token",
		account: "previous",
	}, "new-account", func(context.Context, string) error {
		installCalls++
		return nil
	}, func(context.Context, string) error {
		return errors.New("gh logout failed")
	}, func(context.Context) (string, error) { return "previous", nil })
	if !errors.Is(err, errGitHubAuthCleanup) {
		t.Fatalf("uncertain rollback error = %v, want cleanup failure", err)
	}
	if installCalls != 0 {
		t.Fatalf("restored prior snapshot after uncertain new-account removal: %d install calls", installCalls)
	}
}

func TestGitHubCredentialInstallRefusesPreExistingInactiveAccount(t *testing.T) {
	installCalls := 0
	logoutCalls := 0
	beforeInstallCalls := 0
	snapshot := func(context.Context) (githubCredentialSnapshot, error) {
		return githubCredentialSnapshot{accounts: []string{"active-account", "target-account"}}, nil
	}
	_, err := installGitHubCredentialWithCallbacks(
		context.Background(),
		"new-token",
		"target-account",
		func() error {
			beforeInstallCalls++
			return nil
		},
		snapshot,
		func(context.Context, string) error {
			installCalls++
			return nil
		},
		func(context.Context, string) error {
			logoutCalls++
			return nil
		},
		func(context.Context) (string, error) { return "target-account", nil },
	)
	if !errors.Is(err, errGitHubCredentialAlreadyPresent) {
		t.Fatalf("pre-existing inactive account error = %v, want refusal", err)
	}
	if installCalls != 0 || logoutCalls != 0 || beforeInstallCalls != 0 {
		t.Fatalf("pre-existing account was changed: install=%d logout=%d before_install=%d", installCalls, logoutCalls, beforeInstallCalls)
	}
}

func TestGitHubAccountStatusJSONListsActiveAndInactiveAccounts(t *testing.T) {
	accounts, err := parseGitHubAccountStatusJSON([]byte(`{"hosts":{"github.com":[{"login":"active-account"},{"login":"inactive-account"}],"github.example.com":[{"login":"other-host"}]}}`))
	if err != nil {
		t.Fatalf("parse GitHub account status: %v", err)
	}
	if got, want := strings.Join(accounts, ","), "active-account,inactive-account"; got != want {
		t.Fatalf("accounts = %q, want %q", got, want)
	}
}

func TestGitHubCredentialRollbackRefusesPreExistingTargetAccount(t *testing.T) {
	installCalls := 0
	logoutCalls := 0
	err := restoreGitHubCredentialWithCallbacks(
		context.Background(),
		githubCredentialSnapshot{accounts: []string{"target-account"}},
		"target-account",
		func(context.Context, string) error {
			installCalls++
			return nil
		},
		func(context.Context, string) error {
			logoutCalls++
			return nil
		},
		func(context.Context) (string, error) { return "target-account", nil },
	)
	if !errors.Is(err, errGitHubAuthCleanup) {
		t.Fatalf("pre-existing target rollback error = %v, want cleanup failure", err)
	}
	if installCalls != 0 || logoutCalls != 0 {
		t.Fatalf("rollback touched pre-existing target: install=%d logout=%d", installCalls, logoutCalls)
	}
}

func TestGitHubCredentialInspectionFailureIsNotMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh helper uses a POSIX shell")
	}
	directory := t.TempDir()
	pathName := filepath.Join(directory, "gh")
	if err := os.WriteFile(pathName, []byte("#!/bin/sh\nprintf 'credential inspection failed\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := readGitHubToken(context.Background()); err == nil || errors.Is(err, errGitHubCredentialMissing) {
		t.Fatalf("credential inspection failure = %v, want a non-missing error", err)
	}
}

func TestGitHubAuthCancellationAfterFinalVerificationRollsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	rolledBack := false
	cleanupFinished := false
	var notices []chat.AuthHandoffNotice
	var manager githubAuthManager
	manager = newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		VerifyToken: func(context.Context, string) (string, error) { return "alice", nil },
		InstallCredential: func(context.Context, string, string, func() error) (githubCredentialRollback, error) {
			installed = true
			return func(context.Context, string) error {
				installed = false
				rolledBack = true
				cleanupFinished = true
				return nil
			}, nil
		},
		VerifyAccount: func(context.Context) (string, error) {
			if got := manager.cancel(context.Background(), 42); got != githubAuthCancellationPending {
				t.Errorf("verification-time cancellation result = %q, want pending", got)
			}
			return "alice", nil
		},
	})
	manager.authorize(context.Background(), 42)
	if got := manager.start(context.Background(), 42, func(notice chat.AuthHandoffNotice) {
		if notice.Status == "canceled" && !cleanupFinished {
			t.Error("canceled event was emitted before cleanup completed")
		}
		notices = append(notices, notice)
	}); got != githubAuthCanceled {
		t.Fatalf("final-verification cancellation result = %q, want cancellation", got)
	}
	if installed || !rolledBack {
		t.Fatalf("final-verification cancellation state = installed %v rolled_back %v", installed, rolledBack)
	}
	if len(notices) != 3 || notices[1].Status != "cancellation_requested" || notices[2].Status != "canceled" {
		t.Fatalf("final-verification cancellation notices = %+v", notices)
	}
}

func TestGitHubAuthCleanupFailureBlocksNewLoginUntilReconciled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var notices []chat.AuthHandoffNotice
	var manager githubAuthManager
	manager = newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		VerifyToken: func(context.Context, string) (string, error) { return "alice", nil },
		InstallCredential: func(context.Context, string, string, func() error) (githubCredentialRollback, error) {
			return func(context.Context, string) error { return errors.New("cleanup unavailable") }, nil
		},
		VerifyAccount: func(context.Context) (string, error) {
			if got := manager.cancel(context.Background(), 42); got != githubAuthCancellationPending {
				return "", errors.New("cancellation was not pending")
			}
			return "alice", nil
		},
	})
	if got := manager.authorize(context.Background(), 42); got != githubAuthAuthorizationGranted {
		t.Fatalf("authorization result = %q", got)
	}
	result := manager.start(context.Background(), 42, func(notice chat.AuthHandoffNotice) {
		notices = append(notices, notice)
	})
	if result != githubAuthReconciliationNeeded {
		t.Fatalf("cleanup failure result = %q, want reconciliation", result)
	}
	if len(notices) != 3 || notices[0].Status != "started" || notices[1].Status != "cancellation_requested" || notices[2].Status != "reconciliation_required" {
		t.Fatalf("cleanup failure notices = %+v", notices)
	}
	if got := manager.authorize(context.Background(), 42); got != githubAuthBlocked {
		t.Fatalf("authorization while blocked = %q, want blocked", got)
	}
	if got := manager.start(context.Background(), 42, nil); got != githubAuthBlocked {
		t.Fatalf("start while blocked = %q, want blocked", got)
	}
	if got := manager.reconcile(context.Background(), 42); got != githubAuthReconciled {
		t.Fatalf("manual reconciliation result = %q", got)
	}
	if got := manager.authorize(context.Background(), 42); got != githubAuthAuthorizationGranted {
		t.Fatalf("authorization after reconciliation = %q", got)
	}
}

func TestGitHubAuthCancelAfterCompletionDoesNotReportCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case githubDevicePath:
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
		case githubTokenPath:
			_, _ = w.Write([]byte(`{"access_token":"token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	installed := false
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		VerifyToken: func(context.Context, string) (string, error) { return "alice", nil },
		InstallCredential: func(context.Context, string, string, func() error) (githubCredentialRollback, error) {
			installed = true
			return func(context.Context, string) error {
				installed = false
				return nil
			}, nil
		},
		VerifyAccount: func(context.Context) (string, error) { return "alice", nil },
	})
	manager.authorize(context.Background(), 42)
	if got := manager.start(context.Background(), 42, nil); got != githubAuthCompleted {
		t.Fatalf("completed result = %q", got)
	}
	if got := manager.cancel(context.Background(), 42); got != githubAuthNoPending {
		t.Fatalf("post-completion cancel result = %q, want no pending handoff", got)
	}
	if !installed {
		t.Fatal("post-completion cancellation removed the completed credential")
	}
}

func TestGitHubAuthExpiresAuthorizationAndDeviceCode(t *testing.T) {
	current := time.Unix(100, 0)
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:       "public-client-id",
		ConsentTimeout: time.Second,
		Now:            func() time.Time { return current },
		HTTPClient:     http.Client{},
	})
	if got := manager.authorize(context.Background(), 42); got != githubAuthAuthorizationGranted {
		t.Fatalf("authorization result = %q", got)
	}
	current = current.Add(2 * time.Second)
	if got := manager.start(context.Background(), 42, nil); got != githubAuthExpired {
		t.Fatalf("expired authorization result = %q", got)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == githubDevicePath {
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":1,"interval":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer server.Close()
	current = time.Unix(200, 0)
	manager = newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		Now:         func() time.Time { return current },
		Sleep: func(_ context.Context, delay time.Duration) error {
			current = current.Add(delay)
			return nil
		},
		InstallToken: func(context.Context, string) error { return nil },
	})
	manager.authorize(context.Background(), 42)
	if got := manager.start(context.Background(), 42, nil); got != githubAuthExpired {
		t.Fatalf("expired device result = %q", got)
	}
}

func TestGitHubAuthCancellationStopsPolling(t *testing.T) {
	polling := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == githubDevicePath {
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"` + githubDeviceVerification + `","expires_in":900,"interval":1}`))
			return
		}
		once.Do(func() { close(polling) })
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer server.Close()
	manager := newGitHubAuthManager(githubAuthConfig{
		ClientID:    "public-client-id",
		AuthBaseURL: server.URL,
		APIBaseURL:  server.URL,
		HTTPClient:  *server.Client(),
		Sleep: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	manager.authorize(context.Background(), 42)
	result := make(chan string, 1)
	go func() { result <- manager.start(context.Background(), 42, nil) }()
	select {
	case <-polling:
	case <-time.After(5 * time.Second):
		t.Fatal("GitHub device polling did not start")
	}
	if got := manager.cancel(context.Background(), 42); got != githubAuthCancellationPending {
		t.Fatalf("cancellation request result = %q, want pending", got)
	}
	select {
	case got := <-result:
		if got != githubAuthCanceled {
			t.Fatalf("canceled result = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GitHub device polling did not stop after cancellation")
	}
}

func TestGitHubAuthNoticeJSONDoesNotContainRawCredential(t *testing.T) {
	notice := chat.AuthHandoffNotice{
		Provider:        githubAuthProvider,
		Status:          "started",
		VerificationURL: githubDeviceVerification,
		UserCode:        "ABCD-EFGH",
		ExpiresAt:       time.Unix(100, 0).UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "access-token") || strings.Contains(string(data), "device-secret") {
		t.Fatalf("notice unexpectedly contained a credential: %s", data)
	}
}

func TestGitHubCLIEnvironmentDoesNotUseTokenOverrides(t *testing.T) {
	const secret = "GH-ENV-TOKEN-MUST-STAY-OUT"
	t.Setenv("GH_TOKEN", secret)
	t.Setenv("GITHUB_TOKEN", secret)
	t.Setenv("GH_ENTERPRISE_TOKEN", secret)
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", secret)
	environment := strings.Join(githubCommandEnvironment(), "\n")
	if strings.Contains(environment, secret) {
		t.Fatalf("GitHub CLI environment retained a token override")
	}
}
