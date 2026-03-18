package remotes

import (
	"context"
	"fmt"
	"sync"
	"time"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

// RemoteType indicates the direction of the remote connection
type RemoteType string

const (
	RemoteTypeInbound  RemoteType = "inbound"  // Other connects to us (gRPC server stream)
	RemoteTypeOutbound RemoteType = "outbound" // We connect to other (gRPC client stream)
)

// Remote represents a peer that can receive notifications
type Remote struct {
	Hostname     string
	IsLocked     bool
	Priority     int
	Type         RemoteType
	ConnectedAt  time.Time
	LastSeen     time.Time
	ResponseChan chan *notify_relayv1.NotificationResponse

	// Connection-specific fields
	ServerStream notify_relayv1.RelayService_ConnectServer // For inbound (we send to them)
	ClientStream notify_relayv1.RelayService_ConnectClient // For outbound (we send to them)
}

// Manager tracks all remote connections (both inbound and outbound)
type Manager struct {
	remotes  map[string]*Remote // hostname -> remote
	mu       sync.RWMutex
	onChange func(hostname string, connected bool)
}

// NewManager creates a new remote manager
func NewManager() *Manager {
	return &Manager{
		remotes: make(map[string]*Remote),
	}
}

// SetChangeCallback sets a callback for connection changes
func (m *Manager) SetChangeCallback(cb func(hostname string, connected bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = cb
}

// AddRemote adds a new remote connection
func (m *Manager) AddRemote(remote *Remote) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.remotes[remote.Hostname]; exists {
		return fmt.Errorf("remote %s already connected", remote.Hostname)
	}

	m.remotes[remote.Hostname] = remote

	if m.onChange != nil {
		go m.onChange(remote.Hostname, true)
	}

	return nil
}

// RemoveRemote removes a remote connection
func (m *Manager) RemoveRemote(hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.remotes[hostname]; exists {
		delete(m.remotes, hostname)

		if m.onChange != nil {
			go m.onChange(hostname, false)
		}
	}
}

// UpdateLockState updates the lock state of a remote
func (m *Manager) UpdateLockState(hostname string, isLocked bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	remote, exists := m.remotes[hostname]
	if !exists {
		return false
	}

	remote.IsLocked = isLocked
	remote.LastSeen = time.Now()
	return true
}

// UpdateLastSeen updates the last seen timestamp
func (m *Manager) UpdateLastSeen(hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if remote, exists := m.remotes[hostname]; exists {
		remote.LastSeen = time.Now()
	}
}

// GetRemote retrieves a remote by hostname
func (m *Manager) GetRemote(hostname string) (*Remote, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	remote, exists := m.remotes[hostname]
	return remote, exists
}

// GetAllRemotes returns all connected remotes
func (m *Manager) GetAllRemotes() map[string]*Remote {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*Remote, len(m.remotes))
	for k, v := range m.remotes {
		result[k] = v
	}
	return result
}

// FindBestRemote returns the highest priority unlocked remote
// Returns nil if no unlocked remotes are connected
func (m *Manager) FindBestRemote() *Remote {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var bestRemote *Remote
	bestPriority := int(^uint(0) >> 1) // Max int

	for _, remote := range m.remotes {
		if !remote.IsLocked && remote.Priority < bestPriority {
			bestRemote = remote
			bestPriority = remote.Priority
		}
	}

	return bestRemote
}

// HasUnlockedRemote returns true if any remote is unlocked
func (m *Manager) HasUnlockedRemote() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, remote := range m.remotes {
		if !remote.IsLocked {
			return true
		}
	}
	return false
}

// HasConnectedRemote returns true if any remote is connected
func (m *Manager) HasConnectedRemote() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.remotes) > 0
}

// ForwardNotification sends a notification to a specific remote
func (m *Manager) ForwardNotification(ctx context.Context, hostname string, notification *notify_relayv1.ForwardedNotification) (*notify_relayv1.NotificationResponse, error) {
	remote, exists := m.GetRemote(hostname)
	if !exists {
		return nil, fmt.Errorf("remote %s not connected", hostname)
	}

	// Create response channel for this request
	responseChan := make(chan *notify_relayv1.NotificationResponse, 1)
	remote.ResponseChan = responseChan

	var err error
	if remote.Type == RemoteTypeInbound && remote.ServerStream != nil {
		// Send notification via server stream (inbound remote)
		msg := &notify_relayv1.ServerMessage{
			Message: &notify_relayv1.ServerMessage_Notification{
				Notification: notification,
			},
		}
		err = remote.ServerStream.Send(msg)
	} else if remote.Type == RemoteTypeOutbound && remote.ClientStream != nil {
		// For outbound connections, we'd need bidirectional streaming in the protocol
		// For now, this is not supported - outbound remotes are clients, not servers
		return nil, fmt.Errorf("sending notifications to outbound remotes not yet supported")
	} else {
		return nil, fmt.Errorf("remote %s has no valid stream", hostname)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to send to remote %s: %w", hostname, err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseChan:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response from remote %s", hostname)
	}
}

// CleanupDisconnected removes remotes that haven't been seen for a while
func (m *Manager) CleanupDisconnected(timeout time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := make([]string, 0)

	for hostname, remote := range m.remotes {
		if now.Sub(remote.LastSeen) > timeout {
			delete(m.remotes, hostname)
			removed = append(removed, hostname)
		}
	}

	return removed
}
