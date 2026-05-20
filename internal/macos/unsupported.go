//go:build darwin && !cgo

package macos

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// NotifyChannel sends notifications via osascript when CGO is unavailable.
type NotifyChannel struct{}

// NewNotifyChannel creates a new osascript-backed notification channel.
func NewNotifyChannel() *NotifyChannel {
	return &NotifyChannel{}
}

// Name returns the channel name.
func (c *NotifyChannel) Name() string {
	return "macos"
}

// Run blocks until the context is cancelled.
func (c *NotifyChannel) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Send delivers a notification using osascript.
func (c *NotifyChannel) Send(_ context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	title := req.Summary
	if title == "" {
		title = "notify-relay"
	}
	script := fmt.Sprintf(`display notification %q with title %q`, req.Body, title)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("osascript: %w", err)
	}
	return protocol.NotifyResponse{}, nil
}

// Close is a no-op.
func (c *NotifyChannel) Close() error {
	return nil
}

// Capabilities returns basic capabilities.
func (c *NotifyChannel) Capabilities(_ context.Context) ([]string, error) {
	return []string{"body"}, nil
}

// ServerInfo returns information about the notification server.
func (c *NotifyChannel) ServerInfo(_ context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{
		Name:    "macos",
		Vendor:  "Apple",
		Version: "1.0",
		Spec:    "1.0",
	}, nil
}

// IsAvailable returns true because osascript is always present.
func (c *NotifyChannel) IsAvailable(_ context.Context) (bool, error) {
	return true, nil
}

// LockDetector polls ioreg to detect screen lock when CGO is unavailable.
type LockDetector struct {
	mu       sync.RWMutex
	isLocked bool
	onChange func(bool)
	ready    chan struct{}
}

// NewLockDetector creates a new ioreg-based lock detector.
func NewLockDetector() *LockDetector {
	return &LockDetector{
		ready: make(chan struct{}),
	}
}

// IsScreenLocked implements condition.Evaluator.
func (d *LockDetector) IsScreenLocked() bool {
	<-d.ready
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isLocked
}

// IsLocked is an alias for IsScreenLocked.
func (d *LockDetector) IsLocked() bool {
	return d.IsScreenLocked()
}

// IsAvailable always returns true.
func (d *LockDetector) IsAvailable(_ context.Context) (bool, error) {
	return true, nil
}

// Run polls the lock state every 2 seconds until ctx is cancelled.
func (d *LockDetector) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	d.check()
	close(d.ready)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			d.check()
		}
	}
}

func (d *LockDetector) check() {
	locked := d.lockedViaIOReg()

	d.mu.Lock()
	changed := d.isLocked != locked
	d.isLocked = locked
	cb := d.onChange
	d.mu.Unlock()

	if changed && cb != nil {
		cb(locked)
	}
}

func (d *LockDetector) lockedViaIOReg() bool {
	out, err := exec.Command("ioreg", "-n", "Root", "-d1").Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte("CGSSessionScreenIsLocked"))
}

// SetChangeCallback registers a callback invoked when the lock state changes.
func (d *LockDetector) SetChangeCallback(fn func(bool)) {
	d.mu.Lock()
	d.onChange = fn
	d.mu.Unlock()
}
