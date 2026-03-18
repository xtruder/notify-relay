package remotes

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/xtruder/notify-relay/internal/lock"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client represents a client connection to a remote server
type Client struct {
	serverAddr     string
	hostname       string
	token          string
	conn           *grpc.ClientConn
	stream         notify_relayv1.RelayService_ConnectClient
	lockState      *lock.Detector
	mu             sync.RWMutex
	connected      bool
	onConnect      func()
	onDisconnect   func()
	onNotification func(*notify_relayv1.ForwardedNotification)

	// Reconnection settings
	reconnectInterval time.Duration
	maxReconnectDelay time.Duration
}

// ClientConfig holds configuration for the client
type ClientConfig struct {
	ServerAddr string
	Hostname   string
	Token      string
	LockState  *lock.Detector
}

// NewClient creates a new remote client
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		serverAddr:        cfg.ServerAddr,
		hostname:          cfg.Hostname,
		token:             cfg.Token,
		lockState:         cfg.LockState,
		reconnectInterval: 5 * time.Second,
		maxReconnectDelay: 5 * time.Minute,
	}
}

// SetCallbacks sets connection and notification callbacks
func (c *Client) SetCallbacks(onConnect, onDisconnect func(), onNotification func(*notify_relayv1.ForwardedNotification)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onConnect = onConnect
	c.onDisconnect = onDisconnect
	c.onNotification = onNotification
}

// IsConnected returns true if client is currently connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Connect establishes connection to the server with auto-reconnect
func (c *Client) Connect(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := c.connectOnce(ctx)
		if err == nil {
			// Successful connection, wait for disconnect
			err = c.handleStream(ctx)
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		slog.Info("connection to server failed, reconnecting", "error", err, "delay", c.reconnectInterval)

		// Exponential backoff
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.reconnectInterval):
			// Increase delay for next attempt
			c.reconnectInterval = min(c.reconnectInterval*2, c.maxReconnectDelay)
		}
	}
}

func (c *Client) connectOnce(ctx context.Context) error {
	// Setup connection options
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	var conn *grpc.ClientConn
	var err error

	// Check if serverAddr is a Unix socket path
	if strings.HasPrefix(c.serverAddr, "/") || strings.HasPrefix(c.serverAddr, ".") {
		// Unix socket - use custom dialer
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.serverAddr)
		}))
		conn, err = grpc.Dial("passthrough:///unix", opts...)
	} else {
		// TCP
		conn, err = grpc.Dial(c.serverAddr, opts...)
	}

	if err != nil {
		return fmt.Errorf("failed to dial server: %w", err)
	}

	// Create gRPC client
	client := notify_relayv1.NewRelayServiceClient(conn)

	// Open bidirectional stream
	stream, err := client.Connect(ctx)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to open stream: %w", err)
	}

	// Send initial lock state
	isLocked := false
	if c.lockState != nil {
		isLocked = c.lockState.IsLocked()
	}

	initialMsg := &notify_relayv1.ClientMessage{
		Message: &notify_relayv1.ClientMessage_LockState{
			LockState: &notify_relayv1.LockStateUpdate{
				Hostname:  c.hostname,
				IsLocked:  isLocked,
				Timestamp: time.Now().Unix(),
			},
		},
	}

	if err := stream.Send(initialMsg); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send initial message: %w", err)
	}

	// Store connection
	c.mu.Lock()
	c.conn = conn
	c.stream = stream
	c.connected = true
	onConnect := c.onConnect
	c.mu.Unlock()

	// Reset reconnection interval on successful connection
	c.reconnectInterval = 5 * time.Second

	if onConnect != nil {
		onConnect()
	}

	slog.Info("connected to server", "address", c.serverAddr)
	return nil
}

func (c *Client) handleStream(ctx context.Context) error {
	// Start goroutine to watch and report lock state changes
	lockDone := make(chan struct{})
	go c.watchLockState(lockDone)
	defer close(lockDone)

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := c.stream.Recv()
		if err == io.EOF {
			c.setDisconnected()
			return fmt.Errorf("server closed connection")
		}
		if err != nil {
			c.setDisconnected()
			return fmt.Errorf("receive error: %w", err)
		}

		// Handle message
		if err := c.handleServerMessage(msg); err != nil {
			slog.Error("error handling server message", "error", err)
		}
	}
}

func (c *Client) handleServerMessage(msg *notify_relayv1.ServerMessage) error {
	switch m := msg.Message.(type) {
	case *notify_relayv1.ServerMessage_Pong:
		// Health check response, nothing to do

	case *notify_relayv1.ServerMessage_Notification:
		// Received forwarded notification from server
		c.mu.RLock()
		onNotification := c.onNotification
		stream := c.stream
		c.mu.RUnlock()

		if onNotification != nil {
			onNotification(m.Notification)
		}

		// Send acknowledgment response back to server
		if stream != nil {
			resp := &notify_relayv1.ClientMessage{
				Message: &notify_relayv1.ClientMessage_Response{
					Response: &notify_relayv1.NotificationResponse{
						Id:    1, // Simple ack ID
						Event: "ack",
					},
				},
			}
			if err := stream.Send(resp); err != nil {
				slog.Error("failed to send notification ack", "error", err)
			}
		}

	case *notify_relayv1.ServerMessage_ClientList:
		// Client list update, can be used for UI/debugging
		slog.Debug("connected clients", "count", len(m.ClientList.Clients))

	default:
		return fmt.Errorf("unknown message type from server")
	}

	return nil
}

func (c *Client) watchLockState(done chan struct{}) {
	if c.lockState == nil {
		return
	}

	// Watch for lock state changes
	c.lockState.SetChangeCallback(func(locked bool) {
		c.mu.RLock()
		stream := c.stream
		connected := c.connected
		c.mu.RUnlock()

		if !connected || stream == nil {
			return
		}

		msg := &notify_relayv1.ClientMessage{
			Message: &notify_relayv1.ClientMessage_LockState{
				LockState: &notify_relayv1.LockStateUpdate{
					Hostname:  c.hostname,
					IsLocked:  locked,
					Timestamp: time.Now().Unix(),
				},
			},
		}

		if err := stream.Send(msg); err != nil {
			slog.Error("failed to send lock state update", "error", err)
		} else {
			slog.Debug("sent lock state update", "locked", locked)
		}
	})

	// Wait for done signal
	<-done
}

func (c *Client) setDisconnected() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return
	}

	c.connected = false
	if c.conn != nil {
		c.conn.Close()
	}

	onDisconnect := c.onDisconnect
	c.mu.Unlock()

	if onDisconnect != nil {
		onDisconnect()
	}
	c.mu.Lock()

	slog.Info("disconnected from server", "address", c.serverAddr)
}

// Close closes the client connection
func (c *Client) Close() error {
	c.setDisconnected()
	return nil
}

// SendResponse sends a notification response back to the server
func (c *Client) SendResponse(resp *notify_relayv1.NotificationResponse) error {
	c.mu.RLock()
	stream := c.stream
	c.mu.RUnlock()

	if stream == nil {
		return fmt.Errorf("not connected")
	}

	msg := &notify_relayv1.ClientMessage{
		Message: &notify_relayv1.ClientMessage_Response{
			Response: resp,
		},
	}

	return stream.Send(msg)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
