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

## Project layout

- `cmd/notify-relayd`: host relay entrypoint
- `cmd/notify-send-proxy`: `notify-send` compatible client
- `internal/notify`: D-Bus integration with `org.freedesktop.Notifications`
- `internal/server`: HTTP and Unix socket server
- `internal/protocol`: shared request and response types
- `packaging/systemd`: user service unit
- `packaging/homebrew`: tap formula template
- `scripts/install.sh`: GitHub release installer

## Running locally

Run the relay against the default Unix socket path:

```bash
go run ./cmd/notify-relayd --unix /run/user/$(id -u)/notify-relay.sock
```

Run the proxy against that socket:

```bash
NOTIFY_RELAY_SOCKET=/run/user/$(id -u)/notify-relay.sock \
  go run ./cmd/notify-send-proxy -- "Local test" "Hello from notify-relay"
```

Test TCP mode instead:

```bash
go run ./cmd/notify-relayd --listen 127.0.0.1:8787 --token-file ~/.config/notify-relay/token
NOTIFY_RELAY_URL=http://127.0.0.1:8787 \
NOTIFY_RELAY_TOKEN="$(cat ~/.config/notify-relay/token)" \
  go run ./cmd/notify-send-proxy -- "TCP test" "Hello from notify-relay"
```

## Testing the installer

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
git tag v0.1.x
git push origin v0.1.x
```

The release workflow then:

- builds Linux and macOS tarballs
- publishes the GitHub release
- updates `xtruder/homebrew-tap`

## GitHub Actions

- `CI`: formatting and build checks
- `Brew`: validates the formula on Linux and macOS
- `Release`: builds tarballs, publishes GitHub releases, and syncs `xtruder/homebrew-tap`
