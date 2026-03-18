# notify-relay v0.3.0

Distributed notification relay supporting desktop-to-desktop and desktop-to-phone forwarding with gRPC.

## Overview

`notify-relay` forwards notifications between machines using gRPC bidirectional streaming. Each daemon instance can:

- Accept incoming connections from other machines (**inbound remotes**)
- Connect to other machines (**outbound remotes**)
- Route notifications intelligently based on remote lock state
- Send notifications to local desktop (dbus) or phone (ntfy.sh)

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    MACHINE A (Office Desktop)                   │
│  ┌──────────┐    ┌──────────────┐    ┌─────────────────────┐   │
│  │  Proxy   │───►│ gRPC Server  │───►│ Router              │   │
│  └──────────┘    └──────────────┘    │ ├─ Remote unlocked   │   │
│                                      │ ├─ Screen locked     │   │
│  ┌──────────┐    ┌──────────────┐    │ └─ Always (dbus)    │   │
│  │ Outbound │───►│ Remote Mgr   │    └─────────────────────┘   │
│  │ (backup) │    └──────────────┘                               │
│  └──────────┘                                                   │
└─────────────────────────────────────────────────────────────────┘
         ▲                           ▲
         │                           │
    gRPC │ stream              gRPC  │ stream
         │                           │
         ▼                           ▼
┌──────────────────┐         ┌──────────────────┐
│   MACHINE B      │         │   MACHINE C      │
│ (Backup Server)  │         │ (Laptop at home) │
│ ┌──────────────┐ │         │ ┌──────────────┐ │
│ │ gRPC Server  │ │         │ │ gRPC Client  │ │
│ └──────────────┘ │         │ │ (connects)   │ │
└──────────────────┘         └──────────────────┘
```

## Quick Start

```bash
# Simple local-only notifications
notify-relayd

# With phone notifications when screen locked
notify-relayd --ntfy-topic my-phone

# Full distributed setup with config
notify-relayd --config ~/.config/notify-relay.conf
```

## Configuration

Default config location: `~/.config/notify-relay.conf`

### Minimal Example (local only)

```json
{
  "server": {
    "unix": "/run/user/1000/notify-relay.sock"
  },
  "channels": {
    "dbus": { "type": "dbus" }
  },
  "routes": [
    { "condition": "always", "channel": "dbus" }
  ]
}
```

### Server with Inbound Remotes

Accepts connections from laptops via forwarded sockets:

```json
{
  "server": {
    "listen": "0.0.0.0:8787",
    "token": "secret-token"
  },
  "channels": {
    "dbus": { "type": "dbus" },
    "phone": {
      "type": "ntfy",
      "config": { "topic": "server-alerts" }
    }
  },
  "routes": [
    { "condition": "remote_unlocked", "channel": "forward" },
    { "condition": "screen_locked", "channel": "phone" },
    { "condition": "always", "channel": "dbus" }
  ],
  "remotes": [
    {
      "name": "laptop-work",
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop-work.sock",
      "priority": 1
    },
    {
      "name": "laptop-personal",
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop-personal.sock",
      "priority": 2
    }
  ]
}
```

### Laptop with Outbound Remote

Connects to a server:

```json
{
  "server": {
    "unix": "/run/user/1000/notify-relay.sock"
  },
  "channels": {
    "dbus": { "type": "dbus" },
    "phone": {
      "type": "ntfy",
      "config": { "topic": "my-alerts" }
    }
  },
  "routes": [
    { "condition": "screen_locked", "channel": "phone" },
    { "condition": "always", "channel": "dbus" }
  ],
  "remotes": [
    {
      "name": "office-server",
      "type": "outbound",
      "host": "office.example.com:8787",
      "token": "secret-token"
    }
  ]
}
```

### Hub-and-Spoke Setup

Machine that connects to multiple servers AND accepts connections:

```json
{
  "server": {
    "listen": "0.0.0.0:8787",
    "token": "hub-token"
  },
  "remotes": [
    {
      "name": "office-server",
      "type": "outbound",
      "host": "office.internal:8787",
      "token": "office-token",
      "priority": 1
    },
    {
      "name": "home-server",
      "type": "outbound",
      "host": "home.local:8787",
      "token": "home-token",
      "priority": 2
    }
  ],
  "channels": {
    "dbus": { "type": "dbus" }
  },
  "routes": [
    { "condition": "remote_unlocked", "channel": "forward" },
    { "condition": "always", "channel": "dbus" }
  ]
}
```

## Configuration Reference

### Server Settings (`server`)

| Field | Description |
|-------|-------------|
| `listen` | TCP address to listen on (e.g., `0.0.0.0:8787`) |
| `unix` | Unix socket path (e.g., `/run/user/1000/notify-relay.sock`) |
| `token` | Bearer token for authentication |
| `token_file` | Path to file containing bearer token |

### Remotes (`remotes[]`)

| Field | Description |
|-------|-------------|
| `name` | Unique identifier for this remote |
| `type` | `"inbound"` (accept connections) or `"outbound"` (connect to) |
| `socket` | For inbound: path to watch for forwarded sockets |
| `host` | For outbound: server address (e.g., `server:8787`) |
| `token` | For outbound: authentication token |
| `priority` | Routing priority (lower = higher priority) |

### Channels (`channels{}`)

| Type | Config |
|------|--------|
| `dbus` | None needed |
| `ntfy` | `{ "server": "https://ntfy.sh", "topic": "my-topic", "token": "..." }` |

### Routes (`routes[]`)

| Condition | Description |
|-----------|-------------|
| `always` | Always matches (use as fallback) |
| `screen_locked` | Matches when local screen is locked |
| `remote_available` | Matches when any remote is connected |
| `remote_unlocked` | Matches when any remote has unlocked screen |

## CLI Flags

```
--listen string        TCP listen address (default: "127.0.0.1:8787")
--unix string          Unix socket path
--token string         Bearer token for authentication
--token-file string    File containing bearer token
--config string        Configuration file (default: ~/.config/notify-relay.conf)
--ntfy-topic string    ntfy.sh topic (enables phone notifications)
--version              Show version
```

## Environment Variables

```
NOTIFY_RELAY_TOKEN          Server token
NOTIFY_RELAY_SOCKET         Unix socket path
NOTIFY_RELAY_NTFY_TOPIC     Default ntfy topic
```

## Usage Examples

### Local Development

```bash
# Terminal 1: Start daemon
notify-relayd

# Terminal 2: Send notification
notify-send-proxy "Build finished" "Tests passed"
```

### Office Desktop with Laptop

**Desktop (office):**
```bash
notify-relayd --listen 0.0.0.0:8787 --token my-secret
```

**Laptop (via SSH tunnel):**
```bash
ssh -L 8787:localhost:8787 office-desktop &
notify-relayd --config laptop.json
```

Where `laptop.json`:
```json
{
  "server": { "unix": "/run/user/1000/notify-relay.sock" },
  "remotes": [
    {
      "name": "office",
      "type": "outbound",
      "host": "localhost:8787",
      "token": "my-secret"
    }
  ]
}
```

**Result:**
- When laptop is unlocked: notifications appear on laptop
- When laptop is locked: notifications go to phone via ntfy.sh

### SSH RemoteForward Setup

Forward laptop's socket to the server via SSH:

**Laptop:**
```bash
notify-relayd --config laptop.json
```

**Server `.ssh/config`:**
```
Host laptop
  HostName laptop.local
  RemoteForward /run/user/1000/notify-relay-laptop.sock /run/user/1000/notify-relay.sock
```

**Server config:**
```json
{
  "server": { "listen": "0.0.0.0:8787" },
  "remotes": [
    {
      "name": "laptop",
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop.sock",
      "priority": 1
    }
  ]
}
```

## Protocol

Uses gRPC bidirectional streaming:

- `Connect` - Bidirectional stream for real-time lock state and notification forwarding
- `Notify` - Send notification
- `CloseNotification` - Close a notification
- `GetCapabilities` - Query capabilities
- `GetServerInfo` - Query server information

See `proto/notify_relay/v1/relay.proto` for full definition.

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for:
- Building from source
- Running tests
- Protocol buffer generation

## License

MIT
