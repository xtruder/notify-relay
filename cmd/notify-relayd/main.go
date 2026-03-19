package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/xtruder/notify-relay/internal/buildinfo"
	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/config"
	"github.com/xtruder/notify-relay/internal/dbus"
	"github.com/xtruder/notify-relay/internal/lock"
	"github.com/xtruder/notify-relay/internal/ntfy"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/remotes"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
)

func main() {
	// Create loader first so we can bind flags to viper
	loader := config.NewLoader()
	v := loader.GetViper()

	// Define pflags
	pflag.String("listen", "127.0.0.1:8787", "TCP listen address")
	pflag.String("unix", "", "Unix socket path instead of TCP")
	pflag.String("token", "", "Bearer token for API authentication")
	pflag.String("token-file", "", "File containing bearer token")
	pflag.String("config", "", "Configuration file (JSONC). Default: ~/.config/notify-relay.jsonc")
	pflag.String("ntfy-topic", "", "ntfy.sh topic for phone notifications when screen is locked")
	pflag.Bool("version", false, "Show version information")

	// Bind pflags to viper using BindPFlag
	v.BindPFlag("server.listen", pflag.Lookup("listen"))
	v.BindPFlag("server.unix", pflag.Lookup("unix"))
	v.BindPFlag("server.token", pflag.Lookup("token"))
	v.BindPFlag("server.token_file", pflag.Lookup("token-file"))
	v.BindPFlag("config", pflag.Lookup("config"))
	v.BindPFlag("ntfy_topic", pflag.Lookup("ntfy-topic"))
	v.BindPFlag("version", pflag.Lookup("version"))

	// Parse pflags
	pflag.Parse()

	// Handle version flag
	if v.GetBool("version") {
		fmt.Println(buildinfo.String("notify-relayd"))
		return
	}

	// Load configuration
	var cfg config.Config
	if err := loader.Load(&cfg); err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	if cfg.ConfigFile != "" {
		slog.Info("loaded config", "file", cfg.ConfigFile)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Run unified daemon
	run(cfg)
}

func run(cfg config.Config) {
	slog.Info("starting notify-relayd", "mode", "unified")

	// Prepare token
	generatedToken, err := prepareToken(&cfg.Server)
	if err != nil {
		slog.Error("prepare token failed", "error", err)
		os.Exit(1)
	}

	// Initialize lock detector
	lockDetector, err := lock.New()
	if err != nil {
		slog.Warn("could not initialize lock detector", "error", err)
		lockDetector = nil
	}

	// Create channels
	channels, err := createChannels(cfg.Channels, cfg.NtfyTopic)
	if err != nil {
		slog.Error("init channels failed", "error", err)
		os.Exit(1)
	}

	// Build router configuration
	routerCfg := buildRouterConfig(cfg.Routes, cfg.NtfyTopic)

	// Create remote manager
	manager := remotes.NewManager()

	// Start watchers for inbound remotes
	inboundSockets := cfg.GetInboundSockets()
	if len(inboundSockets) > 0 {
		outboundWatcher := remotes.NewOutboundWatcher(manager, inboundSockets, "", "server")
		go func() {
			if err := outboundWatcher.Start(context.Background()); err != nil {
				slog.Error("outbound watcher error", "error", err)
			}
		}()
		defer outboundWatcher.Stop()
		slog.Info("watching for inbound sockets", "sockets", inboundSockets)
	}

	// Start outbound remote connections
	outboundRemotes := cfg.GetOutboundRemotes()
	var outboundClients []*remotes.Client
	for _, remote := range outboundRemotes {
		client := startOutboundRemote(remote, lockDetector)
		outboundClients = append(outboundClients, client)
	}

	// Create router with remote forwarding support
	r, err := router.NewWithRemotes(routerCfg, lockDetector, channels, manager)
	if err != nil {
		slog.Error("init router failed", "error", err)
		os.Exit(1)
	}
	defer r.Close()

	// Create gRPC server (if configured)
	var grpcServer *server.GRPCServer
	if cfg.Server.Listen != "" || cfg.Server.Unix != "" {
		grpcServer, err = server.NewGRPCServer(cfg.Server, r)
		if err != nil {
			slog.Error("init grpc server failed", "error", err)
			os.Exit(1)
		}
		grpcServer.SetRemoteManager(manager)
		if generatedToken {
			slog.Info("notify-relayd auto-generated bearer token")
		}
		slog.Info("gRPC server listening", "address", cfg.Server.Listen)
	}

	// Start cleanup goroutine
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	remoteServer := remotes.NewServer(manager)
	remoteServer.StartCleanup(ctx)

	// Start gRPC server if configured
	if grpcServer != nil {
		go func() {
			if err := grpcServer.Serve(); err != nil {
				slog.Error("grpc server error", "error", err)
			}
		}()
	}

	// Start outbound remote connections with context
	for _, client := range outboundClients {
		go func(c *remotes.Client) {
			if err := c.Connect(ctx); err != nil {
				slog.Error("remote connection error", "error", err)
			}
		}(client)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if grpcServer != nil {
		if err := grpcServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown server error", "error", err)
		}
	}

	// Close all outbound clients
	for _, client := range outboundClients {
		client.Close()
	}

	if lockDetector != nil {
		lockDetector.Close()
	}
}

func startOutboundRemote(remote config.RemoteConfig, lockDetector *lock.Detector) *remotes.Client {
	remoteClient := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: remote.Host,
		Hostname:   remote.Name,
		Token:      remote.Token,
		LockState:  lockDetector,
	})

	remoteClient.SetCallbacks(
		func() { slog.Info("connected to remote", "name", remote.Name, "host", remote.Host) },
		func() { slog.Info("disconnected from remote", "name", remote.Name, "host", remote.Host) },
		func(notif *notify_relayv1.ForwardedNotification) {
			slog.Info("received forwarded notification", "from", remote.Name, "summary", notif.Notification.Summary)
		},
	)

	return remoteClient
}

func buildRouterConfig(routes []router.Route, ntfyTopic string) router.Config {
	// Default routes if none specified
	if len(routes) == 0 {
		if ntfyTopic != "" {
			routes = []router.Route{
				{Condition: "screen_locked", Channel: "ntfy"},
				{Condition: "always", Channel: "dbus"},
			}
		} else {
			routes = []router.Route{
				{Condition: "remote_unlocked", Channel: "forward"},
				{Condition: "screen_locked", Channel: "phone"},
				{Condition: "always", Channel: "dbus"},
			}
		}
	}

	return router.Config{
		Routes: routes,
	}
}

func createChannels(configs map[string]config.ChannelConfig, ntfyTopic string) ([]channel.Channel, error) {
	// If ntfy topic provided via CLI, create default channels with it
	if ntfyTopic != "" {
		return createChannelsWithNtfy(ntfyTopic)
	}

	// If no channels configured, default to dbus only
	if len(configs) == 0 {
		dbusCh, err := dbus.New()
		if err != nil {
			return nil, fmt.Errorf("init dbus channel: %w", err)
		}
		return []channel.Channel{dbusCh}, nil
	}

	channels := make([]channel.Channel, 0, len(configs))

	for name, cfg := range configs {
		switch cfg.Type {
		case "dbus":
			dbusCh, err := dbus.New()
			if err != nil {
				return nil, fmt.Errorf("init dbus channel %s: %w", name, err)
			}
			channels = append(channels, dbusCh)

		case "ntfy":
			var ntfyCfg ntfy.Config
			if len(cfg.Config) > 0 {
				if err := json.Unmarshal(cfg.Config, &ntfyCfg); err != nil {
					return nil, fmt.Errorf("parse ntfy config for %s: %w", name, err)
				}
			}
			ntfyCh, err := ntfy.New(ntfyCfg)
			if err != nil {
				return nil, fmt.Errorf("init ntfy channel %s: %w", name, err)
			}
			channels = append(channels, ntfyCh)

		default:
			return nil, fmt.Errorf("unknown channel type %q for %s", cfg.Type, name)
		}
	}

	return channels, nil
}

func createChannelsWithNtfy(topic string) ([]channel.Channel, error) {
	dbusCh, err := dbus.New()
	if err != nil {
		return nil, fmt.Errorf("init dbus channel: %w", err)
	}

	ntfyCh, err := ntfy.New(ntfy.Config{
		Server: "https://ntfy.sh",
		Topic:  topic,
	})
	if err != nil {
		return nil, fmt.Errorf("init ntfy channel: %w", err)
	}

	return []channel.Channel{dbusCh, ntfyCh}, nil
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
