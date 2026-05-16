package channel

import (
	"context"
	"errors"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// ErrUnavailable indicates a channel is temporarily or permanently unavailable.
// When a Send returns this error, the router skips this channel and tries
// the next matching route.
var ErrUnavailable = errors.New("channel unavailable")

// Channel defines the interface for notification delivery channels
type Channel interface {
	// Name returns the unique name of this channel
	Name() string

	// Send sends a notification through this channel
	Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error)

	// Close closes the channel and releases any resources
	Close() error

	// Capabilities returns the capabilities supported by this channel
	Capabilities(ctx context.Context) ([]string, error)

	// ServerInfo returns information about the notification server
	ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error)
}

// Closer is a notification that can be closed
type Closer interface {
	// CloseNotification closes a notification by ID
	CloseNotification(ctx context.Context, id uint32) error
}
