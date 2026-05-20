//go:build darwin

package macos

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/protocol"
)

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

void sendMacOSNotification(const char *title, const char *body) {
	@autoreleasepool {
		NSUserNotification *notification = [[NSUserNotification alloc] init];
		notification.title = [NSString stringWithUTF8String:title];
		notification.informativeText = [NSString stringWithUTF8String:body];
		[[NSUserNotificationCenter defaultUserNotificationCenter] deliverNotification:notification];
	}
}
*/
import "C"

// NotifyChannel implements channel.Channel using macOS NSUserNotificationCenter.
type NotifyChannel struct {
	mu     sync.Mutex
	closed bool
}

// NewNotifyChannel creates a new macOS notification channel.
func NewNotifyChannel() *NotifyChannel {
	return &NotifyChannel{}
}

// Name returns the channel name.
func (c *NotifyChannel) Name() string {
	return "macos"
}

// Run blocks until the context is cancelled. For macOS this is a no-op
// because NSUserNotificationCenter is stateless.
func (c *NotifyChannel) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Send delivers a notification via NSUserNotificationCenter.
func (c *NotifyChannel) Send(_ context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return protocol.NotifyResponse{}, channel.ErrUnavailable
	}

	title := C.CString(req.Summary)
	defer C.free(unsafe.Pointer(title))
	body := C.CString(req.Body)
	defer C.free(unsafe.Pointer(body))

	C.sendMacOSNotification(title, body)
	return protocol.NotifyResponse{}, nil
}

// Close marks the channel as closed.
func (c *NotifyChannel) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// Capabilities returns supported capabilities.
func (c *NotifyChannel) Capabilities(_ context.Context) ([]string, error) {
	return []string{"body"}, nil
}

// ServerInfo returns information about the notification server.
func (c *NotifyChannel) ServerInfo(_ context.Context) (protocol.ServerInfoResponse, error) {
	return protocol.ServerInfoResponse{
		Name:    "macos",
		Vendor:  "Apple",
		Version: "1.0",
		Spec:    "1.0",
	}, nil
}

// IsAvailable always returns true on macOS when running in a GUI session.
func (c *NotifyChannel) IsAvailable(_ context.Context) (bool, error) {
	return true, nil
}
