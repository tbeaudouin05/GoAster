// goaster-agent exposes GoAster chat only on the server's loopback interface.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
)

func main() {
	store, err := agentCredentialStore(os.UserHomeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot initialize credential store")
		os.Exit(1)
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, chat.NewOpenAIAuthenticator(store), serve); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
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
	if len(args) != 0 {
		return fmt.Errorf("invalid invocation; use goaster-agent or goaster-agent login")
	}
	return start(auth.Store)
}

func serve(store chat.CredentialStore) error {
	memory := chat.NewMemoryStore("")
	provider := chat.NewCodexProvider(store)
	server := &http.Server{Addr: "127.0.0.1:8765", Handler: handlerWithMemory(provider, memory, provider.CheckAuth), ReadHeaderTimeout: 10 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		return fmt.Errorf("server stopped")
	}
	return nil
}

func handler(provider chat.Provider, checkAuth func(context.Context) error) http.Handler {
	return handlerWithMemory(provider, nil, checkAuth)
}

func handlerWithMemory(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error) http.Handler {
	var sessionsMu sync.Mutex
	sessions := make(map[int64]*chat.Session)
	sessionFor := func(chatID int64) *chat.Session {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if session := sessions[chatID]; session != nil {
			return session
		}
		session := chat.NewSessionWithMemory(chat.Config{PrimaryModel: chat.DefaultPrimaryModel, FallbackModel: chat.DefaultFallbackModel}, provider, memory)
		sessions[chatID] = session
		return session
	}
	mux := http.NewServeMux()
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
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		var request struct {
			Message     string                `json:"message"`
			ChatID      int64                 `json:"chat_id"`
			Attachments []attachment.Metadata `json:"attachments,omitempty"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
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
		for _, item := range request.Attachments {
			if err := item.Validate(); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
		}
		if checkAuth == nil || checkAuth(r.Context()) != nil {
			http.Error(w, "agent request failed; check remote authentication", http.StatusServiceUnavailable)
			return
		}
		session := sessionFor(request.ChatID)
		reply, err := session.ReplyWithAttachments(r.Context(), request.Message, request.Attachments)
		if err != nil {
			http.Error(w, "agent request failed; check remote authentication", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Reply string `json:"reply"`
		}{reply})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8765" && r.Host != "127.0.0.1" {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
