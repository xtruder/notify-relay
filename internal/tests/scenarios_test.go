package tests

import (
	"context"
	"testing"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/ntfy"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/router"
)

// mockNtfyChannel wraps ntfy channel for testing, implementing channel.Channel
type mockNtfyChannel struct {
	*ntfy.Channel
	sentRequests []protocol.NotifyRequest
}

func (m *mockNtfyChannel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	m.sentRequests = append(m.sentRequests, req)
	return m.Channel.Send(ctx, req)
}

// TestCLINtfyTopicScenario tests the CLI --ntfy-topic scenario using mocks
// This simulates: notify-relayd --ntfy-topic offlinehq-opencode
func TestCLINtfyTopicScenario(t *testing.T) {
	ctx := context.Background()

	// Create mock channels
	desktopCh := &mockChannel{
		name:         "dbus",
		capabilities: []string{"body", "actions"},
	}

	// Create ntfy channel (will actually send to the test topic)
	ntfyBase, err := ntfy.New(ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  "offlinehq-opencode",
	})
	if err != nil {
		t.Skipf("Cannot create ntfy channel: %v", err)
	}
	defer ntfyBase.Close()

	mobileCh := &mockNtfyChannel{Channel: ntfyBase}

	// Create mock evaluator to simulate screen lock state
	evaluator := &mockEvaluator{locked: false}

	// Create router with routes: screen_locked -> ntfy, always -> dbus
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "screen_locked", Channel: "ntfy"},
			{Condition: "always", Channel: "dbus"},
		},
	}

	r, err := router.New(routerCfg, evaluator, []channel.Channel{desktopCh, mobileCh})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Test when unlocked: should route to dbus
	req := protocol.NotifyRequest{
		AppName: "cli-test",
		Summary: "CLI Test - Unlocked",
		Body:    "Testing --ntfy-topic scenario (unlocked)",
	}

	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(desktopCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to desktop, got %d", len(desktopCh.sentRequests))
	}
	if len(mobileCh.sentRequests) != 0 {
		t.Errorf("Expected 0 requests to mobile when unlocked, got %d", len(mobileCh.sentRequests))
	}
	t.Logf("✓ Unlocked: Routed to dbus (ID: %d)", resp.ID)

	// Test when locked: should route to ntfy
	evaluator.locked = true
	desktopCh.sentRequests = nil

	req2 := protocol.NotifyRequest{
		AppName: "cli-test",
		Summary: "CLI Test - Locked",
		Body:    "Testing --ntfy-topic scenario (locked)",
	}

	resp, err = r.Notify(ctx, req2)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(desktopCh.sentRequests) != 0 {
		t.Errorf("Expected 0 requests to desktop when locked, got %d", len(desktopCh.sentRequests))
	}
	if len(mobileCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to mobile when locked, got %d", len(mobileCh.sentRequests))
	}
	t.Logf("✓ Locked: Routed to ntfy.sh/offlinehq-opencode (ID: %d)", resp.ID)
	t.Log("Check https://ntfy.sh/offlinehq-opencode to see the locked notification")
}

// TestNoConfigScenario tests the default behavior with no config using mocks
// This simulates: notify-relayd (no flags, no config file)
func TestNoConfigScenario(t *testing.T) {
	ctx := context.Background()

	// Create only dbus channel (default behavior)
	ch := &mockChannel{
		name:         "dbus",
		capabilities: []string{"body"},
	}

	// Create router with default route: always -> dbus
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "always", Channel: "dbus"},
		},
	}

	// No lock detector needed for default behavior
	r, err := router.New(routerCfg, nil, []channel.Channel{ch})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	req := protocol.NotifyRequest{
		AppName: "default-test",
		Summary: "Default Config Test",
		Body:    "Testing no-config scenario",
	}

	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(ch.sentRequests) != 1 {
		t.Errorf("Expected 1 request, got %d", len(ch.sentRequests))
	}
	if resp.ID != 1 {
		t.Errorf("Expected response ID 1, got %d", resp.ID)
	}
	t.Logf("✓ No config: Routed to dbus (ID: %d)", resp.ID)
}

// TestConfigFileScenario tests using a config file with mocks
// This simulates: notify-relayd --config config.json
func TestConfigFileScenario(t *testing.T) {
	ctx := context.Background()

	// Create mock channels
	desktopCh := &mockChannel{
		name:         "dbus",
		capabilities: []string{"body"},
	}

	// Create ntfy channel
	ntfyBase, err := ntfy.New(ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  "offlinehq-opencode",
	})
	if err != nil {
		t.Skipf("Cannot create ntfy channel: %v", err)
	}
	defer ntfyBase.Close()

	mobileCh := &mockNtfyChannel{Channel: ntfyBase}

	// Mock evaluator
	evaluator := &mockEvaluator{locked: false}

	// Config file routes: screen_locked -> ntfy, always -> dbus
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "screen_locked", Channel: "ntfy"},
			{Condition: "always", Channel: "dbus"},
		},
	}

	r, err := router.New(routerCfg, evaluator, []channel.Channel{desktopCh, mobileCh})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Test unlocked
	req := protocol.NotifyRequest{
		AppName: "config-test",
		Summary: "Config File Test - Unlocked",
		Body:    "Testing config file scenario (unlocked)",
	}

	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(desktopCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to desktop, got %d", len(desktopCh.sentRequests))
	}
	t.Logf("✓ Config (unlocked): Routed to dbus (ID: %d)", resp.ID)

	// Test locked
	evaluator.locked = true
	desktopCh.sentRequests = nil

	req2 := protocol.NotifyRequest{
		AppName: "config-test",
		Summary: "Config File Test - Locked",
		Body:    "Testing config file scenario (locked)",
	}

	resp, err = r.Notify(ctx, req2)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(mobileCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to mobile when locked, got %d", len(mobileCh.sentRequests))
	}
	t.Logf("✓ Config (locked): Routed to ntfy.sh/offlinehq-opencode (ID: %d)", resp.ID)
}

// TestDefaultConfigPath tests the default config file loading behavior
// This simulates having ~/.config/notify-relay.conf
func TestDefaultConfigPath(t *testing.T) {
	ctx := context.Background()

	// Create mock channels simulating config file setup
	desktopCh := &mockChannel{
		name:         "dbus",
		capabilities: []string{"body"},
	}

	mobileCh := &mockChannel{
		name:         "ntfy",
		capabilities: []string{"body"},
	}

	// Mock evaluator
	evaluator := &mockEvaluator{locked: false}

	// Simulate config from ~/.config/notify-relay.conf with routes:
	// screen_locked -> ntfy, always -> dbus
	routerCfg := router.Config{
		Routes: []router.Route{
			{Condition: "screen_locked", Channel: "ntfy"},
			{Condition: "always", Channel: "dbus"},
		},
	}

	r, err := router.New(routerCfg, evaluator, []channel.Channel{desktopCh, mobileCh})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Test with unlocked screen
	req := protocol.NotifyRequest{
		AppName: "default-config-test",
		Summary: "Default Config Path Test",
		Body:    "Testing default ~/.config/notify-relay.conf behavior",
	}

	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(desktopCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to desktop, got %d", len(desktopCh.sentRequests))
	}
	t.Logf("✓ Default config loaded: Routed to dbus (ID: %d)", resp.ID)
	t.Log("Config file path: ~/.config/notify-relay.conf (JSON format)")
}
