package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"goaster/internal/localenv"
	"goaster/internal/ovh"
	"goaster/internal/telegram"
)

const fakeRef = "0123456789abcdef0123456789abcdef01234567"

type onboardingHTTP struct {
	posts     int
	publicKey string
	requests  []string
}

func (f *onboardingHTTP) Do(r *http.Request) (*http.Response, error) {
	var requestBody []byte
	if r.Body != nil {
		requestBody, _ = io.ReadAll(r.Body)
	}
	f.requests = append(f.requests, r.URL.String()+string(requestBody)+fmt.Sprint(r.Header))
	var body string
	switch r.Method + " " + r.URL.Path {
	case "GET /1.0/auth/time":
		body = fmt.Sprint(time.Now().Unix())
	case "GET /1.0/cloud/project":
		body = `["project"]`
	case "GET /1.0/cloud/project/project":
		body = `{"project_id":"project","status":"ok"}`
	case "GET /1.0/cloud/project/project/region":
		body = `["GRA11"]`
	case "GET /1.0/cloud/project/project/region/GRA11":
		body = `{"name":"GRA11","status":"UP"}`
	case "GET /1.0/cloud/project/project/image":
		body = `[{"id":"image","region":"GRA11","name":"Ubuntu 24.04","status":"active","user":"ubuntu"}]`
	case "GET /1.0/cloud/project/project/flavor":
		body = `[{"id":"flavor","region":"GRA11","name":"test-flavor","available":true,"quota":1,"ram":2,"disk":20}]`
	case "GET /1.0/cloud/project/project/sshkey":
		body = fmt.Sprintf(`[{"id":"key","name":"test-key","regions":["GRA11"],"publicKey":%q}]`, f.publicKey)
	case "POST /1.0/cloud/project/project/sshkey":
		body = `{"id":"key","name":"test-key","regions":["GRA11"]}`
	case "POST /1.0/cloud/project/project/instance":
		f.posts++
		body = `{"id":"server","status":"BUILD"}`
	case "GET /1.0/cloud/project/project/instance/server":
		body = `{"id":"server","status":"ACTIVE","ipAddresses":[{"ip":"203.0.113.7","type":"public"}]}`
	default:
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type telegramCall struct {
	target      ovh.Target
	token, hash string
}

type onboardingRunners struct {
	installs     int
	installError bool
	setupErrors  int
	telegram     []telegramCall
}

func (*onboardingRunners) Check(context.Context, ovh.Target) error { return nil }
func (r *onboardingRunners) Install(_ context.Context, target ovh.Target, ref string, bundle ovh.RuntimeBundle) (string, string, error) {
	r.installs++
	if !strings.Contains(string(bundle.Auth), "fake-access") || bundle.Values["OVH_APPLICATION_SECRET"] != "fake-app-secret" {
		return "", "", fmt.Errorf("incomplete runtime bundle")
	}
	if r.installError {
		return "", "", fmt.Errorf("fake install failure")
	}
	if target.IP != "203.0.113.7" {
		return "", "", fmt.Errorf("unexpected fake IP")
	}
	return ref, "active", nil
}

func (r *onboardingRunners) SetupTelegram(_ context.Context, target ovh.Target, token, hash string) error {
	r.telegram = append(r.telegram, telegramCall{target, token, hash})
	if len(r.telegram) <= r.setupErrors {
		return fmt.Errorf("fake failure including %s and https://example.invalid/bot%s", token, token)
	}
	return nil
}

func TestOVHLocalConfirmationAndReporting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	auth, _ := NewCredentialStore("")
	if err := auth.Save(Credential{AccessToken: "fake-access", RefreshToken: "fake-refresh", AccountID: "fake-account"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, answer string
		creates      bool
	}{{"refuse", "no", false}, {"approve", "CREATE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "key")
			pair, err := ovh.EnsureKeyPairAt(keyPath)
			if err != nil {
				t.Skipf("OpenSSH ssh-keygen is unavailable: %v", err)
			}
			store := localenv.NewStoreAt(filepath.Join(dir, "env"))
			values := map[string]string{"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "fake-app-key", "OVH_APPLICATION_SECRET": "fake-app-secret", "OVH_CONSUMER_KEY": "fake-consumer-secret"}
			if err := store.Save(values); err != nil {
				t.Fatal(err)
			}
			fake := &onboardingHTTP{publicKey: pair.PublicKey}
			runners := &onboardingRunners{}
			var output bytes.Buffer
			if err := runOVHSetupWithDependenciesAndOptions(context.Background(), bufio.NewScanner(strings.NewReader(tc.answer+"\n")), bufio.NewWriter(&output), store, fake, runners, runners, func() (ovh.KeyPair, error) { return pair, nil }, func() (string, error) { return fakeRef, nil }); err != nil {
				t.Fatal(err)
			}
			if (fake.posts > 0) != tc.creates || (runners.installs > 0) != tc.creates {
				t.Fatal("confirmation did not gate creation and installation")
			}
			if !strings.Contains(output.String(), "Type CREATE") {
				t.Fatal("missing explicit confirmation prompt")
			}
			if tc.creates {
				for _, want := range []string{"server_id=server", "public_ip=203.0.113.7", "commit=" + fakeRef, "service=active"} {
					if !strings.Contains(output.String(), want) {
						t.Errorf("missing %s", want)
					}
				}
			} else if !strings.Contains(output.String(), "not confirmed") {
				t.Error("refusal not reported")
			}
			for _, secret := range []string{"fake-app-key", "fake-app-secret", "fake-consumer-secret", "fake-key-content", keyPath} {
				if strings.Contains(output.String(), secret) {
					t.Error("local output exposed a fake credential")
				}
			}
		})
	}
}

func TestTelegramSetupAfterOVHConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	auth, _ := NewCredentialStore("")
	if err := auth.Save(Credential{AccessToken: "fake-access", RefreshToken: "fake-refresh", AccountID: "fake-account"}); err != nil {
		t.Fatal(err)
	}
	const token = "123456789:abcdefghijklmnopqrstuvwxyzABCDE_12345"
	dir := t.TempDir()
	pair, err := ovh.EnsureKeyPairAt(filepath.Join(dir, "key"))
	if err != nil {
		t.Skipf("OpenSSH ssh-keygen is unavailable: %v", err)
	}
	for _, tc := range []struct {
		name, input  string
		token        string
		installError bool
		setupErrors  int
		wantCalls    int
		wantMessage  string
		wantCode     bool
		importToken  bool
	}{
		{name: "creation refused", input: "no\n", token: token, wantMessage: "not confirmed"},
		{name: "agent failed", input: "CREATE\nSETUP TELEGRAM\n", token: token, installError: true, wantMessage: "install or verify agent service failed"},
		{name: "skipped", input: "CREATE\nskip\n", token: token, wantMessage: "Telegram setup skipped"},
		{name: "not exact confirmation", input: "CREATE\nsetup telegram\n", token: token, wantMessage: "Telegram setup skipped"},
		{name: "missing token", input: "CREATE\nSETUP TELEGRAM\nskip\n", wantMessage: "Telegram token is missing or invalid"},
		{name: "approved", input: "CREATE\nSETUP TELEGRAM\n", token: token, wantCalls: 1, wantCode: true, wantMessage: "Telegram service is active"},
		{name: "failed then retry", input: "CREATE\nSETUP TELEGRAM\nSETUP TELEGRAM\n", token: token, setupErrors: 1, wantCalls: 2, wantCode: true, wantMessage: "verification failed"},
		{name: "import during workflow", input: "CREATE\n/env\ny\nSETUP TELEGRAM\n", importToken: true, wantCalls: 1, wantCode: true, wantMessage: "Telegram service is active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
			values := map[string]string{"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "fake-app-key", "OVH_APPLICATION_SECRET": "fake-app-secret", "OVH_CONSUMER_KEY": "fake-consumer-secret"}
			if tc.token != "" {
				values["TELEGRAM_BOT_TOKEN"] = tc.token
			}
			if err := store.Save(values); err != nil {
				t.Fatal(err)
			}
			picker := EnvironmentFilePickerFunc(func() (string, error) {
				if !tc.importToken {
					t.Fatal("unexpected picker use")
				}
				path := filepath.Join(t.TempDir(), "input")
				data := "OVH_ENDPOINT=ovh-eu\nOVH_APPLICATION_KEY=fake-app-key\nOVH_APPLICATION_SECRET=fake-app-secret\nOVH_CONSUMER_KEY=fake-consumer-secret\nTELEGRAM_BOT_TOKEN=" + token + "\n"
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return path, nil
			})
			fake := &onboardingHTTP{publicKey: pair.PublicKey}
			runners := &onboardingRunners{installError: tc.installError, setupErrors: tc.setupErrors}
			var output bytes.Buffer
			err := runOVHSetupWithOptionsAndPicker(context.Background(), bufio.NewScanner(strings.NewReader(tc.input)), bufio.NewWriter(&output), store, fake, runners, runners, func() (ovh.KeyPair, error) { return pair, nil }, func() (string, error) { return fakeRef, nil }, picker)
			if err != nil {
				t.Fatal(err)
			}
			if len(runners.telegram) != tc.wantCalls || !strings.Contains(output.String(), tc.wantMessage) {
				t.Fatalf("setup calls = %d, output = %s", len(runners.telegram), output.String())
			}
			if tc.importToken && (strings.Contains(output.String(), "Next command: /ovh") || !strings.Contains(output.String(), "Back in Telegram setup")) {
				t.Fatal("environment import did not return to the current Telegram setup")
			}
			for _, call := range runners.telegram {
				if call.token != token || call.target.IP != "203.0.113.7" || call.target.User != "goaster" || call.target.PrivateKey != pair.PrivateKeyPath {
					t.Fatal("Telegram setup did not receive only the intended target and token")
				}
			}
			if len(runners.telegram) == 2 && runners.telegram[0].hash != runners.telegram[1].hash {
				t.Fatal("retry changed the undisclosed pairing code")
			}
			matches := regexp.MustCompile(`(?m)^  /start ([A-Za-z0-9_-]+)$`).FindStringSubmatch(output.String())
			if (len(matches) == 2) != tc.wantCode {
				t.Fatal("pairing code was not shown exclusively after verified startup")
			}
			if tc.wantCode {
				digest := sha256.Sum256([]byte(matches[1]))
				if len(matches[1]) != 32 || runners.telegram[len(runners.telegram)-1].hash != hex.EncodeToString(digest[:]) {
					t.Fatal("pairing code or verifier was invalid")
				}
			}
			for _, secret := range []string{token, "fake-app-key", "fake-app-secret", "fake-consumer-secret", pair.PrivateKeyPath, "https://example.invalid/bot"} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("workflow output exposed a fake secret or URL")
				}
			}
			for _, request := range fake.requests {
				if strings.Contains(request, token) || strings.Contains(request, pair.PrivateKeyPath) {
					t.Fatal("Telegram token or private key path was sent to OVH")
				}
			}
		})
	}
}

func TestPairingCodeIsRandomAndHasAValidVerifier(t *testing.T) {
	code, hash, err := telegram.PairingCode()
	if err != nil || len(code) != 32 || len(hash) != 64 {
		t.Fatal("pairing code generation failed")
	}
	other, _, err := telegram.PairingCode()
	if err != nil || code == other {
		t.Fatal("pairing codes were not generated independently")
	}
}

func TestRuntimeCredentialPreflightAndCustomAuthFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Save(map[string]string{"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "fake-app-key", "OVH_APPLICATION_SECRET": "fake-app-secret", "OVH_CONSUMER_KEY": "fake-consumer-secret"}); err != nil {
		t.Fatal(err)
	}
	custom := CredentialStore{Path: filepath.Join(t.TempDir(), "custom-auth")}
	for _, present := range []bool{false, true} {
		if present {
			if err := custom.Save(Credential{AccessToken: "custom-access", RefreshToken: "custom-refresh", AccountID: "custom-account"}); err != nil {
				t.Fatal(err)
			}
		}
		fake := &onboardingHTTP{}
		var output bytes.Buffer
		keyCalls := 0
		err := runOVHSetupWithOptionsAndPicker(context.Background(), bufio.NewScanner(strings.NewReader("no\n")), bufio.NewWriter(&output), store, fake, &onboardingRunners{}, &onboardingRunners{}, func() (ovh.KeyPair, error) { keyCalls++; return ovh.KeyPair{}, fmt.Errorf("test stops before network") }, func() (string, error) { t.Fatal("unexpected commit lookup"); return "", nil }, nil, custom)
		if err != nil {
			t.Fatal(err)
		}
		if (keyCalls > 0) != present || len(fake.requests) != 0 {
			t.Fatal("credential preflight did not use custom auth file before provisioning")
		}
		if strings.Contains(output.String(), "custom-access") || strings.Contains(output.String(), "custom-refresh") {
			t.Fatal("credential exposed")
		}
	}
}
