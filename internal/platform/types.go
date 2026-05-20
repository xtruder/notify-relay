package platform

import (
	"context"

	"github.com/xtruder/notify-relay/internal/channel"
)

// DisplayChannel is a channel.Channel with lifecycle methods for display
// notification services.
type DisplayChannel interface {
	channel.Channel
	IsAvailable(ctx context.Context) (bool, error)
	Run(ctx context.Context) error
}
