package dbus

import (
	"context"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// LockDetector monitors screen lock state via dbus.
//
// Lifecycle:
//   - NewLockDetector constructs the detector; no I/O is performed.
//   - Run(ctx) connects to the session bus, subscribes to screensaver
//     signals, queries the initial state, then blocks consuming signals
//     until ctx is cancelled. Before returning it closes the dbus
//     connection.
//
// IsAvailable can be called independently to probe whether screen lock
// detection is supported on this system.
type LockDetector struct {
	mu       sync.RWMutex
	isLocked bool
	onChange func(bool)

	// conn and signals are set by Start before ready is closed, then read-only.
	conn    *dbus.Conn
	signals chan *dbus.Signal

	ready chan struct{}
}

// NewLockDetector constructs the detector. No dbus connection is opened
// until Start.
func NewLockDetector() *LockDetector {
	return &LockDetector{
		ready: make(chan struct{}),
	}
}

// screensaverServices lists dbus service names that LockDetector knows how
// to query for lock state. KDE Plasma also registers under
// org.freedesktop.ScreenSaver.
var screensaverServices = []string{
	"org.gnome.ScreenSaver",
	"org.freedesktop.ScreenSaver",
}

// IsAvailable probes whether a supported screensaver service is registered
// on the session bus. It opens a short-lived connection independent of
// Start. Returns (true, nil) if at least one screensaver service is
// available, (false, nil) if the session bus is unreachable or no
// supported screensaver service is registered.
func (d *LockDetector) IsAvailable(_ context.Context) (bool, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, nil
	}
	defer conn.Close()

	var names []string
	call := conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0)
	if call.Err != nil {
		return false, fmt.Errorf("list bus names: %w", call.Err)
	}
	if err := call.Store(&names); err != nil {
		return false, fmt.Errorf("decode bus names: %w", err)
	}

	registered := make(map[string]struct{}, len(names))
	for _, n := range names {
		registered[n] = struct{}{}
	}
	for _, svc := range screensaverServices {
		if _, ok := registered[svc]; ok {
			return true, nil
		}
	}
	return false, nil
}

// Start connects to the session bus, subscribes to screensaver signals,
// queries the initial lock state, then runs the signal-consumer loop.
// Blocks until ctx is cancelled. Before returning it closes the dbus
// connection. Must only be called once.
func (d *LockDetector) Run(ctx context.Context) error {
	readyClosed := false
	defer func() {
		if !readyClosed {
			close(d.ready)
		}
	}()

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connect session bus: %w", err)
	}
	defer func() {
		conn.RemoveSignal(d.signals)
		_ = conn.Close()
	}()

	d.conn = conn
	d.signals = make(chan *dbus.Signal, 32)
	conn.Signal(d.signals)

	// Subscribe to ScreenSaver signals from common desktop environments.
	_ = conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface("org.gnome.ScreenSaver"))
	_ = conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface("org.freedesktop.ScreenSaver"))
	_ = conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface("org.freedesktop.login1.Session"))

	d.queryInitialState()

	close(d.ready)
	readyClosed = true

	for {
		select {
		case <-ctx.Done():
			return nil
		case signal := <-d.signals:
			if signal == nil {
				continue
			}
			d.handleSignal(signal)
		}
	}
}

// IsLocked returns true if the screen is currently locked. Returns false
// before Start has finished querying the initial state.
func (d *LockDetector) IsLocked() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isLocked
}

// IsScreenLocked implements condition.Evaluator interface.
func (d *LockDetector) IsScreenLocked() bool {
	return d.IsLocked()
}

// SetChangeCallback sets a callback to be called when lock state changes.
func (d *LockDetector) SetChangeCallback(cb func(bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onChange = cb
}

func (d *LockDetector) queryInitialState() {
	// Try GNOME/Mutter first
	obj := d.conn.Object("org.gnome.ScreenSaver", "/org/gnome/ScreenSaver")
	call := obj.Call("org.gnome.ScreenSaver.GetActive", 0)
	if call.Err == nil {
		var active bool
		if err := call.Store(&active); err == nil {
			d.setLocked(active)
			return
		}
	}

	// Try KDE/Plasma
	obj = d.conn.Object("org.freedesktop.ScreenSaver", "/ScreenSaver")
	call = obj.Call("org.freedesktop.ScreenSaver.GetActive", 0)
	if call.Err == nil {
		var active bool
		if err := call.Store(&active); err == nil {
			d.setLocked(active)
			return
		}
	}

	d.setLocked(false)
}

func (d *LockDetector) handleSignal(signal *dbus.Signal) {
	switch signal.Name {
	case "org.gnome.ScreenSaver.ActiveChanged":
		if len(signal.Body) >= 1 {
			if active, ok := signal.Body[0].(bool); ok {
				d.setLocked(active)
			}
		}
	case "org.freedesktop.ScreenSaver.ActiveChanged":
		if len(signal.Body) >= 1 {
			if active, ok := signal.Body[0].(bool); ok {
				d.setLocked(active)
			}
		}
	}
}

func (d *LockDetector) setLocked(locked bool) {
	d.mu.Lock()
	changed := d.isLocked != locked
	d.isLocked = locked
	cb := d.onChange
	d.mu.Unlock()

	if changed && cb != nil {
		cb(locked)
	}
}
