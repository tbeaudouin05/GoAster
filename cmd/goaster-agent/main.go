// goaster-agent exposes GoAster chat locally over loopback or a Unix socket.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"goaster/internal/artefacts"
	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/command"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
)

const maxChatEnvelopeBytes int64 = 1 << 20

type countingReader struct {
	io.Reader
	bytesRead int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func main() {
	store, err := agentCredentialStore(os.UserHomeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot initialize credential store")
		os.Exit(1)
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, chat.NewOpenAIAuthenticator(store), serve); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", chat.SanitizeTerminalText(err.Error()))
		os.Exit(1)
	}
}

// The remote agent must never fall back to the terminal's auth.json, even if
// both happen to run under the same home directory.
func agentCredentialStore(homeDir func() (string, error)) (chat.CredentialStore, error) {
	home, err := homeDir()
	if err != nil {
		return chat.CredentialStore{}, err
	}
	return chat.NewCredentialStore(filepath.Join(home, ".goaster", "agent-auth.json"))
}

func run(ctx context.Context, args []string, output io.Writer, auth *chat.OpenAIAuthenticator, start func(chat.CredentialStore) error) error {
	if len(args) == 1 && args[0] == "login" {
		return auth.Login(ctx, output)
	}
	if len(args) == 1 && (args[0] == "live" || args[0] == "health") {
		return probeAgent(ctx, args[0], output)
	}
	if len(args) != 0 {
		return fmt.Errorf("invalid invocation; use goaster-agent, login, live, or health")
	}
	return start(auth.Store)
}

func probeAgent(ctx context.Context, probe string, output io.Writer) error {
	transport, err := controltransport.ClientTransport()
	if err != nil {
		return err
	}
	endpoint := "/livez"
	if probe == "health" {
		endpoint = "/healthz"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+controltransport.Address+endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent probe failed")
	}
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent probe failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		return fmt.Errorf("agent probe failed")
	}
	_, err = io.WriteString(output, "ok\n")
	return err
}

func serve(store chat.CredentialStore) error {
	controlSecret, err := controlsecret.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	memory, err := chat.NewMemoryStoreWithError("")
	if err != nil {
		return fmt.Errorf("initialize Markdown memory store: %w", err)
	}
	if err := artefacts.InstallStarters(ctx); err != nil {
		return fmt.Errorf("install starter skills: %w", err)
	}
	provider := chat.NewCodexProvider(store)
	backgroundManager, err := command.NewBackgroundCommandManager()
	if err != nil {
		return fmt.Errorf("initialize background command manager: %w", err)
	}
	defer backgroundManager.Close()
	if err := chat.RegisterBackgroundCommandTool(provider, backgroundManager); err != nil {
		return fmt.Errorf("register background command tool: %w", err)
	}
	githubAuth := newGitHubAuthManager(githubAuthConfig{
		ClientID:   os.Getenv(githubClientIDEnv),
		HTTPClient: http.Client{Timeout: githubDefaultHTTPTimeout},
	})
	if err := registerGitHubAuthTool(provider, githubAuth); err != nil {
		return fmt.Errorf("register GitHub authentication handoff: %w", err)
	}
	registry, err := chat.NewModelRegistry("")
	if err != nil {
		return fmt.Errorf("initialize model registry: %w", err)
	}
	listener, err := controltransport.Listen()
	if err != nil {
		return fmt.Errorf("could not start local agent listener: %w", err)
	}
	defer listener.Close()
	server := http.Server{Handler: handlerWithMemoryAndRegistryAndGitHubAuthWithSecret(provider, memory, provider.CheckAuth, registry, githubAuth, controlSecret), ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		_ = server.Close()
	}()
	err = server.Serve(listener)
	stop()
	<-stopped // Wait for Close to finish unlinking the Unix socket.
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server stopped")
	}
	return nil
}

func handlerWithMemoryAndRegistryAndGitHubAuthWithSecret(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error, registry chat.ModelRegistry, githubAuth githubAuthManager, controlSecret string) http.Handler {
	var sessionsMu sync.Mutex
	sessions := make(map[int64]*chat.Session)
	sessionFor := func(chatID int64) *chat.Session {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if session := sessions[chatID]; session != nil {
			return session
		}
		session := chat.NewSessionWithMemory(chat.Config{Registry: registry, ChatID: chatID}, provider, memory)
		sessions[chatID] = session
		return session
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if checkAuth == nil || checkAuth(r.Context()) != nil {
			http.Error(w, "remote agent authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/auth/github/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validGitHubAuthControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		request, ok := decodeGitHubAuthHTTPRequest(w, r)
		if !ok {
			return
		}
		result := githubAuth.authorize(r.Context(), request.ChatID)
		if result != githubAuthAuthorizationGranted {
			http.Error(w, result, http.StatusForbidden)
			return
		}
		writeGitHubAuthHTTPResult(w, result)
	})
	mux.HandleFunc("/auth/github/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validGitHubAuthControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		request, ok := decodeGitHubAuthHTTPRequest(w, r)
		if !ok {
			return
		}
		result := githubAuth.cancel(r.Context(), request.ChatID)
		if result != githubAuthCancellationPending {
			http.Error(w, result, http.StatusConflict)
			return
		}
		writeGitHubAuthHTTPResult(w, result)
	})
	mux.HandleFunc("/auth/github/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validGitHubAuthControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		request, ok := decodeGitHubAuthHTTPRequest(w, r)
		if !ok {
			return
		}
		result := githubAuth.reconcile(r.Context(), request.ChatID)
		if result != githubAuthReconciled {
			http.Error(w, result, http.StatusConflict)
			return
		}
		writeGitHubAuthHTTPResult(w, result)
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validGitHubAuthControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		var request struct {
			Message     string                `json:"message"`
			ChatID      int64                 `json:"chat_id"`
			Attachments []attachment.Incoming `json:"attachments,omitempty"`
		}
		maxBodyBytes := int64(base64.StdEncoding.EncodedLen(int(attachment.MaxBytes))) + maxChatEnvelopeBytes
		body := &countingReader{Reader: http.MaxBytesReader(w, r.Body, maxBodyBytes)}
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil ||
			request.ChatID == 0 ||
			(strings.TrimSpace(request.Message) == "" && len(request.Attachments) == 0) ||
			len(request.Attachments) > attachment.MaxAttachments {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if dec.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// Preserve the original request envelope limit for text and metadata;
		// only the base64 representation of attachment bytes adds to it.
		envelopeBytes := body.bytesRead
		for _, item := range request.Attachments {
			envelopeBytes -= int64(base64.StdEncoding.EncodedLen(len(item.Data)))
		}
		if envelopeBytes > maxChatEnvelopeBytes {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		metadata := make([]attachment.Metadata, 0, len(request.Attachments))
		for _, item := range request.Attachments {
			validated, err := item.Validate()
			if errors.Is(err, attachment.ErrUnsupportedImage) {
				http.Error(w, "unsupported image format", http.StatusUnsupportedMediaType)
				return
			}
			if err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			metadata = append(metadata, validated)
		}
		if !chat.IsClearCommand(request.Message) && !chat.IsModelCommand(request.Message) && (checkAuth == nil || checkAuth(r.Context()) != nil) {
			http.Error(w, "agent request failed; check remote authentication", http.StatusServiceUnavailable)
			return
		}
		if len(metadata) > 0 {
			root, err := filepath.Abs(os.TempDir())
			if err != nil {
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			directory, err := os.MkdirTemp(root, attachment.AgentTemporaryDirectoryPrefix)
			if err != nil {
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			defer func() { _ = os.RemoveAll(directory) }()
			if err := os.Chmod(directory, 0700); err != nil {
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			for index := range metadata {
				metadata[index].Path = filepath.Join(directory, metadata[index].Filename)
				file, err := os.OpenFile(metadata[index].Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
				if err = file.Chmod(0600); err == nil {
					_, err = file.Write(request.Attachments[index].Data)
				}
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		encoder := json.NewEncoder(w)
		flusher, canFlush := w.(http.Flusher)
		streamEmitted := false
		emit := func(event any) {
			streamEmitted = true
			_ = encoder.Encode(event)
			if canFlush {
				flusher.Flush()
			}
		}
		if chat.IsModelCommand(request.Message) {
			if len(metadata) != 0 {
				http.Error(w, "model command does not accept attachments", http.StatusBadRequest)
				return
			}
			emit(struct {
				Type  string `json:"type"`
				Reply string `json:"reply"`
			}{"final", modelCommandReply(registry, request.ChatID, request.Message)})
			return
		}
		observer := func(name, arguments string) {
			if name == githubAuthToolName {
				// Authentication arguments are never useful to the channel adapter;
				// keep even malformed model-supplied secrets inside the agent.
				arguments = ""
			}
			emit(struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{"tool_call", name, arguments})
		}
		authObserver := func(notice chat.AuthHandoffNotice) {
			emit(struct {
				Type            string `json:"type"`
				Provider        string `json:"provider"`
				Status          string `json:"status"`
				VerificationURL string `json:"verification_url,omitempty"`
				UserCode        string `json:"user_code,omitempty"`
				ExpiresAt       string `json:"expires_at,omitempty"`
				Account         string `json:"account,omitempty"`
			}{"auth_handoff", notice.Provider, notice.Status, notice.VerificationURL, notice.UserCode, notice.ExpiresAt, notice.VerifiedAccount})
		}
		session := sessionFor(request.ChatID)
		if !chat.IsClearCommand(request.Message) {
			if notice := session.PendingModelRecoveryNotice(); notice != "" {
				emit(struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{"model_recovery", notice})
			}
		}
		requestContext := chat.WithToolCallObserver(r.Context(), observer)
		requestContext = chat.WithAuthHandoffObserver(requestContext, authObserver)
		requestContext = chat.WithChatID(requestContext, request.ChatID)
		reply, err := session.ReplyWithAttachments(requestContext, request.Message, metadata)
		if err != nil {
			if errors.Is(err, attachment.ErrUnsupportedImage) {
				http.Error(w, "unsupported image format", http.StatusUnsupportedMediaType)
				return
			}
			if message := chat.PostToolAuthenticationMessage(err); message != "" {
				if streamEmitted {
					emit(struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"post_tool_authentication", message})
					return
				}
				w.Header().Set("X-GoAster-Error", "post-tool-authentication")
				http.Error(w, message, http.StatusServiceUnavailable)
				return
			}
			if message := chat.SkillRecoveryMessage(err); message != "" {
				if streamEmitted {
					emit(struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"skill_recovery", message})
					return
				}
				w.Header().Set("X-GoAster-Error", "skill-recovery")
				http.Error(w, message, http.StatusServiceUnavailable)
				return
			}
			if errors.Is(err, chat.ErrModelUnavailable) {
				if streamEmitted {
					emit(struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"error", boundedAgentModelError(err)})
					return
				}
				http.Error(w, boundedAgentModelError(err), http.StatusServiceUnavailable)
				return
			}
			if streamEmitted {
				emit(struct {
					Type string `json:"type"`
				}{"error"})
				return
			}
			http.Error(w, "agent request failed; check remote authentication", http.StatusServiceUnavailable)
			return
		}
		emit(struct {
			Type  string `json:"type"`
			Reply string `json:"reply"`
		}{"final", reply})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8765" && r.Host != "127.0.0.1" {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validGitHubAuthControlRequest(r *http.Request, controlSecret string) bool {
	if r == nil || controlSecret == "" || strings.ContainsAny(controlSecret, "\r\n") {
		return false
	}
	provided := r.Header.Get(githubAuthControlHeader)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(controlSecret)) == 1
}

type githubAuthHTTPRequest struct {
	ChatID int64 `json:"chat_id"`
}

func decodeGitHubAuthHTTPRequest(w http.ResponseWriter, r *http.Request) (githubAuthHTTPRequest, bool) {
	var request githubAuthHTTPRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.ChatID <= 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return githubAuthHTTPRequest{}, false
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return githubAuthHTTPRequest{}, false
	}
	return request, true
}

func writeGitHubAuthHTTPResult(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(result + "\n"))
}

func modelCommandReply(registry chat.ModelRegistry, chatID int64, message string) string {
	reference, ok := chat.ModelCommandArgument(message)
	if !ok {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	if strings.TrimSpace(reference) == "" {
		state, _ := registry.Current(chatID)
		return formatModelSelection("Current model", state.Selection)
	}
	if strings.ContainsAny(reference, " \t\r\n") {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	selection, err := registry.Select(chatID, reference)
	if err != nil {
		if errors.Is(err, chat.ErrModelConfigBroken) {
			return "Model configuration is recovering; using emergency model " + chat.EmergencyModel + "."
		}
		if errors.Is(err, chat.ErrModelNotConfigured) {
			return "Model is not configured: " + boundedAgentText(reference, 128)
		}
		return "Model selection could not be saved."
	}
	return formatModelSelection("Selected model", selection)
}

func formatModelSelection(prefix string, selection chat.ModelSelection) string {
	if selection.Alias == "" {
		return prefix + ": " + selection.ID
	}
	return prefix + ": " + selection.ID + " (" + selection.Alias + ")"
}

func boundedAgentModelError(err error) string {
	return boundedAgentText(err.Error(), 512)
}

func boundedAgentText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
