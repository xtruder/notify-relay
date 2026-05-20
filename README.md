# notify-relay

Peer-to-peer notification relay built on [libp2p](https://libp2p.io). Sync notifications across your desktop, laptop, and phone using a shared secret channel — no central server required.

## Overview

`notify-relay` connects your devices into a private p2p mesh. Send a notification on any device, and it's broadcast to all your other devices over an encrypted pub/sub channel. Each node decides locally whether to display the notification based on its own state (screen locked? has a display? what's its priority?).

- **Encrypted pub/sub** over libp2p GossipSub (XChaCha20-Poly1305 via channel-derived key)
- **Shared-secret channel** — a 5-word ID like `acid-bold-crew-dome-fork` is the only thing your devices need to share
- **LAN discovery** via mDNS, **WAN discovery** via IPFS DHT rendezvous through bootstrap peers
- **NAT traversal** via libp2p AutoRelay through your own VPS or public relays
- **Peer-aware routing**: each node sees who else is "present" (unlocked + has a display) and decides whether to act
- **VPS as last-resort forwarder**: route to ntfy.sh (and your phone) only when no desktop/laptop is available
- **Drop-in `notify-send` compatibility** via local gRPC API + `notify-relay` CLI

## Architecture

```
┌────────────┐         ┌────────────┐         ┌────────────┐
│  Desktop   │         │   Laptop   │         │    VPS     │
│ priority=1 │         │ priority=2 │         │ priority=50│
│ dbus       │         │ dbus       │         │ ntfy.sh    │
└─────┬──────┘         └─────┬──────┘         └─────┬──────┘
      │                      │                      │
      └──────────── encrypted GossipSub ────────────┘
                    (mDNS on LAN, DHT on WAN)

Local notification flow:
  notify-relay send "Hello"
       │
       ▼ (Unix socket gRPC)
  notify-relay daemon
       │
       ├─► local router (display via dbus if best_active)
       │
       └─► pub/sub broadcast to channel
                  │
                  ▼
       remote daemons (each runs its own router,
                       decides locally to act or skip)
```

A notification is broadcast to every node. Each node's router evaluates conditions against its peer table — only the right node(s) act. There's no leader election, no claim protocol; routing is just deterministic local logic.

## Quick Start

```bash
# Build
go build -o notify-relay ./cmd/notify-relay

# First run generates ~/.config/notify-relay.toml with a fresh channel ID:
./notify-relay serve
# → Generated channel ID: acid-bold-crew-dome-fork
# → Configure the same channel on your other devices

# On another device, edit ~/.config/notify-relay.toml and set the same channel,
# then run:
./notify-relay serve

# Send a notification (uses the local gRPC API):
./notify-relay "Hello" "from anywhere"
```

The `notify-relay` binary acts as a `notify-send`-compatible client when invoked without `serve`. Drop it in as `/usr/bin/notify-send` symlink for transparent integration.

## How Routing Works

Each node is configured with:
- A **priority number** (lower = higher priority — `1` for your main desktop, `50` for the VPS)
- **Routes**: ordered list of `(condition, channel)` pairs evaluated on every received notification

When a notification arrives, the router walks the routes in order and runs the first one whose condition is true. The available conditions:

| Condition | Matches when |
|-----------|--------------|
| `best_active` | This node is the lowest-priority-number node that is currently *present* (has a working dbus channel AND screen is unlocked). Tiebreak by peer ID. |
| `no_better_peer` | No *present* node exists anywhere in the mesh, AND this node has the highest priority number (last-resort forwarder). |
| `remote_present` | At least one remote peer is present. |
| `remote_available` | At least one remote peer is reachable. |
| `screen_locked` | Local screen is locked. |
| `always` | Always matches (use as fallback). |

**Presence** = `has_dbus && !screen_locked`. It's published over the dedicated presence pub/sub topic with a periodic heartbeat (10s) and stale timeout (30s).

### Typical setup

- **Desktop** (priority `1`): `best_active` → `dbus`. Displays notifications when unlocked.
- **Laptop** (priority `2`): `best_active` → `dbus`. Displays only when desktop is locked.
- **VPS** (priority `50`, `--relay`): `no_better_peer` → `ntfy`. Pushes to your phone when nothing else is around.

## Configuration

Config lives at `~/.config/notify-relay.toml` (TOML format with inline comments). The first `serve` run generates a default config with a fresh random channel ID.

### Minimal example

```toml
[p2p]
channel = "acid-bold-crew-dome-fork"
priority = 1

[server]
unix = "/run/user/1000/notify-relay.sock"

[channels.dbus]
type = "dbus"

[[routes]]
condition = "best_active"
channel = "dbus"
```

### Desktop + Laptop + VPS

**Desktop** (`~/.config/notify-relay.toml`):
```toml
[p2p]
channel = "acid-bold-crew-dome-fork"
priority = 1
bootstrap = ["/ip4/your-vps-ip/tcp/4001/p2p/12D3KooW..."]

[server]
unix = "/run/user/1000/notify-relay.sock"

[channels.dbus]
type = "dbus"

[[routes]]
condition = "best_active"
channel = "dbus"
```

**Laptop**: same as desktop but `priority = 2`.

**VPS** (run with `notify-relay serve --relay`):
```toml
[p2p]
channel = "acid-bold-crew-dome-fork"
priority = 50
listen = ["/ip4/0.0.0.0/tcp/4001"]

[channels.phone]
type = "ntfy"
server = "https://ntfy.sh"
topic = "my-private-topic"

[[routes]]
condition = "no_better_peer"
channel = "phone"
```

The VPS acts as both a **circuit relay** (for NAT traversal between your other devices) and a **participant** in the channel (last-resort forwarder to your phone).

## Configuration Reference

### `[p2p]`

| Field | Description |
|-------|-------------|
| `channel` | Shared secret channel ID. Must match across all your devices. |
| `priority` | Routing priority. Lower = higher priority. |
| `listen` | Multiaddrs to listen on. Defaults to `/ip4/0.0.0.0/tcp/0` (random port). |
| `identity_path` | File path for persistent ed25519 identity key. |
| `mdns` | Enable mDNS LAN discovery (default `true`). |
| `bootstrap` | Bootstrap peers for WAN discovery. Empty = use IPFS public DHT. |

### `[server]`

| Field | Description |
|-------|-------------|
| `listen` | TCP address for local gRPC API (optional). |
| `unix` | Unix socket path for local gRPC API. |
| `token` | Bearer token for `listen` mode. Auto-generated if not set. |
| `token_file` | File containing/storing the bearer token. |

### `[channels.<name>]`

| Type | Fields |
|------|--------|
| `dbus` | (none — uses `org.freedesktop.Notifications`) |
| `ntfy` | `server`, `topic`, `token` (optional) |

### `[[routes]]`

Ordered list. First matching condition wins. See conditions table above.

## CLI

### `notify-relay serve`

Start the daemon.

```
--config <path>       Config file (default: ~/.config/notify-relay.toml)
--channel <id>        Override p2p.channel
--priority <n>        Override p2p.priority
--listen <addr>       Override server.listen
--unix <path>         Override server.unix
--token <token>       Override server.token
--token-file <path>   Override server.token_file
--ntfy-topic <topic>  Add an ntfy channel + no_better_peer route
--relay               Run as circuit relay (combine with --channel for relay+participant)
--save                Persist CLI overrides to the config file
```

### `notify-relay [send args]`

Without a subcommand, acts as a `notify-send`-compatible client over the local socket.

```
notify-relay "Summary" "Optional body"
notify-relay --urgency=critical --icon=dialog-error "Build failed"
notify-relay --action=open=Open --wait "Update ready"   # wait for action reply
```

## Security Model

- Channel ID acts as a **pre-shared secret**. Anyone who knows it can join the mesh.
- Pub/sub messages are encrypted with a key derived from the channel ID via HKDF (XChaCha20-Poly1305).
- The GossipSub topic validator rejects any message that fails to decrypt, so non-members are pruned from the mesh.
- libp2p identities are ed25519 keypairs; the underlying transport is TLS/Noise.

Treat the channel ID like a password — anyone with it can both send and receive your notifications.

## Development

```bash
go build ./...
go test ./...
```

See `internal/p2p/`, `internal/router/`, and `internal/tests/network_test.go` for the core implementation and end-to-end tests covering multi-node routing scenarios (presence, priority tiebreak, relay+participant, DHT discovery, channel isolation).

## License

MIT
