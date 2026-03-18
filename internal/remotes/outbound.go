package remotes

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

// OutboundWatcher watches for socket files and manages outbound connections
type OutboundWatcher struct {
	manager      *Manager
	watchPaths   []string // Specific socket files to watch
	watchPattern string   // Pattern to match
	hostname     string
	onConnect    func()
	onDisconnect func()
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewOutboundWatcher creates a watcher for outbound socket connections
func NewOutboundWatcher(manager *Manager, paths []string, pattern string, hostname string) *OutboundWatcher {
	return &OutboundWatcher{
		manager:      manager,
		watchPaths:   paths,
		watchPattern: pattern,
		hostname:     hostname,
	}
}

// SetCallbacks sets connection callbacks
func (ow *OutboundWatcher) SetCallbacks(onConnect, onDisconnect func()) {
	ow.onConnect = onConnect
	ow.onDisconnect = onDisconnect
}

// Start begins watching for socket files
func (ow *OutboundWatcher) Start(ctx context.Context) error {
	ow.ctx, ow.cancel = context.WithCancel(ctx)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ow.ctx.Done():
			return ow.ctx.Err()
		case <-ticker.C:
			ow.scanAndConnect()
		}
	}
}

// Stop stops watching
func (ow *OutboundWatcher) Stop() {
	if ow.cancel != nil {
		ow.cancel()
	}
}

func (ow *OutboundWatcher) scanAndConnect() {
	// Check specific paths
	for _, path := range ow.watchPaths {
		ow.tryConnect(path)
	}

	// Check pattern if specified
	if ow.watchPattern != "" {
		ow.scanPattern()
	}
}

func (ow *OutboundWatcher) tryConnect(socketPath string) {
	// Check if socket exists
	if _, err := os.Stat(socketPath); err != nil {
		// Socket doesn't exist
		if remote, exists := ow.manager.GetRemote(socketPath); exists {
			// Remote exists but socket gone, remove it
			slog.Info("socket removed, disconnecting remote", "socket", socketPath)
			ow.manager.RemoveRemote(remote.Hostname)
			if ow.onDisconnect != nil {
				ow.onDisconnect()
			}
		}
		return
	}

	// Socket exists, check if already connected
	hostname := ow.extractHostname(socketPath)
	if _, exists := ow.manager.GetRemote(hostname); exists {
		// Already connected
		return
	}

	// Try to connect
	slog.Info("found socket, connecting", "socket", socketPath)

	client := NewClient(ClientConfig{
		ServerAddr: socketPath,
		Hostname:   hostname,
	})

	client.SetCallbacks(
		func() {
			slog.Info("connected to remote", "hostname", hostname, "socket", socketPath)
			if ow.onConnect != nil {
				ow.onConnect()
			}
		},
		func() {
			slog.Info("disconnected from remote", "hostname", hostname)
			ow.manager.RemoveRemote(hostname)
			if ow.onDisconnect != nil {
				ow.onDisconnect()
			}
		},
		func(notif *notify_relayv1.ForwardedNotification) {
			// Handle forwarded notification
			slog.Info("received forwarded notification", "from", notif.SourceHostname, "summary", notif.Notification.Summary)
		},
	)

	// Connect in background
	go func() {
		if err := client.Connect(ow.ctx); err != nil {
			slog.Error("failed to connect", "socket", socketPath, "error", err)
		}
	}()
}

func (ow *OutboundWatcher) scanPattern() {
	dir := filepath.Dir(ow.watchPattern)
	pattern := filepath.Base(ow.watchPattern)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		matched, _ := filepath.Match(pattern, name)
		if matched {
			socketPath := filepath.Join(dir, name)
			ow.tryConnect(socketPath)
		}
	}
}

func (ow *OutboundWatcher) extractHostname(socketPath string) string {
	base := filepath.Base(socketPath)

	// Try to extract hostname from pattern like "notify-relay-laptop.sock"
	if strings.HasPrefix(base, "notify-relay-") {
		hostname := strings.TrimPrefix(base, "notify-relay-")
		hostname = strings.TrimSuffix(hostname, ".sock")
		if hostname != "" {
			return hostname
		}
	}

	// Fallback to socket path
	return base
}
