package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xtruder/notify-relay/internal/buildinfo"
	"github.com/xtruder/notify-relay/internal/channel"
	"github.com/xtruder/notify-relay/internal/dbus"
	"github.com/xtruder/notify-relay/internal/lock"
	"github.com/xtruder/notify-relay/internal/ntfy"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/remotes"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
)

// ChannelConfig holds configuration for a single channel
type ChannelConfig struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// RouteConfig holds a single routing rule
type RouteConfig struct {
	Condition string `json:"condition"`
	Channel   string `json:"channel"`
}

// ServerConfig holds server-specific configuration
type ServerConfig struct {
	Listen    string `json:"listen,omitempty"`
	Unix      string `json:"unix,omitempty"`
	Token     string `json:"token,omitempty"`
	TokenFile string `json:"token_file,omitempty"`
}

// RemoteEndpoint defines a single remote connection endpoint
type RemoteEndpoint struct {
	Name     string `json:"name"`             // Unique identifier (e.g., "laptop-work")
	Type     string `json:"type"`             // "outbound" (we connect) or "inbound" (they connect)
	Host     string `json:"host,omitempty"`   // For outbound: "server:8787"
	Socket   string `json:"socket,omitempty"` // For inbound: watch this socket path
	Token    string `json:"token,omitempty"`  // Auth token for outbound connections
	Priority int    `json:"priority"`         // Routing priority (lower = higher priority)
}

// Config holds the full application configuration
type Config struct {
	Server   ServerConfig             `json:"server"`   // Local server settings
	Remotes  []RemoteEndpoint         `json:"remotes"`  // Multiple remote endpoints
	Channels map[string]ChannelConfig `json:"channels"` // Channel definitions
	Routes   []RouteConfig            `json:"routes"`   // Routing rules
}

func main() {
	var cfg Config
	var configFile string
	var ntfyTopic string
	var showVersion bool

	flag.StringVar(&cfg.Server.Listen, "listen", "127.0.0.1:8787", "TCP listen address")
	flag.StringVar(&cfg.Server.Unix, "unix", "", "Unix socket path instead of TCP")
	flag.StringVar(&cfg.Server.Token, "token", os.Getenv("NOTIFY_RELAY_TOKEN"), "Bearer token for API authentication")
	flag.StringVar(&cfg.Server.TokenFile, "token-file", "", "File containing bearer token")
	flag.StringVar(&configFile, "config", "", "Configuration file (JSON). Default: ~/.config/notify-relay.conf")
	flag.StringVar(&ntfyTopic, "ntfy-topic", os.Getenv("NOTIFY_RELAY_NTFY_TOPIC"), "ntfy.sh topic for phone notifications when screen is locked")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.Parse()

	if showVersion {
		fmt.Println(buildinfo.String("notify-relayd"))
		return
	}

	// Load config file: explicit flag takes precedence over default path
	if configFile == "" {
		configFile = defaultConfigPath()
	}

	if configFile != "" {
		if err := loadConfig(configFile, &cfg); err != nil {
			if !os.IsNotExist(err) {
				log.Fatalf("load config: %v", err)
			}
			// Config file doesn't exist, that's ok - we'll use defaults
		} else {
			log.Printf("Loaded config from %s", configFile)
		}
	}

	// Run unified daemon
	run(cfg, ntfyTopic)
}

func run(cfg Config, ntfyTopic string) {
	log.Printf("Starting notify-relayd (unified mode)")

	// Prepare token
	generatedToken, err := prepareToken(&cfg.Server)
	if err != nil {
		log.Fatalf("prepare token: %v", err)
	}

	// Initialize lock detector
	lockDetector, err := lock.New()
	if err != nil {
		log.Printf("warning: could not initialize lock detector: %v", err)
		lockDetector = nil
	}

	// Create channels
	channels, err := createChannels(cfg.Channels, ntfyTopic)
	if err != nil {
		log.Fatalf("init channels: %v", err)
	}

	// Build router configuration
	routerCfg := router.Config{
		Routes: make([]router.Route, len(cfg.Routes)),
	}
	for i, r := range cfg.Routes {
		routerCfg.Routes[i] = router.Route{
			Condition: r.Condition,
			Channel:   r.Channel,
		}
	}

	// Default routes if none specified
	if len(routerCfg.Routes) == 0 {
		if ntfyTopic != "" {
			routerCfg.Routes = []router.Route{
				{Condition: "screen_locked", Channel: "ntfy"},
				{Condition: "always", Channel: "dbus"},
			}
		} else {
			routerCfg.Routes = []router.Route{
				{Condition: "remote_unlocked", Channel: "forward"},
				{Condition: "screen_locked", Channel: "phone"},
				{Condition: "always", Channel: "dbus"},
			}
		}
	}

	// Create remote manager
	manager := remotes.NewManager()

	// Start watchers for inbound remotes (sockets to watch and connect to)
	var inboundSockets []string
	for _, remote := range cfg.Remotes {
		if remote.Type == "inbound" && remote.Socket != "" {
			inboundSockets = append(inboundSockets, remote.Socket)
		}
	}

	if len(inboundSockets) > 0 {
		outboundWatcher := remotes.NewOutboundWatcher(manager, inboundSockets, "", "server")
		go func() {
			if err := outboundWatcher.Start(context.Background()); err != nil {
				log.Printf("Outbound watcher error: %v", err)
			}
		}()
		defer outboundWatcher.Stop()
		log.Printf("Watching for inbound sockets: %v", inboundSockets)
	}

	// Start outbound remote connections
	var outboundClients []*remotes.Client
	for _, remote := range cfg.Remotes {
		if remote.Type == "outbound" && remote.Host != "" {
			client := startOutboundRemote(remote, lockDetector)
			outboundClients = append(outboundClients, client)
		}
	}

	// Create router with remote forwarding support
	r, err := router.NewWithRemotes(routerCfg, lockDetector, channels, manager)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}
	defer r.Close()

	// Create gRPC server (if configured)
	var grpcServer *server.GRPCServer
	if cfg.Server.Listen != "" || cfg.Server.Unix != "" {
		srvConfig := server.Config{
			Listen: cfg.Server.Listen,
			Unix:   cfg.Server.Unix,
			Token:  cfg.Server.Token,
		}
		grpcServer, err = server.NewGRPCServer(srvConfig, r)
		if err != nil {
			log.Fatalf("init grpc server: %v", err)
		}
		grpcServer.SetRemoteManager(manager)
		if generatedToken {
			log.Printf("notify-relayd auto-generated bearer token")
		}
		log.Printf("gRPC server listening on %s", cfg.Server.Listen)
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
				log.Printf("grpc server error: %v", err)
			}
		}()
	}

	// Start outbound remote connections with context
	for _, client := range outboundClients {
		go func(c *remotes.Client) {
			if err := c.Connect(ctx); err != nil {
				log.Printf("Remote connection error: %v", err)
			}
		}(client)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	log.Printf("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if grpcServer != nil {
		if err := grpcServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
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

func startOutboundRemote(remote RemoteEndpoint, lockDetector *lock.Detector) *remotes.Client {
	remoteClient := remotes.NewClient(remotes.ClientConfig{
		ServerAddr: remote.Host,
		Hostname:   remote.Name,
		Token:      remote.Token,
		LockState:  lockDetector,
	})

	remoteClient.SetCallbacks(
		func() { log.Printf("Connected to remote %s at %s", remote.Name, remote.Host) },
		func() { log.Printf("Disconnected from remote %s at %s", remote.Name, remote.Host) },
		func(notif *notify_relayv1.ForwardedNotification) {
			log.Printf("Received forwarded notification from %s: %s", remote.Name, notif.Notification.Summary)
		},
	)

	return remoteClient
}

func loadConfig(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}
	return nil
}

func createChannels(configs map[string]ChannelConfig, ntfyTopic string) ([]channel.Channel, error) {
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

func prepareToken(cfg *ServerConfig) (bool, error) {
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

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "notify-relay.conf")
}
