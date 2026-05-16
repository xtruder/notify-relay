package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/condition"
	"github.com/xtruder/notify-relay/internal/p2p"
	"github.com/xtruder/notify-relay/internal/protocol"
)

// ErrNoMatchingRoute is returned when no route condition matched.
var ErrNoMatchingRoute = errors.New("no matching route found")

// Route defines a routing rule: when condition matches, use this channel
type Route struct {
	Condition string `toml:"condition" json:"condition"` // e.g., "best_active", "no_better_peer", "always"
	Channel   string `toml:"channel" json:"channel"`     // channel name
}

// Config holds router configuration
type Config struct {
	Routes []Route `toml:"routes" json:"routes"`
}

// Router routes notifications to appropriate channels based on conditions
type Router struct {
	config     Config
	channels   map[string]channel.Channel
	conditions map[string]condition.Condition
	mu         sync.RWMutex
}

// New creates a new router with the given configuration, evaluator, and channels
func New(cfg Config, evaluator condition.Evaluator, channels []channel.Channel) (*Router, error) {
	r := &Router{
		config:     cfg,
		channels:   make(map[string]channel.Channel, len(channels)),
		conditions: make(map[string]condition.Condition),
	}

	for _, ch := range channels {
		r.channels[ch.Name()] = ch
	}

	r.conditions[condition.Always{}.Name()] = condition.Always{}
	if evaluator != nil {
		r.conditions[condition.NewScreenLocked(evaluator).Name()] = condition.NewScreenLocked(evaluator)
	}

	return r, nil
}

// NewWithP2P creates a router with p2p peer-aware conditions
func NewWithP2P(cfg Config, evaluator condition.Evaluator, channels []channel.Channel, table *p2p.PeerTable) (*Router, error) {
	r := &Router{
		config:     cfg,
		channels:   make(map[string]channel.Channel, len(channels)),
		conditions: make(map[string]condition.Condition),
	}

	for _, ch := range channels {
		r.channels[ch.Name()] = ch
	}

	r.conditions[condition.Always{}.Name()] = condition.Always{}
	if evaluator != nil {
		r.conditions[condition.NewScreenLocked(evaluator).Name()] = condition.NewScreenLocked(evaluator)
	}

	if table != nil {
		r.conditions["remote_available"] = &RemoteAvailableCondition{table: table}
		r.conditions["remote_present"] = &RemotePresentCondition{table: table}
		r.conditions["best_active"] = &BestActiveCondition{table: table}
		r.conditions["no_better_peer"] = &NoBetterPeerCondition{table: table}
	}

	return r, nil
}

// RemoteAvailableCondition checks if any remote peer is connected
type RemoteAvailableCondition struct {
	table *p2p.PeerTable
}

func (r *RemoteAvailableCondition) Name() string {
	return "remote_available"
}

func (r *RemoteAvailableCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.table.HasConnectedPeer(p2p.StaleTimeout)
}

// RemotePresentCondition checks if any remote peer is present (can handle notifications).
type RemotePresentCondition struct {
	table *p2p.PeerTable
}

func (r *RemotePresentCondition) Name() string {
	return "remote_present"
}

func (r *RemotePresentCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return r.table.HasPresentPeer(p2p.StaleTimeout)
}

// BestActiveCondition returns true if the local node is the best (lowest priority)
// present node among all known nodes. Use this on desktop/laptop nodes to display
// notifications only when this node should be the primary handler.
type BestActiveCondition struct {
	table *p2p.PeerTable
}

func (b *BestActiveCondition) Name() string {
	return "best_active"
}

func (b *BestActiveCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return b.table.IsBestActive(p2p.StaleTimeout)
}

// NoBetterPeerCondition returns true when no present node exists anywhere
// and the local node has the highest priority number (worst priority = last resort).
// Use this on always-on nodes (VPS) to forward to ntfy.sh as a fallback.
type NoBetterPeerCondition struct {
	table *p2p.PeerTable
}

func (n *NoBetterPeerCondition) Name() string {
	return "no_better_peer"
}

func (n *NoBetterPeerCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return n.table.IsNoBetterPeer(p2p.StaleTimeout)
}

// Notify routes a notification to the appropriate channel.
// Routes are evaluated in order — first match wins.
func (r *Router) Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, route := range r.config.Routes {
		cond, ok := r.conditions[route.Condition]
		if !ok {
			slog.Debug("unknown condition", "condition", route.Condition)
			continue
		}

		if !cond.Evaluate(ctx, req) {
			continue
		}

		ch, ok := r.channels[route.Channel]
		if !ok {
			slog.Debug("unknown channel in route", "channel", route.Channel)
			continue
		}

		resp, err := ch.Send(ctx, req)
		if err != nil && errors.Is(err, channel.ErrUnavailable) {
			slog.Warn("channel unavailable, trying next route", "channel", route.Channel)
			continue
		}
		return resp, err
	}

	return protocol.NotifyResponse{}, ErrNoMatchingRoute
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

	for i := len(r.config.Routes) - 1; i >= 0; i-- {
		route := r.config.Routes[i]
		if ch, ok := r.channels[route.Channel]; ok {
			return ch.ServerInfo(ctx)
		}
	}

	return protocol.ServerInfoResponse{}, fmt.Errorf("no channels available")
}
