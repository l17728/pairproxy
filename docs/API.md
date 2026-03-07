# PairProxy REST API Reference

All endpoints are served by `s-proxy` (default port `9000`).

---

## Authentication

### User API (`/auth/*`)

User-facing authentication endpoints used by `cproxy login` and the `c-proxy` daemon.

#### `POST /auth/login`

Authenticate with username and password. Returns a short-lived access token and a long-lived refresh token.

**Request**
```json
{
  "username": "alice",
  "password": "my-password"
}
```

**Response 200**
```json
{
  "access_token": "<JWT>",
  "refresh_token": "<UUID>",
  "expires_in": 86400,
  "token_type": "Bearer",
  "username": "alice"
}
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | Missing or malformed JSON body |
| 401 | `invalid_credentials` | Wrong username or password |
| 403 | `account_disabled` | Account has been deactivated |
| 500 | `internal_error` | Database or token generation error |

---

#### `POST /auth/refresh`

Exchange a valid refresh token for a new access token. The refresh token is **not** rotated.

**Request**
```json
{
  "refresh_token": "<UUID from /auth/login>"
}
```

**Response 200**
```json
{
  "access_token": "<new JWT>",
  "expires_in": 86400,
  "token_type": "Bearer"
}
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | Missing `refresh_token` |
| 401 | `invalid_refresh_token` | Token expired, revoked, or unknown |
| 403 | `account_disabled` | Account deactivated after token was issued |

---

#### `POST /auth/logout`

Invalidate the current session. Blacklists the access token (if provided) and revokes the refresh token.

**Headers**
```
Authorization: Bearer <access_token>   (optional but recommended)
```

**Request body** (optional)
```json
{
  "refresh_token": "<UUID>"
}
```

**Response**
```
204 No Content
```

---

## Admin API (`/api/admin/*`)

All admin endpoints (except `/api/admin/login`) require a valid admin JWT.

**Authentication**: Bearer token from `/api/admin/login`, **or** the `pairproxy_admin` cookie set by the dashboard login page.

```
Authorization: Bearer <admin_token>
```

---

### Admin Login

#### `POST /api/admin/login`

Authenticate as the admin user using the configured password hash.

**Request**
```json
{
  "password": "admin-password"
}
```

**Response 200**
```json
{
  "token": "<admin JWT>",
  "expires_in": 86400
}
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | Missing password |
| 401 | `unauthorized` | Wrong password |

---

### User Management

#### `GET /api/admin/users`

List all users, optionally filtered by group.

**Query parameters**

| Name | Type | Description |
|------|------|-------------|
| `group_id` | string | Filter by group ID (optional) |

**Response 200**
```json
{
  "users": [
    {
      "id": "uuid",
      "username": "alice",
      "group_id": "uuid-or-null",
      "is_active": true,
      "created_at": "2025-01-01T00:00:00Z",
      "last_login_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

#### `POST /api/admin/users`

Create a new user.

**Request**
```json
{
  "username": "bob",
  "password": "initial-password",
  "group_id": "group-uuid"
}
```
`group_id` is optional. Omit or set to `""` for no group (unlimited quota).

**Response 201**
```json
{ "id": "new-user-uuid" }
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | Missing username or password |
| 409 | `conflict` | Username already exists |

---

#### `PUT /api/admin/users/{id}/active`

Enable or disable a user account.

**Request**
```json
{ "active": false }
```

**Response 204** — No Content

---

#### `PUT /api/admin/users/{id}/password`

Reset a user's password.

**Request**
```json
{ "password": "new-password" }
```

**Response 204** — No Content

---

### Group Management

#### `GET /api/admin/groups`

List all groups with their current quota settings.

**Response 200**
```json
{
  "groups": [
    {
      "id": "uuid",
      "name": "engineering",
      "daily_token_limit": 100000,
      "monthly_token_limit": 2000000,
      "requests_per_minute": 20,
      "max_tokens_per_request": 4096,
      "concurrent_requests": 5,
      "created_at": "2025-01-01T00:00:00Z"
    }
  ]
}
```

All limit fields are `null` when unlimited.

| Field | Description |
|-------|-------------|
| `daily_token_limit` | Daily token cap (input + output combined). `null` = unlimited |
| `monthly_token_limit` | Monthly token cap. `null` = unlimited |
| `requests_per_minute` | Per-user RPM limit. `null` = unlimited |
| `max_tokens_per_request` | Maximum `max_tokens` value allowed in a single request. `null` = unlimited |
| `concurrent_requests` | Maximum number of simultaneous in-flight requests per user. `null` = unlimited |

---

#### `POST /api/admin/groups`

Create a new group.

**Request**
```json
{
  "name": "trial",
  "daily_token_limit": 10000,
  "monthly_token_limit": 200000,
  "requests_per_minute": 10,
  "max_tokens_per_request": 4096,
  "concurrent_requests": 2
}
```

All limit fields are optional. Omit or set to `null` for unlimited.

**Response 201**
```json
{ "id": "new-group-uuid" }
```

---

#### `PUT /api/admin/groups/{id}/quota`

Update quota limits for an existing group.

**Request**
```json
{
  "daily_token_limit": 50000,
  "monthly_token_limit": null,
  "requests_per_minute": 30,
  "max_tokens_per_request": 8192,
  "concurrent_requests": 3
}
```

Set a field to `null` to remove that limit (unlimited). Omitting a field leaves it unchanged.

**Response 204** — No Content

---

#### `DELETE /api/admin/groups/{id}`

Delete a group. Users currently in the group are moved to no group (unlimited quota).

**Response 204** — No Content

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | Group not found |
| 409 | `conflict` | Cannot delete group with active users |

---

#### `PUT /api/admin/users/{id}/group`

Assign a user to a group (or remove from group by setting `group_id` to `null`).

**Request**
```json
{ "group_id": "group-uuid" }
```

Set `group_id` to `null` to remove the user from their current group.

**Response 204** — No Content

---

#### `POST /api/admin/users/{id}/revoke-tokens`

Immediately revoke all active tokens (access + refresh) for a user. Use this when disabling a user or responding to a security incident.

**Response 204** — No Content

---

### Statistics

#### `GET /api/admin/stats/summary`

Global token and cost summary for a time range.

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `from` | start of today | ISO 8601 timestamp |
| `to` | now | ISO 8601 timestamp |

**Response 200**
```json
{
  "total_input_tokens": 1234567,
  "total_output_tokens": 345678,
  "total_tokens": 1580245,
  "request_count": 420,
  "error_count": 3,
  "cost_usd": 12.34
}
```

---

#### `GET /api/admin/stats/users`

Per-user token usage breakdown.

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `from` | start of today | ISO 8601 timestamp |
| `to` | now | ISO 8601 timestamp |
| `limit` | `50` | Max rows returned |

**Response 200**
```json
{
  "rows": [
    {
      "user_id": "uuid",
      "username": "alice",
      "total_tokens": 98000,
      "request_count": 42
    }
  ]
}
```

---

#### `GET /api/admin/stats/logs`

Paginated request log entries.

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `user_id` | (all users) | Filter by user ID |
| `limit` | `100` | Max rows returned |

**Response 200**
```json
{
  "logs": [
    {
      "id": 1001,
      "request_id": "uuid",
      "user_id": "uuid",
      "model": "claude-3-5-sonnet-20241022",
      "input_tokens": 512,
      "output_tokens": 128,
      "status_code": 200,
      "duration_ms": 1230,
      "cost_usd": 0.0032,
      "is_streaming": true,
      "upstream_url": "https://api.anthropic.com",
      "created_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

### Quota Management

#### `GET /api/admin/quota/status`

Query current quota usage for a user or group.

**Query parameters**

| Name | Required | Description |
|------|----------|-------------|
| `user` | no | Username to query |
| `group` | no | Group name to query |

Exactly one of `user` or `group` must be provided.

**Response 200**
```json
{
  "daily_used": 12345,
  "daily_limit": 50000,
  "monthly_used": 234567,
  "monthly_limit": 1000000,
  "rpm_limit": 10
}
```

Limit fields are `null` when unlimited.

---

### Audit & Logs

#### `GET /api/admin/audit`

List recent admin operations (user/group changes, quota updates, etc.).

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `limit` | `100` | Max entries returned |

**Response 200**
```json
{
  "entries": [
    {
      "id": 1,
      "admin_user": "admin",
      "action": "create_user",
      "target": "alice",
      "details": "{\"group\":\"trial\"}",
      "created_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

#### `DELETE /api/admin/logs`

Purge old request logs.

**Query parameters**

| Name | Required | Description |
|------|----------|-------------|
| `before` | no | Delete logs before this date (ISO 8601) |
| `days` | no | Delete logs older than N days |

Exactly one of `before` or `days` must be provided.

**Response 204** — No Content

---

#### `GET /api/admin/export`

Export request logs in CSV or JSON format.

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `format` | `json` | Output format: `json` or `csv` |
| `from` | start of today | Start timestamp (ISO 8601) |
| `to` | now | End timestamp (ISO 8601) |
| `user_id` | (all) | Filter by user ID |

**Response 200** — Returns file download with appropriate Content-Type.

---

### Drain Mode

#### `POST /api/admin/drain`

Enter drain mode (reject new requests, allow in-flight to complete).

**Response 204** — No Content

---

#### `POST /api/admin/undrain`

Exit drain mode (resume accepting requests).

**Response 204** — No Content

---

#### `GET /api/admin/drain/status`

Check current drain mode status.

**Response 200**
```json
{
  "draining": false,
  "active_requests": 3
}
```

---

### API Key Management

#### `GET /api/admin/api-keys`

List all configured API keys (values are masked).

**Response 200**
```json
{
  "keys": [
    {
      "id": "uuid",
      "name": "anthropic-prod",
      "provider": "anthropic",
      "masked_value": "sk-ant-***xyz",
      "is_active": true,
      "assigned_to": "user-uuid",
      "created_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

#### `POST /api/admin/api-keys`

Create a new API key.

**Request**
```json
{
  "name": "anthropic-backup",
  "provider": "anthropic",
  "value": "sk-ant-api03-..."
}
```

**Response 201**
```json
{
  "id": "uuid",
  "name": "anthropic-backup"
}
```

---

#### `POST /api/admin/api-keys/{id}/assign`

Assign an API key to a specific user or group.

**Request**
```json
{
  "user_id": "uuid"
}
```

Or:
```json
{
  "group_id": "uuid"
}
```

**Response 204** — No Content

---

#### `DELETE /api/admin/api-keys/{id}`

Revoke an API key.

**Response 204** — No Content

---

### LLM Target Management

#### `GET /api/admin/llm/targets`

List all configured LLM targets with health status.

**Response 200**
```json
{
  "targets": [
    {
      "id": "https://api.anthropic.com",
      "name": "Primary",
      "provider": "anthropic",
      "healthy": true,
      "weight": 100,
      "bound_users": 5,
      "bound_groups": 2
    }
  ]
}
```

---

#### `GET /api/admin/llm/bindings`

List all user/group LLM bindings.

**Response 200**
```json
{
  "bindings": [
    {
      "id": "uuid",
      "user_id": "uuid",
      "username": "alice",
      "target_url": "https://api.anthropic.com",
      "created_at": "2025-06-01T12:00:00Z"
    },
    {
      "id": "uuid",
      "group_id": "uuid",
      "group_name": "premium",
      "target_url": "https://api.anthropic.com",
      "created_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

#### `POST /api/admin/llm/bindings`

Create a new LLM binding for a user or group.

**Request**
```json
{
  "user_id": "uuid",
  "target_url": "https://api.anthropic.com"
}
```

Or:
```json
{
  "group_id": "uuid",
  "target_url": "https://api.anthropic.com"
}
```

**Response 201**
```json
{
  "id": "uuid"
}
```

---

#### `DELETE /api/admin/llm/bindings/{id}`

Delete an LLM binding.

**Response 204** — No Content

---

#### `POST /api/admin/llm/distribute`

Automatically distribute all active users evenly across all healthy LLM targets.

**Response 200**
```json
{
  "distributed": 42,
  "targets": 3
}
```

---

## Conversation Tracking CLI (`sproxy admin track`)

> **v2.4.0+** — 按用户粒度记录 LLM 对话内容（messages + 助手回复）到磁盘 JSON 文件。
> 仅 CLI 操作，无 REST API。对话文件存储在数据库文件同级的 `track/` 目录下。

### Enable Tracking

```bash
sproxy admin track enable <username>
```

为指定用户启用对话跟踪。启用后该用户的所有后续请求均被记录，存量请求不受影响。幂等（多次调用无副作用）。

**Exit codes**: `0` 成功，`1` 用户名非法（含路径遍历字符）

---

### Disable Tracking

```bash
sproxy admin track disable <username>
```

禁用指定用户的对话跟踪。已写入的历史记录不会被删除。幂等（用户不存在时也返回成功）。

---

### List Tracked Users

```bash
sproxy admin track list
```

列出当前所有处于跟踪状态的用户名。

**Output example**:
```
Tracked users (2):
  alice
  bob
```

若无跟踪用户，输出：
```
No users are currently being tracked.
```

---

### Show Conversation Records

```bash
sproxy admin track show <username>
```

列出指定用户的所有对话记录文件（按时间倒序，含文件大小）。

**Output example**:
```
Conversations for alice [tracking: ENABLED] — 3 record(s):
  2026-03-07T13-05-22Z-req-abc123.json   (2.1 KB)
  2026-03-07T12-31-09Z-req-def456.json   (1.8 KB)
  2026-03-07T11-47-53Z-req-ghi789.json   (3.4 KB)
```

**Record JSON format** (单条文件内容):
```json
{
  "request_id": "ca0e1b3b-bc75-4a7d-9925-2cda8cf2b318",
  "username": "alice",
  "timestamp": "2026-03-07T13:05:22Z",
  "provider": "anthropic",
  "model": "claude-3-opus",
  "messages": [
    { "role": "user",      "content": "Hello Claude" },
    { "role": "assistant", "content": "Sure" },
    { "role": "user",      "content": "Thanks" }
  ],
  "response": "You're welcome!",
  "input_tokens": 15,
  "output_tokens": 7
}
```

| 字段 | 说明 |
|------|------|
| `request_id` | 请求唯一 ID（UUID） |
| `username` | 用户名 |
| `timestamp` | 请求时间（UTC） |
| `provider` | `"anthropic"` / `"openai"` / `"ollama"` |
| `model` | 请求中指定的模型名（可能为空） |
| `messages` | 请求中的 messages 列表（已展开 content block） |
| `response` | 助手回复全文（流式自动累积，非流式从响应体提取） |
| `input_tokens` | 输入 token 数（来自响应 usage 字段） |
| `output_tokens` | 输出 token 数 |

---

### Clear Conversation Records

```bash
sproxy admin track clear <username>
```

删除指定用户的**所有**对话记录文件。跟踪状态不受影响（已启用的继续记录新对话）。

> ⚠️ 此操作不可逆，删除前请确认或先备份。

---

### File Storage Layout

```
<db_dir>/track/
├── users/
│   ├── alice          # 空标记文件，存在即表示追踪已启用
│   └── bob
└── conversations/
    ├── alice/
    │   ├── 2026-03-07T13-05-22Z-<reqID>.json
    │   └── 2026-03-07T12-31-09Z-<reqID>.json
    └── bob/
        └── 2026-03-07T11-00-00Z-<reqID>.json
```

文件命名格式：`<UTC时间戳>-<requestID>.json`，字典序即时间序，便于 `ls`/`sort` 排序。

---

## Dashboard API (`/api/dashboard/*`)

Dashboard 数据接口，用于概览页面的趋势图表。需要 admin 认证。

**Authentication**: Bearer token (admin JWT) 或 `pairproxy_admin` cookie。

---

#### `GET /api/dashboard/trends`

返回趋势数据，用于 Dashboard 概览页的图表展示。

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `days` | `7` | 时间范围（天），最大 365 |

**Response 200**
```json
{
  "daily_tokens": [
    {
      "date": "2025-03-01",
      "input_tokens": 123456,
      "output_tokens": 34567,
      "total_tokens": 158023,
      "request_count": 42
    }
  ],
  "daily_cost": [
    {
      "date": "2025-03-01",
      "cost_usd": 1.23
    }
  ],
  "top_users": [
    {
      "user_id": "uuid",
      "total_input": 50000,
      "total_output": 15000,
      "request_count": 20
    }
  ]
}
```

- `daily_tokens`: 按日期聚合的 token 用量
- `daily_cost`: 按日期聚合的费用（USD）
- `top_users`: Top 5 用户按 token 总量排序

---

## User API (`/api/user/*`)

用户自助服务接口，普通用户可访问（无需 admin 角色）。需要有效的用户 JWT。

**Authentication**: `Authorization: Bearer <user_jwt>`

---

#### `GET /api/user/quota-status`

返回当前用户的配额状态（已用 / 限额）。

**Response 200**
```json
{
  "daily_limit": 50000,
  "daily_used": 12345,
  "monthly_limit": 1000000,
  "monthly_used": 234567,
  "rpm_limit": 10
}
```

| Field | Description |
|-------|-------------|
| `daily_limit` | 日配额上限，`0` 表示无限制 |
| `daily_used` | 今日已用 token |
| `monthly_limit` | 月配额上限，`0` 表示无限制 |
| `monthly_used` | 本月已用 token |
| `rpm_limit` | 每分钟请求限制，`0` 表示无限制 |

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 401 | `unauthorized` | 缺少或无效的 JWT |
| 404 | `not_found` | 用户不存在 |

---

#### `GET /api/user/usage-history`

返回当前用户的每日 token 用量历史。

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `days` | `30` | 回溯天数（最大 365） |

**Response 200**
```json
{
  "history": [
    {
      "date": "2025-03-01",
      "input_tokens": 12345,
      "output_tokens": 3456,
      "total_tokens": 15801,
      "request_count": 12
    },
    {
      "date": "2025-03-02",
      "input_tokens": 23456,
      "output_tokens": 5678,
      "total_tokens": 29134,
      "request_count": 18
    }
  ]
}
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 401 | `unauthorized` | 缺少或无效的 JWT |
| 500 | `internal_error` | 数据库查询失败 |

---

## Internal Cluster API (`/api/internal/*`)

Used by worker `s-proxy` nodes to communicate with the primary node. Requests must carry the shared secret:

```
Authorization: Bearer <shared_secret>
```

> ⚠️ These endpoints are not intended for external clients.

---

#### `POST /api/internal/register`

Worker node heartbeat. The primary registers the peer and updates its routing table.

**Request**
```json
{
  "id": "sp-2",
  "addr": "http://sp-2:9000",
  "weight": 50,
  "source_node": "sp-2"
}
```

**Response 200**
```json
{ "status": "ok" }
```

---

#### `POST /api/internal/usage`

Worker node batch-uploads usage records collected locally.

**Request**
```json
{
  "source_node": "sp-2",
  "records": [
    {
      "request_id": "uuid",
      "user_id": "uuid",
      "model": "claude-3-5-sonnet-20241022",
      "input_tokens": 512,
      "output_tokens": 128,
      "status_code": 200,
      "duration_ms": 980,
      "created_at": "2025-06-01T12:00:00Z"
    }
  ]
}
```

**Response 200**
```json
{ "status": "ok" }
```

---

#### `GET /cluster/routing`

Returns the current peer routing table (useful for debugging).

**Response 200**
```json
{
  "peers": [
    {
      "id": "sp-2",
      "addr": "http://sp-2:9000",
      "weight": 50,
      "last_seen": "2025-06-01T12:00:00Z"
    }
  ]
}
```

---

## Observability

#### `GET /metrics`

Prometheus-format metrics. No authentication required (restrict via network policy).

```
# HELP pairproxy_tokens_total Total tokens processed
# TYPE pairproxy_tokens_total counter
pairproxy_tokens_today{type="input"} 1234567
pairproxy_tokens_today{type="output"} 345678

# HELP pairproxy_requests_today Total requests today
# TYPE pairproxy_requests_today gauge
pairproxy_requests_today{status="success"} 417
pairproxy_requests_today{status="error"} 3

# HELP pairproxy_active_users_today Unique users with at least one request today
# TYPE pairproxy_active_users_today gauge
pairproxy_active_users_today 12

# HELP pairproxy_cost_usd_today Estimated LLM cost today in USD
# TYPE pairproxy_cost_usd_today gauge
pairproxy_cost_usd_today 12.345678

# HELP pairproxy_tokens_month Total tokens this calendar month
# TYPE pairproxy_tokens_month gauge
pairproxy_tokens_month{type="input"} 12000000
pairproxy_tokens_month{type="output"} 3500000

# HELP pairproxy_requests_month Total requests this calendar month
# TYPE pairproxy_requests_month gauge
pairproxy_requests_month{status="success"} 8100
pairproxy_requests_month{status="error"} 47
```

Metrics are cached for 30 seconds to avoid excessive DB queries.

---

#### `GET /health`

Liveness probe. No authentication required.

**Response 200**
```json
{ "status": "ok", "service": "sproxy" }
```

---

## Error Response Format

All JSON error responses use the following envelope:

```json
{
  "error": "<machine-readable code>",
  "message": "<human-readable description>"
}
```

### Quota Exceeded (429)

When a user exceeds their daily or monthly token quota, or request rate limit:

```json
{
  "error": "quota_exceeded",
  "kind": "daily",
  "current": 100500,
  "limit": 100000,
  "reset_at": "2025-06-02T00:00:00Z"
}
```

`kind` is one of `"daily"`, `"monthly"`, or `"rate_limit"`.
`reset_at` indicates when the quota window resets.

The following headers are also included:

```
X-RateLimit-Limit: 100000
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1748822400
X-RateLimit-Kind: daily
```
