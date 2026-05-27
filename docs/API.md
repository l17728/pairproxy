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

#### `POST /api/admin/llm/targets`

> **v2.7.0+** — 动态添加新 LLM Target，无需重启服务。

**Request**
```json
{
  "url": "https://api.example.com",
  "api_key": "sk-...",
  "provider": "anthropic",
  "name": "新节点",
  "weight": 1
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `url` | ✅ | Target 的唯一标识 URL |
| `api_key` | ✅ | 调用该上游时使用的 API Key |
| `provider` | ✅ | `"anthropic"` / `"openai"` / `"ollama"` |
| `name` | ❌ | 可读名称（留空使用 URL） |
| `weight` | ❌ | 负载权重，默认 `1` |

**Response 201**
```json
{ "id": "uuid" }
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | 缺少必填字段或 URL 格式非法 |
| 409 | `conflict` | 相同 URL 的 Target 已存在 |

---

#### `PUT /api/admin/llm/targets/{url}/enable`

> **v2.7.0+** — 启用指定 Target（URL 需 URLEncode）。已禁用的 Target 重新加入路由。

**Path parameter**: `url` — Target URL，需进行 URL 编码（`%3A`、`%2F` 等）。

**Response 204** — No Content

---

#### `PUT /api/admin/llm/targets/{url}/disable`

> **v2.7.0+** — 禁用指定 Target，不删除配置，停止向其路由新请求。

**Response 204** — No Content

---

#### `PUT /api/admin/llm/targets/{url}`

> **v2.7.0+** — 更新 Target 配置（部分更新，仅传入需修改的字段）。

**Request**（各字段均为可选）
```json
{
  "api_key": "sk-new-key",
  "weight": 2,
  "name": "新名称"
}
```

**Response 204** — No Content

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | Target URL 不存在 |

---

#### `DELETE /api/admin/llm/targets/{url}`

> **v2.7.0+** — 删除指定 Target。默认拒绝删除仍有 binding 的 Target，可使用 `force=true` 强制解除所有绑定后删除。

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `force` | `false` | 设为 `true` 则先解除全部用户/分组绑定再删除 |

**Response 204** — No Content

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | Target URL 不存在 |
| 409 | `conflict` | Target 仍有活跃绑定且未设置 `force=true` |

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

---

## Bulk Import API (`/api/admin/import`) <small>v2.8.0+</small>

#### `POST /api/admin/import`

从模板文件（text/plain 格式）批量导入用户和分组。支持 `dry_run` 预览模式，不写入数据库。

**Authentication**: Bearer token (admin JWT)

**Headers**
```
Content-Type: text/plain
```

**Query parameters**

| Name | Default | Description |
|------|---------|-------------|
| `dry_run` | `false` | 设为 `true` 时仅预览结果，不实际写入 |

**Request body** — 模板文件内容（与 `sproxy admin import` CLI 使用的格式相同）

```
group:engineering
  alice  password123
  bob    password456

group:trial
  carol  password789
```

**Response 200**
```json
{
  "groups_created": 2,
  "users_created": 5,
  "skipped": 1,
  "dry_run": false
}
```

| Field | Description |
|-------|-------------|
| `groups_created` | 新建的分组数量 |
| `users_created` | 新建的用户数量 |
| `skipped` | 跳过的条目数（用户名已存在等） |
| `dry_run` | 是否为预览模式（与请求参数一致） |

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | 模板格式解析失败 |
| 422 | `validation_error` | 模板内容存在业务约束冲突 |

---

## Direct Proxy / Keygen API (`/keygen/*`) <small>v2.9.0+</small>

> **v2.9.0+** — 为普通用户签发 `sk-pp-` 格式的 API Key，用户无需运行 `cproxy` 守护进程，可直接以 API Key 访问 PairProxy，与 OpenAI / Anthropic SDK 兼容。
>
> **v2.15.0** — Keygen 算法升级为 HMAC-SHA256，替换旧版指纹算法，同一用户每次调用返回确定性相同的密钥。
>
> **v2.24.7** — Key 派生由共享 `keygen_secret` 改为 per-user `PasswordHash`；新增用户自助改密码端点 `POST /keygen/api/change-password`。

---

#### `GET /keygen/{username}`

为指定用户生成（或重新获取）`sk-pp-` 前缀的 API Key。

**Authentication**: Bearer token (admin JWT)

**Path parameter**: `username` — 目标用户名（须已存在于数据库中）

**Response 200**
```json
{
  "api_key": "sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "username": "alice",
  "expires_at": null
}
```

| Field | Description |
|-------|-------------|
| `api_key` | 生成的 API Key，`sk-pp-` 前缀，由用户 PasswordHash 派生（v2.24.7+） |
| `username` | 对应的用户名 |
| `expires_at` | 过期时间（`null` 表示永不过期） |

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | 用户名不存在 |
| 403 | `account_disabled` | 用户账户已被禁用 |

---

#### `POST /keygen/verify`

验证一个 `sk-pp-` API Key 是否有效，并返回对应的用户名。

**Request**
```json
{
  "api_key": "sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
}
```

**Response 200**
```json
{
  "valid": true,
  "username": "alice"
}
```

若 Key 无效或用户已被禁用：
```json
{
  "valid": false,
  "username": ""
}
```

---

#### `POST /keygen/api/change-password` <small>v2.24.7+</small>

用户自助修改密码，并获取由新密码派生的 API Key。修改成功后旧 Key 立即失效。

**Authentication**: `Authorization: Bearer <session_token>`（登录 Keygen WebUI 后获得）

**Request**
```json
{
  "old_password": "current-password",
  "new_password": "new-strong-password"
}
```

**Response 200**
```json
{
  "key": "sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "message": "密码已更新，新 API Key 已生成"
}
```

| Field | Description |
|-------|-------------|
| `key` | 新 API Key，由新 PasswordHash 派生，立即可用 |
| `message` | 操作成功提示 |

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | 请求体解析失败、字段为空、新旧密码相同 |
| 401 | `unauthorized` | session_token 无效或已过期 |
| 401 | `invalid_password` | 旧密码验证失败 |
| 403 | `forbidden` | LDAP 账户不支持本地密码修改 |
| 503 | `worker_node` | 当前节点为 Worker，写操作须转发至 Primary |

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
<track.dir>/
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

## Alert Stream API (`/api/admin/alerts/stream`) <small>v2.8.0+</small>

#### `GET /api/admin/alerts/stream`

以 SSE（Server-Sent Events）方式实时推送 `WARN` 及以上级别的日志事件。连接保持打开，服务端持续推送；客户端断开后自动清理订阅。

**Authentication**: Bearer token (admin JWT)

**Response headers**
```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

**Event format**（每条日志一个 SSE 事件）
```
data: {"level":"WARN","msg":"upstream latency spike","ts":"2026-03-22T00:00:00Z","fields":{"target":"https://api.anthropic.com","latency_ms":4200}}

data: {"level":"ERROR","msg":"upstream returned 529","ts":"2026-03-22T00:01:05Z","fields":{"target":"https://api.openai.com","status":529}}
```

| Field | Description |
|-------|-------------|
| `level` | 日志级别：`"WARN"` 或 `"ERROR"` |
| `msg` | 日志消息正文 |
| `ts` | 事件时间戳（UTC，ISO 8601） |
| `fields` | 结构化附加字段（随事件类型不同而变化） |

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 401 | `unauthorized` | 缺少或无效的 admin JWT |

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

**Response 200**（v2.7.0 起响应包含更多运行状态字段）
```json
{
  "status": "ok",
  "service": "sproxy",
  "version": "2.18.0",
  "uptime_seconds": 86400,
  "active_requests": 3,
  "usage_queue_depth": 12,
  "db_type": "sqlite",
  "draining": false
}
```

| Field | Description |
|-------|-------------|
| `status` | 始终为 `"ok"`（服务存活） |
| `service` | 服务标识，固定为 `"sproxy"` |
| `version` | 当前运行版本号 |
| `uptime_seconds` | 服务启动至今的运行秒数 |
| `active_requests` | 当前正在处理的请求数 |
| `usage_queue_depth` | 内部用量上报队列深度 |
| `db_type` | 数据库类型，`"sqlite"` 或 `"postgres"` |
| `draining` | 是否处于 drain 模式 |

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

---

## Semantic Router API (`/api/admin/semantic-router/*`) <small>v2.18.0</small>

> **v2.18.0** — 语义路由功能通过外部分类器服务（默认监听 `:9000`）对请求内容进行意图分类，并将请求路由至匹配规则所指定的 LLM Target。规则持久化存储于数据库，优先级高的规则优先匹配。

**Authentication**: Bearer token (admin JWT)

---

#### `GET /api/admin/semantic-router/rules`

列出所有语义路由规则。

**Response 200**
```json
{
  "rules": [
    {
      "id": "uuid",
      "name": "code-tasks",
      "description": "编程、调试、代码审查类任务",
      "targets": ["https://api.anthropic.com"],
      "priority": 10,
      "enabled": true,
      "source": "db"
    }
  ]
}
```

| Field | Description |
|-------|-------------|
| `id` | 规则唯一 ID（UUID） |
| `name` | 规则名称（唯一） |
| `description` | 规则描述，供分类器理解匹配意图 |
| `targets` | 匹配后路由至的 LLM Target URL 列表（轮询） |
| `priority` | 优先级，数值越大越先匹配 |
| `enabled` | 是否启用 |
| `source` | `"db"`（来自数据库）或 `"config"`（来自配置文件，只读） |

---

#### `POST /api/admin/semantic-router/rules`

创建新的语义路由规则。

**Request**
```json
{
  "name": "code-tasks",
  "description": "编程、调试、代码审查类任务",
  "targets": ["https://api.anthropic.com"],
  "priority": 10
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | ✅ | 规则名称，须唯一 |
| `description` | ✅ | 分类器用于意图匹配的描述文本 |
| `targets` | ✅ | 至少包含一个 Target URL |
| `priority` | ❌ | 优先级，默认 `0` |

**Response 201**
```json
{ "id": "uuid" }
```

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 400 | `invalid_request` | 缺少必填字段或 targets 为空 |
| 409 | `conflict` | 同名规则已存在 |

---

#### `PUT /api/admin/semantic-router/rules/{id}`

更新指定规则（部分更新，仅传入需修改的字段）。

**Path parameter**: `id` — 规则 UUID

**Request**（各字段均为可选）
```json
{
  "description": "更新后的描述",
  "targets": ["https://api.anthropic.com", "https://api.openai.com"],
  "priority": 5,
  "enabled": false
}
```

**Response 204** — No Content

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | 规则 ID 不存在 |

---

#### `DELETE /api/admin/semantic-router/rules/{id}`

删除指定规则。

**Response 204** — No Content

**Error responses**

| Status | `error` code | Reason |
|--------|-------------|--------|
| 404 | `not_found` | 规则 ID 不存在 |

---

#### `POST /api/admin/semantic-router/rules/{id}/enable`

启用指定规则（幂等）。

**Response 204** — No Content

---

#### `POST /api/admin/semantic-router/rules/{id}/disable`

禁用指定规则（幂等）。禁用后该规则不参与路由匹配，但不会被删除。

**Response 204** — No Content

---

#### `GET /api/admin/semantic-router/status`

查看语义路由系统当前运行状态。

**Response 200**
```json
{
  "enabled": true,
  "classifier_url": "http://sproxy.internal:9000",
  "rules_count": 5,
  "fallback_count": 12,
  "last_classification_ms": 45
}
```

| Field | Description |
|-------|-------------|
| `enabled` | 语义路由功能是否全局启用 |
| `classifier_url` | 分类器服务地址 |
| `rules_count` | 当前已加载的启用规则数量 |
| `fallback_count` | 自上次重启以来因无规则匹配而走 fallback 的请求数 |
| `last_classification_ms` | 最近一次分类请求耗时（毫秒） |

---

## Semantic Router CLI (`sproxy admin semantic-router`) <small>v2.18.0</small>

> **v2.18.0** — 通过 CLI 管理语义路由规则，与 REST API 功能一一对应。

```bash
# 列出所有语义路由规则
sproxy admin semantic-router list

# 创建新规则
sproxy admin semantic-router add \
  --name "code-tasks" \
  --description "编程、调试、代码审查类任务" \
  --targets "https://api.anthropic.com,https://api.openai.com" \
  --priority 10

# 更新规则（部分更新，仅修改传入的字段）
sproxy admin semantic-router update <id> --priority 5 --enabled false

# 删除规则
sproxy admin semantic-router delete <id>

# 启用 / 禁用规则
sproxy admin semantic-router enable <id>
sproxy admin semantic-router disable <id>

# 查看运行状态
sproxy admin semantic-router status
```

| 子命令 | 说明 |
|--------|------|
| `list` | 列出所有规则（含 ID、名称、优先级、启用状态） |
| `add` | 创建新规则 |
| `update <id>` | 部分更新指定规则 |
| `delete <id>` | 删除规则 |
| `enable <id>` | 启用规则 |
| `disable <id>` | 禁用规则 |
| `status` | 查看语义路由系统运行状态 |

---

## Corpus Collection CLI (`sproxy admin corpus`) <small>v2.16.0+</small>

> **v2.16.0+** — 训练语料采集功能，将符合条件的请求/响应对持久化至磁盘，用于后续模型微调或分类器训练。可按分组粒度启用或全局启用。

```bash
# 查看当前采集状态（全局及各分组）
sproxy admin corpus status

# 全局启用语料采集
sproxy admin corpus enable

# 仅为指定分组启用采集
sproxy admin corpus enable --group engineering

# 全局禁用语料采集
sproxy admin corpus disable

# 仅为指定分组禁用采集
sproxy admin corpus disable --group trial

# 列出已采集的语料文件（含路径、大小、时间）
sproxy admin corpus list
```

| 子命令 | 选项 | 说明 |
|--------|------|------|
| `status` | — | 查看全局及各分组的采集开关状态 |
| `enable` | `--group <name>` | 启用采集；省略 `--group` 则全局启用 |
| `disable` | `--group <name>` | 禁用采集；省略 `--group` 则全局禁用 |
| `list` | — | 列出 corpus 目录下所有语料文件 |

语料文件默认存储于数据库文件同级的 `corpus/` 目录下，JSON 格式，命名规则与 `track/` 目录保持一致：`<UTC时间戳>-<requestID>.json`。

---

## API 变更历史

| 版本 | 新增 / 变更 |
|------|------------|
| v2.4.0 | `sproxy admin track` CLI — 按用户粒度记录对话内容至磁盘 |
| v2.7.0 | LLM Target 动态管理：`POST /api/admin/llm/targets`、`PUT /api/admin/llm/targets/{url}`（部分更新）、`PUT /api/admin/llm/targets/{url}/enable`、`PUT /api/admin/llm/targets/{url}/disable`、`DELETE /api/admin/llm/targets/{url}` |
| v2.7.0 | `/health` 响应新增 `version`、`uptime_seconds`、`active_requests`、`usage_queue_depth`、`db_type`、`draining` 字段 |
| v2.8.0 | `GET /api/admin/alerts/stream` — SSE 实时告警流 |
| v2.8.0 | `POST /api/admin/import` — 从模板文件批量导入用户/分组，支持 `dry_run` 预览 |
| v2.9.0 | `GET /keygen/{username}` — 为用户签发 `sk-pp-` API Key，支持 Direct Proxy 模式 |
| v2.9.0 | `POST /keygen/verify` — 验证 `sk-pp-` API Key 有效性 |
| v2.15.0 | Keygen 算法升级为 HMAC-SHA256，`/keygen/` 端点产生确定性密钥，替换旧版指纹算法 |
| v2.16.0 | `sproxy admin corpus` CLI — 训练语料采集管理（status / enable / disable / list） |
| v2.18.0 | `GET/POST /api/admin/semantic-router/rules` — 语义路由规则 CRUD |
| v2.18.0 | `PUT/DELETE /api/admin/semantic-router/rules/{id}` — 规则更新与删除 |
| v2.18.0 | `POST /api/admin/semantic-router/rules/{id}/enable\|disable` — 规则启用/禁用 |
| v2.18.0 | `GET /api/admin/semantic-router/status` — 语义路由系统状态查询 |
| v2.18.0 | `sproxy admin semantic-router` CLI — 语义路由规则完整命令行管理 |
| v2.24.0 | `POST /api/admin/llm/targets` 新增 `supported_models`（`[]string`，支持通配符）、`auto_model`（`string`）字段 — Model-Aware Routing |
| v2.24.0 | `PUT /api/admin/llm/targets/{url}` 新增 `supported_models`、`auto_model` 可选字段（部分更新） |
| v2.24.0 | `sproxy admin llm target add/update` 新增 `--supported-models`、`--auto-model` 参数 |
