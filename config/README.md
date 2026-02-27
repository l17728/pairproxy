# PairProxy 配置文件说明

## 文件列表

| 文件 | 用途 |
|------|------|
| `cproxy.yaml.example` | 开发者本地运行的 c-proxy 配置 |
| `sproxy.yaml.example` | 服务端 s-proxy 主节点（primary）配置 |
| `sproxy-worker.yaml.example` | 服务端 s-proxy 工作节点（worker）配置 |

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

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `listen.port` | `9000` | 服务监听端口 |
| `llm.targets` | — | LLM API 节点列表（**必填**） |
| `database.path` | — | SQLite 文件路径（**必填**） |
| `auth.jwt_secret` | — | JWT 密钥（**必填**，建议从环境变量注入） |
| `admin.password_hash` | — | Admin 面板密码 hash（**必填**） |
| `cluster.role` | `primary` | `primary` 或 `worker` |
| `pricing.models` | — | 按模型定价，用于 Dashboard 费用统计 |
| `cluster.alert_webhook` | — | 告警 Webhook（Slack/飞书等，留空不发） |

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
