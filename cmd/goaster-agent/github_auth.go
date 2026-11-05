package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"goaster/internal/chat"
	"goaster/internal/command"
)

const (
	githubAuthToolName          = "auth_handoff"
	githubAuthProvider          = "github"
	githubHost                  = "github.com"
	githubDevicePath            = "/login/device/code"
	githubTokenPath             = "/login/oauth/access_token"
	githubDeviceVerification    = "https://github.com/login/device"
	githubDefaultAuthBase       = "https://github.com"
	githubDefaultAPIBase        = "https://api.github.com"
	githubClientIDEnv           = "GOASTER_GITHUB_CLIENT_ID"
	githubAuthControlHeader     = "X-GoAster-GitHub-Auth-Control"
	githubDefaultConsentTTL     = 5 * time.Minute
	githubDefaultDeviceTTL      = 15 * time.Minute
	githubDefaultHTTPTimeout    = 30 * time.Second
	githubDefaultCleanupTimeout = 30 * time.Second
	githubMaxResponseBytes      = 64 * 1024
	githubScope                 = "repo read:org gist"
)

const (
	githubAuthAuthorizationGranted = "GitHub authentication is authorized for this private Telegram chat for a short time. Ask GoAster to continue the GitHub workflow."
	githubAuthAuthorizationNeeded  = "GitHub authentication was not started because this private Telegram chat has not explicitly authorized it. Send /github-login first."
	githubAuthAlreadyRunning       = "GitHub authentication is already in progress for this chat."
	githubAuthNotConfigured        = "GitHub authentication was not started because the supported GitHub provider is not configured."
	githubAuthCompleted            = "GitHub authentication completed and the GitHub account was verified."
	githubAuthCanceled             = "GitHub authentication was canceled and cleanup completed."
	githubAuthCancellationPending  = "GitHub authentication cancellation was requested. Cleanup is pending; wait for the final status."
	githubAuthNoPending            = "GitHub authentication has no pending handoff to cancel."
	githubAuthExpired              = "GitHub authentication expired. Send /github-login again for a new handoff."
	githubAuthFailed               = "GitHub authentication could not be completed. No account was verified."
	githubAuthReconciliationNeeded = "GitHub authentication could not be safely rolled back. Manually reconcile the GitHub account before retrying."
	githubAuthBlocked              = "GitHub authentication is blocked pending manual GitHub credential reconciliation."
	githubAuthReconciled           = "GitHub credential reconciliation was acknowledged. Send /github-login to authorize a new handoff."
	githubAuthNoReconciliation     = "GitHub authentication has no pending reconciliation."
)

var (
	errGitHubAuthExpired              = errors.New("github authentication expired")
	errGitHubAuthCleanup              = errors.New("github credential cleanup failed")
	errGitHubCredentialAlreadyPresent = errors.New("github account already has stored credentials")
)

type githubAuthCommand func(context.Context, string) error
type githubAccountVerifier func(context.Context) (string, error)
type githubTokenVerifier func(context.Context, string) (string, error)
type githubCredentialRollback func(context.Context, string) error
type githubCredentialInstaller func(context.Context, string, string, func() error) (githubCredentialRollback, error)
type githubCredentialSnapshotter func(context.Context) (githubCredentialSnapshot, error)
type githubCredentialAccountLister func(context.Context) ([]string, error)
type githubCredentialInstall func(context.Context, string) error
type githubCredentialLogout func(context.Context, string) error
type githubCredentialAccountVerifier func(context.Context) (string, error)

type githubAuthConfig struct {
	ClientID           string
	HTTPClient         http.Client
	AuthBaseURL        string
	APIBaseURL         string
	Now                func() time.Time
	Sleep              func(context.Context, time.Duration) error
	ConsentTimeout     time.Duration
	DeviceTimeout      time.Duration
	InstallToken       githubAuthCommand
	InstallCredential  githubCredentialInstaller
	RollbackCredential githubCredentialRollback
	SnapshotCredential githubCredentialSnapshotter
	ListAccounts       githubCredentialAccountLister
	VerifyAccount      githubAccountVerifier
	VerifyToken        githubTokenVerifier
}

// githubAuthManager is the only stateful authentication handoff exposed by
// the remote agent. Its callbacks keep the device code out of model results.
type githubAuthManager struct {
	authorizeFunc func(context.Context, int64) string
	startFunc     func(context.Context, int64, chat.AuthHandoffObserver) string
	cancelFunc    func(context.Context, int64) string
	reconcileFunc func(context.Context, int64) string
}

func (manager githubAuthManager) authorize(ctx context.Context, chatID int64) string {
	if manager.authorizeFunc == nil {
		return githubAuthFailed
	}
	return manager.authorizeFunc(ctx, chatID)
}

func (manager githubAuthManager) start(ctx context.Context, chatID int64, observer chat.AuthHandoffObserver) string {
	if manager.startFunc == nil {
		return githubAuthNotConfigured
	}
	return manager.startFunc(ctx, chatID, observer)
}

func (manager githubAuthManager) cancel(ctx context.Context, chatID int64) string {
	if manager.cancelFunc == nil {
		return githubAuthNoPending
	}
	return manager.cancelFunc(ctx, chatID)
}

func (manager githubAuthManager) reconcile(ctx context.Context, chatID int64) string {
	if manager.reconcileFunc == nil {
		return githubAuthNoReconciliation
	}
	return manager.reconcileFunc(ctx, chatID)
}

type githubDeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time
	Interval        time.Duration
}

type githubAuthFlow struct {
	cancel    context.CancelFunc
	canceled  bool
	completed bool
}

func newGitHubAuthManager(config githubAuthConfig) githubAuthManager {
	clientID := strings.TrimSpace(config.ClientID)
	client := config.HTTPClient
	if client.Timeout <= 0 {
		client.Timeout = githubDefaultHTTPTimeout
	}
	authBase := strings.TrimRight(strings.TrimSpace(config.AuthBaseURL), "/")
	if authBase == "" {
		authBase = githubDefaultAuthBase
	}
	apiBase := strings.TrimRight(strings.TrimSpace(config.APIBaseURL), "/")
	if apiBase == "" {
		apiBase = githubDefaultAPIBase
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	consentTTL := config.ConsentTimeout
	if consentTTL <= 0 {
		consentTTL = githubDefaultConsentTTL
	}
	deviceTTL := config.DeviceTimeout
	if deviceTTL <= 0 {
		deviceTTL = githubDefaultDeviceTTL
	}
	snapshotCredential := config.SnapshotCredential
	if snapshotCredential == nil && config.ListAccounts != nil {
		snapshotCredential = func(ctx context.Context) (githubCredentialSnapshot, error) {
			return snapshotGitHubCredentialWithAccountLister(ctx, config.ListAccounts)
		}
	}
	if snapshotCredential == nil {
		snapshotCredential = snapshotGitHubCredential
	}
	guardSnapshot := config.SnapshotCredential
	if guardSnapshot == nil && config.ListAccounts != nil {
		guardSnapshot = func(ctx context.Context) (githubCredentialSnapshot, error) {
			accounts, err := config.ListAccounts(ctx)
			if err != nil {
				return githubCredentialSnapshot{}, err
			}
			return githubCredentialSnapshot{accounts: accounts}, nil
		}
	}
	installToken := config.InstallToken
	installCredential := config.InstallCredential
	if installCredential == nil {
		if installToken != nil {
			rollback := config.RollbackCredential
			if rollback == nil {
				rollback = func(context.Context, string) error {
					return errGitHubAuthCleanup
				}
			}
			installCredential = func(ctx context.Context, token, account string, beforeInstall func() error) (githubCredentialRollback, error) {
				if guardSnapshot != nil {
					snapshot, err := guardSnapshot(ctx)
					if err != nil {
						return nil, err
					}
					if snapshot.hasAccount(account) {
						return nil, errGitHubCredentialAlreadyPresent
					}
				}
				if err := checkBeforeGitHubInstall(beforeInstall); err != nil {
					return nil, err
				}
				if err := installToken(ctx, token); err != nil {
					return nil, err
				}
				return rollback, nil
			}
		} else {
			installCredential = func(ctx context.Context, token, account string, beforeInstall func() error) (githubCredentialRollback, error) {
				return installGitHubCredentialWithCallbacks(ctx, token, account, beforeInstall, snapshotCredential, installGitHubToken, logoutGitHubCredential, verifyGitHubCLIAccount)
			}
		}
	}
	if config.InstallCredential != nil && guardSnapshot != nil {
		originalInstallCredential := installCredential
		installCredential = func(ctx context.Context, token, account string, beforeInstall func() error) (githubCredentialRollback, error) {
			snapshot, err := guardSnapshot(ctx)
			if err != nil {
				return nil, err
			}
			if snapshot.hasAccount(account) {
				return nil, errGitHubCredentialAlreadyPresent
			}
			return originalInstallCredential(ctx, token, account, beforeInstall)
		}
	}
	verifyToken := config.VerifyToken
	if verifyToken == nil {
		verifyToken = func(ctx context.Context, token string) (string, error) {
			return verifyGitHubToken(ctx, client, apiBase, token)
		}
	}
	verifyAccount := config.VerifyAccount
	if verifyAccount == nil {
		verifyAccount = verifyGitHubCLIAccount
	}

	var stateMu sync.Mutex
	authorizations := make(map[int64]time.Time)
	active := make(map[int64]githubAuthFlow)
	blocked := make(map[int64]bool)

	authorize := func(ctx context.Context, chatID int64) string {
		if ctx == nil || ctx.Err() != nil || chatID <= 0 {
			return githubAuthAuthorizationNeeded
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		if blocked[chatID] {
			return githubAuthBlocked
		}
		if _, running := active[chatID]; running {
			return githubAuthAlreadyRunning
		}
		authorizations[chatID] = now().Add(consentTTL)
		return githubAuthAuthorizationGranted
	}

	cancel := func(ctx context.Context, chatID int64) string {
		if chatID <= 0 {
			return githubAuthNoPending
		}
		stateMu.Lock()
		flow, running := active[chatID]
		if !running {
			if blocked[chatID] {
				stateMu.Unlock()
				return githubAuthBlocked
			}
			delete(authorizations, chatID)
			stateMu.Unlock()
			return githubAuthNoPending
		}
		if flow.completed {
			stateMu.Unlock()
			return githubAuthNoPending
		}
		if ctx == nil || ctx.Err() != nil {
			stateMu.Unlock()
			return githubAuthNoPending
		}
		if flow.canceled {
			stateMu.Unlock()
			return githubAuthCancellationPending
		}
		flow.canceled = true
		active[chatID] = flow
		delete(authorizations, chatID)
		stateMu.Unlock()
		flow.cancel()
		return githubAuthCancellationPending
	}

	reconcile := func(ctx context.Context, chatID int64) string {
		if ctx == nil || ctx.Err() != nil || chatID <= 0 {
			return githubAuthNoReconciliation
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		if _, running := active[chatID]; running {
			return githubAuthAlreadyRunning
		}
		if !blocked[chatID] {
			return githubAuthNoReconciliation
		}
		delete(blocked, chatID)
		return githubAuthReconciled
	}

	start := func(ctx context.Context, chatID int64, observer chat.AuthHandoffObserver) string {
		if ctx == nil || ctx.Err() != nil || chatID <= 0 {
			return githubAuthAuthorizationNeeded
		}

		stateMu.Lock()
		if blocked[chatID] {
			stateMu.Unlock()
			return githubAuthBlocked
		}
		expiresAt, authorized := authorizations[chatID]
		if !authorized {
			stateMu.Unlock()
			return githubAuthAuthorizationNeeded
		}
		delete(authorizations, chatID)
		if !now().Before(expiresAt) {
			stateMu.Unlock()
			return githubAuthExpired
		}
		if _, running := active[chatID]; running {
			stateMu.Unlock()
			return githubAuthAlreadyRunning
		}
		if clientID == "" || strings.ContainsAny(clientID, "\r\n") {
			stateMu.Unlock()
			return githubAuthNotConfigured
		}
		flowContext, cancelFlow := context.WithCancel(ctx)
		active[chatID] = githubAuthFlow{cancel: cancelFlow}
		stateMu.Unlock()

		installForFlow := func(installContext context.Context, token, account string, _ func() error) (githubCredentialRollback, error) {
			stateMu.Lock()
			flow, running := active[chatID]
			if !running || flow.canceled {
				stateMu.Unlock()
				return nil, context.Canceled
			}
			if installContext.Err() != nil {
				stateMu.Unlock()
				return nil, installContext.Err()
			}
			if !now().Before(expiresAt) {
				stateMu.Unlock()
				return nil, errGitHubAuthExpired
			}
			stateMu.Unlock()

			beforeInstall := func() error {
				stateMu.Lock()
				defer stateMu.Unlock()
				flow, running := active[chatID]
				if !running || flow.canceled {
					return context.Canceled
				}
				if err := installContext.Err(); err != nil {
					return err
				}
				if !now().Before(expiresAt) {
					return errGitHubAuthExpired
				}
				return nil
			}
			rollback, err := installCredential(installContext, token, account, beforeInstall)
			if err != nil {
				return nil, err
			}

			stateMu.Lock()
			flow, running = active[chatID]
			canceled := !running || flow.canceled || installContext.Err() != nil
			expired := !now().Before(expiresAt)
			stateMu.Unlock()
			if canceled || expired {
				if err := rollbackGitHubCredential(rollback, account); err != nil {
					return nil, err
				}
				if canceled {
					return nil, context.Canceled
				}
				return nil, errGitHubAuthExpired
			}
			return rollback, nil
		}

		completeFlow := func() error {
			stateMu.Lock()
			defer stateMu.Unlock()
			flow, running := active[chatID]
			if !running || flow.canceled {
				return context.Canceled
			}
			if err := flowContext.Err(); err != nil {
				return err
			}
			if !now().Before(expiresAt) {
				return errGitHubAuthExpired
			}
			flow.completed = true
			active[chatID] = flow
			return nil
		}

		result := performGitHubAuth(flowContext, clientID, authBase, client, now, sleep, deviceTTL, installForFlow, verifyToken, verifyAccount, completeFlow, observer)
		cancelFlow()
		stateMu.Lock()
		if result == githubAuthReconciliationNeeded {
			blocked[chatID] = true
			delete(authorizations, chatID)
		}
		delete(active, chatID)
		stateMu.Unlock()
		return result
	}

	return githubAuthManager{authorizeFunc: authorize, startFunc: start, cancelFunc: cancel, reconcileFunc: reconcile}
}

func performGitHubAuth(ctx context.Context, clientID, authBase string, client http.Client, now func() time.Time, sleep func(context.Context, time.Duration) error, deviceTTL time.Duration, installCredential githubCredentialInstaller, verifyToken githubTokenVerifier, verifyAccount githubAccountVerifier, complete func() error, observer chat.AuthHandoffObserver) string {
	device, err := requestGitHubDeviceCode(ctx, client, authBase, clientID, now(), deviceTTL)
	if err != nil {
		return githubAuthFailureResult(observer, ctx, err, githubAuthFailed)
	}
	notifyGitHubAuth(observer, chat.AuthHandoffNotice{
		Provider:        githubAuthProvider,
		Status:          "started",
		VerificationURL: device.VerificationURL,
		UserCode:        device.UserCode,
		ExpiresAt:       device.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})

	token, pollStatus := pollGitHubDeviceCode(ctx, client, authBase, clientID, device, now, sleep)
	if pollStatus != "completed" {
		status := pollStatus
		message := githubAuthFailed
		switch pollStatus {
		case "canceled":
			if errors.Is(ctx.Err(), context.Canceled) {
				notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: "cancellation_requested"})
			}
			message = githubAuthCanceled
		case "expired":
			message = githubAuthExpired
			status = "expired"
		default:
			status = "failed"
		}
		notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: status})
		return message
	}
	if err := checkGitHubAuthWindow(ctx, now, device.ExpiresAt); err != nil {
		return githubAuthFailureResult(observer, ctx, err, githubAuthFailed)
	}

	account, err := verifyToken(ctx, token)
	if err != nil {
		return githubAuthFailureResult(observer, ctx, err, githubAuthFailed)
	}
	account, err = safeGitHubLogin(account)
	if err != nil {
		notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: "failed"})
		return githubAuthFailed
	}
	if err := checkGitHubAuthWindow(ctx, now, device.ExpiresAt); err != nil {
		return githubAuthFailureResult(observer, ctx, err, githubAuthFailed)
	}
	rollback, err := installCredential(ctx, token, account, nil)
	if err != nil {
		return githubAuthFailureResult(observer, ctx, err, githubAuthFailed)
	}
	failAfterInstall := func(failure error) string {
		if errors.Is(failure, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: "cancellation_requested"})
		}
		if err := rollbackGitHubCredential(rollback, account); err != nil {
			return githubAuthReconciliationResult(observer)
		}
		return githubAuthFailureResult(observer, ctx, failure, githubAuthFailed)
	}
	if err := checkGitHubAuthWindow(ctx, now, device.ExpiresAt); err != nil {
		return failAfterInstall(err)
	}
	verifiedAccount, err := verifyAccount(ctx)
	if err != nil {
		return failAfterInstall(err)
	}
	verifiedAccount, err = safeGitHubLogin(verifiedAccount)
	if windowErr := checkGitHubAuthWindow(ctx, now, device.ExpiresAt); windowErr != nil {
		return failAfterInstall(windowErr)
	}
	if err != nil || !strings.EqualFold(account, verifiedAccount) {
		if err == nil {
			err = errors.New("github account verification did not match the device token")
		}
		return failAfterInstall(err)
	}
	if windowErr := checkGitHubAuthWindow(ctx, now, device.ExpiresAt); windowErr != nil {
		return failAfterInstall(windowErr)
	}
	if complete != nil {
		if err := complete(); err != nil {
			return failAfterInstall(err)
		}
	}
	notifyGitHubAuth(observer, chat.AuthHandoffNotice{
		Provider:        githubAuthProvider,
		Status:          "completed",
		VerifiedAccount: verifiedAccount,
	})
	return githubAuthCompleted
}

func checkGitHubAuthWindow(ctx context.Context, now func() time.Time, expiresAt time.Time) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if !now().Before(expiresAt) {
		return errGitHubAuthExpired
	}
	return nil
}

func rollbackGitHubCredential(rollback githubCredentialRollback, account string) error {
	if rollback == nil {
		return errGitHubAuthCleanup
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), githubDefaultCleanupTimeout)
	defer cancel()
	if err := rollback(cleanupContext, account); err != nil {
		return errors.Join(errGitHubAuthCleanup, err)
	}
	return nil
}

func installGitHubCredential(ctx context.Context, token, account string, beforeInstall func() error) (githubCredentialRollback, error) {
	return installGitHubCredentialWithCallbacks(ctx, token, account, beforeInstall, snapshotGitHubCredential, installGitHubToken, logoutGitHubCredential, verifyGitHubCLIAccount)
}

func installGitHubCredentialWithCallbacks(ctx context.Context, token, account string, beforeInstall func() error, snapshotCredential githubCredentialSnapshotter, installCredential githubCredentialInstall, logoutCredential githubCredentialLogout, verifyAccount githubCredentialAccountVerifier) (githubCredentialRollback, error) {
	if snapshotCredential == nil || installCredential == nil || logoutCredential == nil || verifyAccount == nil {
		return nil, errGitHubAuthCleanup
	}
	snapshot, err := snapshotCredential(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.hasAccount(account) {
		return nil, errGitHubCredentialAlreadyPresent
	}
	if err := checkBeforeGitHubInstall(beforeInstall); err != nil {
		return nil, err
	}
	if err := installCredential(ctx, token); err != nil {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubDefaultCleanupTimeout)
		defer cancel()
		if cleanupErr := restoreGitHubCredentialWithCallbacks(cleanupContext, snapshot, account, installCredential, logoutCredential, verifyAccount); cleanupErr != nil {
			return nil, errors.Join(err, errGitHubAuthCleanup, cleanupErr)
		}
		return nil, err
	}
	return func(cleanupContext context.Context, installedAccount string) error {
		return restoreGitHubCredentialWithCallbacks(cleanupContext, snapshot, installedAccount, installCredential, logoutCredential, verifyAccount)
	}, nil
}

func checkBeforeGitHubInstall(beforeInstall func() error) error {
	if beforeInstall == nil {
		return nil
	}
	return beforeInstall()
}

type githubCredentialSnapshot struct {
	present  bool
	token    string
	account  string
	accounts []string
}

var errGitHubCredentialMissing = errors.New("github credential is not installed")

func snapshotGitHubCredential(ctx context.Context) (githubCredentialSnapshot, error) {
	return snapshotGitHubCredentialWithAccountLister(ctx, listGitHubAccounts)
}

func snapshotGitHubCredentialWithAccountLister(ctx context.Context, listAccounts githubCredentialAccountLister) (githubCredentialSnapshot, error) {
	if listAccounts == nil {
		return githubCredentialSnapshot{}, errors.New("existing GitHub accounts could not be inspected")
	}
	accounts, err := listAccounts(ctx)
	if err != nil {
		return githubCredentialSnapshot{}, errors.New("existing GitHub accounts could not be inspected")
	}
	accounts = append([]string(nil), accounts...)
	token, err := readGitHubToken(ctx)
	if errors.Is(err, errGitHubCredentialMissing) {
		return githubCredentialSnapshot{accounts: accounts}, nil
	}
	if err != nil {
		return githubCredentialSnapshot{}, errors.New("existing GitHub credential could not be inspected")
	}
	account, err := verifyGitHubCLIAccount(ctx)
	if err != nil {
		return githubCredentialSnapshot{}, errors.New("existing GitHub account could not be verified")
	}
	accounts = appendGitHubAccount(accounts, account)
	return githubCredentialSnapshot{present: true, token: token, account: account, accounts: accounts}, nil
}

func (snapshot githubCredentialSnapshot) hasAccount(account string) bool {
	account, err := safeGitHubLogin(account)
	if err != nil {
		return true
	}
	if snapshot.present && strings.EqualFold(snapshot.account, account) {
		return true
	}
	for _, existing := range snapshot.accounts {
		if strings.EqualFold(strings.TrimSpace(existing), account) {
			return true
		}
	}
	return false
}

func appendGitHubAccount(accounts []string, account string) []string {
	for _, existing := range accounts {
		if strings.EqualFold(strings.TrimSpace(existing), account) {
			return accounts
		}
	}
	return append(accounts, account)
}

func listGitHubAccounts(ctx context.Context) ([]string, error) {
	command := exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", githubHost, "--json", "hosts")
	command.Env = githubCommandEnvironment()
	output, err := command.CombinedOutput()
	if err == nil {
		return parseGitHubAccountStatusJSON(output)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("github CLI is unavailable")
	}
	if !githubStatusJSONUnsupported(output) {
		if noGitHubCredentialReported(output) {
			return nil, nil
		}
		return nil, errors.New("github account inspection failed")
	}

	// Older gh releases do not support --json on auth status. Their text
	// output still names every stored account, so retain a bounded fallback.
	command = exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", githubHost)
	command.Env = githubCommandEnvironment()
	output, err = command.CombinedOutput()
	if err != nil && noGitHubCredentialReported(output) {
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("github account inspection failed")
	}
	accounts := make([]string, 0, 1)
	marker := "Logged in to " + githubHost + " account "
	for _, line := range strings.Split(string(output), "\n") {
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(line[index+len(marker):])
		if end := strings.IndexAny(value, " (\t"); end >= 0 {
			value = value[:end]
		}
		account, accountErr := safeGitHubLogin(value)
		if accountErr != nil {
			return nil, errors.New("github account inspection returned an invalid account")
		}
		accounts = appendGitHubAccount(accounts, account)
	}
	if len(accounts) == 0 {
		return nil, errors.New("github account inspection returned no accounts")
	}
	return accounts, nil
}

func parseGitHubAccountStatusJSON(output []byte) ([]string, error) {
	var status struct {
		Hosts json.RawMessage `json:"hosts"`
	}
	if err := json.Unmarshal(output, &status); err != nil || strings.TrimSpace(string(status.Hosts)) == "" || strings.TrimSpace(string(status.Hosts)) == "null" {
		return nil, errors.New("github account inspection returned invalid JSON")
	}
	var hosts map[string][]struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(status.Hosts, &hosts); err != nil {
		return nil, errors.New("github account inspection returned invalid JSON")
	}
	accounts := make([]string, 0, 1)
	for host, entries := range hosts {
		if !strings.EqualFold(host, githubHost) {
			continue
		}
		for _, entry := range entries {
			account, err := safeGitHubLogin(entry.Login)
			if err != nil {
				return nil, errors.New("github account inspection returned an invalid account")
			}
			accounts = appendGitHubAccount(accounts, account)
		}
	}
	return accounts, nil
}

func githubStatusJSONUnsupported(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "unknown flag") && strings.Contains(message, "json")
}

func readGitHubToken(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", githubHost)
	command.Env = githubCommandEnvironment()
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("github CLI is unavailable")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && noGitHubCredentialReported(exitErr.Stderr) {
			return "", errGitHubCredentialMissing
		}
		return "", errors.New("github credential inspection failed")
	}
	token := strings.TrimSpace(string(output))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("existing GitHub credential was invalid")
	}
	return token, nil
}

func restoreGitHubCredential(cleanupContext context.Context, snapshot githubCredentialSnapshot, account string) error {
	return restoreGitHubCredentialWithCallbacks(cleanupContext, snapshot, account, installGitHubToken, logoutGitHubCredential, verifyGitHubCLIAccount)
}

func restoreGitHubCredentialWithCallbacks(cleanupContext context.Context, snapshot githubCredentialSnapshot, account string, installCredential githubCredentialInstall, logoutCredential githubCredentialLogout, verifyAccount githubCredentialAccountVerifier) error {
	account, err := safeGitHubLogin(account)
	if err != nil {
		return errors.Join(errGitHubAuthCleanup, errors.New("new GitHub account could not be removed"))
	}
	if installCredential == nil || logoutCredential == nil || verifyAccount == nil {
		return errors.Join(errGitHubAuthCleanup, errors.New("GitHub credential rollback is not configured"))
	}
	if snapshot.hasAccount(account) {
		return errors.Join(errGitHubAuthCleanup, errors.New("target GitHub account existed before installation; rollback refused to delete it"))
	}
	// gh auth logout --hostname/--user targets the named account even when it
	// is inactive. Remove it before restoring the previous account so the new
	// account cannot remain in gh's stored multi-account configuration.
	if err := logoutCredential(cleanupContext, account); err != nil {
		return errors.Join(errGitHubAuthCleanup, errors.New("new GitHub credential could not be removed"), err)
	}
	if snapshot.present {
		if err := installCredential(cleanupContext, snapshot.token); err == nil {
			restoredAccount, verifyErr := verifyAccount(cleanupContext)
			if verifyErr == nil && strings.EqualFold(snapshot.account, restoredAccount) {
				return nil
			}
		}
		return errors.Join(errGitHubAuthCleanup, errors.New("pre-existing GitHub credential could not be restored"))
	}
	return nil
}

func logoutGitHubCredential(ctx context.Context, account string) error {
	account, err := safeGitHubLogin(account)
	if err != nil {
		return errors.New("new GitHub account could not be removed")
	}
	command := exec.CommandContext(ctx, "gh", "auth", "logout", "--hostname", githubHost, "--user", account)
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = githubCommandEnvironment()
	if err := command.Run(); err != nil {
		return errors.New("new GitHub credential could not be removed")
	}
	return nil
}

func noGitHubCredentialReported(stderr []byte) bool {
	message := strings.ToLower(strings.TrimSpace(string(stderr)))
	if message == "no oauth token found for github.com" || message == "not logged into github.com" || message == "not logged into any github hosts" {
		return true
	}
	return strings.Contains(message, "not logged into any github hosts")
}

func requestGitHubDeviceCode(ctx context.Context, client http.Client, authBase, clientID string, startedAt time.Time, maxTTL time.Duration) (githubDeviceCode, error) {
	form := url.Values{"client_id": {clientID}, "scope": {githubScope}}
	status, body, err := githubRequest(ctx, client, http.MethodPost, authBase+githubDevicePath, form, "")
	if err != nil {
		return githubDeviceCode{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return githubDeviceCode{}, errors.New("github device authorization request failed")
	}
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return githubDeviceCode{}, errors.New("github device authorization response was invalid")
	}
	if strings.TrimSpace(response.DeviceCode) == "" || !validGitHubUserCode(response.UserCode) || response.ExpiresIn < 1 {
		return githubDeviceCode{}, errors.New("github device authorization response was incomplete")
	}
	verificationURL := strings.TrimSpace(response.VerificationURI)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(response.VerificationURL)
	}
	if verificationURL == "" {
		verificationURL = githubDeviceVerification
	}
	if !validGitHubVerificationURL(verificationURL) {
		return githubDeviceCode{}, errors.New("github verification URL was not allowlisted")
	}
	interval := time.Duration(response.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresAt := startedAt.Add(time.Duration(response.ExpiresIn) * time.Second)
	if maxTTL > 0 && expiresAt.After(startedAt.Add(maxTTL)) {
		expiresAt = startedAt.Add(maxTTL)
	}
	return githubDeviceCode{
		DeviceCode:      strings.TrimSpace(response.DeviceCode),
		UserCode:        strings.TrimSpace(response.UserCode),
		VerificationURL: verificationURL,
		ExpiresAt:       expiresAt,
		Interval:        interval,
	}, nil
}

func pollGitHubDeviceCode(ctx context.Context, client http.Client, authBase, clientID string, device githubDeviceCode, now func() time.Time, sleep func(context.Context, time.Duration) error) (string, string) {
	interval := device.Interval
	for {
		if ctx.Err() != nil {
			return "", "canceled"
		}
		if !now().Before(device.ExpiresAt) {
			return "", "expired"
		}
		form := url.Values{
			"client_id":   {clientID},
			"device_code": {device.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		status, body, err := githubRequest(ctx, client, http.MethodPost, authBase+githubTokenPath, form, "")
		if err != nil {
			if ctx.Err() != nil {
				return "", "canceled"
			}
			return "", "failed"
		}
		var response struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return "", "failed"
		}
		if status >= http.StatusOK && status < http.StatusMultipleChoices && strings.TrimSpace(response.AccessToken) != "" {
			token := strings.TrimSpace(response.AccessToken)
			if strings.ContainsAny(token, "\r\n") {
				return "", "failed"
			}
			if ctx.Err() != nil {
				return "", "canceled"
			}
			if !now().Before(device.ExpiresAt) {
				return "", "expired"
			}
			return token, "completed"
		}
		switch response.Error {
		case "authorization_pending":
			// Continue below after the provider-required delay.
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", "canceled"
		case "expired_token":
			return "", "expired"
		default:
			return "", "failed"
		}
		if err := sleep(ctx, interval); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", "canceled"
			}
			return "", "failed"
		}
	}
}

func verifyGitHubToken(ctx context.Context, client http.Client, apiBase, token string) (string, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("github access token was invalid")
	}
	status, body, err := githubRequest(ctx, client, http.MethodGet, apiBase+"/user", nil, token)
	if err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", errors.New("github account verification failed")
	}
	var response struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", errors.New("github account verification response was invalid")
	}
	return safeGitHubLogin(response.Login)
}

func githubRequest(ctx context.Context, client http.Client, method, endpoint string, form url.Values, token string) (int, []byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, nil, errors.New("github request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "GoAster")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, errors.New("github request failed")
	}
	if response.Body == nil {
		return response.StatusCode, nil, errors.New("github response had no body")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, githubMaxResponseBytes+1))
	if err != nil || len(data) > githubMaxResponseBytes {
		return response.StatusCode, nil, errors.New("github response was too large")
	}
	return response.StatusCode, data, nil
}

func notifyGitHubAuth(observer chat.AuthHandoffObserver, notice chat.AuthHandoffNotice) {
	if observer != nil {
		observer(notice)
	}
}

func githubAuthStatusForError(ctx context.Context, err error, fallback string) string {
	if errors.Is(err, errGitHubAuthCleanup) {
		return githubAuthReconciliationNeeded
	}
	if ctx != nil && ctx.Err() != nil {
		return githubAuthCanceled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return githubAuthCanceled
	}
	if errors.Is(err, errGitHubAuthExpired) {
		return githubAuthExpired
	}
	return fallback
}

func githubAuthFailureResult(observer chat.AuthHandoffObserver, ctx context.Context, err error, fallback string) string {
	result := githubAuthStatusForError(ctx, err, fallback)
	status := "failed"
	if result == githubAuthCanceled {
		status = "canceled"
	} else if result == githubAuthExpired {
		status = "expired"
	} else if result == githubAuthReconciliationNeeded {
		status = "reconciliation_required"
	}
	notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: status})
	return result
}

func githubAuthReconciliationResult(observer chat.AuthHandoffObserver) string {
	notifyGitHubAuth(observer, chat.AuthHandoffNotice{Provider: githubAuthProvider, Status: "reconciliation_required"})
	return githubAuthReconciliationNeeded
}

func validGitHubVerificationURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host == githubHost && parsed.Path == "/login/device" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}

func validGitHubUserCode(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' {
			continue
		}
		if character >= 'A' && character <= 'Z' {
			continue
		}
		if character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func safeGitHubLogin(value string) (string, error) {
	login := strings.TrimSpace(value)
	if len(login) == 0 || len(login) > 39 || strings.ContainsAny(login, "\r\n") {
		return "", errors.New("github account name was invalid")
	}
	for _, character := range login {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return "", errors.New("github account name was invalid")
	}
	return login, nil
}

func installGitHubToken(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("github access token was invalid")
	}
	command := exec.CommandContext(ctx, "gh", "auth", "login", "--hostname", githubHost, "--git-protocol", "https", "--with-token")
	command.Stdin = strings.NewReader(token + "\n")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = githubCommandEnvironment()
	if err := command.Run(); err != nil {
		return errors.New("github credential installation failed")
	}
	return nil
}

func verifyGitHubCLIAccount(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "gh", "api", "--hostname", githubHost, "user", "--jq", ".login")
	command.Stderr = io.Discard
	command.Env = githubCommandEnvironment()
	output, err := command.Output()
	if err != nil {
		return "", errors.New("github CLI account verification failed")
	}
	return safeGitHubLogin(string(output))
}

func githubCommandEnvironment() []string {
	return command.EnvironmentWithoutGitHubTokenOverrides()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
