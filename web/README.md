# notify-relay web

Browser-based management interface for notify-relay P2P notification meshes.

## Features

- Connect to a notification mesh using only the channel key
- Auto-discover peers via DHT rendezvous (uses public IPFS bootstrap nodes by default)
- Optional direct bootstrap to your own VPS/WebSocket relay
- View peer list with presence status
- Send and receive notifications
- All communication is encrypted with XChaCha20-Poly1305 via the channel-derived key
- Uses protojson for shared types between frontend and backend

## Development

```bash
npm install
npm run dev
```

## Build

```bash
npm run build
```

## Regenerate protobuf types

After modifying `../proto/notify_relay/v1/relay.proto`:

```bash
npm run generate
```

This requires `buf` CLI and `@bufbuild/protoc-gen-es` to be installed.

## Architecture

- `src/lib/crypto.ts` — HKDF key derivation and XChaCha20-Poly1305 encryption (matches Go implementation)
- `src/lib/p2p.ts` — js-libp2p browser node with GossipSub, DHT, WebSocket/WebTransport/WebRTC transports
- `src/App.tsx` — React UI for channel setup, peer list, and notification sending
- `src/gen/` — Generated TypeScript protobuf types from the shared `.proto` file
