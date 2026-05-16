package tests

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/p2p"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/router"
)

// --------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------

// testNode holds all components of a test p2p node with a router.
type testNode struct {
	Host      host.Host
	PeerTable *p2p.PeerTable
	Presence  *p2p.PresenceService
	Notify    *p2p.NotifyService
	Router    *router.Router

	// mock channels for assertions
	dbusCh *recordingChannel
	ntfyCh *recordingChannel

	hostname string
	priority int
	locked   *atomic.Bool // pointer to shared lock state
}

func (n *testNode) PublishPresence(ctx context.Context) {
	n.Presence.SetPresent(ctx, !n.locked.Load())
}

// Close closes the libp2p host. Service goroutines (Notify, Presence,
// Discovery) are expected to have exited via ctx cancellation already.
func (n *testNode) Close() {
	n.Host.Close()
}

// recordingChannel is a mock channel that records every notification it receives.
type recordingChannel struct {
	name string
	mu   sync.Mutex
	reqs []protocol.NotifyRequest
	// nextID is returned by Send
	nextID atomic.Uint32
}

func newRecordingChannel(name string) *recordingChannel {
	return &recordingChannel{name: name}
}

func (c *recordingChannel) Name() string { return c.name }

func (c *recordingChannel) Send(_ context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	id := c.nextID.Add(1)
	return protocol.NotifyResponse{ID: id, Event: "displayed"}, nil
}

func (c *recordingChannel) Close() error                                   { return nil }
func (c *recordingChannel) Capabilities(context.Context) ([]string, error) { return nil, nil }
func (c *recordingChannel) ServerInfo(context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{Name: c.name}, nil
}

func (c *recordingChannel) received() []protocol.NotifyRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]protocol.NotifyRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

func (c *recordingChannel) reset() {
	c.mu.Lock()
	c.reqs = nil
	c.mu.Unlock()
}

// createTestHost creates a minimal libp2p host listening on loopback.
func createTestHost(t *testing.T, relayEnabled bool) host.Host {
	t.Helper()
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	}
	if relayEnabled {
		opts = append(opts, libp2p.EnableRelayService())
		opts = append(opts, libp2p.ForceReachabilityPublic())
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		t.Fatalf("create test host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// connect directly connects two hosts.
func connect(t *testing.T, a, b host.Host) {
	t.Helper()
	info := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}
	if err := a.Connect(context.Background(), info); err != nil {
		t.Fatalf("connect %s -> %s: %v", a.ID().String()[:8], b.ID().String()[:8], err)
	}
}

// createNode builds a full test node (host + pubsub + presence + notify + router).
// The relay host is used to simulate WAN: desktop and laptop connect ONLY through the relay,
// while the VPS is the relay itself (it connects directly to both).
func createNode(
	t *testing.T,
	ctx context.Context,
	h host.Host,
	channelID string,
	hostname string,
	priority int,
	locked *atomic.Bool,
	routes []router.Route,
) *testNode {
	t.Helper()

	ps, err := p2p.NewPubSub(ctx, h, channelID)
	if err != nil {
		t.Fatalf("pubsub %s: %v", hostname, err)
	}

	table := p2p.NewPeerTable()

	pres := p2p.NewPresenceService(h, ps, table, p2p.PresenceConfig{
		Hostname:          hostname,
		Priority:          priority,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	go func() { _ = pres.Run(ctx) }()

	pres.SetPresent(ctx, !locked.Load())

	dbusCh := newRecordingChannel("dbus")
	ntfyCh := newRecordingChannel("ntfy")

	r, err := router.NewWithP2P(
		router.Config{Routes: routes},
		nil, // no screen-lock evaluator in tests
		[]channel.Channel{dbusCh, ntfyCh},
		table,
	)
	if err != nil {
		t.Fatalf("router %s: %v", hostname, err)
	}

	notify := p2p.NewNotifyService(h, ps, p2p.NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			return r.Notify(ctx, req)
		},
	})
	go func() { _ = notify.Run(ctx) }()

	n := &testNode{
		Host:      h,
		PeerTable: table,
		Presence:  pres,
		Notify:    notify,
		Router:    r,
		dbusCh:    dbusCh,
		ntfyCh:    ntfyCh,
		hostname:  hostname,
		priority:  priority,
		locked:    locked,
	}

	t.Cleanup(func() { n.Close() })
	return n
}

// waitForPresence waits until every node's peer table sees all other nodes.
// The presence service's internal heartbeat handles re-broadcasting.
func waitForPresence(t *testing.T, timeout time.Duration, nodes ...*testNode) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		allSeen := true
		for _, n := range nodes {
			for _, other := range nodes {
				if other.Host.ID() == n.Host.ID() {
					continue
				}
				if _, ok := n.PeerTable.Get(other.Host.ID()); !ok {
					allSeen = false
					break
				}
			}
			if !allSeen {
				break
			}
		}
		if allSeen {
			return
		}
		select {
		case <-deadline:
			// Print diagnostic
			for _, n := range nodes {
				peers := n.PeerTable.GetAll()
				t.Logf("  %s sees %d peers", n.hostname, len(peers))
				for _, p := range peers {
					t.Logf("    - %s (priority=%d locked=%v)", p.Hostname, p.Priority, p.IsPresent)
				}
			}
			t.Fatalf("timeout waiting for full presence mesh")
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// sendNotification publishes a notification from the given node and returns
// whether an action reply was received within the timeout.
func sendNotification(ctx context.Context, from *testNode, summary, body string, timeout time.Duration) (protocol.NotifyResponse, bool) {
	req := protocol.NotifyRequest{
		AppName: "test",
		Summary: summary,
		Body:    body,
	}

	// Also route locally (same as localRouter in main.go)
	go from.Router.Notify(ctx, req)

	return from.Notify.Publish(ctx, req, timeout)
}

// --------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------

// TestThreeNodeNetwork sets up:
//
//	Desktop (priority 1) — has dbus, uses best_active
//	Laptop  (priority 2) — has dbus, uses best_active
//	VPS     (priority 50, relay) — has ntfy, uses no_better_peer
//
// Topology: Desktop <-> Relay/VPS <-> Laptop (star through relay)
func TestThreeNodeNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	channelID := "test-three-node-network"

	// Lock state controls
	var desktopLocked, laptopLocked, vpsLocked atomic.Bool
	vpsLocked.Store(true) // VPS is always "locked" (headless)

	// Create hosts — VPS is the relay, desktop and laptop are regular nodes
	vpsHost := createTestHost(t, true /*relay*/)
	desktopHost := createTestHost(t, false)
	laptopHost := createTestHost(t, false)

	// Star topology: desktop <-> VPS, laptop <-> VPS (no direct desktop <-> laptop)
	connect(t, desktopHost, vpsHost)
	connect(t, laptopHost, vpsHost)

	// Create nodes with their routing configs
	desktop := createNode(t, ctx, desktopHost, channelID, "desktop", 1, &desktopLocked,
		[]router.Route{
			{Condition: "best_active", Channel: "dbus"},
		},
	)

	laptop := createNode(t, ctx, laptopHost, channelID, "laptop", 2, &laptopLocked,
		[]router.Route{
			{Condition: "best_active", Channel: "dbus"},
		},
	)

	vps := createNode(t, ctx, vpsHost, channelID, "vps", 50, &vpsLocked,
		[]router.Route{
			{Condition: "no_better_peer", Channel: "ntfy"},
		},
	)

	// Wait for all three nodes to see each other
	t.Log("waiting for presence mesh...")
	waitForPresence(t, 15*time.Second, desktop, laptop, vps)
	t.Log("presence mesh established")

	// Give GossipSub extra time to establish the notification topic mesh
	time.Sleep(1 * time.Second)

	// ------------------------------------------------------------------
	// Scenario 1: Desktop and Laptop both unlocked
	// Desktop (priority 1) should display via dbus.
	// Laptop and VPS should NOT act.
	// ------------------------------------------------------------------
	t.Run("both_unlocked_desktop_wins", func(t *testing.T) {
		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		resp, ok := sendNotification(ctx, laptop, "Scenario 1", "Both unlocked", 5*time.Second)
		t.Logf("publish result: ok=%v resp=%+v", ok, resp)

		// Wait a bit for routing to complete on all nodes
		time.Sleep(500 * time.Millisecond)

		desktopGot := desktop.dbusCh.received()
		laptopGot := laptop.dbusCh.received()
		vpsGot := vps.ntfyCh.received()

		if len(desktopGot) != 1 {
			t.Errorf("desktop should receive 1 notification, got %d", len(desktopGot))
		} else if desktopGot[0].Summary != "Scenario 1" {
			t.Errorf("desktop got wrong summary: %q", desktopGot[0].Summary)
		}
		if len(laptopGot) != 0 {
			t.Errorf("laptop should NOT display (priority 2 > 1), got %d", len(laptopGot))
		}
		if len(vpsGot) != 0 {
			t.Errorf("VPS should NOT display (unlocked node exists), got %d", len(vpsGot))
		}
	})

	// ------------------------------------------------------------------
	// Scenario 2: Desktop locked, Laptop unlocked
	// Laptop (only unlocked node) should display via dbus.
	// ------------------------------------------------------------------
	t.Run("desktop_locked_laptop_wins", func(t *testing.T) {
		desktopLocked.Store(true)
		// Publish presence to propagate new lock state
		desktop.PublishPresence(ctx)
		time.Sleep(1 * time.Second) // allow presence to propagate

		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		sendNotification(ctx, desktop, "Scenario 2", "Desktop locked", 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		desktopGot := desktop.dbusCh.received()
		laptopGot := laptop.dbusCh.received()
		vpsGot := vps.ntfyCh.received()

		if len(desktopGot) != 0 {
			t.Errorf("desktop should NOT display (locked), got %d", len(desktopGot))
		}
		if len(laptopGot) != 1 {
			t.Errorf("laptop should receive 1 notification, got %d", len(laptopGot))
		}
		if len(vpsGot) != 0 {
			t.Errorf("VPS should NOT display (unlocked laptop exists), got %d", len(vpsGot))
		}

		desktopLocked.Store(false)
		desktop.PublishPresence(ctx)
	})

	// ------------------------------------------------------------------
	// Scenario 3: Both Desktop and Laptop locked
	// VPS (priority 50, no_better_peer) should forward to ntfy.
	// Neither Desktop nor Laptop should display.
	// ------------------------------------------------------------------
	t.Run("all_locked_vps_forwards", func(t *testing.T) {
		desktopLocked.Store(true)
		laptopLocked.Store(true)
		desktop.PublishPresence(ctx)
		laptop.PublishPresence(ctx)
		time.Sleep(1 * time.Second)

		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		sendNotification(ctx, desktop, "Scenario 3", "All locked", 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		desktopGot := desktop.dbusCh.received()
		laptopGot := laptop.dbusCh.received()
		vpsGot := vps.ntfyCh.received()

		if len(desktopGot) != 0 {
			t.Errorf("desktop should NOT display (locked), got %d", len(desktopGot))
		}
		if len(laptopGot) != 0 {
			t.Errorf("laptop should NOT display (locked), got %d", len(laptopGot))
		}
		if len(vpsGot) != 1 {
			t.Errorf("VPS should forward 1 notification to ntfy, got %d", len(vpsGot))
		} else if vpsGot[0].Summary != "Scenario 3" {
			t.Errorf("VPS got wrong summary: %q", vpsGot[0].Summary)
		}

		desktopLocked.Store(false)
		laptopLocked.Store(false)
		desktop.PublishPresence(ctx)
		laptop.PublishPresence(ctx)
	})

	// ------------------------------------------------------------------
	// Scenario 4: Desktop unlocks mid-scenario
	// Start with both locked (VPS forwards), then unlock desktop.
	// Next notification should go to desktop, not VPS.
	// ------------------------------------------------------------------
	t.Run("dynamic_unlock_switches_routing", func(t *testing.T) {
		desktopLocked.Store(true)
		laptopLocked.Store(true)
		desktop.PublishPresence(ctx)
		laptop.PublishPresence(ctx)
		time.Sleep(1 * time.Second)

		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		// First notification — VPS should handle
		sendNotification(ctx, laptop, "Scenario 4a", "Still locked", 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		if len(vps.ntfyCh.received()) != 1 {
			t.Errorf("VPS should handle first notification, got %d", len(vps.ntfyCh.received()))
		}

		// Now unlock desktop
		desktopLocked.Store(false)
		desktop.PublishPresence(ctx)
		time.Sleep(1 * time.Second)

		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		// Second notification — desktop should handle
		sendNotification(ctx, laptop, "Scenario 4b", "Desktop unlocked now", 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		desktopGot := desktop.dbusCh.received()
		vpsGot := vps.ntfyCh.received()

		if len(desktopGot) != 1 {
			t.Errorf("desktop should handle second notification, got %d", len(desktopGot))
		}
		if len(vpsGot) != 0 {
			t.Errorf("VPS should NOT handle (desktop is unlocked), got %d", len(vpsGot))
		}

		laptopLocked.Store(false)
		laptop.PublishPresence(ctx)
	})

	// ------------------------------------------------------------------
	// Scenario 5: Notification sent FROM the VPS (e.g., cron job)
	// Desktop (unlocked, priority 1) should display it.
	// ------------------------------------------------------------------
	t.Run("vps_sends_desktop_displays", func(t *testing.T) {
		desktop.dbusCh.reset()
		laptop.dbusCh.reset()
		vps.ntfyCh.reset()

		sendNotification(ctx, vps, "Scenario 5", "From VPS cron", 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		desktopGot := desktop.dbusCh.received()
		vpsGot := vps.ntfyCh.received()

		if len(desktopGot) != 1 {
			t.Errorf("desktop should display VPS notification, got %d", len(desktopGot))
		}
		if len(vpsGot) != 0 {
			t.Errorf("VPS should NOT self-handle when desktop is unlocked, got %d", len(vpsGot))
		}
	})

	// ------------------------------------------------------------------
	// Scenario 6: Action reply propagation
	// Laptop sends notification, desktop handles it and returns an action reply.
	// Laptop should receive the action reply.
	// ------------------------------------------------------------------
	t.Run("action_reply_propagation", func(t *testing.T) {
		desktop.dbusCh.reset()

		resp, ok := sendNotification(ctx, laptop, "Scenario 6", "Needs reply", 5*time.Second)

		if !ok {
			t.Fatal("expected action reply from desktop")
		}
		if resp.Event != "displayed" {
			t.Errorf("expected event 'displayed', got %q", resp.Event)
		}
	})
}

// TestRelayParticipantNode tests that a relay+participant node (VPS with channel ID)
// can relay GossipSub messages between two nodes that only connect through it,
// while also participating in the channel as a last-resort forwarder.
func TestRelayParticipantNode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	channelID := "test-relay-participant"

	// Relay + participant: acts as relay AND joins the channel
	relayHost := createTestHost(t, true)

	// Two participant nodes — connected ONLY through the relay (star topology)
	var node1Locked, node2Locked, relayLocked atomic.Bool
	relayLocked.Store(true) // relay/VPS is always "locked"
	node1Host := createTestHost(t, false)
	node2Host := createTestHost(t, false)

	// Star topology: node1 <-> relay, node2 <-> relay (no direct node1 <-> node2)
	connect(t, node1Host, relayHost)
	connect(t, node2Host, relayHost)

	node1 := createNode(t, ctx, node1Host, channelID, "node1", 1, &node1Locked,
		[]router.Route{
			{Condition: "best_active", Channel: "dbus"},
		},
	)

	node2 := createNode(t, ctx, node2Host, channelID, "node2", 2, &node2Locked,
		[]router.Route{
			{Condition: "best_active", Channel: "dbus"},
		},
	)

	// Relay also joins the channel — so it participates in GossipSub mesh
	// and can forward messages between node1 and node2
	relay := createNode(t, ctx, relayHost, channelID, "relay-vps", 50, &relayLocked,
		[]router.Route{
			{Condition: "no_better_peer", Channel: "ntfy"},
		},
	)

	waitForPresence(t, 15*time.Second, node1, node2, relay)
	time.Sleep(1 * time.Second)

	node1.dbusCh.reset()
	node2.dbusCh.reset()
	relay.ntfyCh.reset()

	// node2 sends, node1 (priority 1, unlocked) should display
	// Messages flow: node2 -> relay -> node1 (GossipSub through relay)
	sendNotification(ctx, node2, "RelayTest", "Through relay participant", 5*time.Second)
	time.Sleep(500 * time.Millisecond)

	if got := node1.dbusCh.received(); len(got) != 1 {
		t.Errorf("node1 should receive notification through relay, got %d", len(got))
	}
	if got := node2.dbusCh.received(); len(got) != 0 {
		t.Errorf("node2 should NOT display own notification, got %d", len(got))
	}
	if got := relay.ntfyCh.received(); len(got) != 0 {
		t.Errorf("relay should NOT forward to ntfy (unlocked nodes exist), got %d", len(got))
	}

	// Now lock both nodes — relay should forward to ntfy
	node1Locked.Store(true)
	node2Locked.Store(true)
	node1.PublishPresence(ctx)
	node2.PublishPresence(ctx)
	time.Sleep(1 * time.Second)

	node1.dbusCh.reset()
	node2.dbusCh.reset()
	relay.ntfyCh.reset()

	sendNotification(ctx, node2, "RelayFallback", "All locked", 5*time.Second)
	time.Sleep(500 * time.Millisecond)

	if got := relay.ntfyCh.received(); len(got) != 1 {
		t.Errorf("relay should forward to ntfy when all locked, got %d", len(got))
	}
	if got := node1.dbusCh.received(); len(got) != 0 {
		t.Errorf("node1 should NOT display (locked), got %d", len(got))
	}
}

// TestWrongChannelIsolation verifies that a node on a different channel
// cannot see notifications from the test network.
func TestWrongChannelIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	h1 := createTestHost(t, false)
	h2 := createTestHost(t, false) // eavesdropper
	connect(t, h1, h2)

	var locked1, locked2 atomic.Bool

	node1 := createNode(t, ctx, h1, "secret-channel-abc", "node1", 1, &locked1,
		[]router.Route{{Condition: "always", Channel: "dbus"}},
	)

	node2 := createNode(t, ctx, h2, "wrong-channel-xyz", "eavesdropper", 1, &locked2,
		[]router.Route{{Condition: "always", Channel: "dbus"}},
	)

	time.Sleep(3 * time.Second) // let GossipSub settle

	node1.dbusCh.reset()
	node2.dbusCh.reset()

	// Publish from node1 — node2 is on a different channel, should not receive
	node1.Notify.Publish(ctx, protocol.NotifyRequest{
		Summary: "Secret",
		Body:    "Should not be visible",
	}, 2*time.Second)

	time.Sleep(1 * time.Second)

	if got := node2.dbusCh.received(); len(got) != 0 {
		t.Errorf("eavesdropper on wrong channel should receive 0 notifications, got %d", len(got))
	}

	_ = node1
	_ = node2
}

// TestThreeNodePriorityTiebreak verifies that when two nodes have the same
// priority, the one with the lower peer ID wins (deterministic tiebreak).
func TestThreeNodePriorityTiebreak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	channelID := "test-tiebreak"

	var locked1, locked2, locked3 atomic.Bool
	locked3.Store(true) // node3 is the sender and locked

	h1 := createTestHost(t, false)
	h2 := createTestHost(t, false)
	h3 := createTestHost(t, false)

	connect(t, h1, h2)
	connect(t, h2, h3)
	connect(t, h1, h3)

	// Same priority!
	node1 := createNode(t, ctx, h1, channelID, "node1", 1, &locked1,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
	)
	node2 := createNode(t, ctx, h2, channelID, "node2", 1, &locked2,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
	)
	node3 := createNode(t, ctx, h3, channelID, "sender", 5, &locked3,
		[]router.Route{{Condition: "always", Channel: "dbus"}},
	)

	waitForPresence(t, 15*time.Second, node1, node2, node3)
	time.Sleep(1 * time.Second)

	node1.dbusCh.reset()
	node2.dbusCh.reset()

	sendNotification(ctx, node3, "Tiebreak", "Same priority", 5*time.Second)
	time.Sleep(500 * time.Millisecond)

	n1Got := node1.dbusCh.received()
	n2Got := node2.dbusCh.received()

	// Exactly one should display
	total := len(n1Got) + len(n2Got)
	if total != 1 {
		t.Errorf("expected exactly 1 node to display, got %d (node1=%d, node2=%d)", total, len(n1Got), len(n2Got))
	}

	// Verify the winner is deterministic based on peer ID
	if len(n1Got) == 1 {
		if h1.ID() > h2.ID() {
			t.Errorf("node1 won but has higher peer ID — tiebreak should favor lower ID")
		}
		t.Logf("node1 (ID=%s) won tiebreak vs node2 (ID=%s)", h1.ID().String()[:8], h2.ID().String()[:8])
	} else {
		if h2.ID() > h1.ID() {
			t.Errorf("node2 won but has higher peer ID — tiebreak should favor lower ID")
		}
		t.Logf("node2 (ID=%s) won tiebreak vs node1 (ID=%s)", h2.ID().String()[:8], h1.ID().String()[:8])
	}

	_ = node3
}

// --------------------------------------------------------------------
// Discovery tests
// --------------------------------------------------------------------

// testNodeWithDiscovery is like testNode but also runs Discovery.
type testNodeWithDiscovery struct {
	*testNode
	Discovery *p2p.Discovery
}

func (n *testNodeWithDiscovery) Close() {
	n.testNode.Close()
}

// createNodeWithDiscovery builds a full node WITH discovery (mDNS and/or DHT).
// No manual connect() is needed — discovery finds peers automatically.
func createNodeWithDiscovery(
	t *testing.T,
	ctx context.Context,
	h host.Host,
	channelID string,
	hostname string,
	priority int,
	locked *atomic.Bool,
	routes []router.Route,
	enableMDNS bool,
	bootstrapPeers []peer.AddrInfo,
) *testNodeWithDiscovery {
	t.Helper()

	ps, err := p2p.NewPubSub(ctx, h, channelID)
	if err != nil {
		t.Fatalf("pubsub %s: %v", hostname, err)
	}

	table := p2p.NewPeerTable()

	// Construct a DHT for discovery (in production, the host provides one
	// via libp2p.Routing).
	dhtOpts := []dht.Option{dht.Mode(dht.ModeAutoServer)}
	if len(bootstrapPeers) > 0 {
		dhtOpts = append(dhtOpts, dht.BootstrapPeers(bootstrapPeers...))
	} else {
		dhtOpts = append(dhtOpts, dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...))
	}
	kdht, err := dht.New(ctx, h, dhtOpts...)
	if err != nil {
		t.Fatalf("create test DHT: %v", err)
	}

	// Start discovery BEFORE presence so that peers connect before heartbeats
	disc := p2p.NewDiscovery(h, p2p.DiscoveryConfig{
		ChannelID:  channelID,
		EnableMDNS: enableMDNS,
	})
	go func() { _ = disc.Run(ctx, kdht) }()

	pres := p2p.NewPresenceService(h, ps, table, p2p.PresenceConfig{
		Hostname:          hostname,
		Priority:          priority,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	go func() { _ = pres.Run(ctx) }()

	pres.SetPresent(ctx, !locked.Load())

	dbusCh := newRecordingChannel("dbus")
	ntfyCh := newRecordingChannel("ntfy")

	r, err := router.NewWithP2P(
		router.Config{Routes: routes},
		nil,
		[]channel.Channel{dbusCh, ntfyCh},
		table,
	)
	if err != nil {
		t.Fatalf("router %s: %v", hostname, err)
	}

	notify := p2p.NewNotifyService(h, ps, p2p.NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			return r.Notify(ctx, req)
		},
	})
	go func() { _ = notify.Run(ctx) }()

	n := &testNodeWithDiscovery{
		testNode: &testNode{
			Host:      h,
			PeerTable: table,
			Presence:  pres,
			Notify:    notify,
			Router:    r,
			dbusCh:    dbusCh,
			ntfyCh:    ntfyCh,
			hostname:  hostname,
			priority:  priority,
			locked:    locked,
		},
		Discovery: disc,
	}

	t.Cleanup(func() { n.Close() })
	return n
}

// waitForPresenceDiscovery is like waitForPresence but for testNodeWithDiscovery.
// The presence service's internal heartbeat handles re-broadcasting.
func waitForPresenceDiscovery(t *testing.T, timeout time.Duration, nodes ...*testNodeWithDiscovery) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		allSeen := true
		for _, n := range nodes {
			for _, other := range nodes {
				if other.Host.ID() == n.Host.ID() {
					continue
				}
				if _, ok := n.PeerTable.Get(other.Host.ID()); !ok {
					allSeen = false
					break
				}
			}
			if !allSeen {
				break
			}
		}
		if allSeen {
			return
		}
		select {
		case <-deadline:
			for _, n := range nodes {
				peers := n.PeerTable.GetAll()
				conns := n.Host.Network().Peers()
				t.Logf("  %s: %d presence peers, %d libp2p connections", n.hostname, len(peers), len(conns))
				for _, p := range peers {
					t.Logf("    presence: %s (priority=%d locked=%v)", p.Hostname, p.Priority, p.IsPresent)
				}
				for _, pid := range conns {
					t.Logf("    connected: %s", pid.String()[:16])
				}
			}
			t.Fatalf("timeout waiting for discovery-based presence mesh")
		default:
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// TestMDNSDiscovery tests that two nodes on the same LAN discover each other
// via mDNS without any manual connect() call, and can exchange notifications.
//
// This test requires working multicast on the loopback or LAN interface.
// It may not work in containers, CI, or VMs without multicast support.
// Set TEST_MDNS=1 to run it explicitly.
func TestMDNSDiscovery(t *testing.T) {
	if os.Getenv("TEST_MDNS") != "1" {
		t.Skip("skipping mDNS test (requires multicast network); set TEST_MDNS=1 to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	channelID := "test-mdns-discovery"

	var desktopLocked, laptopLocked atomic.Bool

	// Create hosts on 0.0.0.0 (not 127.0.0.1) so mDNS can use the LAN interface
	desktopHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		t.Fatalf("create desktop host: %v", err)
	}
	t.Cleanup(func() { desktopHost.Close() })

	laptopHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		t.Fatalf("create laptop host: %v", err)
	}
	t.Cleanup(func() { laptopHost.Close() })

	desktop := createNodeWithDiscovery(t, ctx, desktopHost, channelID,
		"desktop", 1, &desktopLocked,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
		true, nil, // mDNS enabled, no bootstrap
	)

	laptop := createNodeWithDiscovery(t, ctx, laptopHost, channelID,
		"laptop", 2, &laptopLocked,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
		true, nil, // mDNS enabled, no bootstrap
	)

	// Wait for mDNS to discover peers — no manual connect!
	t.Log("waiting for mDNS discovery...")
	waitForPresenceDiscovery(t, 20*time.Second, desktop, laptop)
	t.Log("mDNS discovery succeeded, presence mesh established")

	// Give GossipSub time to establish notification topic mesh
	time.Sleep(1 * time.Second)

	// Send notification from laptop, desktop should display
	desktop.dbusCh.reset()
	laptop.dbusCh.reset()

	resp, ok := sendNotification(ctx, laptop.testNode, "mDNS Test", "Discovered via mDNS", 5*time.Second)
	t.Logf("publish result: ok=%v resp=%+v", ok, resp)
	time.Sleep(500 * time.Millisecond)

	desktopGot := desktop.dbusCh.received()
	laptopGot := laptop.dbusCh.received()

	if len(desktopGot) != 1 {
		t.Errorf("desktop should receive 1 notification via mDNS-discovered peer, got %d", len(desktopGot))
	} else if desktopGot[0].Summary != "mDNS Test" {
		t.Errorf("wrong summary: %q", desktopGot[0].Summary)
	}
	if len(laptopGot) != 0 {
		t.Errorf("laptop should NOT display (lower priority), got %d", len(laptopGot))
	}
}

// TestDHTDiscovery tests that three nodes discover each other via DHT rendezvous
// through a bootstrap node, without mDNS and without manual connect() between
// the participant nodes.
//
// Topology: bootstrap node is connected manually to both participants,
// but the two participants find each other via DHT — not connected directly.
func TestDHTDiscovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DHT discovery test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	channelID := "test-dht-discovery"

	var locked1, locked2 atomic.Bool

	// Bootstrap/relay node — acts as the DHT entry point
	bootstrapHost := createTestHost(t, true)

	// Participant nodes
	node1Host := createTestHost(t, false)
	node2Host := createTestHost(t, false)

	// Only connect participants to bootstrap (NOT to each other!)
	connect(t, node1Host, bootstrapHost)
	connect(t, node2Host, bootstrapHost)

	// Bootstrap node's AddrInfo for DHT bootstrapping
	bootstrapPI := peer.AddrInfo{ID: bootstrapHost.ID(), Addrs: bootstrapHost.Addrs()}

	// Create bootstrap node with discovery (DHT server mode, no mDNS)
	var bootstrapLocked atomic.Bool
	bootstrapLocked.Store(true)
	bootstrap := createNodeWithDiscovery(t, ctx, bootstrapHost, channelID,
		"bootstrap", 99, &bootstrapLocked,
		[]router.Route{{Condition: "no_better_peer", Channel: "ntfy"}},
		false, nil, // no mDNS, no bootstrap peers (it IS the bootstrap)
	)

	// Create participant nodes with DHT discovery pointing to bootstrap
	node1 := createNodeWithDiscovery(t, ctx, node1Host, channelID,
		"node1", 1, &locked1,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
		false, []peer.AddrInfo{bootstrapPI}, // no mDNS, bootstrap via DHT
	)

	node2 := createNodeWithDiscovery(t, ctx, node2Host, channelID,
		"node2", 2, &locked2,
		[]router.Route{{Condition: "best_active", Channel: "dbus"}},
		false, []peer.AddrInfo{bootstrapPI}, // no mDNS, bootstrap via DHT
	)

	// Wait for DHT rendezvous to discover all peers
	t.Log("waiting for DHT rendezvous discovery...")
	waitForPresenceDiscovery(t, 30*time.Second, node1, node2, bootstrap)
	t.Log("DHT discovery succeeded, presence mesh established")

	time.Sleep(1 * time.Second)

	// Send notification from node2, node1 (priority 1, unlocked) should display
	node1.dbusCh.reset()
	node2.dbusCh.reset()
	bootstrap.ntfyCh.reset()

	resp, ok := sendNotification(ctx, node2.testNode, "DHT Test", "Discovered via DHT", 5*time.Second)
	t.Logf("publish result: ok=%v resp=%+v", ok, resp)
	time.Sleep(500 * time.Millisecond)

	node1Got := node1.dbusCh.received()
	node2Got := node2.dbusCh.received()
	bootstrapGot := bootstrap.ntfyCh.received()

	if len(node1Got) != 1 {
		t.Errorf("node1 should receive notification via DHT-discovered peer, got %d", len(node1Got))
	} else if node1Got[0].Summary != "DHT Test" {
		t.Errorf("wrong summary: %q", node1Got[0].Summary)
	}
	if len(node2Got) != 0 {
		t.Errorf("node2 should NOT display (lower priority), got %d", len(node2Got))
	}
	if len(bootstrapGot) != 0 {
		t.Errorf("bootstrap should NOT forward (unlocked nodes exist), got %d", len(bootstrapGot))
	}
}

// Ensure unused imports are used.
var _ = fmt.Sprintf
