package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/errgroup"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// Message types for the notification topic.
const (
	MsgTypeNotification = "notification"
	MsgTypeAction       = "action"
)

// NotifyMessage is the envelope published on the notifications topic.
type NotifyMessage struct {
	Type         string `json:"type"`
	MsgID        string `json:"msg_id"`
	SenderPeerID string `json:"sender_peer_id"`

	// For MsgTypeNotification
	Notification *protocol.NotifyRequest `json:"notification,omitempty"`

	// For MsgTypeAction
	ActionKey string `json:"action_key,omitempty"`
	Event     string `json:"event,omitempty"`
	Reason    uint32 `json:"reason,omitempty"`
}

// NotifyHandler is called when a notification is received from the p2p network.
// The handler should evaluate routes and decide locally whether to act.
type NotifyHandler func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error)

// NotifyService handles notification pub/sub.
//
// Lifecycle (mirrors net/http.Server):
//   - NewNotifyService constructs the service.
//   - Start blocks until Shutdown is called or an unrecoverable error occurs.
//   - Shutdown signals Start to return and waits for the subscription loop
//     and in-flight notification handlers (bounded by ctx).
type NotifyService struct {
	host   host.Host
	pubsub *PubSub

	handler NotifyHandler

	pending   map[string]*pendingNotification
	pendingMu sync.Mutex

	// topic is set during Start before ready is closed, then read-only.
	topic *Topic

	// ready is closed once topic is joined.
	ready chan struct{}
}

type pendingNotification struct {
	actionCh chan protocol.NotifyResponse
}

// NotifyServiceConfig configures the notification service.
type NotifyServiceConfig struct {
	// Handler is called when a notification is received from the network.
	// The handler evaluates local conditions/routes to decide whether to act.
	Handler NotifyHandler
}

// NewNotifyService constructs the notification service. No goroutines are
// started and no topics are joined until Start is called.
func NewNotifyService(h host.Host, ps *PubSub, cfg NotifyServiceConfig) *NotifyService {
	return &NotifyService{
		host:    h,
		pubsub:  ps,
		handler: cfg.Handler,
		pending: make(map[string]*pendingNotification),
		ready:   make(chan struct{}),
	}
}

// SetHandler sets the notification handler. Must be called before Start;
// it is unsafe for concurrent use with a running service.
func (s *NotifyService) SetHandler(h NotifyHandler) {
	s.handler = h
}

// Start joins the notifications topic and runs the subscription loop.
// Blocks until ctx is cancelled, then drains any in-flight notification
// handlers before returning. Must only be called once.
func (s *NotifyService) Run(ctx context.Context) error {
	readyClosed := false
	defer func() {
		if !readyClosed {
			close(s.ready)
		}
	}()

	topicName := s.pubsub.TopicName("notifications")
	topic, err := s.pubsub.JoinTopic(topicName)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		_ = topic.Close()
		return err
	}
	s.topic = topic
	close(s.ready)
	readyClosed = true

	g, gctx := errgroup.WithContext(ctx)
	// Subscription loop: spawns one handler goroutine per incoming
	// notification, tracked via g so Start waits for in-flight handlers.
	g.Go(func() error {
		s.handleSubscription(gctx, sub, g)
		return nil
	})

	slog.Info("notification service started", "topic", topicName)

	err = g.Wait()
	_ = topic.Close()
	return err
}

// Broadcast publishes a notification to the p2p network without waiting for
// replies. Blocks until Start has joined the topic, or ctx is cancelled.
func (s *NotifyService) Broadcast(ctx context.Context, req protocol.NotifyRequest) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return
	}
	msgID := fmt.Sprintf("%s-%d", s.host.ID().String(), time.Now().UnixNano())
	s.publishMsg(ctx, msgID, req)
}

// Publish publishes a notification to the p2p network and waits for an action
// reply from the peer that handled it.
// Returns (response, true) if an action reply was received within the timeout,
// or (zero, false) if the timeout elapsed or Start has not joined the topic yet.
func (s *NotifyService) Publish(ctx context.Context, req protocol.NotifyRequest, timeout time.Duration) (protocol.NotifyResponse, bool) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return protocol.NotifyResponse{}, false
	}
	msgID := fmt.Sprintf("%s-%d", s.host.ID().String(), time.Now().UnixNano())

	pn := &pendingNotification{
		actionCh: make(chan protocol.NotifyResponse, 1),
	}
	s.pendingMu.Lock()
	s.pending[msgID] = pn
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, msgID)
		s.pendingMu.Unlock()
	}()

	if !s.publishMsg(ctx, msgID, req) {
		return protocol.NotifyResponse{}, false
	}

	if timeout == 0 {
		timeout = 30 * time.Second
	}

	select {
	case resp := <-pn.actionCh:
		return resp, true
	case <-time.After(timeout):
		return protocol.NotifyResponse{}, false
	case <-ctx.Done():
		return protocol.NotifyResponse{}, false
	}
}

func (s *NotifyService) publishMsg(ctx context.Context, msgID string, req protocol.NotifyRequest) bool {
	msg := NotifyMessage{
		Type:         MsgTypeNotification,
		MsgID:        msgID,
		SenderPeerID: s.host.ID().String(),
		Notification: &req,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("marshal notification failed", "error", err)
		return false
	}

	if s.topic == nil {
		slog.Error("notify service not started")
		return false
	}

	peers := s.topic.ListPeers()
	if len(peers) == 0 {
		slog.Warn("no peers subscribed to notification topic, message may not be delivered")
	} else {
		slog.Debug("publishing notification", "peers", len(peers))
	}

	if err := s.topic.Publish(ctx, data); err != nil {
		slog.Error("publish notification failed", "error", err)
		return false
	}

	return true
}

func (s *NotifyService) handleSubscription(ctx context.Context, sub *Subscription, g *errgroup.Group) {
	defer sub.Cancel()

	for {
		data, _, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("notification subscription error", "error", err)
			continue
		}

		var msg NotifyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Debug("invalid notification message", "error", err)
			continue
		}

		switch msg.Type {
		case MsgTypeNotification:
			// Handle in a separate goroutine so blocking handlers
			// (e.g. dbus.Send with Wait) don't stall the subscription loop.
			// Tracked in g so Start waits for in-flight handlers before
			// returning.
			g.Go(func() error {
				s.handleNotification(ctx, msg)
				return nil
			})
		case MsgTypeAction:
			s.handleAction(msg)
		}
	}
}

func (s *NotifyService) handleNotification(ctx context.Context, msg NotifyMessage) {
	senderID, err := peer.Decode(msg.SenderPeerID)
	if err != nil {
		return
	}
	if senderID == s.host.ID() {
		return
	}

	if msg.Notification == nil || s.handler == nil {
		return
	}

	slog.Debug("received notification from peer", "sender", msg.SenderPeerID, "summary", msg.Notification.Summary)

	resp, err := s.handler(ctx, *msg.Notification)
	if err != nil {
		slog.Debug("notification handler returned error", "error", err)
		return
	}

	if resp.Event != "" {
		action := NotifyMessage{
			Type:         MsgTypeAction,
			MsgID:        msg.MsgID,
			SenderPeerID: msg.SenderPeerID,
			ActionKey:    resp.ActionKey,
			Event:        resp.Event,
			Reason:       resp.Reason,
		}
		actionData, _ := json.Marshal(action)
		if err := s.topic.Publish(ctx, actionData); err != nil {
			slog.Debug("publish action reply failed", "error", err)
		}
	}
}

func (s *NotifyService) handleAction(msg NotifyMessage) {
	s.pendingMu.Lock()
	pn, ok := s.pending[msg.MsgID]
	s.pendingMu.Unlock()

	if ok {
		resp := protocol.NotifyResponse{
			Event:     msg.Event,
			ActionKey: msg.ActionKey,
			Reason:    msg.Reason,
		}
		select {
		case pn.actionCh <- resp:
		default:
		}
	}
}
