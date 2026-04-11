# Configuration Guide

## Overview

Pairproxy uses YAML configuration files with environment variable overrides for flexible deployment across different environments.

## Configuration File Location

Default: `cproxy.yaml` in the working directory

Override with environment variable:
```bash
export CPROXY_CONFIG_FILE=/etc/pairproxy/config.yaml
```

## Configuration Structure

### Basic Configuration

```yaml
# Server settings
listen:
  host: "127.0.0.1"
  port: 8080

# S-Proxy settings
sproxy:
  primary: "http://proxy.company.com:9000"
  targets:
    - "http://backup1.company.com:9000"
    - "http://backup2.company.com:9000"

# Health check settings
health_check:
  interval: 30s
  timeout: 5s
  path: "/health"

# Request settings
request:
  timeout: 5m
  max_body_size: 100MB

# Logging
log:
  level: "info"  # debug, info, warn, error
  format: "json" # json, text

# Database
database:
  path: "./pairproxy.db"
  max_open_conns: 10
  max_idle_conns: 5
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m

# Caching
cache:
  enabled: true
  ttl: 5m
  max_size: 1000

# Quota settings
quota:
  enabled: true
  default_daily_limit: 100000
  default_monthly_limit: 3000000

# Alert settings
alerts:
  enabled: true
  webhook_url: "https://alerts.company.com/webhook"
  channels:
    - "slack"
    - "email"
```

## Environment Variables

Override any configuration value with environment variables using the pattern `CPROXY_<SECTION>_<KEY>`:

```bash
# Server
export CPROXY_LISTEN_HOST="0.0.0.0"
export CPROXY_LISTEN_PORT="9000"

# S-Proxy
export CPROXY_SPROXY_PRIMARY="http://primary.example.com:9000"

# Health check
export CPROXY_HEALTH_CHECK_INTERVAL="60s"

# Logging
export CPROXY_LOG_LEVEL="debug"

# Database
export CPROXY_DATABASE_PATH="/var/lib/pairproxy/data.db"
export CPROXY_DATABASE_MAX_OPEN_CONNS="20"

# Quota
export CPROXY_QUOTA_ENABLED="true"
export CPROXY_QUOTA_DEFAULT_DAILY_LIMIT="500000"
```

## Configuration Sections

### Listen Configuration

```yaml
listen:
  host: "127.0.0.1"  # Bind address
  port: 8080         # Port number (1-65535)
```

**Environment Variables:**
- `CPROXY_LISTEN_HOST`
- `CPROXY_LISTEN_PORT`

### S-Proxy Configuration

```yaml
sproxy:
  primary: "http://primary.example.com:9000"  # Primary target
  targets:                                     # Backup targets
    - "http://backup1.example.com:9000"
    - "http://backup2.example.com:9000"
```

**Notes:**
- At least one of `primary` or `targets` must be configured
- Targets are used for load balancing and failover
- Primary target has priority in routing decisions

### Health Check Configuration

> **Scope note**: Health check configuration described here applies to **sproxy** (server-side)
> LLM target management. The `cproxy.yaml` client config does not have health check settings —
> sproxy handles all target health checking internally. For the authoritative sproxy health check
> reference, see [§15.4 of the manual](manual.md#154-智能探活smart-probe) and `config/sproxy.yaml.example`.

PairProxy uses **Smart Probe** (v2.24.5+) to automatically discover the best health check strategy for each target. No manual path configuration is required.

#### Smart Probe (Recommended)

Leave `health_check_path` empty (or omit it). The system will automatically try the following strategies in priority order and cache the first one that works (default TTL: 2 hours):

| Priority | Strategy | OK Status Codes | Best For |
|----------|----------|-----------------|----------|
| 1 (Anthropic) | `GET /v1/models` | 200, 401, 403, 400 | Anthropic-compatible APIs |
| 2 (Anthropic) | `POST /v1/messages` | 200, 401, 403, 400 | Anthropic-compatible APIs |
| 3 (Generic) | `GET /health` | 200 | vLLM, sglang, self-hosted |
| 4 (Generic) | `GET /v1/models` | 200, 401, 403 | OpenAI-compatible APIs |
| 5 (Generic) | `POST /v1/chat/completions` | 200, 401, 403, 400 | Universal fallback |

**Two-phase semantics**: During discovery, 401/403 means "endpoint found" (service is online). During regular health checks, 401/403 means "API key invalid" (mark unhealthy).

> **Note** (v2.24.6+): The `health_check_timeout` setting now correctly applies to the Smart Probe HTTP client as well. Prior to v2.24.6, `WithTimeout` only updated the regular heartbeat client; the Discover client retained the default timeout regardless of configuration.

> **Note** (v2.24.6+): A whitespace-only `api_key` (spaces, tabs) is treated as absent — no `Authorization` header is injected. This matches the behavior of omitting the key entirely.

```yaml
# Recommended: omit health_check_path — Smart Probe handles discovery automatically
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      # No health_check_path needed — Smart Probe discovers GET /v1/models

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      # No health_check_path needed — Smart Probe discovers GET /v1/models
```

#### Explicit Path (Advanced)

If you know the exact health check path, you can set it explicitly on a per-target basis in `sproxy.yaml` to skip auto-discovery:

```yaml
# In sproxy.yaml — per-target explicit health check path
llm:
  targets:
    - url: "https://my-custom-llm.company.com"
      api_key: "${MY_KEY}"
      provider: "openai"
      health_check_path: "/api/health"   # Explicit path — bypasses Smart Probe
```

**Path priority**: per-target `health_check_path` > Smart Probe auto-discovery

> **Note**: The `health_check_path` field is specific to `sproxy.yaml` (server-side LLM target configuration).
> There is no global `health_check.interval`/`health_check.path` block in the cproxy config — those parameters
> are internal to sproxy and not exposed as cproxy client settings.

### Request Configuration

```yaml
request:
  timeout: 5m        # Request timeout
  max_body_size: 100MB  # Maximum request body size
  max_retries: 3     # Maximum retry attempts
  retry_delay: 1s    # Delay between retries
```

### Logging Configuration

```yaml
log:
  level: "info"      # debug, info, warn, error
  format: "json"     # json, text
  output: "stdout"   # stdout, file
  file_path: "/var/log/pairproxy/app.log"  # Log file path
  max_size: 100MB    # Max log file size
  max_backups: 10    # Number of backup files
  max_age: 30        # Days to keep logs
```

**Log Levels:**
- `debug`: Detailed debugging information
- `info`: General informational messages
- `warn`: Warning messages
- `error`: Error messages only

### Database Configuration

```yaml
database:
  path: "./pairproxy.db"     # SQLite file path
  max_open_conns: 10         # Maximum open connections
  max_idle_conns: 5          # Maximum idle connections
  conn_max_lifetime: 1h      # Connection lifetime
  conn_max_idle_time: 10m    # Idle connection timeout
```

**For PostgreSQL:**
```yaml
database:
  driver: "postgres"
  dsn: "postgres://user:pass@localhost/pairproxy"
  max_open_conns: 20
  max_idle_conns: 10
```

### Quota Configuration

```yaml
quota:
  enabled: true
  default_daily_limit: 100000      # Default daily limit
  default_monthly_limit: 3000000   # Default monthly limit
  enforcement: "strict"             # strict, soft
  reset_time: "00:00"              # Daily reset time (UTC)
```

**Enforcement Modes:**
- `strict`: Reject requests exceeding quota
- `soft`: Log warning but allow requests

### Cache Configuration

```yaml
cache:
  enabled: true
  ttl: 5m           # Time to live
  max_size: 1000    # Maximum cache entries
  backend: "memory" # memory, redis
```

### Alert Configuration

```yaml
alerts:
  enabled: true
  webhook_url: "https://alerts.example.com/webhook"
  channels:
    - "slack"
    - "email"
    - "webhook"

  # Slack configuration
  slack:
    webhook_url: "https://hooks.slack.com/services/..."
    channel: "#alerts"

  # Email configuration
  email:
    smtp_host: "smtp.example.com"
    smtp_port: 587
    from: "alerts@example.com"
    to:
      - "ops@example.com"
```

## Configuration Validation

Validate configuration on startup:

```bash
pairproxy validate-config
```

Or programmatically:

```go
config, err := config.Load("cproxy.yaml")
if err != nil {
  log.Fatal(err)
}

if err := config.Validate(); err != nil {
  log.Fatal(err)
}
```

## Configuration Examples

### Development Configuration

```yaml
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://localhost:9000"

log:
  level: "debug"
  format: "text"

database:
  path: "./dev.db"

quota:
  enabled: false

health_check:
  interval: 10s
```

### Production Configuration

```yaml
listen:
  host: "0.0.0.0"
  port: 8080

sproxy:
  primary: "https://primary.prod.example.com:9000"
  targets:
    - "https://backup1.prod.example.com:9000"
    - "https://backup2.prod.example.com:9000"

log:
  level: "warn"
  format: "json"
  output: "file"
  file_path: "/var/log/pairproxy/app.log"

database:
  driver: "postgres"
  dsn: "postgres://user:pass@db.prod.example.com/pairproxy"
  max_open_conns: 50
  max_idle_conns: 20

quota:
  enabled: true
  enforcement: "strict"

health_check:
  interval: 30s
  timeout: 5s

alerts:
  enabled: true
  channels:
    - "slack"
    - "email"
```

### High-Availability Configuration

```yaml
listen:
  host: "0.0.0.0"
  port: 8080

sproxy:
  primary: "https://primary.ha.example.com:9000"
  targets:
    - "https://backup1.ha.example.com:9000"
    - "https://backup2.ha.example.com:9000"
    - "https://backup3.ha.example.com:9000"

database:
  driver: "postgres"
  dsn: "postgres://user:pass@db-cluster.ha.example.com/pairproxy"
  max_open_conns: 100
  max_idle_conns: 50
  conn_max_lifetime: 30m

cache:
  enabled: true
  backend: "redis"
  redis_url: "redis://cache-cluster.ha.example.com:6379"

health_check:
  interval: 10s
  timeout: 3s

quota:
  enabled: true
  enforcement: "strict"

alerts:
  enabled: true
  channels:
    - "slack"
    - "email"
    - "webhook"
```

## Configuration Best Practices

1. **Use Environment Variables for Secrets**
   ```bash
   export CPROXY_DATABASE_DSN="postgres://user:${DB_PASSWORD}@db.example.com/pairproxy"
   ```

2. **Version Control Configuration**
   - Commit `cproxy.yaml` to version control
   - Use `.gitignore` for sensitive files
   - Document all configuration options

3. **Validate on Startup**
   - Always validate configuration before starting
   - Fail fast on invalid configuration
   - Log configuration errors clearly

4. **Use Appropriate Log Levels**
   - Development: `debug`
   - Staging: `info`
   - Production: `warn` or `error`

5. **Monitor Configuration Changes**
   - Track configuration changes in audit logs
   - Notify on configuration updates
   - Test configuration changes in staging first

## Troubleshooting Configuration

### Configuration Not Loading

```bash
# Check configuration file path
export CPROXY_CONFIG_FILE=/path/to/config.yaml

# Validate configuration
pairproxy validate-config

# Check file permissions
ls -la /path/to/config.yaml
```

### Environment Variables Not Working

```bash
# Verify environment variable is set
echo $CPROXY_LISTEN_PORT

# Check variable format (must be uppercase with underscores)
export CPROXY_LISTEN_PORT=9000  # ✓ Correct
export cproxy_listen_port=9000  # ✗ Wrong
```

### Database Connection Issues

```bash
# Check database path/DSN
echo $CPROXY_DATABASE_PATH
echo $CPROXY_DATABASE_DSN

# Verify database file permissions
ls -la ./pairproxy.db

# Test database connection
pairproxy test-db
```

## Configuration Migration

When upgrading pairproxy, configuration may need updates:

```bash
# Backup current configuration
cp cproxy.yaml cproxy.yaml.backup

# Run migration
pairproxy migrate-config --from 1.0 --to 2.0

# Validate new configuration
pairproxy validate-config

# Review changes
diff cproxy.yaml.backup cproxy.yaml
```

---

## reportgen 工具参数（tools/reportgen）

reportgen 是独立的报表生成工具，通过命令行参数配置，不使用 YAML 配置文件。

### 基本参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-db <path>` | — | SQLite 数据库文件路径（与 `-pg-dsn` 二选一） |
| `-from <date>` | — | 报告开始日期（`YYYY-MM-DD`） |
| `-to <date>` | — | 报告结束日期（`YYYY-MM-DD`） |
| `-out <file>` | `report.html` | 输出 HTML 文件路径 |
| `-top <n>` | `10` | Top N 用户数量 |

### PostgreSQL 参数（v2.24.2+）

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-pg-dsn <dsn>` | — | PostgreSQL DSN（如 `postgres://user:pass@host:5432/dbname`） |
| `-pg-host <host>` | `localhost` | PostgreSQL 主机 |
| `-pg-port <port>` | `5432` | PostgreSQL 端口 |
| `-pg-user <user>` | — | PostgreSQL 用户名 |
| `-pg-password <pw>` | — | PostgreSQL 密码 |
| `-pg-dbname <db>` | — | PostgreSQL 数据库名 |
| `-pg-sslmode <mode>` | `disable` | SSL 模式（`disable`/`require`/`verify-full`） |

### LLM 参数（v2.24.3+）

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-llm-url <url>` | — | LLM 端点 URL（如 `http://localhost:9000`），优先于数据库配置 |
| `-llm-key <key>` | — | LLM API Key（Bearer token） |
| `-llm-model <model>` | `gpt-4o-mini` | LLM 模型名 |

### 用法示例

```bash
# SQLite 数据库 + 不使用 LLM（纯规则分析）
./reportgen -db ./pairproxy.db -from 2026-01-01 -to 2026-04-01

# SQLite + LLM 直连（跳过数据库中的 LLM 配置）
./reportgen -db ./pairproxy.db \
  -from 2026-03-01 -to 2026-04-01 \
  -llm-url http://localhost:9000 \
  -llm-key "sk-pp-xxxxxxxx" \
  -llm-model claude-3-5-haiku-20241022

# PostgreSQL 数据库（DSN 方式）
./reportgen \
  -pg-dsn "postgres://pairproxy:pass@db.example.com:5432/pairproxy?sslmode=require" \
  -from 2026-04-01 -to 2026-04-09

# PostgreSQL 数据库（独立字段方式）
./reportgen \
  -pg-host db.example.com -pg-port 5432 \
  -pg-user pairproxy -pg-password secret \
  -pg-dbname pairproxy -pg-sslmode require \
  -from 2026-04-01 -to 2026-04-09 \
  -llm-url http://llm.example.com -llm-key "sk-pp-xxxxxxxx"
```

### 容错行为

- **LLM 连接失败**：自动降级为纯规则分析，生成不含 AI 洞察的报告
- **查询失败**：输出 `WARNING: <QueryName> failed: <err>` 到 stderr，跳过该数据块继续渲染
- **模板缺失**：自动使用内置最小模板
