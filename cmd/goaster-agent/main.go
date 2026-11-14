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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"goaster/internal/artefacts"
	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/command"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
	"goaster/internal/localenv"
	"goaster/internal/migration"
)

const maxChatEnvelopeBytes int64 = 1 << 20

const (
	environmentCommand       = "/env"
	environmentAcceptKeyword = "ACCEPT"
	environmentAcceptCommand = environmentCommand + " " + environmentAcceptKeyword
	environmentFilename      = "goaster.env"
	environmentProposalTTL   = 10 * time.Minute
)

const controlAuthorizationHeader = "X-GoAster-GitHub-Auth-Control"

type countingReader struct {
	io.Reader
	bytesRead int64
}

type environmentProposalKey struct {
	userID int64
	chatID int64
}

type environmentProposal struct {
	baseDigest string
	values     map[string]string
	expiresAt  time.Time
}

type environmentRequestKind uint8

const (
	environmentRequestNone environmentRequestKind = iota
	environmentPreviewRequest
	environmentAcceptRequest
	environmentMalformedPreviewRequest
	environmentMalformedAcceptRequest
)

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func main() {
	store, err := agentCredentialStore(os.UserHomeDir)
	if err != nil {
		diagnostics.Error("agent.startup", err)
		fmt.Fprintln(os.Stderr, "agent: cannot initialize credential store")
		os.Exit(1)
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, chat.NewOpenAIAuthenticator(store), serve); err != nil {
		diagnostics.Error("agent.main", err)
		fmt.Fprintln(os.Stderr, "agent: operation failed")
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
	if len(args) > 0 && (args[0] == "prompt" || args[0] == "test-prompt") {
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New(`usage: goaster-agent prompt|test-prompt "MESSAGE"`)
		}
		if auth == nil {
			return errors.New("agent authentication is unavailable")
		}
		if args[0] == "test-prompt" {
			return runTestPrompt(ctx, args[1], output, auth.Store)
		}
		return runPrompt(ctx, args[1], output, auth.Store)
	}
	if len(args) == 1 && (args[0] == "live" || args[0] == "health") {
		return probeAgent(ctx, args[0], output)
	}
	if len(args) != 0 {
		return fmt.Errorf("invalid invocation; use goaster-agent, login, prompt, test-prompt, live, or health")
	}
	return start(auth.Store)
}

func runTestPrompt(ctx context.Context, message string, output io.Writer, store chat.CredentialStore) error {
	reply, err := chat.ReplyWithoutTools(ctx, chat.NewCodexProvider(store), chat.DefaultModel, message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, reply); err != nil {
		return fmt.Errorf("write prompt reply: %w", err)
	}
	return nil
}

type agentPromptSetup struct {
	memory   *chat.MemoryStore
	registry chat.ModelRegistry
}

func newAgentPromptSetup(ctx context.Context) (agentPromptSetup, error) {
	memory, err := chat.NewMemoryStoreWithError("")
	if err != nil {
		return agentPromptSetup{}, fmt.Errorf("initialize Markdown memory store: %w", err)
	}
	if err := artefacts.InstallStarters(ctx); err != nil {
		return agentPromptSetup{}, fmt.Errorf("install starter skills: %w", err)
	}
	registry, err := chat.NewModelRegistry("")
	if err != nil {
		return agentPromptSetup{}, fmt.Errorf("initialize model registry: %w", err)
	}
	return agentPromptSetup{memory: memory, registry: registry}, nil
}

func newAgentSession(provider chat.Provider, memory *chat.MemoryStore, registry chat.ModelRegistry, chatID int64) *chat.Session {
	return chat.NewSessionWithMemory(chat.Config{Registry: registry, ChatID: chatID}, provider, memory)
}

func newAgentProvider(store chat.CredentialStore) (*chat.CodexProvider, command.BackgroundCommandManager, error) {
	provider := chat.NewCodexProvider(store)
	backgroundManager, err := command.NewBackgroundCommandManager()
	if err != nil {
		return nil, command.BackgroundCommandManager{}, fmt.Errorf("initialize background command manager: %w", err)
	}
	if err := chat.RegisterBackgroundCommandTool(provider, backgroundManager); err != nil {
		closeErr := backgroundManager.Close()
		return nil, command.BackgroundCommandManager{}, errors.Join(fmt.Errorf("register background command tool: %w", err), closeErr)
	}
	return provider, backgroundManager, nil
}

func runPrompt(ctx context.Context, message string, output io.Writer, store chat.CredentialStore) (promptErr error) {
	provider, backgroundManager, err := newAgentProvider(store)
	if err != nil {
		return err
	}
	defer func() { promptErr = errors.Join(promptErr, backgroundManager.Close()) }()
	return runAgentPrompt(ctx, message, output, provider)
}

func runAgentPrompt(ctx context.Context, message string, output io.Writer, provider chat.Provider) error {
	setup, err := newAgentPromptSetup(ctx)
	if err != nil {
		return err
	}
	session := newAgentSession(provider, setup.memory, setup.registry, 0)
	reply, err := session.Reply(ctx, message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, reply); err != nil {
		return fmt.Errorf("write prompt reply: %w", err)
	}
	return nil
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
		return fmt.Errorf("build agent probe request: %w", err)
	}
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent probe transport: %w", err)
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("agent probe returned no response body")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil {
		return fmt.Errorf("read agent probe response: %w", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		return fmt.Errorf("agent probe returned status %s or an invalid response", response.Status)
	}
	_, err = io.WriteString(output, "ok\n")
	return err
}

type migrationSchedulerRuntime interface {
	migration.MonitorScheduler
	Start(context.Context) error
	Stop()
}

func migrationOperations(options migration.Options, jobs migrationSchedulerRuntime, ready bool) chat.MigrationToolOperations {
	return chat.MigrationToolOperations{
		Estimate: func(ctx context.Context, request migration.PriceRequest) (migration.PriceEstimate, error) {
			return migration.EstimatePriceOptional(ctx, request, options)
		},
		Availability: func(ctx context.Context, request migration.PriceRequest) (migration.AvailabilityResult, error) {
			return migration.Availability(ctx, request, options)
		},
		Start: func(_ context.Context, request migration.Request) (migration.Result, error) {
			return migration.Result{Status: "unavailable", ServerType: request.ServerType}, errors.New("migration execution is disabled")
		},
		Status: func(_ context.Context, jobID string) (migration.StatusResult, error) {
			if !ready || jobs == nil {
				return migration.StatusResult{}, errors.New("migration scheduler capability is unavailable")
			}
			return migration.MigrationStatus(jobID, jobs)
		},
		Stop: func(_ context.Context, jobID string) (migration.Result, error) {
			if !ready || jobs == nil {
				return migration.Result{}, errors.New("migration scheduler capability is unavailable")
			}
			return migration.StopMonitor(jobID, jobs)
		},
	}
}

func serve(store chat.CredentialStore) (serveErr error) {
	controlSecret, err := controlsecret.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setup, err := newAgentPromptSetup(ctx)
	if err != nil {
		return err
	}
	provider, backgroundManager, err := newAgentProvider(store)
	if err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, backgroundManager.Close()) }()
	migrationOptions := migration.DefaultOptions()
	// Migration execution remains disabled. Ordinary conversational approval in
	// a private chat is workflow guidance only, not a trusted cryptographic
	// binding; do not resume previously stored scheduler jobs through this
	// runtime.
	if err := chat.RegisterMigrationTool(provider, migrationOperations(migrationOptions, nil, false)); err != nil {
		return fmt.Errorf("register migration tool: %w", err)
	}
	listener, err := controltransport.Listen()
	if err != nil {
		return fmt.Errorf("could not start local agent listener: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			serveErr = errors.Join(serveErr, err)
		}
	}()
	server := http.Server{Handler: handlerWithMemoryAndRegistryWithSecret(provider, setup.memory, provider.CheckAuth, setup.registry, controlSecret), ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		if err := server.Close(); err != nil && !normalServerStop(err) {
			diagnostics.Error("agent.server.close", err)
		}
	}()
	err = server.Serve(listener)
	stop()
	<-stopped // Wait for Close to finish unlinking the Unix socket.
	if err != nil && !(ctx.Err() != nil && normalServerStop(err)) {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

func normalServerStop(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !normalServerStop(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return normalServerStop(cause)
	}
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

func handlerWithMemoryAndRegistryWithSecret(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error, registry chat.ModelRegistry, controlSecret string) http.Handler {
	var sessionsMu sync.Mutex
	sessions := make(map[int64]*chat.Session)
	var environmentSerialMu sync.Mutex
	environmentProposals := make(map[environmentProposalKey]environmentProposal)
	sessionFor := func(chatID int64) *chat.Session {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if session := sessions[chatID]; session != nil {
			return session
		}
		session := newAgentSession(provider, memory, registry, chatID)
		sessions[chatID] = session
		return session
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := w.Write([]byte("ok\n")); err != nil {
			diagnostics.Error("agent.livez.write", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var authErr error
		if checkAuth == nil {
			authErr = errors.New("authentication checker is unavailable")
		} else {
			authErr = checkAuth(r.Context())
		}
		if authErr != nil {
			diagnostics.Error("agent.healthz.authentication", authErr)
			http.Error(w, "remote agent authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := w.Write([]byte("ok\n")); err != nil {
			diagnostics.Error("agent.healthz.write", err)
		}
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validControlRequest(r, controlSecret) {
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
			UserID      int64                 `json:"user_id,omitempty"`
			Attachments []attachment.Incoming `json:"attachments,omitempty"`
		}
		maxBodyBytes := int64(base64.StdEncoding.EncodedLen(int(attachment.MaxBytes))) + maxChatEnvelopeBytes
		body := &countingReader{Reader: http.MaxBytesReader(w, r.Body, maxBodyBytes)}
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			diagnostics.Error("agent.chat.decode", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.ChatID == 0 || (strings.TrimSpace(request.Message) == "" && len(request.Attachments) == 0) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		environmentKind := classifyEnvironmentRequest(request.Message)
		isEnvironment := environmentKind != environmentRequestNone
		if isEnvironment && request.UserID <= 0 {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if !isEnvironment && hasReservedEnvironmentAttachment(request.Attachments) {
			http.Error(w, "goaster.env uploads require /env", http.StatusBadRequest)
			return
		}
		if isEnvironment {
			environmentSerialMu.Lock()
			defer environmentSerialMu.Unlock()
		}
		// Any /env command-shaped request is a replacement attempt, even when
		// its attachment or command spelling is malformed and will be rejected
		// below. This prevents an older proposal from surviving a replacement
		// attempt.
		if environmentKind == environmentPreviewRequest || environmentKind == environmentMalformedPreviewRequest {
			key := environmentProposalKey{userID: request.UserID, chatID: request.ChatID}
			delete(environmentProposals, key)
		}
		if len(request.Attachments) > attachment.MaxAttachments {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			if err != nil {
				diagnostics.Error("agent.chat.trailing_data", err)
			} else {
				diagnostics.Error("agent.chat.trailing_data", errors.New("request contains multiple JSON values"))
			}
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
		if isEnvironment && environmentKind == environmentPreviewRequest &&
			len(request.Attachments) > 0 &&
			(len(request.Attachments) != 1 || len(metadata) != 1 ||
				request.Attachments[0].Filename != environmentFilename ||
				metadata[0].Filename != environmentFilename || metadata[0].Type != "document" ||
				metadata[0].Size > int64(localenv.MaxFileBytes)) {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if isEnvironment && request.Message == environmentAcceptCommand && len(request.Attachments) != 0 {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if !chat.IsClearCommand(request.Message) && !chat.IsModelCommand(request.Message) && !isEnvironment {
			var authErr error
			if checkAuth == nil {
				authErr = errors.New("authentication checker is unavailable")
			} else {
				authErr = checkAuth(r.Context())
			}
			if authErr != nil {
				diagnostics.Error("agent.chat.authentication", authErr)
				http.Error(w, "agent request failed; check remote authentication", http.StatusServiceUnavailable)
				return
			}
		}
		if len(metadata) > 0 {
			root, err := filepath.Abs(os.TempDir())
			if err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err)
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			directory, err := os.MkdirTemp(root, attachment.AgentTemporaryDirectoryPrefix)
			if err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err)
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			defer func() {
				if err := os.RemoveAll(directory); err != nil {
					diagnostics.Error("agent.chat.attachment_cleanup", err)
				}
			}()
			if err := os.Chmod(directory, 0700); err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err)
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			for index := range metadata {
				metadata[index].Path = filepath.Join(directory, metadata[index].Filename)
				file, err := os.OpenFile(metadata[index].Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					diagnostics.Error("agent.chat.attachment_write", err)
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
				if err = file.Chmod(0600); err == nil {
					_, err = file.Write(request.Attachments[index].Data)
				}
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					diagnostics.Error("agent.chat.attachment_write", errors.Join(err, closeErr))
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
			}
		}
		environmentReply := func(userID, chatID int64, _ string, incoming []attachment.Incoming, metadata []attachment.Metadata) string {
			key := environmentProposalKey{userID: userID, chatID: chatID}
			now := time.Now()
			for proposalKey, proposal := range environmentProposals {
				if now.After(proposal.expiresAt) {
					delete(environmentProposals, proposalKey)
				}
			}
			if environmentKind == environmentMalformedPreviewRequest {
				delete(environmentProposals, key)
				return "Send /env with a document named goaster.env attached in the same message."
			}
			if environmentKind == environmentMalformedAcceptRequest {
				return "Reply exactly ACCEPT as a text message after reviewing the environment preview."
			}
			if environmentKind == environmentPreviewRequest {
				delete(environmentProposals, key)
				if len(metadata) != 1 || len(incoming) != 1 || metadata[0].Path == "" {
					return "Send /env with a document named goaster.env attached in the same message."
				}
				store, err := localenv.NewStore()
				if err != nil {
					diagnostics.Error("agent.environment.store", err)
					return "Environment settings are unavailable; no changes were saved."
				}
				if err := store.Load(); err != nil {
					diagnostics.Error("agent.environment.load", err)
					return "Environment settings are unavailable; no changes were saved."
				}
				values, err := localenv.LoadFile(metadata[0].Path)
				if err != nil {
					diagnostics.Error("agent.environment.input", err)
					return "The goaster.env file was rejected: use unique KEY=VALUE names, one setting per line, with no blank lines. No changes were saved."
				}
				changes := localenv.Changes(store.Values(), values)
				if len(changes.Added) == 0 && len(changes.Modified) == 0 {
					delete(environmentProposals, key)
					return "No environment changes detected; nothing was saved."
				}
				environmentProposals[key] = environmentProposal{
					baseDigest: localenv.Digest(store.Values()),
					values:     localenv.Merge(nil, values),
					expiresAt:  time.Now().Add(environmentProposalTTL),
				}
				return formatEnvironmentProposal(changes)
			}

			proposal, ok := environmentProposals[key]
			if !ok || now.After(proposal.expiresAt) {
				delete(environmentProposals, key)
				return "No environment proposal is pending. Send /env with goaster.env attached in the same message."
			}
			delete(environmentProposals, key)

			store, err := localenv.NewStore()
			if err != nil {
				diagnostics.Error("agent.environment.store", err)
				return "Environment settings are unavailable; no changes were saved."
			}
			if err := store.Load(); err != nil {
				diagnostics.Error("agent.environment.load", err)
				return "Environment settings are unavailable; no changes were saved."
			}
			changes, err := store.UpsertIfUnchanged(proposal.baseDigest, proposal.values)
			if errors.Is(err, localenv.ErrChanged) {
				diagnostics.Error("agent.environment.save", err)
				return "Environment settings changed since this preview; send /env with goaster.env attached in the same message to review a fresh proposal."
			}
			if err != nil {
				diagnostics.Error("agent.environment.save", err)
				return "Environment settings could not be saved; no changes were applied."
			}
			if len(changes.Added) == 0 && len(changes.Modified) == 0 {
				return "No environment changes detected; nothing was saved."
			}
			return formatEnvironmentApplied(changes)
		}
		if isEnvironment {
			reply := environmentReply(request.UserID, request.ChatID, request.Message, request.Attachments, metadata)
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-cache")
			if err := json.NewEncoder(w).Encode(struct {
				Type  string `json:"type"`
				Reply string `json:"reply"`
			}{"final", reply}); err != nil {
				diagnostics.Error("agent.environment.stream_encode", err)
			}
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		encoder := json.NewEncoder(w)
		_, canFlush := w.(http.Flusher)
		streamEmitted := false
		var streamErr error
		emit := func(event any) {
			if streamErr != nil {
				return
			}
			streamEmitted = true
			if err := encoder.Encode(event); err != nil {
				streamErr = err
				diagnostics.Error("agent.chat.stream_encode", err)
				return
			}
			if canFlush {
				if err := http.NewResponseController(w).Flush(); err != nil {
					streamErr = err
					diagnostics.Error("agent.chat.stream_flush", err)
				}
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
			emit(struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{"tool_call", name, arguments})
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
		requestContext = chat.WithRuntimeFailureOperation(requestContext, "agent.chat")
		values, environmentErr := runtimeEnvironment()
		if environmentErr != nil {
			diagnostics.Error("agent.chat.runtime_environment", environmentErr)
			requestContext = chat.WithShellEnvironmentUnavailable(requestContext)
		} else if len(values) > 0 {
			requestContext = chat.WithShellEnvironment(requestContext, values)
		}
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

func validControlRequest(r *http.Request, controlSecret string) bool {
	if r == nil || controlSecret == "" || strings.ContainsAny(controlSecret, "\r\n") {
		return false
	}
	provided := r.Header.Get(controlAuthorizationHeader)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(controlSecret)) == 1
}

func isEnvironmentRequest(message string) bool {
	return classifyEnvironmentRequest(message) != environmentRequestNone
}

func hasReservedEnvironmentAttachment(attachments []attachment.Incoming) bool {
	for _, item := range attachments {
		if attachment.SanitizeFilename(item.Filename) == environmentFilename {
			return true
		}
	}
	return false
}

func classifyEnvironmentRequest(message string) environmentRequestKind {
	switch message {
	case environmentCommand:
		return environmentPreviewRequest
	case environmentAcceptCommand:
		return environmentAcceptRequest
	case environmentAcceptKeyword:
		return environmentMalformedAcceptRequest
	}
	if isEnvironmentCommandMention(message) {
		return environmentPreviewRequest
	}
	if strings.HasPrefix(message, environmentCommand+"@") {
		return environmentMalformedPreviewRequest
	}
	if hasEnvironmentWhitespacePrefix(message, environmentCommand) {
		return environmentMalformedPreviewRequest
	}
	if hasEnvironmentWhitespacePrefix(message, environmentAcceptKeyword) {
		return environmentMalformedAcceptRequest
	}
	return environmentRequestNone
}

func hasEnvironmentWhitespacePrefix(message, prefix string) bool {
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	rest := message[len(prefix):]
	for _, character := range rest {
		return unicode.IsSpace(character)
	}
	return false
}

func isEnvironmentCommandMention(message string) bool {
	prefix := environmentCommand + "@"
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	username := message[len(prefix):]
	if username == "" {
		return false
	}
	for _, character := range username {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func formatEnvironmentNames(label string, names []string) string {
	if len(names) == 0 {
		return label + ": none"
	}
	return label + ": " + strings.Join(names, ", ")
}

func formatEnvironmentProposal(changes localenv.ChangeSet) string {
	return "Environment proposal (values hidden).\n" +
		formatEnvironmentNames("Added", changes.Added) + "\n" +
		formatEnvironmentNames("Modified", changes.Modified) + "\n" +
		"Reply exactly ACCEPT to apply this proposal."
}

func formatEnvironmentApplied(changes localenv.ChangeSet) string {
	return "Environment settings saved locally (values hidden).\n" +
		formatEnvironmentNames("Added", changes.Added) + "\n" +
		formatEnvironmentNames("Modified", changes.Modified) + "\n" +
		"The new settings are available to subsequent GoAster shell commands. Settings read by long-lived processes at startup may require a restart; no services were restarted."
}

func runtimeEnvironment() (map[string]string, error) {
	store, err := localenv.NewStore()
	if err != nil {
		return nil, err
	}
	if err := store.Load(); err != nil {
		return nil, err
	}
	return store.Values(), nil
}

func modelCommandReply(registry chat.ModelRegistry, chatID int64, message string) string {
	reference, ok := chat.ModelCommandArgument(message)
	if !ok {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	if strings.TrimSpace(reference) == "" {
		state, err := registry.Current(chatID)
		if err != nil {
			diagnostics.Error("agent.model.current", err)
			return "Model selection could not be read."
		}
		return formatModelSelection("Current model", state.Selection)
	}
	if strings.ContainsAny(reference, " \t\r\n") {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	selection, err := registry.Select(chatID, reference)
	if err != nil {
		diagnostics.Error("agent.model.select", err, diagnostics.Field{Key: "model", Value: reference})
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
