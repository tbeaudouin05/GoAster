package migration

import (
	"context"
	"encoding/hex"
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

	"goaster/internal/scheduler"
)

type machineFixture struct {
	server              *httptest.Server
	now                 time.Time
	token               string
	available           atomic.Bool
	lostCreate          atomic.Bool
	availability        atomic.Int32
	creates             atomic.Int32
	ownerURL            string
	requestBody         []byte
	createBody          []byte
	availabilityStarted chan struct{}
	availabilityRelease chan struct{}
	availabilityOnce    sync.Once
}

func newMachineFixture(t *testing.T) *machineFixture {
	t.Helper()
	fixture := &machineFixture{now: time.Unix(1_800_000_000, 0).UTC(), token: "synthetic-hcloud-token"}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.handle(writer, request)
	}))
	fixture.ownerURL = fixture.server.URL + "/migration#attempt=" + strings.Repeat("a", 64)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *machineFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != "" {
		writeJSONStatus(writer, http.StatusBadRequest, map[string]string{"error": "origin"})
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writeJSONStatus(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "content type"})
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeJSONStatus(writer, http.StatusBadRequest, map[string]string{"error": "body"})
		return
	}
	switch request.URL.Path {
	case "/migration/v1/machines/availability":
		fixture.availability.Add(1)
		fixture.requestBody = append([]byte(nil), body...)
		if fixture.availabilityStarted != nil {
			fixture.availabilityOnce.Do(func() { close(fixture.availabilityStarted) })
			<-fixture.availabilityRelease
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(body, &value) != nil || value["server_type"] == nil || value["hetzner_token"] == nil {
			writeJSONStatus(writer, http.StatusBadRequest, map[string]string{"error": "request"})
			return
		}
		var token string
		if json.Unmarshal(value["hetzner_token"], &token) != nil || token != fixture.token {
			writeJSONStatus(writer, http.StatusUnauthorized, map[string]string{"error": "token"})
			return
		}
		locations := []string{}
		if fixture.available.Load() {
			locations = []string{"fsn1"}
		}
		writeJSONStatus(writer, http.StatusOK, map[string]any{"version": "v1", "server_type": "cx22", "locations": locations})
	case "/migration/v1/machines/create":
		fixture.creates.Add(1)
		fixture.createBody = append([]byte(nil), body...)
		if fixture.lostCreate.Load() {
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				return
			}
			connection, _, hijackErr := hijacker.Hijack()
			if hijackErr == nil {
				_ = connection.Close()
			}
			return
		}
		var value struct {
			ServerType        string   `json:"server_type"`
			Locations         []string `json:"locations"`
			LoginPublicKey    string   `json:"login_public_key"`
			OwnerSecretDigest string   `json:"owner_secret_digest"`
			HetznerToken      string   `json:"hetzner_token"`
		}
		if json.Unmarshal(body, &value) != nil || value.ServerType != "cx22" || len(value.Locations) != 1 || value.Locations[0] != "fsn1" || !strings.HasPrefix(value.LoginPublicKey, "ssh-ed25519 ") || value.HetznerToken != fixture.token || len(value.OwnerSecretDigest) != 64 || strings.ToLower(value.OwnerSecretDigest) != value.OwnerSecretDigest {
			writeJSONStatus(writer, http.StatusBadRequest, map[string]string{"error": "request"})
			return
		}
		if _, err := hex.DecodeString(value.OwnerSecretDigest); err != nil || strings.Contains(string(body), "PRIVATE KEY") {
			writeJSONStatus(writer, http.StatusBadRequest, map[string]string{"error": "secret boundary"})
			return
		}
		writeJSONStatus(writer, http.StatusCreated, map[string]any{
			"version": "v1", "state": "created", "attempt": strings.Repeat("a", 64),
			"server_label": "goaster-migration-" + strings.Repeat("a", 16), "server_id": 123,
			"ip": "198.51.100.10", "owner_url": fixture.ownerURL, "expires": fixture.now.Add(time.Hour).Unix(),
		})
	default:
		writeJSONStatus(writer, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (fixture *machineFixture) options(taskDirectory string) Options {
	return Options{
		WebOrigin:         fixture.server.URL,
		HTTPClient:        fixture.server.Client(),
		Now:               func() time.Time { return fixture.now },
		LoadHetznerToken:  func() (string, error) { return fixture.token, nil },
		TempDirectory:     taskDirectory,
		AllowInsecureHTTP: true,
	}
}

func writeJSONStatus(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func TestAvailabilityUsesOnlyTheV1ContractAndNoOriginHeader(t *testing.T) {
	fixture := newMachineFixture(t)
	result, err := Availability(t.Context(), PriceRequest{ServerType: "cx22", Locations: []string{"fsn1", "nbg1"}}, fixture.options(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "v1" || result.ServerType != "cx22" || result.LocationPolicy != LocationPolicyAllowlist || len(result.RequestedLocations) != 2 || result.RequestedLocations[0] != "fsn1" || result.RequestedLocations[1] != "nbg1" || len(result.Locations) != 0 || fixture.availability.Load() != 1 {
		t.Fatalf("availability result = %#v; calls = %d", result, fixture.availability.Load())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(fixture.requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 || body["server_type"] == nil || body["locations"] == nil || body["hetzner_token"] == nil {
		t.Fatalf("availability request fields = %#v", body)
	}
	if strings.Contains(string(fixture.requestBody), "Origin") {
		t.Fatal("availability request body contained a header name")
	}
}

func TestCreateKeepsPrivateMaterialOutOfTheRequestAndModelResult(t *testing.T) {
	fixture := newMachineFixture(t)
	taskDirectory := t.TempDir()
	if err := os.Chmod(taskDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	var delivered []byte
	options := fixture.options(taskDirectory)
	options.DeliverHandoff = func(_ context.Context, data []byte) error {
		delivered = append([]byte(nil), data...)
		return nil
	}
	result, err := Create(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "created" || !result.Delivered || result.ServerID != 123 || result.IP != "198.51.100.10" {
		t.Fatalf("create result = %#v", result)
	}
	if strings.Contains(string(fixture.createBody), "BEGIN PRIVATE KEY") || strings.Contains(string(fixture.createBody), "owner_url") {
		t.Fatalf("create request crossed the secret boundary: %q", fixture.createBody)
	}
	if !strings.Contains(string(delivered), "&owner=") || !strings.Contains(string(delivered), "BEGIN PRIVATE KEY") || !strings.Contains(string(delivered), "Server ID: 123") {
		t.Fatalf("delivered handoff = %q", delivered)
	}
	for _, path := range []string{filepath.Join(taskDirectory, handoffPrivateKeyName), filepath.Join(taskDirectory, handoffDocumentName)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private handoff file %q = %v, mode %v", path, err, info)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "owner_url") || strings.Contains(string(encoded), "BEGIN PRIVATE KEY") || strings.Contains(string(encoded), "&owner=") {
		t.Fatalf("model result exposed handoff material: %s", encoded)
	}
}

func TestCreateSupportsPreparedHandoffDelivery(t *testing.T) {
	fixture := newMachineFixture(t)
	taskDirectory := t.TempDir()
	if err := os.Chmod(taskDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	options := fixture.options(taskDirectory)
	options.PrepareHandoff = func() (func(context.Context, []byte) error, error) {
		prepared++
		return func(context.Context, []byte) error { return nil }, nil
	}
	result, err := Create(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if prepared != 1 || fixture.creates.Load() != 1 || !result.Delivered || result.Status != "created" {
		t.Fatalf("prepared handoff create = %#v; prepared=%d creates=%d", result, prepared, fixture.creates.Load())
	}
}

func TestCreateLostResponseIsOneRequestAndSchedulerDisablesMonitor(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.lostCreate.Store(true)
	jobPath := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := scheduler.New(scheduler.Options{Path: jobPath})
	if err != nil {
		t.Fatal(err)
	}
	options := fixture.options(t.TempDir())
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-lost-response"}
	result, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManualReview || result.Status != "manual_review" || fixture.creates.Load() != 1 {
		t.Fatalf("lost response result = %#v; creates = %d", result, fixture.creates.Load())
	}
	job, ok := jobs.Get(request.JobID)
	if !ok || job.Enabled {
		t.Fatalf("lost response monitor = %#v, exists = %v", job, ok)
	}
	if runs := jobs.RunDue(t.Context()); runs != 0 || fixture.creates.Load() != 1 {
		t.Fatalf("disabled monitor reran: runs=%d creates=%d", runs, fixture.creates.Load())
	}
	// Model-facing starts omit the ID and must converge on one durable slot.
	singletonJobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "singleton.json")})
	if err != nil {
		t.Fatal(err)
	}
	options.TempDirectory = t.TempDir()
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22"}, options, singletonJobs)
	if err != nil || !first.ManualReview {
		t.Fatalf("first singleton start: %#v %v", first, err)
	}
	second, err := StartMonitor(t.Context(), Request{ServerType: "cx22"}, options, singletonJobs)
	if err != nil || !second.ManualReview || first.MonitorID != second.MonitorID || fixture.creates.Load() != 2 {
		t.Fatalf("repeat: %#v %v creates=%d", second, err, fixture.creates.Load())
	}

}

func TestUnavailableMonitorRemainsEnabledUntilAvailability(t *testing.T) {
	fixture := newMachineFixture(t)
	jobPath := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := scheduler.New(scheduler.Options{Path: jobPath})
	if err != nil {
		t.Fatal(err)
	}
	options := fixture.options(t.TempDir())
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-unavailable"}
	result, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil || result.Status != "monitoring" {
		t.Fatalf("unavailable start = %#v, %v", result, err)
	}
	job, ok := jobs.Get(request.JobID)
	if !ok || !job.Enabled || fixture.creates.Load() != 0 {
		t.Fatalf("unavailable monitor = %#v, exists = %v; creates=%d", job, ok, fixture.creates.Load())
	}
	fixture.available.Store(true)
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); err != nil {
		t.Fatalf("scheduled execution: %v; cause: %v", err, errors.Unwrap(err))
	}
	job, _ = jobs.Get(request.JobID)
	if job.Enabled || fixture.creates.Load() != 1 {
		t.Fatalf("available monitor = %#v; creates=%d", job, fixture.creates.Load())
	}
}

func TestStopMonitorReportsRunningDuringAvailability(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.availabilityStarted = make(chan struct{})
	fixture.availabilityRelease = make(chan struct{})
	jobPath := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := scheduler.New(scheduler.Options{Path: jobPath})
	if err != nil {
		t.Fatal(err)
	}
	options := fixture.options(t.TempDir())
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-stop-race"}
	message, err := schedulerMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Add(scheduler.Job{ID: request.JobID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: message}); err != nil {
		t.Fatal(err)
	}

	executionDone := make(chan error, 1)
	go func() { executionDone <- ScheduledExecutor(t.Context(), message, jobs, options) }()
	select {
	case <-fixture.availabilityStarted:
	case <-time.After(time.Second):
		t.Fatal("availability check did not start")
	}
	stopped, err := StopMonitor(request.JobID, jobs)
	if !errors.Is(err, scheduler.ErrExecutionInProgress) {
		t.Fatalf("stop during availability = %#v, %v", stopped, err)
	}
	close(fixture.availabilityRelease)
	if err := <-executionDone; err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if fixture.creates.Load() != 1 {
		t.Fatalf("expected one create: %d", fixture.creates.Load())
	}
	job, ok := jobs.Get(request.JobID)
	if !ok || job.Enabled || job.Status == scheduler.StatusRunning {
		t.Fatalf("stopped monitor state = %#v, exists=%v", job, ok)
	}
}

func TestLegacyPriceApprovalFieldsRequireReviewBeforeExecution(t *testing.T) {
	for _, legacy := range []Request{
		{ApprovedHourlyGross: "0.10"},
		{ApprovedCurrency: "EUR"},
		{ApprovedHourlyGross: "0.10", ApprovedCurrency: "EUR"},
	} {
		legacy.ServerType = "cx22"
		legacy.JobID = "migration-legacy"
		message, err := schedulerMessage(legacy)
		if err != nil {
			t.Fatal(err)
		}
		jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.Add(scheduler.Job{ID: legacy.JobID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: message}); err != nil {
			t.Fatal(err)
		}
		called := false
		options := Options{LoadHetznerToken: func() (string, error) { called = true; return "", nil }, DeliverHandoff: func(context.Context, []byte) error { called = true; return nil }}
		if err := ScheduledExecutor(t.Context(), message, jobs, options); err == nil {
			t.Fatalf("legacy scheduled approval accepted: %#v", legacy)
		} else {
			if _, ok := err.(scheduler.ReviewRequiredError); !ok {
				t.Fatalf("legacy approval did not require review: %v", err)
			}
		}
		if _, err := StartMonitor(t.Context(), legacy, options, jobs); err == nil || called {
			t.Fatalf("legacy approval reached execution: err=%v called=%v", err, called)
		}
	}
}

func TestOriginQueryAndInvalidResponseAreRejected(t *testing.T) {
	fixture := newMachineFixture(t)
	options := fixture.options(t.TempDir())
	options.WebOrigin = fixture.server.URL + "?query=not-allowed"
	if _, err := Availability(t.Context(), PriceRequest{ServerType: "cx22"}, options); err == nil {
		t.Fatal("origin query was accepted")
	}
	fixture.server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeJSONStatus(writer, http.StatusOK, map[string]any{"version": "v1", "server_type": "other", "locations": []string{}})
	})
	if _, err := Availability(t.Context(), PriceRequest{ServerType: "cx22"}, fixture.options(t.TempDir())); err == nil {
		t.Fatal("wrong server type response was accepted")
	}
}

func TestInvalidTokenAndNoDeliveryStopBeforeCreate(t *testing.T) {
	fixture := newMachineFixture(t)
	options := fixture.options(t.TempDir())
	options.LoadHetznerToken = func() (string, error) { return "", errors.New("synthetic token failure") }
	if _, err := Create(t.Context(), Request{ServerType: "cx22"}, options); err == nil || fixture.creates.Load() != 0 {
		t.Fatalf("invalid token create = %v; creates=%d", err, fixture.creates.Load())
	}

	fixture = newMachineFixture(t)
	checked := false
	options = fixture.options(t.TempDir())
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	options.CheckHandoffDelivery = func() error {
		checked = true
		return errors.New("synthetic paired delivery unavailable")
	}
	result, err := Create(t.Context(), Request{ServerType: "cx22"}, options)
	if err == nil || !errors.Is(err, errManualReview) || !result.ManualReview || !checked || fixture.creates.Load() != 0 {
		t.Fatalf("handoff preflight = %#v, %v; checked=%v creates=%d", result, err, checked, fixture.creates.Load())
	}

	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	result, err = StartMonitor(t.Context(), Request{ServerType: "cx22", JobID: "migration-preflight"}, options, jobs)
	if err == nil || !errors.Is(err, errManualReview) || !result.ManualReview {
		t.Fatalf("monitor handoff preflight = %#v, %v", result, err)
	}
	if _, ok := jobs.Get("migration-preflight"); ok {
		t.Fatal("handoff preflight left a monitor behind")
	}
}

func TestCreateRejectsLegacyPriceApprovalBeforeProviderCreate(t *testing.T) {
	for _, request := range []Request{
		{ServerType: "cx22", ApprovedHourlyGross: "0.10"},
		{ServerType: "cx22", ApprovedCurrency: "EUR"},
	} {
		fixture := newMachineFixture(t)
		options := fixture.options(t.TempDir())
		options.DeliverHandoff = func(context.Context, []byte) error { return nil }
		if _, err := Create(t.Context(), request, options); !errors.Is(err, errLegacyPriceApproval) {
			t.Fatalf("legacy create request = %v; want rejection", err)
		}
		if fixture.creates.Load() != 0 {
			t.Fatalf("legacy create request reached provider: %d", fixture.creates.Load())
		}
	}
}

func TestAvailabilityDefaultsToAnyEligibleLocation(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	result, err := Availability(t.Context(), PriceRequest{ServerType: "cx22"}, fixture.options(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Locations == nil || len(result.Locations) != 1 || result.Locations[0] != "fsn1" || result.LocationPolicy != LocationPolicyAnyEligible || len(result.RequestedLocations) != 0 {
		t.Fatalf("default availability result = %#v", result)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(fixture.requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["locations"]; present {
		t.Fatalf("default location policy was not omitted from request: %s", fixture.requestBody)
	}
}

func TestAvailabilityRejectsLocationOutsideStrictAllowlist(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSONStatus(writer, http.StatusOK, map[string]any{"version": "v1", "server_type": "cx22", "locations": []string{"nbg1"}})
	})
	if _, err := Availability(t.Context(), PriceRequest{ServerType: "cx22", Locations: []string{"fsn1"}}, fixture.options(t.TempDir())); err == nil {
		t.Fatal("availability widened a strict location allow-list")
	}
}

func TestAvailabilityDoesNotAcceptSourceOnlyPolicyFieldsFromWeb(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSONStatus(writer, http.StatusOK, map[string]any{
			"version": "v1", "server_type": "cx22", "locations": []string{},
			"location_policy": LocationPolicyAllowlist,
		})
	})
	if _, err := Availability(t.Context(), PriceRequest{ServerType: "cx22", Locations: []string{"fsn1"}}, fixture.options(t.TempDir())); err == nil {
		t.Fatal("availability accepted source-only policy fields from the Web response")
	}
}

func TestPriceEstimateUsesReadOnlyProviderPricingAndStrictLocations(t *testing.T) {
	var sawQuery atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/server_types":
			sawQuery.Store(request.URL.Query().Get("per_page") == "100" && request.URL.Query().Get("page") == "1")
			writeJSONStatus(writer, http.StatusOK, map[string]any{
				"server_types": []any{map[string]any{
					"name": "cx22", "deprecated": false,
					"locations": []any{
						map[string]any{"name": "fsn1", "available": true},
						map[string]any{"name": "nbg1", "available": true},
					},
					"prices": []any{
						map[string]any{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.09"}},
						map[string]any{"location": "nbg1", "price_hourly": map[string]string{"gross": "0.08"}},
					},
				}},
				"meta": map[string]any{"pagination": map[string]any{"last_page": 1}},
			})
		case "/v1/pricing":
			writeJSONStatus(writer, http.StatusOK, map[string]any{"pricing": map[string]any{
				"currency": "EUR",
				"primary_ips": []any{map[string]any{"type": "ipv4", "prices": []any{
					map[string]any{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.01"}},
					map[string]any{"location": "nbg1", "price_hourly": map[string]string{"gross": "0.01"}},
				}}},
			}})
		default:
			writeJSONStatus(writer, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer server.Close()
	options := DefaultOptions()
	options.ProviderEndpoint = server.URL + "/v1"
	options.HTTPClient = server.Client()
	options.AllowInsecureHTTP = true
	options.LoadHetznerToken = func() (string, error) { return "synthetic-token", nil }
	result, err := EstimatePrice(t.Context(), PriceRequest{ServerType: "cx22", Locations: []string{"fsn1", "nbg1"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if !sawQuery.Load() || result.Status != PriceStatusQuoted || result.ServerType != "cx22" || result.LocationPolicy != LocationPolicyAllowlist || len(result.RequestedLocations) != 2 || result.RequestedLocations[0] != "fsn1" || result.RequestedLocations[1] != "nbg1" || result.Currency != "EUR" || result.LowestHourlyGross != "" || len(result.Locations) != 2 || result.Locations[0].Location != "fsn1" || result.Locations[1].Location != "nbg1" {
		t.Fatalf("price estimate = %#v; query = %v", result, sawQuery.Load())
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "lowest_hourly_gross") {
		t.Fatalf("price estimate exposed a ranking field: %s", encoded)
	}
	anyLocation, err := EstimatePrice(t.Context(), PriceRequest{ServerType: "cx22"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if anyLocation.Status != PriceStatusQuoted || anyLocation.LocationPolicy != LocationPolicyAnyEligible || len(anyLocation.RequestedLocations) != 0 || len(anyLocation.Locations) != 2 {
		t.Fatalf("any-location price estimate = %#v", anyLocation)
	}
}

func TestOptionalPriceEstimateReportsUnavailableWithoutAuthorizingCreate(t *testing.T) {
	options := Options{
		ProviderEndpoint: "https://pricing.invalid/v1",
		HTTPClient:       offlineDoer(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic pricing outage") }),
		LoadHetznerToken: func() (string, error) { return "synthetic-token", nil },
	}
	result, err := EstimatePriceOptional(t.Context(), PriceRequest{ServerType: "cx22", Locations: []string{"fsn1"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PriceStatusUnavailable || result.ServerType != "cx22" || result.LocationPolicy != LocationPolicyAllowlist || len(result.RequestedLocations) != 1 || result.RequestedLocations[0] != "fsn1" || len(result.Locations) != 0 {
		t.Fatalf("optional price estimate = %#v", result)
	}
}
