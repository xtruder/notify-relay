package tests

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/condition"
	"github.com/xtruder/notify-relay/internal/ntfy"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/router"
)

// mockChannel is a test channel that records sent notifications
type mockChannel struct {
	name         string
	sentRequests []protocol.NotifyRequest
	capabilities []string
}

func (m *mockChannel) Name() string {
	return m.name
}

func (m *mockChannel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	m.sentRequests = append(m.sentRequests, req)
	return protocol.NotifyResponse{ID: uint32(len(m.sentRequests))}, nil
}

func (m *mockChannel) Close() error {
	return nil
}

func (m *mockChannel) Capabilities(ctx context.Context) ([]string, error) {
	return m.capabilities, nil
}

func (m *mockChannel) ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{Name: m.name}, nil
}

// mockEvaluator simulates screen lock state
type mockEvaluator struct {
	locked bool
}

func (m *mockEvaluator) IsScreenLocked() bool {
	return m.locked
}

// TestRouterBasicRouting tests basic routing to different channels
func TestRouterBasicRouting(t *testing.T) {
	ctx := context.Background()

	// Create mock channels
	desktopCh := &mockChannel{name: "dbus", capabilities: []string{"body", "actions"}}
	mobileCh := &mockChannel{name: "ntfy", capabilities: []string{"body"}}

	// Create router with routes: screen_locked -> ntfy, always -> dbus
	cfg := router.Config{
		Routes: []router.Route{
			{Condition: "screen_locked", Channel: "ntfy"},
			{Condition: "always", Channel: "dbus"},
		},
	}

	// Test 1: When unlocked, should route to dbus
	evaluator := &mockEvaluator{locked: false}
	r, err := router.New(cfg, evaluator, []channel.Channel{desktopCh, mobileCh})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	req := protocol.NotifyRequest{
		AppName: "test",
		Summary: "Test notification",
		Body:    "This is a test",
	}

	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification: %v", err)
	}

	if len(desktopCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to desktop channel, got %d", len(desktopCh.sentRequests))
	}
	if len(mobileCh.sentRequests) != 0 {
		t.Errorf("Expected 0 requests to mobile channel when unlocked, got %d", len(mobileCh.sentRequests))
	}
	if resp.ID != 1 {
		t.Errorf("Expected response ID 1, got %d", resp.ID)
	}

	// Test 2: When locked, should route to ntfy
	evaluator.locked = true
	desktopCh.sentRequests = nil

	resp, err = r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification when locked: %v", err)
	}

	if len(desktopCh.sentRequests) != 0 {
		t.Errorf("Expected 0 requests to desktop channel when locked, got %d", len(desktopCh.sentRequests))
	}
	if len(mobileCh.sentRequests) != 1 {
		t.Errorf("Expected 1 request to mobile channel when locked, got %d", len(mobileCh.sentRequests))
	}
	if resp.ID != 1 {
		t.Errorf("Expected response ID 1 from mobile, got %d", resp.ID)
	}
}

// TestRouterFallback tests that router falls back when no conditions match
func TestRouterFallback(t *testing.T) {
	ctx := context.Background()

	desktopCh := &mockChannel{name: "dbus", capabilities: []string{"body"}}

	// Create router with only screen_locked route (no match when unlocked)
	cfg := router.Config{
		Routes: []router.Route{
			{Condition: "screen_locked", Channel: "dbus"},
		},
	}

	evaluator := &mockEvaluator{locked: false}
	r, err := router.New(cfg, evaluator, []channel.Channel{desktopCh})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	req := protocol.NotifyRequest{
		Summary: "Test",
		Body:    "Body",
	}

	_, err = r.Notify(ctx, req)
	if err == nil {
		t.Error("Expected error when no route matches, got nil")
	}
}

// TestRouterAlwaysCondition tests the "always" condition
func TestRouterAlwaysCondition(t *testing.T) {
	ctx := context.Background()

	ch := &mockChannel{name: "test", capabilities: []string{"body"}}

	cfg := router.Config{
		Routes: []router.Route{
			{Condition: "always", Channel: "test"},
		},
	}

	// Always condition should work even without evaluator
	r, err := router.New(cfg, nil, []channel.Channel{ch})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	req := protocol.NotifyRequest{Summary: "Test", Body: "Body"}
	_, err = r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send with always condition: %v", err)
	}

	if len(ch.sentRequests) != 1 {
		t.Errorf("Expected 1 request, got %d", len(ch.sentRequests))
	}
}

// TestNtfyChannel tests the ntfy channel with the test topic
// This is a live integration test that actually sends to ntfy.sh
func TestNtfyChannel(t *testing.T) {
	// Skip in CI environments without network access
	if os.Getenv("CI") == "true" && os.Getenv("TEST_NTFY") != "1" {
		t.Skip("Skipping live ntfy test in CI. Set TEST_NTFY=1 to run.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use the test topic provided
	cfg := ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  "offlinehq-opencode",
	}

	ch, err := ntfy.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create ntfy channel: %v", err)
	}
	defer ch.Close()

	req := protocol.NotifyRequest{
		AppName: "notify-relay-test",
		Summary: "Integration Test",
		Body:    "This is a test notification from notify-relay integration tests",
		Hints: []protocol.Hint{
			{Name: "urgency", Type: "byte", Value: "1"}, // normal priority
		},
	}

	resp, err := ch.Send(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send notification to ntfy: %v", err)
	}

	if resp.ID == 0 {
		t.Error("Expected non-zero response ID")
	}

	t.Logf("Successfully sent notification to ntfy.sh/offlinehq-opencode, got ID: %d", resp.ID)
	t.Log("Check https://ntfy.sh/offlinehq-opencode to see the notification")
}

// TestNtfyChannelPriority tests urgency mapping to ntfy priorities
func TestNtfyChannelPriority(t *testing.T) {
	// Skip in CI environments without network access
	if os.Getenv("CI") == "true" && os.Getenv("TEST_NTFY") != "1" {
		t.Skip("Skipping live ntfy test in CI. Set TEST_NTFY=1 to run.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  "offlinehq-opencode",
	}

	ch, err := ntfy.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create ntfy channel: %v", err)
	}
	defer ch.Close()

	// Test low urgency
	lowReq := protocol.NotifyRequest{
		Summary: "Low Priority Test",
		Body:    "This should have low priority (1)",
		Hints:   []protocol.Hint{{Name: "urgency", Type: "byte", Value: "0"}},
	}
	_, err = ch.Send(ctx, lowReq)
	if err != nil {
		t.Fatalf("Failed to send low priority: %v", err)
	}
	t.Log("Sent low priority notification")

	// Test critical urgency
	criticalReq := protocol.NotifyRequest{
		Summary: "Critical Priority Test",
		Body:    "This should have max priority (5)",
		Hints:   []protocol.Hint{{Name: "urgency", Type: "byte", Value: "2"}},
	}
	_, err = ch.Send(ctx, criticalReq)
	if err != nil {
		t.Fatalf("Failed to send critical priority: %v", err)
	}
	t.Log("Sent critical priority notification")
}

// TestNtfyChannelWithCategory tests ntfy with category/tags
func TestNtfyChannelWithCategory(t *testing.T) {
	// Skip in CI environments without network access
	if os.Getenv("CI") == "true" && os.Getenv("TEST_NTFY") != "1" {
		t.Skip("Skipping live ntfy test in CI. Set TEST_NTFY=1 to run.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  "offlinehq-opencode",
	}

	ch, err := ntfy.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create ntfy channel: %v", err)
	}
	defer ch.Close()

	req := protocol.NotifyRequest{
		AppName: "test-app",
		Summary: "Test with Category",
		Body:    "This has a category tag",
		Hints: []protocol.Hint{
			{Name: "category", Type: "string", Value: "test,integration"},
		},
	}

	_, err = ch.Send(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send with category: %v", err)
	}
	t.Log("Sent notification with category tags")
}

// TestConditionInterfaces tests condition evaluation
func TestConditionInterfaces(t *testing.T) {
	ctx := context.Background()

	// Test Always condition
	always := condition.Always{}
	if always.Name() != "always" {
		t.Errorf("Expected name 'always', got %s", always.Name())
	}
	if !always.Evaluate(ctx, protocol.NotifyRequest{}) {
		t.Error("Always condition should always return true")
	}

	// Test ScreenLocked condition with unlocked state
	evaluator := &mockEvaluator{locked: false}
	screenLocked := condition.NewScreenLocked(evaluator)
	if screenLocked.Name() != "screen_locked" {
		t.Errorf("Expected name 'screen_locked', got %s", screenLocked.Name())
	}
	if screenLocked.Evaluate(ctx, protocol.NotifyRequest{}) {
		t.Error("ScreenLocked should return false when unlocked")
	}

	// Test ScreenLocked condition with locked state
	evaluator.locked = true
	if !screenLocked.Evaluate(ctx, protocol.NotifyRequest{}) {
		t.Error("ScreenLocked should return true when locked")
	}
}

// TestRouterCapabilitiesAndServerInfo tests capabilities and server info endpoints
func TestRouterCapabilitiesAndServerInfo(t *testing.T) {
	ctx := context.Background()

	ch := &mockChannel{
		name:         "dbus",
		capabilities: []string{"body", "actions", "icon-static"},
	}

	cfg := router.Config{
		Routes: []router.Route{
			{Condition: "always", Channel: "dbus"},
		},
	}

	r, err := router.New(cfg, nil, []channel.Channel{ch})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Test Capabilities
	caps, err := r.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}
	if len(caps) != 3 {
		t.Errorf("Expected 3 capabilities, got %d", len(caps))
	}

	// Test ServerInfo
	info, err := r.ServerInformation(ctx)
	if err != nil {
		t.Fatalf("Failed to get server info: %v", err)
	}
	if info.Name != "dbus" {
		t.Errorf("Expected name 'dbus', got %s", info.Name)
	}
}

// TestRouterCloseNotification tests closing notifications
func TestRouterCloseNotification(t *testing.T) {
	ctx := context.Background()

	// Create mock channel that implements Closer
	ch := &mockCloserChannel{
		name:         "dbus",
		capabilities: []string{"body"},
	}

	cfg := router.Config{
		Routes: []router.Route{
			{Condition: "always", Channel: "dbus"},
		},
	}

	r, err := router.New(cfg, nil, []channel.Channel{ch})
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	defer r.Close()

	// Send a notification first
	req := protocol.NotifyRequest{Summary: "Test", Body: "Body"}
	resp, err := r.Notify(ctx, req)
	if err != nil {
		t.Fatalf("Failed to send: %v", err)
	}

	// Try to close it
	err = r.CloseNotification(ctx, resp.ID)
	if err != nil {
		t.Logf("CloseNotification returned: %v", err)
	}

	if !ch.closeCalled {
		t.Error("Expected CloseNotification to be called on channel")
	}
	if ch.closedID != resp.ID {
		t.Errorf("Expected closed ID %d, got %d", resp.ID, ch.closedID)
	}
}

// mockCloserChannel extends mockChannel with Closer support
type mockCloserChannel struct {
	name         string
	sentRequests []protocol.NotifyRequest
	capabilities []string
	closeCalled  bool
	closedID     uint32
}

func (m *mockCloserChannel) Name() string {
	return m.name
}

func (m *mockCloserChannel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	m.sentRequests = append(m.sentRequests, req)
	return protocol.NotifyResponse{ID: uint32(len(m.sentRequests))}, nil
}

func (m *mockCloserChannel) Close() error {
	return nil
}

func (m *mockCloserChannel) Capabilities(ctx context.Context) ([]string, error) {
	return m.capabilities, nil
}

func (m *mockCloserChannel) ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{Name: m.name}, nil
}

func (m *mockCloserChannel) CloseNotification(ctx context.Context, id uint32) error {
	m.closeCalled = true
	m.closedID = id
	return nil
}
