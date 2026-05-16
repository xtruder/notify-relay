package p2p

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// testHost creates a minimal in-memory libp2p host for testing.
func testHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create test host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// connectHosts connects two hosts directly.
func connectHosts(t *testing.T, a, b host.Host) {
	t.Helper()
	bInfo := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}
	if err := a.Connect(context.Background(), bInfo); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
}

// runPresence and runNotify spawn the service's Start in a goroutine with
// a child context. Cleanup cancels the context and waits for Start to
// return so the test goroutine doesn't leak.
func runPresence(t *testing.T, ctx context.Context, svc *PresenceService) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(runCtx)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func runNotify(t *testing.T, ctx context.Context, svc *NotifyService) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(runCtx)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestPubSubEncryptedRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	channelID := "test-encrypted-pubsub"

	ps1, err := NewPubSub(ctx, h1, channelID)
	if err != nil {
		t.Fatalf("create pubsub 1: %v", err)
	}
	ps2, err := NewPubSub(ctx, h2, channelID)
	if err != nil {
		t.Fatalf("create pubsub 2: %v", err)
	}

	topicName := ps1.TopicName("test")

	topic1, err := ps1.JoinTopic(topicName)
	if err != nil {
		t.Fatalf("join topic 1: %v", err)
	}
	defer topic1.Close()

	topic2, err := ps2.JoinTopic(topicName)
	if err != nil {
		t.Fatalf("join topic 2: %v", err)
	}
	defer topic2.Close()

	sub, err := topic2.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Cancel()

	// Give GossipSub time to establish mesh
	time.Sleep(500 * time.Millisecond)

	// Publish from host1
	msg := []byte("hello from host1")
	if err := topic1.Publish(ctx, msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Receive on host2
	data, from, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	if string(data) != "hello from host1" {
		t.Fatalf("expected 'hello from host1', got %q", data)
	}
	if from != h1.ID() {
		t.Errorf("expected from %s, got %s", h1.ID(), from)
	}
}

func TestPubSubWrongKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	ps1, _ := NewPubSub(ctx, h1, "channel-A")
	ps2, _ := NewPubSub(ctx, h2, "channel-B") // different channel!

	// Use the same topic name string but different encryption keys
	topicName := "notify-relay/test-wrong-key"

	topic1, _ := ps1.JoinTopic(topicName)
	defer topic1.Close()
	topic2, _ := ps2.JoinTopic(topicName)
	defer topic2.Close()

	sub, _ := topic2.Subscribe()
	defer sub.Cancel()

	time.Sleep(500 * time.Millisecond)

	// Publish from h1 (encrypted with channel-A key)
	topic1.Publish(ctx, []byte("secret message"))

	// h2 should NOT receive it (validator rejects due to decryption failure)
	recvCtx, recvCancel := context.WithTimeout(ctx, 2*time.Second)
	defer recvCancel()

	_, _, err := sub.Next(recvCtx)
	if err == nil {
		t.Fatal("expected no message to be received with wrong key")
	}
	// context deadline exceeded is expected
}

func TestPresenceService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	channelID := "test-presence"
	ps1, _ := NewPubSub(ctx, h1, channelID)
	ps2, _ := NewPubSub(ctx, h2, channelID)

	table1 := NewPeerTable()
	table2 := NewPeerTable()

	svc1 := NewPresenceService(h1, ps1, table1, PresenceConfig{
		Hostname:          "node-1",
		Priority:          1,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	runPresence(t, ctx, svc1)

	svc2 := NewPresenceService(h2, ps2, table2, PresenceConfig{
		Hostname:          "node-2",
		Priority:          2,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	runPresence(t, ctx, svc2)

	// Set presence state — the service's internal heartbeat will
	// keep re-broadcasting until GossipSub mesh is ready.
	svc1.SetPresent(ctx, true)
	svc2.SetPresent(ctx, false)

	// Wait for presence messages to propagate
	deadline := time.After(10 * time.Second)
	for {
		info2, ok2 := table1.Get(h2.ID())
		info1, ok1 := table2.Get(h1.ID())

		if ok1 && ok2 {
			if info1.Hostname != "node-1" {
				t.Errorf("expected hostname 'node-1', got %q", info1.Hostname)
			}
			if info1.Priority != 1 {
				t.Errorf("expected priority 1, got %d", info1.Priority)
			}
			if !info1.IsPresent {
				t.Error("expected node-1 to be present")
			}

			if info2.Hostname != "node-2" {
				t.Errorf("expected hostname 'node-2', got %q", info2.Hostname)
			}
			if info2.IsPresent {
				t.Error("expected node-2 to not be present")
			}
			break
		}

		select {
		case <-deadline:
			t.Fatal("timeout waiting for presence messages")
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Verify local info is set in peer tables
	local1, ok := table1.GetLocal()
	if !ok {
		t.Fatal("expected local info to be set in table1")
	}
	if local1.Hostname != "node-1" || local1.Priority != 1 || !local1.IsPresent {
		t.Errorf("unexpected local info: %+v", local1)
	}

	local2, ok := table2.GetLocal()
	if !ok {
		t.Fatal("expected local info to be set in table2")
	}
	if local2.Hostname != "node-2" || local2.Priority != 2 || local2.IsPresent {
		t.Errorf("unexpected local info: %+v", local2)
	}
}

// TestPresenceGracefulLeave verifies that when a peer closes its presence
// service, it publishes a Leaving message and the remaining peer removes
// it from the table immediately (without waiting for stale timeout).
func TestPresenceGracefulLeave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	channelID := "test-presence-leave"
	ps1, _ := NewPubSub(ctx, h1, channelID)
	ps2, _ := NewPubSub(ctx, h2, channelID)

	table1 := NewPeerTable()
	table2 := NewPeerTable()

	// Use a long stale timeout so we know any removal is from the Leaving
	// announcement, not the stale cleanup loop.
	svc1 := NewPresenceService(h1, ps1, table1, PresenceConfig{
		Hostname:          "node-1",
		Priority:          1,
		HeartbeatInterval: 500 * time.Millisecond,
		StaleTimeout:      60 * time.Second,
	})
	runPresence(t, ctx, svc1)

	svc2 := NewPresenceService(h2, ps2, table2, PresenceConfig{
		Hostname:          "node-2",
		Priority:          2,
		HeartbeatInterval: 500 * time.Millisecond,
		StaleTimeout:      60 * time.Second,
	})
	// Manage svc2 manually so we can cancel its context before the test ends.
	svc2Ctx, svc2Cancel := context.WithCancel(ctx)
	svc2Done := make(chan struct{})
	go func() {
		defer close(svc2Done)
		_ = svc2.Run(svc2Ctx)
	}()

	svc1.SetPresent(ctx, true)
	svc2.SetPresent(ctx, true)

	// Wait until node-1's table sees node-2.
	waitFor(t, 10*time.Second, func() bool {
		_, ok := table1.Get(h2.ID())
		return ok
	}, "node-1 to see node-2")

	// node-2 leaves gracefully (ctx cancel triggers shutdown + leaving announcement).
	svc2Cancel()
	<-svc2Done

	// node-1 should remove node-2 from its table well before stale timeout.
	waitFor(t, 5*time.Second, func() bool {
		_, ok := table1.Get(h2.ID())
		return !ok
	}, "node-2 to be removed from node-1's table")
}

// waitFor polls cond until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestNotificationBroadcastAndReceive tests that notifications are broadcast
// to all peers and each peer's handler is invoked.
func TestNotificationBroadcastAndReceive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	channelID := "test-notify-broadcast"
	ps1, _ := NewPubSub(ctx, h1, channelID)
	ps2, _ := NewPubSub(ctx, h2, channelID)

	table1 := NewPeerTable()
	table2 := NewPeerTable()

	// Start presence so nodes know about each other
	pres1 := NewPresenceService(h1, ps1, table1, PresenceConfig{
		Hostname:          "sender",
		Priority:          1,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	runPresence(t, ctx, pres1)

	pres2 := NewPresenceService(h2, ps2, table2, PresenceConfig{
		Hostname:          "receiver",
		Priority:          2,
		HeartbeatInterval: 1 * time.Second,
		StaleTimeout:      5 * time.Second,
	})
	runPresence(t, ctx, pres2)

	// Set presence — internal heartbeat re-broadcasts until mesh is ready.
	pres1.SetPresent(ctx, true)
	pres2.SetPresent(ctx, true)

	// Wait for presence to propagate before sending notifications.
	deadline := time.After(15 * time.Second)
	for {
		_, ok1 := table1.Get(h2.ID())
		_, ok2 := table2.Get(h1.ID())
		if ok1 && ok2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for presence exchange")
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Track received notifications on node 2
	var receivedMu sync.Mutex
	var receivedNotifs []protocol.NotifyRequest

	// Node 1: sender
	notify1 := NewNotifyService(h1, ps1, NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			t.Error("sender should not receive its own notification")
			return protocol.NotifyResponse{}, nil
		},
	})
	runNotify(t, ctx, notify1)

	// Node 2: receiver — handler always acts
	notify2 := NewNotifyService(h2, ps2, NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			receivedMu.Lock()
			receivedNotifs = append(receivedNotifs, req)
			receivedMu.Unlock()
			return protocol.NotifyResponse{ID: 42, Event: "displayed"}, nil
		},
	})
	runNotify(t, ctx, notify2)

	// Give GossipSub time to establish mesh
	time.Sleep(1 * time.Second)

	// Publish a notification from node 1
	req := protocol.NotifyRequest{
		AppName: "test-app",
		Summary: "Hello",
		Body:    "World",
	}

	resp, ok := notify1.Publish(ctx, req, 5*time.Second)
	if !ok {
		t.Fatal("expected action reply from receiver")
	}
	if resp.Event != "displayed" {
		t.Errorf("expected event 'displayed', got %q", resp.Event)
	}

	// Verify node 2 received it
	receivedMu.Lock()
	count := len(receivedNotifs)
	receivedMu.Unlock()

	if count == 0 {
		t.Fatal("expected receiver to get the notification")
	}

	receivedMu.Lock()
	got := receivedNotifs[0]
	receivedMu.Unlock()

	if got.Summary != "Hello" || got.Body != "World" {
		t.Errorf("unexpected notification: %+v", got)
	}
}

// TestNotificationNoReplyTimeout tests that Publish times out when
// no peer sends an action reply.
func TestNotificationNoReplyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h1 := testHost(t)
	// Only one node — nobody to reply

	channelID := "test-no-reply"
	ps1, _ := NewPubSub(ctx, h1, channelID)

	notify1 := NewNotifyService(h1, ps1, NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			return protocol.NotifyResponse{}, nil
		},
	})
	runNotify(t, ctx, notify1)

	time.Sleep(500 * time.Millisecond)

	// Publish — should timeout since no other peers to reply
	_, ok := notify1.Publish(ctx, protocol.NotifyRequest{Summary: "test"}, 1*time.Second)
	if ok {
		t.Fatal("expected no reply (no peers)")
	}
}

// TestNotificationHandlerError tests that handler errors don't produce action replies.
func TestNotificationHandlerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h1 := testHost(t)
	h2 := testHost(t)
	connectHosts(t, h1, h2)

	channelID := "test-handler-error"
	ps1, _ := NewPubSub(ctx, h1, channelID)
	ps2, _ := NewPubSub(ctx, h2, channelID)

	// Node 1: sender
	notify1 := NewNotifyService(h1, ps1, NotifyServiceConfig{})
	runNotify(t, ctx, notify1)

	// Node 2: handler returns error (e.g., no matching route)
	notify2 := NewNotifyService(h2, ps2, NotifyServiceConfig{
		Handler: func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
			return protocol.NotifyResponse{}, fmt.Errorf("no matching route found")
		},
	})
	runNotify(t, ctx, notify2)

	time.Sleep(2 * time.Second)

	// Publish — should timeout because handler returned error (no action reply)
	_, ok := notify1.Publish(ctx, protocol.NotifyRequest{Summary: "test"}, 2*time.Second)
	if ok {
		t.Fatal("expected no reply when handler errors")
	}
}
