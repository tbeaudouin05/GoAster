// goaster-agent exposes GoAster chat only on the server's loopback interface.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"goaster/internal/chat"
)

func main() {
	store, err := chat.NewCredentialStore("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot initialize credential store")
		os.Exit(1)
	}
	server := &http.Server{Addr: "127.0.0.1:8765", Handler: handler(chat.NewCodexProvider(store)), ReadHeaderTimeout: 10 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "agent: server stopped")
		os.Exit(1)
	}
}

func handler(provider chat.Provider) http.Handler {
	var mu sync.Mutex // serialize credential refreshes
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
			Message string `json:"message"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil || strings.TrimSpace(request.Message) == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if dec.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		session := chat.NewSession(chat.Config{PrimaryModel: chat.DefaultPrimaryModel, FallbackModel: chat.DefaultFallbackModel}, provider)
		reply, err := session.Reply(r.Context(), request.Message)
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
