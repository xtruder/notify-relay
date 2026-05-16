package p2p

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// Discovery manages peer discovery via mDNS and DHT rendezvous.
//
// Lifecycle:
//   - NewDiscovery constructs the service.
//   - Run(ctx) bootstraps the DHT, starts mDNS if enabled, runs the
//     advertise/find loop, and blocks until ctx is cancelled. Before
//     returning it closes owned resources (mDNS, DHT if Discovery created
//     it).
type Discovery struct {
	host       host.Host
	cfg        DiscoveryConfig
	rendezvous string
}

// DiscoveryConfig configures peer discovery.
type DiscoveryConfig struct {
	// ChannelID is the channel identifier; its hash is used as the rendezvous string.
	ChannelID string

	// EnableMDNS enables mDNS discovery for LAN peers.
	EnableMDNS bool
}

// NewDiscovery constructs a Discovery. No goroutines or network activity
// happen until Run is called.
func NewDiscovery(h host.Host, cfg DiscoveryConfig) *Discovery {
	return &Discovery{
		host:       h,
		cfg:        cfg,
		rendezvous: "notify-relay/" + ChannelHash(cfg.ChannelID),
	}
}

// Start bootstraps the DHT, starts mDNS if enabled, and runs the
// advertise/discover loop. Blocks until ctx is cancelled. Before returning
// it closes the mDNS service and the DHT (when Discovery created it).
// kdht is the Kademlia DHT used for rendezvous-based peer discovery.
// Discovery does not own its lifecycle (the caller does); it must remain
// alive for the duration of Run.
//
// Must only be called once.
func (d *Discovery) Run(ctx context.Context, kdht *dht.IpfsDHT) error {
	if kdht == nil {
		return fmt.Errorf("discovery: DHT is required")
	}

	if err := kdht.Bootstrap(ctx); err != nil {
		return err
	}

	// Start mDNS if enabled. DNS labels are limited to 63 chars, so use a
	// short prefix of the channel hash for the service tag.
	var mdnsSvc mdns.Service
	if d.cfg.EnableMDNS {
		hash := ChannelHash(d.cfg.ChannelID)
		mdnsTag := "nr-" + hash[:16]
		notifee := &mdnsNotifee{host: d.host}
		svc := mdns.NewMdnsService(d.host, mdnsTag, notifee)
		if err := svc.Start(); err != nil {
			slog.Warn("mDNS start failed", "error", err)
		} else {
			mdnsSvc = svc
			slog.Info("mDNS discovery started", "tag", mdnsTag, "rendezvous", d.rendezvous)
		}
	}

	d.advertiseAndDiscover(ctx, kdht)

	if mdnsSvc != nil {
		if err := mdnsSvc.Close(); err != nil {
			slog.Warn("mDNS close failed", "error", err)
		}
	}
	return nil
}

func (d *Discovery) advertiseAndDiscover(ctx context.Context, kdht *dht.IpfsDHT) {
	routingDiscovery := drouting.NewRoutingDiscovery(kdht)

	dutil.Advertise(ctx, routingDiscovery, d.rendezvous)
	slog.Info("DHT advertising started", "rendezvous", d.rendezvous)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		d.findPeers(ctx, routingDiscovery)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Discovery) findPeers(ctx context.Context, rd *drouting.RoutingDiscovery) {
	findCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	peerChan, err := rd.FindPeers(findCtx, d.rendezvous)
	if err != nil {
		slog.Debug("DHT find peers failed", "error", err)
		return
	}

	for p := range peerChan {
		if p.ID == d.host.ID() || len(p.Addrs) == 0 {
			continue
		}

		if d.host.Network().Connectedness(p.ID) != 0 {
			continue
		}

		go func(pi peer.AddrInfo) {
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := d.host.Connect(connectCtx, pi); err != nil {
				slog.Debug("DHT peer connect failed", "peer", pi.ID, "error", err)
			} else {
				slog.Info("discovered and connected to peer", "peer", pi.ID)
			}
		}(p)
	}
}

// mdnsNotifee handles mDNS peer discovery events.
type mdnsNotifee struct {
	host host.Host
}

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.host.ID() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.host.Connect(ctx, pi); err != nil {
		slog.Debug("mDNS peer connect failed", "peer", pi.ID, "error", err)
	} else {
		slog.Info("mDNS discovered peer", "peer", pi.ID)
	}
}
