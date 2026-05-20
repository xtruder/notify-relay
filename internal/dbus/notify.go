//go:build linux

package dbus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/protocol"
)

const (
	notificationPath      = dbus.ObjectPath("/org/freedesktop/Notifications")
	notificationInterface = "org.freedesktop.Notifications"
)

// ErrNotifyChannelNotAvailable is returned when the freedesktop notification
// service is not available on the session bus (e.g. headless server).
var ErrNotifyChannelNotAvailable = fmt.Errorf("dbus: %w", channel.ErrUnavailable)

// Event represents a notification event delivered by the dbus server.
type Event struct {
	Name      string
	Reason    uint32
	ActionKey string
}

// NotifyChannel implements channel.Channel for dbus notifications.
//
// Lifecycle:
//   - NewNotifyChannel constructs the channel; no I/O is performed.
//   - Run(ctx) connects to the session bus, probes for the notification
//     service, subscribes to signals, then blocks consuming signals until
//     ctx is cancelled. Before returning it closes the dbus connection.
//   - Close is a no-op (satisfies channel.Channel); lifecycle is ctx-driven.
//
// IsAvailable can be called independently to probe service availability.
// Send/CloseNotification/Capabilities/ServerInfo block until Start has
// finished setup (ready), or ctx is cancelled.
type NotifyChannel struct {
	mu      sync.Mutex
	waiters map[uint32]chan Event

	// conn, obj and signals are set by Start before ready is closed.
	conn    *dbus.Conn
	obj     dbus.BusObject
	signals chan *dbus.Signal

	ready chan struct{}
	// startErr is set by Start before ready is closed when startup fails.
	startErr error
}

// Compile-time interface checks
var (
	_ channel.Channel = (*NotifyChannel)(nil)
	_ channel.Closer  = (*NotifyChannel)(nil)
)

// NewNotifyChannel constructs the channel. No dbus connection is opened
// until Start.
func NewNotifyChannel() *NotifyChannel {
	return &NotifyChannel{
		waiters: make(map[uint32]chan Event),
		ready:   make(chan struct{}),
	}
}

// IsAvailable probes whether the freedesktop notification service is
// reachable on the session bus. It opens a short-lived connection
// independent of Start; safe to call before Start. Returns (true, nil) if
// available, (false, nil) if the bus or service is not available.
func (c *NotifyChannel) IsAvailable(_ context.Context) (bool, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, nil
	}
	defer conn.Close()

	obj := conn.Object(notificationInterface, notificationPath)
	call := obj.Call(notificationInterface+".GetServerInformation", 0)
	if call.Err != nil {
		return false, nil
	}
	return true, nil
}

// Name returns "dbus".
func (c *NotifyChannel) Name() string {
	return "dbus"
}

// Start connects to the session bus, subscribes to notification signals,
// then runs the signal-consumer loop. Blocks until ctx is cancelled.
// Before returning it closes the dbus connection. Returns nil on clean
// exit, or ErrNotifyChannelNotAvailable / a wrapped error if startup fails.
// Must only be called once.
func (c *NotifyChannel) Run(ctx context.Context) error {
	readyClosed := false
	defer func() {
		if !readyClosed {
			close(c.ready)
		}
	}()

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		c.startErr = fmt.Errorf("connect session bus: %w", err)
		return c.startErr
	}
	defer func() {
		conn.RemoveSignal(c.signals)
		_ = conn.Close()
	}()

	obj := conn.Object(notificationInterface, notificationPath)
	call := obj.Call(notificationInterface+".GetServerInformation", 0)
	if call.Err != nil {
		c.startErr = ErrNotifyChannelNotAvailable
		return c.startErr
	}

	c.conn = conn
	c.obj = obj
	c.signals = make(chan *dbus.Signal, 32)
	conn.Signal(c.signals)

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(notificationPath),
		dbus.WithMatchInterface(notificationInterface),
	); err != nil {
		c.startErr = fmt.Errorf("subscribe notification signals: %w", err)
		return c.startErr
	}

	close(c.ready)
	readyClosed = true

	for {
		select {
		case <-ctx.Done():
			return nil
		case signal, ok := <-c.signals:
			if !ok || signal == nil {
				continue
			}
			c.handleSignal(signal)
		}
	}
}

// Close is a no-op; the dbus connection is released when Start returns.
// Present to satisfy channel.Channel.
func (c *NotifyChannel) Close() error { return nil }

// waitReady blocks until Start has finished setup or ctx is cancelled.
// Returns the startup error if Start failed before becoming ready.
func (c *NotifyChannel) waitReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return c.startErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send sends a notification over dbus (implements channel.Channel).
func (c *NotifyChannel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	if err := c.waitReady(ctx); err != nil {
		return protocol.NotifyResponse{}, err
	}

	hints, err := buildHints(req.Hints)
	if err != nil {
		return protocol.NotifyResponse{}, err
	}

	call := c.obj.CallWithContext(ctx, notificationInterface+".Notify", 0,
		req.AppName,
		req.ReplacesID,
		req.AppIcon,
		req.Summary,
		req.Body,
		req.Actions,
		hints,
		req.ExpireTimeout,
	)
	if call.Err != nil {
		if isServiceUnavailable(call.Err) {
			return protocol.NotifyResponse{}, fmt.Errorf("dbus: %w", channel.ErrUnavailable)
		}
		return protocol.NotifyResponse{}, fmt.Errorf("notify over dbus: %w", call.Err)
	}

	var id uint32
	if err := call.Store(&id); err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("decode notification id: %w", err)
	}

	resp := protocol.NotifyResponse{ID: id}
	if !req.Wait {
		return resp, nil
	}

	ch := c.registerWaiter(id)
	defer c.unregisterWaiter(id, ch)

	select {
	case event := <-ch:
		resp.Event = event.Name
		resp.Reason = event.Reason
		resp.ActionKey = event.ActionKey
		return resp, nil
	case <-ctx.Done():
		return protocol.NotifyResponse{}, ctx.Err()
	}
}

// CloseNotification closes a notification by ID (implements channel.Closer).
func (c *NotifyChannel) CloseNotification(ctx context.Context, id uint32) error {
	if err := c.waitReady(ctx); err != nil {
		return err
	}
	call := c.obj.CallWithContext(ctx, notificationInterface+".CloseNotification", 0, id)
	if call.Err != nil {
		return fmt.Errorf("close notification: %w", call.Err)
	}
	return nil
}

// Capabilities returns the capabilities supported by the dbus notification server.
func (c *NotifyChannel) Capabilities(ctx context.Context) ([]string, error) {
	if err := c.waitReady(ctx); err != nil {
		return nil, err
	}
	call := c.obj.CallWithContext(ctx, notificationInterface+".GetCapabilities", 0)
	if call.Err != nil {
		return nil, fmt.Errorf("get capabilities: %w", call.Err)
	}
	var capabilities []string
	if err := call.Store(&capabilities); err != nil {
		return nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return capabilities, nil
}

// ServerInfo returns information about the dbus notification server.
func (c *NotifyChannel) ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error) {
	if err := c.waitReady(ctx); err != nil {
		return protocol.ServerInfoResponse{}, err
	}
	call := c.obj.CallWithContext(ctx, notificationInterface+".GetServerInformation", 0)
	if call.Err != nil {
		return protocol.ServerInfoResponse{}, fmt.Errorf("get server information: %w", call.Err)
	}
	var info protocol.ServerInfoResponse
	if err := call.Store(&info.Name, &info.Vendor, &info.Version, &info.Spec); err != nil {
		return protocol.ServerInfoResponse{}, fmt.Errorf("decode server information: %w", err)
	}
	return info, nil
}

func (c *NotifyChannel) handleSignal(signal *dbus.Signal) {
	switch signal.Name {
	case notificationInterface + ".NotificationClosed":
		if len(signal.Body) != 2 {
			return
		}
		id, ok1 := signal.Body[0].(uint32)
		reason, ok2 := signal.Body[1].(uint32)
		if ok1 && ok2 {
			c.dispatch(id, Event{Name: "closed", Reason: reason})
		}
	case notificationInterface + ".ActionInvoked":
		if len(signal.Body) != 2 {
			return
		}
		id, ok1 := signal.Body[0].(uint32)
		actionKey, ok2 := signal.Body[1].(string)
		if ok1 && ok2 {
			c.dispatch(id, Event{Name: "action_invoked", ActionKey: actionKey})
		}
	}
}

func (c *NotifyChannel) registerWaiter(id uint32) chan Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan Event, 1)
	c.waiters[id] = ch
	return ch
}

func (c *NotifyChannel) unregisterWaiter(id uint32, ch chan Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.waiters[id]
	if ok && current == ch {
		delete(c.waiters, id)
		close(ch)
	}
}

func (c *NotifyChannel) dispatch(id uint32, event Event) {
	c.mu.Lock()
	ch, ok := c.waiters[id]
	if ok {
		delete(c.waiters, id)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	ch <- event
	close(ch)
}

func buildHints(hints []protocol.Hint) (map[string]dbus.Variant, error) {
	result := make(map[string]dbus.Variant, len(hints))
	for _, hint := range hints {
		value, err := parseHintValue(hint.Type, hint.Value)
		if err != nil {
			return nil, fmt.Errorf("parse hint %q: %w", hint.Name, err)
		}
		result[hint.Name] = dbus.MakeVariant(value)
	}
	return result, nil
}

func parseHintValue(kind, raw string) (any, error) {
	switch strings.ToLower(kind) {
	case "string":
		return raw, nil
	case "int", "int32":
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return int32(v), nil
	case "uint", "uint32":
		v, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return uint32(v), nil
	case "int64":
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		return v, nil
	case "uint64":
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		return v, nil
	case "double", "float64":
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		return v, nil
	case "byte", "uchar":
		v, err := strconv.ParseUint(raw, 10, 8)
		if err != nil {
			return nil, err
		}
		return byte(v), nil
	case "bool", "boolean":
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported hint type %q", kind)
	}
}

// isServiceUnavailable checks if a dbus call error indicates the notification
// service is not running.
func isServiceUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.NoReply",
			"org.freedesktop.DBus.Error.Disconnected":
			return true
		}
	}
	s := err.Error()
	return strings.Contains(s, "was not provided by any .service files") ||
		strings.Contains(s, "org.freedesktop.Notifications") && strings.Contains(s, "not provided")
}
