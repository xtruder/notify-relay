package p2p

import (
	"context"
	"log/slog"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// setupRelay configures a host as a relay/bootstrap node.
// The relay node runs DHT in server mode and the circuit relay v2 service
// is enabled via host options in NewHost (when RelayMode=true).
// This function bootstraps the DHT for the relay.
func setupRelay(ctx context.Context, h host.Host) (*dht.IpfsDHT, error) {
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, err
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		return nil, err
	}

	slog.Info("relay mode active",
		"peerID", h.ID(),
		"addrs", h.Addrs(),
	)

	return kadDHT, nil
}

// parseBootstrapPeers parses multiaddr strings into peer.AddrInfo.
func parseBootstrapPeers(addrs []string) []peer.AddrInfo {
	var result []peer.AddrInfo
	for _, addr := range addrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			slog.Warn("invalid bootstrap addr", "addr", addr, "error", err)
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			slog.Warn("invalid bootstrap peer", "addr", addr, "error", err)
			continue
		}
		result = append(result, *pi)
	}
	return result
}
