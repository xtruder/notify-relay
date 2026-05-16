package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")

	// Write default config
	channelID := "acid-bold-crew-dome-fork"
	if err := WriteDefaultConfig(path, channelID); err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}

	// Print generated config for inspection
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	t.Logf("Generated config:\n%s", string(data))

	// Load it back
	var cfg Config
	if err := LoadConfig(path, &cfg); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Verify values
	if cfg.P2P.Channel != channelID {
		t.Errorf("channel = %q, want %q", cfg.P2P.Channel, channelID)
	}
	if cfg.P2P.Priority != 1 {
		t.Errorf("priority = %d, want 1", cfg.P2P.Priority)
	}
	if cfg.Server.Unix == "" {
		t.Error("server.unix should not be empty")
	}
	if len(cfg.Channels) != 1 {
		t.Errorf("channels count = %d, want 1", len(cfg.Channels))
	}
	if ch, ok := cfg.Channels["dbus"]; !ok || ch.Type != "dbus" {
		t.Error("expected dbus channel")
	}
	if len(cfg.Routes) != 1 {
		t.Errorf("routes count = %d, want 1", len(cfg.Routes))
	}
	if cfg.Routes[0].Condition != "best_active" {
		t.Errorf("route condition = %q, want 'best_active'", cfg.Routes[0].Condition)
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestSaveAndLoadWithNtfy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")

	cfg := DefaultConfig("test-channel")
	cfg.Channels["phone"] = ChannelConfig{
		Type:   "ntfy",
		Server: "https://ntfy.sh",
		Topic:  "my-topic",
	}
	cfg.Routes = append(cfg.Routes, Route{
		Condition: "no_better_peer",
		Channel:   "phone",
	})

	if err := SaveConfig(path, &cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	t.Logf("Config with ntfy:\n%s", string(data))

	var loaded Config
	if err := LoadConfig(path, &loaded); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if phone, ok := loaded.Channels["phone"]; !ok {
		t.Error("expected phone channel")
	} else {
		if phone.Server != "https://ntfy.sh" {
			t.Errorf("phone.server = %q", phone.Server)
		}
		if phone.Topic != "my-topic" {
			t.Errorf("phone.topic = %q", phone.Topic)
		}
	}

	if len(loaded.Routes) != 2 {
		t.Errorf("routes count = %d, want 2", len(loaded.Routes))
	}
}
