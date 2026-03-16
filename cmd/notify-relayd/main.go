package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/xtruder/notify-relay/internal/notify"
	"github.com/xtruder/notify-relay/internal/server"
)

func main() {
	var cfg server.Config
	var showVersion bool
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:8787", "TCP listen address")
	flag.StringVar(&cfg.Unix, "unix", "", "Unix socket path instead of TCP")
	flag.StringVar(&cfg.Token, "token", os.Getenv("NOTIFY_RELAY_TOKEN"), "Bearer token for API authentication")
	flag.StringVar(&cfg.TokenFile, "token-file", "", "File containing bearer token")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.Parse()

	if showVersion {
		fmt.Println(buildinfo.String("notify-relayd"))
		return
	}

	generatedToken, err := prepareToken(&cfg)
	if err != nil {
		log.Fatalf("prepare token: %v", err)
	}

	relay, err := notify.New()
	if err != nil {
		log.Fatalf("init notification relay: %v", err)
	}
	defer func() {
		if err := relay.Close(); err != nil {
			log.Printf("close relay: %v", err)
		}
	}()

	srv, err := server.New(cfg, relay)
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
	}()

	if err := srv.Serve(); err != nil {
		log.Fatalf("serve: %v", err)
	}
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
