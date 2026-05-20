package p2p

import (
	"context"
	"fmt"
	"log/slog"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PubSub wraps GossipSub with channel-level encryption.
type PubSub struct {
	ps        *pubsub.PubSub
	key       []byte // derived from channel ID
	channelID string
}

// NewPubSub creates a GossipSub instance with a topic validator that rejects
// messages not encrypted with the channel key.
//
// The pubsub instance's lifetime is bound to context.Background() rather than
// the caller's ctx. This is intentional: pubsub must outlive the caller's
// shutdown context so that final messages (e.g. presence "leaving"
// announcements) can be published during graceful shutdown after the
// service-level ctx has already been cancelled. The pubsub is released when
// the host is closed.
func NewPubSub(_ context.Context, h host.Host, channelID string) (*PubSub, error) {
	key, err := DeriveKey(channelID)
	if err != nil {
		return nil, fmt.Errorf("derive pubsub key: %w", err)
	}

	ps, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		return nil, fmt.Errorf("create gossipsub: %w", err)
	}

	return &PubSub{
		ps:        ps,
		key:       key,
		channelID: channelID,
	}, nil
}

// TopicName returns the full topic name for a given suffix.
func (p *PubSub) TopicName(suffix string) string {
	return "notify-relay/" + ChannelHash(p.channelID) + "/" + suffix
}

// JoinTopic joins a topic and registers a validator that decrypts messages.
// Messages that fail decryption are rejected (pruning non-members from the mesh).
func (p *PubSub) JoinTopic(topicName string) (*Topic, error) {
	// Register validator that checks decryptability
	err := p.ps.RegisterTopicValidator(topicName, func(ctx context.Context, pid peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
		_, err := Decrypt(p.key, msg.Data)
		if err != nil {
			slog.Debug("rejected message: decryption failed", "from", pid, "topic", topicName)
			return pubsub.ValidationReject
		}
		return pubsub.ValidationAccept
	})
	if err != nil {
		return nil, fmt.Errorf("register topic validator: %w", err)
	}

	topic, err := p.ps.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("join topic: %w", err)
	}

	return &Topic{
		topic: topic,
		key:   p.key,
	}, nil
}

// Topic wraps a pubsub.Topic with encryption/decryption.
type Topic struct {
	topic *pubsub.Topic
	key   []byte
}

// Publish encrypts and publishes data to the topic.
func (t *Topic) Publish(ctx context.Context, data []byte) error {
	encrypted, err := Encrypt(t.key, data)
	if err != nil {
		return fmt.Errorf("encrypt message: %w", err)
	}
	return t.topic.Publish(ctx, encrypted)
}

// ListPeers returns the peer IDs subscribed to this topic.
func (t *Topic) ListPeers() []peer.ID {
	return t.topic.ListPeers()
}

// Subscribe returns a subscription that yields decrypted messages.
func (t *Topic) Subscribe() (*Subscription, error) {
	sub, err := t.topic.Subscribe()
	if err != nil {
		return nil, err
	}
	return &Subscription{sub: sub, key: t.key}, nil
}

// Close closes the topic.
func (t *Topic) Close() error {
	return t.topic.Close()
}

// Subscription wraps a pubsub.Subscription with automatic decryption.
type Subscription struct {
	sub *pubsub.Subscription
	key []byte
}

// Next returns the next decrypted message from the subscription.
func (s *Subscription) Next(ctx context.Context) ([]byte, peer.ID, error) {
	msg, err := s.sub.Next(ctx)
	if err != nil {
		return nil, "", err
	}

	plaintext, err := Decrypt(s.key, msg.Data)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt message: %w", err)
	}

	return plaintext, msg.ReceivedFrom, nil
}

// Cancel cancels the subscription.
func (s *Subscription) Cancel() {
	s.sub.Cancel()
}
