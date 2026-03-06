# PairProxy 用户手册

**版本 v2.1.0**

---

## 阅读指南

**本手册面向两类读者，请按角色直接跳转：**

| 您的角色 | 您要做的事 | 阅读章节 |
|----------|-----------|---------|
| **管理员**（IT 或技术负责人，负责部署） | 在服务器上安装 sproxy，创建用户账号，发放账号给同事 | 第 1、2、3、4、5 章，以及第 9、10 章 |
| **开发者**（使用 Claude Code 的团队成员） | 在自己电脑上安装 cproxy，登录，设置环境变量 | 第 1 章（了解是什么），直接跳到**第 6 章** |

> 如果您是第一次接触这套系统，从第 1 章读起，大约 15 分钟后即可完成全部配置。

---

## 目录

1. [产品简介与名词解释](#1-产品简介与名词解释)
2. [部署前准备（管理员）](#2-部署前准备管理员)
3. [安装与启动 sproxy（管理员）](#3-安装与启动-sproxy管理员)
4. [创建用户和分组（管理员）](#4-创建用户和分组管理员)
5. [高级部署选项（管理员）](#5-高级部署选项管理员)
6. [安装与启动 cproxy（开发者）](#6-安装与启动-cproxy开发者)
7. [配置 Claude Code（开发者）](#7-配置-claude-code开发者)
8. [验证整条链路是否正常](#8-验证整条链路是否正常)
9. [Web Dashboard 使用指南](#9-web-dashboard-使用指南)
   - [9.6 概览趋势图表](#96-概览趋势图表f-10)
   - [9.7 用户自助用量页面](#97-用户自助用量页面f-10)
10. [监控与告警](#10-监控与告警)
    - [10.1 Prometheus 指标接入](#101-prometheus-指标接入)
    - [10.2 Webhook 告警](#102-webhook-告警)
    - [10.3 动态调整日志级别（SIGHUP）](#103-动态调整日志级别不重启服务)
11. [升级指南](#11-升级指南)
12. [常见问题与排查](#12-常见问题与排查)
13. [安全建议](#13-安全建议)
14. [配置文件完整参考](#14-配置文件完整参考)

---

## 1. 产品简介与名词解释

### 1.1 PairProxy 解决什么问题

当一个团队有多名开发者同时使用 Claude Code 时，管理员面临三个棘手问题：

- **API Key 管理难**：Anthropic API Key 需要分发给每个人，一旦泄露无法追责，更换 Key 又要通知所有人
- **用量不透明**：不知道谁用了多少，每月账单来了才发现超支
- **无法限流**：某个同事一天用了 100 万 token，没有任何机制能提前拦截

PairProxy 在 Claude Code 和 Anthropic API 之间插入一个透明代理层，彻底解决以上问题。**开发者无需改变使用习惯**，只需一次性配置两个环境变量。

### 1.2 系统是如何运作的

```
您的电脑                        公司服务器                    Anthropic
┌──────────────┐              ┌──────────────┐              ┌──────────────┐
│  Claude Code │──请求────────▶│   sproxy     │──请求────────▶│  Claude API  │
│              │              │  验证身份     │              │              │
│   cproxy     │              │  检查配额     │              └──────────────┘
│  (本地代理)  │◀─────────────│  统计用量     │◀─────────────
└──────────────┘   响应        └──────────────┘   响应
```

- **cproxy**（客户端代理）：运行在每位开发者的电脑上，拦截 Claude Code 的请求，用用户身份标识替换 API Key，转发给 sproxy
- **sproxy**（服务端代理）：运行在公司服务器上，验证用户身份，检查是否超额，注入真实 API Key，将请求发给 Anthropic，并记录本次用量

**最关键的一点**：真实的 API Key 存放在服务器上，开发者的电脑上永远看不到它。

### 1.3 名词解释

阅读本手册前，先了解以下几个词：

| 词汇 | 含义 |
|------|------|
| **Token（令牌/词元）** | 两种含义：①"身份令牌"——类似登录票据，证明您是谁；②"API Token"——Anthropic 计量用量的单位，大约每 1 个 token 对应 0.75 个英文单词或 1~2 个中文字 |
| **JWT** | JSON Web Token，一种加密的身份令牌格式。cproxy 登录后会获得一个 JWT，用来证明您的身份，有效期 24 小时，会自动续期 |
| **access token / refresh token** | access token 是短期令牌（24 小时），用于每次请求；refresh token 是长期令牌（7 天），用于在 access token 快过期时自动换取新的 access token |
| **bcrypt hash** | 密码存储方式。系统不保存明文密码，而是保存密码经过特殊加密后的"指纹"（即 hash）。登录时系统比对指纹，即使数据库泄露也无法还原原始密码 |
| **sproxy** | Server Proxy，服务端代理，部署在公司服务器上 |
| **cproxy** | Client Proxy，客户端代理，每位开发者在自己电脑上运行 |
| **Dashboard** | sproxy 内置的网页管理后台，可在浏览器中查看用量、管理用户 |
| **分组（Group）** | 用于批量设置配额的概念。同一分组内的用户共享同一套配额规则，但用量是按人单独统计的 |
| **配额** | 使用上限。可以限制每个用户每天/每月最多消耗多少 token，以及每分钟最多发多少次请求 |

### 1.4 功能概览

| 功能 | 说明 |
|------|------|
| **零侵入接入** | 开发者只需设置两个环境变量，无需改动 Claude Code 本身 |
| **身份认证** | 每人独立账号，密码登录，令牌自动续期 |
| **精确统计用量** | 每次请求的输入/输出 token 数、耗时、费用均有记录 |
| **配额管理** | 可按分组设置每日/每月 token 上限和每分钟请求次数上限 |
| **Web 管理后台** | 浏览器中直接查看用量排行、管理用户、调整配额 |
| **费用估算** | 按模型定价实时计算 USD 消耗 |
| **告警通知** | 超额、节点故障等事件可推送到 Slack/飞书/企业微信 |
| **集群模式** | 支持多台 sproxy 服务器协同工作，适合大规模团队 |

---

## 2. 部署前准备（管理员）

> 本章由**管理员**（负责部署 sproxy 的人）阅读。

### 2.1 您需要准备什么

在开始之前，请确认以下内容已经就绪：

**一台服务器**，满足：
- 操作系统：Linux（推荐 Ubuntu 20.04+）、macOS 或 Windows Server
- 最低配置：1 核 CPU、256 MB 内存、1 GB 可用磁盘
- 网络要求：
  - ①可以被公司内部开发者的电脑访问（内网或公网均可）
  - ②可以访问 `api.anthropic.com`（即能联网）
- 开放端口：9000（或您自定义的端口）

**Anthropic API Key**，从 [https://console.anthropic.com/](https://console.anthropic.com/) 获取，格式为 `sk-ant-api03-...`

**openssl 命令行工具**（用于生成密钥），Linux/macOS 系统通常已预装。Windows 用户可安装 [Git for Windows](https://git-scm.com/download/win)，其中包含 openssl。

### 2.2 开放服务器端口

sproxy 默认监听 9000 端口，部署前需确保防火墙允许此端口的入站流量。

**Linux（使用 ufw）**：

```bash
sudo ufw allow 9000/tcp
sudo ufw status
```

**Linux（使用 firewalld，如 CentOS/RHEL）**：

```bash
sudo firewall-cmd --permanent --add-port=9000/tcp
sudo firewall-cmd --reload
```

**云服务器（阿里云/腾讯云/AWS 等）**：

除了系统防火墙，还需在云控制台的**安全组**（Security Group）中添加入站规则，允许 TCP 9000 端口。具体路径：云控制台 → 实例 → 安全组 → 添加规则 → 入方向 → TCP 9000。

### 2.3 生成必要的密钥

在服务器上运行以下命令，生成两个必要的密钥字符串：

```bash
# 生成 JWT 密钥（用于签发用户令牌，必须保密）
openssl rand -hex 32
# 示例输出：a3f8c2e1d7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0
```

**保存这串字符**，稍后会用到（填入 `JWT_SECRET`）。

---

## 3. 安装与启动 sproxy（管理员）

### 3.1 下载 sproxy

在服务器上执行以下命令（以 Linux x86_64 为例）：

```bash
# 下载最新版本
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-linux-amd64.tar.gz

# 解压
tar xzf pairproxy-linux-amd64.tar.gz

# 安装到系统路径（需要 sudo）
sudo mv sproxy /usr/local/bin/sproxy
sudo chmod +x /usr/local/bin/sproxy

# 验证安装成功
sproxy version
# 应输出版本信息，如：sproxy v1.1.0
```

其他平台下载地址：

| 平台 | 文件名 |
|------|--------|
| Linux x86_64（最常见） | `pairproxy-linux-amd64.tar.gz` |
| Linux ARM64（树莓派等） | `pairproxy-linux-arm64.tar.gz` |
| macOS Intel | `pairproxy-darwin-amd64.tar.gz` |
| macOS Apple Silicon（M1/M2/M3） | `pairproxy-darwin-arm64.tar.gz` |
| Windows | `pairproxy-windows-amd64.zip` |

完整下载列表：[https://github.com/l17728/pairproxy/releases](https://github.com/l17728/pairproxy/releases)

### 3.2 生成 admin 密码

```bash
sproxy hash-password
```

运行后按提示输入您想设置的 admin 面板密码（输入时不显示）：

```
Password: ****（您输入的密码）
Hash: $2a$10$6qC8XLsXMcj0mE5LlQ8xb.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

**把这行以 `$2a$` 开头的 Hash 字符串复制保存**，下一步会用到。

> 这串 Hash 是您密码的"加密指纹"，系统通过比对指纹来验证登录，而不存储明文密码。

### 3.3 创建配置文件

创建 sproxy 的配置文件（建议放在 `/etc/pairproxy/sproxy.yaml`）：

```bash
# 创建配置目录和数据目录
sudo mkdir -p /etc/pairproxy /var/lib/pairproxy

# 创建配置文件
sudo tee /etc/pairproxy/sproxy.yaml > /dev/null <<'EOF'
listen:
  host: "0.0.0.0"
  port: 9000

llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"

database:
  path: "/var/lib/pairproxy/pairproxy.db"

auth:
  jwt_secret: "${JWT_SECRET}"

admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"

cluster:
  role: "primary"

dashboard:
  enabled: true

pricing:
  default_input_per_1k: 0.003
  default_output_per_1k: 0.015
  models:
    claude-opus-4-5:
      input_per_1k: 0.015
      output_per_1k: 0.075
    claude-sonnet-4-5:
      input_per_1k: 0.003
      output_per_1k: 0.015
    claude-haiku-4-5-20251001:
      input_per_1k: 0.0008
      output_per_1k: 0.004
    claude-3-5-sonnet-20241022:
      input_per_1k: 0.003
      output_per_1k: 0.015

log:
  level: "info"
EOF
```

> 配置文件中的 `${变量名}` 格式表示从环境变量读取，真实密钥不写在文件里，更安全。

### 3.4 创建环境变量文件

将真实的密钥保存到独立的环境变量文件（权限更严格，不容易被意外泄露）：

```bash
sudo tee /etc/pairproxy/sproxy.env > /dev/null <<'EOF'
ANTHROPIC_API_KEY_1=sk-ant-api03-你的真实APIKey
JWT_SECRET=你在2.3步生成的那串随机字符
ADMIN_PASSWORD_HASH=$2a$10$你在3.2步生成的Hash字符串
EOF

# 设置严格的文件权限（只有 root 和 pairproxy 用户可读）
sudo chmod 640 /etc/pairproxy/sproxy.env
```

> **注意**：`ADMIN_PASSWORD_HASH` 的值以 `$` 开头，在某些情况下需要用单引号包裹，如上面的 `tee` 命令中使用了 `'EOF'`（单引号）来防止 shell 解析 `$` 符号。

### 3.5 首次启动（测试验证）

先手动运行一次，确认配置无误：

```bash
# 临时加载环境变量
export $(sudo cat /etc/pairproxy/sproxy.env | xargs)

# 启动
sproxy start --config /etc/pairproxy/sproxy.yaml
```

看到如下输出说明启动成功：

```
INFO  database migrations done
INFO  dashboard enabled    path=/dashboard/
INFO  sproxy listening     addr=0.0.0.0:9000
```

另开一个终端验证服务正常：

```bash
curl http://localhost:9000/health | jq .
# 应返回：
# {
#   "status": "ok",
#   "version": "v1.2.0 (abc1234) ...",
#   "uptime_seconds": 3,
#   "active_requests": 0,
#   "usage_queue_depth": 0,
#   "db_reachable": true
# }
```

- `status: "ok"` — 服务正常；若 SQLite 数据库不可达则返回 `"degraded"`（HTTP 503）
- `uptime_seconds` — 进程已运行时长（秒）
- `active_requests` — 当前正在处理的代理请求数
- `usage_queue_depth` — 待写入数据库的用量记录积压数，通常为 0
- `db_reachable` — 数据库是否可达（false 时 HTTP 状态码为 503）

用 `Ctrl+C` 停止，继续下一步配置开机自启。

### 3.6 配置开机自启（Linux systemd）

将 sproxy 配置为系统服务，服务器重启后自动运行：

```bash
# 创建专用系统用户（无 shell 登录权限，提高安全性）
sudo useradd --system --no-create-home --shell /usr/sbin/nologin pairproxy

# 设置数据目录权限
sudo chown pairproxy:pairproxy /var/lib/pairproxy

# 调整配置目录权限
sudo chown root:pairproxy /etc/pairproxy/sproxy.yaml
sudo chmod 640 /etc/pairproxy/sproxy.yaml
sudo chown root:pairproxy /etc/pairproxy/sproxy.env
# 权限已在 3.4 步设置

# 创建 systemd service 文件
sudo tee /etc/systemd/system/sproxy.service > /dev/null <<'EOF'
[Unit]
Description=PairProxy sproxy - LLM proxy server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pairproxy
Group=pairproxy
ExecStart=/usr/local/bin/sproxy start --config /etc/pairproxy/sproxy.yaml
Restart=on-failure
RestartSec=5s
EnvironmentFile=/etc/pairproxy/sproxy.env
NoNewPrivileges=yes
ProtectSystem=strict
PrivateTmp=yes
ReadWritePaths=/var/lib/pairproxy

[Install]
WantedBy=multi-user.target
EOF

# 载入配置并启动
sudo systemctl daemon-reload
sudo systemctl enable --now sproxy

# 查看状态
sudo systemctl status sproxy
```

`status` 显示 `Active: active (running)` 即表示成功。

**常用管理命令**：

```bash
sudo systemctl start sproxy      # 启动
sudo systemctl stop sproxy       # 停止
sudo systemctl restart sproxy    # 重启（修改配置后使用）
sudo systemctl status sproxy     # 查看运行状态

sudo journalctl -u sproxy -f                 # 实时查看日志
sudo journalctl -u sproxy --since today      # 查看今日日志
```

### 3.7 在浏览器中访问管理后台

服务启动后，在浏览器中打开：

```
http://服务器IP地址:9000/dashboard/
```

将 `服务器IP地址` 替换为服务器的实际 IP（如 `192.168.1.100` 或 `proxy.yourcompany.com`）。

使用您在第 3.2 步设置的 admin 密码登录。

---

## 4. 创建用户和分组（管理员）

> 以下命令需要在**服务器上**执行。

### 4.1 什么是分组，为什么要先创建分组

分组是配额管理的核心概念。通过分组，您可以对不同类型的用户设置不同的使用上限：

- **工程师组**：每天可用 100 万 token
- **试用组**：每天只能用 1 万 token
- **管理层**：不设限制

同一分组内所有用户遵循相同的配额规则，但用量是**按人单独计算**的（A 用了 50 万不影响 B 的额度）。

**没有分组的用户，没有任何用量限制。**

### 4.2 创建分组

```bash
# 语法
sproxy admin group add <分组名> \
  [--daily-limit <每日token上限>] \
  [--monthly-limit <每月token上限>] \
  [--rpm <每分钟最多请求次数>]
```

**实际示例**：

```bash
# 工程师组：每天 100 万 token，每月 2000 万，每分钟最多 60 次请求
sproxy admin group add engineering \
  --daily-limit 1000000 \
  --monthly-limit 20000000 \
  --rpm 60

# 试用体验组：每天 1 万 token，每分钟最多 10 次
sproxy admin group add trial \
  --daily-limit 10000 \
  --rpm 10

# 无限制组（不加任何 limit 参数）
sproxy admin group add vip
```

查看已创建的分组：

```bash
sproxy admin group list
```

### 4.3 创建用户

```bash
# 创建用户并分配分组
sproxy admin user add <用户名> --group <分组名>

# 程序会提示输入初始密码（输入时不显示字符）
```

**实际示例**：

```bash
sproxy admin user add alice --group engineering
# Password: ****
# ✓ User alice created

sproxy admin user add bob --group trial
# Password: ****
# ✓ User bob created
```

> **提示**：用户名建议使用员工的英文名或拼音，便于在日志和统计中识别。

将每位用户的**账号+密码+服务器地址**告知他们，他们按第 6 章操作即可。

### 4.4 用户管理操作

```bash
# 查看所有用户
sproxy admin user list

# 查看某分组的用户
sproxy admin user list --group engineering

# 禁用用户（立即生效，该用户的请求会被拒绝）
sproxy admin user disable alice

# 重新启用
sproxy admin user enable alice

# 重置用户密码
sproxy admin user reset-password alice
```

**员工离职时**，请务必执行：

```bash
sproxy admin user disable <用户名>
sproxy admin token revoke <用户名>   # 使其现有登录状态立即失效
```

### 4.5 修改分组配额

```bash
# 调整 engineering 分组的每日上限为 200 万 token
sproxy admin group set-quota engineering --daily 2000000

# 取消 trial 分组的月限额（设为 0 表示无限制）
sproxy admin group set-quota trial --monthly 0

# 调整每分钟请求次数限制
sproxy admin group set-quota engineering --rpm 120
```

### 4.6 查看用量统计

```bash
# 查看所有用户最近 7 天的用量
sproxy admin stats --days 7

# 查看某个用户最近 30 天的用量
sproxy admin stats --user alice --days 30
```

---

## 5. 高级部署选项（管理员）

> 本章介绍 Docker 部署和集群部署，适合对 Docker 熟悉的管理员，或团队规模较大时使用。简单场景可跳过此章。

### 5.1 Docker 部署

如果您的服务器已安装 Docker，可以用容器方式运行 sproxy，无需手动安装二进制文件。

**准备工作**：

```bash
# 创建工作目录
mkdir -p ~/pairproxy && cd ~/pairproxy

# 创建环境变量文件
cat > .env <<'EOF'
ANTHROPIC_API_KEY_1=sk-ant-api03-你的APIKey
JWT_SECRET=你生成的随机密钥
ADMIN_PASSWORD_HASH=$2a$10$你生成的Hash
EOF

# 创建配置文件（内容与 3.3 步相同，但数据库路径改为 /data/pairproxy.db）
cat > sproxy.yaml <<'EOF'
listen:
  host: "0.0.0.0"
  port: 9000

llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"

database:
  path: "/data/pairproxy.db"

auth:
  jwt_secret: "${JWT_SECRET}"

admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"

cluster:
  role: "primary"

dashboard:
  enabled: true

pricing:
  default_input_per_1k: 0.003
  default_output_per_1k: 0.015

log:
  level: "info"
EOF
```

**创建 docker-compose.yml 并启动**：

```bash
cat > docker-compose.yml <<'EOF'
version: "3.9"

services:
  sproxy:
    image: ghcr.io/l17728/pairproxy:latest
    restart: unless-stopped
    ports:
      - "9000:9000"
    volumes:
      - ./sproxy.yaml:/etc/pairproxy/sproxy.yaml:ro
      - sproxy-data:/data
    env_file:
      - .env

volumes:
  sproxy-data:
EOF

docker compose up -d
```

验证：

```bash
# 查看日志
docker compose logs sproxy

# 健康检查
curl http://localhost:9000/health
```

常用 Docker 运维命令：

```bash
docker compose logs -f sproxy          # 实时查看日志
docker compose restart sproxy          # 重启
docker compose down                    # 停止并移除容器（数据卷保留）
docker compose pull && docker compose up -d  # 升级到最新版本
```

### 5.2 集群部署（多节点）

当团队规模较大（建议 50 人以上或日请求量较高时），可部署多台 sproxy 节点以提高可用性和并发能力。

**架构说明**：

```
                    ┌─────────────────────────────────────┐
开发者 A 的 cproxy ──▶  sp-1（primary，主节点）:9000       │──▶ Anthropic
开发者 B 的 cproxy ──▶  sp-2（worker，工作节点）:9000      │──▶ Anthropic
开发者 C 的 cproxy ──▶  sp-3（worker，工作节点）:9000      │──▶ Anthropic
                    └──────────────┬──────────────────────┘
                              Web Dashboard
                          只在 primary 上开启
```

- **primary（主节点）**：管理所有节点的注册信息，提供 Dashboard 和 Admin CLI
- **worker（工作节点）**：接受用户请求，每 30 秒向 primary 报告一次心跳和用量

**主节点（sp-1）配置**（在标准 `sproxy.yaml` 基础上修改 cluster 段）：

```yaml
cluster:
  role: "primary"
  self_addr: "http://sp1.company.com:9000"   # 本节点的外部访问地址
  self_weight: 50
  peer_monitor_interval: 30s
```

**工作节点（sp-2、sp-3...）配置**（每个 worker 单独的配置文件）：

```yaml
listen:
  host: "0.0.0.0"
  port: 9000

llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"

database:
  path: "/var/lib/pairproxy/pairproxy.db"

auth:
  jwt_secret: "${JWT_SECRET}"    # ⚠️ 必须与主节点完全相同

admin:
  password_hash: ""              # worker 不需要 admin 密码

cluster:
  role: "worker"                              # 必须是 worker
  primary: "http://sp1.company.com:9000"      # 主节点的地址
  shared_secret: "${CLUSTER_SECRET}"          # ⚠️ 必须与主节点完全相同
  self_addr: "http://sp2.company.com:9000"    # 本节点自身的地址
  self_weight: 50
  report_interval: 30s

dashboard:
  enabled: false    # worker 不开启 Dashboard

log:
  level: "info"
```

> **重要**：
> - 所有节点必须使用**完全相同**的 `JWT_SECRET`，否则用户的登录状态在不同节点间不互认。
> - `CLUSTER_SECRET` 是 worker 向 primary 上报心跳和用量的 HMAC 认证密钥，**worker 模式必填**，且所有节点必须相同。可用 `openssl rand -hex 32` 生成。

worker 节点启动后会自动向 primary 注册，cproxy 会在下一次请求后自动感知到新节点并开始分流。

### 5.3 LDAP / AD 认证集成

默认情况下，sproxy 使用本地数据库（用户名+密码）认证。如果您的团队已有 LDAP 服务器（如 OpenLDAP 或 Active Directory），可以将 sproxy 接入，让员工直接使用公司账号登录，无需单独创建账号。

#### 工作原理

- 用户使用公司 LDAP 账号登录 cproxy（`cproxy login`）
- sproxy 将凭据发送到 LDAP 服务器验证
- **首次登录时**，sproxy 会自动在数据库中创建该用户的记录（JIT 自动配置）
- 后续登录时直接找到已有记录，不再重复创建
- 新创建的用户默认不属于任何分组（无配额限制），管理员可通过 `sproxy admin user` 命令为其分配分组

#### 配置方法

在 `sproxy.yaml` 中添加：

```yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  provider: "ldap"        # 启用 LDAP 认证（默认为 "local"）
  ldap:
    server_addr: "ldap.example.com:389"    # LDAP 服务器地址
    base_dn: "dc=example,dc=com"           # 搜索根 DN
    bind_dn: "cn=service,dc=example,dc=com" # 用于搜索用户的服务账户（空=匿名）
    bind_password: "${LDAP_BIND_PASSWORD}" # 服务账户密码
    user_filter: "(uid=%s)"               # 搜索过滤器（%s 替换为用户名）
    use_tls: false                         # true=使用 LDAPS（端口 636）
```

**Active Directory 配置示例**：

```yaml
auth:
  provider: "ldap"
  ldap:
    server_addr: "ad.company.com:389"
    base_dn: "dc=company,dc=com"
    bind_dn: "cn=sproxy-service,ou=ServiceAccounts,dc=company,dc=com"
    bind_password: "${AD_BIND_PASSWORD}"
    user_filter: "(sAMAccountName=%s)"   # AD 使用 sAMAccountName
    use_tls: false
```

#### 管理 LDAP 用户

LDAP 用户首次登录后，可以通过以下命令管理：

```bash
# 查看已登录的 LDAP 用户
sproxy admin user list

# 为 LDAP 用户分配分组（设置配额）
sproxy admin user list                  # 找到用户名
sproxy admin group list                 # 查看可用分组
# 通过 Admin API 或 Dashboard 修改用户分组

# 禁用某个 LDAP 用户（下次登录将被拒绝）
sproxy admin user disable alice
```

> **提示**：切换回本地认证时，将 `provider` 改为 `"local"` 或删除该字段即可；之前通过 LDAP 创建的用户记录会保留在数据库中，但无法再通过 LDAP 登录（因为他们的 `auth_provider` 标记为 `"ldap"`）。

---

## 6. 安装与启动 cproxy（开发者）

> **从这里开始是开发者的内容**。您需要管理员提供以下信息：
> - sproxy 服务器地址（如 `http://proxy.company.com:9000`）
> - 您的用户名和初始密码

### 6.1 下载并安装 cproxy

根据您的操作系统下载对应文件：

**macOS（Apple Silicon，即 M1/M2/M3 芯片）**：

```bash
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-darwin-arm64.tar.gz
tar xzf pairproxy-darwin-arm64.tar.gz
sudo mv cproxy /usr/local/bin/cproxy
sudo chmod +x /usr/local/bin/cproxy
```

**macOS（Intel 芯片）**：

```bash
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-darwin-amd64.tar.gz
tar xzf pairproxy-darwin-amd64.tar.gz
sudo mv cproxy /usr/local/bin/cproxy
sudo chmod +x /usr/local/bin/cproxy
```

> **macOS 安全提示**：首次运行时 macOS 可能提示"无法验证开发者"，请前往**系统设置 → 隐私与安全性**，找到 cproxy 的拦截提示，点击"仍要打开"。

**Linux**：

```bash
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-linux-amd64.tar.gz
tar xzf pairproxy-linux-amd64.tar.gz
sudo mv cproxy /usr/local/bin/cproxy
sudo chmod +x /usr/local/bin/cproxy
```

**Windows**：

1. 从 [Releases 页面](https://github.com/l17728/pairproxy/releases/latest) 下载 `pairproxy-windows-amd64.zip`
2. 解压，得到 `cproxy.exe`
3. 将 `cproxy.exe` 放到一个固定位置，例如新建文件夹 `C:\pairproxy\`
4. 将该文件夹添加到系统 PATH：
   - 按 `Win + S` 搜索"环境变量"，打开"编辑系统环境变量"
   - 点击"环境变量"→ 选中"Path"→ 点击"编辑"→ 点击"新建"
   - 输入 `C:\pairproxy` → 点击确定
   - **重新打开命令行窗口**（已打开的窗口不会立即生效）

验证安装：

```bash
cproxy version
# 应输出：cproxy v1.1.0
```

### 6.2 登录

```bash
cproxy login --server http://proxy.company.com:9000
```

> 将 `http://proxy.company.com:9000` 替换为管理员提供给您的 sproxy 地址。

按提示输入用户名和密码：

```
Username: alice
Password: ****
✓ Login successful. Token saved to ~/.config/pairproxy/token.json
```

登录成功后，令牌会保存在本地，**有效期 7 天，到期前会自动续期，正常情况下无需频繁重新登录**。

### 6.3 启动 cproxy

```bash
cproxy start
```

看到以下输出说明启动成功：

```
INFO  cproxy listening on http://127.0.0.1:8080
INFO  token valid, expires in 23h 58m
INFO  connected to sproxy: http://proxy.company.com:9000 [healthy]
```

> **注意**：cproxy 默认在前台运行，**关闭这个终端窗口后 cproxy 会停止**。Linux/macOS 可加 `--daemon` 标志让它在后台运行；Windows 请使用 `cproxy install-service`（见 6.4 节）。

### 6.4 让 cproxy 在后台持续运行

cproxy 必须保持运行状态，Claude Code 才能正常工作。各平台均提供一键后台运行方案：

---

#### Linux / macOS — `--daemon` 标志（最简单）

```bash
# 启动并立刻返回命令行（cproxy 在后台运行）
cproxy start --daemon

# 输出示例：
# ✓ cproxy started in background (PID 12345)
#   Logs: /home/alice/.config/pairproxy/cproxy.log
#   PID:  /home/alice/.config/pairproxy/cproxy.pid
#   Stop: kill $(cat /home/alice/.config/pairproxy/cproxy.pid)
```

停止后台进程：

```bash
kill $(cat ~/.config/pairproxy/cproxy.pid)
```

查看日志：

```bash
tail -f ~/.config/pairproxy/cproxy.log
```

> `--daemon` 会将 cproxy 进程从当前终端彻底分离（新建会话），关闭终端后进程继续运行。

---

#### Linux — 开机自启（systemd user service，推荐用于服务器）

如果需要在用户登录前就启动（例如服务器环境），可以使用 systemd：

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/cproxy.service <<'EOF'
[Unit]
Description=PairProxy cproxy client
After=network-online.target

[Service]
ExecStart=/usr/local/bin/cproxy start
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
EOF

# 启用并立刻启动
systemctl --user enable --now cproxy

# 查看状态
systemctl --user status cproxy

# 查看日志
journalctl --user -u cproxy -f
```

---

#### macOS — 开机自启（launchd，推荐用于个人 Mac）

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.pairproxy.cproxy.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>         <string>com.pairproxy.cproxy</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/cproxy</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key>     <true/>
  <key>KeepAlive</key>     <true/>
  <key>StandardOutPath</key>  <string>/tmp/cproxy.log</string>
  <key>StandardErrorPath</key><string>/tmp/cproxy.log</string>
</dict>
</plist>
EOF

# 加载（登录时自动启动）
launchctl load ~/Library/LaunchAgents/com.pairproxy.cproxy.plist

# 手动立刻启动
launchctl start com.pairproxy.cproxy

# 停止
launchctl stop com.pairproxy.cproxy

# 卸载自启
launchctl unload ~/Library/LaunchAgents/com.pairproxy.cproxy.plist
```

---

#### Windows — 系统服务（`cproxy install-service`，推荐）

Windows 不使用 `--daemon` 标志，而是将 cproxy 安装为 Windows 系统服务，实现开机自动启动、系统托管重启。

**第一步：以管理员权限打开 PowerShell 或命令提示符。**

**第二步：准备系统级配置目录**（服务以 LocalSystem 身份运行，无法访问你的用户目录 `%APPDATA%`）：

```powershell
# 创建系统级目录
New-Item -ItemType Directory -Path "C:\ProgramData\pairproxy" -Force

# 将当前登录令牌复制到系统级目录
Copy-Item "$env:APPDATA\pairproxy\token.json" "C:\ProgramData\pairproxy\token.json"
```

**第三步：创建服务专用配置文件** `C:\ProgramData\pairproxy\cproxy.yaml`：

```yaml
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://proxy.company.com:9000"

auth:
  token_dir: "C:\\ProgramData\\pairproxy"   # 指向系统级目录

log:
  level: info
```

**第四步：安装服务**（管理员权限）：

```powershell
cproxy install-service --config "C:\ProgramData\pairproxy\cproxy.yaml"
```

成功输出：
```
✓ Service "CProxy" installed successfully.
  Binary: C:\pairproxy\cproxy.exe
  Config: C:\ProgramData\pairproxy\cproxy.yaml

  Start:  sc start CProxy
  Stop:   sc stop  CProxy
  Status: sc query CProxy
```

**服务管理**：

```powershell
# 立刻启动
sc start CProxy

# 查看状态
sc query CProxy

# 停止
sc stop CProxy

# 查看日志（Windows 事件查看器）
# 开始菜单 → "事件查看器" → Windows 日志 → 应用程序
# 来源筛选: CProxy
```

**卸载服务**（管理员权限）：

```powershell
cproxy uninstall-service
```

> **令牌过期提醒**：Windows 服务使用的令牌文件存放在 `C:\ProgramData\pairproxy\token.json`。令牌过期后，你需要重新登录并将新令牌复制到该目录，或配置自动刷新（令牌有效期 24 小时，刷新令牌 7 天）。

---

#### 注意事项与边界情况

| 情况 | 说明 |
|------|------|
| **Linux/macOS — 日志目录不可写** | 默认日志写入 `~/.config/pairproxy/cproxy.log`；若该目录不可写，自动回退到系统临时目录（`/tmp/cproxy.log`），终端会打印实际路径 |
| **Linux/macOS — PID 文件写入失败** | PID 文件是方便工具，非必须。若写入失败，会显示警告并改用 `kill <PID>` 方式提示停止命令；cproxy 仍正常运行 |
| **Linux/macOS — 符号链接二进制** | `--daemon` 自动解析符号链接，确保子进程执行的是真实二进制文件 |
| **Linux/macOS — 环境变量继承** | 子进程继承父进程的全部环境变量，并追加 `_CPROXY_DAEMON=1`（防递归标志）。若父进程环境中有敏感变量，子进程同样持有 |
| **Windows — 服务停止宽限期** | 收到 Stop/Shutdown 命令后，cproxy 有 **10 秒** 宽限期优雅关闭现有连接。超时后强制退出 |
| **Windows — 故障自动重启策略** | 服务崩溃后按以下间隔自动重启：第 1 次 5 秒，第 2 次 30 秒，第 3 次 60 秒，24 小时后计数重置 |
| **Windows — 服务日志位置** | 运行日志写入 **Windows 事件查看器**（事件查看器 → Windows 日志 → 应用程序，来源筛选：`CProxy`） |
| **Windows — 重新安装服务** | 若需更换配置或升级二进制，先 `cproxy uninstall-service` 再重新 `cproxy install-service` |

### 6.5 查看运行状态

```bash
cproxy status
```

正常输出示例：

```
Status:  running
User:    alice (group: engineering)
Token:   valid, expires in 20h 15m (auto-refresh enabled)
Proxy:   http://proxy.company.com:9000 [healthy]
Listen:  http://127.0.0.1:8080
```

### 6.6 登出

```bash
cproxy logout
```

此命令会使服务器上的登录状态失效，并删除本地保存的令牌文件。下次使用前需重新 `cproxy login`。

---

## 7. 配置 Claude Code（开发者）

cproxy 启动后，需要告诉 Claude Code 把请求发给本地代理（`http://127.0.0.1:8080`）而不是直接发给 Anthropic。

### 7.1 设置环境变量

**macOS / Linux（bash）**：

```bash
# 临时设置（仅当前终端会话有效）
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_API_KEY=placeholder

# 永久设置（添加到 shell 配置文件，每次打开终端自动生效）
echo 'export ANTHROPIC_BASE_URL=http://127.0.0.1:8080' >> ~/.bashrc
echo 'export ANTHROPIC_API_KEY=placeholder' >> ~/.bashrc
source ~/.bashrc
```

**macOS（zsh，macOS Catalina 及之后版本的默认 shell）**：

```bash
echo 'export ANTHROPIC_BASE_URL=http://127.0.0.1:8080' >> ~/.zshrc
echo 'export ANTHROPIC_API_KEY=placeholder' >> ~/.zshrc
source ~/.zshrc
```

**Windows（PowerShell，永久设置）**：

```powershell
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_BASE_URL", "http://127.0.0.1:8080", "User")
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY", "placeholder", "User")
```

设置后**重新打开**命令行窗口使其生效。

**Windows（cmd，仅当前会话）**：

```cmd
set ANTHROPIC_BASE_URL=http://127.0.0.1:8080
set ANTHROPIC_API_KEY=placeholder
```

> **为什么 ANTHROPIC_API_KEY 可以填任意值？**
>
> Claude Code SDK 要求这个环境变量存在且非空，但 cproxy 会忽略这个值，转而使用您登录时获取的 JWT 令牌来证明身份。Anthropic 永远不会收到这个占位符值。

---

## 8. 验证整条链路是否正常

完成以上配置后，通过以下步骤确认一切正常工作。

### 8.1 逐层检查

**第一步：确认 cproxy 在运行**

```bash
cproxy status
```

看到 `Status: running` 说明 cproxy 正常。

**第二步：确认 cproxy 可以接受请求**

```bash
curl http://127.0.0.1:8080/health
```

返回 `{"status":"ok","service":"cproxy"}` 则正常。

**第三步：确认 sproxy 连通**

```bash
# 将地址替换为实际的 sproxy 地址
curl http://proxy.company.com:9000/health | jq .
```

返回 `"status":"ok"` 且 `"db_reachable":true` 则正常。

如果这一步失败，可能是：
- 网络不通（确认防火墙/安全组已开放 9000 端口）
- sproxy 未启动（管理员检查服务状态）

**第四步：用 Claude Code 发一条消息**

在设置了环境变量的终端中启动 Claude Code，输入任意内容测试。

### 8.2 在管理后台确认请求被记录

管理员打开 Dashboard（`http://服务器IP:9000/dashboard/`），在**日志页面**应该能看到刚才那条请求的记录，包含用户名、模型、token 数等信息。

如果看到了记录，说明整条链路（Claude Code → cproxy → sproxy → Anthropic）完全正常。

---

## 9. Web Dashboard 使用指南

Dashboard 是内置的 Web 管理后台，管理员在浏览器访问 `http://服务器IP:9000/dashboard/` 后用 admin 密码登录。

### 9.1 概览页面

登录后默认显示今日汇总数据：

| 指标 | 含义 |
|------|------|
| Input Tokens | 所有用户今日提交给 Claude 的 token 总量（您的问题/代码） |
| Output Tokens | Claude 今日回复的 token 总量 |
| 请求次数 | 今日成功完成的请求数 |
| 活跃用户 | 今日至少发出一次请求的用户数 |
| 估算费用 | 按配置的定价计算的 USD 消耗（需在 sproxy.yaml 中配置 pricing） |
| 最近请求列表 | 最新的请求记录，点击可查看详情 |

### 9.2 用户页面

- 查看所有用户的用量排行（按 token 总量排序）
- 创建新用户：填写用户名、密码、分组
- 禁用/启用用户账号
- 重置用户密码

### 9.3 分组页面

- 查看所有分组及其配额设置
- 创建新分组并设置配额
- 配额字段留空表示该维度无限制

### 9.4 日志页面

显示最近 100 条请求记录，每条包含：

| 字段 | 含义 |
|------|------|
| Time | 请求发生的时间 |
| User | 发起请求的用户 |
| Model | 使用的模型（如 claude-3-5-sonnet） |
| Input / Output | 本次请求消耗的输入/输出 token 数 |
| Status | 请求结果（200=成功，429=超额，401=未授权） |
| Duration | 从发出请求到收到完整回复的时间（毫秒） |
| Cost | 本次请求估算费用（USD） |

支持按用户筛选，方便查看某个人的详细记录。

### 9.5 审计页面（P2-3）

记录所有管理员操作，包括：

| 操作类型 | 触发场景 |
|---------|---------|
| `user.create` | 创建新用户 |
| `user.set_active` | 启用/禁用用户 |
| `user.reset_password` | 重置用户密码 |
| `group.create` | 创建分组 |
| `group.set_quota` | 更新分组配额 |

每条审计记录包含操作者（固定为 `admin`）、操作类型、操作对象、变更详情和时间戳，便于安全审计和追责。

### 9.6 概览趋势图表（F-10）

概览页面提供可视化趋势图表，帮助管理员快速了解用量走势：

| 图表 | 说明 |
|------|------|
| **Token 用量趋势** | 按日期显示输入/输出 token 的柱状图 |
| **API 费用趋势** | 按日期显示 USD 消耗的折线图 |
| **Top 5 用户** | 按 token 总量排名的横向条形图 |

时间范围可在页面右上角切换：**7天 / 30天 / 90天**，数据实时从 `/api/dashboard/trends` 加载。

> **前提**：图表需要 CDN 网络访问（加载 Chart.js）。内网环境可在 `layout.html` 中替换为本地资源。

### 9.7 用户自助用量页面（F-10）

普通用户（非管理员）可通过以下方式查看自己的配额和用量：

1. 登录 Dashboard 后，点击顶部导航的 **"我的用量"**
2. 或直接访问：`http://服务器IP:9000/dashboard/my-usage`

**页面内容：**

| 区块 | 说明 |
|------|------|
| 今日用量进度条 | 已用 / 配额上限；无配额时显示"无限制" |
| 本月用量进度条 | 已用 / 月配额上限 |
| 每分钟请求限制 | 显示 RPM 限制（来自所属分组配置） |
| 最近 30 天趋势图 | 按日期显示 token 柱状图 |

**对应 REST API（Bearer token 认证）：**

```
GET /api/user/quota-status    # 配额状态（今日/本月用量 + 限制）
GET /api/user/usage-history?days=30  # 每日 token 历史
```

示例响应（quota-status）：

```json
{
  "daily_limit":   50000,
  "daily_used":    12345,
  "monthly_limit": 1000000,
  "monthly_used":  234567,
  "rpm_limit":     10
}
```

> 字段为 0 表示该维度无限制（用户不属于任何分组，或分组未设置该项配额）。

---

## 10. 监控与告警

### 10.1 Prometheus 指标接入

如果您的团队使用 Prometheus + Grafana 监控体系，sproxy 提供标准格式的指标端点：

```
GET http://服务器IP:9000/metrics
```

可用指标：

| 指标名 | 说明 |
|--------|------|
| `pairproxy_tokens_today{type="input"}` | 今日输入 token 总量 |
| `pairproxy_tokens_today{type="output"}` | 今日输出 token 总量 |
| `pairproxy_requests_today{type="total"}` | 今日请求总数 |
| `pairproxy_requests_today{type="error"}` | 今日错误数 |
| `pairproxy_active_users_today` | 今日活跃用户数 |
| `pairproxy_cost_usd_today` | 今日估算费用（USD） |
| `pairproxy_tokens_month{type="input/output"}` | 本月累计 token |
| `pairproxy_requests_month{type="total/error"}` | 本月累计请求 |
| `pairproxy_database_size_bytes` | SQLite 数据库文件大小 |
| `pairproxy_quota_cache_hits_total` | 配额缓存命中次数 |
| `pairproxy_quota_cache_misses_total` | 配额缓存未命中次数 |
| `pairproxy_proxy_latency_ms` | 代理请求延迟直方图（毫秒） |
| `pairproxy_llm_latency_ms` | LLM 上游延迟直方图（毫秒） |

延迟直方图桶边界：100ms, 500ms, 1s, 5s, 30s, +Inf

Prometheus 配置示例：

```yaml
scrape_configs:
  - job_name: pairproxy
    static_configs:
      - targets: ["proxy.company.com:9000"]
    metrics_path: /metrics
    scrape_interval: 60s
```

### 10.2 Webhook 告警

在 `sproxy.yaml` 中配置 `cluster.alert_webhook`，以下情况会自动发送告警通知：

| 事件 | 触发时机 |
|------|----------|
| `quota_exceeded` | 某用户超过每日或每月 token 配额 |
| `rate_limited` | 某用户触发每分钟请求次数限制 |
| `node_down` | 某个 sproxy worker 节点停止响应 |
| `node_recovered` | 停止响应的节点恢复正常 |

**Slack 配置**：

在 Slack 中创建一个 Incoming Webhook，将 URL 填入配置：

```yaml
cluster:
  alert_webhook: "https://hooks.slack.com/services/T.../B.../xxx"
```

**飞书配置**：

在飞书群中添加机器人，获取 Webhook 地址：

```yaml
cluster:
  alert_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"
```

**企业微信配置**：

```yaml
cluster:
  alert_webhook: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx"
```

> 告警发送失败不影响正常代理功能，系统会静默忽略发送错误。

### 10.3 动态配置热重载（SIGHUP，不重启服务）

在 Linux/macOS 上，可以在**不停止服务**的情况下即时调整以下配置：

| 配置项 | 说明 |
|--------|------|
| `log.level` | 日志级别动态切换（`debug` / `info` / `warn` / `error`） |
| `log.debug_file` | 双向转发内容 debug 日志：设置路径即启用，清空即关闭 |

其他配置更改（如 LLM targets、JWT secret、端口）需要完整重启才生效。

#### 热重载操作步骤

1. 编辑 `sproxy.yaml` 中需要变更的字段
2. 向 sproxy 进程发送 `SIGHUP` 信号：

```bash
# 使用 systemctl（推荐）
sudo systemctl reload sproxy

# 或直接发送信号
kill -HUP $(pidof sproxy)

# 或通过 PID 文件（自管理部署）
kill -HUP $(cat /var/run/sproxy.pid)
```

#### 示例：开启 debug 转发日志

```yaml
# sproxy.yaml
log:
  level: "info"
  debug_file: "/var/log/pairproxy/debug.log"   # 新增此行
```

```bash
kill -HUP $(pidof sproxy)
# 日志输出：INFO  debug file logging enabled via SIGHUP  path=/var/log/pairproxy/debug.log
```

关闭时，将 `debug_file` 行删除（或置为空字符串），再次发送 `SIGHUP` 即可。

#### debug_file 格式说明

启用后，每条请求产生四类 DEBUG 日志条目（均为 JSON 格式）：

| 方向 | 日志消息 | 包含字段 |
|------|----------|----------|
| ← client request | 客户端（Claude Code）发来的原始请求 | method、path、headers、body（≤64KB） |
| → LLM request | 转发给 LLM 上游的请求（已替换 Authorization） | method、target、headers |
| ← LLM response | LLM 返回的响应头信息 | status、streaming、headers |
| ← LLM stream chunk | streaming 模式下的每个 SSE chunk | data（≤64KB） |

敏感 header（`Authorization`、`X-PairProxy-Auth`、`Cookie`）自动过滤，不写入日志文件。

**注意**：
- `SIGHUP` 热重载仅在 Linux/macOS 上支持；Windows 需完整重启服务：`Restart-Service sproxy`
- sproxy 收到 `SIGHUP` 时始终打印：`INFO  SIGHUP received — reloading config`

**可接受的日志级别**：`debug` | `info` | `warn` | `error`

### 10.4 OpenTelemetry 分布式追踪

PairProxy 支持通过 OpenTelemetry 将请求追踪数据发送到 Jaeger、Grafana Tempo 等追踪后端。默认禁用（零性能开销）。

**快速入门（Jaeger）**：

```bash
# 用 Docker 启动 Jaeger（all-in-one 版，适合测试）
docker run -d --name jaeger \
  -p 4317:4317 \    # gRPC OTLP 接收端口
  -p 16686:16686 \  # Web UI
  jaegertracing/all-in-one:latest
```

在 `sproxy.yaml` 中启用：

```yaml
telemetry:
  enabled: true
  otlp_protocol: "grpc"           # grpc | http | stdout
  otlp_endpoint: "jaeger:4317"    # gRPC 默认端口 4317
  service_name: "pairproxy"
  sampling_rate: 1.0              # 全量采样（生产环境可降低至 0.1）
```

**已插桩的 Span**

| Span 名 | 覆盖范围 | 主要 Attributes |
|---------|----------|----------------|
| `pairproxy.proxy` | 完整代理请求（认证→配额→转发→响应）| `user_id`, `provider`, `upstream_url`, `path` |
| `pairproxy.quota.check` | 配额检查 | `user_id`, `result`, `kind` |
| `pairproxy.db.write` | 用量日志批量写入 | `batch_size` |

重启 sproxy 后，在 Jaeger Web UI（`http://jaeger-host:16686`）中搜索 service: "pairproxy" 即可查看追踪。

---



### 11.1 升级 sproxy（服务端）

```bash
# 1. 下载新版本二进制
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-linux-amd64.tar.gz
tar xzf pairproxy-linux-amd64.tar.gz

# 2. 停止当前运行的 sproxy
sudo systemctl stop sproxy

# 3. 替换二进制文件
sudo mv sproxy /usr/local/bin/sproxy
sudo chmod +x /usr/local/bin/sproxy

# 4. 确认版本
sproxy version

# 5. 重新启动
sudo systemctl start sproxy
sudo systemctl status sproxy
```

**数据库是否需要迁移**：sproxy 每次启动时会自动执行数据库结构升级（migration），无需手动操作，数据不会丢失。

**Docker 升级**：

```bash
docker compose pull
docker compose up -d
```

### 11.2 升级 cproxy（客户端，每位开发者操作）

```bash
# 下载新版本
curl -LO https://github.com/l17728/pairproxy/releases/latest/download/pairproxy-darwin-arm64.tar.gz  # macOS ARM
tar xzf pairproxy-darwin-arm64.tar.gz

# 替换二进制
sudo mv cproxy /usr/local/bin/cproxy

# 重启 cproxy（用哪种方式启动的就用哪种方式重启）

# 如果用 --daemon（Linux/macOS）：
kill $(cat ~/.config/pairproxy/cproxy.pid)
cproxy start --daemon

# 如果用 launchd（macOS）：
launchctl unload ~/Library/LaunchAgents/com.pairproxy.cproxy.plist
launchctl load ~/Library/LaunchAgents/com.pairproxy.cproxy.plist

# 如果用 systemd（Linux）：
systemctl --user restart cproxy

# 如果用 Windows 服务：
sc stop CProxy && sc start CProxy
```

升级后无需重新登录，本地已保存的令牌继续有效。

---

## 12. 常见问题与排查

> 本章汇总开发者最常见的问题。如果遇到启动失败、JWT 错误、连接问题、集群节点异常等更复杂的故障，请参阅完整版故障排查手册：[docs/TROUBLESHOOTING.md](./TROUBLESHOOTING.md)。

### 12.1 cproxy 提示"请重新登录"

**现象**：运行 Claude Code 时报错，或 `cproxy status` 显示 token 已失效。

**原因**：
- 超过 7 天未使用（refresh token 过期）
- 管理员主动撤销了您的登录状态

**解决**：

```bash
cproxy login --server http://proxy.company.com:9000
```

---

### 12.2 Claude Code 显示"连接失败"或"无法访问"

**排查步骤**（按顺序执行）：

```bash
# 第 1 步：确认 cproxy 在运行
cproxy status
# 如果没有输出或显示未运行，执行：cproxy start

# 第 2 步：确认 cproxy 的监听端口正常
curl http://127.0.0.1:8080/health
# 如果没有响应，说明 cproxy 没有正常运行

# 第 3 步：确认环境变量设置正确
echo $ANTHROPIC_BASE_URL
# 应该输出：http://127.0.0.1:8080
# 如果为空，说明环境变量没有生效，参考第 7 章重新设置

# 第 4 步：在设置了环境变量的终端里重启 Claude Code
```

---

### 12.3 请求被拒绝，提示"超出配额"

**现象**：Claude Code 显示错误，错误信息包含"quota_exceeded"或"rate_limit"。

**含义**：
- `quota_exceeded / daily`：今日 token 用量已达上限，明天自动重置
- `quota_exceeded / monthly`：本月 token 用量已达上限，下月初重置
- `rate_limit`：发请求太频繁，等 1 分钟后再试

**解决**：联系管理员，请求提升配额：

```bash
# 管理员执行（将 alice 的每日上限从 100 万提升到 200 万）
sproxy admin group set-quota engineering --daily 2000000
```

---

### 12.4 请求被拒绝，提示"身份验证失败"（401）

**可能原因**：
1. cproxy 持有的令牌已过期，且自动续期失败
2. 账号被管理员禁用
3. 管理员更换了服务器的 JWT 密钥

**解决**：

```bash
cproxy login --server http://proxy.company.com:9000  # 重新登录
```

若重新登录后仍报错，联系管理员确认您的账号状态。

---

### 12.5 cproxy 启动后，重开终端就不能用了

**原因**：cproxy 以前台进程方式运行，关闭终端后进程随之退出。

**解决**：参考第 6.4 节，将 cproxy 配置为后台运行（Linux/macOS 使用 `cproxy start --daemon` 或 systemd/launchd；Windows 使用 `cproxy install-service`）。

---

### 12.6 Dashboard 上费用显示为 0

**原因**：`sproxy.yaml` 中没有配置 `pricing` 字段，或配置的模型名称与实际使用的不完全一致（区分大小写）。

**排查**：

在 Dashboard 日志页面，查看某条请求记录的"Model"字段，复制其中的模型名称，与 `sproxy.yaml` 中 `pricing.models` 下的 key 对比，确保完全一致。

例如，日志显示 `claude-3-5-sonnet-20241022`，则配置中的 key 必须也是 `claude-3-5-sonnet-20241022`，不能是 `claude-3-5-sonnet`。

---

### 12.7 sproxy 启动后无法访问（连接超时）

**最常见原因**：防火墙或云服务器安全组没有开放 9000 端口。

**排查**：

```bash
# 在服务器上验证服务确实在监听
ss -tlnp | grep 9000
# 应该有类似 LISTEN 0 ... 0.0.0.0:9000 的输出

# 如果没有，说明 sproxy 没有正确启动
sudo systemctl status sproxy
sudo journalctl -u sproxy -n 50
```

若服务在运行，则检查防火墙（参考第 2.2 节）和云控制台安全组规则。

---

### 12.8 开启调试日志排查复杂问题

```bash
# cproxy 调试模式
LOG_LEVEL=debug cproxy start

# sproxy 调试模式（无需重启，发 SIGHUP 即可）
# 1. 修改 sproxy.yaml 中 log.level: "debug"
# 2. 发送 SIGHUP 热重载
sudo systemctl reload sproxy      # systemd 部署
# 或：kill -HUP $(pidof sproxy)   # 直接运行时

# Windows（需完整重启）
LOG_LEVEL=debug sproxy start --config /etc/pairproxy/sproxy.yaml
```

调试日志会显示每条请求的详细转发过程，帮助定位问题。排查完成后记得恢复 `log.level: "info"` 并再次发送 `SIGHUP`。

---

## 13. 安全建议

### 13.1 关于密钥的最重要原则

- **JWT_SECRET** 和 **Anthropic API Key** 是最敏感的信息。它们只应存在于服务器的环境变量文件中（如 `/etc/pairproxy/sproxy.env`），**绝对不要**：
  - 写进配置文件后提交到 Git 仓库
  - 通过聊天软件、邮件或文档传递
  - 告知任何不需要的人

- 生成 JWT_SECRET 使用随机命令，不要使用有意义的词语：
  ```bash
  openssl rand -hex 32
  ```

### 13.2 网络访问控制

- sproxy 建议部署在内网，开发者通过 VPN 或内网访问，**避免直接暴露公网**
- 如果必须公网访问，强烈建议在 sproxy 前面加一层 Nginx/Caddy 并配置 HTTPS（参考 Nginx 官方文档设置反向代理和 SSL 证书）
- `/metrics` 端点不需要认证，建议通过防火墙仅允许 Prometheus 服务器访问

#### 反向代理与 IP 来源验证

sproxy 的登录限流器通过客户端 IP 防御暴力破解。当 sproxy 前面有 Nginx/Caddy 等反向代理时，真实客户端 IP 由 `X-Forwarded-For` 头传递。**为防止攻击者伪造此头部绕过限流**，必须在配置中声明可信代理的 CIDR：

```yaml
auth:
  trusted_proxies:
    - "10.0.0.0/8"       # 内网代理（如内网 Nginx）
    - "127.0.0.1/32"     # 本机代理
```

- **`trusted_proxies` 留空（默认）**：永远使用 TCP 连接的 `RemoteAddr` 作为客户端 IP，忽略 `X-Forwarded-For`。适用于 sproxy 直接暴露的场景。
- **填写 CIDR**：只有当请求来自这些 IP 段时，才信任 `X-Forwarded-For` 中的值。配置错误（填了非可信代理）会让攻击者能伪造 IP 绕过限流。

### 13.3 定期维护

- **员工离职**：立即执行 `sproxy admin user disable <用户名>` 和 `sproxy admin token revoke <用户名>`
- **定期审查**：每月查看 `sproxy admin stats --days 30`，留意异常高用量的账号
- **数据库备份**：使用内置命令备份 SQLite 文件（在线热备份，无需停止服务）：
  ```bash
  # 备份到默认路径（原文件名.bak）
  sproxy admin backup

  # 备份到指定路径
  sproxy admin backup --output /backup/pairproxy-$(date +%Y%m%d).db
  ```

- **用量日志导出**：将 usage_logs 表导出为 CSV 或 NDJSON，便于在 Excel / 数据分析工具中查看：
  ```bash
  # 导出全部记录为 CSV（标准输出）
  sproxy admin export --format csv

  # 按时间范围导出为 JSON，写入文件
  sproxy admin export --format json --from 2024-01-01 --to 2024-01-31 --output jan.ndjson

  # 通过 REST API 在浏览器中直接下载（Bearer token 或 Dashboard cookie 均可）
  curl -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9000/api/admin/export?format=csv&from=2024-01-01&to=2024-01-31" \
    -o usage.csv
  ```

  Dashboard 日志页面右上角也提供 **↓ JSON** / **↓ CSV** 快捷下载按钮（导出全量记录）。

### 13.4 admin 账号安全

- admin 密码设置 16 位以上，包含大小写字母、数字和特殊字符
- 不要将 Dashboard 地址分享给不需要管理权限的人
- 定期更换 admin 密码（更换后执行 `sproxy hash-password` 重新生成 hash 并更新配置）

---

## 14. 配置文件完整参考

### 14.1 cproxy 配置（`cproxy.yaml`）

默认路径：
- Linux：`~/.config/pairproxy/cproxy.yaml`
- macOS：`~/.config/pairproxy/cproxy.yaml`
- Windows：`%APPDATA%\pairproxy\cproxy.yaml`

通常不需要手动创建此文件，`cproxy login` 会自动保存服务器地址。仅当需要修改端口等设置时创建：

```yaml
listen:
  host: "127.0.0.1"   # 监听地址（不要改，保持本地回环）
  port: 8080          # 如果 8080 端口冲突，改为其他端口（同时更新 ANTHROPIC_BASE_URL）

sproxy:
  primary: "http://proxy.company.com:9000"  # 种子节点（主 sproxy 地址）

  # 可选：已知 worker 节点地址（主节点故障兜底）
  # 当 primary 宕机时，cproxy 会自动将请求路由到以下节点，保持不中断。
  # 单节点部署时删除此段或留空。
  targets:
    - "http://proxy2.company.com:9000"
    # - "http://proxy3.company.com:9000"

  # request_timeout: 300s   # 预留字段（当前版本不控制流式请求时长）

auth:
  refresh_threshold: 30m   # 令牌剩余有效期低于此值时自动续期

log:
  level: "info"   # 正常使用 info；遇到问题时改为 debug
```

**`sproxy.targets` 与高可用说明**

cproxy 启动时会将三个来源的地址合并到负载均衡器中：

| 来源 | 字段/文件 | 典型场景 |
|------|-----------|---------|
| 配置文件种子节点 | `sproxy.primary` | 首次连接的入口节点 |
| 配置文件静态列表 | `sproxy.targets` | 管理员预先填写已知 worker 地址 |
| 磁盘路由缓存 | `routing-cache.json`（自动维护）| c-proxy 上次运行时感知到的节点，重启后恢复 |

三个来源的地址按 URL 去重后合并。配置文件中的地址优先标记为健康；仅来自缓存的地址沿用缓存中的健康状态。

> **建议**：集群部署时，将所有已知 worker 地址填入 `targets`，这样即使 primary 宕机，c-proxy 也能无缝切换，用户感知不到中断。

### 14.2 sproxy 配置（`sproxy.yaml`）完整字段说明

```yaml
# 监听配置
listen:
  host: "0.0.0.0"   # 0.0.0.0 表示监听所有网卡（允许外部访问）
  port: 9000

# 上游 LLM 配置
llm:
  lb_strategy: "round_robin"   # 多个 target 时的负载均衡策略：round_robin / random / weighted
  # request_timeout: 300s   # 预留字段（当前版本不控制流式请求时长）
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"  # 从环境变量读取（不要写明文）
      weight: 1                          # 权重，仅 weighted 策略时有效
      # provider: "anthropic"            # 可省略，默认即为 anthropic
    # 可以配置多个 target，实现多 Key 轮询
    # - url: "https://api.anthropic.com"
    #   api_key: "${ANTHROPIC_API_KEY_2}"
    #   weight: 1
    # 接入 OpenAI API（路径 /v1/chat/completions 自动路由到 openai provider）
    # - url: "https://api.openai.com"
    #   api_key: "${OPENAI_API_KEY}"
    #   weight: 1
    #   provider: "openai"
    # 接入 Ollama 本地推理服务（与 OpenAI 格式兼容）
    # - url: "http://localhost:11434"
    #   api_key: "ollama"
    #   weight: 1
    #   provider: "ollama"

# 数据库配置
database:
  path: "/var/lib/pairproxy/pairproxy.db"  # SQLite 文件路径（必须是绝对路径）
  write_buffer_size: 200   # 每次批量写入的最大条数（调大可提高写入效率）
  flush_interval: 5s       # 即使 buffer 没满，最迟多久写一次

# 认证配置
auth:
  jwt_secret: "${JWT_SECRET}"      # JWT 签名密钥（必填）
  access_token_ttl: 24h            # 用户短期令牌有效期
  refresh_token_ttl: 168h          # 用户长期令牌有效期（7天）
  trusted_proxies:                 # 可信反向代理 CIDR 列表（留空=永不信任 X-Forwarded-For）
    - "10.0.0.0/8"                 # 示例：内网代理
    # - "172.16.0.0/12"           # 示例：Docker 网络
    # - "127.0.0.1/32"            # 示例：本机 nginx

# admin 配置
admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"  # admin 密码的 bcrypt hash

# 集群配置
cluster:
  role: "primary"              # primary（主节点）或 worker（工作节点）
  self_addr: ""                # 本节点的外部访问地址（集群模式必填，单机可留空）
  self_weight: 50              # 本节点权重（1~100，影响 cproxy 分配给本节点的流量比例）
  primary: ""                  # worker 专用：主节点地址
  shared_secret: ""            # worker 专用：与主节点通信的 HMAC 密钥（worker 模式必填）
  peer_monitor_interval: 30s  # primary 专用：检查 worker 是否下线的间隔
  report_interval: 30s        # worker 专用：向 primary 心跳的间隔
  alert_webhook: ""           # 告警推送 URL（Slack/飞书等，留空不推送）

# Dashboard 配置
dashboard:
  enabled: true   # 是否启用 Web 管理后台

# 模型定价配置（用于在 Dashboard 显示估算费用）
pricing:
  default_input_per_1k: 0.003    # 未匹配模型的默认输入定价（美元/千 token）
  default_output_per_1k: 0.015   # 未匹配模型的默认输出定价
  models:
    # key 必须与 Anthropic 返回的模型 ID 完全一致（区分大小写）
    claude-opus-4-5:
      input_per_1k: 0.015
      output_per_1k: 0.075
    claude-sonnet-4-5:
      input_per_1k: 0.003
      output_per_1k: 0.015
    claude-haiku-4-5-20251001:
      input_per_1k: 0.0008
      output_per_1k: 0.004
    claude-3-5-sonnet-20241022:
      input_per_1k: 0.003
      output_per_1k: 0.015
    claude-3-5-haiku-20241022:
      input_per_1k: 0.0008
      output_per_1k: 0.004

# 日志配置
log:
  level: "info"   # debug / info / warn / error
```

---

*遇到问题或有功能建议？请访问 [GitHub Issues](https://github.com/l17728/pairproxy/issues) 提交反馈。*

---

## 15. LLM 目标管理（网络可靠性 + 绑定均分）

### 15.1 概述

从 v2.1.0 起，s-proxy 支持：

- **多 LLM target 加权负载均衡**（加权随机）
- **被动熔断**：连续 3 次请求失败 → 自动标记目标为不健康
- **自动恢复（半开）**：经过 `recovery_delay` 后自动重置为健康
- **主动健康检查**：为有 `health_check_path` 的目标定期 GET 检查
- **带重试的代理**：首个目标失败时自动切换到下一个健康目标（最多 `max_retries` 次）
- **用户/分组级 LLM 绑定**：为特定用户或分组绑定专用 LLM target
- **一键均分**：将所有活跃用户按轮询方式均匀分配到所有 target

### 15.2 配置说明

在 `sproxy.yaml` 中：

```yaml
llm:
  lb_strategy: weighted_random   # 目前仅支持 weighted_random
  # request_timeout: 120s   # 预留字段（当前版本不控制流式请求时长）
  max_retries: 2                 # 首次尝试失败后的最大重试次数（默认 2）
  recovery_delay: 60s            # 熔断后自动恢复延迟（默认 60s，0=禁用自动恢复）

  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"    # 可选显示名（Dashboard 展示）
      weight: 70                  # 负载均衡权重（默认 1）
      health_check_path: ""       # 留空=仅被动熔断；填写路径=启用主动 GET 检查

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      name: "OpenAI GPT"
      weight: 30
      health_check_path: ""       # 公共 LLM API 通常无 /health 端点，建议留空
```

**关键参数说明**：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `max_retries` | 2 | 首次失败后最多重试几次换目标 |
| `recovery_delay` | 60s | 熔断后多久自动重置为健康（0=不自动恢复） |
| `weight` | 1 | 负载均衡权重；权重越高被选中概率越大 |
| `health_check_path` | 空 | 主动健康检查路径；空=不主动检查，依赖被动熔断 |
| `name` | 空（显示 URL） | Dashboard 中的显示名称 |

### 15.3 被动熔断与自动恢复

s-proxy 对每个 LLM target 维护连续失败计数：

1. 每次请求成功 → 重置计数，若之前不健康则重新标记为健康
2. 每次请求失败（连接错误或 5xx）→ 计数 +1，达到 3 次 → 标记为不健康
3. 不健康后若 `recovery_delay > 0`：经过延迟后自动重置为健康（半开状态）
4. 半开后若真实请求再次失败 → 重新进入不健康状态

> **注意**：4xx 响应不触发熔断（客户端错误由客户端自身处理）。

### 15.4 用户/分组 LLM 绑定

#### 通过 Dashboard

1. 打开 `/dashboard/llm`
2. 在"添加绑定"表单中选择类型（用户/分组）、对象和目标 URL
3. 点击"绑定"，或使用"一键均分所有活跃用户"将用户平均分配到所有 target

#### 通过管理员 CLI

```bash
# 查看所有 target 及当前绑定数
sproxy admin llm targets

# 列出所有绑定关系
sproxy admin llm list

# 将用户 alice 绑定到指定 LLM
sproxy admin llm bind alice --target https://api.anthropic.com

# 将分组 premium 绑定到指定 LLM
sproxy admin llm bind --group premium --target https://api.openai.com

# 删除用户 alice 的 LLM 绑定（回退到负载均衡）
sproxy admin llm unbind alice

# 将所有活跃用户均分到配置文件中的所有 target
sproxy admin llm distribute
```

#### 通过 REST API

```bash
# 查看目标健康状态
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/llm/targets

# 列出绑定关系
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/llm/bindings

# 创建用户绑定
curl -XPOST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"target_url":"https://api.anthropic.com","user_id":"USER_ID"}' \
  http://localhost:9000/api/admin/llm/bindings

# 删除绑定（需要先从 list 获取 binding ID）
curl -XDELETE -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/llm/bindings/BINDING_ID

# 一键均分
curl -XPOST -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/llm/distribute
```

### 15.5 路由优先级

当请求到达时，路由目标按以下顺序决定：

1. **用户级绑定**（最高优先级）：若该用户有专属绑定且目标健康
2. **分组级绑定**：若用户所在分组有绑定且目标健康
3. **负载均衡**：从健康目标中按权重随机选取
4. **回退**：若均衡器无配置，简单轮询

### 15.6 结构化日志

所有 LLM 路由相关事件均有结构化 zap 日志：

| 日志消息 | 级别 | 含义 |
|----------|------|------|
| `using bound LLM target` | DEBUG | 使用用户/分组绑定 |
| `bound LLM target unhealthy, falling back to load balancer` | WARN | 绑定目标不健康，回退 LB |
| `picked LLM target (weighted random)` | DEBUG | LB 选出目标 |
| `llm request failed, retrying with next target` | WARN | 首次失败，切换重试 |
| `target marked unhealthy` | WARN | 被动熔断触发 |
| `target recovered` | INFO | 主动健康检查恢复 |
| `target auto-recovered after delay` | INFO | 半开延迟恢复 |

### 15.7 滚动升级（Drain/Undrain）

多节点集群支持通过排水（Drain）机制实现零停机滚动升级。

#### 概念说明

- **Drain（排水）**：节点进入排水模式后，不再接受新请求，但继续处理现有请求
- **Undrain（恢复）**：节点恢复正常模式，重新接受流量
- **Wait（等待）**：阻塞直到活跃请求数归零

#### 通过管理员 CLI

```bash
# 进入排水模式
sproxy admin drain enter

# 查看排水状态和活跃请求数
sproxy admin drain status

# 等待活跃请求归零（最多等 60 秒）
sproxy admin drain wait --timeout 60s

# 退出排水模式（恢复正常）
sproxy admin drain exit
```

#### 通过 REST API

```bash
# 进入排水模式
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/drain

# 查看状态
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/drain/status

# 退出排水模式
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/undrain
```

#### 滚动升级流程示例

```bash
# 1. 进入排水模式
./sproxy admin drain enter

# 2. 等待请求排空
./sproxy admin drain wait --timeout 120s

# 3. 停止服务
systemctl stop sproxy

# 4. 替换二进制并启动
cp sproxy-new /usr/local/bin/sproxy
systemctl start sproxy

# 5. 验证
curl http://localhost:9000/health
```

详细的滚动升级指南请参见 `docs/UPGRADE.md`。

---

## 16. 接入 OpenAI 格式客户端

### 16.1 支持的客户端

PairProxy sproxy 同时支持 Anthropic 和 OpenAI 两种 API 格式：

- **Anthropic 格式**：Claude Code、cproxy（`POST /v1/messages`）
- **OpenAI 格式**：Cursor、Continue.dev、任何兼容 OpenAI API 的工具（`POST /v1/chat/completions`）

两种格式的客户端共享同一套配额、审计、统计系统。

---

### 16.2 配置步骤

#### 1. 在 `sproxy.yaml` 中添加 OpenAI target

```yaml
llm:
  targets:
    # 现有 Anthropic target
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      weight: 1

    # 新增 OpenAI target
    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      name: "OpenAI GPT"
      weight: 1
```

#### 2. 设置环境变量

```bash
export OPENAI_API_KEY="sk-..."
```

#### 3. 重启 sproxy

```bash
systemctl restart sproxy
```

---

### 16.3 客户端配置

OpenAI 格式客户端需要配置两个参数：

| 参数 | 值 |
|---|---|
| **Base URL** | `http://your-sproxy:9000` |
| **API Key** | PairProxy JWT（通过 `/auth/login` 获取的 `access_token`）|

**重要**：API Key 字段填写的是 **PairProxy JWT**，不是 OpenAI API Key。

---

### 16.4 认证方式

OpenAI 格式客户端使用标准 `Authorization: Bearer <token>` 头，其中 `<token>` 是 PairProxy JWT。

#### 示例（curl）

```bash
# 1. 登录获取 JWT
TOKEN=$(curl -X POST http://localhost:9000/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"your-password"}' \
  | jq -r '.access_token')

# 2. 使用 JWT 调用 OpenAI 格式 API
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

---

### 16.5 自动功能

sproxy 对 OpenAI 格式客户端提供以下自动功能：

#### 路由

根据请求路径自动选择对应 provider 的 target：

- `/v1/messages` → 路由到 `provider: anthropic` 的 target
- `/v1/chat/completions` → 路由到 `provider: openai` 的 target

#### Token 计数

流式请求自动注入 `stream_options.include_usage: true`，确保配额统计准确。客户端无需手动设置。

#### 配额与审计

OpenAI 格式客户端与 Anthropic 客户端共享：

- 用户/分组配额限制
- 审计日志记录
- 统计报表
- Dashboard 展示

---

### 16.6 常见客户端配置示例

#### Cursor

1. 打开 Settings → Models
2. 添加自定义 OpenAI 兼容 API：
   - Base URL: `http://your-sproxy:9000`
   - API Key: `<your-pairproxy-jwt>`

#### Continue.dev

编辑 `~/.continue/config.json`：

```json
{
  "models": [
    {
      "title": "PairProxy GPT-4",
      "provider": "openai",
      "model": "gpt-4",
      "apiBase": "http://your-sproxy:9000",
      "apiKey": "<your-pairproxy-jwt>"
    }
  ]
}
```

#### OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://your-sproxy:9000",
    api_key="<your-pairproxy-jwt>"  # PairProxy JWT，非 OpenAI API Key
)

response = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True
)

for chunk in response:
    print(chunk.choices[0].delta.content, end="")
```

---

### 16.7 故障排查

#### 问题：401 Unauthorized

**原因**：JWT 过期或无效。

**解决**：重新登录获取新 JWT：

```bash
curl -X POST http://localhost:9000/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"your-password"}'
```

#### 问题：502 Bad Gateway, "no_upstream"

**原因**：未配置 `provider: openai` 的 target。

**解决**：检查 `sproxy.yaml` 中是否有 `provider: openai` 的 target，并重启 sproxy。

#### 问题：流式请求 token 计数为 0

**原因**：sproxy 版本过旧，不支持自动注入 `stream_options`。

**解决**：升级到 v2.0.0 或更高版本。

---

### 16.8 与 cproxy 的区别

| 特性 | cproxy（Anthropic 格式）| OpenAI 格式客户端 |
|---|---|---|
| 认证头 | `X-PairProxy-Auth: <jwt>` | `Authorization: Bearer <jwt>` |
| 请求路径 | `/v1/messages` | `/v1/chat/completions` |
| 自动 token 刷新 | ✅ 支持 | ❌ 客户端自行处理 |
| 路由缓存 | ✅ 支持 | ❌ 无缓存 |
| 适用场景 | Claude Code 等 Anthropic 客户端 | Cursor、Continue.dev 等 OpenAI 客户端 |

---
