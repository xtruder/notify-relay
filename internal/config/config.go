// Package config provides centralized configuration management using viper.
// It supports JSONC config files, environment variables, and command-line flags.
package config

import (
	"encoding/json"
	"fmt"

	"github.com/xtruder/notify-relay/internal/remotes"
	"github.com/xtruder/notify-relay/internal/router"
	"github.com/xtruder/notify-relay/internal/server"
)

// ChannelConfig holds configuration for a single channel
type ChannelConfig struct {
	Type   string          `mapstructure:"type" json:"type"`
	Config json.RawMessage `mapstructure:"config,omitempty" json:"config,omitempty"`
}

// ServerConfig re-exports server.Config for convenience
type ServerConfig = server.Config

// Route re-exports router.Route for convenience
type Route = router.Route

// RemoteConfig wraps remotes.ClientConfig with additional fields for config file
type RemoteConfig struct {
	remotes.ClientConfig
	Name   string `mapstructure:"name" json:"name"`
	Type   string `mapstructure:"type" json:"type"`                         // "outbound" or "inbound"
	Socket string `mapstructure:"socket,omitempty" json:"socket,omitempty"` // For inbound remotes
	Host   string `mapstructure:"host,omitempty" json:"host,omitempty"`     // For outbound remotes (alias for ServerAddr)
}

// Config holds the full application configuration
type Config struct {
	Server   ServerConfig             `mapstructure:"server" json:"server"`     // Local server settings
	Remotes  map[string]RemoteConfig  `mapstructure:"remotes" json:"remotes"`   // Map of remote name to config
	Channels map[string]ChannelConfig `mapstructure:"channels" json:"channels"` // Channel definitions
	Routes   []Route                  `mapstructure:"routes" json:"routes"`     // Ordered routing rules

	// CLI-only options (not in config file)
	ConfigFile string `mapstructure:"config"`     // Configuration file path
	NtfyTopic  string `mapstructure:"ntfy_topic"` // ntfy.sh topic for phone notifications
	Version    bool   `mapstructure:"version"`    // Show version
}

// GetOutboundRemotes extracts outbound remotes from config
func (c *Config) GetOutboundRemotes() []RemoteConfig {
	var outbounds []RemoteConfig
	for name, remote := range c.Remotes {
		if remote.Type == "outbound" {
			// Copy Host to ServerAddr if needed
			if remote.Host != "" && remote.ServerAddr == "" {
				remote.ServerAddr = remote.Host
			}
			// Ensure name is set
			if remote.Name == "" {
				remote.Name = name
			}
			outbounds = append(outbounds, remote)
		}
	}
	return outbounds
}

// GetInboundSockets extracts inbound socket paths from config
func (c *Config) GetInboundSockets() []string {
	var sockets []string
	for _, remote := range c.Remotes {
		if remote.Type == "inbound" && remote.Socket != "" {
			sockets = append(sockets, remote.Socket)
		}
	}
	return sockets
}

// Validate performs basic validation on the configuration
func (c *Config) Validate() error {
	// Validate server config
	if c.Server.Listen == "" && c.Server.Unix == "" {
		// This is actually valid - could be client-only mode
	}

	// Validate remote configs
	for name, remote := range c.Remotes {
		if remote.Type != "inbound" && remote.Type != "outbound" {
			return fmt.Errorf("remote %s: invalid type %q (must be 'inbound' or 'outbound')", name, remote.Type)
		}
		if remote.Type == "outbound" && remote.Host == "" && remote.ServerAddr == "" {
			return fmt.Errorf("remote %s: outbound remotes require 'host'", name)
		}
		if remote.Type == "inbound" && remote.Socket == "" {
			return fmt.Errorf("remote %s: inbound remotes require 'socket'", name)
		}
	}

	// Validate channels
	for name, ch := range c.Channels {
		if ch.Type != "dbus" && ch.Type != "ntfy" {
			return fmt.Errorf("channel %s: invalid type %q (must be 'dbus' or 'ntfy')", name, ch.Type)
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
