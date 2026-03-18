package condition

import (
	"context"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// Always is a condition that is always true
type Always struct{}

// Name returns "always"
func (a Always) Name() string {
	return "always"
}

// Evaluate always returns true
func (a Always) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return true
}
