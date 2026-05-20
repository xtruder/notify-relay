//go:build darwin

package platform

import (
	"context"

	"github.com/xtruder/notify-relay/internal/condition"
	"github.com/xtruder/notify-relay/internal/macos"
)

// LockDetector combines condition.Evaluator with runtime lifecycle methods.
type LockDetector interface {
	condition.Evaluator
	IsAvailable(ctx context.Context) (bool, error)
	Run(ctx context.Context) error
	SetChangeCallback(fn func(bool))
	IsLocked() bool
}

// NewLockDetector creates a new macOS lock detector.
func NewLockDetector() LockDetector {
	return macos.NewLockDetector()
}
