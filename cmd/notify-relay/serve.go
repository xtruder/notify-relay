package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/config"
	"github.com/xtruder/notify-relay/internal/ntfy"
	"github.com/xtruder/notify-relay/internal/p2p"
	"github.com/xtruder/notify-relay/internal/platform"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the relay daemon",
		Long: `Start the notification relay daemon.

Joins a P2P notification channel, listens for notifications from peers,
and exposes a local gRPC API for notify-send.`,
		RunE:          runServe,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	f := cmd.Flags()
	f.String("listen", "", "TCP listen address for local gRPC API")
	f.String("unix", "", "Unix socket path for local gRPC API")
	f.String("token", "", "Bearer token for local gRPC API authentication")
	f.String("token-file", "", "File containing bearer token")
	f.String("config", "", "Configuration file (TOML)")
	f.String("ntfy-topic", "", "ntfy.sh topic for phone notifications when screen is locked")
	f.String("channel", "", "P2P channel ID (shared secret)")
	f.Int("priority", 0, "P2P notification priority (lower = higher priority)")
	f.Bool("relay", false, "Run as relay/bootstrap node (also joins channel if --channel is set)")
	f.Bool("save", false, "Persist CLI flag overrides to config file")

	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	// Determine config file path
	configPath, _ := f.GetString("config")
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	// If no config file exists, generate a default one with a fresh channel ID.
	if err := ensureConfigFile(configPath); err != nil {
		return err
	}

	// Load config from TOML file
	var cfg config.Config
	if err := config.LoadConfig(configPath, &cfg); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("load configuration: %w", err)
		}
		// No config file, use defaults
	}

	// Apply CLI flag overrides (used for this run; persisted with --save).
	if f.Changed("listen") {
		cfg.Server.Listen, _ = f.GetString("listen")
	}
	if f.Changed("unix") {
		cfg.Server.Unix, _ = f.GetString("unix")
	}
	if f.Changed("token") {
		cfg.Server.Token, _ = f.GetString("token")
	}
	if f.Changed("token-file") {
		cfg.Server.TokenFile, _ = f.GetString("token-file")
	}
	if f.Changed("channel") {
		cfg.P2P.Channel, _ = f.GetString("channel")
	}
	if f.Changed("priority") {
		cfg.P2P.Priority, _ = f.GetInt("priority")
	}
	if f.Changed("ntfy-topic") {
		topic, _ := f.GetString("ntfy-topic")
		cfg.SetNtfyTopic(topic)
	}
	if f.Changed("relay") {
		cfg.RelayMode, _ = f.GetBool("relay")
	}
	cfg.ConfigFile = configPath

	save, _ := f.GetBool("save")
	if save {
		if err := config.SaveConfig(configPath, &cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		slog.Info("saved config from CLI flags", "file", configPath)
	}

	slog.Info("loaded config", "file", configPath)

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	return serve(cfg)
}

// ensureConfigFile writes a default TOML config if the file doesn't exist yet.
func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	channelID, err := config.GenerateChannelID()
	if err != nil {
		return fmt.Errorf("generate channel ID: %w", err)
	}

	if err := config.WriteDefaultConfig(path, channelID); err != nil {
		return err
	}

	slog.Info("created default config", "file", path)
	fmt.Printf("Generated channel ID: %s\n", channelID)
	fmt.Println("Configure the same channel on your other devices to join the notification group.")
	return nil
}

func serve(cfg config.Config) error {
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("starting notify-relay daemon", "channel", cfg.P2P.Channel, "priority", cfg.P2P.Priority, "relay", cfg.RelayMode)

	generatedToken, err := prepareToken(&cfg.Server)
	if err != nil {
		return fmt.Errorf("prepare token: %w", err)
	}

	// errgroup ties the lifetime of all background services together: when
	// any one returns, gctx is cancelled and the rest unwind. g.Wait at the
	// end of serve collects the first non-nil error.
	g, gctx := errgroup.WithContext(rootCtx)

	lockDetector := platform.NewLockDetector()
	if ok, err := lockDetector.IsAvailable(gctx); err != nil || !ok {
		if err != nil {
			slog.Warn("lock detector unavailable", "error", err)
		} else {
			slog.Info("lock detector unavailable (no supported screensaver service)")
		}
		lockDetector = nil
	} else {
		slog.Info("lock detector available")
	}
	if lockDetector != nil {
		g.Go(func() error {
			if err := lockDetector.Run(gctx); err != nil {
				return fmt.Errorf("lock detector: %w", err)
			}
			return nil
		})
	}

	webhookHandler := ntfy.NewWebhookHandler()
	webhookHandler.StartCleanup(gctx)

	channels, err := createChannels(gctx, g, cfg.Channels, webhookHandler)
	if err != nil {
		return fmt.Errorf("init channels: %w", err)
	}

	displayName := platform.DisplayChannelName()
	hasDisplay := false
	for _, ch := range channels {
		if ch.Name() == displayName {
			hasDisplay = true
			break
		}
	}

	// isPresent returns whether this node can currently handle notifications:
	// has a working display channel AND screen is unlocked.
	isPresent := func() bool {
		if !hasDisplay {
			return false
		}
		if lockDetector != nil && lockDetector.IsLocked() {
			return false
		}
		return true
	}

	node, err := p2p.NewNode(gctx, p2p.NodeConfig{
		ChannelID:      cfg.P2P.Channel,
		Priority:       cfg.P2P.Priority,
		ListenAddrs:    cfg.P2P.ListenAddrs,
		IdentityPath:   cfg.P2P.IdentityPath,
		BootstrapPeers: cfg.P2P.Bootstrap,
		EnableMDNS:     cfg.P2P.IsMDNSEnabled(),
		RelayMode:      cfg.RelayMode,
	})
	if err != nil {
		return fmt.Errorf("init p2p node: %w", err)
	}

	routerCfg := buildRouterConfig(cfg.Routes)
	r, err := router.NewWithP2P(routerCfg, lockDetector, channels, node.PeerTable)
	if err != nil {
		_ = node.Host.Close()
		return fmt.Errorf("init router: %w", err)
	}
	// Wire the p2p notification handler to the router before starting the
	// node so we never receive a message without a handler set.
	node.Notify.SetHandler(func(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
		return r.Notify(ctx, req)
	})

	g.Go(func() error {
		if err := node.Run(gctx); err != nil {
			return fmt.Errorf("p2p node: %w", err)
		}
		return nil
	})

	if node.Presence != nil {
		node.Presence.SetPresent(gctx, isPresent())
	}

	if lockDetector != nil {
		lockDetector.SetChangeCallback(func(locked bool) {
			slog.Debug("lock state changed", "locked", locked)
			if node.Presence != nil {
				node.Presence.SetPresent(context.Background(), !locked && hasDisplay)
			}
		})
	}

	if cfg.Server.Listen != "" || cfg.Server.Unix != "" {
		grpcServer, err := server.NewGRPCServer(cfg.Server, newLocalRouter(r, node.Notify), node.PeerTable)
		if err != nil {
			return fmt.Errorf("init grpc server: %w", err)
		}
		if generatedToken {
			slog.Info("auto-generated bearer token for local API")
		}
		// Watcher: when gctx is cancelled, shut down the grpc server. This
		// causes grpcServer.Serve (running in the next goroutine) to return.
		g.Go(func() error {
			<-gctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			grpcServer.Shutdown(shutdownCtx)
			return nil
		})
		g.Go(func() error {
			if err := grpcServer.Serve(); err != nil {
				return fmt.Errorf("grpc server: %w", err)
			}
			return nil
		})
	}

	if cfg.Server.WebhookListen != "" {
		if err := webhookHandler.Listen(cfg.Server.WebhookListen); err != nil {
			return fmt.Errorf("init webhook server: %w", err)
		}
		g.Go(func() error {
			<-gctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			webhookHandler.Shutdown(shutdownCtx)
			return nil
		})
		g.Go(func() error {
			if err := webhookHandler.Serve(); err != nil {
				return fmt.Errorf("webhook server: %w", err)
			}
			return nil
		})
	}

	err = g.Wait()
	slog.Info("shutting down")
	// rootCtx cancellation (SIGINT) propagates to gctx and members return
	// nil for clean shutdown. Any non-nil here is an unexpected failure.
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// localRouter wraps the router and also publishes notifications to the p2p network.
type localRouter struct {
	router        *router.Router
	notifyService *p2p.NotifyService
}

func newLocalRouter(r *router.Router, ns *p2p.NotifyService) *localRouter {
	return &localRouter{router: r, notifyService: ns}
}

func (lr *localRouter) Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	// Route locally first (e.g. dbus display).
	resp, err := lr.router.Notify(ctx, req)
	localHandled := err == nil
	if errors.Is(err, router.ErrNoMatchingRoute) {
		err = nil
	}
	if err != nil {
		return resp, err
	}

	if lr.notifyService == nil {
		return resp, nil
	}

	// If Wait is set and local routing didn't handle it (no matching route
	// or dbus unavailable), publish to p2p synchronously and wait for the
	// remote peer's action reply.
	if req.Wait && !localHandled {
		p2pResp, ok := lr.notifyService.Publish(ctx, req, 30*time.Second)
		if ok {
			return p2pResp, nil
		}
		return resp, nil
	}

	// Fire-and-forget: broadcast to peers, don't wait for reply.
	go lr.notifyService.Broadcast(context.WithoutCancel(ctx), req)
	return resp, nil
}

func (lr *localRouter) CloseNotification(ctx context.Context, id uint32) error {
	return lr.router.CloseNotification(ctx, id)
}

func (lr *localRouter) Capabilities(ctx context.Context) ([]string, error) {
	return lr.router.Capabilities(ctx)
}

func (lr *localRouter) ServerInformation(ctx context.Context) (protocol.ServerInfoResponse, error) {
	return lr.router.ServerInformation(ctx)
}

func buildRouterConfig(routes []router.Route) router.Config {
	if len(routes) == 0 {
		display := platform.DisplayChannelName()
		routes = []router.Route{
			{Condition: "best_active", Channel: display},
			{Condition: "always", Channel: display},
		}
	}
	return router.Config{Routes: routes}
}

// createChannels constructs notification channels from config. Channels
// with a Start/Close lifecycle (e.g. dbus) are launched via the supplied
// errgroup, which ties their lifetime to the rest of serve's services.
func createChannels(ctx context.Context, g *errgroup.Group, configs map[string]config.ChannelConfig, webhookHandler *ntfy.WebhookHandler) ([]channel.Channel, error) {
	addDisplay := func(name string) (platform.DisplayChannel, error) {
		ch := platform.NewNotifyChannel()
		ok, err := ch.IsAvailable(ctx)
		if err != nil {
			return nil, fmt.Errorf("probe display channel %s: %w", name, err)
		}
		if !ok {
			slog.Warn("display notification service not available, skipping channel", "channel", name)
			return nil, nil
		}
		slog.Info("display notification channel available", "channel", name)
		g.Go(func() error {
			if err := ch.Run(ctx); err != nil {
				return fmt.Errorf("display channel %s: %w", name, err)
			}
			return nil
		})
		return ch, nil
	}

	if len(configs) == 0 {
		ch, err := addDisplay(platform.DisplayChannelName())
		if err != nil {
			return nil, err
		}
		if ch == nil {
			return nil, nil
		}
		return []channel.Channel{ch}, nil
	}

	channels := make([]channel.Channel, 0, len(configs))
	for name, chCfg := range configs {
		switch chCfg.Type {
		case "dbus":
			if runtime.GOOS != "linux" {
				slog.Warn("dbus channel only supported on linux", "channel", name)
				continue
			}
			ch, err := addDisplay(name)
			if err != nil {
				return nil, err
			}
			if ch != nil {
				channels = append(channels, ch)
			}
		case "macos":
			if runtime.GOOS != "darwin" {
				slog.Warn("macos channel only supported on darwin", "channel", name)
				continue
			}
			ch, err := addDisplay(name)
			if err != nil {
				return nil, err
			}
			if ch != nil {
				channels = append(channels, ch)
			}
		case "ntfy":
			ntfyCh, err := ntfy.New(ntfy.Config{
				Server:     chCfg.Server,
				Topic:      chCfg.Topic,
				Token:      chCfg.Token,
				WebhookURL: chCfg.WebhookURL,
			})
			if err != nil {
				return nil, fmt.Errorf("init ntfy channel %s: %w", name, err)
			}
			if webhookHandler != nil {
				ntfyCh.SetWebhookHandler(webhookHandler)
			}
			channels = append(channels, ntfyCh)
		default:
			return nil, fmt.Errorf("unknown channel type %q for %s", chCfg.Type, name)
		}
	}
	return channels, nil
}

func prepareToken(cfg *config.ServerConfig) (bool, error) {
	if cfg.Token != "" {
		return false, nil
	}
	if cfg.TokenFile != "" {
		data, err := os.ReadFile(cfg.TokenFile)
		if err == nil {
			cfg.Token = strings.TrimSpace(string(data))
			return false, nil
		}
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read token file: %w", err)
		}
		token, err := generateToken()
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(cfg.TokenFile), 0o700); err != nil {
			return false, fmt.Errorf("create token directory: %w", err)
		}
		if err := os.WriteFile(cfg.TokenFile, []byte(token+"\n"), 0o600); err != nil {
			return false, fmt.Errorf("write token file: %w", err)
		}
		cfg.Token = token
		return true, nil
	}
	if cfg.Unix != "" {
		return false, nil
	}
	if cfg.Listen == "" {
		return false, nil
	}
	token, err := generateToken()
	if err != nil {
		return false, err
	}
	cfg.Token = token
	return true, nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
