# PairProxy 配置文件说明

## 文件列表

| 文件 | 用途 |
|------|------|
| `cproxy.yaml.example` | 开发者本地运行的 c-proxy 配置 |
| `sproxy.yaml.example` | 服务端 s-proxy 主节点（primary）配置 |
| `sproxy-worker.yaml.example` | 服务端 s-proxy 工作节点（worker）配置 |
| `sproxy.service` | systemd unit 文件（primary / 单机节点） |
| `sproxy-worker.service` | systemd unit 文件（worker 节点） |

## 快速开始

### 1. 生成 admin 密码 hash

```bash
sproxy hash-password
# 输入密码后输出 bcrypt hash，填入 sproxy.yaml 的 admin.password_hash
```

### 2. 准备环境变量

```bash
export ANTHROPIC_API_KEY_1="sk-ant-api03-..."
export JWT_SECRET="$(openssl rand -hex 32)"
export ADMIN_PASSWORD_HASH='$2a$10$...'   # 上一步生成的 hash
# v2.15.0+ 新增必填
export KEYGEN_SECRET="$(openssl rand -hex 32)"   # sk-pp- API Key 生成密钥
# PostgreSQL 模式（v2.13.0+，可选）
# export PG_DSN="host=localhost user=pairproxy password=xx dbname=pairproxy sslmode=require"
```

### 3. 启动 s-proxy（服务端，单机模式）

```bash
cp sproxy.yaml.example sproxy.yaml
# 编辑 sproxy.yaml，填入实际值
sproxy start --config sproxy.yaml
```

### 4. 用户登录并启动 c-proxy（开发者本地）

```bash
cp cproxy.yaml.example ~/.config/pairproxy/cproxy.yaml
# 编辑 cproxy.yaml，将 sproxy.primary 改为实际 s-proxy 地址

cproxy login --server http://proxy.company.com:9000
cproxy start
```

### 5. 配置 Claude Code

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_API_KEY=any-placeholder-value
```

---

## 配置项速查

### c-proxy 关键配置

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `listen.port` | `8080` | Claude Code 指向的本地端口 |
| `sproxy.primary` | — | s-proxy 地址（**必填**） |
| `sproxy.request_timeout` | `300s` | 请求超时（含 SSE 流） |
| `auth.refresh_threshold` | `30m` | 过期前多久自动刷新 token |

### s-proxy 关键配置

| 配置项 | 默认值 | 版本 | 说明 |
|--------|--------|------|------|
| `listen.port` | `9000` | v1.0 | 服务监听端口 |
| `llm.targets` | — | v1.0 | LLM API 节点列表（**必填**） |
| `database.path` | — | v1.0 | SQLite 文件路径（**必填**，使用 SQLite 时）|
| `database.driver` | `sqlite` | v2.13.0 | 数据库驱动：`sqlite` 或 `postgres` |
| `database.dsn` | — | v2.13.0 | PostgreSQL 连接串（使用 PG 时必填）|
| `auth.jwt_secret` | — | v1.0 | JWT 密钥（**必填**，建议从环境变量注入）|
| `auth.keygen_secret` | — | v2.15.0 | sk-pp- Key 生成密钥（**必填**，≥32字符）|
| `auth.provider` | `local` | v2.x | 认证提供者：`local` 或 `ldap` |
| `admin.password_hash` | — | v1.0 | Admin 面板密码 hash（**必填**）|
| `admin.key_encryption_key` | — | v2.x | API Key 加密密钥（使用 `admin apikey` 命令时**必填**，≥32字符）|
| `cluster.role` | `primary` | v2.0 | `primary` 或 `worker`（SQLite 模式）|
| `cluster.mode` | — | v2.14.0 | `peer`（PostgreSQL Peer Mode）|
| `pricing.models` | — | v2.x | 按模型定价，用于 Dashboard 费用统计 |
| `cluster.alert_webhook` | — | v2.x | 告警 Webhook（Slack/飞书等，留空不发）|
| `corpus.enabled` | `false` | v2.16.0 | 是否启用训练语料采集 |
| `corpus.output_dir` | `./corpus` | v2.16.0 | 语料 JSONL 输出目录 |
| `semantic_router.enabled` | `false` | v2.18.0 | 是否启用语义路由 |
| `semantic_router.classifier_url` | `http://localhost:9000` | v2.18.0 | 分类器端点 |
| `semantic_router.classifier_timeout` | `5s` | v2.18.0 | 分类器超时时间 |

### 分组配额（通过 Admin API 或 CLI 设置）

配额不在配置文件中设置，通过以下方式管理：

```bash
# 创建分组并设置配额
sproxy admin group add engineering --daily-limit 1000000 --monthly-limit 20000000 --rpm 60

# 修改已有分组配额
sproxy admin group set-quota engineering --daily 2000000 --monthly 40000000 --rpm 120

# 创建用户并分配分组
sproxy admin user add alice --group engineering
```

---

## 集群部署示意

```
                    ┌─────────────────────────────┐
[开发者 A]  cproxy ─┤                             │
[开发者 B]  cproxy ─┤   s-proxy 集群              │
[开发者 C]  cproxy ─┤                             │
                    │  sp-1 (primary, :9000)       │──▶ Anthropic API
                    │  sp-2 (worker,  :9000)       │──▶ Anthropic API
                    │  sp-3 (worker,  :9000)       │──▶ Anthropic API
                    └─────────────────────────────┘
                           │
                     Web Dashboard
                    /dashboard/ (sp-1)
```

c-proxy 从 sp-1 获取路由表，自动感知 sp-2/sp-3，实现负载均衡。

---

## 配置演进速查

各版本新增的重要配置字段：

| 版本 | 新增配置字段 | 是否必填 |
|------|------------|---------|
| v2.4.0 | — | — |
| v2.5.0 | `sproxy: health_check_timeout`, `retry.*`, `cluster.usage_buffer.*` | 否（有默认值）|
| v2.7.0 | LLM Target 动态管理（通过 CLI/API，非配置文件）| — |
| v2.9.0 | `auth.keygen_secret`（当时可选） | 否 |
| v2.13.0 | `database.driver`, `database.dsn` | 否（默认 sqlite）|
| v2.14.0 | `cluster.mode: peer` | 否（PG Peer Mode 需要）|
| v2.15.0 | `auth.keygen_secret` | **必填**（v2.15.0+）|
| v2.16.0 | `corpus.enabled`, `corpus.output_dir`, `corpus.max_file_size_mb`, `corpus.excluded_groups` | 否 |
| v2.18.0 | `semantic_router.enabled`, `semantic_router.classifier_url`, `semantic_router.classifier_timeout`, `semantic_router.rules` | 否 |

## 完整配置示例（v2.18.0）

以下为包含所有主要配置项的 sproxy.yaml 示例（仅供参考，生产环境根据实际需要裁剪）：

```yaml
listen:
  port: 9000

database:
  # SQLite 模式（默认）
  path: "/var/lib/pairproxy/pairproxy.db"
  write_buffer_size: 200
  flush_interval: 5s
  # PostgreSQL 模式（v2.13.0+，可选替代 SQLite）
  # driver: postgres
  # dsn: "${PG_DSN}"

auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"     # v2.15.0+ 必填
  access_token_ttl: 24h
  refresh_token_ttl: 168h
  provider: local                        # local 或 ldap

admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"

dashboard:
  enabled: true

llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"
      provider: "anthropic"
      name: "Primary"
      weight: 1

cluster:
  role: primary                          # primary 或 worker（SQLite 模式）
  shared_secret: "${CLUSTER_SECRET}"
  # mode: peer                           # v2.14.0+ PG Peer Mode

pricing:
  models:
    claude-3-5-sonnet-20241022:
      input_per_1k: 0.003
      output_per_1k: 0.015

# 训练语料采集（v2.16.0+，默认禁用）
corpus:
  enabled: false
  output_dir: "./corpus"
  max_file_size_mb: 100
  excluded_groups: []

# 语义路由（v2.18.0+，默认禁用）
semantic_router:
  enabled: false
  classifier_url: "http://localhost:9000"
  classifier_timeout: 5s
  rules:
    - name: "code-tasks"
      description: "编程、调试、代码审查类任务"
      targets:
        - "https://api.anthropic.com"
      priority: 10
```
