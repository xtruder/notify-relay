# notify-relay

`notify-relay` forwards `notify-send` notifications from a VM or remote shell to a Linux desktop session, with optional phone notifications when your screen is locked.

- `notify-relayd`: host-side relay supporting multiple notification channels
- `notify-send-proxy`: guest-side `notify-send` compatible client for Linux or macOS

## Features

- **Multi-channel support**: Send to desktop (dbus) or phone (ntfy.sh) based on screen lock state
- **Smart routing**: Automatically route to phone when screen is locked
- **Multiple configuration methods**: CLI flags, config file, or environment variables
- Single binary tools with no external runtime dependencies
- forwards notifications over Unix sockets or TCP
- defaults to `/run/user/<uid>/notify-relay.sock` on Linux when available
- supports `notify-send` urgency, icons, categories, hints, replacement IDs, actions, `--wait`, and `--print-id`
- auto-generates a token for TCP mode; Unix socket mode uses no token by default

## Install

From a GitHub release:

```bash
curl -fsSL https://raw.githubusercontent.com/xtruder/notify-relay/main/scripts/install.sh | bash
```

From Homebrew/Linuxbrew:

```bash
brew tap xtruder/tap
brew install notify-relay
```

Installed binaries:

- `notify-relayd`
- `notify-send-proxy`

If you want the proxy to replace `notify-send`, create the symlink yourself:

```bash
ln -sfn ~/.local/bin/notify-send-proxy ~/.local/bin/notify-send
```

## Quick Start

### Basic Usage (dbus only)

Run the relay as your desktop user:

```bash
notify-relayd --unix /run/user/$(id -u)/notify-relay.sock
```

### Phone Notifications when Locked

Send notifications to your phone via ntfy.sh when your screen is locked:

```bash
notify-relayd --ntfy-topic my-laptop-notifications
```

This creates a smart setup where:
- When **unlocked**: notifications appear on your desktop
- When **locked**: notifications go to `https://ntfy.sh/my-laptop-notifications`

## Configuration

### Method 1: CLI Flag (Simplest)

```bash
# Send to phone when locked, desktop when unlocked
notify-relayd --ntfy-topic my-topic-name

# With custom server
notify-relayd --ntfy-topic my-topic-name --listen 0.0.0.0:8787
```

### Method 2: Config File

Create `~/.config/notify-relay.conf`:

```json
{
  "server": {
    "listen": "127.0.0.1:8787"
  },
  "channels": {
    "dbus": {
      "type": "dbus"
    },
    "phone": {
      "type": "ntfy",
      "config": {
        "server": "https://ntfy.sh",
        "topic": "my-laptop-notifications"
      }
    }
  },
  "routes": [
    {
      "condition": "screen_locked",
      "channel": "phone"
    },
    {
      "condition": "always",
      "channel": "dbus"
    }
  ]
}
```

Run with config:
```bash
notify-relayd --config ~/.config/notify-relay.conf
# Or just (uses default path):
notify-relayd
```

### Method 3: Environment Variables

```bash
export NOTIFY_RELAY_NTFY_TOPIC=my-topic-name
notify-relayd
```

## Host Usage

### Unix Socket Mode

Run the relay as your desktop user:

```bash
notify-relayd --unix /run/user/$(id -u)/notify-relay.sock
```

The packaged systemd unit uses `%t/notify-relay.sock`, which resolves to `/run/user/<uid>/notify-relay.sock`.

### TCP Mode

For TCP mode instead:

```bash
notify-relayd --listen 127.0.0.1:8787 --token-file ~/.config/notify-relay/token
```

With `--ntfy-topic`:
```bash
notify-relayd --listen 127.0.0.1:8787 --ntfy-topic my-topic
```

## Guest Usage

Unix socket mode:

```bash
export NOTIFY_RELAY_SOCKET=/run/user/$(id -u)/notify-relay.sock
notify-send-proxy "Build finished" "Tests passed"
```

On Linux, `notify-send-proxy` uses `/run/user/<uid>/notify-relay.sock` automatically when it exists.

TCP mode:

```bash
export NOTIFY_RELAY_URL=http://HOST_IP:8787
export NOTIFY_RELAY_TOKEN=$(cat ~/.config/notify-relay/token)
notify-send-proxy "Build finished" "Tests passed"
```

## SSH Forwarding

Remote-forward a Unix socket into a VM:

```sshconfig
Host ubuntu-dev
  HostName ubuntu-dev
  User offlinehq
  StreamLocalBindUnlink yes
  RemoteForward /run/user/1000/notify-relay.sock /run/user/1000/notify-relay.sock
  ExitOnForwardFailure yes
```

Pattern reference:

```sshconfig
RemoteForward /run/user/1000/gnupg/S.gpg-agent /run/user/1000/gnupg/S.gpg-agent.extra
```

Remote-forward TCP instead:

```sshconfig
Host ubuntu-dev
  HostName ubuntu-dev
  User offlinehq
  RemoteForward 8787 127.0.0.1:8787
  ExitOnForwardFailure yes
```

## Routing Conditions

Available routing conditions for config files:

- `always` - Always matches (use as fallback)
- `screen_locked` - Matches when screen is locked (requires lock detector)

Routes are evaluated in order, and the first matching channel is used.

## Channel Types

### dbus
Desktop notifications via `org.freedesktop.Notifications`.

```json
{
  "type": "dbus"
}
```

### ntfy
Push notifications via ntfy.sh (or self-hosted).

```json
{
  "type": "ntfy",
  "config": {
    "server": "https://ntfy.sh",
    "topic": "my-topic-name",
    "token": "optional-access-token"
  }
}
```

Urgency levels are mapped to ntfy priorities:
- `low` (urgency 0) → priority 1 (min)
- `normal` (urgency 1) → priority 3 (default)
- `critical` (urgency 2) → priority 5 (max)

## OpenCode

If a plugin already uses `notify-send`, place `notify-send-proxy` in `PATH` as `notify-send`.

## More

- Development notes: `DEVELOPMENT.md`
- API endpoints: `GET /healthz`, `POST /notify`, `POST /close`, `GET /capabilities`, `GET /server-info`
