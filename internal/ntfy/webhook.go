package ntfy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultTokenTTL = 1 * time.Hour

type waiterEntry struct {
	ch      chan ActionEvent
	created time.Time
}

// WebhookHandler manages pending action callbacks from ntfy.sh.
// When a notification with actions is sent via the ntfy channel,
// the handler registers a unique token and returns a channel that
// receives the action event when the user taps a button on their phone.
type WebhookHandler struct {
	mu      sync.Mutex
	waiters map[string]*waiterEntry

	http   *http.Server
	ln     net.Listener
}

// ActionEvent represents an action triggered by the user via ntfy.
type ActionEvent struct {
	ActionKey string
}

// NewWebhookHandler creates a new webhook handler.
func NewWebhookHandler() *WebhookHandler {
	return &WebhookHandler{
		waiters: make(map[string]*waiterEntry),
	}
}

// Register creates a new pending action waiter and returns the token
// and a channel that will receive the action event.
func (h *WebhookHandler) Register() (token string, ch chan ActionEvent) {
	token = generateToken()
	ch = make(chan ActionEvent, 1)
	h.mu.Lock()
	h.waiters[token] = &waiterEntry{ch: ch, created: time.Now()}
	h.mu.Unlock()
	return token, ch
}

// Unregister removes a pending waiter and closes its channel.
func (h *WebhookHandler) Unregister(token string) {
	h.mu.Lock()
	entry, ok := h.waiters[token]
	delete(h.waiters, token)
	h.mu.Unlock()
	if ok {
		close(entry.ch)
	}
}

// Dispatch delivers an action event to the waiter identified by token.
// Returns true if the token was found and the event was delivered.
func (h *WebhookHandler) Dispatch(token, actionKey string) bool {
	h.mu.Lock()
	entry, ok := h.waiters[token]
	if ok {
		delete(h.waiters, token)
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case entry.ch <- ActionEvent{ActionKey: actionKey}:
	default:
	}
	close(entry.ch)
	return true
}

// cleanupExpired removes tokens older than ttl. Safe to call concurrently.
func (h *WebhookHandler) cleanupExpired(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	h.mu.Lock()
	for token, entry := range h.waiters {
		if entry.created.Before(cutoff) {
			delete(h.waiters, token)
			close(entry.ch)
		}
	}
	h.mu.Unlock()
}

// StartCleanup runs a background goroutine that periodically removes
// expired tokens. Stop it by cancelling ctx.
func (h *WebhookHandler) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.cleanupExpired(defaultTokenTTL)
			}
		}
	}()
}

// Listen starts a TCP listener for webhook callbacks on addr.
func (h *WebhookHandler) Listen(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleWebhook)

	h.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen webhook on %s: %w", addr, err)
	}
	h.ln = ln
	return nil
}

// Serve starts the webhook server. Blocks until Shutdown is called.
// Call Listen before Serve.
func (h *WebhookHandler) Serve() error {
	if h.ln == nil {
		return fmt.Errorf("webhook server not listening, call Listen first")
	}
	slog.Info("ntfy webhook server listening", "address", h.ln.Addr())
	err := h.http.Serve(h.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the webhook server.
func (h *WebhookHandler) Shutdown(ctx context.Context) error {
	if h.http == nil {
		return nil
	}
	return h.http.Shutdown(ctx)
}

// Address returns the bound address (for tests).
func (h *WebhookHandler) Address() string {
	if h.ln == nil {
		return ""
	}
	return h.ln.Addr().String()
}

func (h *WebhookHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *WebhookHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	token := strings.Trim(r.URL.Path, "/")
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing token"})
		return
	}

	actionKey := r.URL.Query().Get("action")
	if actionKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing action"})
		return
	}

	if !h.Dispatch(token, actionKey) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "token expired or invalid"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("write response failed", "error", err)
	}
}

func generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
