// Package config provides centralized configuration management using TOML.
// It supports TOML config files with comments via pelletier/go-toml/v2.
package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
)

// ChannelConfig holds configuration for a single notification channel.
// For ntfy channels, Server and Topic are used directly.
type ChannelConfig struct {
	Type       string `toml:"type" comment:"Channel type: 'dbus', 'macos' or 'ntfy'"`
	Server     string `toml:"server,omitempty" comment:"ntfy server URL (ntfy channels only)"`
	Topic      string `toml:"topic,omitempty" comment:"ntfy topic name (ntfy channels only)"`
	Token      string `toml:"token,omitempty" comment:"ntfy access token (optional)"`
	WebhookURL string `toml:"webhook_url,omitempty" comment:"public URL for ntfy action callbacks (optional)"`
}

// ServerConfig re-exports server.Config for convenience.
type ServerConfig = server.Config

// Route re-exports router.Route for convenience.
type Route = router.Route

// P2PConfig holds libp2p p2p configuration.
type P2PConfig struct {
	Channel      string   `toml:"channel" comment:"Shared secret channel ID — must be the same on all your devices.\nShare this with your other machines to join the same notification group."`
	Priority     int      `toml:"priority" comment:"Priority for notification routing. Lower number = higher priority.\nDesktop: 1, Laptop: 2, VPS last-resort: 50"`
	ListenAddrs  []string `toml:"listen,omitempty" comment:"Multiaddrs to listen on (default: /ip4/0.0.0.0/tcp/0)"`
	IdentityPath string   `toml:"identity_path,omitempty" comment:"File path for persistent ed25519 identity key"`
	MDNS         *bool    `toml:"mdns,omitempty" comment:"Enable mDNS for automatic LAN peer discovery (default: true)"`
	Bootstrap    []string `toml:"bootstrap,omitempty" comment:"Bootstrap/relay peers for WAN connectivity.\nIf empty, IPFS DHT bootstrap nodes are used for peer discovery.\nAdd your VPS relay address here: /ip4/your-vps-ip/tcp/4001/p2p/12D3KooW..."`
}

// IsMDNSEnabled returns whether mDNS is enabled (default true).
func (c *P2PConfig) IsMDNSEnabled() bool {
	if c.MDNS == nil {
		return true
	}
	return *c.MDNS
}

// Config holds the full application configuration.
type Config struct {
	P2P      P2PConfig                `toml:"p2p" comment:"P2P configuration"`
	Server   ServerConfig             `toml:"server" comment:"Local gRPC server for 'notify-relay' send command"`
	Channels map[string]ChannelConfig `toml:"channels,omitempty" comment:"Notification delivery channels"`
	Routes   []Route                  `toml:"routes" comment:"Routes — order matters, first matching condition wins.\nEach node receives all notifications and decides locally whether to act.\n\nAvailable conditions:\n  always           - Always matches\n  screen_locked    - Local screen is locked\n  best_active      - This node is the best present node (has dbus,\n                     unlocked, lowest priority number)\n  no_better_peer   - No present node exists AND this is the highest\n                     priority number (last-resort forwarder)\n  remote_available - At least one remote peer is online\n  remote_present   - At least one remote peer is present"`

	// Runtime-only options (not serialized to config file)
	ConfigFile string `toml:"-"` // Configuration file path
	RelayMode  bool   `toml:"-"` // Run as relay node
}

// Validate performs basic validation on the configuration.
// Note: empty p2p.channel is allowed — the serve command auto-generates one.
func (c *Config) Validate() error {
	// Validate channels
	for name, ch := range c.Channels {
		if ch.Type != "dbus" && ch.Type != "macos" && ch.Type != "ntfy" {
			return fmt.Errorf("channel %s: invalid type %q (must be 'dbus', 'macos' or 'ntfy')", name, ch.Type)
		}
	}

	// Validate routes
	for i, route := range c.Routes {
		if route.Condition == "" {
			return fmt.Errorf("route %d: condition is required", i)
		}
		if route.Channel == "" {
			return fmt.Errorf("route %d: channel is required", i)
		}
	}

	return nil
}

// SetNtfyTopic adds or updates an ntfy channel with the given topic
// and ensures a "no_better_peer" → "ntfy" route exists.
func (c *Config) SetNtfyTopic(topic string) {
	if c.Channels == nil {
		c.Channels = make(map[string]ChannelConfig)
	}
	c.Channels["ntfy"] = ChannelConfig{
		Type:   "ntfy",
		Server: "https://ntfy.sh",
		Topic:  topic,
	}

	// Add route if not already present
	for _, r := range c.Routes {
		if r.Channel == "ntfy" {
			return
		}
	}
	c.Routes = append(c.Routes, Route{
		Condition: "no_better_peer",
		Channel:   "ntfy",
	})
}

// DefaultConfig returns a Config with sensible defaults for the given channel ID.
func DefaultConfig(channelID string) Config {
	chName := "dbus"
	if runtime.GOOS == "darwin" {
		chName = "macos"
	}
	home, _ := os.UserHomeDir()
	unixPath := filepath.Join(home, ".local", "share", "notify-relay.sock")
	return Config{
		P2P: P2PConfig{
			Channel:  channelID,
			Priority: 1,
		},
		Server: ServerConfig{
			Unix: unixPath,
		},
		Channels: map[string]ChannelConfig{
			chName: {Type: chName},
		},
		Routes: []Route{
			{Condition: "best_active", Channel: chName},
		},
	}
}

// WriteDefaultConfig writes a default TOML configuration file with comments.
// The channelID is embedded into the generated config.
func WriteDefaultConfig(path string, channelID string) error {
	cfg := DefaultConfig(channelID)
	return SaveConfig(path, &cfg)
}

// SaveConfig marshals the config to TOML and writes it to disk.
func SaveConfig(path string, cfg *Config) error {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// LoadConfig reads a TOML config file and unmarshals it into cfg.
func LoadConfig(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.ConfigFile = path
	return nil
}

// DefaultConfigPath returns the default configuration file path.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "notify-relay.toml")
}

// Word list for generating channel IDs (EFF short wordlist subset)
var wordList = []string{
	"acid", "acme", "aged", "also", "arch", "army", "aunt", "axis",
	"back", "bail", "bald", "band", "bank", "barn", "base", "bath",
	"bean", "bear", "beer", "bell", "belt", "best", "bird", "bite",
	"blow", "blue", "blur", "boat", "body", "bold", "bolt", "bomb",
	"bond", "bone", "book", "born", "boss", "both", "brew", "bulk",
	"burn", "busy", "cafe", "cage", "cake", "calm", "camp", "cape",
	"card", "care", "cart", "case", "cash", "cast", "cave", "chef",
	"chip", "city", "clad", "clam", "clan", "claw", "clay", "clip",
	"club", "clue", "coal", "coat", "code", "coil", "cold", "cole",
	"colt", "cone", "cook", "cool", "cope", "copy", "cord", "core",
	"cork", "cost", "coup", "crab", "crew", "crop", "crow", "cube",
	"cult", "cure", "curl", "cute", "dado", "dale", "dame", "damp",
	"dare", "dark", "dart", "data", "dawn", "deal", "dear", "deck",
	"deed", "deem", "deep", "deer", "demo", "deny", "desk", "dial",
	"dice", "diet", "dirt", "disc", "dish", "dock", "dome", "done",
	"doom", "door", "dose", "dove", "down", "draw", "drip", "drop",
}

// GenerateChannelID generates a random channel ID from 5 dictionary words.
func GenerateChannelID() (string, error) {
	words := make([]string, 5)
	for i := range words {
		b := make([]byte, 1)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generate channel ID: %w", err)
		}
		words[i] = wordList[int(b[0])%len(wordList)]
	}
	return strings.Join(words, "-"), nil
}
