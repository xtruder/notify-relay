# Development

## Local build

Build both binaries:

```bash
go build ./cmd/notify-relayd
go build ./cmd/notify-send-proxy
```

Build everything and catch package-level compile errors:

```bash
go build ./...
```

Version metadata is injected by release builds. Local builds report `dev`.

## Common checks

Run the checks used during development:

```bash
gofmt -w .
go build ./...
bash -n scripts/install.sh
ruby -c Formula/notify-relay.rb
```

Run tests:

```bash
go test ./...
```

## Project layout

- `cmd/notify-relayd`: host relay entrypoint supporting multiple notification channels
- `cmd/notify-send-proxy`: `notify-send` compatible client
- `internal/channel`: channel interface definitions
- `internal/dbus`: dbus notification channel implementation
- `internal/ntfy`: ntfy.sh HTTP notification channel
- `internal/router`: notification routing logic with condition evaluation
- `internal/lock`: screen lock state detector via dbus
- `internal/condition`: routing conditions (always, screen_locked)
- `internal/server`: HTTP and Unix socket server
- `internal/protocol`: shared request and response types
- `internal/tests`: integration and scenario tests
- `packaging/systemd`: user service unit
- `packaging/homebrew`: tap formula template
- `scripts/install.sh`: GitHub release installer

## Running locally

### Basic (dbus only)

Run the relay against the default Unix socket path:

```bash
go run ./cmd/notify-relayd --unix /run/user/$(id -u)/notify-relay.sock
```

Run the proxy against that socket:

```bash
NOTIFY_RELAY_SOCKET=/run/user/$(id -u)/notify-relay.sock \
  go run ./cmd/notify-send-proxy -- "Local test" "Hello from notify-relay"
```

### With ntfy.sh phone notifications

Run with automatic phone notifications when screen is locked:

```bash
go run ./cmd/notify-relayd --ntfy-topic my-test-topic
```

Or with a config file:

```bash
go run ./cmd/notify-relayd --config ~/.config/notify-relay.conf
```

Test TCP mode instead:

```bash
go run ./cmd/notify-relayd --listen 127.0.0.1:8787 --token-file ~/.config/notify-relay/token
NOTIFY_RELAY_URL=http://127.0.0.1:8787 \
NOTIFY_RELAY_TOKEN="$(cat ~/.config/notify-relay/token)" \
  go run ./cmd/notify-send-proxy -- "TCP test" "Hello from notify-relay"
```

## Testing

### Unit tests

Mock-based tests that don't require external services:

```bash
go test ./internal/tests/... -v -run "TestRouter|TestCondition"
```

### Integration tests

Tests that send actual notifications to ntfy.sh:

```bash
# Set your test topic via environment
export TEST_NTFY_TOPIC=my-test-topic
go test ./internal/tests/... -v -run "TestNtfy"
```

Note: In CI environments (`CI=true`), live ntfy tests are automatically skipped.

### Testing the installer

Test the local script syntax:

```bash
bash -n scripts/install.sh
```

Test the installer logic against a published release in a temporary directory:

```bash
tmpbin="$(mktemp -d)/bin"
tmpsystemd="$(mktemp -d)/systemd"
bash scripts/install.sh --version v0.1.3 --install-dir "$tmpbin" --systemd-dir "$tmpsystemd"
```

Trace the installer when debugging shell behavior:

```bash
curl -fsSL https://raw.githubusercontent.com/xtruder/notify-relay/main/scripts/install.sh | bash -x
```

If GitHub raw caching is suspicious, test the script by commit SHA instead of `main`.

## Testing the Homebrew formula

Quick syntax check:

```bash
ruby -c Formula/notify-relay.rb
```

Install from the local formula while iterating:

```bash
brew install --HEAD ./Formula/notify-relay.rb
```

Or mimic the CI tap flow locally:

```bash
brew tap-new xtruder/tap
cp Formula/notify-relay.rb "$(brew --repo xtruder/tap)/Formula/notify-relay.rb"
brew install --build-from-source --HEAD xtruder/tap/notify-relay
```

## Testing systemd integration

Install the packaged user unit:

```bash
install -Dm644 packaging/systemd/notify-relayd.service ~/.config/systemd/user/notify-relayd.service
systemctl --user daemon-reload
systemctl --user enable --now notify-relayd.service
```

Check status and recent logs:

```bash
systemctl --user status notify-relayd.service
journalctl --user -u notify-relayd.service -n 50
```

## Release flow

Typical sequence:

```bash
git push origin main
```

Wait for `CI` and `Brew` on `main` to pass, then:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The release workflow then:

- builds Linux and macOS tarballs
- publishes the GitHub release
- updates `xtruder/homebrew-tap`

## GitHub Actions

- `CI`: formatting, build checks, and tests
- `Brew`: validates the formula on Linux and macOS
- `Release`: builds tarballs, publishes GitHub releases, and syncs `xtruder/homebrew-tap`

## Architecture

### Multi-Channel Support

The relay now supports multiple notification channels:

1. **dbus** - Desktop notifications (original behavior)
2. **ntfy** - HTTP-based push notifications to phones

### Routing

Notifications are routed based on configurable conditions:

- `always` - Always matches (fallback)
- `screen_locked` - Matches when screen is locked (via org.freedesktop.ScreenSaver)

Routes are evaluated in order, first match wins.

### Configuration Priority

1. `--config <path>` flag (explicit)
2. `~/.config/notify-relay.conf` (default)
3. `--ntfy-topic <topic>` (CLI convenience)
4. No config (dbus only, backward compatible)
