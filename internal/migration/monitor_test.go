package migration

import (
	"context"
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
	"goaster/internal/scheduler"
)

type migrationHTTPFixture struct {
	server         *httptest.Server
	options        Options
	request        Request
	now            time.Time
	token          string
	ownerSecret    string
	available      map[string]bool
	serverGross    map[string]string
	primaryGross   map[string]string
	currency       string
	registerStatus int
	malformedPrice bool
	lostCreate     bool
	blockCreate    bool
	createStarted  chan struct{}
	releaseCreate  chan struct{}
	createOnce     sync.Once
	deliveryFailed atomic.Bool
	registerCount  atomic.Int32
	statusCount    atomic.Int32
	providerCount  atomic.Int32
	createCount    atomic.Int32
	deliveryCount  atomic.Int32
	disableCount   atomic.Int32
	mu             sync.Mutex
	publicKey      string
	userData       string
	delivered      [][]byte
}

func newMigrationHTTPFixture(t *testing.T) *migrationHTTPFixture {
	t.Helper()
	fixture := &migrationHTTPFixture{
		now:           time.Unix(1_800_000_000, 0).UTC(),
		token:         "synthetic-hcloud-token",
		available:     map[string]bool{"fsn1": true, "nbg1": true, "hel1": true},
		serverGross:   map[string]string{"fsn1": "0.09", "nbg1": "0.08", "hel1": "0.12"},
		primaryGross:  map[string]string{"fsn1": "0.01", "nbg1": "0.01", "hel1": "0.01"},
		currency:      "EUR",
		createStarted: make(chan struct{}),
		releaseCreate: make(chan struct{}),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	runtime := filepath.Join(t.TempDir(), "migration-runtime")
	fixture.request = Request{
		Action:                "monitor",
		JobID:                 "synthetic-migration",
		ApprovedBaselineGross: "0.09",
		ApprovedCurrency:      "EUR",
		Approved:              true,
		Target: Target{
			ServerTypeID:   22,
			ServerTypeName: "cx22",
			Architecture:   "x86",
			ImageID:        123,
			ImageName:      "ubuntu-24.04",
			EnableIPv4:     true,
		},
	}
	fixture.options = Options{
		Paths: Paths{
			State:   filepath.Join(runtime, "migration-state"),
			Lock:    filepath.Join(runtime, "migration-state.lock"),
			Handoff: filepath.Join(runtime, "migration-handoff"),
		},
		ProviderEndpoint:  fixture.server.URL + "/v1",
		WebEndpoint:       fixture.server.URL,
		HTTPClient:        &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: noRedirect},
		Now:               func() time.Time { return fixture.now },
		AllowInsecureHTTP: true,
		LoadProviderToken: func() (string, error) { return fixture.token, nil },
		DeliverHandoff: func(_ context.Context, data []byte) error {
			fixture.deliveryCount.Add(1)
			fixture.mu.Lock()
			fixture.delivered = append(fixture.delivered, append([]byte(nil), data...))
			fixture.mu.Unlock()
			if fixture.deliveryFailed.Load() {
				return errors.New("synthetic delivery failure")
			}
			return nil
		},
		DisableMonitor: func() error {
			fixture.disableCount.Add(1)
			return nil
		},
	}
	return fixture
}

func (fixture *migrationHTTPFixture) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/server_types":
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+fixture.token {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		locations := make([]any, 0, 3)
		for id, name := range map[int64]string{1: "fsn1", 2: "nbg1", 3: "hel1"} {
			locations = append(locations, map[string]any{"id": id, "name": name, "available": fixture.available[name]})
		}
		prices := make([]any, 0, len(fixture.serverGross))
		for _, name := range []string{"fsn1", "nbg1", "hel1"} {
			prices = append(prices, map[string]any{"location": name, "price_hourly": map[string]string{"gross": fixture.serverGross[name]}})
		}
		writeFixtureJSON(w, map[string]any{
			"server_types": []any{map[string]any{"id": 22, "name": "cx22", "architecture": "x86", "deprecated": false, "locations": locations, "prices": prices}},
			"meta":         map[string]any{"pagination": map[string]any{"page": 1, "last_page": 1}},
		})
	case "/v1/images/123":
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+fixture.token {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		writeFixtureJSON(w, map[string]any{"image": map[string]any{
			"id": 123, "name": "ubuntu-24.04", "os_flavor": "ubuntu", "os_version": "24.04", "status": "available", "type": "system", "architecture": "x86", "deprecated": nil, "deleted": nil,
		}})
	case "/v1/pricing":
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+fixture.token {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		prices := make([]any, 0, len(fixture.primaryGross)+1)
		for _, name := range []string{"fsn1", "nbg1", "hel1"} {
			prices = append(prices, map[string]any{"location": name, "price_hourly": map[string]string{"gross": fixture.primaryGross[name]}})
		}
		if fixture.malformedPrice {
			prices = append(prices, map[string]any{"location": "fsn1", "price_hourly": map[string]string{"gross": fixture.primaryGross["fsn1"]}})
		}
		writeFixtureJSON(w, map[string]any{"pricing": map[string]any{
			"currency":    fixture.currency,
			"primary_ips": []any{map[string]any{"type": "ipv4", "prices": prices}},
		}})
	case "/migration/register":
		if r.Method != http.MethodPost || fixture.registerStatus != 0 {
			writeFixtureJSONStatus(w, fixture.registerStatusOr(http.StatusServiceUnavailable), map[string]any{"error": "registration unavailable"})
			return
		}
		var input struct {
			LoginPublicKey string `json:"login_public_key"`
		}
		if decodeFixtureBody(r, &input) != nil || input.LoginPublicKey == "" || strings.Contains(input.LoginPublicKey, "PRIVATE KEY") {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		fixture.publicKey = input.LoginPublicKey
		fixture.registerCount.Add(1)
		attempt := strings.Repeat("a", 64)
		callback := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
		fixture.ownerSecret = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{
			"attempt": attempt, "callback_secret": callback, "owner_secret": fixture.ownerSecret,
			"server_label": "goaster-migration-" + attempt[:16],
			"owner_url":    MigrationWebOrigin + "/migration#attempt=" + attempt + "&owner=" + fixture.ownerSecret,
			"expires":      fixture.now.Add(7 * 24 * time.Hour).Unix(),
		})
	case "/migration/status":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") == "" {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		fixture.statusCount.Add(1)
		writeFixtureJSON(w, map[string]any{
			"state": "registered", "expires": fixture.now.Add(7 * 24 * time.Hour).Unix(),
			"server_label": "goaster-migration-" + strings.Repeat("a", 16), "server_id": "", "ip": "", "key_received": false,
			"login_public_key": fixture.publicKey, "sealed": false,
		})
	case "/v1/ssh_keys":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixture.token {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		var input struct {
			Name      string            `json:"name"`
			PublicKey string            `json:"public_key"`
			Labels    map[string]string `json:"labels"`
		}
		if decodeFixtureBody(r, &input) != nil || input.PublicKey != fixture.publicKey || input.Labels["goaster-migration-attempt"] != strings.Repeat("a", 64) {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		fixture.providerCount.Add(1)
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{"ssh_key": map[string]any{
			"id": 77, "name": input.Name, "public_key": input.PublicKey, "labels": input.Labels,
		}})
	case "/v1/servers":
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixture.token {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		var input struct {
			UserData         string `json:"user_data"`
			Location         string `json:"location"`
			StartAfterCreate bool   `json:"start_after_create"`
		}
		if json.Unmarshal(body, &input) != nil || input.Location == "" || !input.StartAfterCreate || strings.Contains(input.UserData, fixture.token) || strings.Contains(input.UserData, fixture.ownerSecret) {
			writeFixtureJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		fixture.mu.Lock()
		fixture.userData = input.UserData
		fixture.mu.Unlock()
		fixture.createCount.Add(1)
		fixture.createOnce.Do(func() { close(fixture.createStarted) })
		if fixture.blockCreate {
			select {
			case <-fixture.releaseCreate:
			case <-r.Context().Done():
				return
			}
		}
		if fixture.lostCreate {
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, hijackErr := hijacker.Hijack()
				if hijackErr == nil {
					_ = connection.Close()
				}
			}
			return
		}
		writeFixtureJSONStatus(w, http.StatusCreated, map[string]any{"server": map[string]any{"id": 9001, "public_net": map[string]any{"ipv4": map[string]string{"ip": "198.51.100.10"}}}})
	default:
		http.NotFound(w, r)
	}
}

func (fixture *migrationHTTPFixture) registerStatusOr(defaultStatus int) int {
	if fixture.registerStatus != 0 {
		return fixture.registerStatus
	}
	return defaultStatus
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
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing body")
	}
	return nil
}

func TestQuoteSelectsCheapestAllLocationsAndHonorsAllowlist(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	quoted, err := Quote(t.Context(), QuoteRequest{JobID: fixture.request.JobID, Target: fixture.request.Target}, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if quoted.Status != "quoted" || quoted.Currency != "EUR" || quoted.BaselineTotalGross != "0.09" {
		t.Fatalf("quote = %#v", quoted)
	}
	if len(quoted.Offers) != 3 || quoted.Offers[0].Location != "nbg1" {
		t.Fatalf("all-location offers = %#v", quoted.Offers)
	}
	fixture.request.Target.LocationAllowlist = []string{"fsn1"}
	allowlisted, err := Quote(t.Context(), QuoteRequest{JobID: fixture.request.JobID, Target: fixture.request.Target}, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowlisted.Offers) != 1 || allowlisted.Offers[0].Location != "fsn1" || allowlisted.BaselineTotalGross != "0.1" {
		t.Fatalf("allowlisted quote = %#v", allowlisted)
	}
}

func TestExecuteUsesExactBoundaryPrimaryIPPriceAndFixedOrigin(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.request.ApprovedBaselineGross = "0.10"
	fixture.request.Target.LocationAllowlist = []string{"fsn1"}
	fixture.primaryGross["fsn1"] = "0.02"
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusCreated || result.SelectedLocation != "fsn1" || result.ServerID != 9001 {
		t.Fatalf("boundary execution = %#v, %v", result, err)
	}
	if fixture.disableCount.Load() != 1 || fixture.createCount.Load() != 1 {
		t.Fatalf("disable/create count = %d/%d", fixture.disableCount.Load(), fixture.createCount.Load())
	}
	fixture.mu.Lock()
	userData := fixture.userData
	handoff := string(fixture.delivered[0])
	fixture.mu.Unlock()
	if !strings.Contains(userData, MigrationWebOrigin+"/migration/host-key") || strings.Contains(userData, fixture.server.URL) || strings.Contains(userData, fixture.token) {
		t.Fatalf("callback user-data used an unsafe origin or credential: %q", userData)
	}
	if !strings.Contains(handoff, MigrationWebOrigin+"/migration#attempt=") || !strings.Contains(handoff, "Server ID: 9001") || !strings.Contains(handoff, "BEGIN PRIVATE KEY") {
		t.Fatalf("handoff = %q", handoff)
	}
}

func TestExecuteDoesNotCreateAgainAfterLostResponse(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.lostCreate = true
	first, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || first.Status != statusTerminal {
		t.Fatalf("lost response result = %#v, %v", first, err)
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != statusTerminal || fixture.createCount.Load() != 1 {
		t.Fatalf("lost response replay = %#v, %v; creates=%d", second, err, fixture.createCount.Load())
	}
}

func TestExecuteTreatsInterruptedRegisteredAndKeyRegisteredAsTerminal(t *testing.T) {
	for _, status := range []string{statusRegistered, statusKeyRegistered} {
		t.Run(status, func(t *testing.T) {
			fixture := newMigrationHTTPFixture(t)
			if err := ensureRuntimeParents(fixture.options.Paths); err != nil {
				t.Fatal(err)
			}
			attempt := strings.Repeat("a", 64)
			state := migrationState{
				Version: stateVersion, JobID: fixture.request.JobID, Fingerprint: requestFingerprint(fixture.request), Target: fixture.request.Target,
				ApprovedBaselineGross: fixture.request.ApprovedBaselineGross, ApprovedCurrency: fixture.request.ApprovedCurrency,
				Status: status, Attempt: attempt, CallbackSecret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32))),
				OwnerSecret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("d", 32))), ServerLabel: "goaster-migration-" + attempt[:16],
				Expires: fixture.now.Add(time.Hour).Unix(), PublicKey: "ssh-ed25519 synthetic", PrivateKey: "synthetic-private-key", ProviderKeyID: 77,
			}
			if err := saveState(fixture.options.Paths.State, state); err != nil {
				t.Fatal(err)
			}
			result, err := Execute(t.Context(), fixture.request, fixture.options)
			if err != nil || result.Status != statusTerminal || fixture.providerCount.Load() != 0 || fixture.createCount.Load() != 0 {
				t.Fatalf("interrupted %s = %#v, %v; provider=%d create=%d", status, result, err, fixture.providerCount.Load(), fixture.createCount.Load())
			}
		})
	}
}

func TestConcurrentExecuteHasOneCreateClaim(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.blockCreate = true
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
		t.Fatal("first execution did not reach create")
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != "locked" {
		t.Fatalf("concurrent execution = %#v, %v", second, err)
	}
	close(fixture.releaseCreate)
	if err := <-firstError; err != nil {
		t.Fatal(err)
	}
	if result := <-firstResult; result.Status != statusCreated || fixture.createCount.Load() != 1 {
		t.Fatalf("first result = %#v; creates=%d", result, fixture.createCount.Load())
	}
}

func TestMalformedAndWrongCurrencyResponsesDoNotCreate(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.malformedPrice = true
	if _, err := Quote(t.Context(), QuoteRequest{JobID: fixture.request.JobID, Target: fixture.request.Target}, fixture.options); err == nil {
		t.Fatal("malformed pricing response was accepted")
	}

	fixture = newMigrationHTTPFixture(t)
	fixture.currency = "USD"
	result, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || result.Status != statusTerminal || fixture.createCount.Load() != 0 {
		t.Fatalf("wrong currency result = %#v, %v; creates=%d", result, err, fixture.createCount.Load())
	}
}

func TestDeliveryFailureIsPendingAndOnlyManualRepairResends(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.deliveryFailed.Store(true)
	first, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || first.Status != statusDeliveryPending || !first.DeliveryPending || fixture.deliveryCount.Load() != 1 {
		t.Fatalf("pending result = %#v, %v; sends=%d", first, err, fixture.deliveryCount.Load())
	}
	second, err := Execute(t.Context(), fixture.request, fixture.options)
	if err != nil || second.Status != statusDeliveryPending || fixture.deliveryCount.Load() != 1 {
		t.Fatalf("automatic resend = %#v, %v; sends=%d", second, err, fixture.deliveryCount.Load())
	}
	fixture.deliveryFailed.Store(false)
	repaired, err := RepairHandoff(t.Context(), fixture.request.JobID, fixture.options)
	if err != nil || repaired.Status != statusCreated || fixture.deliveryCount.Load() != 2 {
		t.Fatalf("manual repair = %#v, %v; sends=%d", repaired, err, fixture.deliveryCount.Load())
	}
	if _, err := os.Stat(fixture.options.Paths.Handoff); !os.IsNotExist(err) {
		t.Fatalf("successful repair left handoff: %v", err)
	}
}

func TestDefaultMigrationHTTPClientDoesNotProxyOrFollowRedirects(t *testing.T) {
	options := DefaultOptions()
	client, ok := options.HTTPClient.(*http.Client)
	if !ok || client == nil {
		t.Fatalf("default HTTP client = %T", options.HTTPClient)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("default migration client permits proxy configuration")
	}
	request, err := http.NewRequest(http.MethodGet, MigrationWebOrigin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse) {
		t.Fatal("default migration client follows redirects")
	}
}

func TestUnavailableMonitorStaysEnabledAndTerminalMonitorDisablesAcrossRestart(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.available["fsn1"] = false
	fixture.available["nbg1"] = false
	fixture.available["hel1"] = false
	jobPath := filepath.Join(t.TempDir(), "migration-jobs.json")
	jobs, err := scheduler.New(scheduler.Options{Path: jobPath})
	if err != nil {
		t.Fatal(err)
	}
	result, err := StartMonitor(fixture.request, fixture.options, jobs)
	if err != nil || result.Status != statusMonitoring {
		t.Fatalf("start monitor = %#v, %v", result, err)
	}
	job, ok := jobs.Get(schedulerJobID(fixture.request.JobID))
	if !ok || !job.Enabled || job.EverySeconds != MonitorIntervalSeconds || job.Message != schedulerMessage(fixture.request.JobID) {
		t.Fatalf("saved monitor = %#v, exists=%v", job, ok)
	}
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, fixture.options); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.Get(job.ID)
	if !job.Enabled {
		t.Fatal("read-only unavailable monitor was disabled")
	}

	fixture.available["fsn1"] = true
	fixture.available["nbg1"] = true
	fixture.available["hel1"] = true
	fixture.registerStatus = http.StatusServiceUnavailable
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, fixture.options); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.Get(job.ID)
	if job.Enabled || job.Status != scheduler.StatusDisabled {
		t.Fatalf("terminal monitor remained active: %#v", job)
	}

	restarted, err := scheduler.New(scheduler.Options{Path: jobPath})
	if err != nil {
		t.Fatal(err)
	}
	restartedJob, ok := restarted.Get(job.ID)
	if !ok || restartedJob.Enabled || restartedJob.Status != scheduler.StatusDisabled {
		t.Fatalf("disabled monitor after restart = %#v, exists=%v", restartedJob, ok)
	}
}

func TestProtectedHandoffEnvelopeIsBoundedAndIdentityChecked(t *testing.T) {
	fixture := newMigrationHTTPFixture(t)
	fixture.deliveryFailed.Store(true)
	if _, err := Execute(t.Context(), fixture.request, fixture.options); err != nil {
		t.Fatal(err)
	}
	content, err := protectedfile.Read(fixture.options.Paths.Handoff, handoffMaxBytes)
	if err != nil || len(content.Data) == 0 {
		t.Fatalf("pending handoff = %v, bytes=%d", err, len(content.Data))
	}
	if content.Identity == (protectedfile.Identity{}) {
		t.Fatal("pending handoff has no protected identity")
	}
}
