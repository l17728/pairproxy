# Architecture Overview

## System Design

Pairproxy is a multi-tenant LLM proxy service that routes requests to multiple LLM backends with intelligent load balancing, health monitoring, and quota management.

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Client Applications                      │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────┐
│                    Pairproxy Service                         │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────────────────────────────────────────────┐   │
│  │              Request Handler Layer                   │   │
│  │  - Authentication (JWT, API Keys)                   │   │
│  │  - Request Validation                               │   │
│  │  - Quota Enforcement                                │   │
│  └──────────────────────────────────────────────────────┘   │
│                         │                                    │
│                         ▼                                    │
│  ┌──────────────────────────────────────────────────────┐   │
│  │           Routing & Load Balancing                   │   │
│  │  - Semantic Routing                                 │   │
│  │  - Model-based Routing                              │   │
│  │  - Weighted Load Balancing                          │   │
│  │  - Circuit Breaker                                  │   │
│  └──────────────────────────────────────────────────────┘   │
│                         │                                    │
│                         ▼                                    │
│  ┌──────────────────────────────────────────────────────┐   │
│  │         Health Monitoring & Alerting                 │   │
│  │  - Target Health Checks                             │   │
│  │  - Alert Generation                                 │   │
│  │  - Status Tracking                                  │   │
│  └──────────────────────────────────────────────────────┘   │
│                         │                                    │
│                         ▼                                    │
│  ┌──────────────────────────────────────────────────────┐   │
│  │         Backend Communication Layer                  │   │
│  │  - Protocol Conversion (OpenAI ↔ Anthropic)        │   │
│  │  - Stream Handling                                  │   │
│  │  - Error Handling & Retries                         │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                         │
        ┌────────────────┼────────────────┐
        ▼                ▼                ▼
   ┌─────────┐      ┌─────────┐      ┌─────────┐
   │ OpenAI  │      │Anthropic│      │ Custom  │
   │  API    │      │   API   │      │  LLM    │
   └─────────┘      └─────────┘      └─────────┘
```

## Core Components

### 1. Request Handler (`internal/proxy/handler.go`)

Processes incoming requests with:
- **Authentication**: JWT tokens, API keys
- **Validation**: Request format, model availability
- **Quota Checking**: Per-user/group limits
- **Logging**: Request/response tracking

### 2. Router (`internal/proxy/router.go`)

Routes requests to appropriate backends:
- **Semantic Routing**: Route based on request content
- **Model Routing**: Route based on model name
- **Load Balancing**: Distribute across healthy targets
- **Fallback**: Retry on failure

### 3. Health Monitor (`internal/lb/health.go`, `internal/lb/probe.go`)

Monitors upstream LLM target health using **Smart Probe** (v2.24.5+):

- **Smart Probe Discovery**: Automatically discovers the best health check strategy for each target on first check — no manual path configuration required
- **Strategy Cache**: Caches discovered probe strategies per target (default TTL: 2h) to avoid repeated discovery overhead; **`unreachable` is never cached** — connection-refused targets are retried on every heartbeat cycle so recovery is detected promptly
- **Two-phase Semantics**: During discovery, 401/403 = "endpoint found"; during health checks, 401/403 = "key invalid"
- **Credential Injection**: Provider-aware auth injection (`x-api-key` for Anthropic, `Authorization: Bearer` for OpenAI-compatible); whitespace-only keys treated as absent
- **Passive Circuit Breaking**: Tracks consecutive failures; marks targets unhealthy after threshold reached
- **Alert Generation**: Creates alerts on status changes
- **Recovery Detection**: Marks targets healthy again after recovery delay; recovery goroutines respond to shutdown signal within milliseconds (no blocking on the full delay at exit)

**Supported probe strategies (in priority order for generic targets):**

```
GET /health                  → 200 (vLLM, sglang, self-hosted)
GET /v1/models               → 200/401/403 (OpenAI-compatible)
POST /v1/chat/completions    → 200/401/403/400 (universal fallback)

Additional for provider=anthropic:
GET /v1/models (anthropic)   → 200/401/403/400
POST /v1/messages            → 200/401/403/400
```

### 4. Database Layer (`internal/db/`)

Persistent storage:
- **User Management**: Users, groups, authentication
- **Target Management**: LLM targets, health status
- **Usage Tracking**: Request metrics, quotas
- **Audit Logging**: Administrative actions

### 5. Protocol Conversion (`internal/proxy/protocol.go`)

Converts between API formats:
- **OpenAI → Anthropic**: Request/response transformation
- **Anthropic → OpenAI**: Response formatting
- **Stream Handling**: Streaming response conversion

## Data Flow

### Request Processing Flow

```
1. Client Request
   ↓
2. Authentication & Validation
   ├─ Check JWT/API Key
   ├─ Validate request format
   └─ Check quota
   ↓
3. Routing Decision
   ├─ Semantic routing rules
   ├─ Model availability
   └─ Load balancing
   ↓
4. Backend Selection
   ├─ Get available targets
   ├─ Apply weights
   └─ Select target
   ↓
5. Protocol Conversion (if needed)
   ├─ Convert request format
   └─ Prepare for backend
   ↓
6. Send to Backend
   ├─ HTTP request
   ├─ Handle streaming
   └─ Collect response
   ↓
7. Response Processing
   ├─ Convert response format
   ├─ Track usage
   └─ Update quotas
   ↓
8. Return to Client
```

### Health Check Flow (Smart Probe)

```
Periodic Timer (every 30s)
  ↓
For Each Target
  ├─ Has explicit health_check_path?
  │   ├─ Yes → Send request to that path with auth injection
  │   └─ No  → Smart Probe
  │              ├─ ProbeCache hit?
  │              │   ├─ unreachable → recordFailure() (no HTTP request)
  │              │   └─ has method  → CheckWithMethod() + okWithAuth()
  │              └─ Cache miss → Discover()
  │                              ├─ Try strategies in priority order
  │                              │   (okForDiscovery: 401=found)
  │                              ├─ Found → cache + CheckWithMethod() + okWithAuth()
  │                              ├─ Unreachable → cache unreachable + recordFailure()
  │                              └─ No path → recordFailure() (no cache)
  ↓
Update Status
  ├─ recordSuccess() → mark healthy, reset failure count
  └─ recordFailure() → increment count; at threshold → mark unhealthy
  ↓
Generate Alerts
  ├─ Detect status changes
  ├─ Create alert records
  └─ Notify subscribers
```

## Multi-Tenancy

### Isolation Levels

1. **User Level**: Each user has separate quotas and usage tracking
2. **Group Level**: Users grouped for shared resources
3. **Target Set Level**: Groups can have custom target sets

### Quota Management

- **Per-User Quotas**: Request limits per user
- **Per-Group Quotas**: Shared limits for groups
- **Time Windows**: Daily/monthly quota resets
- **Enforcement**: Reject requests exceeding quotas

## Scalability Considerations

### Horizontal Scaling

- **Stateless Design**: No session state in service
- **Shared Database**: All instances use same database
- **Load Balancer**: Distribute requests across instances

### Performance Optimization

- **Connection Pooling**: Reuse database connections
- **Caching**: Cache target lists, routing rules
- **Batch Operations**: Batch usage log writes
- **Async Processing**: Async health checks, alerts

## Security Architecture

### Authentication

- **JWT Tokens**: Signed tokens with expiration
- **API Keys**: Long-lived keys for service-to-service
- **Refresh Tokens**: Rotate access tokens

### Authorization

- **Role-Based Access Control**: Admin, user roles
- **Resource Ownership**: Users can only access their resources
- **Group Isolation**: Users isolated by group

### Data Protection

- **Encryption in Transit**: HTTPS/TLS
- **Password Hashing**: bcrypt with salt
- **Audit Logging**: Track all administrative actions

## Error Handling

### Retry Strategy

```
1. Transient Errors (timeout, connection refused)
   └─ Retry with exponential backoff

2. Rate Limit Errors (429)
   └─ Retry after delay

3. Permanent Errors (401, 403, 404)
   └─ Fail immediately

4. Server Errors (5xx)
   └─ Retry with circuit breaker
```

### Circuit Breaker

- **Open**: Reject requests to failing target
- **Half-Open**: Allow test request
- **Closed**: Accept requests normally

## Monitoring & Observability

### Metrics

- **Request Metrics**: Count, latency, errors
- **Target Metrics**: Health status, response times
- **Quota Metrics**: Usage, remaining quota
- **System Metrics**: CPU, memory, connections

### Logging

- **Request Logs**: All incoming requests
- **Error Logs**: Errors and exceptions
- **Audit Logs**: Administrative actions
- **Debug Logs**: Detailed operation traces

### Alerting

- **Target Down**: Alert when target becomes unhealthy
- **High Error Rate**: Alert on error threshold
- **Quota Exceeded**: Alert on quota violations
- **System Issues**: Alert on resource exhaustion

## Deployment Architecture

### Components

```
┌─────────────────────────────────────────┐
│         Load Balancer (nginx)           │
└────────────────┬────────────────────────┘
                 │
    ┌────────────┼────────────┐
    ▼            ▼            ▼
┌────────┐  ┌────────┐  ┌────────┐
│Instance│  │Instance│  │Instance│
│   1    │  │   2    │  │   3    │
└────────┘  └────────┘  └────────┘
    │            │            │
    └────────────┼────────────┘
                 │
         ┌───────▼────────┐
         │  SQLite/PG DB  │
         └────────────────┘
```

## Configuration Management

### Environment-Based

- **Development**: Local SQLite, debug logging
- **Staging**: PostgreSQL, info logging
- **Production**: PostgreSQL, error logging, monitoring

### Configuration Sources

1. **Config File** (`cproxy.yaml`): Primary configuration
2. **Environment Variables**: Override config file
3. **Command-line Flags**: Override environment

## Future Architecture Improvements

- [ ] Add caching layer (Redis)
- [ ] Implement message queue (RabbitMQ)
- [ ] Add distributed tracing (Jaeger)
- [ ] Implement service mesh (Istio)
- [ ] Add GraphQL API
- [ ] Implement webhook notifications

---

## v2.20.0 新增组件

### Group-Target Set Integration (internal/proxy)

GroupTargetSetIntegration 是代理层与 Group-Target Set 功能的集成层，封装 selector、alertManager、healthMonitor，对外提供 SelectTarget、RecordError、RecordSuccess、GetActiveAlerts、SubscribeAlerts、GetHealthStatus 接口。

### Target Alert Manager (internal/alert)

TargetAlertManager 维护内存中的活跃告警状态（activeAlerts map），通过 eventCh 异步广播告警事件给所有 SSE 订阅者。事件循环每 30s 执行一次 checkRecovery，自动解除已恢复的告警。

### Target Health Monitor (internal/alert)

TargetHealthMonitor 每 interval（默认30s）对所有 IsActive=true 的 GroupTargetSetMember 并发发起 HTTP GET health check，根据连续失败/成功次数更新数据库 health_status，并向 TargetAlertManager 发送 target_health_changed 事件。

### GroupTargetSetRepo (internal/db)

管理 group_target_sets 和 group_target_set_members 两张表。关键实现：AddMember 使用原生 SQL INSERT 绕过 GORM 零值陷阱（Bug 7），GetAvailableTargetsForGroup 对 is_active 和 group_id 做内联过滤。