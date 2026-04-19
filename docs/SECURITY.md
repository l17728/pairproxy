# PairProxy Security Guide

> **版本**：v3.0.0 | 最后更新：2026-04-19

This document describes the security model of PairProxy, the threats it addresses, the mitigations in place, and operational hardening recommendations.

---

## Threat Model

PairProxy sits between internal developer tooling (Claude Code) and commercial LLM APIs. The primary threats are:

| Threat | Impact |
|--------|--------|
| Unauthorized LLM access | API cost without accountability |
| Token theft / replay | Impersonation, quota bypass |
| JWT algorithm confusion | Forged tokens accepted by s-proxy |
| Unauthenticated cluster API | Worker injection, usage data manipulation |
| Config misconfiguration at startup | Silent security degradation |
| Resource exhaustion (quota bypass) | Runaway API spend |
| keygen_secret leakage | ~~All Direct Proxy API Keys forgeable~~ — 已弃用（v2.24.7+），Key 改为按用户 PasswordHash 派生 |
| User password hash leakage | Direct Proxy API Key for that user forgeable (bcrypt hash is non-reversible to plaintext) |
| Legacy key not invalidated after password change | v2.24.8 前：改密后旧 legacy Key 仍可用；v2.24.8+ 自动吊销 |
| Classifier data exfiltration | User message content leaked to external service |
| Corpus data at rest | Training data exposed via filesystem access |

---

## JWT Authentication (User ↔ s-proxy)

### Algorithm Pinning (HS256 only)

All JWTs issued by `s-proxy` use **HMAC-SHA256 (HS256)**. The `Parse()` function
enforces this with an explicit algorithm check:

```
if token.Method.Alg() != "HS256" → reject with ErrInvalidToken
```

This prevents **algorithm confusion attacks** where an attacker signs a token
with a different HMAC variant (HS384, HS512) or an asymmetric algorithm
(RS256 with the HS256 secret as the public key) in an attempt to bypass
signature verification.

**What is NOT accepted:**
- `HS384`, `HS512` — even if signed with the correct secret
- `RS256`, `ES256` — asymmetric algorithms
- `none` — unsigned tokens

### JWT Blacklist (Token Revocation)

A short-lived in-memory blacklist stores revoked JTI (JWT ID) values. When
`sproxy admin token revoke <username>` is called:

1. The user's refresh token is deleted from the database.
2. The access token's JTI is added to the in-memory blacklist with a TTL equal
   to the token's remaining validity.

On `Parse()`, blacklisted JTIs are rejected with `ErrTokenRevoked`.

**Caveat**: the blacklist is in-memory. A s-proxy restart clears it. On
restart, still-valid access tokens from revoked users will be accepted until
they expire naturally (≤ 24h by default). For immediate revocation, set
`access_token_ttl` to a short value (e.g., `1h`).

### Secure Secret Management

- `auth.jwt_secret` **must** be set (config validation rejects empty values).
- Use environment variable substitution (`${JWT_SECRET}`) — never commit secrets
  to version control.
- Minimum recommended length: 32 random bytes (`openssl rand -hex 32`).

---

## Direct Proxy API Key 安全 (v2.9.0+)

`sk-pp-` 前缀 API Key 是另一种接入方式，用户通过 API Key 直接访问 sproxy 而无需 cproxy 和 JWT Token。

### HMAC-SHA256 Per-User Keygen (v2.24.7+)

v2.24.7 起，API Key 由**用户自己的 bcrypt PasswordHash** 作为 HMAC 密钥派生，替换了原来的共享 `keygen_secret`：

```
API Key = HMAC-SHA256(username, user.PasswordHash) → Base62 编码（48 字符）
```

- **确定性生成**：相同用户名 + 相同 PasswordHash → 相同 API Key，服务器无需存储 Key
- **互不影响**：每个用户拥有独立的派生密钥，一个用户改密码不影响其他用户的 Key
- **改密即轮换**：用户修改密码后，`legacy_key_revoked` 标记自动设置，旧 legacy Key 即时失效，新 Key 即时可用（v2.24.8+）
- **256 位安全强度**：HMAC-SHA256 输出截断后取 Base62 编码（48 字符）
- **LDAP 用户限制**：LDAP 账号无本地 PasswordHash，无法持有 sk-pp- Key

> **注**：`auth.keygen_secret` 配置字段已弃用（v2.24.7+），不再读取或验证，可安全从配置文件中删除。

### 安全注意事项

| 风险 | 缓解措施 |
|------|---------|
| 用户密码泄漏导致 API Key 可伪造 | bcrypt 哈希不可逆推，攻击者无法从 PasswordHash 还原密码 |
| API Key 无过期时间 | 用户自助改密码可立即轮换 Key；管理员重置密码亦可强制失效 |
| sk-pp- Key 明文传输 | 要求 HTTPS 接入，同 JWT Token |
| 旧 Key 缓存未及时清除 | 管理员重置密码 / 用户自助改密码后，KeyCache 立即清除旧条目 |

### Key 轮换流程

**用户自助轮换**（推荐）：
1. 访问 `https://sproxy.company.com/keygen/`
2. 使用用户名 + 密码登录
3. 点击"修改密码"，输入旧密码和新密码
4. 页面立即显示新 Key，复制后更新客户端配置

**管理员强制轮换**：
1. 管理员通过 Dashboard 或 CLI 重置用户密码：
   ```bash
   sproxy admin user reset-password <username>
   ```
2. 用户以新密码登录 Keygen WebUI 获取新 Key

---

## Cluster Internal API Authentication (Primary ↔ Worker)

The cluster internal API (`/api/internal/register`, `/api/internal/usage`,
`/cluster/routing`) uses a **shared Bearer token** (`cluster.shared_secret`).

### Fail-Closed Policy

The primary operates **fail-closed**: if `shared_secret` is empty, **all**
requests to the cluster internal API are rejected with HTTP 401 and a WARN
log entry. There is no unauthenticated mode.

| Scenario | Behavior |
|----------|----------|
| `shared_secret` empty on primary | All cluster API requests → 401 (WARN logged) |
| `shared_secret` set, correct token | Request accepted |
| `shared_secret` set, wrong token | Request → 401 (WARN logged) |
| `shared_secret` set, no Authorization header | Request → 401 (WARN logged) |

### Deployment Requirements

1. Generate a strong secret (≥ 32 random bytes):
   ```bash
   openssl rand -hex 32
   ```
2. Set it as `cluster.shared_secret` on the **primary** and all **workers**
   using the same value.
3. Use `${CLUSTER_SECRET}` substitution — inject via environment variable.
4. Restrict network access to `/api/internal/*` to the cluster's private
   network only (firewall / security group rules).

### Single-Node Deployments

On a single-node deployment (no workers), leave `shared_secret` empty. The
internal API will never be called by any worker, so the fail-closed rejection
is harmless.

---

## Config Validation at Startup

Both `LoadSProxyConfig` and `LoadCProxyConfig` run a `Validate()` check
immediately after loading and applying defaults. The process exits early with a
descriptive error if any required field is missing or out of range.

### Fields Validated (s-proxy)

| Field | Rule |
|-------|------|
| `auth.jwt_secret` | Must be non-empty |
| `auth.keygen_secret` | Must be ≥ 32 characters (v2.15.0+) |
| `database.path` | Must be non-empty |
| `llm.targets` | Must have at least one entry |
| `listen.port` | Must be in range 1–65535 |
| `cluster.role` | Must be `primary` or `worker` (or empty, treated as `primary`) |
| `cluster.primary` | Required when `role = worker` |

### Multiple Errors

`Validate()` collects **all** validation errors before returning, so a
misconfigured deployment reports every problem at once rather than failing on
the first error.

### Fields Validated (c-proxy)

| Field | Rule |
|-------|------|
| `listen.port` | Must be in range 1–65535 |

---

## Database Connection Lifecycle

Both the `sproxy start` command and all `sproxy admin` sub-commands open a GORM
database connection and close it via `defer` before the process exits:

```
sproxy start  → defer closeGormDB()  (covers both normal exit and fatal errors)
sproxy admin  → each subcommand defers closeGormDB() individually
```

This prevents SQLite WAL file corruption and leaked file descriptors.

---

## PostgreSQL 数据库安全 (v2.13.0+)

v2.13.0 起支持 PostgreSQL 作为数据库后端（替代 SQLite）。

### 连接安全

- 使用 SSL/TLS 连接：在 DSN 中添加 `sslmode=require`
- 使用专用数据库用户，最小权限（仅 CRUD 权限，无 DDL 权限用于生产环境）
- 通过环境变量注入连接字符串：`${PG_DSN}`，避免明文写入配置文件

### 连接串示例

```yaml
database:
  driver: postgres
  dsn: "${PG_DSN}"
# PG_DSN = "host=pg.company.com user=pairproxy password=xxx dbname=pairproxy sslmode=require"
```

### 权限加固

```sql
-- 创建专用用户
CREATE USER pairproxy WITH PASSWORD 'strong-password';
GRANT CONNECT ON DATABASE pairproxy TO pairproxy;
GRANT USAGE ON SCHEMA public TO pairproxy;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO pairproxy;
```

---

## Quota and Rate Limiting

### Quota Enforcement (daily / monthly tokens)

Quotas are enforced per user group. When a user exceeds their group quota:
- The request is rejected with HTTP **429 Too Many Requests**.
- Response headers `X-RateLimit-Limit`, `X-RateLimit-Used`, `X-RateLimit-Reset`
  provide machine-readable quota information.
- An optional alert webhook is called (async, fire-and-forget).

**Fail-open caveat**: if the database is unreachable during a quota check, the
request is **allowed** (fail-open). This prioritizes availability over strict
enforcement. If strict enforcement is critical, monitor database health.

### Rate Limiting (requests per minute)

RPM limits use a per-user sliding window (1-minute window). When the limit is
exceeded, the request is rejected with HTTP **429**. The rate limiter is
automatically purged of stale entries every minute.

---

## Password Security

Admin passwords are stored as **bcrypt hashes** (work factor 12 by default).
Plaintext passwords are never written to disk or logged.

User login credentials are transmitted only over HTTPS. In development
environments, ensure that the connection between c-proxy and s-proxy uses TLS
(e.g., place s-proxy behind an nginx/Caddy reverse proxy with a TLS certificate).

---

## LDAP Authentication (F-4)

When `auth.provider: "ldap"` is configured:

- **Credentials flow**: The user's plaintext password is forwarded from s-proxy to the LDAP server over the network. **Always use `use_tls: true` (LDAPS) in production** to prevent credential interception.
- **Service account**: The `bind_dn` / `bind_password` are used only for searching user DNs (read-only). Store `bind_password` in an environment variable (e.g., `${LDAP_BIND_PASSWORD}`).
- **JIT provisioning**: On first successful LDAP login, a `users` row is created with `auth_provider = 'ldap'` and `password_hash = ''`. These users cannot log in via the local auth path.
- **User disabling**: Disabling a user in s-proxy (`sproxy admin user disable`) prevents login even if the LDAP account is still active. The reverse (LDAP account disabled, s-proxy user still active) is **not** automatically enforced — the user can still obtain a token until the s-proxy entry is disabled.
- **JIT users have no group**: Newly provisioned LDAP users have no group (unlimited quota). Assign groups via the Admin Dashboard or API after provisioning.

---

## Logging and Auditing

PairProxy uses structured logging (zap) with the following security-relevant
log entries:

| Event | Level | Fields |
|-------|-------|--------|
| JWT verification failure | WARN | `remote_addr`, `path`, `error` |
| JWT algorithm mismatch | WARN | `got_alg`, `want_alg` |
| Cluster API auth failure | WARN | `remote_addr`, `path` |
| Cluster `shared_secret` not configured | WARN | `remote_addr`, `path` |
| Quota exceeded | WARN | `user_id`, `kind` (daily/monthly/rate_limit), `reset_at` |
| Admin login failure | WARN | implicit (401 response) |
| Token revoked | INFO | `user_id`, `jti` |

Set `log.level: debug` during security testing to enable per-request verbose logs.

---

## Conversation Tracking Data Privacy

> v2.4.0+

The `sproxy admin track` feature records full conversation content (user messages and assistant replies) to disk. This data is **sensitive** and requires explicit operational controls.

### Data Stored

Each conversation record (`track/conversations/<username>/*.json`) contains:

- Full message content sent by the user to the LLM
- Full assistant reply text
- Request metadata (timestamp, model, token counts)

This data is **not encrypted at rest**. The file permissions are `0644` by default.

### Recommended Controls

| Control | Action |
|---------|--------|
| **File permissions** | `chmod 600 <track_dir>/conversations/<username>/*.json` or set umask |
| **Directory ownership** | Ensure only the sproxy service user can read the directory |
| **Encryption at rest** | Use filesystem-level encryption (LUKS, eCryptfs) for the data volume |
| **Retention policy** | Run `sproxy admin track clear <username>` periodically, or use `find … -mtime +N -delete` |
| **Minimal scope** | Enable tracking only for specific users and only for the duration needed |
| **Access audit** | Log admin CLI access; `sproxy admin track show` reads files locally |

### User Notification

Enabling conversation tracking without the user's knowledge may violate privacy regulations (GDPR, PIPL, etc.) in your jurisdiction. Ensure appropriate legal basis and disclosure before enabling tracking on production users.

### Disable Debug Logging in Production

`log.debug_file` (if configured) logs raw HTTP bytes for **all** users. Ensure it is unset or pointed to a secured path in production. The `track` feature is more targeted but has the same confidentiality requirements.

---

## 训练语料采集数据隐私 (v2.16.0+)

`corpus` 功能以 JSONL 格式采集 LLM 请求/响应对，用于训练语料收集。

### 数据存储

- JSONL 文件存储在配置的 `corpus.output_dir` 目录
- 按日期 + 大小进行文件轮转（如 `corpus-2026-03-22.jsonl`）
- 数据**不加密**，文件权限默认 0644

### 质量过滤

以下内容不会被采集（降低隐私风险）：
- 错误响应（status != 200）
- 极短回复（< N tokens）
- 被 `excluded_groups` 配置排除的分组

### 建议控制

与 Conversation Tracking 相同的控制措施：文件权限 600、目录权限仅 sproxy 用户可读、定期清理（`sproxy admin corpus` 命令管理）、采集前获取合法依据。

---

## Reporting Vulnerabilities

Please report security vulnerabilities by opening a **private** GitHub Security
Advisory at: `https://github.com/l17728/pairproxy/security/advisories/new`

Do **not** file public GitHub issues for security vulnerabilities.

---

## File Permission Hardening

### Config Files

Config files contain secrets (JWT secret, API keys, admin password hash).
Restrict read access to the service account only:

```bash
chmod 600 /etc/pairproxy/sproxy.yaml
chown sproxy:sproxy /etc/pairproxy/sproxy.yaml

chmod 600 /etc/pairproxy/sproxy-worker.yaml
chown sproxy:sproxy /etc/pairproxy/sproxy-worker.yaml

# Verify
ls -la /etc/pairproxy/
# -rw------- 1 sproxy sproxy ... sproxy.yaml
```

### Database File

The SQLite database contains usage logs and user credentials:

```bash
chmod 640 /var/lib/pairproxy/pairproxy.db
chmod 640 /var/lib/pairproxy/pairproxy.db-wal
chown sproxy:sproxy /var/lib/pairproxy/
chmod 750 /var/lib/pairproxy/
```

### Token File (c-proxy users)

The c-proxy JWT token file should be readable only by the local user:

```bash
chmod 600 ~/.config/pairproxy/token.json   # Linux/macOS
# Windows: stored in %APPDATA%\pairproxy\token.json (accessible only to current user)
```

---

## TLS / HTTPS Configuration

PairProxy itself does **not** terminate TLS. Place a reverse proxy (nginx or
Caddy) in front of `sproxy` for HTTPS in production.

### Nginx + Let's Encrypt

```nginx
server {
    listen 443 ssl http2;
    server_name proxy.company.com;

    ssl_certificate     /etc/letsencrypt/live/proxy.company.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/proxy.company.com/privkey.pem;

    # Modern TLS only
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers 'ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:...';
    ssl_prefer_server_ciphers off;

    # SSE requires disabled buffering
    proxy_buffering off;
    # LLM extended thinking 可静默超过 30 分钟；设为 0 表示不限制，
    # 依赖 sproxy 自身的客户端断开检测来回收挂起连接。
    proxy_read_timeout 0;
    proxy_send_timeout 0;

    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# Redirect HTTP to HTTPS
server {
    listen 80;
    server_name proxy.company.com;
    return 301 https://$host$request_uri;
}
```

### Caddy (automatic HTTPS)

```caddyfile
proxy.company.com {
    # Caddy automatically obtains and renews TLS certs via Let's Encrypt

    # Disable response buffering (required for SSE / streaming)
    @streaming {
        header Content-Type text/event-stream
    }
    flush_interval -1

    reverse_proxy 127.0.0.1:9000 {
        flush_interval -1
        transport http {
            read_buffer 0
        }
    }
}
```

### c-proxy with HTTPS sproxy

Update `cproxy.yaml` to use the HTTPS address:

```yaml
sproxy:
  primary: "https://proxy.company.com"   # Note: HTTPS, default port 443
```

### Network Isolation

For the cluster internal API, restrict firewall access so only s-proxy nodes
can reach each other:

```bash
# Linux: allow port 9000 only from cluster subnet
ufw allow from 10.0.0.0/24 to any port 9000
ufw deny 9000

# Or iptables
iptables -A INPUT -p tcp --dport 9000 -s 10.0.0.0/24 -j ACCEPT
iptables -A INPUT -p tcp --dport 9000 -j DROP
```

---

## API Key Rotation

### Anthropic API Key Rotation

1. Generate a new API key in the Anthropic Console.
2. Add the new key to `sproxy.yaml` alongside the existing key:
   ```yaml
   llm:
     targets:
       - url: "https://api.anthropic.com"
         api_key: "${ANTHROPIC_API_KEY_1}"  # existing
         weight: 1
       - url: "https://api.anthropic.com"
         api_key: "${ANTHROPIC_API_KEY_NEW}" # new
         weight: 1
   ```
3. Export the new environment variable and send SIGHUP (Linux) or restart:
   ```bash
   export ANTHROPIC_API_KEY_NEW=sk-ant-new-key
   kill -HUP $(pidof sproxy)   # Linux/macOS only
   # Windows: restart the service
   ```
   **Note**: API key changes require a restart on Linux because SIGHUP only
   reloads `log.level`. Restart the process to pick up new key values.
4. Verify the new key is working (check sproxy logs for LLM requests).
5. Revoke the old key in the Anthropic Console.
6. Remove the old key from config and restart.

---

## JWT Secret Rotation

Rotating the JWT secret invalidates **all existing tokens** immediately.
Plan for a maintenance window.

**Steps:**

1. Generate a new secret:
   ```bash
   openssl rand -hex 32
   ```
2. Update `JWT_SECRET` environment variable on all s-proxy nodes (primary + workers).
3. Restart all s-proxy instances.
4. Notify users to run `cproxy login` again (their existing tokens are invalid).

**Mitigating disruption:**

- Short-lived access tokens (24h default) limit blast radius if the secret leaks.
- Consider setting `access_token_ttl: 1h` before rotating to minimize re-login friction.

---

## Cluster Shared Secret Rotation

The cluster shared secret protects worker → primary communication.

**Steps:**

1. Generate a new secret:
   ```bash
   openssl rand -hex 32
   ```
2. Update `CLUSTER_SECRET` on the primary node first.
3. Restart the primary (workers will get 401 during the window — they retry).
4. Update `CLUSTER_SECRET` on each worker and restart.
5. Verify workers re-register (check `GET /cluster/routing` output).

**Rolling restart** (zero downtime for end users):
- End users connect to c-proxy, which routes to healthy nodes.
- c-proxy health checker detects nodes that go down during restart and re-routes.
- Restart one node at a time, waiting for it to rejoin the cluster before the next.

---

## 语义路由安全 (v2.18.0+)

语义路由分类器将用户消息内容发送给另一个 LLM 进行意图分类。

### 数据流安全

- **分类器请求**：用户 messages 的副本被发送给分类器端点（默认为本机 sproxy，使用独立 LLM Target Pool）
- **递归防止**：分类器复用现有 LB 但跳过语义路由层，防止无限递归
- **分类失败降级**：分类器超时或错误时，自动降级为完整候选池路由，不影响请求处理

### 敏感数据泄漏风险

若分类器端点配置为外部服务（非本机），用户消息内容将被发送至该外部服务。

建议：
- **优先使用本机 sproxy 作为分类器**（默认配置）
- 若必须使用外部分类器，确保其满足与主 LLM 相同的隐私合规要求
- 审计 `semantic_router.classifier_url` 配置

### 规则管理访问控制

语义路由规则的 REST API（`/api/admin/semantic-router/*`）和 CLI（`sproxy admin semantic-router`）均需要 admin 身份认证，遵循与其他 admin API 相同的鉴权策略。
