package p2p

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/multiformats/go-multiaddr"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// HostConfig configures the libp2p host.
type HostConfig struct {
	// ListenAddrs are the multiaddrs to listen on.
	// Defaults to ["/ip4/0.0.0.0/tcp/0"] if empty.
	ListenAddrs []string

	// IdentityPath is the path to persist the ed25519 private key.
	// Defaults to ~/.config/notify-relay/identity.key
	IdentityPath string

	// BootstrapPeers are static peer multiaddrs to connect to on startup.
	BootstrapPeers []string

	// StaticRelays are peer.AddrInfos of known relay nodes (e.g., user's VPS).
	// Used by AutoRelay as preferred static relays for NAT traversal.
	StaticRelays []peer.AddrInfo

	// RelayMode enables circuit relay v2 service (for relay nodes).
	RelayMode bool

	// EnableMDNS enables local network mDNS discovery.
	EnableMDNS bool

	// DHTOpts are options for the Kademlia DHT created via libp2p.Routing.
	// If nil, no DHT is created at host level (discovery will create its own).
	DHTOpts []dht.Option
}

// HostResult holds the host and any resources created during construction.
type HostResult struct {
	Host host.Host
	DHT  *dht.IpfsDHT
}

// NewHost creates a libp2p host with the given config.
func NewHost(ctx context.Context, cfg HostConfig) (host.Host, *dht.IpfsDHT, error) {
	privKey, err := loadOrGenerateKey(cfg.IdentityPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load identity key: %w", err)
	}

	listenAddrs := cfg.ListenAddrs
	if len(listenAddrs) == 0 {
		listenAddrs = []string{"/ip4/0.0.0.0/tcp/0"}
	}

	maddrs := make([]multiaddr.Multiaddr, 0, len(listenAddrs))
	for _, a := range listenAddrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			return nil, nil, fmt.Errorf("parse listen addr %q: %w", a, err)
		}
		maddrs = append(maddrs, ma)
	}

	opts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrs(maddrs...),
		libp2p.EnableRelay(),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	}

	// Create DHT via libp2p.Routing so AutoRelay can use it for peer source.
	var ipfsDHT *dht.IpfsDHT
	if len(cfg.DHTOpts) > 0 {
		opts = append(opts, libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			kadDHT, err := dht.New(ctx, h, cfg.DHTOpts...)
			if err != nil {
				return nil, err
			}
			ipfsDHT = kadDHT
			return kadDHT, err
		}))
	}

	if cfg.RelayMode {
		opts = append(opts, libp2p.EnableRelayService())
		opts = append(opts, libp2p.ForceReachabilityPublic())
	} else if len(cfg.StaticRelays) > 0 {
		// User configured known relay peers (e.g., their VPS) — use as static relays.
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(cfg.StaticRelays,
			autorelay.WithNumRelays(2),
			autorelay.WithMaxCandidates(10),
		))
	} else if len(cfg.DHTOpts) > 0 {
		// No static relays, but DHT is available — discover relay candidates via DHT.
		// Use GetClosestPeers with a random key to find candidate peers.
		// AutoRelay probes each candidate to check if it supports circuit relay v2.
		peerSource := func(ctx context.Context, num int) <-chan peer.AddrInfo {
			ch := make(chan peer.AddrInfo, num)
			go func() {
				defer close(ch)
				if ipfsDHT == nil {
					return
				}
				// Generate a random key to query the DHT for nearby peers.
				randBuf := make([]byte, 32)
				if _, err := rand.Read(randBuf); err != nil {
					return
				}
				peers, err := ipfsDHT.GetClosestPeers(ctx, string(randBuf))
				if err != nil {
					return
				}
				sent := 0
				for _, id := range peers {
					if sent >= num {
						return
					}
					addrs := ipfsDHT.Host().Peerstore().Addrs(id)
					if len(addrs) > 0 {
						select {
						case ch <- peer.AddrInfo{ID: id, Addrs: addrs}:
							sent++
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return ch
		}
		opts = append(opts, libp2p.EnableAutoRelayWithPeerSource(peerSource,
			autorelay.WithNumRelays(2),
			autorelay.WithMaxCandidates(10),
		))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create libp2p host: %w", err)
	}

	slog.Info("libp2p host started", "peerID", h.ID(), "addrs", h.Addrs())
	return h, ipfsDHT, nil
}

// connectBootstrapPeer dials a single bootstrap peer and blocks until the
// connection succeeds, fails, or ctx is cancelled. Returns nil if the
// address refers to the local host (self-connect skipped).
func connectBootstrapPeer(ctx context.Context, h host.Host, addr string) error {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("parse addr %q: %w", addr, err)
	}
	pi, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return fmt.Errorf("parse peer info %q: %w", addr, err)
	}
	if pi.ID == h.ID() {
		return nil
	}
	if err := h.Connect(ctx, *pi); err != nil {
		return fmt.Errorf("connect %s: %w", pi.ID, err)
	}
	slog.Info("connected to bootstrap peer", "peer", pi.ID)
	return nil
}

func loadOrGenerateKey(path string) (crypto.PrivKey, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".config", "notify-relay", "identity.key")
	}

	data, err := os.ReadFile(path)
	if err == nil {
		key, err := crypto.UnmarshalPrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("unmarshal identity key: %w", err)
		}
		return key, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read identity key: %w", err)
	}

	// Generate new key
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}

	raw, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal identity key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create identity dir: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, fmt.Errorf("write identity key: %w", err)
	}

	slog.Info("generated new identity key", "path", path)
	return key, nil
}
