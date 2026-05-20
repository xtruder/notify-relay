//go:build darwin

package macos

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
int macosIsScreenLocked() {
	CFDictionaryRef dict = CGSessionCopyCurrentDictionary();
	if (!dict) {
		return -1;
	}
	CFBooleanRef locked = CFDictionaryGetValue(dict, CFSTR("CGSSessionScreenIsLocked"));
	int result = (locked == kCFBooleanTrue) ? 1 : 0;
	CFRelease(dict);
	return result;
}
*/
import "C"

// LockDetector polls the macOS session state to detect screen lock.
type LockDetector struct {
	mu       sync.RWMutex
	isLocked bool
	onChange func(bool)
	ready    chan struct{}
}

// NewLockDetector creates a new macOS lock detector.
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

// IsAvailable probes whether screen lock detection works in this session.
func (d *LockDetector) IsAvailable(_ context.Context) (bool, error) {
	res := C.macosIsScreenLocked()
	return res >= 0, nil
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
	res := C.macosIsScreenLocked()
	if res < 0 {
		return
	}
	locked := res == 1

	d.mu.Lock()
	changed := d.isLocked != locked
	d.isLocked = locked
	cb := d.onChange
	d.mu.Unlock()

	if changed {
		slog.Debug("macos lock state changed", "locked", locked)
		if cb != nil {
			cb(locked)
		}
	}
}

// SetChangeCallback registers a callback invoked when the lock state changes.
func (d *LockDetector) SetChangeCallback(fn func(bool)) {
	d.mu.Lock()
	d.onChange = fn
	d.mu.Unlock()
}
