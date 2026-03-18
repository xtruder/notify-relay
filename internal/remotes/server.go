package remotes

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

// Server handles incoming remote client connections
type Server struct {
	manager      *Manager
	timeout      time.Duration
	pingInterval time.Duration
}

// NewServer creates a new remote server handler
func NewServer(manager *Manager) *Server {
	return &Server{
		manager:      manager,
		timeout:      5 * time.Minute,
		pingInterval: 30 * time.Second,
	}
}

// HandleConnect handles a client connection stream
func (s *Server) HandleConnect(stream notify_relayv1.RelayService_ConnectServer) error {
	ctx := stream.Context()

	// Wait for first message to get client info (hostname from lock state update)
	firstMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive initial message: %w", err)
	}

	lockUpdate := firstMsg.GetLockState()
	if lockUpdate == nil {
		return fmt.Errorf("expected initial LockStateUpdate from client")
	}

	hostname := lockUpdate.Hostname
	if hostname == "" {
		return fmt.Errorf("client hostname cannot be empty")
	}

	// Create remote info
	remote := &Remote{
		Hostname:     hostname,
		IsLocked:     lockUpdate.IsLocked,
		Priority:     0, // Will be configured
		Type:         RemoteTypeInbound,
		ServerStream: stream,
		ConnectedAt:  time.Now(),
		LastSeen:     time.Now(),
		ResponseChan: make(chan *notify_relayv1.NotificationResponse, 1),
	}

	// Add remote to manager
	if err := s.manager.AddRemote(remote); err != nil {
		return err
	}
	defer s.manager.RemoveRemote(hostname)

	slog.Info("remote inbound connected", "hostname", hostname, "locked", remote.IsLocked)

	// Start goroutine to send periodic pings
	pingDone := make(chan struct{})
	go s.sendPings(stream, pingDone)

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			close(pingDone)
			return ctx.Err()
		default:
		}

		msg, err := stream.Recv()
		if err == io.EOF {
			close(pingDone)
			slog.Info("remote client disconnected", "hostname", hostname)
			return nil
		}
		if err != nil {
			close(pingDone)
			return fmt.Errorf("receive error from %s: %w", hostname, err)
		}

		// Update last seen
		s.manager.UpdateLastSeen(hostname)

		// Get remote for handling message
		remote, _ := s.manager.GetRemote(hostname)
		if remote == nil {
			continue
		}

		// Handle message
		if err := s.handleClientMessage(hostname, msg, remote); err != nil {
			slog.Error("error handling message", "hostname", hostname, "error", err)
		}
	}
}

func (s *Server) handleClientMessage(hostname string, msg *notify_relayv1.ClientMessage, remote *Remote) error {
	switch m := msg.Message.(type) {
	case *notify_relayv1.ClientMessage_LockState:
		s.manager.UpdateLockState(hostname, m.LockState.IsLocked)
		slog.Debug("lock state update", "hostname", hostname, "locked", m.LockState.IsLocked)

	case *notify_relayv1.ClientMessage_Ping:
		// Just updating last seen is enough, pong sent by sendPings goroutine

	case *notify_relayv1.ClientMessage_Response:
		// Forward response to the waiting request
		if remote.ResponseChan != nil {
			select {
			case remote.ResponseChan <- m.Response:
			default:
				// Channel full or closed, drop response
			}
		}

	default:
		return fmt.Errorf("unknown message type from %s", hostname)
	}

	return nil
}

func (s *Server) sendPings(stream notify_relayv1.RelayService_ConnectServer, done chan struct{}) {
	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			msg := &notify_relayv1.ServerMessage{
				Message: &notify_relayv1.ServerMessage_Pong{
					Pong: &notify_relayv1.HealthPong{
						Timestamp: time.Now().Unix(),
					},
				},
			}
			if err := stream.Send(msg); err != nil {
				// Client likely disconnected, exit
				return
			}
		}
	}
}

// StartCleanup starts a goroutine to cleanup disconnected clients
func (s *Server) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.timeout / 2)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				removed := s.manager.CleanupDisconnected(s.timeout)
				for _, hostname := range removed {
					slog.Info("cleaned up disconnected client", "hostname", hostname)
				}
			}
		}
	}()
}
