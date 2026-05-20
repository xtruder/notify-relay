package p2p

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protojson"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

const (
	// PresenceInterval is how often to broadcast presence.
	PresenceInterval = 10 * time.Second

	// StaleTimeout is how long before a peer is considered stale.
	StaleTimeout = 30 * time.Second
)

// PresenceService handles presence pub/sub communication.
// It runs an internal heartbeat loop that re-broadcasts the last known
// isPresent state, and periodically cleans up stale peers.
//
// Lifecycle:
//   - NewPresenceService constructs the service; no work is done yet.
//   - Run(ctx) joins the topic, runs background loops, and blocks until
//     ctx is cancelled. Before returning it broadcasts a "leaving"
//     presence message and releases owned resources.
type PresenceService struct {
	host              host.Host
	pubsub            *PubSub
	table             *PeerTable
	hostname          string
	priority          int
	heartbeatInterval time.Duration
	staleTimeout      time.Duration
	isPresent         atomic.Bool

	// topic is set during Start before ready is closed, then read-only.
	topic *Topic

	// ready is closed by Start once topic is joined. SetPresent waits on it.
	ready chan struct{}
}

// PresenceConfig configures the presence service.
type PresenceConfig struct {
	Hostname string
	Priority int
	// HeartbeatInterval overrides the default PresenceInterval.
	// Zero means use the default.
	HeartbeatInterval time.Duration
	// StaleTimeout overrides the default StaleTimeout.
	// Zero means use the default.
	StaleTimeout time.Duration
}

// NewPresenceService constructs the presence service. No goroutines are
// started and no topics are joined until Start is called.
func NewPresenceService(h host.Host, ps *PubSub, table *PeerTable, cfg PresenceConfig) *PresenceService {
	heartbeat := cfg.HeartbeatInterval
	if heartbeat == 0 {
		heartbeat = PresenceInterval
	}
	stale := cfg.StaleTimeout
	if stale == 0 {
		stale = StaleTimeout
	}

	return &PresenceService{
		host:              h,
		pubsub:            ps,
		table:             table,
		hostname:          cfg.Hostname,
		priority:          cfg.Priority,
		heartbeatInterval: heartbeat,
		staleTimeout:      stale,
		ready:             make(chan struct{}),
	}
}

// Start joins the presence topic and runs the subscription handler and
// heartbeat loop. Blocks until ctx is cancelled. Before returning it
// broadcasts a "leaving" message and closes the topic. Must only be
// called once. Returns nil on clean shutdown.
func (s *PresenceService) Run(ctx context.Context) error {
	readyClosed := false
	defer func() {
		if !readyClosed {
			close(s.ready)
		}
	}()

	topicName := s.pubsub.TopicName("presence")
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
	g.Go(func() error {
		s.handleSubscription(gctx, sub)
		return nil
	})
	g.Go(func() error {
		s.heartbeatLoop(gctx)
		return nil
	})

	slog.Info("presence service started", "topic", topicName)

	_ = g.Wait()

	// Announce departure to peers so they can drop us immediately instead
	// of waiting for the stale timeout. Use a fresh, short context since
	// runCtx is already cancelled.
	leavingCtx, leavingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s.publishLeaving(leavingCtx); err != nil {
		slog.Warn("presence leaving announcement failed", "error", err)
	} else {
		slog.Info("presence leaving announcement sent", "peers", len(s.topic.ListPeers()))
		// pubsub.Publish is asynchronous: the message is queued and then
		// flushed to mesh peers. Pause briefly so the flush happens before
		// the host closes its connections.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-leavingCtx.Done():
		}
	}
	leavingCancel()

	_ = topic.Close()
	return nil
}

// publishLeaving broadcasts a final presence update marked as Leaving so
// peers can drop the local node without waiting for the stale timeout.
func (s *PresenceService) publishLeaving(ctx context.Context) error {
	msg := &notify_relayv1.PresenceMessage{
		PeerId:    s.host.ID().String(),
		Hostname:  s.hostname,
		IsPresent: false,
		Priority:  int32(s.priority),
		Leaving:   true,
		Timestamp: time.Now().UnixMilli(),
	}
	data, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	return s.topic.Publish(ctx, data)
}

// SetPresent updates the cached isPresent state and immediately broadcasts
// a presence update. Blocks until Start has joined the presence topic, or
// until ctx is cancelled.
func (s *PresenceService) SetPresent(ctx context.Context, isPresent bool) error {
	s.isPresent.Store(isPresent)
	select {
	case <-s.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.publish(ctx, isPresent)
}

func (s *PresenceService) publish(ctx context.Context, isPresent bool) error {
	s.table.SetLocal(PeerInfo{
		PeerID:    s.host.ID(),
		Hostname:  s.hostname,
		IsPresent: isPresent,
		Priority:  s.priority,
	})

	msg := &notify_relayv1.PresenceMessage{
		PeerId:    s.host.ID().String(),
		Hostname:  s.hostname,
		IsPresent: isPresent,
		Priority:  int32(s.priority),
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}

	if s.topic == nil {
		return errors.New("presence service not started")
	}
	if err := s.topic.Publish(ctx, data); err != nil {
		return err
	}

	slog.Debug("presence published", "peers", len(s.topic.ListPeers()), "isPresent", isPresent)
	return nil
}

func (s *PresenceService) heartbeatLoop(ctx context.Context) {
	heartbeat := time.NewTicker(s.heartbeatInterval)
	cleanup := time.NewTicker(s.staleTimeout / 2)
	defer heartbeat.Stop()
	defer cleanup.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := s.publish(ctx, s.isPresent.Load()); err != nil {
				slog.Debug("presence heartbeat failed", "error", err)
			}
		case <-cleanup.C:
			s.table.CleanupStale(s.staleTimeout)
		}
	}
}

func (s *PresenceService) handleSubscription(ctx context.Context, sub *Subscription) {
	defer sub.Cancel()

	for {
		data, from, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("presence subscription error", "error", err)
			continue
		}

		msg := &notify_relayv1.PresenceMessage{}
		if err := protojson.Unmarshal(data, msg); err != nil {
			slog.Debug("invalid presence message", "from", from, "error", err)
			continue
		}

		peerID, err := peer.Decode(msg.PeerId)
		if err != nil {
			continue
		}
		if peerID == s.host.ID() {
			continue
		}

		if msg.Leaving {
			slog.Info("peer announced departure", "peer", peerID, "hostname", msg.Hostname)
			s.table.Remove(peerID)
			continue
		}

		s.table.Update(PeerInfo{
			PeerID:    peerID,
			Hostname:  msg.Hostname,
			IsPresent: msg.IsPresent,
			Priority:  int(msg.Priority),
		})
	}
}
