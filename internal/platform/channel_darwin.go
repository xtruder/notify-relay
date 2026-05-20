//go:build darwin

package platform

import "github.com/xtruder/notify-relay/internal/macos"

// DisplayChannelName returns the canonical name of the display channel on macOS.
func DisplayChannelName() string {
	return "macos"
}

// NewNotifyChannel creates a new macOS notification channel.
func NewNotifyChannel() DisplayChannel {
	return macos.NewNotifyChannel()
}
