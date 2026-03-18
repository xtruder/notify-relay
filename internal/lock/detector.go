package lock

import (
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Detector monitors screen lock state via dbus
type Detector struct {
	conn     *dbus.Conn
	signals  chan *dbus.Signal
	mu       sync.RWMutex
	isLocked bool
	onChange func(bool)
	stopCh   chan struct{}
}

// New creates a new screen lock detector
func New() (*Detector, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}

	d := &Detector{
		conn:     conn,
		signals:  make(chan *dbus.Signal, 32),
		stopCh:   make(chan struct{}),
		isLocked: false,
	}

	conn.Signal(d.signals)

	// Try to subscribe to ScreenSaver signals from different desktop environments
	// GNOME/Mutter
	_ = conn.AddMatchSignal(
		dbus.WithMatchInterface("org.gnome.ScreenSaver"),
	)
	// KDE/Plasma
	_ = conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.ScreenSaver"),
	)
	// Generic/freedesktop
	_ = conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.login1.Session"),
	)

	// Query initial lock state
	d.queryInitialState()

	// Start listening for signals
	go d.consumeSignals()

	return d, nil
}

// IsLocked returns true if the screen is currently locked
func (d *Detector) IsLocked() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isLocked
}

// IsScreenLocked implements condition.Evaluator interface
func (d *Detector) IsScreenLocked() bool {
	return d.IsLocked()
}

// SetChangeCallback sets a callback to be called when lock state changes
func (d *Detector) SetChangeCallback(cb func(bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onChange = cb
}

// Close stops the detector and closes the dbus connection
func (d *Detector) Close() error {
	close(d.stopCh)
	d.conn.RemoveSignal(d.signals)
	return d.conn.Close()
}

func (d *Detector) queryInitialState() {
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

	// Default to unlocked if we can't determine state
	d.setLocked(false)
}

func (d *Detector) consumeSignals() {
	for {
		select {
		case <-d.stopCh:
			return
		case signal := <-d.signals:
			if signal == nil {
				continue
			}
			d.handleSignal(signal)
		}
	}
}

func (d *Detector) handleSignal(signal *dbus.Signal) {
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

func (d *Detector) setLocked(locked bool) {
	d.mu.Lock()
	changed := d.isLocked != locked
	d.isLocked = locked
	cb := d.onChange
	d.mu.Unlock()

	if changed && cb != nil {
		cb(locked)
	}
}
