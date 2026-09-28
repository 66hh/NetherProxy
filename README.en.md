# NetherProxy

**[简体中文](README.md) | English**

> **AI-assisted setup**: this repository ships a configuration guide written for AI assistants at
> [docs/skill/netherproxy-setup.md](docs/skill/netherproxy-setup.md). Paste its full content into
> your AI assistant (or provide it as a context file) to get guided configuration help covering
> the NetherNet protocol, how this proxy works, and every configuration field.

A single-port multiplexing edge proxy for the Minecraft Bedrock **NetherNet** protocol
(WebRTC: ICE + DTLS + SCTP over UDP).

Only one UDP port is exposed to the public internet for all player traffic. Neither the
internal BDS nor clients require any modification — DTLS and game-layer encryption stay
end-to-end: the proxy forwards, it never terminates.

## Features

- **Single-port multiplexing**: all players' STUN/DTLS/SCTP traffic shares one UDP port, routed by ice-ufrag and forwarded by 5-tuple
- **Multi-BDS domain routing**: routes signaling by request Host, with wildcard support
- **Multi-entry load balancing**: sessions are balanced across public entries by least-connections, with per-entry capacity limits
- **Entry health checking**: active heartbeat probes; unresponsive entries are taken offline automatically and rejoin when recovered; statistics persisted to JSON
- **Identity verification**: verifies the Microsoft-signed identity token (JWKS signature check), rejecting forged connections
- **Access control**: XUID blacklist/whitelist, webhook-based dynamic verdicts, per-player join rate limiting
- **Relay-only mode**: hides real client addresses and forces all traffic through the proxy
- **Health alert webhooks**: POST notifications when an entry/BDS goes unresponsive (optionally on recovery)
- **Control panel**: embedded web UI — session management, entry/BDS management, health history graphs, visual config editing; Chinese/English/Japanese/Korean
- **Observability**: Prometheus metrics, structured logging with rotation, persisted entry statistics

## Architecture

```
Player             NetherProxy                                  Internal BDS
 │  HTTP signaling ──► passthrough (GET/POST /v1/join) ────────────►│
 │◄──── SDP answer ─── intercept & rewrite candidate/m=/c= ─────────│
 │                     identity check · rate limit · ACL · session  │
 │═ STUN(USERNAME=ufrag) ═► route by ufrag + learn client addr ════►│
 │═ DTLS/SCTP ═══════════► forward by learned 5-tuple ═════════════►│
```

## Quick Start

```bash
go build -o netherproxy.exe ./cmd/netherproxy
./netherproxy.exe
```

On first launch a default `config.yml` is generated; edit it and restart. The startup log
prints the panel address and access token:

```
level=INFO msg="web panel ready" url=http://127.0.0.1:19130/ token=...
level=INFO msg="gateway listening" addr=0.0.0.0:19130
level=INFO msg="multiplexer listening" addr=0.0.0.0:19131
```

Add a server in the Minecraft client with `gateway-host:gateway-port`.

## Build

### Requirements

- Go 1.27.1+
- Node.js 18+ (only needed when modifying the frontend)

### Backend only

The frontend build artifact is committed to the repo
(`internal/server/gateway/web/index.html`), so a plain build is enough:

```bash
go build -o netherproxy.exe ./cmd/netherproxy
```

### Rebuild after frontend changes

```bash
cd web
npm install        # first time
npm run build      # output goes to internal/server/gateway/web/
cd ..
go build -o netherproxy.exe ./cmd/netherproxy
```

### Frontend development

```bash
cd web
npm run dev        # vite dev server, /api proxied to 127.0.0.1:19130
```

### Cross-compilation

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o netherproxy ./cmd/netherproxy

# macOS (ARM)
GOOS=darwin GOARCH=arm64 go build -o netherproxy ./cmd/netherproxy
```

## Documentation

- [Configuration reference](docs/config.md): all fields, defaults, hot-reload behavior (Chinese)
- [API reference](docs/api.md): management API, webhook contracts, signaling endpoints (Chinese)

## Control Panel

Open the gateway address in a browser and log in with `gateway.token`:

- Sessions: live list (player, traffic, state, age), kill connections, one-click block
- Entries/BDS: health status with history graphs, add/edit/delete, heartbeat configuration
- Config: type-grouped form editor with validation on save and restart-required hints

## Monitoring

With `gateway.metrics.enable` enabled, Prometheus scrapes `/api/metrics`:

- Signaling: HTTP requests, join/MOTD results by category
- Data plane: drop reasons, global bidirectional traffic
- Sessions: active count, state distribution, per-player traffic
- Entries/BDS: health status, heartbeat success rate and latency

## Testing

```bash
# End-to-end functional tests (auto-starts a mock BDS, switches config via the admin API)
python tests/run_test.py
```

## Deployment Notes

1. **The data-plane port must avoid the BDS port pool**: BDS pre-binds 19132–19139 on every
   NIC address, and the OS delivers UDP to the most specific binding — a proxy port inside
   the pool receives nothing.
2. **Never use loopback as an entry address**: client ICE stacks discard remote candidates
   pointing at 127.0.0.1. Entries must be real, reachable public/LAN addresses.
3. **Same-segment bypass**: when a client and BDS share a LAN segment, BDS can dial back the
   client address from the offer and bypass the proxy; enable `gateway.relay_only` to force
   all traffic through the proxy.
4. **Health alerts**: set `notify.enable` + `notify.url` to receive a POST webhook whenever
   an entry or BDS goes unresponsive (see [docs/api.md](docs/api.md) for the payload contract).

## Project Layout

```
cmd/netherproxy/        Entry point
internal/
  conf/                 Config: load/validate/hot-reload/atomic save
  logger/               Logging: slog + rotation
  notify/               Health alert webhooks
  session/              Session table: state machine + dual index + traffic stats
  server/
    gateway/            HTTP signaling, SDP rewriting, identity verification, admin API, panel
    multiplexer/        UDP data plane, heartbeat probes, entry statistics
tests/                  End-to-end functional tests (mock client/BDS)
docs/                   Configuration and API documentation
```

## License

See [LICENSE](LICENSE).
