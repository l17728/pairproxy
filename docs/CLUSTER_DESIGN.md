# PairProxy Cluster Design

This document describes the multi-node s-proxy clustering architecture: how nodes discover each other, how routing tables are distributed to c-proxy clients, and the consistency guarantees of each mechanism.

---

## Overview

PairProxy supports three deployment modes:

| Mode | Description |
|------|-------------|
| **Single node** | One `s-proxy` instance serves all traffic. No clustering required. |
| **Multi-node (Primary+Worker)** | One **primary** (`sp-1`) and one or more **workers** (`sp-2`, `sp-3`, …). Workers register with the primary via heartbeat. The primary distributes load across all nodes to c-proxy clients. Typically used with SQLite. |
| **Peer mode (v2.14.0)** | All nodes are **equal peers** sharing a PostgreSQL database. Any node can handle management operations. Nodes discover each other via the `peers` DB table. No primary/worker distinction. |

```
                  heartbeat (POST /api/internal/register)
                  usage upload (POST /api/internal/usage)
Worker sp-2 ───────────────────────────────────────────▶ Primary sp-1
Worker sp-3 ───────────────────────────────────────────▶

c-proxy ─── request + X-Routing-Version ──────────────▶ sp-1
            ◀── response + X-Routing-Update (if stale) ─
```

---

## Node Roles

### Primary (`role: primary`)

- Accepts end-user proxy requests from c-proxy.
- Maintains a `PeerRegistry`: an in-memory table of all known worker nodes.
- Runs a periodic **eviction goroutine** (default: every 30s) that removes peers whose last heartbeat is older than `ttl` (default: 90s ≈ 3 missed heartbeats).
- On every proxy response, optionally injects routing table headers (see §Routing Table Propagation).
- Serves the internal cluster API (`/api/internal/*`).
- Optionally runs the Admin API and Dashboard.

### Worker (`role: worker`)

- Accepts end-user proxy requests independently (acts as a standalone s-proxy).
- Runs a `Reporter` goroutine that, on a configurable interval (default: 30s):
  1. **Heartbeat**: `POST /api/internal/register` on sp-1 with `{id, addr, weight}`.
  2. **Usage upload**: `POST /api/internal/usage` with accumulated `UsageRecord` rows.
- Workers collect usage records locally in SQLite and upload them to sp-1.
- Workers do **not** serve the Admin API or Dashboard.

---

## Peer Registry

`PeerRegistry` (`internal/cluster/peer_registry.go`) is an in-memory structure on the primary node. It tracks live worker peers using a `sync.Map`:

```
key: peer ID (string)
value: PeerEntry {
    ID:       "sp-2",
    Addr:     "http://sp-2:9000",
    Weight:   50,
    LastSeen: time.Time,
}
```

### Registration

Workers register on startup (immediately) and then on every heartbeat tick. The primary calls `registry.Register(id, addr, sourceNode, weight)` which upserts the entry and refreshes `LastSeen`.

### Eviction

The primary runs `registry.EvictStale(ttl)` periodically. Any peer whose `LastSeen` is older than `ttl` is removed. The default TTL is 90 seconds (3× the default 30s heartbeat interval), giving workers two missed heartbeats before eviction.

### Routing Table Generation

The `cluster.Manager` (`internal/cluster/manager.go`) wraps the `PeerRegistry` and produces a versioned `RoutingTable`. Every time the peer set changes, the Manager increments a monotonic version counter.

```go
type RoutingTable struct {
    Version int64         // monotonically increasing
    Entries []RoutingEntry
}

type RoutingEntry struct {
    ID      string
    Addr    string
    Weight  int
    Healthy bool
}
```

The routing table is also **persisted** to a local JSON file (`routing_table.json`) so that c-proxy can recover its known peers after a restart without waiting for the next heartbeat cycle.

---

## Routing Table Propagation

To avoid a separate polling mechanism, s-proxy **piggybacks** routing updates onto existing proxy responses using custom HTTP response headers.

### Headers

| Header | Direction | Description |
|--------|-----------|-------------|
| `X-Routing-Version` | c-proxy → s-proxy | Client's current routing table version |
| `X-Routing-Update` | s-proxy → c-proxy | Base64-encoded JSON routing table (only sent when newer) |

### Flow

```
c-proxy                                    sp-1 (primary)
   │                                           │
   │  POST /v1/messages                        │
   │  X-Routing-Version: 7  ───────────────▶  │
   │                                           │  clusterMgr.InjectResponseHeaders(h, 7)
   │                                           │  If Manager.version > 7:
   │                                           │    encode RoutingTable → base64
   │                                           │    set X-Routing-Version: 12
   │                                           │    set X-Routing-Update: <base64>
   │  ◀─ 200 + X-Routing-Version: 12          │
   │     X-Routing-Update: <base64> ──────────│
   │                                           │
   │  Decode base64 → RoutingTable            │
   │  balancer.UpdateTargets(entries)         │
   │  Save to routing cache file              │
```

If the client is already up to date (`clientVersion >= serverVersion`), no update headers are written, and the response has zero overhead.

### c-proxy Cache Recovery

On startup, c-proxy reads `~/.config/pairproxy/routing_table.json` (or the platform equivalent). If the file is present and non-empty, it populates the `WeightedRandomBalancer` immediately, avoiding cold-start single-target behavior.

---

## Usage Data Flow (Worker → Primary)

Workers record `UsageRecord` rows to their local SQLite database. The `Reporter` goroutine uploads these records to sp-1:

```
Worker sp-2 (SQLite)
  │
  │  reporter.loop()
  │  every 30s: ReportUsage(pending_records)
  │  POST /api/internal/usage  ────────────▶  sp-1
  │                                            │
  │                                            │  usageWriter.Record(each_record)
  │                                            │  → written to sp-1's SQLite
  │                                            │  → available in Dashboard / metrics
```

Workers do **not** delete local records after uploading — the local DB serves as an audit log and as a source for local quota checks (if the worker runs in standalone mode).

---

## Consistency Guarantees

| Property | Guarantee |
|----------|-----------|
| Routing table freshness | c-proxy receives updates within one request-response cycle after a worker registers or is evicted |
| Worker liveness detection | Workers missing 3 consecutive heartbeats (90s by default) are evicted from the routing table |
| Usage record delivery | At-least-once: records uploaded by the reporter; if sp-1 is unreachable, the worker retains records locally and retries on the next interval |
| Usage deduplication | `request_id` (UUID per request) provides idempotency in the DB insert (`INSERT OR IGNORE`) |
| Quota accuracy | Quota checks use a 60-second in-memory cache; a user may exceed their limit by at most one request window per node |
| **Config sync latency** | Worker pulls a full config snapshot from Primary every 30 s (default). User/group/target/binding changes made on Primary propagate to Workers within ~30 s. |
| **Quota enforcement (multi-node)** | **Soft limit**: quota checks run against each node's local usage DB. Cross-node aggregation is not performed in real time. In the worst case, a user can exceed their quota by up to one sync interval (30 s) worth of requests across multiple workers simultaneously. This is an acceptable trade-off for request latency. |
| **User disable propagation** | A user disabled on Primary will be marked inactive on all Workers within one sync interval (~30 s). Their refresh tokens are revoked immediately on sync. In-flight access tokens (JWT) remain valid until their TTL expires (recommended: ≤15 min). |
| **Worker write protection** | All write operations (POST/PUT/DELETE) on the Admin API and Dashboard are blocked on Worker nodes (HTTP 403 `worker_read_only`). Configuration changes must be performed on the Primary node. |
| **Worker stats scope** | Stats endpoints on a Worker node return only local data (`X-Node-Role: worker`, `X-Stats-Scope: local`). For global aggregated statistics, query the Primary node. |
| Split-brain | Partially mitigated: if sp-1 becomes unreachable, c-proxy health checker detects the failure and stops routing to sp-1. Workers continue serving traffic independently. c-proxy can also maintain routes to workers directly (see §c-proxy Resilience below). Usage from the outage window will not be aggregated until sp-1 recovers. |

---

## Sequence: New Worker Joins

```
Time 0:  Worker sp-2 starts
Time 1:  reporter.loop() fires immediately
         POST /api/internal/register {id:"sp-2", addr:"http://sp-2:9000", weight:50}
         → sp-1 PeerRegistry upserts sp-2, Manager increments version to N+1

Time 2:  Next c-proxy request to sp-1
         c-proxy sends X-Routing-Version: N
         sp-1 injects X-Routing-Update: <base64 of version N+1>
         c-proxy decodes, updates balancer → sp-2 now receives traffic
```

---

## Sequence: Worker Fails

```
Time 0:  Worker sp-2 crashes (no more heartbeats)

Time 90s: sp-1 EvictStale() runs
          sp-2 LastSeen > 90s → evicted
          Manager increments version to M+1

Time 91s: Next c-proxy request to sp-1
          c-proxy sends X-Routing-Version: M
          sp-1 injects X-Routing-Update: <base64 of version M+1 (no sp-2)>
          c-proxy removes sp-2 from balancer
```

---

## Configuration Reference

### Primary node (`sproxy.yaml`)

```yaml
cluster:
  role: primary
  self_addr: "http://sp-1:9000"
  self_weight: 50
  alert_webhook: "https://hooks.slack.com/..."   # optional
  peer_monitor_interval: 30s                     # eviction check interval
  # Shared secret for cluster internal API authentication (fail-closed).
  # Workers must present this value as a Bearer token.
  # Leave empty only on single-node deployments (internal API is never called).
  shared_secret: "${CLUSTER_SECRET}"
```

### Worker node (`sproxy.yaml`)

```yaml
cluster:
  role: worker
  primary: "http://sp-1:9000"    # sp-1 address for heartbeat
  self_addr: "http://sp-2:9000"  # this node's externally reachable address
  self_weight: 50                # relative weight in sp-1's load balancer
  report_interval: 30s           # heartbeat + usage upload interval
  # Must exactly match the primary's cluster.shared_secret.
  shared_secret: "${CLUSTER_SECRET}"
```

### c-proxy Client (`cproxy.yaml`)

```yaml
sproxy:
  # Seed node — the primary s-proxy (sp-1).
  primary: "http://sp-1:9000"

  # Static worker list — pre-seeds the balancer at startup.
  # When sp-1 is unreachable, c-proxy routes to these nodes instead.
  # See §c-proxy Resilience for the three-source merge logic.
  targets:
    - "http://sp-2:9000"
    - "http://sp-3:9000"
```

---

## c-proxy Resilience (Primary Failover)

A c-proxy that only knows the primary node (`sproxy.primary`) becomes unable to
forward requests if sp-1 is unreachable. To mitigate this, c-proxy builds its
initial load-balancer target list from **three independent sources** at startup:

| Priority | Source | Field / File |
|----------|--------|--------------|
| 1 (highest) | Config — seed node | `sproxy.primary` |
| 2 | Config — static worker list | `sproxy.targets` |
| 3 (fallback) | Disk cache — persisted routing table | `routing-cache.json` (auto-maintained) |

### Merge Algorithm

1. Addresses are collected from all three sources in priority order.
2. Deduplication by URL (`Addr` string). Config-supplied addresses always take
   priority: they are inserted first and marked `Healthy: true`.
3. Cache-only entries (addresses present in the disk cache but absent from the
   config) are appended with their persisted `Healthy` flag.
4. If no addresses are found in any source, `cproxy start` returns an error
   directing the user to add `sproxy.primary` or `sproxy.targets`.

### Why this matters

```
Scenario: sp-1 (primary) crashes at 02:00. sp-2 and sp-3 workers are healthy.

Without targets / cache:
  c-proxy only knows sp-1 → 502 on every request until sp-1 restarts.

With sproxy.targets (or routing cache):
  c-proxy balancer at startup = {sp-1 (healthy), sp-2 (healthy), sp-3 (healthy)}
  Active health check (every 30s) detects sp-1 is down → marks sp-1 Healthy:false.
  All subsequent requests route to sp-2 and sp-3 → zero user-visible downtime.
  When sp-1 recovers, health check marks it Healthy:true → traffic resumes normally.
```

## Operational Recommendation

For clusters with N s-proxy nodes, populate `sproxy.targets` in every
developer's `cproxy.yaml` with all known worker addresses. This provides
immediate failover even across c-proxy restarts, independent of the disk cache.

```yaml
sproxy:
  primary: "http://sp-1.internal:9000"
  targets:
    - "http://sp-2.internal:9000"
    - "http://sp-3.internal:9000"
```

The disk cache (`routing-cache.json`) complements `targets`: it captures
dynamically discovered nodes (e.g., workers added while c-proxy is already
running) and restores them on the next restart without manual config changes.

---

## Security: Cluster Internal API Authentication

The cluster internal API (`/api/internal/register`, `/api/internal/usage`,
`/cluster/routing`) is protected by a **shared secret** Bearer token.

### Fail-Closed Policy

The primary enforces a strict **fail-closed** policy:

| `shared_secret` value | Behavior |
|-----------------------|----------|
| Empty string (`""`) | **All requests rejected (401)** — a WARN log is emitted. This protects deployments where `shared_secret` was accidentally omitted. |
| Non-empty | Requests must carry `Authorization: Bearer <shared_secret>` exactly. Wrong or missing headers → 401. |

There is no "unauthenticated mode" for the cluster API. If you run a
single-node deployment without any workers, leave `shared_secret` empty — no
worker will ever call the internal API, so the fail-closed behavior is harmless.

### Key Requirements

1. **Generate a strong secret**: at least 32 bytes of random data.
   ```bash
   openssl rand -hex 32
   ```
2. **Use the same value** on primary and all workers (`cluster.shared_secret`).
3. **Never hard-code** the secret in config files — use environment variable
   substitution (`${CLUSTER_SECRET}`) and set the variable in your shell profile
   or systemd unit file.
4. **Rotate regularly**: update the secret on all nodes simultaneously during a
   maintenance window (workers will get 401 until restarted with the new secret).

### Network Isolation Recommendation

The cluster internal API should not be exposed to the public internet. Use
firewall rules or a private network to restrict `/api/internal/*` access to
only the nodes that need it (i.e., the workers and the primary).

---

## Peer Mode (v2.14.0)

Peer mode is designed for PostgreSQL deployments where all nodes share the same
database. It eliminates the primary/worker asymmetry by making all nodes equal
peers.

### When to Use Peer Mode

- You are using `database.driver: "postgres"` (shared PG instance)
- You want any node to be able to handle management operations (no 403 write-lock)
- You want distributed node discovery without designating a primary
- You do not need ConfigSyncer (data is already consistent via shared DB)

### Enabling Peer Mode

Peer mode is **automatically enabled** when `database.driver = "postgres"` and
`cluster.role` is not set. To enable explicitly:

```yaml
database:
  driver: "postgres"
  dsn: "host=pg.company.com user=pairproxy password=secret dbname=pairproxy sslmode=disable"
cluster:
  role: "peer"                          # explicit, or leave empty for auto-detect
  self_addr: "sproxy-1.company.com:9000"  # this node's address for peer discovery
  self_weight: 50
  shared_secret: "cluster-secret"       # still required for /cluster/routing auth
```

### PGPeerRegistry

The `PGPeerRegistry` uses the existing `peers` database table for distributed
node discovery:

```
Node startup  → Heartbeat() — UPSERT self into peers table
Every 30s     → Heartbeat() + EvictStale() — update last_seen, mark stale nodes inactive
Node shutdown → Unregister() — set own is_active = false
```

**Heartbeat (UPSERT)**: Uses `ON CONFLICT(addr) DO UPDATE` — atomic, safe for concurrent nodes.

**EvictStale**: Marks nodes with `last_seen < NOW() - 90s` as `is_active = false`.
Skips self (so a temporarily slow node doesn't evict itself).

**ListHealthy**: Returns peers where `is_active = true AND last_seen > NOW() - 90s`.

**staleTimeout**: Defaults to `3 × heartbeatInterval` (3 × 30s = 90s).

### Routing in Peer Mode

`GET /cluster/routing` (authenticated with `shared_secret`) reads healthy peers
from the `peers` table rather than from primary's in-memory registry:

```json
{
  "peers": [
    {"id": "sproxy-1:9000", "addr": "sproxy-1:9000", "weight": 50, "is_active": true},
    {"id": "sproxy-2:9000", "addr": "sproxy-2:9000", "weight": 50, "is_active": true}
  ]
}
```

C-proxy can connect to **any peer node** to pull the routing table.

### Behavior Differences vs. Primary+Worker

| Behavior | Primary+Worker | Peer Mode |
|----------|---------------|-----------|
| Write lock (worker_read_only 403) | Yes (worker nodes) | **No** |
| ConfigSyncer 30s poll | Yes (workers) | **No** (shared DB) |
| Reporter usage push | Yes (workers→primary) | **No** (direct DB write) |
| Node discovery | Primary in-memory registry | **DB `peers` table** |
| Management operations | Primary only | **Any node** |
| `/api/internal/register` | Active (workers register) | Returns 404 (not needed) |

### Backward Compatibility

- Existing `role: "primary"` configs: **unchanged**
- Existing `role: "worker"` configs: **unchanged**
- Existing PG deployments without explicit `role`: auto-upgraded to `"peer"` on
  first start with v2.14.0. Worker write-lock is removed and Reporter stops.
  Set `role: "primary"` explicitly to preserve the old behavior.


---