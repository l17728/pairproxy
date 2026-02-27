# PairProxy

> 企业级 Claude Code 透明代理 — 统一管控 LLM API Key，精确追踪 token 用量，零侵入接入。

```
┌─────────────┐   HTTP/SSE    ┌───────────────────────────┐   HTTPS   ┌──────────────────┐
│ Claude Code │──────────────▶│  cproxy  127.0.0.1:8080   │──────────▶│  sproxy  :9000   │──▶ Anthropic API
│ (code agent)│               │  注入用户 JWT              │           │  验证身份 · 配额  │
└─────────────┘               └───────────────────────────┘           │  统计用量 · 转发  │
                                                                       └──────────────────┘
```

**两个组件，解决一个问题**：多人共用 LLM API Key 时，谁用了多少、超没超额、钱花哪了——一目了然。

---

## 功能特性

| 分类 | 功能 |
|------|------|
| **零侵入接入** | 用户只需设置两个环境变量，无需修改 Claude Code 配置 |
| **JWT 认证** | 每位用户独立 JWT，access token 24h，refresh token 7天，支持自动刷新 |
| **Token 统计** | 同步/流式（SSE）请求均精确统计 input/output tokens，不缓冲不延迟 |
| **费用估算** | 按模型定价配置，Dashboard 实时显示 USD 消耗 |
| **用户配额** | 按分组设置每日/每月 token 上限，超额返回 429 |
| **速率限制** | 每用户每分钟请求数（RPM）限制，滑动窗口算法 |
| **负载均衡** | cproxy↔sproxy、sproxy↔LLM 两级负载均衡，加权随机策略 |
| **健康检查** | 主动（GET /health）+ 被动（连续失败熔断）双重检查 |
| **集群模式** | primary + worker 多节点，路由表自动下发给 cproxy |
| **Web Dashboard** | Go 模板 + Tailwind CSS，内嵌二进制，无需前端构建 |
| **Admin CLI** | 命令行管理用户、分组、配额、统计 |
| **Prometheus 指标** | GET /metrics，标准文本格式，可接 Grafana |
| **Webhook 告警** | 节点故障、配额超限等事件推送到 Slack/飞书/企业微信 |

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
export ANTHROPIC_API_KEY=any-placeholder   # 随意填写，cproxy 会替换
```

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

## 项目结构

```
pairproxy/
├── cmd/
│   ├── cproxy/main.go        # cproxy CLI 入口
│   └── sproxy/main.go        # sproxy CLI 入口 + admin 子命令
├── internal/
│   ├── auth/                 # JWT 管理、bcrypt、本地 token 文件
│   ├── proxy/                # cproxy/sproxy 核心 HTTP 处理器 + 中间件
│   ├── tap/                  # TeeResponseWriter + Anthropic SSE 解析器
│   ├── lb/                   # Balancer 接口、加权随机、健康检查
│   ├── cluster/              # 路由表、PeerRegistry、Reporter
│   ├── quota/                # 配额检查、速率限制、中间件
│   ├── db/                   # SQLite + GORM 模型、UserRepo、UsageRepo
│   ├── api/                  # AuthHandler、AdminHandler、ClusterHandler
│   ├── dashboard/            # Web Dashboard（Go 模板 + embed）
│   ├── metrics/              # Prometheus 格式 /metrics 端点
│   ├── alert/                # Webhook 告警通知器
│   ├── config/               # YAML 配置加载（支持 ${ENV_VAR} 展开）
│   └── version/              # 版本信息
├── config/
│   ├── cproxy.yaml.example
│   ├── sproxy.yaml.example
│   └── sproxy-worker.yaml.example
├── Makefile
└── go.mod                    # module: github.com/pairproxy/pairproxy
```

---

## 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| HTTP 反向代理 | `net/http/httputil.ReverseProxy` | 标准库，SSE 天然支持 |
| 数据库 | SQLite + GORM (`glebarez/sqlite`) | 纯 Go，无 CGO，单文件部署 |
| CLI | `cobra` | 标准 Go CLI 框架 |
| 日志 | `zap` | 结构化日志，高性能 |
| 密码 | bcrypt (`golang.org/x/crypto`) | 行业标准 |
| 前端 | Go HTML 模板 + Tailwind CSS CDN | 无需构建，内嵌二进制 |

---

## License

[Apache License 2.0](LICENSE)
