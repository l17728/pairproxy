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
      "created_at": "2025-01-01T00:00:00Z"
    }
  ]
}
```

Fields `daily_token_limit`, `monthly_token_limit`, and `requests_per_minute` are `null` when unlimited.

---

#### `POST /api/admin/groups`

Create a new group.

**Request**
```json
{
  "name": "trial",
  "daily_token_limit": 10000,
  "monthly_token_limit": 200000,
  "requests_per_minute": 10
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
  "requests_per_minute": 30
}
```

Set a field to `null` to remove that limit (unlimited). Omitting a field leaves it unchanged.

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
