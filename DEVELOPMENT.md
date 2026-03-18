# Development Guide

## Building

### Requirements

- Go 1.21 or later
- buf CLI (for protocol buffer generation)
- Protocol buffer plugins:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```

### Build All Binaries

```bash
go build ./cmd/notify-relayd
go build ./cmd/notify-send-proxy
```

### Run Tests

```bash
go test ./...
```

## Architecture

### Components

1. **notify-relayd** - The main daemon
   - Standalone mode: Local-only with router
   - Server mode: Accepts remote client connections
   - Client mode: Connects to remote server

2. **notify-send-proxy** - Drop-in notify-send replacement
   - Communicates via gRPC to local daemon
   - Same CLI interface as notify-send

3. **Protocol Buffer Definitions**
   - `proto/notify_relay/v1/relay.proto` - Service definition
   - Generated code in `internal/proto/`

### Package Structure

```
internal/
├── channel/       # Channel interface
├── dbus/          # D-Bus desktop notifications
├── ntfy/          # ntfy.sh HTTP notifications
├── router/        # Notification routing with conditions
├── remotes/       # Remote client management (gRPC)
│   ├── manager.go # Client tracking and routing
│   ├── server.go  # Server-side stream handling
│   └── client.go  # Client-side connection with reconnect
├── server/        # gRPC server implementation
├── lock/          # Screen lock detection
├── condition/     # Routing conditions
└── proto/         # Generated protobuf code
```

## Protocol Buffers

### Regenerate Code

```bash
buf generate
```

This generates Go code from `proto/notify_relay/v1/relay.proto`.

### Adding New Fields

1. Edit `proto/notify_relay/v1/relay.proto`
2. Run `buf generate`
3. Commit generated files

## Testing

### Unit Tests

```bash
go test ./internal/tests/... -v
```

### Integration Tests (Live ntfy.sh)

```bash
export TEST_NTFY_TOPIC=my-test-topic
go test ./internal/tests/... -v -run TestNtfy
```

Note: In CI (`CI=true`), live ntfy tests are skipped.

## Debugging

### Enable Verbose Logging

```bash
notify-relayd --mode server 2>&1 | tee relay.log
```

### gRPC Debugging

```bash
GRPC_GO_LOG_VERBOSITY_LEVEL=99 GRPC_GO_LOG_SEVERITY_LEVEL=info notify-relayd
```

## Common Tasks

### Adding a New Routing Condition

1. Define condition in `internal/condition/`:
   ```go
   type MyCondition struct{}
   
   func (m MyCondition) Name() string { return "my_condition" }
   func (m MyCondition) Evaluate(ctx context.Context, req protocol.NotifyRequest) bool {
       // Your logic here
       return true
   }
   ```

2. Register in router:
   ```go
   r.conditions["my_condition"] = condition.MyCondition{}
   ```

3. Use in config:
   ```json
   { "condition": "my_condition", "channel": "dbus" }
   ```

### Adding a New Channel Type

1. Implement `channel.Channel` interface
2. Add to `createChannels()` in main.go
3. Update documentation

## Release Process

1. Update version in `internal/buildinfo/buildinfo.go`
2. Update CHANGELOG.md
3. Tag release:
   ```bash
   git tag v0.3.0
   git push origin v0.3.0
   ```
4. GitHub Actions builds and releases automatically

## Architecture Decisions

### Why gRPC?

- **Streaming**: Bidirectional streams for real-time lock state updates
- **Type Safety**: Generated code prevents API mismatches
- **Performance**: Binary protocol, HTTP/2 multiplexing
- **Tooling**: Excellent debugging and load balancing support

### Why Three Modes?

- **Standalone**: Simple local use, no complexity
- **Server**: Central point for routing decisions
- **Client**: Edge nodes that report state and receive notifications

### Connection Model

- Client initiates connection to server (outbound, works through NAT)
- Persistent gRPC stream with automatic reconnect
- Lock state changes pushed in real-time
- Server makes routing decisions based on all connected clients

## Troubleshooting

### Connection Refused

Check if daemon is running:
```bash
systemctl --user status notify-relayd
# or
lsof -i :8787
```

### Authentication Failed

Verify token:
```bash
# On server
notify-relayd --token correct-token

# On client
notify-relayd --remote-host server:8787 --remote-token correct-token
```

### No Remote Forwarding

Check client connection:
```bash
# Server should show:
# "Remote client connected: laptop-work (locked: false)"

# If not, check:
# 1. Client is running
# 2. Network connectivity
# 3. Token is correct
# 4. Server is in --mode server
```
