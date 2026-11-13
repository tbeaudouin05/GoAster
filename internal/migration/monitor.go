// Package migration contains the narrow, in-process migration monitor.
// It owns the durable create-once claim and the minimum provider/Web calls
// needed by the approved handoff contract. It never exposes migration secrets
// to the model or scheduler message.
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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"goaster/internal/filesync"
	"goaster/internal/localenv"
	"goaster/internal/protectedfile"
)

const (
	MigrationWebOrigin        = "https://goasterweb-thomas.fly.dev"
	DefaultStatePath          = "/var/lib/goaster-telegram/migration-state"
	DefaultLockPath           = "/var/lib/goaster-telegram/migration-state.lock"
	DefaultHandoffPath        = "/var/lib/goaster-telegram/migration-handoff"
	DefaultProviderEndpoint   = "https://api.hetzner.cloud/v1"
	stateVersion              = 1
	stateMaxBytes             = 256 * 1024
	handoffMaxBytes           = 32 * 1024
	providerResponseMaxBytes  = 256 * 1024
	webResponseMaxBytes       = 32 * 1024
	providerMutationTimeout   = 15 * time.Second
	migrationGrantLifetime    = 7 * 24 * time.Hour
	stateStagingSuffix        = ".staging"
	handoffStagingSuffix      = ".staging"
	migrationImageName        = "ubuntu-24.04"
	migrationArchitectureX86  = "x86"
	migrationArchitectureArm  = "arm"
	statusMonitoring          = "monitoring"
	statusWebRegistering      = "web_registering"
	statusRegistered          = "registered"
	statusProviderKeyRegister = "provider_key_registering"
	statusKeyRegistered       = "key_registered"
	statusCreating            = "creating"
	statusCreated             = "created"
	statusDeliveryPending     = "delivery_pending"
	statusTerminal            = "terminal"
)

var (
	errInvalidInput        = errors.New("invalid migration input")
	errStateUnavailable    = errors.New("migration state unavailable")
	errTerminal            = errors.New("migration requires manual reconciliation")
	errTargetChanged       = errors.New("approved migration target changed")
	errRemoteUnavailable   = errors.New("migration remote request failed")
	errRemoteMalformed     = errors.New("migration remote response was malformed")
	errMigrationExpired    = errors.New("migration handoff expired")
	errProviderUnavailable = errors.New("migration provider credential unavailable")

	// ErrCleanupBusy means a monitor or delivery operation currently owns the
	// durable migration lock. The supervised Telegram tick may try again later.
	ErrCleanupBusy = errors.New("migration cleanup is busy")
)

// Paths are fixed runtime locations. Production callers use DefaultPaths;
// tests may provide an already protected directory.
type Paths struct {
	State   string
	Lock    string
	Handoff string
}

var DefaultPaths = Paths{
	State:   DefaultStatePath,
	Lock:    DefaultLockPath,
	Handoff: DefaultHandoffPath,
}

// Target contains the immutable terms approved for this exact migration job.
// IDs and names are both required to prevent a provider-side replacement from
// silently satisfying an old approval.
type Target struct {
	ServerTypeID      int64    `json:"server_type_id"`
	ServerTypeName    string   `json:"server_type_name"`
	Architecture      string   `json:"architecture"`
	ImageID           int64    `json:"image_id"`
	ImageName         string   `json:"image_name"`
	LocationAllowlist []string `json:"location_allowlist,omitempty"`
	NetworkIDs        []int64  `json:"network_ids"`
	EnableIPv4        bool     `json:"enable_ipv4"`
	EnableIPv6        bool     `json:"enable_ipv6"`
	PrimaryIPType     string   `json:"primary_ip_type,omitempty"`
}

// Request is the non-secret internal migration request. Provider credentials,
// Web grant secrets, owner URLs, and private keys are never request fields.
type Request struct {
	Action                string `json:"action"`
	JobID                 string `json:"job_id"`
	Target                Target `json:"target"`
	ApprovedBaselineGross string `json:"approved_baseline_gross,omitempty"`
	ApprovedCurrency      string `json:"approved_currency,omitempty"`
	Approved              bool   `json:"approved,omitempty"`
}

// QuoteRequest contains only the target terms needed for a read-only quote.
type QuoteRequest struct {
	JobID  string `json:"job_id"`
	Target Target `json:"target"`
}

// Offer is safe to return to the model and contains no credentials or URLs.
type Offer struct {
	Location       string `json:"location"`
	ServerGross    string `json:"server_gross"`
	PrimaryIPGross string `json:"primary_ip_gross"`
	TotalGross     string `json:"total_gross"`
}

// QuoteResult is the read-only baseline that the skill presents before
// requesting one explicit start approval.
type QuoteResult struct {
	Status             string  `json:"status"`
	Currency           string  `json:"currency,omitempty"`
	BaselineTotalGross string  `json:"baseline_total_gross,omitempty"`
	Offers             []Offer `json:"offers,omitempty"`
}

// Result is safe for the model and contains no URL, key, token, callback
// secret, response body, or provider error text.
type Result struct {
	JobID              string `json:"job_id"`
	Status             string `json:"status"`
	Attempt            string `json:"attempt,omitempty"`
	ServerID           int64  `json:"server_id,omitempty"`
	SelectedLocation   string `json:"selected_location,omitempty"`
	ApprovedCurrency   string `json:"approved_currency,omitempty"`
	BaselineTotalGross string `json:"baseline_total_gross,omitempty"`
	DeliveryPending    bool   `json:"delivery_pending,omitempty"`
	StopMonitor        bool   `json:"stop_monitor"`
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options keeps network and clock seams private to the package tests while
// leaving the production command with one fixed HTTPS client and paths.
type Options struct {
	Paths             Paths
	ProviderEndpoint  string
	WebEndpoint       string
	HTTPClient        httpDoer
	Now               func() time.Time
	AllowInsecureHTTP bool
	LoadProviderToken func() (string, error)
	DeliverHandoff    func(context.Context, []byte) error
	DisableMonitor    func() error
}

// DefaultOptions returns the production settings. It performs no network I/O.
func DefaultOptions() Options {
	return Options{
		Paths:             DefaultPaths,
		ProviderEndpoint:  DefaultProviderEndpoint,
		WebEndpoint:       MigrationWebOrigin,
		HTTPClient:        newMigrationHTTPClient(),
		Now:               func() time.Time { return time.Now().UTC() },
		LoadProviderToken: loadProviderToken,
	}
}

func newMigrationHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       providerMutationTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: noRedirect,
	}
}

// Execute performs one non-overlapping monitor invocation. It is safe to call
// again after a restart: mutation states are terminal rather than replayed.
func Execute(ctx context.Context, request Request, options Options) (Result, error) {
	if err := validateRequest(request, options.AllowInsecureHTTP); err != nil {
		return Result{}, err
	}
	options = completeOptions(options)
	if err := ensureRuntimeParents(options.Paths); err != nil {
		return Result{}, errStateUnavailable
	}
	fingerprint := requestFingerprint(request)
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		if filesync.IsLockBusy(err) {
			return Result{JobID: request.JobID, Status: "locked", StopMonitor: false}, nil
		}
		return Result{}, errStateUnavailable
	}
	defer lock.Close()
	if err := cleanupStateStaging(options.Paths.State); err != nil {
		return Result{}, errStateUnavailable
	}

	now := options.Now().UTC()
	state, exists, err := loadState(options.Paths.State)
	if err != nil {
		return Result{}, errStateUnavailable
	}
	if !exists {
		state = migrationState{
			Version:               stateVersion,
			JobID:                 request.JobID,
			Fingerprint:           fingerprint,
			Target:                request.Target,
			ApprovedBaselineGross: request.ApprovedBaselineGross,
			ApprovedCurrency:      request.ApprovedCurrency,
			Status:                statusMonitoring,
		}
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	} else if state.JobID != request.JobID || state.Fingerprint != fingerprint ||
		!sameTarget(state.Target, request.Target) ||
		state.ApprovedBaselineGross != request.ApprovedBaselineGross ||
		state.ApprovedCurrency != request.ApprovedCurrency {
		return Result{}, errInvalidInput
	}

	base := resultForState(state)
	if state.Status == statusTerminal || state.Status == statusDeliveryPending || state.Status == statusCreated {
		base.StopMonitor = true
		base.DeliveryPending = state.Status == statusDeliveryPending || state.HandoffReady
		return base, nil
	}
	if state.Status == statusWebRegistering || state.Status == statusRegistered || state.Status == statusProviderKeyRegister || state.Status == statusKeyRegistered || state.Status == statusCreating {
		base = machineTerminal(options, state, base, errTerminal)
		return base, nil
	}

	token, tokenErr := options.LoadProviderToken()
	if tokenErr != nil || !validProviderToken(token) {
		base = machineTerminal(options, state, base, errProviderUnavailable)
		return base, nil
	}
	machine := monitorMachine{request: request, options: options, now: now, providerToken: token}
	if state.Status == statusMonitoring {
		availability, ready, checkErr := machine.checkAvailability(ctx, request.Target, state.ApprovedBaselineGross, state.ApprovedCurrency)
		if checkErr != nil {
			base = machine.terminalResult(state, base, checkErr)
			return base, nil
		}
		if !ready {
			base.Status = statusMonitoring
			return base, nil
		}
		state.SelectedLocationID = availability.LocationID
		state.SelectedLocationName = availability.LocationName
		state.SelectedServerGross = availability.ServerGross
		state.SelectedPrimaryIPGross = availability.PrimaryIPGross
		state.SelectedTotalGross = availability.TotalGross
		if err := disableBeforeMutation(options); err != nil {
			base = machine.terminalResult(state, base, err)
			return base, nil
		}
		publicKey, privateKey, keyErr := generateLoginKey()
		if keyErr != nil {
			base = machine.terminalResult(state, base, keyErr)
			return base, nil
		}
		state.PublicKey = publicKey
		state.PrivateKey = privateKey
		state.Status = statusWebRegistering
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
		grant, registerErr := machine.registerWeb(ctx, publicKey)
		if registerErr != nil {
			base = machine.terminalResult(state, base, registerErr)
			return base, nil
		}
		state.Attempt = grant.Attempt
		state.CallbackSecret = grant.CallbackSecret
		state.OwnerSecret = grant.OwnerSecret
		state.ServerLabel = grant.ServerLabel
		state.Expires = grant.Expires
		state.Status = statusRegistered
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	}

	if state.Status != statusRegistered && state.Status != statusKeyRegistered {
		base = machine.terminalResult(state, base, errTerminal)
		return base, nil
	}
	if state.Expires <= machine.currentUnix() || state.Attempt == "" || state.OwnerSecret == "" || state.CallbackSecret == "" || state.PublicKey == "" || state.PrivateKey == "" {
		base = machineTerminal(options, state, base, errTerminal)
		return base, nil
	}
	webStatus, statusErr := machine.readWebStatus(ctx, state)
	if statusErr != nil || webStatus.State != "registered" || webStatus.Expires != state.Expires || webStatus.ServerLabel != state.ServerLabel || webStatus.LoginPublicKey != state.PublicKey || webStatus.KeyReceived || webStatus.Sealed {
		base = machine.terminalResult(state, base, errRemoteMalformed)
		return base, nil
	}
	if state.Expires <= machine.currentUnix() {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}

	if state.Status == statusRegistered {
		state.Status = statusProviderKeyRegister
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
		providerKeyID, keyErr := machine.registerProviderKey(ctx, state)
		if keyErr != nil {
			base = machine.terminalResult(state, base, keyErr)
			return base, nil
		}
		state.ProviderKeyID = providerKeyID
		state.Status = statusKeyRegistered
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	}

	if state.Status != statusKeyRegistered || state.ProviderKeyID <= 0 || state.SelectedLocationID <= 0 || !safeIdentifier(state.SelectedLocationName, 128) {
		base = machine.terminalResult(state, base, errTerminal)
		return base, nil
	}
	if state.Expires <= machine.currentUnix() {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	availability, ready, checkErr := machine.checkAvailability(ctx, request.Target, state.ApprovedBaselineGross, state.ApprovedCurrency)
	if checkErr != nil || !ready {
		base = machine.terminalResult(state, base, errTargetChanged)
		return base, nil
	}
	state.SelectedLocationID = availability.LocationID
	state.SelectedLocationName = availability.LocationName
	state.SelectedServerGross = availability.ServerGross
	state.SelectedPrimaryIPGross = availability.PrimaryIPGross
	state.SelectedTotalGross = availability.TotalGross
	if err := machine.checkNetworks(ctx, request.Target.NetworkIDs); err != nil {
		base = machine.terminalResult(state, base, err)
		return base, nil
	}
	if state.Expires <= machine.currentUnix() {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}

	// This durable state transition is the exclusive create claim. It is
	// persisted before the HTTP mutation and is never replayed after restart.
	state.Status = statusCreating
	if err := saveState(options.Paths.State, state); err != nil {
		return Result{}, errStateUnavailable
	}
	if state.Expires <= machine.currentUnix() {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	createNow := machine.currentUnix()
	if state.Expires <= createNow {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	createCtx, cancel := createContext(ctx, state.Expires, createNow)
	if createCtx.Err() != nil {
		cancel()
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	serverID, createErr := machine.createServer(createCtx, state)
	cancel()
	if createErr != nil {
		base = machine.terminalResult(state, base, createErr)
		return base, nil
	}
	state.ServerID = serverID
	state.Status = statusCreated
	if err := saveState(options.Paths.State, state); err != nil {
		// The provider call already happened. Never call it again; recovery of
		// the durable creating state is terminal/manual as well.
		base.Status, base.StopMonitor, base.ServerID = statusTerminal, true, serverID
		return base, nil
	}
	state, handoffErr := publishHandoff(options.Paths, state)
	if handoffErr != nil {
		state.Status = statusDeliveryPending
		state.TerminalReason = "handoff_pending"
		_ = saveState(options.Paths.State, state)
		base = resultForState(state)
		base.StopMonitor = true
		base.DeliveryPending = true
		return base, nil
	}
	if options.DeliverHandoff == nil {
		state.Status = statusDeliveryPending
		state.TerminalReason = "delivery_unavailable"
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
		base = resultForState(state)
		base.StopMonitor = true
		base.DeliveryPending = true
		return base, nil
	}
	var deliveryErr error
	state, deliveryErr = deliverHandoffLocked(ctx, options, options.DeliverHandoff, state)
	if deliveryErr != nil {
		state.Status = statusDeliveryPending
		state.TerminalReason = "delivery_pending"
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
		base = resultForState(state)
		base.StopMonitor = true
		base.DeliveryPending = true
		return base, nil
	}
	state.Status = statusCreated
	base = resultForState(state)
	base.StopMonitor = true
	return base, nil
}

type migrationState struct {
	Version                int                    `json:"version"`
	JobID                  string                 `json:"job_id"`
	Fingerprint            string                 `json:"fingerprint"`
	Target                 Target                 `json:"target"`
	ApprovedBaselineGross  string                 `json:"approved_baseline_gross"`
	ApprovedCurrency       string                 `json:"approved_currency"`
	Status                 string                 `json:"status"`
	Attempt                string                 `json:"attempt,omitempty"`
	CallbackSecret         string                 `json:"callback_secret,omitempty"`
	OwnerSecret            string                 `json:"owner_secret,omitempty"`
	ServerLabel            string                 `json:"server_label,omitempty"`
	Expires                int64                  `json:"expires,omitempty"`
	PublicKey              string                 `json:"public_key,omitempty"`
	PrivateKey             string                 `json:"private_key,omitempty"`
	ProviderKeyID          int64                  `json:"provider_key_id,omitempty"`
	SelectedLocationID     int64                  `json:"selected_location_id,omitempty"`
	SelectedLocationName   string                 `json:"selected_location_name,omitempty"`
	SelectedServerGross    string                 `json:"selected_server_gross,omitempty"`
	SelectedPrimaryIPGross string                 `json:"selected_primary_ip_gross,omitempty"`
	SelectedTotalGross     string                 `json:"selected_total_gross,omitempty"`
	HandoffReady           bool                   `json:"handoff_ready,omitempty"`
	HandoffIdentity        protectedfile.Identity `json:"handoff_identity,omitempty"`
	ServerID               int64                  `json:"server_id,omitempty"`
	TerminalReason         string                 `json:"terminal_reason,omitempty"`
}

type grant struct {
	Attempt        string `json:"attempt"`
	CallbackSecret string `json:"callback_secret"`
	OwnerSecret    string `json:"owner_secret"`
	ServerLabel    string `json:"server_label"`
	OwnerURL       string `json:"owner_url"`
	Expires        int64  `json:"expires"`
}

type webStatus struct {
	State          string `json:"state"`
	Expires        int64  `json:"expires"`
	ServerLabel    string `json:"server_label"`
	ServerID       string `json:"server_id"`
	IP             string `json:"ip"`
	KeyReceived    bool   `json:"key_received"`
	LoginPublicKey string `json:"login_public_key"`
	Sealed         bool   `json:"sealed"`
}

type monitorMachine struct {
	request       Request
	options       Options
	now           time.Time
	providerToken string
}

type providerAvailability struct {
	LocationID     int64
	LocationName   string
	ServerGross    string
	PrimaryIPGross string
	TotalGross     string
	Currency       string
}

type providerServerType struct {
	ID           int64                  `json:"id"`
	Name         string                 `json:"name"`
	Architecture string                 `json:"architecture"`
	Deprecated   bool                   `json:"deprecated"`
	Deprecation  json.RawMessage        `json:"deprecation"`
	Locations    []providerTypeLocation `json:"locations"`
	Prices       []providerTypePrice    `json:"prices"`
}

type providerServerTypesPage struct {
	ServerTypes []providerServerType `json:"server_types"`
	Meta        providerPageMeta     `json:"meta"`
}

type providerPageMeta struct {
	Pagination providerPagination `json:"pagination"`
}

type providerPagination struct {
	Page     int `json:"page"`
	LastPage int `json:"last_page"`
}

type providerTypeLocation struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Available   bool            `json:"available"`
	Deprecation json.RawMessage `json:"deprecation"`
}

type providerTypePrice struct {
	Location    string `json:"location"`
	PriceHourly struct {
		Gross string `json:"gross"`
	} `json:"price_hourly"`
}

type providerPricing struct {
	Currency   string              `json:"currency"`
	PrimaryIPs []providerPrimaryIP `json:"primary_ips"`
}

type providerPrimaryIP struct {
	Type   string              `json:"type"`
	Prices []providerTypePrice `json:"prices"`
}

type providerImage struct {
	ID           int64           `json:"id"`
	Status       string          `json:"status"`
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	OSFlavor     string          `json:"os_flavor"`
	OSVersion    string          `json:"os_version"`
	Architecture string          `json:"architecture"`
	Deprecated   json.RawMessage `json:"deprecated"`
	Deleted      json.RawMessage `json:"deleted"`
}

type providerNetwork struct {
	ID int64 `json:"id"`
}

type providerSSHKey struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	PublicKey string            `json:"public_key"`
	Labels    map[string]string `json:"labels"`
}

type providerServer struct {
	ID int64 `json:"id"`
}

func validateRequest(request Request, allowInsecure bool) error {
	if request.Action != "monitor" || !safeIdentifier(request.JobID, 256) || !request.Approved {
		return errInvalidInput
	}
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if _, err := parseDecimal(request.ApprovedBaselineGross); err != nil {
		return errInvalidInput
	}
	if !safeCurrency(request.ApprovedCurrency) {
		return errInvalidInput
	}
	if !allowInsecure && !strings.HasPrefix(MigrationWebOrigin, "https://") {
		return errInvalidInput
	}
	return nil
}

func validateTarget(target Target) error {
	if target.ServerTypeID <= 0 || !safeIdentifier(target.ServerTypeName, 128) ||
		!supportedMigrationArchitecture(target.Architecture) || target.ImageID <= 0 ||
		target.ImageName != migrationImageName || !target.EnableIPv4 {
		return errInvalidInput
	}
	if target.PrimaryIPType != "" && !safeIdentifier(target.PrimaryIPType, 32) {
		return errInvalidInput
	}
	seenLocations := make(map[string]bool, len(target.LocationAllowlist))
	for _, location := range target.LocationAllowlist {
		if !safeIdentifier(location, 128) || seenLocations[location] {
			return errInvalidInput
		}
		seenLocations[location] = true
	}
	seenNetworks := make(map[int64]bool, len(target.NetworkIDs))
	for _, networkID := range target.NetworkIDs {
		if networkID <= 0 || seenNetworks[networkID] {
			return errInvalidInput
		}
		seenNetworks[networkID] = true
	}
	return nil
}

func supportedMigrationArchitecture(value string) bool {
	return value == migrationArchitectureX86 || value == migrationArchitectureArm
}

func safeIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n\t")
}

func completeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.Paths.State == "" {
		options.Paths.State = defaults.Paths.State
	}
	if options.Paths.Lock == "" {
		options.Paths.Lock = defaults.Paths.Lock
	}
	if options.Paths.Handoff == "" {
		options.Paths.Handoff = defaults.Paths.Handoff
	}
	if options.ProviderEndpoint == "" {
		options.ProviderEndpoint = defaults.ProviderEndpoint
	}
	if options.WebEndpoint == "" {
		options.WebEndpoint = defaults.WebEndpoint
	}
	if options.HTTPClient == nil {
		options.HTTPClient = defaults.HTTPClient
	}
	if options.Now == nil {
		options.Now = defaults.Now
	}
	if options.LoadProviderToken == nil {
		options.LoadProviderToken = defaults.LoadProviderToken
	}
	return options
}

func loadProviderToken() (string, error) {
	store, err := localenv.NewStore()
	if err == nil {
		err = store.Load()
	}
	if err == nil {
		if token := store.Values()["HCLOUD_TOKEN"]; validProviderToken(token) {
			return token, nil
		}
	}
	if token, found := os.LookupEnv("HCLOUD_TOKEN"); found && validProviderToken(token) {
		return token, nil
	}
	return "", errProviderUnavailable
}

func validProviderToken(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func safeCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func resultForState(state migrationState) Result {
	return Result{
		JobID:              state.JobID,
		Status:             state.Status,
		Attempt:            state.Attempt,
		ServerID:           state.ServerID,
		SelectedLocation:   state.SelectedLocationName,
		ApprovedCurrency:   state.ApprovedCurrency,
		BaselineTotalGross: state.ApprovedBaselineGross,
		DeliveryPending:    state.Status == statusDeliveryPending || state.HandoffReady,
	}
}

func disableBeforeMutation(options Options) error {
	if options.DisableMonitor == nil {
		return errTerminal
	}
	if err := options.DisableMonitor(); err != nil {
		return errors.Join(errTerminal, err)
	}
	return nil
}

// Quote performs only bounded, read-only provider lookups. It returns the
// cheapest currently compatible offer so the skill can present one complete
// gross baseline before asking for approval.
func Quote(ctx context.Context, request QuoteRequest, options Options) (QuoteResult, error) {
	if !safeIdentifier(request.JobID, 256) {
		return QuoteResult{}, errInvalidInput
	}
	if err := validateTarget(request.Target); err != nil {
		return QuoteResult{}, err
	}
	options = completeOptions(options)
	token, err := options.LoadProviderToken()
	if err != nil || !validProviderToken(token) {
		return QuoteResult{}, errProviderUnavailable
	}
	machine := monitorMachine{options: options, providerToken: token}
	serverType, err := machine.fetchServerType(ctx, request.Target)
	if err != nil {
		return QuoteResult{}, err
	}
	imageBody, err := machine.providerGET(ctx, "/images/"+strconv.FormatInt(request.Target.ImageID, 10), providerResponseMaxBytes)
	if err != nil {
		return QuoteResult{}, errRemoteUnavailable
	}
	var imageResponse struct {
		Image providerImage `json:"image"`
	}
	if decodeJSON(imageBody, &imageResponse) != nil {
		return QuoteResult{}, errRemoteMalformed
	}
	image := imageResponse.Image
	if image.ID != request.Target.ImageID || image.Name != request.Target.ImageName || image.OSFlavor != "ubuntu" || image.OSVersion != "24.04" || image.Status != "available" || image.Type != "system" || image.Architecture != request.Target.Architecture || !supportedMigrationArchitecture(image.Architecture) || present(image.Deprecated) || present(image.Deleted) {
		return QuoteResult{}, errTargetChanged
	}
	pricing, err := machine.fetchPricing(ctx)
	if err != nil {
		return QuoteResult{}, err
	}
	offers, err := compatibleOffers(request.Target, serverType, pricing)
	if err != nil {
		return QuoteResult{}, err
	}
	result := QuoteResult{Currency: pricing.Currency}
	for _, offer := range offers {
		result.Offers = append(result.Offers, Offer{
			Location:       offer.location.Name,
			ServerGross:    decimalString(offer.serverGross),
			PrimaryIPGross: decimalString(offer.primaryIPGross),
			TotalGross:     decimalString(offer.total),
		})
	}
	sort.Slice(result.Offers, func(left, right int) bool {
		leftValue, _ := parseDecimal(result.Offers[left].TotalGross)
		rightValue, _ := parseDecimal(result.Offers[right].TotalGross)
		return compareDecimal(leftValue, rightValue) < 0 || compareDecimal(leftValue, rightValue) == 0 && result.Offers[left].Location < result.Offers[right].Location
	})
	if len(result.Offers) == 0 {
		result.Status = "unavailable"
		return result, nil
	}
	result.Status = "quoted"
	result.BaselineTotalGross = result.Offers[0].TotalGross
	return result, nil
}

func requestFingerprint(request Request) string {
	value, _ := json.Marshal(struct {
		JobID                 string `json:"job_id"`
		Target                Target `json:"target"`
		ApprovedBaselineGross string `json:"approved_baseline_gross"`
		ApprovedCurrency      string `json:"approved_currency"`
	}{request.JobID, request.Target, request.ApprovedBaselineGross, request.ApprovedCurrency})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sameTarget(left, right Target) bool {
	leftData, _ := json.Marshal(left)
	rightData, _ := json.Marshal(right)
	return bytes.Equal(leftData, rightData)
}

func ensureRuntimeParents(paths Paths) error {
	for _, path := range []string{paths.State, paths.Lock, paths.Handoff} {
		if err := ensureDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errStateUnavailable
	}
	missing := make([]string, 0, 4)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errStateUnavailable
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return errStateUnavailable
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return errStateUnavailable
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		created := true
		if err := os.Mkdir(missing[index], 0700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return errStateUnavailable
			}
			created = false
		}
		if err := os.Chmod(missing[index], 0700); err != nil {
			return errStateUnavailable
		}
		if created {
			if err := syncDirectoryPath(filepath.Dir(missing[index])); err != nil {
				return errStateUnavailable
			}
		}
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	info, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, errStateUnavailable
	}
	var file *os.File
	var err error
	if created {
		file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			created = false
		}
	} else {
		if _, err := protectedfile.Read(path, 1); err != nil {
			return nil, errStateUnavailable
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if !created && file == nil {
		if _, err := protectedfile.Read(path, 1); err != nil {
			return nil, errStateUnavailable
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, errStateUnavailable
	}
	opened, statErr := file.Stat()
	if statErr != nil || (info != nil && !os.SameFile(info, opened)) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		file.Close()
		return nil, errStateUnavailable
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, errStateUnavailable
	}
	if err := filesync.LockFile(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func loadState(path string) (migrationState, bool, error) {
	content, err := protectedfile.Read(path, stateMaxBytes)
	if err != nil {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return migrationState{}, false, nil
		}
		return migrationState{}, false, errStateUnavailable
	}
	defer clear(content.Data)
	var state migrationState
	decoder := json.NewDecoder(bytes.NewReader(content.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return migrationState{}, false, errStateUnavailable
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || !validState(state) {
		return migrationState{}, false, errStateUnavailable
	}
	return state, true, nil
}

func validState(state migrationState) bool {
	if state.Version != stateVersion || !safeIdentifier(state.JobID, 256) ||
		len(state.Fingerprint) != sha256.Size*2 || strings.ToLower(state.Fingerprint) != state.Fingerprint ||
		!safeIdentifier(state.Status, 64) || !safeCurrency(state.ApprovedCurrency) {
		return false
	}
	switch state.Status {
	case statusMonitoring, statusWebRegistering, statusRegistered, statusProviderKeyRegister, statusKeyRegistered, statusCreating, statusCreated, statusDeliveryPending, statusTerminal:
	default:
		return false
	}
	if _, err := parseDecimal(state.ApprovedBaselineGross); err != nil {
		return false
	}
	return validateTarget(state.Target) == nil
}

func saveState(path string, state migrationState) error {
	data, err := json.Marshal(state)
	if err != nil || len(data) > stateMaxBytes {
		return errStateUnavailable
	}
	defer clear(data)
	if err := cleanupStateStaging(path); err != nil {
		return errStateUnavailable
	}
	stagePath := stateStagingPath(path)
	identity, err := protectedfile.WriteExclusive(stagePath, data)
	if err != nil {
		return errStateUnavailable
	}
	staged := true
	defer func() {
		if staged {
			_ = protectedfile.RemoveIfSame(stagePath, identity)
		}
	}()
	content, err := protectedfile.Read(stagePath, stateMaxBytes)
	if err != nil || content.Identity != identity {
		clear(content.Data)
		return errStateUnavailable
	}
	clear(content.Data)
	if err := validStateDestination(path); err != nil {
		return errStateUnavailable
	}
	if err := os.Rename(stagePath, path); err != nil {
		return errStateUnavailable
	}
	staged = false
	if err := syncDirectoryPath(filepath.Dir(path)); err != nil {
		return errStateUnavailable
	}
	return nil
}

var syncDirectoryPath = filesync.DirectoryPath

func stateStagingPath(path string) string { return path + stateStagingSuffix }

func cleanupStateStaging(path string) error {
	stagePath := stateStagingPath(path)
	content, err := protectedfile.Read(stagePath, stateMaxBytes)
	if err != nil {
		if _, statErr := os.Lstat(stagePath); errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return errStateUnavailable
	}
	clear(content.Data)
	if err := protectedfile.RemoveIfSame(stagePath, content.Identity); err != nil {
		return errStateUnavailable
	}
	return nil
}

func validStateDestination(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errStateUnavailable
	}
	content, err := protectedfile.Read(path, stateMaxBytes)
	if err != nil {
		return errStateUnavailable
	}
	clear(content.Data)
	return nil
}

func handoffStagePath(path string) string { return path + handoffStagingSuffix }

func publishHandoff(paths Paths, state migrationState) (migrationState, error) {
	stagePath := handoffStagePath(paths.Handoff)
	if state.HandoffIdentity != (protectedfile.Identity{}) {
		if content, err := protectedfile.Read(paths.Handoff, handoffMaxBytes); err == nil && content.Identity == state.HandoffIdentity {
			state.HandoffReady = true
			if err := saveState(paths.State, state); err != nil {
				return state, errStateUnavailable
			}
			return state, nil
		}
		if content, err := protectedfile.Read(stagePath, handoffMaxBytes); err == nil && content.Identity == state.HandoffIdentity {
			if err := publishStagedFile(stagePath, paths.Handoff); err != nil {
				_ = protectedfile.RemoveIfSame(stagePath, state.HandoffIdentity)
				return state, err
			}
			state.HandoffReady = true
			if err := saveState(paths.State, state); err != nil {
				return state, errStateUnavailable
			}
			return state, nil
		}
		return state, errTerminal
	}

	// The staging name is reserved and never published. Removing an orphan
	// from a crash before the identity was persisted is safe under the monitor
	// lock and prevents it from blocking the next local staging attempt.
	if err := protectedfile.RemoveUnpublished(stagePath); err != nil {
		return state, err
	}
	handoff := []byte(buildHandoff(state))
	identity, err := protectedfile.WriteExclusive(stagePath, handoff)
	clear(handoff)
	if err != nil {
		return state, err
	}
	state.HandoffIdentity = identity
	state.HandoffReady = false
	if err := saveState(paths.State, state); err != nil {
		_ = protectedfile.RemoveIfSame(stagePath, identity)
		return state, errStateUnavailable
	}
	if err := publishStagedFile(stagePath, paths.Handoff); err != nil {
		_ = protectedfile.RemoveIfSame(stagePath, identity)
		return state, err
	}
	state.HandoffReady = true
	if err := saveState(paths.State, state); err != nil {
		return state, errStateUnavailable
	}
	return state, nil
}

func publishStagedFile(stagePath, handoffPath string) error {
	if _, err := os.Lstat(handoffPath); err == nil {
		return errTerminal
	} else if !errors.Is(err, os.ErrNotExist) {
		return errStateUnavailable
	}
	if err := os.Rename(stagePath, handoffPath); err != nil {
		return errStateUnavailable
	}
	if err := syncDirectoryPath(filepath.Dir(handoffPath)); err != nil {
		return errStateUnavailable
	}
	return nil
}

func clearSecret(value string) string {
	data := []byte(value)
	clear(data)
	return ""
}

func clearNoGrantPrivateKey(state migrationState) (migrationState, bool) {
	if state.PrivateKey == "" || state.Attempt != "" && state.Expires > 0 {
		return state, false
	}
	state.PrivateKey = clearSecret(state.PrivateKey)
	return state, true
}

func markTerminal(path string, state migrationState, reason string) error {
	state.Status = statusTerminal
	state.TerminalReason = "operation_failed"
	state, _ = clearNoGrantPrivateKey(state)
	return saveState(path, state)
}

func safeReason(errorValue error) string {
	switch {
	case errors.Is(errorValue, errTargetChanged):
		return "target_changed"
	case errors.Is(errorValue, errRemoteMalformed):
		return "remote_malformed"
	case errors.Is(errorValue, errRemoteUnavailable):
		return "remote_unavailable"
	case errors.Is(errorValue, errTerminal):
		return "manual_reconcile"
	default:
		return "operation_failed"
	}
}

func (m monitorMachine) terminalResult(state migrationState, base Result, reason error) Result {
	state, _ = clearNoGrantPrivateKey(state)
	state.Status = statusTerminal
	state.TerminalReason = safeReason(reason)
	_ = saveState(m.options.Paths.State, state)
	base.Status, base.StopMonitor = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base
}

func (m monitorMachine) currentUnix() int64 {
	return m.options.Now().UTC().Unix()
}

func machineTerminal(options Options, state migrationState, base Result, reason error) Result {
	state, _ = clearNoGrantPrivateKey(state)
	state.Status = statusTerminal
	state.TerminalReason = safeReason(reason)
	_ = saveState(options.Paths.State, state)
	base.Status, base.StopMonitor = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base
}

func expireState(statePath, handoffPath string, state migrationState, base Result) (Result, error) {
	cleanupErr := cleanupStateStaging(statePath)
	if state.HandoffIdentity != (protectedfile.Identity{}) {
		if state.HandoffReady {
			cleanupErr = errors.Join(cleanupErr, protectedfile.RemoveIfSame(handoffPath, state.HandoffIdentity))
		} else {
			stageErr := protectedfile.RemoveIfSame(handoffStagePath(handoffPath), state.HandoffIdentity)
			finalErr := protectedfile.RemoveIfSame(handoffPath, state.HandoffIdentity)
			cleanupErr = errors.Join(cleanupErr, stageErr, finalErr)
		}
		if cleanupErr == nil {
			state.HandoffReady = false
			state.HandoffIdentity = protectedfile.Identity{}
		}
	} else {
		cleanupErr = errors.Join(cleanupErr, protectedfile.RemoveUnpublished(handoffStagePath(handoffPath)))
	}
	state.CallbackSecret = clearSecret(state.CallbackSecret)
	state.OwnerSecret = clearSecret(state.OwnerSecret)
	state.PrivateKey = clearSecret(state.PrivateKey)
	state.Status = statusTerminal
	if cleanupErr != nil {
		state.TerminalReason = "cleanup_failed"
	} else {
		state.TerminalReason = "expired"
	}
	stateErr := saveState(statePath, state)
	base.Status, base.StopMonitor = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base, errors.Join(cleanupErr, stateErr)
}

// CleanupExpired performs expiry cleanup under the same lock used by the
// monitor and handoff delivery. It does not create a runtime directory when
// no migration state exists, so Telegram startup does not depend on migration
// setup. A busy lock is a normal tick miss; callers may try on the next tick.
func CleanupExpired(paths Paths, now func() time.Time) error {
	found := false
	for _, path := range []string{paths.State, stateStagingPath(paths.State)} {
		if _, err := os.Lstat(path); err == nil {
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return errStateUnavailable
		}
	}
	if !found {
		return nil
	}
	lock, err := acquireLock(paths.Lock)
	if err != nil {
		if filesync.IsLockBusy(err) {
			return ErrCleanupBusy
		}
		return errStateUnavailable
	}
	defer lock.Close()
	if err := cleanupStateStaging(paths.State); err != nil {
		return errStateUnavailable
	}
	state, exists, err := loadState(paths.State)
	if err != nil {
		return errStateUnavailable
	}
	if !exists {
		return nil
	}
	var changed bool
	state, changed = clearNoGrantPrivateKey(state)
	if changed {
		if err := saveState(paths.State, state); err != nil {
			return errStateUnavailable
		}
	}
	if state.Expires <= 0 || currentTime(now).Unix() < state.Expires {
		return nil
	}
	_, err = expireState(paths.State, paths.Handoff, state, Result{JobID: state.JobID, Status: state.Status})
	return err
}

// DeliverHandoff reads and sends one complete handoff while holding the
// monitor lock. There is intentionally no durable delivery claim: a failed
// or ambiguous send leaves the file available for manual/repeated owner use
// until expiry. A successful send removes the exact identity that was read.
func DeliverHandoff(ctx context.Context, paths Paths, now func() time.Time, send func([]byte) error) error {
	if send == nil {
		return errors.New("migration handoff unavailable")
	}
	lock, err := acquireLock(paths.Lock)
	if err != nil {
		if filesync.IsLockBusy(err) {
			return ErrCleanupBusy
		}
		return errors.New("migration handoff unavailable")
	}
	defer lock.Close()
	options := completeOptions(Options{Paths: paths, Now: now})
	if err := cleanupStateStaging(paths.State); err != nil {
		return errors.New("migration handoff unavailable")
	}
	state, exists, err := loadState(paths.State)
	if err != nil || !exists || state.Expires <= 0 || state.ServerID <= 0 {
		return errors.New("migration handoff unavailable")
	}
	if currentTime(now).Unix() >= state.Expires {
		return errMigrationExpired
	}
	state, err = deliverHandoffLocked(ctx, options, func(_ context.Context, data []byte) error { return send(data) }, state)
	if err != nil {
		return err
	}
	state.Status = statusCreated
	return saveState(paths.State, state)
}

func deliverHandoffLocked(ctx context.Context, options Options, send func(context.Context, []byte) error, state migrationState) (migrationState, error) {
	if send == nil || !state.HandoffReady || state.HandoffIdentity == (protectedfile.Identity{}) {
		return state, errors.New("migration handoff unavailable")
	}
	content, err := protectedfile.Read(options.Paths.Handoff, handoffMaxBytes)
	if err != nil || len(content.Data) == 0 || content.Identity != state.HandoffIdentity {
		clear(content.Data)
		return state, errors.New("migration handoff unavailable")
	}
	defer clear(content.Data)
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if err := send(ctx, content.Data); err != nil {
		return state, err
	}
	if err := protectedfile.RemoveIfSame(options.Paths.Handoff, content.Identity); err != nil {
		return state, errors.New("migration handoff changed during delivery")
	}
	state.HandoffReady = false
	state.HandoffIdentity = protectedfile.Identity{}
	if err := saveState(options.Paths.State, state); err != nil {
		return state, errors.New("migration handoff state could not be saved")
	}
	return state, nil
}

func currentTime(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func acquireMutationContext(parent context.Context, seconds int64) (context.Context, context.CancelFunc) {
	duration := providerMutationTimeout
	if seconds > 0 && seconds < int64(duration/time.Second) {
		duration = time.Duration(seconds) * time.Second
	}
	return context.WithTimeout(parent, duration)
}

func createContext(parent context.Context, expires, now int64) (context.Context, context.CancelFunc) {
	remaining := expires - now
	if remaining <= 0 {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	return acquireMutationContext(parent, remaining)
}

func generateLoginKey() (string, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", errTerminal
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	clear(private)
	if err != nil {
		return "", "", errTerminal
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	if len(privatePEM) == 0 {
		return "", "", errTerminal
	}
	wire := make([]byte, 4+len("ssh-ed25519")+4+len(public))
	binary.BigEndian.PutUint32(wire[0:4], uint32(len("ssh-ed25519")))
	copy(wire[4:], "ssh-ed25519")
	position := 4 + len("ssh-ed25519")
	binary.BigEndian.PutUint32(wire[position:position+4], uint32(len(public)))
	copy(wire[position+4:], public)
	publicKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire)
	return publicKey, string(privatePEM), nil
}

func buildHandoff(state migrationState) string {
	return ownerURL(MigrationWebOrigin, state.Attempt, state.OwnerSecret) + "\n" +
		"Server ID: " + strconv.FormatInt(state.ServerID, 10) + "\n" + state.PrivateKey
}

func ownerURL(origin, attempt, ownerSecret string) string {
	return strings.TrimSuffix(origin, "/") + "/migration#attempt=" + attempt + "&owner=" + ownerSecret
}

func (m monitorMachine) registerWeb(ctx context.Context, publicKey string) (grant, error) {
	body, err := json.Marshal(struct {
		LoginPublicKey string `json:"login_public_key"`
	}{publicKey})
	if err != nil {
		return grant{}, errRemoteMalformed
	}
	status, response, requestErr := m.jsonRequest(ctx, strings.TrimSuffix(m.options.WebEndpoint, "/")+"/migration/register", http.MethodPost, "", "", "application/json", body, webResponseMaxBytes)
	clear(body)
	if requestErr != nil {
		return grant{}, requestErr
	}
	defer clear(response)
	if status != http.StatusCreated {
		return grant{}, errRemoteUnavailable
	}
	var value grant
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validGrant(value, m.now, MigrationWebOrigin) {
		return grant{}, errRemoteMalformed
	}
	return value, nil
}

func validGrant(value grant, now time.Time, origin string) bool {
	if len(value.Attempt) != 64 || strings.ToLower(value.Attempt) != value.Attempt {
		return false
	}
	if _, err := hex.DecodeString(value.Attempt); err != nil || !validSecret(value.CallbackSecret) || !validSecret(value.OwnerSecret) || value.CallbackSecret == value.OwnerSecret || value.ServerLabel != "goaster-migration-"+value.Attempt[:16] || value.Expires <= now.Unix() || value.Expires > now.Add(migrationGrantLifetime+time.Hour).Unix() {
		return false
	}
	return value.OwnerURL == ownerURL(origin, value.Attempt, value.OwnerSecret)
}

func validSecret(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func (m monitorMachine) readWebStatus(ctx context.Context, state migrationState) (webStatus, error) {
	status, body, err := m.jsonRequest(ctx, strings.TrimSuffix(m.options.WebEndpoint, "/")+"/migration/status", http.MethodPost, state.Attempt, state.OwnerSecret, "application/json", []byte("{}"), webResponseMaxBytes)
	if err != nil {
		return webStatus{}, err
	}
	defer clear(body)
	if status != http.StatusOK {
		return webStatus{}, errRemoteUnavailable
	}
	var value webStatus
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return webStatus{}, errRemoteMalformed
	}
	return value, nil
}

func (m monitorMachine) checkAvailability(ctx context.Context, target Target, baseline, currency string) (providerAvailability, bool, error) {
	serverType, err := m.fetchServerType(ctx, target)
	if err != nil {
		return providerAvailability{}, false, err
	}
	imageBody, imageErr := m.providerGET(ctx, "/images/"+strconv.FormatInt(target.ImageID, 10), providerResponseMaxBytes)
	if imageErr != nil {
		return providerAvailability{}, false, errRemoteUnavailable
	}
	var imageResponse struct {
		Image providerImage `json:"image"`
	}
	if decodeJSON(imageBody, &imageResponse) != nil {
		return providerAvailability{}, false, errRemoteMalformed
	}
	image := imageResponse.Image
	if image.ID != target.ImageID || image.Name != target.ImageName || image.OSFlavor != "ubuntu" || image.OSVersion != "24.04" || image.Status != "available" || image.Type != "system" || image.Architecture != target.Architecture || !supportedMigrationArchitecture(image.Architecture) || present(image.Deprecated) || present(image.Deleted) {
		return providerAvailability{}, false, errTargetChanged
	}
	pricing, err := m.fetchPricing(ctx)
	if err != nil {
		return providerAvailability{}, false, err
	}
	offers, err := compatibleOffers(target, serverType, pricing)
	if err != nil {
		return providerAvailability{}, false, err
	}
	if len(offers) == 0 {
		return providerAvailability{}, false, nil
	}
	approved, err := parseDecimal(baseline)
	if err != nil || pricing.Currency != currency {
		return providerAvailability{}, false, errTargetChanged
	}
	cap := maximumApprovedDecimal(approved)
	selected := offerCandidate{}
	for _, offer := range offers {
		if compareDecimal(offer.total, cap) > 0 {
			continue
		}
		if selected.location.Name == "" || compareDecimal(offer.total, selected.total) < 0 || compareDecimal(offer.total, selected.total) == 0 && offer.location.Name < selected.location.Name {
			selected = offer
		}
	}
	if selected.location.Name == "" {
		return providerAvailability{}, false, nil
	}
	return providerAvailability{
		LocationID:     selected.location.ID,
		LocationName:   selected.location.Name,
		ServerGross:    decimalString(selected.serverGross),
		PrimaryIPGross: decimalString(selected.primaryIPGross),
		TotalGross:     decimalString(selected.total),
		Currency:       pricing.Currency,
	}, true, nil
}

type offerCandidate struct {
	location       providerTypeLocation
	serverGross    decimal
	primaryIPGross decimal
	total          decimal
}

func (m monitorMachine) fetchServerType(ctx context.Context, target Target) (providerServerType, error) {
	for page := 1; page <= 100; page++ {
		body, err := m.providerGET(ctx, "/server_types?per_page=100&page="+strconv.Itoa(page), providerResponseMaxBytes)
		if err != nil {
			return providerServerType{}, errRemoteUnavailable
		}
		var response providerServerTypesPage
		if decodeJSON(body, &response) != nil || len(response.ServerTypes) == 0 && page == 1 {
			return providerServerType{}, errRemoteMalformed
		}
		matches := make([]providerServerType, 0, 1)
		for _, value := range response.ServerTypes {
			if value.ID == target.ServerTypeID {
				matches = append(matches, value)
			}
		}
		if len(matches) > 1 {
			return providerServerType{}, errRemoteMalformed
		}
		if len(matches) == 1 {
			value := matches[0]
			if value.Name != target.ServerTypeName || value.Architecture != target.Architecture || value.Deprecated || present(value.Deprecation) {
				return providerServerType{}, errTargetChanged
			}
			return value, nil
		}
		lastPage := response.Meta.Pagination.LastPage
		if lastPage == 0 || page >= lastPage || len(response.ServerTypes) == 0 {
			break
		}
	}
	return providerServerType{}, errTargetChanged
}

func (m monitorMachine) fetchPricing(ctx context.Context) (providerPricing, error) {
	body, err := m.providerGET(ctx, "/pricing", providerResponseMaxBytes)
	if err != nil {
		return providerPricing{}, errRemoteUnavailable
	}
	var response struct {
		Pricing providerPricing `json:"pricing"`
	}
	if decodeJSON(body, &response) != nil || !safeCurrency(response.Pricing.Currency) {
		return providerPricing{}, errRemoteMalformed
	}
	return response.Pricing, nil
}

func compatibleOffers(target Target, serverType providerServerType, pricing providerPricing) ([]offerCandidate, error) {
	locationByName := make(map[string]providerTypeLocation, len(serverType.Locations))
	for _, location := range serverType.Locations {
		if location.ID <= 0 || !safeIdentifier(location.Name, 128) || locationByName[location.Name].Name != "" {
			return nil, errRemoteMalformed
		}
		locationByName[location.Name] = location
	}
	serverPriceByLocation := make(map[string]decimal, len(serverType.Prices))
	for _, price := range serverType.Prices {
		if _, exists := serverPriceByLocation[price.Location]; exists {
			return nil, errRemoteMalformed
		}
		parsed, err := parseDecimal(price.PriceHourly.Gross)
		if err != nil {
			return nil, errRemoteMalformed
		}
		serverPriceByLocation[price.Location] = parsed
	}
	primaryPriceByType := make(map[string]map[string]decimal, len(pricing.PrimaryIPs))
	for _, primaryIP := range pricing.PrimaryIPs {
		if !safeIdentifier(primaryIP.Type, 32) || primaryPriceByType[primaryIP.Type] != nil {
			return nil, errRemoteMalformed
		}
		byLocation := make(map[string]decimal, len(primaryIP.Prices))
		for _, price := range primaryIP.Prices {
			if _, exists := byLocation[price.Location]; exists {
				return nil, errRemoteMalformed
			}
			parsed, err := parseDecimal(price.PriceHourly.Gross)
			if err != nil {
				return nil, errRemoteMalformed
			}
			byLocation[price.Location] = parsed
		}
		primaryPriceByType[primaryIP.Type] = byLocation
	}
	locationNames := make([]string, 0, len(locationByName))
	for name, location := range locationByName {
		if location.Available && locationAllowed(target, name) {
			locationNames = append(locationNames, name)
		}
	}
	sort.Strings(locationNames)
	offers := make([]offerCandidate, 0, len(locationNames))
	for _, name := range locationNames {
		serverGross, ok := serverPriceByLocation[name]
		if !ok {
			return nil, errRemoteMalformed
		}
		primaryIPGross, err := primaryIPCost(target, name, primaryPriceByType)
		if err != nil {
			return nil, err
		}
		offers = append(offers, offerCandidate{
			location:       locationByName[name],
			serverGross:    serverGross,
			primaryIPGross: primaryIPGross,
			total:          addDecimal(serverGross, primaryIPGross),
		})
	}
	return offers, nil
}

func locationAllowed(target Target, name string) bool {
	if len(target.LocationAllowlist) == 0 {
		return true
	}
	for _, allowed := range target.LocationAllowlist {
		if allowed == name {
			return true
		}
	}
	return false
}

func primaryIPCost(target Target, location string, prices map[string]map[string]decimal) (decimal, error) {
	types := make([]string, 0, 2)
	if target.EnableIPv4 {
		if target.PrimaryIPType != "" {
			types = append(types, target.PrimaryIPType)
		} else {
			types = append(types, "ipv4")
		}
	}
	if target.EnableIPv6 {
		types = append(types, "ipv6")
	}
	total := decimal{digits: "0"}
	for _, ipType := range types {
		byLocation, ok := prices[ipType]
		if !ok {
			return decimal{}, errRemoteMalformed
		}
		price, ok := byLocation[location]
		if !ok {
			return decimal{}, errRemoteMalformed
		}
		total = addDecimal(total, price)
	}
	return total, nil
}

func (m monitorMachine) checkNetworks(ctx context.Context, networkIDs []int64) error {
	for _, networkID := range networkIDs {
		body, err := m.providerGET(ctx, "/networks/"+strconv.FormatInt(networkID, 10), providerResponseMaxBytes)
		if err != nil {
			return errRemoteUnavailable
		}
		var response struct {
			Network providerNetwork `json:"network"`
		}
		if decodeJSON(body, &response) != nil || response.Network.ID != networkID {
			return errRemoteMalformed
		}
	}
	return nil
}

func (m monitorMachine) registerProviderKey(ctx context.Context, state migrationState) (int64, error) {
	body, err := json.Marshal(struct {
		Name      string            `json:"name"`
		PublicKey string            `json:"public_key"`
		Labels    map[string]string `json:"labels"`
	}{state.ServerLabel, state.PublicKey, map[string]string{"goaster-migration-attempt": state.Attempt}})
	if err != nil {
		return 0, errRemoteMalformed
	}
	status, response, requestErr := m.providerJSON(ctx, "/ssh_keys", body, providerResponseMaxBytes)
	clear(body)
	if requestErr != nil {
		return 0, requestErr
	}
	defer clear(response)
	if status != http.StatusCreated {
		return 0, errRemoteUnavailable
	}
	var result struct {
		SSHKey providerSSHKey `json:"ssh_key"`
	}
	if decodeJSON(response, &result) != nil || result.SSHKey.ID <= 0 || result.SSHKey.Name != state.ServerLabel || result.SSHKey.PublicKey != state.PublicKey || result.SSHKey.Labels["goaster-migration-attempt"] != state.Attempt {
		return 0, errRemoteMalformed
	}
	return result.SSHKey.ID, nil
}

func (m monitorMachine) createServer(ctx context.Context, state migrationState) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, errRemoteUnavailable
	}
	locationName := state.SelectedLocationName
	userData := callbackUserData(MigrationWebOrigin, state.Attempt, state.CallbackSecret)
	body, err := json.Marshal(struct {
		Name             string            `json:"name"`
		ServerType       int64             `json:"server_type"`
		Image            int64             `json:"image"`
		SSHKeys          []int64           `json:"ssh_keys,omitempty"`
		Location         string            `json:"location"`
		UserData         string            `json:"user_data"`
		StartAfterCreate bool              `json:"start_after_create"`
		Labels           map[string]string `json:"labels"`
		Networks         []int64           `json:"networks,omitempty"`
		PublicNet        publicNet         `json:"public_net"`
	}{state.ServerLabel, state.Target.ServerTypeID, state.Target.ImageID, []int64{state.ProviderKeyID}, locationName, userData, true, map[string]string{"goaster-migration-attempt": state.Attempt}, append([]int64(nil), state.Target.NetworkIDs...), publicNet{EnableIPv4: state.Target.EnableIPv4, EnableIPv6: state.Target.EnableIPv6}})
	clear([]byte(userData))
	if err != nil {
		return 0, errRemoteMalformed
	}
	status, response, requestErr := m.providerJSON(ctx, "/servers", body, providerResponseMaxBytes)
	clear(body)
	if requestErr != nil {
		return 0, requestErr
	}
	defer clear(response)
	if status != http.StatusCreated {
		return 0, errRemoteUnavailable
	}
	var result struct {
		Server providerServer `json:"server"`
	}
	if decodeJSON(response, &result) != nil || result.Server.ID <= 0 {
		return 0, errRemoteMalformed
	}
	return result.Server.ID, nil
}

type publicNet struct {
	EnableIPv4 bool `json:"enable_ipv4"`
	EnableIPv6 bool `json:"enable_ipv6"`
}

func callbackUserData(_ string, attempt, callbackSecret string) string {
	return "#!/bin/sh\n" +
		"set +x\n" +
		"umask 077\n" +
		"command -v curl >/dev/null 2>&1 || exit 1\n" +
		"[ -r /etc/ssl/certs/ca-certificates.crt ] || exit 1\n" +
		"[ -r /etc/ssh/ssh_host_ed25519_key.pub ] || exit 1\n" +
		"keyfile=$(mktemp /run/goaster-hostkey.XXXXXX) || exit 1\n" +
		"trap 'rm -f \"$keyfile\"' EXIT HUP INT TERM\n" +
		"awk 'NR == 1 && $1 == \"ssh-ed25519\" && NF >= 2 {print $1 \" \" $2; ok=1} END {if (!ok) exit 1}' /etc/ssh/ssh_host_ed25519_key.pub > \"$keyfile\" || exit 1\n" +
		"status=$(curl --silent --max-time 10 --connect-timeout 5 --output /dev/null --write-out \"%{http_code}\" --config - <<EOF\n" +
		"url = \"" + MigrationWebOrigin + "/migration/host-key\"\n" +
		"request = \"POST\"\n" +
		"header = \"Authorization: Bearer " + callbackSecret + "\"\n" +
		"header = \"X-Goaster-Attempt: " + attempt + "\"\n" +
		"header = \"Content-Type: text/plain\"\n" +
		"data-binary = \"@$keyfile\"\n" +
		"EOF\n" +
		"[ \"$status\" = \"204\" ]\n" +
		"exit $?\n"
}

func (m monitorMachine) providerGET(ctx context.Context, endpoint string, maxBytes int64) ([]byte, error) {
	status, body, err := m.requestJSON(ctx, strings.TrimSuffix(m.options.ProviderEndpoint, "/")+endpoint, http.MethodGet, requestAuthProvider, "", "", "", nil, maxBytes)
	if err != nil || status != http.StatusOK {
		clear(body)
		return nil, errRemoteUnavailable
	}
	return body, nil
}

func (m monitorMachine) providerJSON(ctx context.Context, endpoint string, body []byte, maxBytes int64) (int, []byte, error) {
	return m.requestJSON(ctx, strings.TrimSuffix(m.options.ProviderEndpoint, "/")+endpoint, http.MethodPost, requestAuthProvider, "", "", "application/json", body, maxBytes)
}

func (m monitorMachine) jsonRequest(ctx context.Context, endpoint, method, attempt, secret, contentType string, body []byte, maxBytes int64) (int, []byte, error) {
	return m.requestJSON(ctx, endpoint, method, requestAuthWeb, attempt, secret, contentType, body, maxBytes)
}

type requestAuth uint8

const (
	requestAuthNone requestAuth = iota
	requestAuthWeb
	requestAuthProvider
)

func (m monitorMachine) requestJSON(ctx context.Context, endpoint, method string, auth requestAuth, attempt, secret, contentType string, body []byte, maxBytes int64) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errRemoteUnavailable
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if attempt != "" {
		req.Header.Set("X-Goaster-Attempt", attempt)
	}
	if auth == requestAuthWeb && secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if auth == requestAuthProvider {
		req.Header.Set("Authorization", "Bearer "+m.providerToken)
	}
	response, err := m.options.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, errRemoteUnavailable
	}
	if response == nil || response.Body == nil {
		return 0, nil, errRemoteUnavailable
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	response.Body.Close()
	if readErr != nil || int64(len(responseBody)) > maxBytes {
		clear(responseBody)
		return 0, nil, errRemoteMalformed
	}
	return response.StatusCode, responseBody, nil
}

func decodeJSON(body []byte, value any) error {
	defer clear(body)
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(value); err != nil {
		return errRemoteMalformed
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errRemoteMalformed
	}
	return nil
}

func present(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
