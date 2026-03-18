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

// Config holds the full application configuration
type Config struct {
	Server   server.Config            `json:"server"`
	Channels map[string]ChannelConfig `json:"channels"`
	Routes   []RouteConfig            `json:"routes"`
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

	if configFile != "" {
		// Config file provided: use routes and channels from config
		routerCfg.Routes = make([]router.Route, len(cfg.Routes))
		for i, r := range cfg.Routes {
			routerCfg.Routes[i] = router.Route{
				Condition: r.Condition,
				Channel:   r.Channel,
			}
		}
		channels, err = createChannels(cfg.Channels)
		if err != nil {
			log.Fatalf("init channels: %v", err)
		}
	} else if ntfyTopic != "" {
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
		// Default: dbus only
		routerCfg.Routes = []router.Route{
			{Condition: "always", Channel: "dbus"},
		}
		channels, err = createChannels(nil)
		if err != nil {
			log.Fatalf("init channels: %v", err)
		}
	}

	// Create router with channels and lock detector
	r, err := router.New(routerCfg, lockDetector, channels)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}

	// Create server
	srv, err := server.New(cfg.Server, r)
	if err != nil {
		log.Fatalf("init http server: %v", err)
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
		return fmt.Errorf("read config file: %w", err)
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
