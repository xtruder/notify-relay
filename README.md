# notify-relay v0.3.0

Distributed notification relay supporting desktop-to-desktop and desktop-to-phone forwarding with gRPC.

## Overview

`notify-relay` forwards notifications between machines using gRPC. It supports three modes:

- **Standalone** (default): Local-only notifications with optional phone forwarding via ntfy.sh
- **Server**: Accepts connections from remote clients (laptops) and routes intelligently
- **Client**: Connects to a server and forwards lock state changes

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         SERVER                                  │
│  ┌──────────┐    ┌──────────────┐    ┌─────────────────────┐  │
│  │  Proxy   │───►│ gRPC Server  │───►│ Router              │  │
│  └──────────┘    └──────────────┘    │ ├─ Remote unlocked   │  │
│                                      │ ├─ Screen locked     │  │
│  ┌──────────┐    ┌──────────────┐    │ └─ Always (dbus)   │  │
│  │ Client A │◄──►│ Remote Mgr   │    └─────────────────────┘  │
│  │ (laptop) │    └──────────────┘                            │
│  └──────────┘                                                 │
└─────────────────────────────────────────────────────────────────┘
         ▲                           ▲
         │                           │
    gRPC │ stream              gRPC  │ stream
         │                           │
         ▼                           ▼
┌──────────────────┐         ┌──────────────────┐
│   LAPTOP A       │         │   LAPTOP B       │
│ ┌──────────────┐ │         │ ┌──────────────┐ │
│ │ gRPC Client  │ │         │ │ gRPC Client  │ │
│ │ Lock Reporter│ │         │ │ Lock Reporter│ │
│ └──────────────┘ │         │ └──────────────┘ │
│ ┌──────────────┐ │         │ ┌──────────────┐ │
│ │ Local Router │ │         │ │ Local Router │ │
│ │ ├─ dbus      │ │         │ │ ├─ dbus      │ │
│ │ └─ ntfy      │ │         │ │ └─ ntfy      │ │
│ └──────────────┘ │         │ └──────────────┘ │
└──────────────────┘         └──────────────────┘
```

## Modes

### Standalone Mode (Default)

Simple local-only operation with smart routing:

```bash
# Basic - notifications only on local desktop
notify-relayd

# With phone notifications when locked
notify-relayd --ntfy-topic my-phone

# Or with config file
notify-relayd --config ~/.config/notify-relay.conf
```

### Server Mode

Accepts connections from remote clients and routes intelligently:

```bash
notify-relayd --mode server --listen 0.0.0.0:8787
```

When a client is connected and unlocked, notifications go to the client. When locked or disconnected, they go to local channels (ntfy/dbus).

### Client Mode

Connects to a server and reports lock state:

```bash
notify-relayd --mode client --remote-host server.example.com:8787
```

The client:
1. Maintains persistent gRPC connection to server
2. Reports screen lock/unlock events
3. Auto-reconnects on disconnect
4. Receives forwarded notifications from server
5. Routes locally based on its own lock state

## Configuration

### Config File

Default location: `~/.config/notify-relay.conf`

**Standalone Example:**
```json
{
  "mode": "standalone",
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
  ]
}
```

**Server Example:**
```json
{
  "mode": "server",
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
    { "name": "laptop-work", "priority": 1 },
    { "name": "laptop-personal", "priority": 2 }
  ]
}
```

**Client Example:**
```json
{
  "mode": "client",
  "remote": {
    "host": "server.example.com:8787",
    "name": "laptop-work",
    "token": "secret-token"
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
  ]
}
```

### CLI Flags

```
--mode string          Daemon mode: standalone, server, or client (default: standalone)
--listen string        TCP listen address (default: "127.0.0.1:8787")
--unix string          Unix socket path
--token string         Bearer token for authentication
--token-file string    File containing bearer token
--config string        Configuration file (default: ~/.config/notify-relay.conf)
--ntfy-topic string    ntfy.sh topic (enables phone notifications)

# Client mode only:
--remote-host string   Server address to connect to
--remote-name string   Client hostname (defaults to system hostname)
--remote-token string  Token for server authentication
```

### Environment Variables

```
NOTIFY_RELAY_TOKEN          Server token
NOTIFY_RELAY_SOCKET         Unix socket path for proxy
NOTIFY_RELAY_URL            Server URL for proxy
NOTIFY_RELAY_NTFY_TOPIC     Default ntfy topic
NOTIFY_RELAY_REMOTE_HOST    Default remote host for client mode
NOTIFY_RELAY_REMOTE_NAME    Default remote name for client mode
NOTIFY_RELAY_REMOTE_TOKEN   Default remote token for client mode
```

## Usage Examples

### Local Development

```bash
# Terminal 1: Start daemon
notify-relayd

# Terminal 2: Send notification
notify-send-proxy "Build finished" "Tests passed"
```

### Server with Remote Laptop

**Server (desktop in office):**
```bash
notify-relayd --mode server --listen 0.0.0.0:8787 --token my-secret
```

**Client (laptop at home):**
```bash
# Via SSH tunnel
ssh -L 8787:localhost:8787 office-desktop

# Then in another terminal
notify-relayd --mode client --remote-host localhost:8787 --remote-token my-secret
```

**Result:**
- When laptop is unlocked: notifications appear on laptop
- When laptop is locked: notifications go to phone via ntfy.sh

### Multiple Laptops

**Server:**
```bash
notify-relayd --mode server --listen 0.0.0.0:8787 --config server.json
```

**Work Laptop (Priority 1):**
```bash
notify-relayd --mode client --remote-host server:8787 --remote-name laptop-work --config laptop.json
```

**Personal Laptop (Priority 2):**
```bash
notify-relayd --mode client --remote-host server:8787 --remote-name laptop-personal --config laptop.json
```

Notifications go to the highest priority unlocked laptop.

## Routing Conditions

Available conditions for routes:

- `always` - Always matches (use as fallback)
- `screen_locked` - Matches when screen is locked
- `remote_available` - Matches when any remote client is connected (server only)
- `remote_unlocked` - Matches when a remote client is unlocked (server only)

Routes are evaluated in order, first match wins.

## Channel Types

### dbus

Desktop notifications via `org.freedesktop.Notifications`.

```json
{ "type": "dbus" }
```

### ntfy

Push notifications via ntfy.sh.

```json
{
  "type": "ntfy",
  "config": {
    "server": "https://ntfy.sh",
    "topic": "my-topic",
    "token": "optional-access-token"
  }
}
```

Urgency levels are automatically mapped to ntfy priorities:
- `low` → priority 1 (min)
- `normal` → priority 3 (default)
- `critical` → priority 5 (max)

## SSH Forwarding

Forward server port through SSH:

```sshconfig
Host office
  HostName office.example.com
  User myuser
  LocalForward 8787 localhost:8787
  ExitOnForwardFailure yes
```

Then run client:
```bash
ssh office
notify-relayd --mode client --remote-host localhost:8787
```

## Protocol

Uses gRPC with the following service methods:

- `Notify` - Send notification
- `Connect` - Bidirectional stream for client-server communication
- `CloseNotification` - Close a notification
- `GetCapabilities` - Query capabilities
- `GetServerInfo` - Query server information
- `Health` - Health check

See `proto/notify_relay/v1/relay.proto` for full protocol definition.

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for:
- Building from source
- Running tests
- Protocol buffer generation
- Architecture details

## License

[Your License Here]
