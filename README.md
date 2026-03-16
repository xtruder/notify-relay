# notify-relay

`notify-relay` lets a guest VM or remote shell forward `notify-send` notifications to a host desktop session.

`notify-relayd` is for Linux hosts because it talks to `org.freedesktop.Notifications` over D-Bus. The client proxy can be used from Linux or macOS.

It has two binaries:

- `notify-relayd`: host-side HTTP relay that talks directly to `org.freedesktop.Notifications` on the host session D-Bus
- `notify-send-proxy`: guest-side `notify-send` compatible CLI that forwards notifications to the relay

It also ships with:

- a user-level systemd unit at `packaging/systemd/notify-relayd.service`
- a release installer at `scripts/install.sh`
- a Homebrew/Linuxbrew formula at `Formula/notify-relay.rb`

## Features

- maps directly to the freedesktop notification API instead of shelling out to host `notify-send`
- supports app name, icon, urgency, category, timeout, replacement ids, transient notifications, typed hints, action buttons, printing ids, and waiting for close/action events
- can listen on TCP or a Unix socket
- auto-generates a bearer token for TCP listeners unless you provide one; Unix sockets default to no token

## Build

```bash
go build ./cmd/notify-relayd
go build ./cmd/notify-send-proxy
```

Version metadata is injected during release builds with linker flags. Local builds report `dev`.

## Install from GitHub release

```bash
curl -fsSL https://raw.githubusercontent.com/xtruder/notify-relay/main/scripts/install.sh | bash
```

Useful flags:

```bash
bash install.sh --version v0.1.0
bash install.sh --no-systemd
bash install.sh --install-dir ~/.local/bin
```

The installer places:

- `notify-relayd` in `~/.local/bin`
- `notify-send-proxy` in `~/.local/bin`
- `notify-send` symlinked to `notify-send-proxy`
- `notify-relayd.service` in `~/.config/systemd/user`

## Install with Homebrew or Linuxbrew

From a tap or local checkout:

```bash
brew install --HEAD ./Formula/notify-relay.rb
```

This builds `notify-relayd`, `notify-send-proxy`, and installs a `notify-send` symlink. The proxy is useful on Linux and macOS clients; the relay is intended for Linux hosts.

## Host usage

Run the relay as the desktop user so it can reach the session bus:

```bash
notify-relayd --listen 127.0.0.1:8787 --token-file ~/.config/notify-relay/token
```

If `~/.config/notify-relay/token` does not exist yet, `notify-relayd` generates one automatically, writes it with mode `0600`, and uses it for the TCP listener.

If you omit `--token-file` and do not set `NOTIFY_RELAY_TOKEN` or `--token`, a random token is still generated for TCP mode and printed in the daemon logs.

Or listen on a Unix socket:

```bash
notify-relayd --unix ~/.cache/notify-relay.sock
```

Unix socket mode does not require a token unless you explicitly set one.

### Example systemd user service

The repository includes this unit as `packaging/systemd/notify-relayd.service`:

```ini
[Unit]
Description=notify-relay host service

[Service]
ExecStart=%h/.local/bin/notify-relayd --listen 127.0.0.1:8787 --token-file %h/.config/notify-relay/token
Restart=on-failure

[Install]
WantedBy=default.target
```

To install it manually:

```bash
install -Dm644 packaging/systemd/notify-relayd.service ~/.config/systemd/user/notify-relayd.service
systemctl --user daemon-reload
systemctl --user enable --now notify-relayd.service
```

## Guest usage

Set the relay endpoint and token:

```bash
export NOTIFY_RELAY_URL=http://HOST_IP:8787
export NOTIFY_RELAY_TOKEN=$(cat ~/.config/notify-relay/token)
```

If you use a Unix socket on the host and share it into the guest, you can skip `NOTIFY_RELAY_TOKEN` entirely.

Then either call the proxy directly:

```bash
notify-send-proxy "Build finished" "Tests passed"
```

Or install it as `notify-send` earlier in `PATH`:

```bash
install -Dm755 notify-send-proxy ~/.local/bin/notify-send
```

## OpenCode plugin integration

If a plugin or wrapper already uses `notify-send`, placing `notify-send-proxy` in `PATH` is enough.

If you prefer a custom command hook, point it at `notify-send-proxy` with the same environment variables.

## SSH forwarding examples

### Remote forward from a Linux host into a VM

If `notify-relayd` runs on your local Linux host and you SSH from that host into a VM, use `RemoteForward` so the VM gets a loopback port that tunnels back to the host relay:

```sshconfig
Host ubuntu-dev
  HostName ubuntu-dev
  User offlinehq
  RemoteForward 8787 127.0.0.1:8787
  ExitOnForwardFailure yes
```

Then inside the VM:

```bash
export NOTIFY_RELAY_URL=http://127.0.0.1:8787
export NOTIFY_RELAY_TOKEN=$(cat ~/.config/notify-relay/token)
```

### Local forward from a VM to a remote Linux host

If `notify-relayd` runs on the SSH server and the client machine needs access to it, use `LocalForward` instead:

```sshconfig
Host linux-host
  HostName linux-host.example.com
  User offlinehq
  LocalForward 18787 127.0.0.1:8787
  ExitOnForwardFailure yes
```

Then on the client side:

```bash
export NOTIFY_RELAY_URL=http://127.0.0.1:18787
export NOTIFY_RELAY_TOKEN=$(cat ~/.config/notify-relay/token)
```

### Existing multiplexed SSH session

If you already have an SSH master connection open, you can add the forward without reconnecting:

```bash
ssh -O forward -R 8787:127.0.0.1:8787 ubuntu-dev
```

Or remove it later:

```bash
ssh -O cancel -R 8787:127.0.0.1:8787 ubuntu-dev
```

## API

- `GET /healthz`
- `POST /notify`
- `POST /close`
- `GET /capabilities`
- `GET /server-info`

## Notes

- the relay should only be exposed on a trusted interface or behind SSH port forwarding
- bearer token auth is optional but strongly recommended for TCP use
- final feature support still depends on the host notification daemon

## GitHub Actions

- `CI` checks formatting and builds the project on pushes and pull requests
- `Release` builds Linux and macOS tarballs, generates checksums, and publishes GitHub release assets on `v*` tags
- `Brew` verifies the Homebrew/Linuxbrew formula builds on macOS and Linux
