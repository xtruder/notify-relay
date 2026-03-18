package remotes

import (
	"context"
	"fmt"
	"sync"
	"time"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

// ClientInfo represents a connected remote client
type ClientInfo struct {
	Hostname     string
	IsLocked     bool
	Priority     int
	ServerStream notify_relayv1.RelayService_ConnectServer
	ConnectedAt  time.Time
	LastSeen     time.Time
	ResponseChan chan *notify_relayv1.NotificationResponse
}

// Manager tracks all connected remote clients
type Manager struct {
	clients  map[string]*ClientInfo // hostname -> client
	mu       sync.RWMutex
	onChange func(hostname string, connected bool)
}

// NewManager creates a new remote client manager
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*ClientInfo),
	}
}

// SetChangeCallback sets a callback to be called when client connections change
func (m *Manager) SetChangeCallback(cb func(hostname string, connected bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = cb
}

// AddClient adds a new client to the manager
func (m *Manager) AddClient(client *ClientInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.clients[client.Hostname]; exists {
		return fmt.Errorf("client %s already connected", client.Hostname)
	}

	m.clients[client.Hostname] = client

	if m.onChange != nil {
		go m.onChange(client.Hostname, true)
	}

	return nil
}

// RemoveClient removes a client from the manager
func (m *Manager) RemoveClient(hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.clients[hostname]; exists {
		delete(m.clients, hostname)

		if m.onChange != nil {
			go m.onChange(hostname, false)
		}
	}
}

// UpdateLockState updates the lock state of a client
func (m *Manager) UpdateLockState(hostname string, isLocked bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, exists := m.clients[hostname]
	if !exists {
		return false
	}

	client.IsLocked = isLocked
	client.LastSeen = time.Now()
	return true
}

// UpdateLastSeen updates the last seen timestamp for a client
func (m *Manager) UpdateLastSeen(hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, exists := m.clients[hostname]; exists {
		client.LastSeen = time.Now()
	}
}

// GetClient retrieves a client by hostname
func (m *Manager) GetClient(hostname string) (*ClientInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, exists := m.clients[hostname]
	return client, exists
}

// GetAllClients returns a copy of all connected clients
func (m *Manager) GetAllClients() map[string]*ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ClientInfo, len(m.clients))
	for k, v := range m.clients {
		result[k] = v
	}
	return result
}

// FindBestClient returns the highest priority unlocked client
// Returns nil if no unlocked clients are connected
func (m *Manager) FindBestClient() *ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var bestClient *ClientInfo
	bestPriority := int(^uint(0) >> 1) // Max int

	for _, client := range m.clients {
		if !client.IsLocked && client.Priority < bestPriority {
			bestClient = client
			bestPriority = client.Priority
		}
	}

	return bestClient
}

// HasUnlockedClient returns true if any client is unlocked
func (m *Manager) HasUnlockedClient() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clients {
		if !client.IsLocked {
			return true
		}
	}
	return false
}

// HasConnectedClient returns true if any client is connected
func (m *Manager) HasConnectedClient() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.clients) > 0
}

// ForwardNotification sends a notification to a specific client
func (m *Manager) ForwardNotification(ctx context.Context, hostname string, notification *notify_relayv1.ForwardedNotification) (*notify_relayv1.NotificationResponse, error) {
	client, exists := m.GetClient(hostname)
	if !exists {
		return nil, fmt.Errorf("client %s not connected", hostname)
	}

	// Create response channel for this request
	responseChan := make(chan *notify_relayv1.NotificationResponse, 1)
	client.ResponseChan = responseChan

	// Send notification via stream
	msg := &notify_relayv1.ServerMessage{
		Message: &notify_relayv1.ServerMessage_Notification{
			Notification: notification,
		},
	}

	if err := client.ServerStream.Send(msg); err != nil {
		return nil, fmt.Errorf("failed to send to client %s: %w", hostname, err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseChan:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response from client %s", hostname)
	}
}

// CleanupDisconnected removes clients that haven't been seen for a while
func (m *Manager) CleanupDisconnected(timeout time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := make([]string, 0)

	for hostname, client := range m.clients {
		if now.Sub(client.LastSeen) > timeout {
			delete(m.clients, hostname)
			removed = append(removed, hostname)
		}
	}

	return removed
}

// GetClientListUpdate returns a ClientListUpdate message for broadcasting
func (m *Manager) GetClientListUpdate() *notify_relayv1.ClientListUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	clients := make([]*notify_relayv1.RemoteClient, 0, len(m.clients))
	for hostname, client := range m.clients {
		clients = append(clients, &notify_relayv1.RemoteClient{
			Hostname:    hostname,
			IsLocked:    client.IsLocked,
			Priority:    int32(client.Priority),
			IsConnected: true,
		})
	}

	return &notify_relayv1.ClientListUpdate{
		Clients: clients,
	}
}

// BroadcastClientList sends the client list to all connected clients
func (m *Manager) BroadcastClientList() {
	update := m.GetClientListUpdate()
	clients := m.GetAllClients()

	for hostname, client := range clients {
		msg := &notify_relayv1.ServerMessage{
			Message: &notify_relayv1.ServerMessage_ClientList{
				ClientList: update,
			},
		}

		// Don't block on send failures
		go func(c *ClientInfo, h string) {
			if err := c.ServerStream.Send(msg); err != nil {
				// Client might be disconnected, will be cleaned up later
			}
		}(client, hostname)
	}
}
