# PairProxy Security Guide

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
