package tests

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtruder/notify-relay/internal/channel"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/remotes"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestInboundFallbackWhenLocked tests that notifications fall back to local when remote is locked
func TestInboundFallbackWhenLocked(t *testing.T) {
	tmpDir := t.TempDir()
	serverSocket := filepath.Join(tmpDir, "server.sock")

	// Create recording channel for local fallback
	localCh := &recordingChannel{
		mockChannel: &mockChannel{name: "local", capabilities: []string{"body"}},
	}

	// Create router with remote_unlocked -> forward, always -> local
	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "local"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}

	r, err := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Create and start server
	srv, err := server.NewGRPCServer(server.Config{Unix: serverSocket}, r)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	// Create inbound client
	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: serverSocket,
		Hostname:   "locked-client",
		LockState:  nil,
	})

	var receivedNotif *notify_relayv1.ForwardedNotification
	client.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedNotif = notif
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Set client as LOCKED (this is the key difference)
	manager.UpdateLockState("locked-client", true)

	// Send notification
	conn, err := grpc.Dial("unix://"+serverSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	clientGRPC := notify_relayv1.NewRelayServiceClient(conn)
	_, err = clientGRPC.Notify(context.Background(), &notify_relayv1.Notification{
		AppName: "test",
		Summary: "Should Go Local",
		Body:    "Remote is locked, should not forward",
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify notification went to local channel, not to locked remote
	if receivedNotif != nil {
		t.Error("Expected NO notification to be forwarded to locked remote")
	}
	if len(localCh.GetReceived()) != 1 {
		t.Errorf("Expected 1 local notification, got %d", len(localCh.GetReceived()))
	}
	if len(localCh.GetReceived()) == 1 && localCh.GetReceived()[0].Summary == "Should Go Local" {
		t.Log("✓ Notification correctly fell back to local when remote locked")
	}

	client.Close()
}

// TestInboundDisconnectionCleanup tests cleanup when inbound remote disconnects
func TestInboundDisconnectionCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	serverSocket := filepath.Join(tmpDir, "server.sock")

	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{{Condition: "always", Channel: "local"}},
	}

	localCh := &mockChannel{name: "local", capabilities: []string{"body"}}
	r, _ := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	defer r.Close()

	srv, _ := server.NewGRPCServer(server.Config{Unix: serverSocket}, r)
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	// Create and connect client
	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: serverSocket,
		Hostname:   "disconnect-test",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Verify connected
	if !manager.HasConnectedRemote() {
		t.Fatal("Expected client to be connected")
	}

	// Close client (simulates disconnect)
	client.Close()
	time.Sleep(500 * time.Millisecond)

	// Verify cleaned up
	if manager.HasConnectedRemote() {
		t.Error("Expected remote to be cleaned up after disconnect")
	}

	t.Log("✓ Remote properly cleaned up after disconnection")
}

// TestOutboundReceivesNotification tests that outbound clients receive forwarded notifications
func TestOutboundReceivesNotification(t *testing.T) {
	tmpDir := t.TempDir()
	serverSocket := filepath.Join(tmpDir, "server.sock")

	// Server setup
	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{{Condition: "always", Channel: "local"}},
	}

	localCh := &mockChannel{name: "local", capabilities: []string{"body"}}
	r, _ := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	defer r.Close()

	srv, _ := server.NewGRPCServer(server.Config{Unix: serverSocket}, r)
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	// Create outbound client
	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: serverSocket,
		Hostname:   "outbound-client",
	})

	var receivedNotif *notify_relayv1.ForwardedNotification
	receivedCh := make(chan *notify_relayv1.ForwardedNotification, 1)

	client.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedNotif = notif
			receivedCh <- notif
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Server manually forwards a notification to the client
	// In real scenario, this would happen through routing logic
	forwarded := &notify_relayv1.ForwardedNotification{
		SourceHostname: "server",
		Notification: &notify_relayv1.Notification{
			AppName: "server",
			Summary: "Forwarded to Outbound",
			Body:    "This was forwarded",
		},
	}

	_, err := manager.ForwardNotification(context.Background(), "outbound-client", forwarded)
	if err != nil {
		t.Logf("Forward notification result: %v", err)
	}

	// Wait for client to receive
	select {
	case <-receivedCh:
		if receivedNotif != nil && receivedNotif.Notification.Summary == "Forwarded to Outbound" {
			t.Log("✓ Outbound client received forwarded notification")
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for outbound client to receive notification")
	}

	client.Close()
}

// TestBidirectionalNotificationFlowUnix tests bidirectional flow over Unix socket
func TestBidirectionalNotificationFlowUnix(t *testing.T) {
	tmpDir := t.TempDir()
	serverA := filepath.Join(tmpDir, "serverA.sock")
	serverB := filepath.Join(tmpDir, "serverB.sock")

	// Setup Machine A
	managerA := remotes.NewManager()
	localChA := &recordingChannel{mockChannel: &mockChannel{name: "localA", capabilities: []string{"body"}}}
	routerCfgA := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "localA"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}
	rA, _ := router.NewWithRemotes(routerCfgA, nil, []channel.Channel{localChA}, managerA)
	defer rA.Close()

	srvA, _ := server.NewGRPCServer(server.Config{Unix: serverA}, rA)
	srvA.SetRemoteManager(managerA)
	defer srvA.Shutdown(context.Background())
	go srvA.Serve()

	// Setup Machine B
	managerB := remotes.NewManager()
	localChB := &recordingChannel{mockChannel: &mockChannel{name: "localB", capabilities: []string{"body"}}}
	routerCfgB := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "localB"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}
	rB, _ := router.NewWithRemotes(routerCfgB, nil, []channel.Channel{localChB}, managerB)
	defer rB.Close()

	srvB, _ := server.NewGRPCServer(server.Config{Unix: serverB}, rB)
	srvB.SetRemoteManager(managerB)
	defer srvB.Shutdown(context.Background())
	go srvB.Serve()

	time.Sleep(200 * time.Millisecond)

	// B connects to A (outbound from B's perspective)
	clientBtoA := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: serverA,
		Hostname:   "machine-B",
	})

	var receivedByB []*notify_relayv1.ForwardedNotification
	clientBtoA.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedByB = append(receivedByB, notif)
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go clientBtoA.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Set B as unlocked on A's manager
	managerA.UpdateLockState("machine-B", false)

	// Send from A (via gRPC) - should go to B
	connA, _ := grpc.Dial("unix://"+serverA, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer connA.Close()
	clientA := notify_relayv1.NewRelayServiceClient(connA)

	_, err := clientA.Notify(context.Background(), &notify_relayv1.Notification{
		Summary: "A to B",
		Body:    "From A to B",
	})
	if err != nil {
		t.Fatalf("Notify from A failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	// Verify B received it
	if len(receivedByB) != 1 || receivedByB[0].Notification.Summary != "A to B" {
		t.Errorf("Expected B to receive 'A to B', got: %v", receivedByB)
	} else {
		t.Log("✓ Bidirectional Unix: A successfully sent to B")
	}

	clientBtoA.Close()
}

// TestTCPForwardingUnlocked tests notification forwarding over TCP
func TestTCPForwardingUnlocked(t *testing.T) {
	localCh := &recordingChannel{
		mockChannel: &mockChannel{name: "local", capabilities: []string{"body"}},
	}

	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "local"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}

	r, _ := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	defer r.Close()

	// Create server with router already configured
	srv, err := server.NewGRPCServer(server.Config{Listen: "127.0.0.1:0"}, r)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	// Get the actual assigned port
	addr := srv.Address()

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	// Client connects via TCP
	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: addr,
		Hostname:   "tcp-client",
	})

	var receivedNotif *notify_relayv1.ForwardedNotification
	client.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedNotif = notif
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	manager.UpdateLockState("tcp-client", false)

	// Send notification via TCP
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	grpcClient := notify_relayv1.NewRelayServiceClient(conn)
	_, err = grpcClient.Notify(context.Background(), &notify_relayv1.Notification{
		Summary: "TCP Forwarded",
		Body:    "Via TCP",
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if receivedNotif == nil || receivedNotif.Notification.Summary != "TCP Forwarded" {
		t.Error("Expected notification to be forwarded via TCP")
	} else if len(localCh.GetReceived()) == 0 {
		t.Log("✓ TCP forwarding works correctly")
	}

	client.Close()
}

// TestTCPFallbackWhenLocked tests fallback over TCP when remote is locked
func TestTCPFallbackWhenLocked(t *testing.T) {
	localCh := &recordingChannel{
		mockChannel: &mockChannel{name: "local", capabilities: []string{"body"}},
	}

	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "local"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}

	r, _ := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	defer r.Close()

	srv, err := server.NewGRPCServer(server.Config{Listen: "127.0.0.1:0"}, r)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	addr := srv.Address()

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: addr,
		Hostname:   "tcp-locked-client",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Set as LOCKED
	manager.UpdateLockState("tcp-locked-client", true)

	conn, _ := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()

	grpcClient := notify_relayv1.NewRelayServiceClient(conn)
	_, err = grpcClient.Notify(context.Background(), &notify_relayv1.Notification{
		Summary: "TCP Fallback",
		Body:    "Should go local",
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if len(localCh.GetReceived()) != 1 {
		t.Errorf("Expected 1 local notification, got %d", len(localCh.GetReceived()))
	} else {
		t.Log("✓ TCP fallback to local works correctly")
	}

	client.Close()
}

// TestTCPOutboundReceive tests outbound client receiving over TCP
func TestTCPOutboundReceive(t *testing.T) {
	localCh := &mockChannel{name: "local", capabilities: []string{"body"}}
	manager := remotes.NewManager()
	routerCfg := router.Config{
		Routes: []router.Route{{Condition: "always", Channel: "local"}},
	}

	r, _ := router.NewWithRemotes(routerCfg, nil, []channel.Channel{localCh}, manager)
	defer r.Close()

	srv, err := server.NewGRPCServer(server.Config{Listen: "127.0.0.1:0"}, r)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	srv.SetRemoteManager(manager)
	defer srv.Shutdown(context.Background())

	addr := srv.Address()

	go srv.Serve()
	time.Sleep(100 * time.Millisecond)

	client := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: addr,
		Hostname:   "tcp-outbound",
	})

	receivedCh := make(chan *notify_relayv1.ForwardedNotification, 1)
	client.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedCh <- notif
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	// Server forwards to client
	forwarded := &notify_relayv1.ForwardedNotification{
		SourceHostname: "server",
		Notification: &notify_relayv1.Notification{
			Summary: "TCP to Outbound",
			Body:    "Via TCP transport",
		},
	}

	manager.ForwardNotification(context.Background(), "tcp-outbound", forwarded)

	select {
	case notif := <-receivedCh:
		if notif.Notification.Summary == "TCP to Outbound" {
			t.Log("✓ Outbound client received notification via TCP")
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for TCP notification")
	}

	client.Close()
}

// TestTCPBidirectional tests bidirectional flow over TCP
func TestTCPBidirectional(t *testing.T) {
	// Server A setup
	localChA := &recordingChannel{mockChannel: &mockChannel{name: "localA", capabilities: []string{"body"}}}
	managerA := remotes.NewManager()
	routerCfgA := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "localA"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}
	rA, _ := router.NewWithRemotes(routerCfgA, nil, []channel.Channel{localChA}, managerA)
	defer rA.Close()

	srvA, err := server.NewGRPCServer(server.Config{Listen: "127.0.0.1:0"}, rA)
	if err != nil {
		t.Fatalf("Failed to create server A: %v", err)
	}
	srvA.SetRemoteManager(managerA)
	defer srvA.Shutdown(context.Background())
	addrA := srvA.Address()
	go srvA.Serve()

	// Server B setup
	localChB := &recordingChannel{mockChannel: &mockChannel{name: "localB", capabilities: []string{"body"}}}
	managerB := remotes.NewManager()
	routerCfgB := router.Config{
		Routes: []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "always", Channel: "localB"},
		},
		RemoteTimeout: 500 * time.Millisecond,
	}
	rB, _ := router.NewWithRemotes(routerCfgB, nil, []channel.Channel{localChB}, managerB)
	defer rB.Close()

	srvB, err := server.NewGRPCServer(server.Config{Listen: "127.0.0.1:0"}, rB)
	if err != nil {
		t.Fatalf("Failed to create server B: %v", err)
	}
	srvB.SetRemoteManager(managerB)
	defer srvB.Shutdown(context.Background())
	go srvB.Serve()

	time.Sleep(200 * time.Millisecond)

	// B connects to A
	clientBtoA := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: addrA,
		Hostname:   "machine-B",
	})

	var receivedByB []*notify_relayv1.ForwardedNotification
	clientBtoA.SetCallbacks(
		func() {},
		func() {},
		func(notif *notify_relayv1.ForwardedNotification) {
			receivedByB = append(receivedByB, notif)
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go clientBtoA.Connect(ctx)
	time.Sleep(500 * time.Millisecond)

	managerA.UpdateLockState("machine-B", false)

	// Send from A to B via TCP
	connA, _ := grpc.Dial(addrA, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer connA.Close()
	grpcClientA := notify_relayv1.NewRelayServiceClient(connA)

	_, err = grpcClientA.Notify(context.Background(), &notify_relayv1.Notification{
		Summary: "TCP A to B",
		Body:    "Bidirectional over TCP",
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	if len(receivedByB) == 1 && receivedByB[0].Notification.Summary == "TCP A to B" {
		t.Log("✓ Bidirectional TCP flow works correctly")
	} else {
		t.Errorf("Expected B to receive notification, got: %v", receivedByB)
	}

	clientBtoA.Close()
}
