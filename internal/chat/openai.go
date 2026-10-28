package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	openAIAuthURL       = "https://auth.openai.com"
	openAICodexURL      = "https://chatgpt.com/backend-api/codex/responses"
	openAIClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIOriginator    = "codex_cli_rs"
	openAIInstructions  = "You are Codex, a coding assistant." + ovhSetupInstructions
	credentialDirectory = ".goaster"
	credentialFilename  = "auth.json"

	tokenRefreshSkew  = 5 * time.Minute
	deviceCodeTimeout = 15 * time.Minute
	defaultPollDelay  = 5 * time.Second
	maxResponseBody   = 8 << 20
)

var ErrLoginRequired = errors.New("OpenAI subscription authentication required")

// Credential is the local ChatGPT/Codex OAuth state. It deliberately has no
// API-key field: GoAster uses the subscription-backed Codex service only.
type Credential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
}

// CredentialStore persists OAuth credentials in one private JSON file.
type CredentialStore struct {
	Path string
}

func NewCredentialStore(path string) (CredentialStore, error) {
	if strings.TrimSpace(path) != "" {
		return CredentialStore{Path: path}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return CredentialStore{}, fmt.Errorf("find home directory: %w", err)
	}
	return CredentialStore{Path: filepath.Join(home, credentialDirectory, credentialFilename)}, nil
}

func (s CredentialStore) Load() (Credential, error) {
	if strings.TrimSpace(s.Path) == "" {
		return Credential{}, fmt.Errorf("credential path is empty")
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, fmt.Errorf("%w: credentials were not found", ErrLoginRequired)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("inspect credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Credential{}, fmt.Errorf("credential file must not be a symbolic link")
	}
	if info.Mode().Perm()&0077 != 0 {
		return Credential{}, fmt.Errorf("credential file permissions are too broad: %04o (want 0600)", info.Mode().Perm())
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return Credential{}, fmt.Errorf("read credential file: %w", err)
	}
	var credential Credential
	if err := json.Unmarshal(data, &credential); err != nil {
		return Credential{}, fmt.Errorf("%w: parse credential file: %v", ErrLoginRequired, err)
	}
	credential.normalize()
	if credential.AccessToken == "" {
		return Credential{}, fmt.Errorf("%w: credential file has no access token", ErrLoginRequired)
	}
	return credential, nil
}

func (s CredentialStore) Save(credential Credential) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("credential path is empty")
	}
	credential.normalize()
	if credential.AccessToken == "" {
		return fmt.Errorf("cannot save credentials without an access token")
	}

	directory := filepath.Dir(s.Path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create credential directory: %w", err)
		}
		// The default directory is owned by GoAster. A custom -auth-file may
		// live under an application-managed directory, so do not chmod an
		// arbitrary parent (for example /tmp or /).
		if filepath.Base(directory) == credentialDirectory {
			if err := os.Chmod(directory, 0700); err != nil {
				return fmt.Errorf("restrict credential directory: %w", err)
			}
		}
	}
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(directory, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary credential file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary credential file: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	if err := os.Chmod(s.Path, 0600); err != nil {
		return fmt.Errorf("restrict credential file: %w", err)
	}
	return nil
}

func (c *Credential) normalize() {
	c.AccessToken = strings.TrimSpace(c.AccessToken)
	c.RefreshToken = strings.TrimSpace(c.RefreshToken)
	c.AccountID = strings.TrimSpace(c.AccountID)
	if c.AccountID == "" {
		c.AccountID = accountIDFromToken(c.AccessToken)
	}
}

func (c Credential) needsRefresh(now time.Time) bool {
	return c.AccessToken == "" || (!c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now.Add(tokenRefreshSkew)))
}

type OpenAIAuthenticator struct {
	Store       CredentialStore
	HTTPClient  *http.Client
	AuthBaseURL string
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
	PollTimeout time.Duration
}

func NewOpenAIAuthenticator(store CredentialStore) *OpenAIAuthenticator {
	return &OpenAIAuthenticator{
		Store:       store,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
		AuthBaseURL: openAIAuthURL,
		Now:         time.Now,
		Sleep:       sleepContext,
		PollTimeout: deviceCodeTimeout,
	}
}

func (a *OpenAIAuthenticator) Credential(ctx context.Context) (Credential, error) {
	credential, err := a.Store.Load()
	if err != nil {
		return Credential{}, err
	}
	now := a.now()
	if !credential.needsRefresh(now) {
		return credential, nil
	}
	if credential.RefreshToken == "" {
		return Credential{}, fmt.Errorf("%w: saved access token is expired and has no refresh token", ErrLoginRequired)
	}
	return a.refresh(ctx, credential)
}

func (a *OpenAIAuthenticator) Refresh(ctx context.Context) (Credential, error) {
	credential, err := a.Store.Load()
	if err != nil {
		return Credential{}, err
	}
	return a.refresh(ctx, credential)
}

type DeviceCode struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	Interval        int
}

func (a *OpenAIAuthenticator) RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	payload, err := json.Marshal(map[string]string{"client_id": openAIClientID})
	if err != nil {
		return DeviceCode{}, err
	}
	response, err := a.do(ctx, http.MethodPost, a.endpoint("/api/accounts/deviceauth/usercode"), "application/json", payload)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("read device code response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DeviceCode{}, fmt.Errorf("device code request failed with status %s", response.Status)
	}
	var result struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
		VerifyURL    string          `json:"verification_url"`
		VerifyURLAlt string          `json:"verify_url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return DeviceCode{}, fmt.Errorf("parse device code response: %w", err)
	}
	if result.UserCode == "" {
		result.UserCode = result.UserCodeAlt
	}
	interval, err := flexibleInt(result.Interval)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("parse device code interval: %w", err)
	}
	if interval < 1 {
		interval = int(defaultPollDelay / time.Second)
	}
	if result.DeviceAuthID == "" || result.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("device code response is missing the device ID or user code")
	}
	verificationURL := strings.TrimSpace(result.VerifyURL)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(result.VerifyURLAlt)
	}
	if verificationURL == "" {
		verificationURL = a.endpoint("/codex/device")
	}
	return DeviceCode{
		DeviceAuthID:    result.DeviceAuthID,
		UserCode:        result.UserCode,
		VerificationURL: verificationURL,
		Interval:        interval,
	}, nil
}

func (a *OpenAIAuthenticator) Login(ctx context.Context, output io.Writer) error {
	deviceCode, err := a.RequestDeviceCode(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Open %s\nEnter this one-time code: %s\nWaiting for OpenAI authorization...\n", deviceCode.VerificationURL, deviceCode.UserCode); err != nil {
		return fmt.Errorf("write login instructions: %w", err)
	}
	result, err := a.pollDeviceCode(ctx, deviceCode)
	if err != nil {
		return err
	}
	credential, err := a.credentialFromTokenResponse(result, nil)
	if err != nil {
		return fmt.Errorf("complete OpenAI login: %w", err)
	}
	if err := a.Store.Save(credential); err != nil {
		return fmt.Errorf("save OpenAI credentials: %w", err)
	}
	_, err = fmt.Fprintf(output, "OpenAI subscription authentication complete. Credentials saved to %s.\n", a.Store.Path)
	return err
}

type deviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

func (a *OpenAIAuthenticator) pollDeviceCode(ctx context.Context, deviceCode DeviceCode) (tokenResponse, error) {
	deadline := a.now().Add(a.pollTimeout())
	for {
		if !a.now().Before(deadline) {
			return tokenResponse{}, fmt.Errorf("device code authentication timed out after 15 minutes")
		}
		payload, err := json.Marshal(map[string]string{
			"device_auth_id": deviceCode.DeviceAuthID,
			"user_code":      deviceCode.UserCode,
		})
		if err != nil {
			return tokenResponse{}, err
		}
		response, err := a.do(ctx, http.MethodPost, a.endpoint("/api/accounts/deviceauth/token"), "application/json", payload)
		if err != nil {
			return tokenResponse{}, fmt.Errorf("poll device code: %w", err)
		}
		body, readErr := readLimited(response.Body)
		response.Body.Close()
		if readErr != nil {
			return tokenResponse{}, fmt.Errorf("read device token response: %w", readErr)
		}
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
			if err := a.sleep(ctx, time.Duration(deviceCode.Interval)*time.Second); err != nil {
				return tokenResponse{}, err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return tokenResponse{}, fmt.Errorf("device authentication failed with status %s", response.Status)
		}
		var code deviceTokenResponse
		if err := json.Unmarshal(body, &code); err != nil {
			return tokenResponse{}, fmt.Errorf("parse device token response: %w", err)
		}
		if code.AuthorizationCode == "" || code.CodeVerifier == "" {
			return tokenResponse{}, fmt.Errorf("device token response is missing the authorization code or verifier")
		}
		return a.exchangeCode(ctx, code)
	}
}

func (a *OpenAIAuthenticator) exchangeCode(ctx context.Context, code deviceTokenResponse) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code.AuthorizationCode},
		"redirect_uri":  {a.endpoint("/deviceauth/callback")},
		"client_id":     {openAIClientID},
		"code_verifier": {code.CodeVerifier},
	}
	response, err := a.do(ctx, http.MethodPost, a.endpoint("/oauth/token"), "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read token response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token exchange failed with status %s", response.Status)
	}
	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return tokenResponse{}, fmt.Errorf("parse token response: %w", err)
	}
	return result, nil
}

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	AccountID    string          `json:"chatgpt_account_id"`
	AccountIDAlt string          `json:"account_id"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
}

func (a *OpenAIAuthenticator) refresh(ctx context.Context, current Credential) (Credential, error) {
	if current.RefreshToken == "" {
		return Credential{}, fmt.Errorf("%w: saved credentials have no refresh token", ErrLoginRequired)
	}
	payload, err := json.Marshal(map[string]string{
		"grant_type":     "refresh_token",
		"client_id":      openAIClientID,
		"refresh_token":  current.RefreshToken,
		"refresh_reason": "proactive_expiry",
	})
	if err != nil {
		return Credential{}, err
	}
	response, err := a.do(ctx, http.MethodPost, a.endpoint("/oauth/token"), "application/json", payload)
	if err != nil {
		return Credential{}, fmt.Errorf("refresh token: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body)
	if err != nil {
		return Credential{}, fmt.Errorf("read refreshed token response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized {
			return Credential{}, fmt.Errorf("%w: saved refresh token was rejected with status %s", ErrLoginRequired, response.Status)
		}
		return Credential{}, fmt.Errorf("token refresh failed with status %s", response.Status)
	}
	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return Credential{}, fmt.Errorf("parse refreshed token response: %w", err)
	}
	credential, err := a.credentialFromTokenResponse(result, &current)
	if err != nil {
		return Credential{}, err
	}
	if err := a.Store.Save(credential); err != nil {
		return Credential{}, fmt.Errorf("save refreshed credentials: %w", err)
	}
	return credential, nil
}

func (a *OpenAIAuthenticator) credentialFromTokenResponse(result tokenResponse, previous *Credential) (Credential, error) {
	accessToken := strings.TrimSpace(result.AccessToken)
	if accessToken == "" {
		return Credential{}, fmt.Errorf("token response has no access token")
	}
	refreshToken := strings.TrimSpace(result.RefreshToken)
	if refreshToken == "" && previous != nil {
		refreshToken = previous.RefreshToken
	}
	accountID := strings.TrimSpace(result.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(result.AccountIDAlt)
	}
	if accountID == "" {
		accountID = accountIDFromToken(result.IDToken)
	}
	if accountID == "" {
		accountID = accountIDFromToken(accessToken)
	}
	if accountID == "" && previous != nil {
		accountID = previous.AccountID
	}
	expiresAt := time.Time{}
	seconds, err := flexibleInt(result.ExpiresIn)
	if err != nil {
		return Credential{}, fmt.Errorf("parse token expiry: %w", err)
	}
	if seconds > 0 {
		expiresAt = a.now().Add(time.Duration(seconds) * time.Second)
	}
	if expiresAt.IsZero() {
		expiresAt = tokenExpiry(accessToken)
	}
	if expiresAt.IsZero() && previous != nil {
		expiresAt = previous.ExpiresAt
	}
	return Credential{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    accountID,
	}, nil
}

func (a *OpenAIAuthenticator) do(ctx context.Context, method, endpoint, contentType string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	return a.httpClient().Do(request)
}

func (a *OpenAIAuthenticator) endpoint(path string) string {
	return strings.TrimRight(a.authBaseURL(), "/") + path
}

func (a *OpenAIAuthenticator) authBaseURL() string {
	if strings.TrimSpace(a.AuthBaseURL) == "" {
		return openAIAuthURL
	}
	return strings.TrimRight(strings.TrimSpace(a.AuthBaseURL), "/")
}

func (a *OpenAIAuthenticator) httpClient() *http.Client {
	if a.HTTPClient == nil {
		return http.DefaultClient
	}
	return a.HTTPClient
}

func (a *OpenAIAuthenticator) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *OpenAIAuthenticator) sleep(ctx context.Context, delay time.Duration) error {
	if a.Sleep == nil {
		return sleepContext(ctx, delay)
	}
	return a.Sleep(ctx, delay)
}

func (a *OpenAIAuthenticator) pollTimeout() time.Duration {
	if a.PollTimeout <= 0 {
		return deviceCodeTimeout
	}
	return a.PollTimeout
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

func readLimited(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBody {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxResponseBody)
	}
	return data, nil
}

func flexibleInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return 0, nil
		}
		return strconv.Atoi(strings.TrimSpace(text))
	}
	return 0, fmt.Errorf("invalid integer %s", string(raw))
}

func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("not a JWT")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func accountIDFromToken(token string) string {
	claims, err := jwtClaims(token)
	if err != nil {
		return ""
	}
	return accountIDFromClaims(claims)
}

func accountIDFromClaims(claims map[string]any) string {
	for _, key := range []string{
		"chatgpt_account_id",
		"account_id",
		"https://api.openai.com/auth.chatgpt_account_id",
	} {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, key := range []string{"https://api.openai.com/auth", "auth"} {
		if nested, ok := claims[key].(map[string]any); ok {
			if accountID := accountIDFromClaims(nested); accountID != "" {
				return accountID
			}
		}
	}
	if organizations, ok := claims["organizations"].([]any); ok {
		for _, organization := range organizations {
			if nested, ok := organization.(map[string]any); ok {
				if value, ok := nested["id"].(string); ok && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
	}
	return ""
}

func tokenExpiry(token string) time.Time {
	claims, err := jwtClaims(token)
	if err != nil {
		return time.Time{}
	}
	var seconds int64
	switch value := claims["exp"].(type) {
	case float64:
		seconds = int64(value)
	case string:
		seconds, _ = strconv.ParseInt(value, 10, 64)
	}
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

type CodexProvider struct {
	auth       *OpenAIAuthenticator
	httpClient *http.Client
	backendURL string
}

func NewCodexProvider(store CredentialStore) *CodexProvider {
	authenticator := NewOpenAIAuthenticator(store)
	return &CodexProvider{
		auth:       authenticator,
		httpClient: authenticator.HTTPClient,
		backendURL: openAICodexURL,
	}
}

func (p *CodexProvider) Description() string {
	return "OpenAI subscription via ChatGPT/Codex backend"
}

func (p *CodexProvider) EnsureAuth(ctx context.Context, outputs ...io.Writer) error {
	var output io.Writer = io.Discard
	if len(outputs) > 0 && outputs[0] != nil {
		output = outputs[0]
	}
	if _, err := p.auth.Credential(ctx); err == nil {
		return nil
	} else if !errors.Is(err, ErrLoginRequired) {
		return fmt.Errorf("OpenAI subscription authentication failed: %w", err)
	}
	if err := p.auth.Login(ctx, output); err != nil {
		return fmt.Errorf("OpenAI subscription authentication failed: %w", err)
	}
	return nil
}

func (p *CodexProvider) Reply(ctx context.Context, model, message string) (string, error) {
	credential, err := p.auth.Credential(ctx)
	if err != nil {
		if errors.Is(err, ErrLoginRequired) {
			return "", fmt.Errorf("%w; restart GoAster to authenticate", err)
		}
		return "", fmt.Errorf("OpenAI subscription authentication failed: %w", err)
	}
	reply, status, err := p.requestReply(ctx, credential, model, message)
	if status == http.StatusUnauthorized {
		refreshed, refreshErr := p.auth.Refresh(ctx)
		if refreshErr != nil {
			return "", fmt.Errorf("OpenAI session expired; restart GoAster to authenticate: %w", refreshErr)
		}
		reply, status, err = p.requestReply(ctx, refreshed, model, message)
	}
	if err != nil {
		if status == http.StatusUnauthorized {
			return "", fmt.Errorf("%w; restart GoAster to authenticate", ErrLoginRequired)
		}
		if status == http.StatusNotFound {
			return "", fmt.Errorf("%w: Codex model %s was not found", ErrModelUnavailable, model)
		}
		return "", err
	}
	return reply, nil
}

type codexRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions"`
	Input        []codexInputItem `json:"input"`
	Stream       bool             `json:"stream"`
	Store        bool             `json:"store"`
}

type codexInputItem struct {
	Type    string              `json:"type"`
	Role    string              `json:"role"`
	Content []codexInputContent `json:"content"`
}

type codexInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (p *CodexProvider) requestReply(ctx context.Context, credential Credential, model, message string) (string, int, error) {
	payload, err := json.Marshal(codexRequest{
		Model:        model,
		Instructions: openAIInstructions,
		Input: []codexInputItem{{
			Type:    "message",
			Role:    "user",
			Content: []codexInputContent{{Type: "input_text", Text: message}},
		}},
		Stream: true,
		Store:  false,
	})
	if err != nil {
		return "", 0, fmt.Errorf("encode Codex request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.backendURL, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("build Codex request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	if credential.AccountID != "" {
		request.Header.Set("ChatGPT-Account-ID", credential.AccountID)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "goaster")
	request.Header.Set("originator", openAIOriginator)
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	response, err := p.client().Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("Codex request: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body)
	if err != nil {
		return "", response.StatusCode, fmt.Errorf("read Codex response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", response.StatusCode, fmt.Errorf("Codex backend request failed with status %s", response.Status)
	}
	reply, err := parseCodexResponse(body)
	if err != nil {
		return "", response.StatusCode, err
	}
	return reply, response.StatusCode, nil
}

func (p *CodexProvider) client() *http.Client {
	if p.httpClient == nil {
		return http.DefaultClient
	}
	return p.httpClient
}

type codexResponse struct {
	Output     []codexOutputItem `json:"output"`
	OutputText string            `json:"output_text"`
}

type codexOutputItem struct {
	Type    string               `json:"type"`
	Text    string               `json:"text"`
	Content []codexOutputContent `json:"content"`
}

type codexOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexStreamEvent struct {
	Type     string           `json:"type"`
	Delta    string           `json:"delta"`
	Text     string           `json:"text"`
	Error    *codexEventError `json:"error"`
	Response *codexResponse   `json:"response"`
	Item     *codexOutputItem `json:"item"`
}

type codexEventError struct {
	Message string `json:"message"`
}

func parseCodexResponse(body []byte) (string, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("Codex response contained no output")
	}
	if trimmed[0] == '{' {
		var response codexResponse
		if err := json.Unmarshal(trimmed, &response); err == nil {
			if text := responseText(response); text != "" {
				return text, nil
			}
		}
	}

	var output strings.Builder
	sawDelta := false
	sawItem := false
	process := func(data string) error {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			return nil
		}
		var event codexStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("parse Codex stream event: %w", err)
		}
		switch event.Type {
		case "response.failed", "response.incomplete", "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("Codex response failed: %s", event.Error.Message)
			}
			return fmt.Errorf("Codex response failed")
		case "response.output_text.delta":
			output.WriteString(event.Delta)
			sawDelta = true
		case "response.output_text.done":
			if !sawDelta {
				output.WriteString(event.Text)
			}
		case "response.output_item.done":
			if !sawDelta && !sawItem && event.Item != nil {
				output.WriteString(outputItemText(*event.Item))
				sawItem = true
			}
		default:
			if !sawDelta && !sawItem && event.Response != nil {
				output.WriteString(responseText(*event.Response))
				sawItem = true
			}
		}
		return nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), maxResponseBody)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		err := process(data.String())
		data.Reset()
		return err
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return "", err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Codex stream: %w", err)
	}
	if err := flush(); err != nil {
		return "", err
	}
	if output.Len() == 0 {
		return "", fmt.Errorf("Codex response contained no output text")
	}
	return output.String(), nil
}

func responseText(response codexResponse) string {
	if response.OutputText != "" {
		return response.OutputText
	}
	var output strings.Builder
	for _, item := range response.Output {
		output.WriteString(outputItemText(item))
	}
	return output.String()
}

func outputItemText(item codexOutputItem) string {
	if item.Text != "" {
		return item.Text
	}
	var output strings.Builder
	for _, content := range item.Content {
		if content.Type == "output_text" || content.Type == "" {
			output.WriteString(content.Text)
		}
	}
	return output.String()
}
