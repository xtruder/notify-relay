package condition

import (
	"context"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// Condition evaluates whether a notification should be sent through a specific channel
type Condition interface {
	// Name returns the unique name of this condition
	Name() string

	// Evaluate returns true if this condition is met for the given notification request
	Evaluate(ctx context.Context, req protocol.NotifyRequest) bool
}

// Evaluator provides methods to check various runtime states
type Evaluator interface {
	// IsScreenLocked returns true if the screen is currently locked
	IsScreenLocked() bool
}
