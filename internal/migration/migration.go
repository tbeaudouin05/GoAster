// Package migration implements the source-side half of the central
// GoAsterWeb migration contract. It keeps provider credentials and handoff
// material out of scheduler messages and model-visible results.
package migration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goaster/internal/localenv"
	"goaster/internal/protectedfile"
)

const (
	MigrationWebOrigin            = "https://goasterweb-thomas.fly.dev"
	DefaultProviderEndpoint       = "https://api.hetzner.cloud/v1"
	MonitorIntervalSeconds  int64 = 30 * 60
	PriceStatusQuoted             = "quoted"
	PriceStatusUnavailable        = "unavailable"

	webRequestMaxBytes    = 16 * 1024
	webResponseMaxBytes   = 64 * 1024
	handoffMaxBytes       = 32 * 1024
	maxMigrationLocations = 64
	migrationLifetime     = 7 * 24 * time.Hour

	handoffPrivateKeyName = "id_ed25519"
	handoffDocumentName   = "migration-handoff.txt"
)

var (
	errInvalidRequest      = errors.New("invalid migration request")
	errManualReview        = errors.New("migration requires manual inspection")
	errLegacyPriceApproval = errors.New("legacy migration price approval fields are unsupported")
)

// Request is the non-secret migration request. The legacy approved price
// fields are retained to detect and reject older price-constrained approvals.
// Current pricing is informational and is not a provider-create input.
type Request struct {
	ServerType string   `json:"server_type"`
	Locations  []string `json:"locations,omitempty"`
	// Deprecated: retained for compatibility with older scheduler messages.
	ApprovedHourlyGross string `json:"approved_hourly_gross,omitempty"`
	// Deprecated: retained for compatibility with older scheduler messages.
	ApprovedCurrency string `json:"approved_currency,omitempty"`
	JobID            string `json:"job_id,omitempty"`
}

// AvailabilityResult contains only public availability information returned by
// GoAsterWeb plus the source-side location intent used for presentation. The
// location policy fields are not part of the GoAsterWeb v1 response or
// request; they make the exact optional-location choice explicit to callers.
// It never contains prices or credentials.
type AvailabilityResult struct {
	Version            string   `json:"version"`
	ServerType         string   `json:"server_type"`
	LocationPolicy     string   `json:"location_policy"`
	RequestedLocations []string `json:"requested_locations,omitempty"`
	Locations          []string `json:"locations"`
}

// PriceRequest identifies the exact server type and optional strict location
// allow-list for a read-only public price estimate.
type PriceRequest struct {
	ServerType string   `json:"server_type"`
	Locations  []string `json:"locations,omitempty"`
}

// QuoteRequest is retained as a clear name for callers that present a quote
// before approval.
type QuoteRequest = PriceRequest

// PriceLocation is a public gross hourly estimate for one eligible location.
type PriceLocation struct {
	Location    string `json:"location"`
	HourlyGross string `json:"hourly_gross"`
}

// PriceEstimate is safe for the model and owner conversation. It is sourced
// from the read-only provider pricing API, not from the GoAsterWeb machine API.
// LocationPolicy and RequestedLocations preserve the exact location intent;
// Locations contains only locations for which a current estimate is present.
// Pricing is informational only; LowestHourlyGross is retained for decoding
// older results but is no longer populated or used for approval.
type PriceEstimate struct {
	Status             string          `json:"status"`
	ServerType         string          `json:"server_type"`
	LocationPolicy     string          `json:"location_policy"`
	RequestedLocations []string        `json:"requested_locations,omitempty"`
	Currency           string          `json:"currency,omitempty"`
	LowestHourlyGross  string          `json:"lowest_hourly_gross,omitempty"`
	Locations          []PriceLocation `json:"locations,omitempty"`
}

// QuoteResult is an alias-friendly name for the public estimate result.
type QuoteResult = PriceEstimate

// CreateResult deliberately excludes the owner URL and private key. It is
// suitable for returning through a model-facing tool after direct delivery.
type CreateResult struct {
	Status        string `json:"status"`
	ServerType    string `json:"server_type"`
	Attempt       string `json:"attempt,omitempty"`
	ServerID      int64  `json:"server_id,omitempty"`
	IP            string `json:"ip,omitempty"`
	Expires       int64  `json:"expires,omitempty"`
	Delivered     bool   `json:"delivered,omitempty"`
	ManualReview  bool   `json:"manual_review,omitempty"`
	MonitorID     string `json:"monitor_id,omitempty"`
	TaskDirectory string `json:"-"`
	HandoffPath   string `json:"-"`
}

// Result is the safe result of one monitor turn.
type Result = CreateResult

// StatusResult reports only local scheduler state and the non-secret request.
type StatusResult struct {
	MonitorID      string  `json:"monitor_id"`
	MonitorOn      bool    `json:"monitor_on"`
	SchedulerState string  `json:"scheduler_state,omitempty"`
	Migration      Result  `json:"migration"`
	Request        Request `json:"request"`
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type availabilityResponse struct {
	Version    string   `json:"version"`
	ServerType string   `json:"server_type"`
	Locations  []string `json:"locations"`
}

// Options contains test seams and the protected handoff delivery callback.
// Production callers should use DefaultOptions and replace only the delivery
// callback. TempDirectory is a pre-created private task directory for tests;
// production leaves it empty so the GoAster temporary policy is resolved here.
type Options struct {
	WebOrigin         string
	ProviderEndpoint  string
	HTTPClient        httpDoer
	Now               func() time.Time
	LoadHetznerToken  func() (string, error)
	DeliverHandoff    func(context.Context, []byte) error
	DisableMonitor    func() error
	TempDirectory     string
	AllowInsecureHTTP bool
}

// DefaultOptions returns fixed HTTPS endpoints and a client that neither uses
// a proxy nor follows redirects. It performs no network or filesystem I/O.
func DefaultOptions() Options {
	return Options{
		WebOrigin:        MigrationWebOrigin,
		ProviderEndpoint: DefaultProviderEndpoint,
		HTTPClient: &http.Client{
			Timeout:       40 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: noRedirect,
		},
		Now:              func() time.Time { return time.Now().UTC() },
		LoadHetznerToken: loadHetznerToken,
	}
}

// Availability performs exactly one bounded read-only v1 availability request.
func Availability(ctx context.Context, request PriceRequest, options Options) (AvailabilityResult, error) {
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return AvailabilityResult{}, err
	}
	options = completeOptions(options)
	token, err := options.LoadHetznerToken()
	if err != nil {
		return AvailabilityResult{}, fmt.Errorf("load migration provider token: %w", err)
	}
	if !validHetznerToken(token) {
		return AvailabilityResult{}, errors.Join(errManualReview, errors.New("migration provider token is invalid"))
	}
	return availabilityWithToken(ctx, request.ServerType, request.Locations, token, options)
}

// CheckAvailability is the explicit name used by monitor callers.
func CheckAvailability(ctx context.Context, request Request, options Options) (AvailabilityResult, error) {
	return Availability(ctx, PriceRequest{ServerType: request.ServerType, Locations: request.Locations}, options)
}

func availabilityWithToken(ctx context.Context, serverType string, locations []string, token string, options Options) (AvailabilityResult, error) {
	payload := struct {
		ServerType   string   `json:"server_type"`
		Locations    []string `json:"locations,omitempty"`
		HetznerToken string   `json:"hetzner_token"`
	}{ServerType: serverType, Locations: append([]string(nil), locations...), HetznerToken: token}
	body, err := json.Marshal(payload)
	if err != nil {
		return AvailabilityResult{}, fmt.Errorf("encode migration availability request: %w", err)
	}
	defer clear(body)
	// A sentinel distinguishes a required missing field from a valid empty
	// array after JSON decoding. A JSON null is rejected by the same check.
	response := availabilityResponse{Locations: []string{"\x00missing"}}
	if err := webJSON(ctx, options, "/migration/v1/machines/availability", body, http.StatusOK, &response); err != nil {
		return AvailabilityResult{}, fmt.Errorf("migration availability request: %w", err)
	}
	result := AvailabilityResult{
		Version:            response.Version,
		ServerType:         response.ServerType,
		LocationPolicy:     locationPolicyFor(locations),
		RequestedLocations: append([]string(nil), locations...),
		Locations:          response.Locations,
	}
	if err := validateAvailabilityResult(result, serverType, locations); err != nil {
		return AvailabilityResult{}, fmt.Errorf("migration availability response: %w", err)
	}
	sort.Strings(result.Locations)
	return result, nil
}

// Create generates the login key and owner-link secret locally, sends one
// central creation request, writes the private handoff in the protected
// GoAster temporary task directory, and delivers that handoff directly to the
// configured paired Telegram user. It never retries.
func Create(ctx context.Context, request Request, options Options) (CreateResult, error) {
	if err := validateBillableRequest(request); err != nil {
		return CreateResult{}, err
	}
	options = completeOptions(options)
	if options.DeliverHandoff == nil {
		return CreateResult{}, errors.New("migration handoff delivery is unavailable")
	}
	token, err := options.LoadHetznerToken()
	if err != nil {
		return CreateResult{}, fmt.Errorf("load migration provider token: %w", err)
	}
	if !validHetznerToken(token) {
		return CreateResult{}, errors.Join(errManualReview, errors.New("migration provider token is invalid"))
	}
	return createWithToken(ctx, request, token, options)
}

func createWithToken(ctx context.Context, request Request, token string, options Options) (CreateResult, error) {
	if err := validateBillableRequest(request); err != nil {
		return CreateResult{}, err
	}
	if options.DeliverHandoff == nil {
		return CreateResult{}, errors.New("migration handoff delivery is unavailable")
	}
	publicKey, privateKey, ownerSecret, ownerDigest, err := generateLoginMaterial()
	if err != nil {
		return CreateResult{}, fmt.Errorf("generate migration login material: %w", err)
	}
	defer clear(privateKey)

	taskDirectory, err := migrationTaskDirectory(options.TempDirectory)
	if err != nil {
		return CreateResult{}, fmt.Errorf("prepare migration temporary directory: %w", err)
	}
	privateKeyPath := filepath.Join(taskDirectory, handoffPrivateKeyName)
	if _, err := protectedfile.WriteExclusive(privateKeyPath, privateKey); err != nil {
		return CreateResult{}, fmt.Errorf("store migration private key: %w", err)
	}

	payload := struct {
		ServerType        string   `json:"server_type"`
		Locations         []string `json:"locations,omitempty"`
		LoginPublicKey    string   `json:"login_public_key"`
		OwnerSecretDigest string   `json:"owner_secret_digest"`
		HetznerToken      string   `json:"hetzner_token"`
	}{
		ServerType:        request.ServerType,
		Locations:         append([]string(nil), request.Locations...),
		LoginPublicKey:    publicKey,
		OwnerSecretDigest: ownerDigest,
		HetznerToken:      token,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CreateResult{}, fmt.Errorf("encode migration create request: %w", err)
	}
	defer clear(body)
	var response createResponse
	if err := webJSON(ctx, options, "/migration/v1/machines/create", body, http.StatusCreated, &response); err != nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, ManualReview: true, TaskDirectory: taskDirectory}, errors.Join(errManualReview, fmt.Errorf("migration creation request: %w", err))
	}
	if err := validateCreateResponse(response, options); err != nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, ManualReview: true, TaskDirectory: taskDirectory}, errors.Join(errManualReview, fmt.Errorf("migration creation response: %w", err))
	}

	setupURL, err := appendOwnerSecret(response.OwnerURL, ownerSecret, options)
	if err != nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, Attempt: response.Attempt, ServerID: response.ServerID, IP: response.IP, Expires: response.Expires, ManualReview: true, TaskDirectory: taskDirectory}, errors.Join(errManualReview, err)
	}
	handoff := buildHandoff(setupURL, response.ServerID, response.IP, privateKey)
	if len(handoff) == 0 || len(handoff) > handoffMaxBytes {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, Attempt: response.Attempt, ServerID: response.ServerID, IP: response.IP, Expires: response.Expires, ManualReview: true, TaskDirectory: taskDirectory}, errors.Join(errManualReview, errors.New("migration handoff exceeds its bound"))
	}
	handoffPath := filepath.Join(taskDirectory, handoffDocumentName)
	if _, err := protectedfile.WriteExclusive(handoffPath, handoff); err != nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, Attempt: response.Attempt, ServerID: response.ServerID, IP: response.IP, Expires: response.Expires, ManualReview: true, TaskDirectory: taskDirectory}, errors.Join(errManualReview, fmt.Errorf("store migration handoff: %w", err))
	}
	defer clear(handoff)
	if options.DeliverHandoff == nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, Attempt: response.Attempt, ServerID: response.ServerID, IP: response.IP, Expires: response.Expires, ManualReview: true, TaskDirectory: taskDirectory, HandoffPath: handoffPath}, errors.Join(errManualReview, errors.New("migration handoff delivery is unavailable"))
	}
	if err := options.DeliverHandoff(ctx, handoff); err != nil {
		return CreateResult{Status: "manual_review", ServerType: request.ServerType, Attempt: response.Attempt, ServerID: response.ServerID, IP: response.IP, Expires: response.Expires, ManualReview: true, TaskDirectory: taskDirectory, HandoffPath: handoffPath}, errors.Join(errManualReview, fmt.Errorf("deliver migration handoff: %w", err))
	}
	return CreateResult{
		Status:        "created",
		ServerType:    request.ServerType,
		Attempt:       response.Attempt,
		ServerID:      response.ServerID,
		IP:            response.IP,
		Expires:       response.Expires,
		Delivered:     true,
		TaskDirectory: taskDirectory,
		HandoffPath:   handoffPath,
	}, nil
}

type createResponse struct {
	Version     string `json:"version"`
	State       string `json:"state"`
	Attempt     string `json:"attempt"`
	ServerLabel string `json:"server_label"`
	ServerID    int64  `json:"server_id"`
	IP          string `json:"ip"`
	OwnerURL    string `json:"owner_url"`
	Expires     int64  `json:"expires"`
}

func validateMachineRequest(serverType string, locations []string) error {
	if !safeMachineName(serverType, 128) {
		return errInvalidRequest
	}
	seen := make(map[string]struct{}, len(locations))
	if len(locations) > maxMigrationLocations {
		return errInvalidRequest
	}
	for _, location := range locations {
		if !safeMachineName(location, 128) {
			return errInvalidRequest
		}
		if _, exists := seen[location]; exists {
			return errInvalidRequest
		}
		seen[location] = struct{}{}
	}
	return nil
}

func locationPolicyFor(locations []string) string {
	if len(locations) == 0 {
		return LocationPolicyAnyEligible
	}
	return LocationPolicyAllowlist
}

func validateBillableRequest(request Request) error {
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return err
	}
	return rejectLegacyPriceApproval(request)
}

func rejectLegacyPriceApproval(request Request) error {
	if request.ApprovedHourlyGross != "" || request.ApprovedCurrency != "" {
		return errLegacyPriceApproval
	}
	return nil
}

func validateAvailabilityResult(result AvailabilityResult, serverType string, requested []string) error {
	if result.Version != "v1" || result.ServerType != serverType || result.Locations == nil || (len(result.Locations) == 1 && result.Locations[0] == "\x00missing") || len(result.Locations) > maxMigrationLocations {
		return errInvalidRequest
	}
	allowed := make(map[string]struct{}, len(requested))
	for _, location := range requested {
		allowed[location] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Locations))
	for _, location := range result.Locations {
		if !safeMachineName(location, 128) {
			return errInvalidRequest
		}
		if len(requested) > 0 {
			if _, ok := allowed[location]; !ok {
				return errInvalidRequest
			}
		}
		if _, exists := seen[location]; exists {
			return errInvalidRequest
		}
		seen[location] = struct{}{}
	}
	return nil
}

func validateCreateResponse(response createResponse, options Options) error {
	if response.Version != "v1" || response.State != "created" || response.ServerID <= 0 || response.ServerLabel == "" || !safeName(response.ServerLabel, 256) {
		return errInvalidRequest
	}
	if len(response.Attempt) != 64 || strings.ToLower(response.Attempt) != response.Attempt {
		return errInvalidRequest
	}
	if _, err := hex.DecodeString(response.Attempt); err != nil {
		return errInvalidRequest
	}
	if response.ServerLabel != "goaster-migration-"+response.Attempt[:16] {
		return errInvalidRequest
	}
	if net.ParseIP(response.IP) == nil || !publicIP(response.IP) {
		return errInvalidRequest
	}
	now := currentTime(options.Now)
	if response.Expires <= now.Unix() || response.Expires > now.Add(migrationLifetime).Unix() {
		return errInvalidRequest
	}
	if err := validateOwnerURL(response.OwnerURL, response.Attempt, options.WebOrigin, options.AllowInsecureHTTP); err != nil {
		return err
	}
	return nil
}

func validateOwnerURL(raw, attempt, origin string, allowInsecure bool) error {
	value, err := url.Parse(raw)
	if err != nil || value.User != nil || value.ForceQuery || value.RawQuery != "" || value.Path != "/migration" || value.Fragment != "attempt="+attempt {
		return errInvalidRequest
	}
	originURL, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil || value.Scheme != originURL.Scheme || value.Host != originURL.Host {
		return errInvalidRequest
	}
	if value.Scheme != "https" && !(allowInsecure && value.Scheme == "http") {
		return errInvalidRequest
	}
	return nil
}

func appendOwnerSecret(raw, ownerSecret string, options Options) (string, error) {
	value, err := url.Parse(raw)
	if err != nil || value.Fragment == "" || strings.Contains(value.Fragment, "owner=") {
		return "", errInvalidRequest
	}
	if err := validateOwnerURL(raw, strings.TrimPrefix(value.Fragment, "attempt="), options.WebOrigin, options.AllowInsecureHTTP); err != nil {
		return "", err
	}
	if !validOwnerSecret(ownerSecret) {
		return "", errInvalidRequest
	}
	return raw + "&owner=" + ownerSecret, nil
}

func generateLoginMaterial() (string, []byte, string, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, "", "", err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	clear(private)
	if err != nil {
		return "", nil, "", "", err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	if len(privatePEM) == 0 {
		return "", nil, "", "", errors.New("private key encoding failed")
	}
	publicWire := sshWireString([]byte("ssh-ed25519"))
	publicWire = append(publicWire, sshWireString(public)...)
	publicValue := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(publicWire)
	var secret [32]byte
	if _, err := io.ReadFull(rand.Reader, secret[:]); err != nil {
		clear(privatePEM)
		return "", nil, "", "", err
	}
	ownerSecret := base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256(secret[:])
	ownerDigest := hex.EncodeToString(digest[:])
	clear(secret[:])
	return publicValue, privatePEM, ownerSecret, ownerDigest, nil
}

func sshWireString(value []byte) []byte {
	wire := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(wire[:4], uint32(len(value)))
	copy(wire[4:], value)
	return wire
}

func buildHandoff(setupURL string, serverID int64, ip string, privateKey []byte) []byte {
	value := "GoAster migration setup: " + setupURL + "\n" +
		"Server ID: " + fmt.Sprintf("%d", serverID) + "\n" +
		"IP: " + ip + "\n\n"
	return append([]byte(value), privateKey...)
}

func migrationTaskDirectory(configured string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured || !tightenPrivateDirectory(configured) {
			return "", errors.New("configured migration directory is not private")
		}
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find GoAster home: %w", err)
	}
	root := filepath.Join(home, ".goaster", "tmp")
	if err := ensurePrivatePath(root); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(root, "migration-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return "", errors.Join(err, os.Remove(directory))
	}
	if !privateDirectory(directory) {
		return "", errors.Join(errors.New("migration task directory is not private"), os.Remove(directory))
	}
	// Deliberately do not arrange automatic cleanup. The owner removes the
	// temporary migration directory after delivery or manual abandonment.
	return directory, nil
}

func ensurePrivatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("GoAster temporary path is invalid")
	}
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	remainder = strings.TrimLeft(remainder, string(filepath.Separator))
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(remainder, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create GoAster temporary directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("GoAster temporary path is not a private directory")
		}
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("protect GoAster temporary directory: %w", err)
	}
	return nil
}

func privateDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0077 == 0
}

func tightenPrivateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if err := os.Chmod(path, 0700); err != nil {
		return false
	}
	return privateDirectory(path)
}

func webJSON(ctx context.Context, options Options, path string, body []byte, expectedStatus int, output any) error {
	endpoint, err := webEndpoint(options.WebOrigin, path, options.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	if len(body) > webRequestMaxBytes {
		return errors.New("migration request body exceeds the limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build migration web request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Del("Origin")
	client := options.HTTPClient
	if client == nil {
		return errors.New("migration HTTP client is unavailable")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("migration web transport: %w", err)
	}
	if response == nil || response.Body == nil {
		return errors.New("migration web response body is unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, webResponseMaxBytes+1))
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		clear(data)
		return fmt.Errorf("read migration web response: %w", err)
	}
	defer clear(data)
	if len(data) > webResponseMaxBytes {
		return errors.New("migration web response exceeds the limit")
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("migration web returned HTTP %d", response.StatusCode)
	}
	if output == nil {
		return nil
	}
	return decodeExactJSON(data, output)
}

func webEndpoint(origin, path string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("migration Web origin is invalid")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return "", errors.New("migration Web origin must use HTTPS")
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func decodeExactJSON(data []byte, output any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("inspect migration JSON fields: %w", err)
	}
	for field := range fields {
		if field != strings.ToLower(field) {
			return errors.New("migration JSON field names must be lower-case")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode migration JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("migration JSON contains trailing data")
		}
		return fmt.Errorf("decode migration trailing data: %w", err)
	}
	return nil
}

func completeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.WebOrigin == "" {
		options.WebOrigin = defaults.WebOrigin
	}
	if options.ProviderEndpoint == "" {
		options.ProviderEndpoint = defaults.ProviderEndpoint
	}
	if options.HTTPClient == nil {
		options.HTTPClient = defaults.HTTPClient
	}
	if options.Now == nil {
		options.Now = defaults.Now
	}
	if options.LoadHetznerToken == nil {
		options.LoadHetznerToken = defaults.LoadHetznerToken
	}
	return options
}

func currentTime(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func loadHetznerToken() (string, error) {
	store, err := localenv.NewStore()
	if err == nil {
		err = store.Load()
	}
	if err == nil {
		if token := store.Values()["HCLOUD_TOKEN"]; validHetznerToken(token) {
			return token, nil
		}
	}
	if token, ok := os.LookupEnv("HCLOUD_TOKEN"); ok && validHetznerToken(token) {
		return token, nil
	}
	if err != nil {
		return "", fmt.Errorf("load Hetzner settings: %w", err)
	}
	return "", errors.New("Hetzner token is unavailable")
}

func validHetznerToken(value string) bool {
	return len(value) > 0 && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t ")
}

func validOwnerSecret(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func safeName(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n\t")
}

func safeMachineName(value string, limit int) bool {
	if !safeName(value, limit) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func publicIP(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
