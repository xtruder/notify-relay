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
notify-relayd --config ~/.config/notify-relay.jsonc
```

## Configuration

Configuration is loaded using [viper](https://github.com/spf13/viper) with support for:
- **Config files**: JSONC (JSON with comments)
- **Environment variables**: `NOTIFY_RELAY_*` prefix
- **Command-line flags**: Override all other sources

Configuration hierarchy (higher = higher priority):
1. CLI flags
2. Environment variables
3. Config file
4. Defaults

Default config location: `~/.config/notify-relay.jsonc`

### JSONC Support

Config files support JSONC format which allows comments (`//` and `/* */`) in your configuration:

```jsonc
{
  // This is a comment
  "server": {
    "listen": "127.0.0.1:8787"
  }
}
```

### Minimal Example (local only)

```jsonc
{
  // Unix socket for local communication
  "server": {
    "unix": "/run/user/1000/notify-relay.sock"
  },
  "channels": {
    "dbus": { "type": "dbus" }
  },
  // Routes are an array - order matters! First match wins.
  "routes": [
    { "condition": "always", "channel": "dbus" }
  ]
}
```

### Server with Inbound Remotes

Accepts connections from laptops via forwarded sockets:

```jsonc
{
  // Server configuration
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
    // Order matters! First match wins.
    { "condition": "remote_unlocked", "channel": "forward" },
    { "condition": "screen_locked", "channel": "phone" },
    // Always route should be last as a fallback
    { "condition": "always", "channel": "dbus" }
  ],
  // Remotes are now a map (key = remote name, name field auto-populated)
  "remotes": {
    "laptop-work": {
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop-work.sock",
      "priority": 1
    },
    "laptop-personal": {
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop-personal.sock",
      "priority": 2
    }
  }
}
```

### Laptop with Outbound Remote

Connects to a server:

```jsonc
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
  "remotes": {
    "office-server": {
      "type": "outbound",
      "host": "office.example.com:8787",
      "token": "secret-token"
    }
  }
}
```

### Hub-and-Spoke Setup

Machine that connects to multiple servers AND accepts connections:

```jsonc
{
  "server": {
    "listen": "0.0.0.0:8787",
    "token": "hub-token"
  },
  "remotes": {
    "office-server": {
      "type": "outbound",
      "host": "office.internal:8787",
      "token": "office-token",
      "priority": 1
    },
    "home-server": {
      "type": "outbound",
      "host": "home.local:8787",
      "token": "home-token",
      "priority": 2
    }
  },
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

| Field | Description | Environment Variable |
|-------|-------------|---------------------|
| `listen` | TCP address to listen on (e.g., `0.0.0.0:8787`) | `NOTIFY_RELAY_SERVER_LISTEN` |
| `unix` | Unix socket path (e.g., `/run/user/1000/notify-relay.sock`) | `NOTIFY_RELAY_SERVER_UNIX` |
| `token` | Bearer token for authentication | `NOTIFY_RELAY_SERVER_TOKEN` |
| `token_file` | Path to file containing bearer token | `NOTIFY_RELAY_SERVER_TOKEN_FILE` |

### Remotes (`remotes{}`)

Remotes are defined as a map where the key is the remote name. The `name` field is automatically populated from the map key and doesn't need to be specified:

```jsonc
{
  "remotes": {
    "remote-name": {
      "type": "outbound",
      "host": "server:8787",
      "token": "secret",
      "priority": 1
    }
  }
}
```

| Field | Description |
|-------|-------------|
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

Routes are defined as an **array** where order matters - the first matching route wins. This ensures predictable routing behavior.

```jsonc
{
  "routes": [
    // Check if any remote is unlocked - forward to it
    { "condition": "remote_unlocked", "channel": "forward" },
    // If screen is locked, send to phone
    { "condition": "screen_locked", "channel": "phone" },
    // Always route should be last as the fallback
    { "condition": "always", "channel": "dbus" }
  ]
}
```

**Via Environment Variable:**

You can set routes via env var using a JSON array string:

```bash
NOTIFY_RELAY_ROUTES='[{"condition":"remote_unlocked","channel":"forward"},{"condition":"screen_locked","channel":"phone"},{"condition":"always","channel":"dbus"}]'
```

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
--config string        Configuration file (default: ~/.config/notify-relay.jsonc)
--ntfy-topic string    ntfy.sh topic (enables phone notifications)
--version              Show version
```

## Environment Variables

All configuration options can be set via environment variables with the `NOTIFY_RELAY_` prefix. Viper automatically converts config keys to environment variable names:

```bash
# Server settings
NOTIFY_RELAY_SERVER_LISTEN=0.0.0.0:8787
NOTIFY_RELAY_SERVER_UNIX=/run/user/1000/notify-relay.sock
NOTIFY_RELAY_SERVER_TOKEN=secret-token
NOTIFY_RELAY_SERVER_TOKEN_FILE=/etc/notify-relay/token

# Channel settings (for simple channels)
NOTIFY_RELAY_CHANNELS_DBUS_TYPE=dbus
NOTIFY_RELAY_CHANNELS_PHONE_TYPE=ntfy
NOTIFY_RELAY_CHANNELS_PHONE_CONFIG_TOPIC=my-alerts

# Config file path
NOTIFY_RELAY_CONFIG=/etc/notify-relay/config.jsonc

# CLI-only options
NOTIFY_RELAY_NTFY_TOPIC=my-phone-alerts

# Arrays via JSON strings (for routes and remotes)
NOTIFY_RELAY_ROUTES='[{"condition":"remote_unlocked","channel":"forward"},{"condition":"always","channel":"dbus"}]'
NOTIFY_RELAY_REMOTES='[{"name":"office","type":"outbound","host":"server:8787","token":"secret"}]'
```

**JSON Arrays in Environment Variables:**

For ordered arrays like `routes[]`, you can provide a JSON array string:

```bash
NOTIFY_RELAY_ROUTES='[
  {"condition": "remote_unlocked", "channel": "forward"},
  {"condition": "screen_locked", "channel": "phone"},
  {"condition": "always", "channel": "dbus"}
]'
```

For maps like `remotes{}`, you can also use a JSON array - it will be converted to a map internally:

```bash
NOTIFY_RELAY_REMOTES='[
  {"name": "office", "type": "outbound", "host": "office.example.com:8787", "token": "secret"},
  {"name": "backup", "type": "outbound", "host": "backup.local:8787"}
]'
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
notify-relayd --config laptop.jsonc
```

Where `laptop.jsonc`:
```jsonc
{
  "server": { "unix": "/run/user/1000/notify-relay.sock" },
  "remotes": {
    "office": {
      "type": "outbound",
      "host": "localhost:8787",
      "token": "my-secret"
    }
  }
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
```jsonc
{
  "server": { "listen": "0.0.0.0:8787" },
  "remotes": {
    "laptop": {
      "type": "inbound",
      "socket": "/run/user/1000/notify-relay-laptop.sock",
      "priority": 1
    }
  }
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
