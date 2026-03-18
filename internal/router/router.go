package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/condition"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/remotes"
)

// Route defines a routing rule: when condition matches, use this channel
type Route struct {
	Condition string `json:"condition"` // e.g., "screen_locked", "always", "remote_unlocked"
	Channel   string `json:"channel"`   // channel name
}

// Config holds router configuration
type Config struct {
	Routes []Route `json:"routes"`
}

// Router routes notifications to appropriate channels based on conditions
type Router struct {
	config     Config
	channels   map[string]channel.Channel
	conditions map[string]condition.Condition
	manager    *remotes.Manager
	mu         sync.RWMutex
}

// New creates a new router with the given configuration, evaluator, and channels
func New(cfg Config, evaluator condition.Evaluator, channels []channel.Channel) (*Router, error) {
	r := &Router{
		config:     cfg,
		channels:   make(map[string]channel.Channel, len(channels)),
		conditions: make(map[string]condition.Condition),
	}

	// Store channels by name
	for _, ch := range channels {
		r.channels[ch.Name()] = ch
	}

	// Register conditions
	r.conditions[condition.Always{}.Name()] = condition.Always{}
	if evaluator != nil {
		r.conditions[condition.NewScreenLocked(evaluator).Name()] = condition.NewScreenLocked(evaluator)
	}

	return r, nil
}

// NewWithRemotes creates a router with remote client forwarding support
func NewWithRemotes(cfg Config, evaluator condition.Evaluator, channels []channel.Channel, manager *remotes.Manager) (*Router, error) {
	r := &Router{
		config:     cfg,
		channels:   make(map[string]channel.Channel, len(channels)),
		conditions: make(map[string]condition.Condition),
		manager:    manager,
	}

	// Store channels by name
	for _, ch := range channels {
		r.channels[ch.Name()] = ch
	}

	// Register conditions
	r.conditions[condition.Always{}.Name()] = condition.Always{}
	if evaluator != nil {
		r.conditions[condition.NewScreenLocked(evaluator).Name()] = condition.NewScreenLocked(evaluator)
	}

	// Register remote conditions if manager is available
	if manager != nil {
		r.conditions["remote_available"] = &RemoteAvailableCondition{manager: manager}
		r.conditions["remote_unlocked"] = &RemoteUnlockedCondition{manager: manager}
	}

	return r, nil
}

// RemoteAvailableCondition checks if any remote client is connected
type RemoteAvailableCondition struct {
	manager *remotes.Manager
}

func (r *RemoteAvailableCondition) Name() string {
	return "remote_available"
}

func (r *RemoteAvailableCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.manager.HasConnectedClient()
}

// RemoteUnlockedCondition checks if any remote client is unlocked
type RemoteUnlockedCondition struct {
	manager *remotes.Manager
}

func (r *RemoteUnlockedCondition) Name() string {
	return "remote_unlocked"
}

func (r *RemoteUnlockedCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.manager.HasUnlockedClient()
}

// Notify routes a notification to the appropriate channel
func (r *Router) Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Evaluate routes in order, send to first matching channel
	for _, route := range r.config.Routes {
		cond, ok := r.conditions[route.Condition]
		if !ok {
			continue
		}

		if !cond.Evaluate(ctx, req) {
			continue
		}

		ch, ok := r.channels[route.Channel]
		if !ok {
			// Special case: "forward" channel means forward to remote
			if route.Channel == "forward" && r.manager != nil {
				client := r.manager.FindBestClient()
				if client != nil {
					// Return a special response indicating remote forwarding
					// The caller (server) handles the actual forwarding
					return protocol.NotifyResponse{}, ErrForwardToRemote{Hostname: client.Hostname}
				}
			}
			continue
		}

		return ch.Send(ctx, req)
	}

	return protocol.NotifyResponse{}, fmt.Errorf("no matching route found")
}

// ErrForwardToRemote is returned when notification should be forwarded to a remote client
type ErrForwardToRemote struct {
	Hostname string
}

func (e ErrForwardToRemote) Error() string {
	return fmt.Sprintf("forward to remote client: %s", e.Hostname)
}

// CloseNotification closes a notification on all channels that support it
func (r *Router) CloseNotification(ctx context.Context, id uint32) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var lastErr error
	for _, ch := range r.channels {
		if closer, ok := ch.(channel.Closer); ok {
			if err := closer.CloseNotification(ctx, id); err != nil {
				lastErr = err
			}
		}
	}

	return lastErr
}

// Capabilities returns capabilities from the primary channel (last route)
func (r *Router) Capabilities(ctx context.Context) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Use the last channel (should be the fallback "always" route)
	for i := len(r.config.Routes) - 1; i >= 0; i-- {
		route := r.config.Routes[i]
		if ch, ok := r.channels[route.Channel]; ok {
			return ch.Capabilities(ctx)
		}
	}

	return nil, fmt.Errorf("no channels available")
}

// ServerInformation returns server info from the primary channel
func (r *Router) ServerInformation(ctx context.Context) (protocol.ServerInfoResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Use the last channel (should be the fallback "always" route)
	for i := len(r.config.Routes) - 1; i >= 0; i-- {
		route := r.config.Routes[i]
		if ch, ok := r.channels[route.Channel]; ok {
			return ch.ServerInfo(ctx)
		}
	}

	return protocol.ServerInfoResponse{}, fmt.Errorf("no channels available")
}

// Close closes all channels
func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lastErr error
	for _, ch := range r.channels {
		if err := ch.Close(); err != nil {
			lastErr = err
		}
	}

	return lastErr
}
