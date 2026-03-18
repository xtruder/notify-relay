package condition

import (
	"context"

	"github.com/xtruder/notify-relay/internal/protocol"
)

// ScreenLocked checks if the screen is currently locked
type ScreenLocked struct {
	evaluator Evaluator
}

// NewScreenLocked creates a new ScreenLocked condition
func NewScreenLocked(evaluator Evaluator) *ScreenLocked {
	return &ScreenLocked{evaluator: evaluator}
}

// Name returns "screen_locked"
func (s *ScreenLocked) Name() string {
	return "screen_locked"
}

// Evaluate returns true if the screen is locked
func (s *ScreenLocked) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
	return s.evaluator.IsScreenLocked()
}
