package p2p

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/errgroup"
)

// NodeConfig holds all configuration for a p2p node.
type NodeConfig struct {
	// ChannelID is the shared secret channel identifier.
	ChannelID string

	// Hostname is this node's hostname for display.
	Hostname string

	// Priority for notification routing (lower = higher priority).
	Priority int

	// IsPresent returns whether this node can currently handle notifications
	// (has a working display channel like dbus AND screen is unlocked).
	IsPresent func() bool

	// NotifyHandler is called when a notification is received from the network.
	// The handler (typically the router) evaluates local conditions to decide
	// whether to act on the notification.
	NotifyHandler NotifyHandler

	// Host configuration
	ListenAddrs    []string
	IdentityPath   string
	BootstrapPeers []string
	EnableMDNS     bool

	// RelayMode runs a circuit relay v2 service.
	// When ChannelID is also set, the node participates in the channel
	// as well (relay + participant mode for VPS nodes).
	RelayMode bool
}

// Node is the top-level p2p node tying together host, discovery, pubsub, presence, and notifications.
//
// Lifecycle:
//   - NewNode constructs the libp2p host and all service objects, but does
//     not start any service goroutines.
//   - Run(ctx) blocks running all sub-services until ctx is cancelled.
//     Before returning it closes the libp2p host.
//
// Typical use:
//
//	node, err := p2p.NewNode(ctx, cfg)
//	if err != nil { ... }
//	if err := node.Run(ctx); err != nil { ... }
type Node struct {
	Host      host.Host
	PeerTable *PeerTable
	Presence  *PresenceService
	Notify    *NotifyService

	cfg       NodeConfig
	dht       *dht.IpfsDHT
	discovery *Discovery
	pubsub    *PubSub
}

// NewNode constructs a p2p node: creates the libp2p host and allocates the
// discovery, presence and notify services. No bootstrap connections, relay
// setup, or background goroutines are started until Run is called.
func NewNode(ctx context.Context, cfg NodeConfig) (*Node, error) {
	// Validate config before acquiring any resources.
	if cfg.ChannelID == "" {
		return nil, fmt.Errorf("channel ID is required")
	}

	if cfg.Hostname == "" {
		h, err := os.Hostname()
		if err != nil {
			cfg.Hostname = "unknown"
		} else {
			cfg.Hostname = h
		}
	}

	// Parse bootstrap peers early — needed for both host config and discovery.
	bootstrapPIs := parseBootstrapPeers(cfg.BootstrapPeers)

	// Build DHT options for host-level routing (enables AutoRelay peer discovery).
	dhtOpts := buildDHTOpts(cfg.RelayMode, bootstrapPIs)

	// Identify which bootstrap peers have relay capability (user's VPS).
	// These are used as preferred static relays for AutoRelay.
	var staticRelays []peer.AddrInfo
	if len(bootstrapPIs) > 0 {
		staticRelays = bootstrapPIs
	}

	host, kdht, err := NewHost(ctx, HostConfig{
		ListenAddrs:  cfg.ListenAddrs,
		IdentityPath: cfg.IdentityPath,
		RelayMode:    cfg.RelayMode,
		EnableMDNS:   cfg.EnableMDNS,
		DHTOpts:      dhtOpts,
		StaticRelays: staticRelays,
	})
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	ps, err := NewPubSub(ctx, host, cfg.ChannelID)
	if err != nil {
		_ = host.Close()
		return nil, fmt.Errorf("create pubsub: %w", err)
	}

	table := NewPeerTable()

	disc := NewDiscovery(host, DiscoveryConfig{
		ChannelID:  cfg.ChannelID,
		EnableMDNS: cfg.EnableMDNS,
	})

	presence := NewPresenceService(host, ps, table, PresenceConfig{
		Hostname: cfg.Hostname,
		Priority: cfg.Priority,
	})

	notify := NewNotifyService(host, ps, NotifyServiceConfig{
		Handler: cfg.NotifyHandler,
	})

	return &Node{
		PeerTable: table,
		Presence:  presence,
		Notify:    notify,
		cfg:       cfg,
		Host:      host,
		dht:       kdht,
		discovery: disc,
		pubsub:    ps,
	}, nil
}

// Run blocks running all sub-services (bootstrap connect, optional relay
// service, discovery, presence, notify) until ctx is cancelled. Before
// returning it closes the libp2p host. Returns the first non-nil
// sub-service error, or nil on clean shutdown.
func (n *Node) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)

	// Setup relay service if in relay mode. Synchronous: must complete
	// before peers start connecting through us.
	if n.cfg.RelayMode {
		if _, err := setupRelay(gctx, n.Host); err != nil {
			_ = n.Host.Close()
			return fmt.Errorf("setup relay: %w", err)
		}
		slog.Info("relay service enabled", "peerID", n.Host.ID(), "addrs", n.Host.Addrs())
	}

	// Dial bootstrap peers concurrently and track via the group so Run
	// waits for them on shutdown. Errors are logged but not propagated:
	// failing to reach one bootstrap peer must not bring down the node.
	for _, addr := range n.cfg.BootstrapPeers {
		addr := addr
		g.Go(func() error {
			if err := connectBootstrapPeer(gctx, n.Host, addr); err != nil {
				slog.Warn("bootstrap peer", "addr", addr, "error", err)
			}
			return nil
		})
	}

	g.Go(func() error {
		if err := n.discovery.Run(gctx, n.dht); err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := n.Presence.Run(gctx); err != nil {
			return fmt.Errorf("presence: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := n.Notify.Run(gctx); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
		return nil
	})

	err := g.Wait()
	if closeErr := n.Host.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

// buildDHTOpts creates DHT options for host-level routing.
func buildDHTOpts(relayMode bool, bootstrapPIs []peer.AddrInfo) []dht.Option {
	var opts []dht.Option
	if relayMode {
		opts = append(opts, dht.Mode(dht.ModeServer))
	} else {
		opts = append(opts, dht.Mode(dht.ModeAutoServer))
	}
	if len(bootstrapPIs) > 0 {
		opts = append(opts, dht.BootstrapPeers(bootstrapPIs...))
	} else {
		opts = append(opts, dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...))
	}
	return opts
}
