package ntfy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/protocol"
)

// Config holds ntfy channel configuration
type Config struct {
	Server string `json:"server"` // e.g., "https://ntfy.sh"
	Topic  string `json:"topic"`  // e.g., "my-laptop-notifications"
	Token  string `json:"token"`  // optional access token
}

// Channel implements channel.Channel for ntfy.sh notifications
type Channel struct {
	config Config
	client *http.Client
}

// Compile-time interface check
var _ channel.Channel = (*Channel)(nil)

// New creates a new ntfy notification channel
func New(config Config) (*Channel, error) {
	if config.Server == "" {
		config.Server = "https://ntfy.sh"
	}
	if config.Topic == "" {
		return nil, fmt.Errorf("ntfy topic is required")
	}

	// Clean up server URL (remove trailing slash)
	config.Server = strings.TrimRight(config.Server, "/")

	return &Channel{
		config: config,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Name returns "ntfy"
func (c *Channel) Name() string {
	return "ntfy"
}

// Close is a no-op for ntfy (no persistent connection)
func (c *Channel) Close() error {
	return nil
}

// Send sends a notification to ntfy (implements channel.Channel)
// Uses the simple HTTP PUT/POST API: https://docs.ntfy.sh/publish/
func (c *Channel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	// Build URL: https://ntfy.sh/{topic}
	url := fmt.Sprintf("%s/%s", c.config.Server, c.config.Topic)

	// Create request with message body
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(req.Body))
	if err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("create ntfy request: %w", err)
	}

	// Set headers for additional metadata
	httpReq.Header.Set("Title", req.Summary)

	// Add priority based on urgency hint
	priority := c.priorityFromHints(req.Hints)
	if priority > 0 {
		httpReq.Header.Set("Priority", fmt.Sprintf("%d", priority))
	}

	// Add tags if we have a category hint
	for _, hint := range req.Hints {
		if hint.Name == "category" && hint.Value != "" {
			httpReq.Header.Set("Tags", hint.Value)
			break
		}
	}

	// Add authorization if token is configured
	if c.config.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.Token)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("send to ntfy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return protocol.NotifyResponse{}, fmt.Errorf("ntfy returned %s", resp.Status)
	}

	// Generate a pseudo-ID for ntfy notifications
	// Since ntfy doesn't have a notification ID system like dbus,
	// we generate one based on time
	return protocol.NotifyResponse{
		ID: uint32(time.Now().Unix()),
	}, nil
}

// Capabilities returns limited capabilities for ntfy
func (c *Channel) Capabilities(ctx context.Context) ([]string, error) {
	// ntfy supports basic text notifications
	return []string{"body", "icon-static"}, nil
}

// ServerInfo returns information about the ntfy server
func (c *Channel) ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{
		Name:    "ntfy",
		Vendor:  "ntfy.sh",
		Version: "1.0",
		Spec:    "1.2",
	}, nil
}

// priorityFromHints extracts priority from urgency hint
// ntfy priorities: 1=min, 3=default, 4=high, 5=max
func (c *Channel) priorityFromHints(hints []protocol.Hint) int {
	for _, hint := range hints {
		if hint.Name == "urgency" {
			switch hint.Value {
			case "0": // low
				return 1
			case "1": // normal
				return 3
			case "2": // critical
				return 5
			}
		}
	}
	return 3 // default
}
