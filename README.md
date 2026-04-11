# PairProxy

[![CI](https://github.com/l17728/pairproxy/actions/workflows/ci.yml/badge.svg)](https://github.com/l17728/pairproxy/actions/workflows/ci.yml)
[![Release](https://github.com/l17728/pairproxy/actions/workflows/release.yml/badge.svg)](https://github.com/l17728/pairproxy/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/l17728/pairproxy)](https://goreportcard.com/report/github.com/l17728/pairproxy)

> 企业级 Claude Code 透明代理 — 统一管控 LLM API Key，精确追踪 token 用量，零侵入接入。

```
┌─────────────┐   HTTP/SSE    ┌───────────────────────────┐
│ Claude Code │──────────────▶│  cproxy  127.0.0.1:8080   │──┐
│ (code agent)│               │  注入用户 JWT              │  │  JWT
└─────────────┘               └───────────────────────────┘  │
                                                               ▼
┌─────────────┐  sk-pp- Key   ┌──────────────────────────────────────┐
│ 任意 client │──────────────▶│  sproxy  :9000                       │──▶ Anthropic / OpenAI / Ollama
│ (直连模式)  │   /v1/ 或      │  验证身份 · 配额 · 统计用量 · 转发   │
└─────────────┘  /anthropic/  └──────────────────────────────────────┘
```

**两种接入方式**：`cproxy` 透明代理（无感知）或 `sk-pp-` API Key 直连（无需本地进程）——多人共用 LLM API Key 时，谁用了多少、超没超额、钱花哪了——一目了然。

---

## 功能特性

| 分类 | 功能 |
|------|------|
| **零侵入接入** | 用户只需设置两个环境变量，无需修改 Claude Code 配置 |
| **JWT 认证** | 每位用户独立 JWT，access token 24h，refresh token 7天，支持自动刷新 |
| **LDAP / AD 集成** | 一行配置接入企业 LDAP / Active Directory，首次登录自动 JIT 创建用户，支持 LDAPS |
| **多 Provider 支持** | 透传 Anthropic / OpenAI / Ollama 请求，按路径自动路由，精确解析各 provider token 用量 |
| **协议自动转换** | Claude CLI → Ollama/OpenAI 自动协议转换，支持流式/非流式双向转换，零配置启用 |
| **数据导出** | `sproxy admin export --format csv\|json` CLI 导出或 `GET /api/admin/export` 流式下载；`sproxy admin backup` 一键备份 SQLite |
| **Token 统计** | 同步/流式（SSE）请求均精确统计 input/output tokens，不缓冲不延迟 |
| **费用估算** | 按模型定价配置，Dashboard 实时显示 USD 消耗 |
| **用户配额** | 按分组设置每日/每月 token 上限，超额返回 429 |
| **速率限制** | 每用户每分钟请求数（RPM）限制，滑动窗口算法 |
| **负载均衡** | cproxy↔sproxy、sproxy↔LLM 两级负载均衡，加权随机策略 |
| **健康检查** | 主动（Smart Probe 自动发现 + GET /health 显式路径）+ 被动（连续失败熔断）双重检查；WebUI/API 变更后运行时同步（v2.19.0）；Smart Probe（v2.24.5+）无需配置路径自动探活 |
| **集群模式** | primary + worker 多节点（SQLite），或 peer 对等节点（PostgreSQL），路由表自动下发给 cproxy |
| **Web Dashboard** | Go 模板 + Tailwind CSS，内嵌二进制，无需前端构建 |
| **Admin CLI** | 命令行管理用户、分组、配额、统计 |
| **Prometheus 指标** | GET /metrics，标准文本格式，可接 Grafana；含 DB 文件大小、配额缓存命中率、心跳延迟 |
| **Webhook 告警** | 节点故障、配额超限等事件推送到 Slack/飞书/企业微信 |
| **OpenTelemetry 追踪** | 可选启用，支持 gRPC/HTTP/stdout exporter；自动对接 Jaeger、Grafana Tempo 等后端 |
| **登录频率限制** | 每 IP 5 次失败后锁定 5 分钟，防暴力破解 |
| **管理审计日志** | 所有用户/分组增删改操作记录到 audit_logs，可在 Dashboard 查看 |
| **Token 自动刷新** | cproxy 自动检测 token 过期，5s 内向 sproxy 换取新 token |
| **趋势图表（F-10）** | Dashboard 概览页显示 Token 用量趋势、费用趋势、Top 5 用户图表，支持 7/30/90 天切换 |
| **用户自助页面（F-10）** | 普通用户可查看自己的配额状态、用量历史，访问 `/dashboard/my-usage` 或调用 `/api/user/*` API |
| **对话内容追踪** | 按用户隔离记录完整对话内容（JSON 文件），支持非流式和 SSE 流式双路径捕获，`sproxy admin track` 管理 |
| **训练语料采集** | 异步采集 LLM 请求/响应对为 JSONL 训练语料，质量过滤（错误/短回复/排除分组），按日期+大小文件轮转，`corpus:` 配置段启用 |
| **动态 LLM Target 管理** | 配置文件 + 数据库双来源，支持运行时增删 LLM 节点，用户/分组级绑定，自动健康检查与负载均衡 |
| **告警页面** | Dashboard 实时 WARN/ERROR 日志查看器，通过 SSE 推送，无需刷新 |
| **批量导入** | `sproxy admin import <file>` 从模板文件批量创建分组/用户，支持 `--dry-run` 预览和 WebUI 操作 |
| **协议转换进阶** | 图片内容块转换（Anthropic→OpenAI）、OpenAI 错误响应转 Anthropic 格式、model_mapping 配置、prefill/thinking 拒绝（HTTP 400） |
| **OtoA 双向协议转换（v2.10.0）** | OpenAI 格式客户端（Cursor、Continue.dev 等）透明访问 Anthropic 端点，请求路径 `/v1/chat/completions` + target `provider: anthropic` 自动触发 |
| **Direct Proxy（v2.9.0）** | `sk-pp-` API Key 直连，无需 cproxy；访问 `/keygen/` 自助生成 Key；同时支持 OpenAI (`/v1/`) 和 Anthropic (`/anthropic/`) 两种头格式 |
| **Worker 节点一致性（v2.12.0）** | ConfigSyncer 每 30s 从 Primary 拉取配置快照同步到本地 DB；Worker 写操作全部封锁（403 `worker_read_only`）；WebUI 只读横幅；统计响应头标注；CLI Primary-only 命令标注 |
| **PostgreSQL 支持（v2.13.0）** | 新增 `driver: postgres` 选项，所有节点共享同一 PostgreSQL 实例，彻底解决 Worker 30s 一致性窗口；SQLite 保持默认向后兼容；支持 DSN 或独立字段（host/port/user/password/dbname/sslmode）；PG 模式下 ConfigSyncer 自动禁用 |
| **Peer Mode 对等节点（v2.14.0）** | PG 模式下自动启用 `role: "peer"`；所有节点完全对等，任意节点可处理管理操作；`PGPeerRegistry` 通过 `peers` 表实现分布式节点发现（心跳/驱逐/优雅注销）；无写封锁、无 ConfigSyncer、无 Reporter |
| **ConfigSyncer URL 冲突修复（v2.14.1）** | 修复 SQLite 集群模式下 Worker 节点 ConfigSyncer 同步 LLM targets 时 UNIQUE constraint 错误；冲突键从 `ON CONFLICT(id)` 改为 `ON CONFLICT(url)` |
| **HMAC-SHA256 Keygen（v2.15.0）** | 替换指纹嵌入算法，消除碰撞漏洞（alice123 vs 321ecila）；HMAC-SHA256 + Base62 编码（48字符）；确定性生成（相同用户名+secret→相同key）；256位安全强度（碰撞概率 < 2^-143）；配置新增 `auth.keygen_secret` 必填字段（≥32字符）；Breaking Change：所有旧 sk-pp- key 立即失效 |
| **训练语料采集 Corpus（v2.16.0）** | 异步采集 LLM 请求/响应对为 JSONL 训练语料；质量过滤（错误响应、短回复、排除分组）；支持 Anthropic/OpenAI/Ollama 三种 SSE 格式；按日期+大小自动文件轮转；记录 `model_requested` 和 `model_actual` 双模型字段；零阻塞热路径（channel + worker goroutine） |
| **LLM 故障转移增强（v2.17.0）** | 新增 `llm.retry_on_status` 配置，支持对指定 HTTP 状态码（如 429 配额耗尽）触发 try-next；遍历所有 target 一次，找到可用端点；空列表默认关闭，完全向后兼容；每次重试打印结构化日志（reason=HTTP 429 / connection error）；失败 target 加入 tried 列表防止重复尝试 |
| **语义路由（v2.18.0）** | 根据请求 messages 语义意图缩窄 LLM 候选池；分类器复用现有 LB（防递归）；规则来自 YAML + DB（DB 优先，热更新）；`sproxy admin route` CLI + REST API `/api/admin/semantic-routes` 管理规则；任何分类失败自动降级到完整候选池；仅对无绑定用户生效；`semantic_router:` 配置段启用 |
| **WebUI 健康检查运行时同步（v2.19.0）** | 修复通过 WebUI/API 添加目标后健康检查永远不健康的问题；`SyncLLMTargets()` 在每次 Create/Update/Delete/Enable/Disable 后同步 `llmBalancer` 和 `llmHC`；有 `HealthCheckPath` 的新节点以 `Healthy=false` 入场并立即触发单次主动检查（秒级，无需等 30s ticker）；无 `HealthCheckPath` 的节点乐观初始化依赖被动熔断；存量节点的健康/排水状态在 Sync 时完整保留；新增 `lb.UpdateHealthPaths()`、`lb.CheckTarget()`、`proxy.SyncLLMTargets()` |
| **reportgen PostgreSQL 支持（v2.24.2）** | reportgen 工具支持 SQLite + PostgreSQL 双驱动；PostgreSQL 连接：`-pg-dsn` 或 `-pg-host/-pg-port/-pg-user/-pg-password/-pg-dbname/-pg-sslmode`；SQL 方言自动适配（占位符 `?`→`$N`、日期函数差异）；完全向后兼容 SQLite 工作流 |
| **reportgen LLM 直连参数（v2.24.3）** | 新增 `-llm-url`、`-llm-key`、`-llm-model` 三个命令行参数；优先于数据库配置，便于本地开发；支持 OpenAI 兼容端点（`/v1/chat/completions`）和 Anthropic native 端点（`/v1/messages`）；连接失败自动降级为纯规则分析 |
| **SQLite 时区修复（v2.24.4）** | `UsageLog.BeforeCreate` GORM hook 强制 `CreatedAt.UTC()`；`usage_repo.go` 9 个时间过滤方法统一 `toUTC()`；彻底修复非 UTC 系统上 token 统计返回 0 的 Bug；reportgen 查询失败从静默丢弃改为 stderr WARNING |

---

## 快速开始

### 前置条件

- Go 1.21+（仅编译时需要，二进制无其他依赖）
- 一台可被开发者访问的服务器（部署 sproxy）
- Anthropic API Key

### 1. 编译

```bash
git clone https://github.com/l17728/pairproxy.git
cd pairproxy
make build
# 输出：bin/cproxy  bin/sproxy
```

跨平台发布包（Linux/macOS/Windows × amd64/arm64）：

```bash
make release
# 输出：dist/pairproxy-linux-amd64.tar.gz 等
```

### 2. 部署 sproxy（服务端，一次性操作）

```bash
# 生成 admin 密码 hash
./bin/sproxy hash-password
# 输入密码后得到 $2a$10$... 格式的 hash

# 准备环境变量
export ANTHROPIC_API_KEY_1="sk-ant-api03-..."
export JWT_SECRET="$(openssl rand -hex 32)"
export ADMIN_PASSWORD_HASH='$2a$10$...'   # 上一步输出的 hash

# 复制并编辑配置
cp config/sproxy.yaml.example sproxy.yaml
# 至少修改：llm.targets[0].url、database.path

# 启动
./bin/sproxy start --config sproxy.yaml
# INFO  sproxy listening  addr=0.0.0.0:9000
# INFO  dashboard enabled  path=/dashboard/
```

**创建用户和分组**（服务启动后操作）：

```bash
# 创建分组（设置每日 100万 token 上限，每分钟 60 次请求）
./bin/sproxy admin group add engineering \
  --daily-limit 1000000 --monthly-limit 20000000 --rpm 60

# 创建用户
./bin/sproxy admin user add alice --group engineering
# Password: ****
```

### 3. 用户本地配置（每位开发者执行一次）

```bash
# 下载 cproxy 二进制（或自行编译）

# 登录，获取 JWT
cproxy login --server http://proxy.company.com:9000
# Username: alice
# Password: ****
# ✓ Login successful. Token saved to ~/.config/pairproxy/token.json

# 启动本地代理
cproxy start
# cproxy listening on http://127.0.0.1:8080
```

### 4. 配置 Claude Code

```bash
# 在 shell profile 中添加（或在 IDE 中设置环境变量）
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_API_KEY=any-placeholder   # 值任意填写，cproxy 会用 JWT 替换真实认证头
```

> **说明**：`ANTHROPIC_API_KEY` 仅用于满足 Claude Code SDK 的非空校验，其值不会被发送给 Anthropic。cproxy 会将此 header 替换为用户 JWT，sproxy 再将其替换为真实 API Key。

之后正常使用 Claude Code，所有请求自动经过代理统计。

---

## 架构说明

### Header 替换流程

```
Claude Code 发出:
  POST http://127.0.0.1:8080/v1/messages
  Authorization: Bearer any-placeholder        ← 假 key

cproxy 转发给 sproxy:
  POST http://proxy.company.com:9000/v1/messages
  X-PairProxy-Auth: eyJhbGc...               ← 用户 JWT（替换原 Authorization）

sproxy 转发给 Anthropic:
  POST https://api.anthropic.com/v1/messages
  Authorization: Bearer sk-ant-REAL-KEY       ← 真实 API Key（替换 X-PairProxy-Auth）
```

用户永远看不到真实 API Key。

### JWT 自动刷新机制

cproxy 启动时读取 `~/.pairproxy/token.json`，并在请求前检查 token 有效期：

```
token 有效期 > refresh_threshold（默认30分钟）
  → 直接使用当前 access token

token 有效期 ≤ refresh_threshold
  → 用 refresh_token 向 sproxy 换取新的 access token
  → 新 token 保存到 token.json

refresh_token 也已过期（> 7天未使用）
  → 返回 401，提示用户重新执行 cproxy login
```

access token 有效期 24h，refresh token 7天，正常使用下用户无需手动登录。

### Streaming Token 捕获

sproxy 使用 `TeeResponseWriter` 包装响应流：

```
Anthropic SSE 流
  │
  ├── 同步写给 Claude Code（零缓冲，不增加延迟）
  │
  └── 同时解析 SSE 事件行
        message_start  → 记录 input_tokens
        message_delta  → 记录 output_tokens
        message_stop   → 异步写入 SQLite usage_logs
```

### 集群路由表推送

```
cproxy 启动
  │
  └── 连接 sproxy primary（cfg.sproxy.primary）
        │
        sproxy primary 在响应头中注入路由表:
          X-Routing-Version: 3
          X-Routing-Update: base64(JSON{entries:[sp1,sp2,sp3]})
        │
        cproxy 解析路由头，更新本地 Balancer
        │
        后续请求自动负载均衡到 sp1/sp2/sp3
```

---

## Web Dashboard

访问 `http://<sproxy-host>:9000/dashboard/`，使用 admin 密码登录。

| 页面 | 内容 |
|------|------|
| **概览** | 今日 token 总量、请求次数、成功率、活跃用户数、估算费用；最近请求列表 |
| **用户** | 用量排行、创建用户、启用/禁用、重置密码 |
| **分组** | 分组管理、设置每日/每月 token 上限和 RPM |
| **日志** | 最近 N 条请求记录，支持按用户 ID 过滤 |
| **审计** | 管理员操作审计日志（用户/分组增删改操作） |
| **告警** | 实时 WARN/ERROR 日志流，SSE 推送（v2.8.0）|
| **批量导入** | 从模板文件批量创建分组和用户，支持 dry-run 预览（v2.8.0） |

---

## Admin CLI

```bash
# 用户管理
sproxy admin user add <username> [--group <group>]
sproxy admin user list [--group <group>]
sproxy admin user disable <username>
sproxy admin user enable <username>
sproxy admin user reset-password <username>

# 分组管理
sproxy admin group add <name> [--daily-limit <n>] [--monthly-limit <n>] [--rpm <n>]
sproxy admin group list
sproxy admin group set-quota <name> [--daily <n>] [--monthly <n>] [--rpm <n>]

# 统计查询
sproxy admin stats [--user <username>] [--days <n>]

# Token 管理
sproxy admin token revoke <username>   # 强制下线用户

# 对话内容追踪
sproxy admin track enable <username>   # 开启指定用户的追踪
sproxy admin track disable <username>  # 关闭追踪
sproxy admin track list                # 查看所有被追踪用户
sproxy admin track show <username>     # 查看追踪记录
sproxy admin track clear <username>    # 清除历史记录

# 批量导入（v2.8.0）
sproxy admin import users.txt          # 从模板文件批量创建分组/用户
sproxy admin import --dry-run users.txt  # 预览（不实际写入）

# 工具
sproxy hash-password [--password <pwd>]   # 生成 bcrypt hash
sproxy version                            # 查看版本信息
```

```bash
# cproxy 命令
cproxy login --server <url>   # 登录并保存 token
cproxy start [--config <path>]# 启动本地代理
cproxy status                 # 查看 token 状态
cproxy logout                 # 登出并撤销 refresh token
cproxy version                # 查看版本信息
```

---

## 配置文件

### cproxy（`~/.config/pairproxy/cproxy.yaml`）

```yaml
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://proxy.company.com:9000"  # 必填
  lb_strategy: "round_robin"
  health_check_interval: 30s
  request_timeout: 300s

auth:
  refresh_threshold: 30m   # token 过期前多久自动刷新

log:
  level: "info"
```

### sproxy（`sproxy.yaml`）

```yaml
listen:
  host: "0.0.0.0"
  port: 9000

llm:
  lb_strategy: "round_robin"
  request_timeout: 300s
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"   # 从环境变量读取
      weight: 1

database:
  path: "/var/lib/pairproxy/pairproxy.db"
  write_buffer_size: 200
  flush_interval: 5s

auth:
  jwt_secret: "${JWT_SECRET}"           # 必填，建议从环境变量注入
  access_token_ttl: 24h
  refresh_token_ttl: 168h

admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"

cluster:
  role: "primary"                        # primary | worker
  self_addr: "http://sp1.company.com:9000"
  self_weight: 50
  alert_webhook: ""                      # Slack/飞书 webhook，留空不发告警

dashboard:
  enabled: true

# 训练语料采集（可选，默认关闭）
corpus:
  enabled: false
  path: "./corpus/"
  min_output_tokens: 50
  # exclude_groups: ["test"]

pricing:
  default_input_per_1k: 0.003
  default_output_per_1k: 0.015
  models:
    claude-sonnet-4-5:
      input_per_1k: 0.003
      output_per_1k: 0.015
    claude-opus-4-5:
      input_per_1k: 0.015
      output_per_1k: 0.075

log:
  level: "info"
```

完整注释版见 [`config/sproxy.yaml.example`](config/sproxy.yaml.example) 和 [`config/cproxy.yaml.example`](config/cproxy.yaml.example)。

---

## 集群部署

```
                    ┌───────────────────────────────┐
[开发者 A] cproxy ──┤                               │
[开发者 B] cproxy ──┤   sp-1  primary  :9000        │──▶ Anthropic API
[开发者 C] cproxy ──┤   sp-2  worker   :9000        │──▶ Anthropic API
                    │   sp-3  worker   :9000        │──▶ Anthropic API
                    └──────────────┬────────────────┘
                                   │
                            Web Dashboard
                          http://sp-1:9000/dashboard/
```

worker 节点额外配置：

```yaml
cluster:
  role: "worker"
  primary: "http://sp1.company.com:9000"   # primary 地址
  self_addr: "http://sp2.company.com:9000"
  self_weight: 50

dashboard:
  enabled: false   # worker 不开 Dashboard
```

worker 节点通过心跳向 primary 注册，primary 将路由表下发给所有 cproxy，实现自动感知和负载均衡。

**v2.12.0 起**：Worker 节点每 30s 从 Primary 拉取配置快照（用户/分组/LLM绑定），保持数据一致性。所有写操作（用户管理、配额设置等）必须在 Primary 节点执行，Worker 节点写操作返回 `403 worker_read_only`。

---

## Docker 部署

### 快速启动

```bash
# 1. 准备配置文件和密钥
cp config/sproxy.yaml.example sproxy.yaml   # 编辑填入实际地址
cp .env.example .env                        # 编辑填入 API Key / JWT Secret

# 2. 启动
docker compose up -d

# 3. 查看日志
docker compose logs -f sproxy
```

### 手动构建镜像

```bash
# 构建 sproxy（注入版本信息）
docker build \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILT=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t pairproxy/sproxy .

# 运行
docker run -d \
  --name sproxy \
  -p 9000:9000 \
  --env-file .env \
  -v $(pwd)/sproxy.yaml:/etc/pairproxy/sproxy.yaml:ro \
  -v sproxy-data:/var/lib/pairproxy \
  pairproxy/sproxy
```

### 镜像说明

最终镜像基于 `gcr.io/distroless/static-debian12`：含 CA 证书（HTTPS 请求 Anthropic 需要）、无 shell、以 UID 65532 非 root 用户运行。二进制为全静态编译（`CGO_ENABLED=0`），镜像约 **15 MB**。

| 文件 | 说明 |
|------|------|
| `Dockerfile` | 多阶段构建，`--build-arg BINARY=cproxy\|sproxy` |
| `.dockerignore` | 排除编译产物、DB 文件、.git 等 |
| `docker-compose.yml` | 单机部署，含 worker 节点注释模板 |
| `.env.example` | 环境变量模板（复制为 `.env` 后填写真实值） |

---

## systemd 部署（Linux 生产环境）

### 安装

```bash
# 1. 复制二进制
sudo cp bin/sproxy /usr/local/bin/sproxy
sudo chmod +x /usr/local/bin/sproxy

# 2. 创建最小权限系统用户
sudo useradd --system --no-create-home --shell /usr/sbin/nologin pairproxy

# 3. 创建目录
sudo mkdir -p /etc/pairproxy /var/lib/pairproxy
sudo chown pairproxy:pairproxy /var/lib/pairproxy
sudo chmod 750 /var/lib/pairproxy

# 4. 复制配置文件
sudo cp config/sproxy.yaml.example /etc/pairproxy/sproxy.yaml
sudo chown root:pairproxy /etc/pairproxy/sproxy.yaml
sudo chmod 640 /etc/pairproxy/sproxy.yaml
# 编辑 sproxy.yaml，填写实际地址和路径

# 5. 创建环境变量文件（存放密钥，不进配置文件）
sudo install -m 640 -o root -g pairproxy /dev/null /etc/pairproxy/sproxy.env
sudo tee /etc/pairproxy/sproxy.env <<'EOF'
ANTHROPIC_API_KEY_1=sk-ant-api03-...
JWT_SECRET=your-32-char-random-secret
ADMIN_PASSWORD_HASH=$2a$10$...
EOF

# 6. 安装 service 文件并启动
sudo cp config/sproxy.service /etc/systemd/system/sproxy.service
sudo systemctl daemon-reload
sudo systemctl enable --now sproxy
```

### 常用运维命令

```bash
sudo systemctl status sproxy          # 查看运行状态
sudo journalctl -u sproxy -f          # 实时查看日志
sudo journalctl -u sproxy --since today  # 今日日志
sudo systemctl reload sproxy          # 热重载（发送 SIGHUP，仅重载 log.level）
sudo systemctl restart sproxy         # 重启服务（完整配置重载）
```

service 文件默认启用了以下安全加固：`NoNewPrivileges`、`ProtectSystem=strict`、`PrivateTmp`、`MemoryDenyWriteExecute`、系统调用白名单过滤、禁止 core dump（防止内存中密钥落盘）。

完整 service 文件见 [`config/sproxy.service`](config/sproxy.service)，worker 节点见 [`config/sproxy-worker.service`](config/sproxy-worker.service)。

---

## Prometheus 指标

```
GET http://<sproxy-host>:9000/metrics
```

```
# HELP pairproxy_tokens_today Total tokens today
pairproxy_tokens_today{type="input"} 1234567
pairproxy_tokens_today{type="output"} 345678
# HELP pairproxy_requests_today Total requests today
pairproxy_requests_today{type="total"} 1000
pairproxy_requests_today{type="error"} 5
# HELP pairproxy_active_users_today Active users today
pairproxy_active_users_today 12
# HELP pairproxy_cost_usd_today Estimated cost today (USD)
pairproxy_cost_usd_today 4.2100
```

指标每 30 秒缓存一次，避免频繁查询 DB。

---

## Makefile 常用命令

```bash
make build          # 编译当前平台二进制到 bin/
make test           # 运行全部测试
make test-race      # 竞态检测
make test-cover     # 生成覆盖率报告
make release        # 跨平台发布包（dist/）
make vet fmt lint   # 代码检查
make bcrypt-hash    # 生成 admin 密码 hash
make clean          # 清理 bin/
```

---

## 发布新版本

确认所有改动已合并到 `main` 后，推送一个符合语义版本的 tag，CI 自动完成剩余全部工作：

```bash
git tag v1.2.3 && git push origin v1.2.3
```

GitHub Actions [`release.yml`](.github/workflows/release.yml) 随后自动执行：

1. **交叉编译** 5 个平台的二进制（Linux/macOS/Windows × amd64/arm64）
2. **生成** `SHA256SUMS.txt` 校验文件
3. **创建** GitHub Release，附上所有产物和自动生成的 release notes
4. **构建** 多架构 Docker 镜像（`linux/amd64` + `linux/arm64`）并推送到 `ghcr.io/l17728/pairproxy`，标签包含 `v1.2.3`、`1.2`、`1`、`latest`

---

## 项目结构

```
pairproxy/
├── cmd/
│   ├── cproxy/main.go        # cproxy CLI 入口
│   └── sproxy/main.go        # sproxy CLI 入口 + admin 子命令
├── internal/
│   ├── auth/                 # JWT 管理、bcrypt、本地 token 文件
│   ├── proxy/                # cproxy/sproxy 核心 HTTP 处理器 + 中间件 + 协议转换
│   ├── keygen/               # sk-pp- API Key 生成、验证、LRU 缓存（v2.9.0）
│   ├── tap/                  # TeeResponseWriter + Anthropic/OpenAI SSE 解析器
│   ├── lb/                   # Balancer 接口、加权随机、健康检查
│   ├── cluster/              # 路由表、PeerRegistry、Reporter
│   ├── quota/                # 配额检查、速率限制、中间件
│   ├── db/                   # SQLite + GORM 模型、UserRepo、UsageRepo
│   ├── api/                  # AuthHandler、AdminHandler、ClusterHandler、KeygenHandler
│   ├── dashboard/            # Web Dashboard（Go 模板 + embed）
│   ├── eventlog/             # SSE 告警日志 Hub（v2.8.0）
│   ├── metrics/              # Prometheus 格式 /metrics 端点
│   ├── alert/                # Webhook 告警通知器
│   ├── track/                # 对话内容追踪（按用户隔离的 JSON 文件记录）
│   ├── corpus/               # 训练语料采集（JSONL 格式，用于模型蒸馏）
│   ├── config/               # YAML 配置加载（支持 ${ENV_VAR} 展开）
│   └── version/              # 版本信息
├── config/
│   ├── cproxy.yaml.example
│   ├── sproxy.yaml.example
│   └── sproxy-worker.yaml.example
├── Makefile
└── go.mod                    # module: github.com/l17728/pairproxy
```

---

## 文档

| 文档 | 说明 |
|------|------|
| [docs/manual.md](docs/manual.md) | 完整用户手册（部署、配置、CLI 命令、Dashboard 使用） |
| [docs/API.md](docs/API.md) | REST API 参考（Auth、Admin、Stats 端点） |
| [docs/CLUSTER_DESIGN.md](docs/CLUSTER_DESIGN.md) | 多节点集群架构设计（路由分发、心跳、故障恢复） |
| [docs/SECURITY.md](docs/SECURITY.md) | 安全模型说明（JWT 防护、集群 API 鉴权、TLS 配置、密钥轮换） |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | 故障排查手册（启动失败、JWT 错误、配额超限、集群问题、性能调优） |
| [docs/UPGRADE.md](docs/UPGRADE.md) | 版本升级指南（Schema 变更、回滚方法、不兼容变更清单） |
| [docs/PERFORMANCE.md](docs/PERFORMANCE.md) | 性能调优指南（缓冲区、WAL 模式、连接池、缓存调优） |
| [docs/GO_CONCURRENCY_TEACHING_MATERIAL.md](docs/GO_CONCURRENCY_TEACHING_MATERIAL.md) | Go 并发编程教材（WaitGroup 同步、Race 调试、GitHub 工作流，含 Mermaid 流程图） |

---

## 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| HTTP 反向代理 | `net/http/httputil.ReverseProxy` | 标准库，SSE 天然支持 |
| 数据库 | SQLite + GORM (`glebarez/sqlite`) / PostgreSQL (`gorm.io/driver/postgres`) | 纯 Go，无 CGO；SQLite 默认单文件部署，PostgreSQL 支持多节点共享（v2.13.0） |
| CLI | `cobra` | 标准 Go CLI 框架 |
| 日志 | `zap` | 结构化日志，高性能 |
| 密码 | bcrypt (`golang.org/x/crypto`) | 行业标准 |
| 前端 | Go HTML 模板 + Tailwind CSS CDN | 无需构建，内嵌二进制 |

---

## License

[Apache License 2.0](LICENSE)
