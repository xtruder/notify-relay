//go:build linux

package platform

import "github.com/xtruder/notify-relay/internal/dbus"

// DisplayChannelName returns the canonical name of the display channel on Linux.
func DisplayChannelName() string {
	return "dbus"
}

// NewNotifyChannel creates a new Linux dbus notification channel.
func NewNotifyChannel() DisplayChannel {
	return dbus.NewNotifyChannel()
}
