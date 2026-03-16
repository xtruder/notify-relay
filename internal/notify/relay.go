package notify

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/xtruder/notify-relay/internal/protocol"
)

const (
	notificationPath      = dbus.ObjectPath("/org/freedesktop/Notifications")
	notificationInterface = "org.freedesktop.Notifications"
)

type Event struct {
	Name      string
	Reason    uint32
	ActionKey string
}

type Relay struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	signals chan *dbus.Signal

	mu      sync.Mutex
	waiters map[uint32]chan Event
}

func New() (*Relay, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}

	r := &Relay{
		conn:    conn,
		obj:     conn.Object(notificationInterface, notificationPath),
		signals: make(chan *dbus.Signal, 32),
		waiters: make(map[uint32]chan Event),
	}

	conn.Signal(r.signals)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(notificationPath),
		dbus.WithMatchInterface(notificationInterface),
	); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("subscribe notification signals: %w", err)
	}

	go r.consumeSignals()

	return r, nil
}

func (r *Relay) Close() error {
	r.conn.RemoveSignal(r.signals)
	return r.conn.Close()
}

func (r *Relay) Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	hints, err := buildHints(req.Hints)
	if err != nil {
		return protocol.NotifyResponse{}, err
	}

	call := r.obj.CallWithContext(ctx, notificationInterface+".Notify", 0,
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

	ch := r.registerWaiter(id)
	defer r.unregisterWaiter(id, ch)

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

func (r *Relay) CloseNotification(ctx context.Context, id uint32) error {
	call := r.obj.CallWithContext(ctx, notificationInterface+".CloseNotification", 0, id)
	if call.Err != nil {
		return fmt.Errorf("close notification: %w", call.Err)
	}
	return nil
}

func (r *Relay) Capabilities(ctx context.Context) ([]string, error) {
	call := r.obj.CallWithContext(ctx, notificationInterface+".GetCapabilities", 0)
	if call.Err != nil {
		return nil, fmt.Errorf("get capabilities: %w", call.Err)
	}
	var capabilities []string
	if err := call.Store(&capabilities); err != nil {
		return nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return capabilities, nil
}

func (r *Relay) ServerInformation(ctx context.Context) (protocol.ServerInfoResponse, error) {
	call := r.obj.CallWithContext(ctx, notificationInterface+".GetServerInformation", 0)
	if call.Err != nil {
		return protocol.ServerInfoResponse{}, fmt.Errorf("get server information: %w", call.Err)
	}
	var info protocol.ServerInfoResponse
	if err := call.Store(&info.Name, &info.Vendor, &info.Version, &info.Spec); err != nil {
		return protocol.ServerInfoResponse{}, fmt.Errorf("decode server information: %w", err)
	}
	return info, nil
}

func (r *Relay) consumeSignals() {
	for signal := range r.signals {
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
				r.dispatch(id, Event{Name: "closed", Reason: reason})
			}
		case notificationInterface + ".ActionInvoked":
			if len(signal.Body) != 2 {
				continue
			}
			id, ok1 := signal.Body[0].(uint32)
			actionKey, ok2 := signal.Body[1].(string)
			if ok1 && ok2 {
				r.dispatch(id, Event{Name: "action_invoked", ActionKey: actionKey})
			}
		}
	}
}

func (r *Relay) registerWaiter(id uint32) chan Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan Event, 1)
	r.waiters[id] = ch
	return ch
}

func (r *Relay) unregisterWaiter(id uint32, ch chan Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.waiters[id]
	if ok && current == ch {
		delete(r.waiters, id)
		close(ch)
	}
}

func (r *Relay) dispatch(id uint32, event Event) {
	r.mu.Lock()
	ch, ok := r.waiters[id]
	if ok {
		delete(r.waiters, id)
	}
	r.mu.Unlock()
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
