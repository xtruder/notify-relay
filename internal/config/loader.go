package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"github.com/tidwall/jsonc"
)

// jsoncReader wraps a file and strips JSONC comments before reading
type jsoncReader struct {
	data []byte
	pos  int
}

func newJSONCReader(filename string) (*jsoncReader, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// Convert JSONC to standard JSON by stripping comments
	jsonData := jsonc.ToJSON(data)

	return &jsoncReader{data: jsonData, pos: 0}, nil
}

func (r *jsoncReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, os.ErrNotExist // EOF
	}

	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// Loader handles viper configuration loading with JSONC support
type Loader struct {
	v *viper.Viper
}

// NewLoader creates a new configuration loader
func NewLoader() *Loader {
	v := viper.New()

	// Set environment prefix and enable automatic env
	// This automatically maps config keys to env vars:
	// server.listen → NOTIFY_RELAY_SERVER_LISTEN
	// server.token → NOTIFY_RELAY_SERVER_TOKEN
	v.SetEnvPrefix("NOTIFY_RELAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	// Set defaults
	v.SetDefault("server.listen", "127.0.0.1:8787")

	return &Loader{v: v}
}

// Load reads the configuration file and unmarshals it into the config struct
func (l *Loader) Load(cfg *Config) error {
	// Get config file path
	configFile := l.v.GetString("config")
	if configFile == "" {
		configFile = DefaultConfigPath()
	}

	if configFile != "" {
		// Check if file exists and is JSONC
		if _, err := os.Stat(configFile); err == nil {
			if err := l.loadFile(configFile); err != nil {
				return fmt.Errorf("load config file %s: %w", configFile, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check config file: %w", err)
		}
		// File doesn't exist, that's ok - use defaults and flags
	}

	// Check for JSON arrays in environment variables
	if err := l.parseJSONArrayEnvVars(); err != nil {
		return fmt.Errorf("parse JSON array env vars: %w", err)
	}

	// Unmarshal into config struct
	if err := l.v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	// Store the config file path
	cfg.ConfigFile = configFile

	return nil
}

// parseJSONArrayEnvVars checks for environment variables containing JSON arrays
// and parses them. This allows setting arrays like routes and remotes via env vars.
func (l *Loader) parseJSONArrayEnvVars() error {
	// Parse routes from env var if it's a JSON string
	if routesStr := l.v.GetString("routes"); routesStr != "" && isJSONArray(routesStr) {
		var routes []Route
		if err := json.Unmarshal([]byte(routesStr), &routes); err != nil {
			return fmt.Errorf("parse routes from env var: %w", err)
		}
		l.v.Set("routes", routes)
	}

	// Parse remotes from env var if it's a JSON string
	if remotesStr := l.v.GetString("remotes"); remotesStr != "" && isJSONArray(remotesStr) {
		// If it's an array, convert to map
		var remoteList []RemoteConfig
		if err := json.Unmarshal([]byte(remotesStr), &remoteList); err != nil {
			return fmt.Errorf("parse remotes from env var: %w", err)
		}
		remotesMap := make(map[string]RemoteConfig)
		for _, r := range remoteList {
			if r.Name == "" {
				continue
			}
			remotesMap[r.Name] = r
		}
		l.v.Set("remotes", remotesMap)
	}

	return nil
}

// isJSONArray checks if a string looks like a JSON array
func isJSONArray(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")
}

// loadFile loads a JSONC config file
func (l *Loader) loadFile(filename string) error {
	ext := strings.ToLower(filepath.Ext(filename))

	// Handle JSONC files
	if ext == ".jsonc" || ext == ".json" {
		reader, err := newJSONCReader(filename)
		if err != nil {
			return err
		}

		// Read all data from the jsoncReader
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(reader); err != nil {
			return fmt.Errorf("read jsonc file: %w", err)
		}

		// Create a new viper instance for this file
		fileViper := viper.New()
		fileViper.SetConfigType("json")

		if err := fileViper.ReadConfig(&buf); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}

		// Merge file config into main viper (file has lower priority than flags/env)
		if err := l.v.MergeConfigMap(fileViper.AllSettings()); err != nil {
			return fmt.Errorf("merge config: %w", err)
		}
	} else {
		// For other formats, use standard viper
		l.v.SetConfigFile(filename)
		if err := l.v.ReadInConfig(); err != nil {
			return err
		}
	}

	return nil
}

// GetViper returns the underlying viper instance
func (l *Loader) GetViper() *viper.Viper {
	return l.v
}

// DefaultConfigPath returns the default configuration file path
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "notify-relay.jsonc")
}
