package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/router"
)

type Config struct {
	Listen    string `mapstructure:"listen" json:"listen"`
	Unix      string `mapstructure:"unix" json:"unix"`
	Token     string `mapstructure:"token" json:"token"`
	TokenFile string `mapstructure:"token_file" json:"token_file"`
}

type Server struct {
	router *router.Router
	cfg    Config
	http   *http.Server
	ln     net.Listener
}

func New(cfg Config, r *router.Router) (*Server, error) {
	s := &Server{cfg: cfg, router: r}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/notify", s.handleNotify)
	mux.HandleFunc("/close", s.handleClose)
	mux.HandleFunc("/capabilities", s.handleCapabilities)
	mux.HandleFunc("/server-info", s.handleServerInfo)

	s.http = &http.Server{
		Handler:           s.authMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	if cfg.Unix != "" {
		if err := os.RemoveAll(cfg.Unix); err != nil {
			return nil, fmt.Errorf("clear unix socket: %w", err)
		}
		ln, err := net.Listen("unix", cfg.Unix)
		if err != nil {
			return nil, fmt.Errorf("listen on unix socket: %w", err)
		}
		s.ln = ln
		return s, nil
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	s.ln = ln
	return s, nil
}

func (s *Server) Serve() error {
	slog.Info("notify-relayd listening", "address", s.ln.Addr())
	if s.cfg.Token != "" {
		slog.Info("notify-relayd bearer token configured")
	}
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if s.cfg.Token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, protocol.ErrorResponse{Error: "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, protocol.ErrorResponse{Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, protocol.ErrorResponse{Error: "method not allowed"})
		return
	}
	var req protocol.NotifyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	resp, err := s.router.Notify(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, protocol.ErrorResponse{Error: "method not allowed"})
		return
	}
	var req protocol.CloseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	if err := s.router.CloseNotification(r.Context(), req.ID); err != nil {
		writeJSON(w, http.StatusBadGateway, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, protocol.ErrorResponse{Error: "method not allowed"})
		return
	}
	capabilities, err := s.router.Capabilities(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, protocol.CapabilitiesResponse{Capabilities: capabilities})
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, protocol.ErrorResponse{Error: "method not allowed"})
		return
	}
	info, err := s.router.ServerInformation(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, protocol.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	if dec.More() {
		return errors.New("decode json: unexpected trailing data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("write response failed", "error", err)
	}
}
