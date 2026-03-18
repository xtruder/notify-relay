package dbus

import (
	"context"
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

// Event represents a notification event
type Event struct {
	Name      string
	Reason    uint32
	ActionKey string
}

// Channel implements channel.Channel for dbus notifications
type Channel struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	signals chan *dbus.Signal

	mu      sync.Mutex
	waiters map[uint32]chan Event
}

// Compile-time interface check
var _ channel.Channel = (*Channel)(nil)
var _ channel.Closer = (*Channel)(nil)

// New creates a new dbus notification channel
func New() (*Channel, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}

	c := &Channel{
		conn:    conn,
		obj:     conn.Object(notificationInterface, notificationPath),
		signals: make(chan *dbus.Signal, 32),
		waiters: make(map[uint32]chan Event),
	}

	conn.Signal(c.signals)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(notificationPath),
		dbus.WithMatchInterface(notificationInterface),
	); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("subscribe notification signals: %w", err)
	}

	go c.consumeSignals()

	return c, nil
}

// Name returns "dbus"
func (c *Channel) Name() string {
	return "dbus"
}

// Close closes the dbus connection
func (c *Channel) Close() error {
	c.conn.RemoveSignal(c.signals)
	return c.conn.Close()
}

// Send sends a notification over dbus (implements channel.Channel)
func (c *Channel) Send(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
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

// CloseNotification closes a notification by ID (implements channel.Closer)
func (c *Channel) CloseNotification(ctx context.Context, id uint32) error {
	call := c.obj.CallWithContext(ctx, notificationInterface+".CloseNotification", 0, id)
	if call.Err != nil {
		return fmt.Errorf("close notification: %w", call.Err)
	}
	return nil
}

// Capabilities returns the capabilities supported by the dbus notification server
func (c *Channel) Capabilities(ctx context.Context) ([]string, error) {
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

// ServerInfo returns information about the dbus notification server
func (c *Channel) ServerInfo(ctx context.Context) (protocol.ServerInfoResponse, error) {
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

func (c *Channel) consumeSignals() {
	for signal := range c.signals {
		if signal == nil {
			continue
		}

		switch signal.Name {
		case notificationInterface + ".NotificationClosed":
			if len(signal.Body) != 2 {
				continue
			}
			id, ok1 := signal.Body[0].(uint32)
			reason, ok2 := signal.Body[1].(uint32)
			if ok1 && ok2 {
				c.dispatch(id, Event{Name: "closed", Reason: reason})
			}
		case notificationInterface + ".ActionInvoked":
			if len(signal.Body) != 2 {
				continue
			}
			id, ok1 := signal.Body[0].(uint32)
			actionKey, ok2 := signal.Body[1].(string)
			if ok1 && ok2 {
				c.dispatch(id, Event{Name: "action_invoked", ActionKey: actionKey})
			}
		}
	}
}

func (c *Channel) registerWaiter(id uint32) chan Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan Event, 1)
	c.waiters[id] = ch
	return ch
}

func (c *Channel) unregisterWaiter(id uint32, ch chan Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.waiters[id]
	if ok && current == ch {
		delete(c.waiters, id)
		close(ch)
	}
}

func (c *Channel) dispatch(id uint32, event Event) {
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
