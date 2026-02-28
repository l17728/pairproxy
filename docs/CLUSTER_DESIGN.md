# PairProxy Cluster Design

This document describes the multi-node s-proxy clustering architecture: how nodes discover each other, how routing tables are distributed to c-proxy clients, and the consistency guarantees of each mechanism.

---

## Overview

PairProxy supports two deployment modes:

| Mode | Description |
|------|-------------|
| **Single node** | One `s-proxy` instance serves all traffic. No clustering required. |
| **Multi-node** | One **primary** (`sp-1`) and one or more **workers** (`sp-2`, `sp-3`, …). Workers register with the primary via heartbeat. The primary distributes load across all nodes to c-proxy clients. |

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
| Split-brain | Not addressed: if sp-1 becomes unreachable, c-proxy will stop routing to sp-1 (health check failure) and workers will continue serving independently. Usage from the outage window will not be aggregated until sp-1 recovers. |

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
```

### Worker node (`sproxy.yaml`)

```yaml
cluster:
  role: worker
  primary: "http://sp-1:9000"    # sp-1 address for heartbeat
  self_addr: "http://sp-2:9000"  # this node's externally reachable address
  self_weight: 50                # relative weight in sp-1's load balancer
  report_interval: 30s           # heartbeat + usage upload interval
  shared_secret: "${CLUSTER_SECRET}"
```
