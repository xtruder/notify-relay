package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/condition"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
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
	Routes        []Route       `json:"routes"`
	RemoteTimeout time.Duration `json:"remote_timeout,omitempty"` // Timeout for remote forwarding (default: 30s)
}

// Router routes notifications to appropriate channels based on conditions
type Router struct {
	config        Config
	channels      map[string]channel.Channel
	conditions    map[string]condition.Condition
	manager       *remotes.Manager
	mu            sync.RWMutex
	remoteTimeout time.Duration
}

// New creates a new router with the given configuration, evaluator, and channels
func New(cfg Config, evaluator condition.Evaluator, channels []channel.Channel) (*Router, error) {
	r := &Router{
		config:     cfg,
		channels:   make(map[string]channel.Channel, len(channels)),
		conditions: make(map[string]condition.Condition),
	}

	// Set default remote timeout if not specified
	if cfg.RemoteTimeout > 0 {
		r.remoteTimeout = cfg.RemoteTimeout
	} else {
		r.remoteTimeout = 30 * time.Second
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

	// Set default remote timeout if not specified
	if cfg.RemoteTimeout > 0 {
		r.remoteTimeout = cfg.RemoteTimeout
	} else {
		r.remoteTimeout = 30 * time.Second
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

// RemoteAvailableCondition checks if any remote is connected
type RemoteAvailableCondition struct {
	manager *remotes.Manager
}

func (r *RemoteAvailableCondition) Name() string {
	return "remote_available"
}

func (r *RemoteAvailableCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.manager.HasConnectedRemote()
}

// RemoteUnlockedCondition checks if any remote is unlocked
type RemoteUnlockedCondition struct {
	manager *remotes.Manager
}

func (r *RemoteUnlockedCondition) Name() string {
	return "remote_unlocked"
}

func (r *RemoteUnlockedCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.manager.HasUnlockedRemote()
}

// Notify routes a notification to the appropriate channel
// For remote forwarding routes, it will wait for the remote response with timeout
// If remote forwarding fails or times out, it falls back to the next matching route
func (r *Router) Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Evaluate routes in order, send to first matching channel
	for i, route := range r.config.Routes {
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
				resp, forwarded := r.tryForwardToRemote(ctx, req, i)
				if forwarded {
					return resp, nil
				}
				// Forwarding failed or timed out, continue to next route (fallback)
				continue
			}
			continue
		}

		return ch.Send(ctx, req)
	}

	return protocol.NotifyResponse{}, fmt.Errorf("no matching route found")
}

// tryForwardToRemote attempts to forward notification to best remote
// Returns (response, true) if successful, (zero, false) if failed/timeout
func (r *Router) tryForwardToRemote(ctx context.Context, req protocol.NotifyRequest, routeIndex int) (protocol.NotifyResponse, bool) {
	remote := r.manager.FindBestRemote()
	if remote == nil {
		return protocol.NotifyResponse{}, false
	}

	// Create forwarded notification
	forwarded := &notify_relayv1.ForwardedNotification{
		SourceHostname: "server",
		Notification: &notify_relayv1.Notification{
			AppName:       req.AppName,
			Summary:       req.Summary,
			Body:          req.Body,
			AppIcon:       req.AppIcon,
			ExpireTimeout: req.ExpireTimeout,
			Hints:         make(map[string]string),
		},
	}

	// Add hints
	for _, hint := range req.Hints {
		forwarded.Notification.Hints[hint.Name] = hint.Value
	}

	// Forward with timeout
	forwardCtx, cancel := context.WithTimeout(ctx, r.remoteTimeout)
	defer cancel()

	resp, err := r.manager.ForwardNotification(forwardCtx, remote.Hostname, forwarded)
	if err != nil {
		return protocol.NotifyResponse{}, false
	}

	// Response received successfully
	return protocol.NotifyResponse{
		ID:        resp.Id,
		Event:     resp.Event,
		Reason:    resp.Reason,
		ActionKey: resp.ActionKey,
	}, true
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
