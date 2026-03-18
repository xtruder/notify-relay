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

// RemoteConfig holds configuration for remote connections
type RemoteConfig struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Token    string `json:"token"`
	Priority int    `json:"priority"`
}

// Config holds the full application configuration
type Config struct {
	Mode     string                   `json:"mode"`
	Server   server.Config            `json:"server"`
	Remote   RemoteConfig             `json:"remote"`
	Channels map[string]ChannelConfig `json:"channels"`
	Routes   []RouteConfig            `json:"routes"`
	Remotes  []RemoteConfig           `json:"remotes"` // for server: known remotes with priorities
}

func main() {
	var cfg Config
	var configFile string
	var ntfyTopic string
	var remoteHost string
	var remoteName string
	var remoteToken string
	var showVersion bool

	flag.StringVar(&cfg.Mode, "mode", "standalone", "Daemon mode: standalone, server, or client")
	flag.StringVar(&cfg.Server.Listen, "listen", "127.0.0.1:8787", "TCP listen address")
	flag.StringVar(&cfg.Server.Unix, "unix", "", "Unix socket path instead of TCP")
	flag.StringVar(&cfg.Server.Token, "token", os.Getenv("NOTIFY_RELAY_TOKEN"), "Bearer token for API authentication")
	flag.StringVar(&cfg.Server.TokenFile, "token-file", "", "File containing bearer token")
	flag.StringVar(&configFile, "config", "", "Configuration file (JSON). Default: ~/.config/notify-relay.conf")
	flag.StringVar(&ntfyTopic, "ntfy-topic", os.Getenv("NOTIFY_RELAY_NTFY_TOPIC"), "ntfy.sh topic for phone notifications when screen is locked")
	flag.StringVar(&remoteHost, "remote-host", os.Getenv("NOTIFY_RELAY_REMOTE_HOST"), "Server address for client mode")
	flag.StringVar(&remoteName, "remote-name", os.Getenv("NOTIFY_RELAY_REMOTE_NAME"), "Client hostname (defaults to system hostname)")
	flag.StringVar(&remoteToken, "remote-token", os.Getenv("NOTIFY_RELAY_REMOTE_TOKEN"), "Token for client mode authentication")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.Parse()

	if showVersion {
		fmt.Println(buildinfo.String("notify-relayd"))
		return
	}

	// Set default hostname if not provided
	if remoteName == "" {
		remoteName, _ = os.Hostname()
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

	// Override config with CLI flags
	if ntfyTopic != "" {
		cfg.Mode = "standalone"
	}
	if remoteHost != "" {
		cfg.Mode = "client"
		cfg.Remote.Host = remoteHost
		cfg.Remote.Name = remoteName
		cfg.Remote.Token = remoteToken
	}

	// Initialize based on mode
	switch cfg.Mode {
	case "server":
		runServerMode(cfg)
	case "client":
		runClientMode(cfg)
	default: // standalone
		runStandaloneMode(cfg, ntfyTopic)
	}
}

func runServerMode(cfg Config) {
	log.Printf("Running in SERVER mode on %s", cfg.Server.Listen)

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
	channels, err := createChannels(cfg.Channels)
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
		routerCfg.Routes = []router.Route{
			{Condition: "remote_unlocked", Channel: "forward"},
			{Condition: "screen_locked", Channel: "phone"},
			{Condition: "always", Channel: "dbus"},
		}
	}

	// Create remote manager
	manager := remotes.NewManager()

	// Create router with remote forwarding support
	r, err := router.NewWithRemotes(routerCfg, lockDetector, channels, manager)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}
	defer r.Close()

	// Create gRPC server
	grpcServer, err := server.NewGRPCServer(cfg.Server, r)
	if err != nil {
		log.Fatalf("init grpc server: %v", err)
	}
	grpcServer.SetRemoteManager(manager)

	if generatedToken {
		log.Printf("notify-relayd auto-generated bearer token")
	}

	// Start cleanup goroutine
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	remoteServer := remotes.NewServer(manager)
	remoteServer.StartCleanup(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := grpcServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
		}
		if lockDetector != nil {
			lockDetector.Close()
		}
	}()

	if err := grpcServer.Serve(); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func runClientMode(cfg Config) {
	log.Printf("Running in CLIENT mode connecting to %s as %s", cfg.Remote.Host, cfg.Remote.Name)

	// Initialize lock detector
	lockDetector, err := lock.New()
	if err != nil {
		log.Fatalf("could not initialize lock detector: %v", err)
	}
	defer lockDetector.Close()

	// Create channels
	channels, err := createChannels(cfg.Channels)
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
		routerCfg.Routes = []router.Route{
			{Condition: "screen_locked", Channel: "phone"},
			{Condition: "always", Channel: "dbus"},
		}
	}

	// Create local router (for handling forwarded notifications)
	r, err := router.New(routerCfg, lockDetector, channels)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}
	defer r.Close()

	// Create local gRPC server (for local proxy)
	localServer, err := server.NewGRPCServer(server.Config{
		Unix: "/run/user/" + fmt.Sprintf("%d", os.Getuid()) + "/notify-relay.sock",
	}, r)
	if err != nil {
		log.Fatalf("init local server: %v", err)
	}

	// Create remote client
	remoteCfg := remotes.ClientConfig{
		ServerAddr: cfg.Remote.Host,
		Hostname:   cfg.Remote.Name,
		Token:      cfg.Remote.Token,
		LockState:  lockDetector,
	}
	remoteClient := remotes.NewClient(remoteCfg)

	// Setup notification handler
	remoteClient.SetCallbacks(
		func() { log.Printf("Connected to server %s", cfg.Remote.Host) },
		func() { log.Printf("Disconnected from server %s", cfg.Remote.Host) },
		func(notif *notify_relayv1.ForwardedNotification) {
			// Handle forwarded notification from server
			log.Printf("Received forwarded notification from %s: %s", notif.SourceHostname, notif.Notification.Summary)
			// Route locally
			// Implementation: convert proto to protocol and call r.Notify()
		},
	)

	// Start everything
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := localServer.Serve(); err != nil {
			log.Printf("local server error: %v", err)
		}
	}()

	go func() {
		if err := remoteClient.Connect(ctx); err != nil {
			log.Printf("remote client error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		localServer.Shutdown(shutdownCtx)
		remoteClient.Close()
	}()

	// Wait for shutdown
	<-ctx.Done()
}

func runStandaloneMode(cfg Config, ntfyTopic string) {
	log.Printf("Running in STANDALONE mode")

	generatedToken, err := prepareToken(&cfg.Server)
	if err != nil {
		log.Fatalf("prepare token: %v", err)
	}

	// Initialize lock detector for screen lock detection
	lockDetector, err := lock.New()
	if err != nil {
		log.Printf("warning: could not initialize lock detector: %v", err)
		lockDetector = nil
	}

	// Build router configuration
	var routerCfg router.Config
	var channels []channel.Channel

	if ntfyTopic != "" {
		// CLI ntfy topic provided: route screen_locked -> ntfy, always -> dbus
		routerCfg.Routes = []router.Route{
			{Condition: "screen_locked", Channel: "ntfy"},
			{Condition: "always", Channel: "dbus"},
		}
		channels, err = createChannelsWithNtfy(ntfyTopic)
		if err != nil {
			log.Fatalf("init channels: %v", err)
		}
		log.Printf("Configured ntfy.sh topic: %s", ntfyTopic)
		log.Printf("Routing: screen_locked -> ntfy, unlocked -> dbus")
	} else {
		// Use config or default
		routerCfg.Routes = make([]router.Route, len(cfg.Routes))
		for i, r := range cfg.Routes {
			routerCfg.Routes[i] = router.Route{
				Condition: r.Condition,
				Channel:   r.Channel,
			}
		}

		// If no routes specified, use default: always -> dbus
		if len(routerCfg.Routes) == 0 {
			routerCfg.Routes = []router.Route{
				{Condition: "always", Channel: "dbus"},
			}
		}

		channels, err = createChannels(cfg.Channels)
		if err != nil {
			log.Fatalf("init channels: %v", err)
		}
	}

	// Create router with channels and lock detector
	r, err := router.New(routerCfg, lockDetector, channels)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}

	// Create gRPC server
	srv, err := server.NewGRPCServer(cfg.Server, r)
	if err != nil {
		log.Fatalf("init grpc server: %v", err)
	}
	if generatedToken {
		log.Printf("notify-relayd auto-generated bearer token")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
		}
		if err := r.Close(); err != nil {
			log.Printf("close router: %v", err)
		}
		if lockDetector != nil {
			lockDetector.Close()
		}
	}()

	if err := srv.Serve(); err != nil {
		log.Fatalf("serve: %v", err)
	}
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

func createChannels(configs map[string]ChannelConfig) ([]channel.Channel, error) {
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

func prepareToken(cfg *server.Config) (bool, error) {
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
