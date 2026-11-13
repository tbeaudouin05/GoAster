// Package migration contains the narrow, root-invoked migration monitor.
// It owns only the durable create-once claim and the minimum provider/web
// calls needed by the approved handoff contract. It does not run a service.
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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goaster/internal/filesync"
	"goaster/internal/protectedfile"
)

const (
	DefaultStatePath          = "/var/lib/goaster-telegram/migration-state"
	DefaultLockPath           = "/var/lib/goaster-telegram/migration-state.lock"
	DefaultHandoffPath        = "/var/lib/goaster-telegram/migration-handoff"
	DefaultProviderEndpoint   = "https://api.hetzner.cloud/v1"
	stateVersion              = 1
	stateMaxBytes             = 256 * 1024
	commandInputMaxBytes      = 64 * 1024
	providerResponseMaxBytes  = 256 * 1024
	webResponseMaxBytes       = 32 * 1024
	providerMutationTimeout   = 15 * time.Second
	migrationGrantLifetime    = 7 * 24 * time.Hour
	statusMonitoring          = "monitoring"
	statusWebRegistering      = "web_registering"
	statusRegistered          = "registered"
	statusProviderKeyRegister = "provider_key_registering"
	statusKeyRegistered       = "key_registered"
	statusCreating            = "creating"
	statusCreated             = "created"
	statusTerminal            = "terminal"
)

var (
	errInvalidInput      = errors.New("invalid migration input")
	errStateUnavailable  = errors.New("migration state unavailable")
	errTerminal          = errors.New("migration requires manual reconciliation")
	errTargetChanged     = errors.New("approved migration target changed")
	errRemoteUnavailable = errors.New("migration remote request failed")
	errRemoteMalformed   = errors.New("migration remote response was malformed")
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

// Location is one approved provider location and its approved current gross
// hourly price. A list permits the owner to approve all eligible locations
// while retaining the exact price for each location.
type Location struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	HourlyGross string `json:"hourly_gross"`
}

// Target contains the immutable terms approved for this exact migration job.
// IDs and names are both required to prevent a provider-side replacement from
// silently satisfying an old approval.
type Target struct {
	ServerTypeID   int64      `json:"server_type_id"`
	ServerTypeName string     `json:"server_type_name"`
	Architecture   string     `json:"architecture"`
	ImageID        int64      `json:"image_id"`
	ImageName      string     `json:"image_name"`
	Locations      []Location `json:"locations"`
	NetworkIDs     []int64    `json:"network_ids"`
	EnableIPv4     bool       `json:"enable_ipv4"`
	EnableIPv6     bool       `json:"enable_ipv6"`
}

// Request is accepted only on root-owned stdin by goaster-telegram migration.
// ProviderToken is never persisted, logged, or returned.
type Request struct {
	Action        string `json:"action"`
	JobID         string `json:"job_id"`
	WebOrigin     string `json:"web_origin"`
	ProviderToken string `json:"provider_token"`
	Target        Target `json:"target"`
}

// Result is the complete safe output of the privileged helper. It contains no
// URL, key, token, callback secret, response body, or provider error text.
type Result struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	Attempt  string `json:"attempt,omitempty"`
	ServerID int64  `json:"server_id,omitempty"`
	StopCron bool   `json:"stop_cron"`
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options keeps network and clock seams private to the package tests while
// leaving the production command with one fixed HTTPS client and paths.
type Options struct {
	Paths             Paths
	ProviderEndpoint  string
	HTTPClient        httpDoer
	Now               func() time.Time
	AllowInsecureHTTP bool
}

// DefaultOptions returns the production settings. It performs no network I/O.
func DefaultOptions() Options {
	return Options{
		Paths:            DefaultPaths,
		ProviderEndpoint: DefaultProviderEndpoint,
		HTTPClient:       &http.Client{Timeout: providerMutationTimeout, CheckRedirect: noRedirect},
		Now:              func() time.Time { return time.Now().UTC() },
	}
}

// RunCommand decodes one bounded stdin request and emits only Result JSON.
func RunCommand(ctx context.Context, input io.Reader, output io.Writer, options Options) error {
	data, err := io.ReadAll(io.LimitReader(input, commandInputMaxBytes+1))
	defer clear(data)
	if err != nil || len(data) > commandInputMaxBytes {
		return errInvalidInput
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return errInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errInvalidInput
	}
	result, err := Execute(ctx, request, options)
	if err != nil {
		return errors.New("migration helper failed")
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return errors.New("migration helper failed")
	}
	return nil
}

// Execute performs one non-overlapping monitor invocation. It is safe to call
// again after a restart: mutation states are terminal rather than replayed.
func Execute(ctx context.Context, request Request, options Options) (Result, error) {
	if err := validateRequest(request, options.AllowInsecureHTTP); err != nil {
		return Result{}, err
	}
	options = completeOptions(options)
	if request.Action == "cleanup" {
		return executeCleanup(options, request.JobID)
	}
	if err := ensureRuntimeParents(options.Paths); err != nil {
		return Result{}, errStateUnavailable
	}
	fingerprint := requestFingerprint(request)
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		if filesync.IsLockBusy(err) {
			return Result{JobID: request.JobID, Status: "locked", StopCron: false}, nil
		}
		return Result{}, errStateUnavailable
	}
	defer lock.Close()

	now := options.Now().UTC()
	state, exists, err := loadState(options.Paths.State)
	if err != nil {
		return Result{}, errStateUnavailable
	}
	if !exists {
		state = migrationState{
			Version:     stateVersion,
			JobID:       request.JobID,
			Fingerprint: fingerprint,
			Origin:      request.WebOrigin,
			Target:      request.Target,
			Status:      statusMonitoring,
		}
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	} else if state.JobID != request.JobID || state.Fingerprint != fingerprint || state.Origin != request.WebOrigin || !sameTarget(state.Target, request.Target) {
		return Result{}, errInvalidInput
	}

	base := Result{JobID: state.JobID, Attempt: state.Attempt, ServerID: state.ServerID}
	if state.Expires > 0 && now.Unix() >= state.Expires {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	if state.Status == statusTerminal {
		base.Status, base.StopCron = statusTerminal, true
		return base, nil
	}
	if state.Status == statusCreated {
		if state.Expires <= 0 {
			base = machineTerminal(options, state, base, errTerminal)
			return base, nil
		}
		base.Status, base.StopCron = statusCreated, true
		return base, nil
	}
	if state.Status == statusWebRegistering || state.Status == statusProviderKeyRegister || state.Status == statusCreating {
		base.Status, base.StopCron = statusTerminal, true
		_ = markTerminal(options.Paths.State, state, "interrupted mutation")
		return base, nil
	}

	machine := monitorMachine{request: request, options: options, now: now}
	if state.Status == statusMonitoring {
		availability, ready, checkErr := machine.checkAvailability(ctx, request.Target)
		if checkErr != nil {
			base = machine.terminalResult(state, base, checkErr)
			return base, nil
		}
		if !ready {
			base.Status = statusMonitoring
			return base, nil
		}
		state.SelectedLocationID = availability.LocationID
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
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
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

	if state.Status == statusKeyRegistered && !state.HandoffReady {
		handoff := buildHandoff(state)
		identity, stageErr := protectedfile.WriteExclusive(options.Paths.Handoff, []byte(handoff))
		clear([]byte(handoff))
		if stageErr != nil {
			base = machine.terminalResult(state, base, stageErr)
			return base, nil
		}
		state.HandoffIdentity = identity
		state.HandoffReady = true
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	}
	if state.Status == statusKeyRegistered && state.HandoffReady {
		content, readErr := protectedfile.Read(options.Paths.Handoff, stateMaxBytes)
		if readErr == nil && content.Identity != state.HandoffIdentity {
			base = machine.terminalResult(state, base, errTerminal)
			return base, nil
		}
		if readErr != nil {
			if _, statErr := os.Lstat(options.Paths.Handoff); !errors.Is(statErr, os.ErrNotExist) {
				base = machine.terminalResult(state, base, errTerminal)
				return base, nil
			}
		}
	}

	if state.Status != statusKeyRegistered || state.ProviderKeyID <= 0 || !state.HandoffReady || state.SelectedLocationID <= 0 {
		base = machine.terminalResult(state, base, errTerminal)
		return base, nil
	}
	if state.Expires <= machine.currentUnix() {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	availability, ready, checkErr := machine.checkAvailability(ctx, request.Target)
	if checkErr != nil || !ready {
		base = machine.terminalResult(state, base, errTargetChanged)
		return base, nil
	}
	state.SelectedLocationID = availability.LocationID
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
	createCtx, cancel := createContext(ctx, state.Expires, machine.currentUnix())
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
		base.Status, base.StopCron, base.ServerID = statusTerminal, true, serverID
		return base, nil
	}
	return Result{JobID: state.JobID, Status: statusCreated, Attempt: state.Attempt, ServerID: serverID, StopCron: true}, nil
}

type migrationState struct {
	Version            int                    `json:"version"`
	JobID              string                 `json:"job_id"`
	Fingerprint        string                 `json:"fingerprint"`
	Origin             string                 `json:"origin"`
	Target             Target                 `json:"target"`
	Status             string                 `json:"status"`
	Attempt            string                 `json:"attempt,omitempty"`
	CallbackSecret     string                 `json:"callback_secret,omitempty"`
	OwnerSecret        string                 `json:"owner_secret,omitempty"`
	ServerLabel        string                 `json:"server_label,omitempty"`
	Expires            int64                  `json:"expires,omitempty"`
	PublicKey          string                 `json:"public_key,omitempty"`
	PrivateKey         string                 `json:"private_key,omitempty"`
	ProviderKeyID      int64                  `json:"provider_key_id,omitempty"`
	SelectedLocationID int64                  `json:"selected_location_id,omitempty"`
	HandoffReady       bool                   `json:"handoff_ready,omitempty"`
	HandoffIdentity    protectedfile.Identity `json:"handoff_identity,omitempty"`
	ServerID           int64                  `json:"server_id,omitempty"`
	TerminalReason     string                 `json:"terminal_reason,omitempty"`
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
	request Request
	options Options
	now     time.Time
}

type providerAvailability struct {
	LocationID   int64
	LocationName string
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

type providerImage struct {
	ID           int64           `json:"id"`
	Status       string          `json:"status"`
	Type         string          `json:"type"`
	Name         string          `json:"name"`
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
	if !safeIdentifier(request.JobID, 256) {
		return errInvalidInput
	}
	if request.Action == "cleanup" {
		if request.WebOrigin != "" || request.ProviderToken != "" || !emptyTarget(request.Target) {
			return errInvalidInput
		}
		return nil
	}
	if request.Action != "monitor" || request.ProviderToken == "" || strings.ContainsAny(request.ProviderToken, "\r\n") {
		return errInvalidInput
	}
	origin, err := normalizeOrigin(request.WebOrigin, allowInsecure)
	if err != nil || origin != request.WebOrigin {
		return errInvalidInput
	}
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	return nil
}

func emptyTarget(target Target) bool {
	return target.ServerTypeID == 0 && target.ServerTypeName == "" && target.Architecture == "" && target.ImageID == 0 && target.ImageName == "" && len(target.Locations) == 0 && len(target.NetworkIDs) == 0 && !target.EnableIPv4 && !target.EnableIPv6
}

func executeCleanup(options Options, jobID string) (Result, error) {
	if err := ensureRuntimeParents(options.Paths); err != nil {
		return Result{}, errStateUnavailable
	}
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		if filesync.IsLockBusy(err) {
			return Result{JobID: jobID, Status: "locked", StopCron: false}, nil
		}
		return Result{}, errStateUnavailable
	}
	defer lock.Close()
	state, exists, err := loadState(options.Paths.State)
	if err != nil {
		return Result{}, errStateUnavailable
	}
	if !exists {
		return Result{JobID: jobID, Status: "not_found", StopCron: false}, nil
	}
	if state.JobID != jobID {
		return Result{}, errInvalidInput
	}
	base := Result{JobID: state.JobID, Status: state.Status, Attempt: state.Attempt, ServerID: state.ServerID}
	if state.Expires > 0 && options.Now().UTC().Unix() >= state.Expires {
		return expireState(options.Paths.State, options.Paths.Handoff, state, base)
	}
	if state.Status == statusTerminal || state.Status == statusCreated {
		base.StopCron = true
	}
	return base, nil
}

func validateTarget(target Target) error {
	if target.ServerTypeID <= 0 || !safeIdentifier(target.ServerTypeName, 128) || !safeIdentifier(target.Architecture, 32) || target.ImageID <= 0 || !safeIdentifier(target.ImageName, 256) || len(target.Locations) == 0 || !target.EnableIPv4 {
		return errInvalidInput
	}
	seenLocations := make(map[int64]bool, len(target.Locations))
	for _, location := range target.Locations {
		if location.ID <= 0 || !safeIdentifier(location.Name, 128) || !safeIdentifier(location.HourlyGross, 64) || seenLocations[location.ID] {
			return errInvalidInput
		}
		seenLocations[location.ID] = true
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

func safeIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n\t")
}

func normalizeOrigin(value string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.ContainsAny(value, "\x00\r\n\t") {
		return "", errInvalidInput
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		if !allowInsecure || !strings.EqualFold(parsed.Scheme, "http") {
			return "", errInvalidInput
		}
	}
	return strings.TrimSuffix(value, "/"), nil
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
	if options.HTTPClient == nil {
		options.HTTPClient = defaults.HTTPClient
	}
	if options.Now == nil {
		options.Now = defaults.Now
	}
	return options
}

func requestFingerprint(request Request) string {
	value, _ := json.Marshal(struct {
		JobID  string `json:"job_id"`
		Origin string `json:"origin"`
		Target Target `json:"target"`
	}{request.JobID, request.WebOrigin, request.Target})
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
		if err := os.Mkdir(missing[index], 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return errStateUnavailable
		}
		if err := os.Chmod(missing[index], 0700); err != nil {
			return errStateUnavailable
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
	if state.Version != stateVersion || !safeIdentifier(state.JobID, 256) || len(state.Fingerprint) != sha256.Size*2 || strings.ToLower(state.Fingerprint) != state.Fingerprint || !safeIdentifier(state.Origin, 512) || !safeIdentifier(state.Status, 64) {
		return false
	}
	switch state.Status {
	case statusMonitoring, statusWebRegistering, statusRegistered, statusProviderKeyRegister, statusKeyRegistered, statusCreating, statusCreated, statusTerminal:
	default:
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
	temporary, err := os.CreateTemp(filepath.Dir(path), ".goaster-migration-state-*")
	if err != nil {
		return errStateUnavailable
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		temporary.Close()
		if !ok {
			os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return errStateUnavailable
	}
	if _, err := temporary.Write(data); err != nil || temporary.Sync() != nil || temporary.Close() != nil {
		return errStateUnavailable
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errStateUnavailable
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil || directory.Sync() != nil {
		if directory != nil {
			directory.Close()
		}
		return errStateUnavailable
	}
	directory.Close()
	ok = true
	return nil
}

func markTerminal(path string, state migrationState, reason string) error {
	state.Status = statusTerminal
	state.TerminalReason = "operation_failed"
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
	state.Status = statusTerminal
	state.TerminalReason = safeReason(reason)
	_ = saveState(m.options.Paths.State, state)
	base.Status, base.StopCron = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base
}

func (m monitorMachine) currentUnix() int64 {
	return m.options.Now().UTC().Unix()
}

func machineTerminal(options Options, state migrationState, base Result, reason error) Result {
	state.Status = statusTerminal
	state.TerminalReason = safeReason(reason)
	_ = saveState(options.Paths.State, state)
	base.Status, base.StopCron = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base
}

func expireState(statePath, handoffPath string, state migrationState, base Result) (Result, error) {
	if state.HandoffReady {
		if err := protectedfile.RemoveIfSame(handoffPath, state.HandoffIdentity); err != nil {
			state.TerminalReason = "handoff_replaced"
		} else {
			state.HandoffReady = false
			state.HandoffIdentity = protectedfile.Identity{}
			state.TerminalReason = "expired"
		}
	} else {
		state.TerminalReason = "expired"
	}
	state.CallbackSecret = ""
	state.OwnerSecret = ""
	state.PrivateKey = ""
	state.Status = statusTerminal
	_ = saveState(statePath, state)
	base.Status, base.StopCron = statusTerminal, true
	base.Attempt, base.ServerID = state.Attempt, state.ServerID
	return base, nil
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
		return context.WithCancel(parent)
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
	publicKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + "\n"
	return publicKey, string(privatePEM), nil
}

func buildHandoff(state migrationState) string {
	return ownerURL(state.Origin, state.Attempt, state.OwnerSecret) + "\n" + state.PrivateKey
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
	status, response, requestErr := m.jsonRequest(ctx, m.request.WebOrigin+"/migration/register", http.MethodPost, "", "", "application/json", body, webResponseMaxBytes)
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
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validGrant(value, m.now, m.request.WebOrigin) {
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
	status, body, err := m.jsonRequest(ctx, m.request.WebOrigin+"/migration/status", http.MethodPost, state.Attempt, state.OwnerSecret, "application/json", []byte("{}"), webResponseMaxBytes)
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

func (m monitorMachine) checkAvailability(ctx context.Context, target Target) (providerAvailability, bool, error) {
	serverType, err := m.providerGET(ctx, "/server_types/"+strconv.FormatInt(target.ServerTypeID, 10), providerResponseMaxBytes)
	if err != nil {
		return providerAvailability{}, false, errRemoteUnavailable
	}
	var typeResponse struct {
		ServerType providerServerType `json:"server_type"`
	}
	if decodeJSON(serverType, &typeResponse) != nil {
		return providerAvailability{}, false, errRemoteMalformed
	}
	value := typeResponse.ServerType
	if value.ID != target.ServerTypeID || value.Name != target.ServerTypeName || value.Architecture != target.Architecture || value.Deprecated || present(value.Deprecation) {
		return providerAvailability{}, false, errTargetChanged
	}
	priceByLocation := make(map[string]string, len(value.Prices))
	for _, price := range value.Prices {
		priceByLocation[price.Location] = price.PriceHourly.Gross
	}
	priceMismatch := false
	for _, approved := range target.Locations {
		for _, location := range value.Locations {
			if location.ID != approved.ID || location.Name != approved.Name {
				continue
			}
			if !location.Available || present(location.Deprecation) {
				break
			}
			if priceByLocation[location.Name] != approved.HourlyGross {
				priceMismatch = true
				break
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
			if image.ID != target.ImageID || image.Name != target.ImageName || image.Status != "available" || image.Type != "system" || image.Architecture != target.Architecture || present(image.Deprecated) || present(image.Deleted) {
				return providerAvailability{}, false, errTargetChanged
			}
			return providerAvailability{LocationID: approved.ID, LocationName: approved.Name}, true, nil
		}
	}
	if priceMismatch {
		return providerAvailability{}, false, errTargetChanged
	}
	return providerAvailability{}, false, nil
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
	locationID := state.SelectedLocationID
	userData := callbackUserData(state.Origin, state.Attempt, state.CallbackSecret)
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
	}{state.ServerLabel, state.Target.ServerTypeID, state.Target.ImageID, []int64{state.ProviderKeyID}, strconv.FormatInt(locationID, 10), userData, true, map[string]string{"goaster-migration-attempt": state.Attempt}, append([]int64(nil), state.Target.NetworkIDs...), publicNet{EnableIPv4: state.Target.EnableIPv4, EnableIPv6: state.Target.EnableIPv6}})
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

func callbackUserData(origin, attempt, callbackSecret string) string {
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
		"url = \"" + origin + "/migration/host-key\"\n" +
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
	status, body, err := m.requestJSON(ctx, strings.TrimSuffix(m.options.ProviderEndpoint, "/")+endpoint, http.MethodGet, "", "", "", nil, maxBytes)
	if err != nil || status != http.StatusOK {
		clear(body)
		return nil, errRemoteUnavailable
	}
	return body, nil
}

func (m monitorMachine) providerJSON(ctx context.Context, endpoint string, body []byte, maxBytes int64) (int, []byte, error) {
	return m.requestJSON(ctx, strings.TrimSuffix(m.options.ProviderEndpoint, "/")+endpoint, http.MethodPost, "", m.request.ProviderToken, "application/json", body, maxBytes)
}

func (m monitorMachine) jsonRequest(ctx context.Context, endpoint, method, attempt, secret, contentType string, body []byte, maxBytes int64) (int, []byte, error) {
	return m.requestJSON(ctx, endpoint, method, attempt, secret, contentType, body, maxBytes)
}

func (m monitorMachine) requestJSON(ctx context.Context, endpoint, method, attempt, secret, contentType string, body []byte, maxBytes int64) (int, []byte, error) {
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
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if method == http.MethodGet || strings.Contains(endpoint, "/servers") || strings.Contains(endpoint, "/ssh_keys") || strings.Contains(endpoint, "/networks/") {
		req.Header.Set("Authorization", "Bearer "+m.request.ProviderToken)
	}
	response, err := m.options.HTTPClient.Do(req)
	if err != nil {
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
