# NetherProxy Setup Guide (for AI assistants)

You are helping a user configure **NetherProxy**, a single-port multiplexing edge proxy for
Minecraft Bedrock Edition's NetherNet protocol. This document gives you the protocol
background, the proxy's internal architecture, and the complete configuration reference.
Use it to answer configuration questions, produce `config.yml` edits, and troubleshoot
connectivity issues.

Authoritative references (same repository, Chinese): `docs/config.md` (all fields and
defaults), `docs/api.md` (management API + webhook contracts). Quote or paraphrase them
freely; this guide covers the same ground in English.

---

## 1. How NetherNet works

NetherNet is Mojang's WebRTC-based transport for Minecraft Bedrock dedicated servers:

- **Signaling over HTTP(S)**: the client POSTs an SDP offer to `https://<host>/v1/join/<networkID>`
  and receives an SDP answer. `GET /v1/join` returns the server list entry (MOTD). These
  paths are hardcoded in the client.
- **Media over UDP**: after signaling, ICE (STUN binding requests) negotiates connectivity,
  DTLS encrypts the channel, and SCTP carries the game data channels. All of this rides on
  UDP between the two SDP candidates.
- **Identity**: the offer carries an `a=identity` attribute with a Microsoft-signed JWT
  (RS256, keys from a JWKS endpoint) proving the player's Xbox identity (gamertag + XUID).

## 2. How NetherProxy works

NetherProxy sits between players and one or more internal BDS instances, exposing exactly
**one UDP port** for all player media traffic. DTLS/game encryption stays end-to-end —
the proxy forwards datagrams and never terminates encryption.

Two cooperating components:

- **Gateway (HTTP)**: terminates the signaling endpoints.
  - `GET /v1/join` is proxied to the matching BDS (optional MOTD caching to absorb server-list
    refresh storms).
  - `POST /v1/join/:networkID`: verifies the player's identity JWT (optional), enforces
    rate limits and XUID access lists / webhooks, picks an entry line (least sessions),
    rewrites the SDP answer so its only candidate points at the entry's public address, and
    registers a session keyed by the server's `ice-ufrag`.
  - Host-header routing selects which BDS receives the request (wildcard domains supported).
  - In `relay_only` mode, client candidates in the offer are replaced by an unreachable
    placeholder so BDS can never dial the client directly — all media must flow through
    the proxy.
- **Multiplexer (UDP data plane)**: one public socket (IPv4+IPv6) carrying every session.
  - STUN packets: parsed zero-allocation; the USERNAME attribute's server ufrag selects the
    session; MESSAGE-INTEGRITY is verified with the session's ice-pwd before the client
    address is (re)learned — unverified packets may forward but never re-bind the 5-tuple.
  - Non-STUN (DTLS/SCTP): forwarded only if the source 5-tuple matches a previously learned
    client address, otherwise dropped.
  - Backend replies flow through a per-session socket back to the learned client address.
  - Entry heartbeats (NPING/NPONG) are answered by the data plane itself and never reach BDS.

Supporting systems: entry/BDS health probes with automatic offlining and JSON-persisted
history, health alert webhooks (`notify`), Prometheus metrics, a token-protected admin API
under `/api`, and an embedded React control panel (served at `/`).

## 3. Configuration reference

Config file: `config.yml` (YAML), auto-generated with defaults on first launch — the
gateway token is printed to the console at startup (`gateway token: ...`) and is what the
user needs for panel login. Missing sections/keys fall back to defaults. Most fields
hot-reload via the panel or `POST /api/config/reload`; the exceptions that require a
restart are: listen addresses (`gateway.host/port`, `multiplexer.host/port`), TLS settings,
the metrics switch, and all `log` fields except `level` (format/file/rotation/buffer size).
The panel says "restart required" when saving such changes. Durations use Go syntax
(`5s`, `1m30s`).

### log

| Field | Default | Meaning |
|---|---|---|
| `level` | `info` | `debug`/`info`/`warn`/`error` (hot) |
| `format` | `text` | `text` or `json` |
| `file` | empty | Log file path; empty = console only. Rotates when set |
| `max_size` / `max_backups` / `max_age` / `compress` | `100`/`7`/`30`/`false` | Rotation: MB per file (0 = 100), kept files (0 = unlimited), kept days (0 = unlimited), gzip old files |
| `buffer_size` | `1000` | In-memory entries for the panel log viewer |

### gateway

| Field | Default | Meaning |
|---|---|---|
| `host` / `port` | `0.0.0.0` / `19130` | HTTP listen address (panel + signaling + admin API) |
| `token` | random hex | Bearer token for the admin API and panel login |
| `verify_identity` | `true` | Verify the player's Microsoft-signed identity JWT |
| `relay_only` | `false` | Hide real client addresses; force all media via the proxy |
| `motd_cache` | `0s` | MOTD cache TTL; `0s` disables caching |
| `api_auth_exempt` | `["/api/healthz"]` | Routes that skip Bearer auth. Entries are `"/api/x"` (all methods) or `"GET /api/x"` (GET only). Write routes (`PUT /api/config`, `POST /api/config/reload`, `DELETE /api/session/*`) can never be exempted; read-only `GET /api/config` may be exempted explicitly |

### gateway.tls

| Field | Default | Meaning |
|---|---|---|
| `enable` | `false` | Serve HTTPS with `cert`/`key` (PEM paths) |
| `dual` | `false` | Same port serves plain HTTP **and** HTTPS (split by first byte). Needed because Android clients require HTTP while iOS requires HTTPS |

### gateway.metrics

`enable` (default `false`) exposes Prometheus metrics at `GET /api/metrics` (auth required
unless exempted). Sample scrape config is embedded as a comment on `MetricsConf` in
`internal/conf/conf.go`.

### gateway.access — XUID access control

| Field | Meaning |
|---|---|
| `mode` | `off` / `blacklist` / `whitelist` |
| `xuids` | XUID list for the chosen mode |
| `webhook.enable` / `url` / `timeout` | On join, POST `{"xuid","xname","client_ip"}`; response HTTP 200 + `{"allow": bool, "reason": "optional, logged"}`. Fail-closed: errors, non-200, and `allow:false` all reject. Default timeout `3s` |

Access control (list modes and webhook) requires a verified identity; with
`verify_identity: false` any enabled access control rejects joins (fail-closed).
Rate limiting still works without verification — it falls back to the client IP.

### gateway.rate_limit — join throttling

`enable`, `interval` (window, default `60s`), `max_joins` (per XUID per window, default 5),
`max_keys` (tracked keys cap, default 1000). Verified players are keyed by XUID; unverified
fall back to client IP. MOTD requests are always keyed by IP.

### multiplexer

`host` / `port` (default `0.0.0.0:19131`): UDP data-plane listen address. `0.0.0.0`, `::`,
or empty listens on both IPv4 and IPv6 (separate sockets; degrades gracefully if IPv6 is
unavailable). A specific address binds only that family.

### bds — backend list (hot-reload)

```yaml
bds:
  - enable: true
    domain: "*.example.com"   # Host-header match: "*" any / "*.x" subdomains / exact; earlier entries win
    host: 127.0.0.1
    port: 19132
    heartbeat:                # HTTP GET /v1/join health probe (disabled by default)
      enable: false
      manual_only: false      # true = record stats/logs only, no auto-offline
      interval: 5s
      timeout: 2s             # must be < interval, max 30s
      retries: 3              # consecutive failures before "unresponsive"
```

### entry — public entry lines (hot-reload)

```yaml
entry:
  - enable: true
    host: 203.0.113.10        # address clients dial; must map to the multiplexer
    port: 19131
    max_session: 100          # 0 = unlimited; balancing is least-sessions,
                              # skipping full or heartbeat-offlined entries
    heartbeat:                # UDP NPING/NPONG probe, never forwarded to BDS
      enable: true            # (disabled by default; shown enabled as recommended)
      manual_only: false
      interval: 5s
      timeout: 2s
      retries: 3
```

`host` accepts IPv4/IPv6 (brackets optional) or a domain (A record preferred, AAAA
fallback). Stats persist to `entry_stats.json` and surface via `GET /api/entry` and
Prometheus.

### stats

`status_history_size` (default 500): probe history points kept per entry/BDS.
`flush_interval` (default `30s`): stats file merge interval; state changes flush immediately.

### notify — health alert webhooks (hot-reload)

Fires when an entry/BDS stays unresponsive for `retries` consecutive probes. `manual_only`
probes also fire (a human needs to know).

| Field | Default | Meaning |
|---|---|---|
| `enable` | `false` | Enable alerts |
| `url` | — | Alert receiver URL (required when enabled) |
| `timeout` | `5s` | Call timeout |
| `on_recovery` | `false` | Also notify on recovery (`entry_up`/`bds_up`) |

Payload: `POST <url>` with JSON
`{"event":"entry_down","key":"203.0.113.10:19131","time":"...","consecutive_fails":3,"error":"..."}`.
Events: `entry_down` / `entry_up` / `bds_down` / `bds_up`. Failures are logged, never
retried, and never block probing or routing.

### session — timeouts (hot-reload)

`signaled_timeout` (30s): wait for the first STUN after signaling. `active_idle_timeout`
(120s): active → idle. `idle_reap_timeout` (300s): idle → reaped; must be ≥
`active_idle_timeout`. `tuple_stale_timeout` (60s): client-address soft-state lifetime,
auto-renewed by data-plane activity.

## 4. Common tasks

- **Add a BDS**: append to `bds` with a unique `domain`; enable `heartbeat` for health
  tracking. Panel: BDS page → Add.
- **Add an entry line**: append to `entry`; set `max_session` and heartbeat. The balancer
  starts using it immediately.
- **Serve both HTTP and HTTPS**: `gateway.tls.enable: true` + `dual: true` with cert/key.
- **Hide player IPs from BDS**: `gateway.relay_only: true` (joins fail instead of leaking
  when rewriting fails — this is intentional).
- **Get downtime alerts**: `notify.enable: true`, `notify.url: <your webhook>`, optionally
  `on_recovery: true`.

## 5. Troubleshooting cheatsheet

- **Proxy receives zero UDP**: the multiplexer port collides with the BDS port pool
  (19132–19139). BDS pre-binds those ports per NIC and the OS prefers the more specific
  binding. Move `multiplexer.port` outside the pool.
- **Clients can't connect via an entry**: entry host must be a real reachable address —
  clients' ICE stacks discard loopback candidates like 127.0.0.1.
- **Numeric error codes in the client** (e.g. 37): BDS-style rejection codes are passed
  through; 37 = identity/access/rate-limit rejection — check proxy logs.
- **Same-LAN bypass**: if BDS and a client share a LAN, BDS may dial the client directly
  from the offer candidates; use `relay_only` to forbid it.
- **Panel unreachable over HTTPS in dual mode**: dual terminates TLS below the HTTP server;
  browsers negotiate HTTP/1.1 only (h2 is deliberately not advertised).
