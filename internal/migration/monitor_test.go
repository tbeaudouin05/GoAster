package migration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/protectedfile"
)

type migrationFixture struct {
	server           *httptest.Server
	options          Options
	request          Request
	now              time.Time
	available        bool
	price            string
	hangCreate       bool
	createStarted    chan struct{}
	releaseCreate    chan struct{}
	createOnce       sync.Once
	registerCount    atomic.Int32
	statusCount      atomic.Int32
	providerKeyCount atomic.Int32
	createCount      atomic.Int32
	publicKey        string
}

func newMigrationFixture(t *testing.T) *migrationFixture {
	t.Helper()
	fixture := &migrationFixture{
		now:           time.Now().UTC().Truncate(time.Second),
		available:     true,
		price:         "0.0100",
		createStarted: make(chan struct{}),
		releaseCreate: make(chan struct{}),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	paths := filepath.Join(t.TempDir(), "runtime")
	fixture.request = Request{
		Action:        "monitor",
		JobID:         "cron-migration-test",
		WebOrigin:     fixture.server.URL,
		ProviderToken: "provider-token-test",
		Target: Target{
			ServerTypeID:   22,
			ServerTypeName: "cx22",
			Architecture:   "x86",
			ImageID:        123,
			ImageName:      "ubuntu-24.04",
			Locations:      []Location{{ID: 1, Name: "fsn1", HourlyGross: fixture.price}},
			EnableIPv4:     true,
		},
	}
	fixture.options = Options{
		Paths: Paths{
			State:   filepath.Join(paths, "migration-state"),
			Lock:    filepath.Join(paths, "migration-state.lock"),
			Handoff: filepath.Join(paths, "migration-handoff"),
		},
		ProviderEndpoint:  fixture.server.URL + "/v1",
		HTTPClient:        &http.Client{Timeout: 2 * time.Second, CheckRedirect: noRedirect},
		Now:               func() time.Time { return fixture.now },
		AllowInsecureHTTP: true,
	}
	return fixture
}

func (fixture *migrationFixture) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/server_types/22":
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+fixture.request.ProviderToken {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeFixtureJSON(w, map[string]any{"server_type": map[string]any{
			"id": 22, "name": "cx22", "architecture": "x86", "deprecated": false,
			"deprecation": nil,
			"locations":   []any{map[string]any{"id": 1, "name": "fsn1", "available": fixture.available, "deprecation": nil}},
			"prices":      []any{map[string]any{"location": "fsn1", "price_hourly": map[string]string{"gross": fixture.price}}},
		}})
	case "/v1/images/123":
		if r.Method != http.MethodGet {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeFixtureJSON(w, map[string]any{"image": map[string]any{
			"id": 123, "name": "ubuntu-24.04", "status": "available", "type": "system", "architecture": "x86", "deprecated": nil, "deleted": nil,
		}})
	case "/migration/register":
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var input struct {
			LoginPublicKey string `json:"login_public_key"`
		}
		if decodeFixtureBody(r, &input) != nil || input.LoginPublicKey == "" || strings.Contains(input.LoginPublicKey, "private") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.publicKey = input.LoginPublicKey
		fixture.registerCount.Add(1)
		attempt := strings.Repeat("a", 64)
		callback := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
		owner := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{
			"attempt": attempt, "callback_secret": callback, "owner_secret": owner,
			"server_label": "goaster-migration-" + attempt[:16],
			"owner_url":    fixture.server.URL + "/migration#attempt=" + attempt + "&owner=" + owner,
			"expires":      fixture.now.Add(7 * 24 * time.Hour).Unix(),
		})
	case "/migration/status":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") == "" || r.Header.Get("X-Goaster-Attempt") != strings.Repeat("a", 64) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.statusCount.Add(1)
		writeFixtureJSON(w, map[string]any{
			"state": "registered", "expires": fixture.now.Add(7 * 24 * time.Hour).Unix(),
			"server_label": "goaster-migration-" + strings.Repeat("a", 16),
			"server_id":    "", "ip": "", "key_received": false,
			"login_public_key": fixture.publicKey, "sealed": false,
		})
	case "/v1/ssh_keys":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixture.request.ProviderToken {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var input struct {
			Name      string            `json:"name"`
			PublicKey string            `json:"public_key"`
			Labels    map[string]string `json:"labels"`
		}
		if decodeFixtureBody(r, &input) != nil || input.PublicKey != fixture.publicKey || input.Labels["goaster-migration-attempt"] != strings.Repeat("a", 64) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.providerKeyCount.Add(1)
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{"ssh_key": map[string]any{
			"id": 77, "name": input.Name, "public_key": input.PublicKey, "labels": input.Labels,
		}})
	case "/v1/servers":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixture.request.ProviderToken {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var input struct {
			Name             string            `json:"name"`
			ServerType       int64             `json:"server_type"`
			Image            int64             `json:"image"`
			Location         string            `json:"location"`
			UserData         string            `json:"user_data"`
			StartAfterCreate bool              `json:"start_after_create"`
			Labels           map[string]string `json:"labels"`
		}
		if json.Unmarshal(body, &input) != nil || input.Name != "goaster-migration-"+strings.Repeat("a", 16) || input.ServerType != 22 || input.Image != 123 || input.Location != "1" || !input.StartAfterCreate || input.Labels["goaster-migration-attempt"] != strings.Repeat("a", 64) || strings.Contains(input.UserData, "provider-token-test") || strings.Contains(input.UserData, "dddddddd") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.createCount.Add(1)
		fixture.createOnce.Do(func() { close(fixture.createStarted) })
		if fixture.hangCreate {
			<-r.Context().Done()
			return
		}
		if fixture.releaseCreate != nil {
			select {
			case <-fixture.releaseCreate:
			case <-r.Context().Done():
				return
			}
		}
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{"server": map[string]any{"id": 9001, "public_net": map[string]any{"ipv4": map[string]string{"ip": "198.51.100.10"}}}})
	default:
		http.NotFound(w, r)
	}
}

func writeFixtureJSON(w http.ResponseWriter, value any) {
	writeFixtureJSONStatus(w, http.StatusOK, value)
}

func writeFixtureJSONStatus(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeFixtureBody(r *http.Request, value any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing body")
	}
	return nil
}

func TestMigrationCreatesOnceAcrossRestart(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	first, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || first.Status != statusCreated || first.ServerID != 9001 || !first.StopCron {
		t.Fatalf("first Execute() = %#v, %v", first, err)
	}
	if fixture.registerCount.Load() != 1 || fixture.providerKeyCount.Load() != 1 || fixture.createCount.Load() != 1 {
		t.Fatalf("mutation counts register=%d key=%d create=%d", fixture.registerCount.Load(), fixture.providerKeyCount.Load(), fixture.createCount.Load())
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != statusCreated || second.ServerID != first.ServerID {
		t.Fatalf("restart Execute() = %#v, %v", second, err)
	}
	if fixture.registerCount.Load() != 1 || fixture.providerKeyCount.Load() != 1 || fixture.createCount.Load() != 1 {
		t.Fatalf("restart repeated mutation register=%d key=%d create=%d", fixture.registerCount.Load(), fixture.providerKeyCount.Load(), fixture.createCount.Load())
	}
	content, err := protectedfile.Read(fixture.options.Paths.Handoff, stateMaxBytes)
	if err != nil || !bytes.Contains(content.Data, []byte("#attempt=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&owner=")) || !bytes.Contains(content.Data, []byte("BEGIN PRIVATE KEY")) {
		t.Fatalf("protected handoff missing or malformed: %q, %v", content.Data, err)
	}
	stateInfo, err := os.Stat(fixture.options.Paths.State)
	if err != nil || stateInfo.Mode().Perm() != 0600 {
		t.Fatalf("state file mode = %v, %v", stateInfo, err)
	}
}

func TestMigrationCommandOutputContainsOnlySafeStatusAndIDs(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	input, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunCommand(t.Context(), bytes.NewReader(input), &output, fixture.options); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, secret := range []string{"provider-token-test", "ZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGQ", "BEGIN PRIVATE KEY"} {
		if strings.Contains(text, secret) {
			t.Fatalf("helper output exposed %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"status":"created"`) || !strings.Contains(text, `"server_id":9001`) {
		t.Fatalf("helper output omitted safe result fields: %s", text)
	}
}

func TestMigrationParallelInvocationsHaveOneCreateClaim(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	firstResult := make(chan Result, 1)
	firstError := make(chan error, 1)
	go func() {
		result, err := Execute(t.Context(), fixture.request, fixture.options)
		firstResult <- result
		firstError <- err
	}()
	select {
	case <-fixture.createStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first invocation did not reach the create call")
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != "locked" {
		t.Fatalf("parallel invocation = %#v, %v", second, err)
	}
	close(fixture.releaseCreate)
	if err := <-firstError; err != nil {
		t.Fatal(err)
	}
	if result := <-firstResult; result.Status != statusCreated {
		t.Fatalf("first parallel result = %#v", result)
	}
	if fixture.createCount.Load() != 1 {
		t.Fatalf("parallel create count = %d", fixture.createCount.Load())
	}
}

func TestMigrationWaitsWithoutGeneratingUntilTargetIsAvailable(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.available = false
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusMonitoring || result.StopCron {
		t.Fatalf("unavailable target result = %#v, %v", result, err)
	}
	if fixture.registerCount.Load() != 0 || fixture.providerKeyCount.Load() != 0 || fixture.createCount.Load() != 0 {
		t.Fatalf("unavailable target performed a mutation")
	}
	if _, err := os.Stat(fixture.options.Paths.Handoff); !os.IsNotExist(err) {
		t.Fatalf("unavailable target staged a handoff: %v", err)
	}
}

func TestMigrationTimeoutBecomesTerminalAndDoesNotRepeat(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.hangCreate = true
	fixture.options.HTTPClient = &http.Client{Timeout: 60 * time.Millisecond, CheckRedirect: noRedirect}
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusTerminal || !result.StopCron {
		t.Fatalf("timeout result = %#v, %v", result, err)
	}
	if fixture.createCount.Load() != 1 {
		t.Fatalf("timeout create count = %d", fixture.createCount.Load())
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != statusTerminal || fixture.createCount.Load() != 1 {
		t.Fatalf("terminal restart = %#v, %v; creates=%d", second, err, fixture.createCount.Load())
	}
}

func TestMigrationStaleHandoffStopsBeforeCreate(t *testing.T) {
	fixture := newMigrationFixture(t)
	if err := os.MkdirAll(filepath.Dir(fixture.options.Paths.Handoff), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.options.Paths.Handoff, []byte("stale handoff"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusTerminal || fixture.createCount.Load() != 0 {
		t.Fatalf("stale handoff result = %#v, %v; creates=%d", result, err, fixture.createCount.Load())
	}
	data, err := os.ReadFile(fixture.options.Paths.Handoff)
	if err != nil || string(data) != "stale handoff" {
		t.Fatalf("stale handoff was replaced: %q, %v", data, err)
	}
}

func TestMigrationExpiryCleansExactHandoffAndPreservesReplacement(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	if result, err := Execute(t.Context(), fixture.request, fixture.options); err != nil || result.Status != statusCreated {
		t.Fatalf("seed Execute() = %#v, %v", result, err)
	}
	state, exists, err := loadState(fixture.options.Paths.State)
	if err != nil || !exists {
		t.Fatalf("load seed state = %#v, %v, exists=%v", state, err, exists)
	}
	state.Expires = fixture.now.Add(-time.Second).Unix()
	if err := saveState(fixture.options.Paths.State, state); err != nil {
		t.Fatal(err)
	}
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusTerminal {
		t.Fatalf("expired Execute() = %#v, %v", result, err)
	}
	if _, err := os.Stat(fixture.options.Paths.Handoff); !os.IsNotExist(err) {
		t.Fatalf("expired handoff remains: %v", err)
	}

	fixture = newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	if _, err := Execute(t.Context(), fixture.request, fixture.options); err != nil {
		t.Fatal(err)
	}
	state, _, err = loadState(fixture.options.Paths.State)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := fixture.options.Paths.Handoff + ".old"
	if err := os.Rename(fixture.options.Paths.Handoff, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.options.Paths.Handoff, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	state.Expires = fixture.now.Add(-time.Second).Unix()
	if err := saveState(fixture.options.Paths.State, state); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(t.Context(), fixture.request, fixture.options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture.options.Paths.Handoff)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("expiry cleanup removed replacement: %q, %v", data, err)
	}
}

func TestMigrationCleanupActionUsesOnlyJobIdentity(t *testing.T) {
	fixture := newMigrationFixture(t)
	fixture.releaseCreate = make(chan struct{})
	close(fixture.releaseCreate)
	if _, err := Execute(t.Context(), fixture.request, fixture.options); err != nil {
		t.Fatal(err)
	}
	state, exists, err := loadState(fixture.options.Paths.State)
	if err != nil || !exists {
		t.Fatalf("load state = %#v, %v, exists=%v", state, err, exists)
	}
	state.Status = statusTerminal
	state.Expires = fixture.now.Add(-time.Second).Unix()
	if err := saveState(fixture.options.Paths.State, state); err != nil {
		t.Fatal(err)
	}
	cleanup := Request{Action: "cleanup", JobID: fixture.request.JobID}
	result, err := Execute(t.Context(), cleanup, fixture.options)
	if err != nil || result.Status != statusTerminal || !result.StopCron {
		t.Fatalf("cleanup result = %#v, %v", result, err)
	}
	if _, err := os.Stat(fixture.options.Paths.Handoff); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the expired handoff: %v", err)
	}
	if _, err := Execute(t.Context(), Request{Action: "cleanup", JobID: "other-job"}, fixture.options); err == nil {
		t.Fatal("cleanup accepted a different job identity")
	}
}
