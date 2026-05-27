# PairProxy 用户手册

**版本 v3.0.0**

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
8. [LLM Target 动态管理](#8-llm-target-动态管理)
9. [验证整条链路是否正常](#9-验证整条链路是否正常)
10. [Web Dashboard 使用指南](#10-web-dashboard-使用指南)
   - [10.6 概览趋势图表](#106-概览趋势图表f-10)
   - [10.7 用户自助用量页面](#107-用户自助用量页面f-10)
11. [监控与告警](#11-监控与告警)
    - [11.1 Prometheus 指标接入](#111-prometheus-指标接入)
    - [11.2 Webhook 告警](#112-webhook-告警)
    - [11.3 动态调整日志级别（SIGHUP）](#113-动态调整日志级别不重启服务)
11. [常见问题与排查](#12-常见问题与排查)
12. [安全建议](#13-安全建议)
13. [配置文件完整参考](#14-配置文件完整参考)
14. [LLM 目标管理（网络可靠性 + 绑定均分）](#15-llm-目标管理网络可靠性--绑定均分)
15. [接入 OpenAI 格式客户端](#16-接入-openai-格式客户端)
    - [16.9 协议自动转换（Claude CLI + Ollama）](#169-协议自动转换claude-cli--ollama)
16. [用户对话内容跟踪](#17-用户对话内容跟踪)
17. [升级指南](#18-升级指南)
- [§28 PostgreSQL 数据库支持（v2.13.0）](#28-postgresql-数据库支持v2130)
- [§29 PostgreSQL 对等节点模式（v2.14.0）](#29-postgresql-对等节点模式v2140)
- [§30 已知问题与修复](#30-已知问题与修复)
- [§32 训练语料采集（Corpus）](#32-训练语料采集corpusv2160)
- [§34 语义路由（Semantic Router）（v2.18.0）](#34-语义路由semantic-routerv2180)
- [§35 LLM 目标运行时同步（v2.19.0）](#35-llm-目标运行时同步v2190)
- [§36 Group-Target Set（分组目标集）（v2.20.0）](#36-group-target-set分组目标集v2200)
- [§37 告警管理（Alert Manager）（v2.20.0）](#37-告警管理alert-managerv2200)
- [§38 目标健康监控（Target Health Monitor）（v2.20.0）](#38-目标健康监控target-health-monitorv2200)
- [§39 WebUI Phase 1：分组目标集管理（v2.22.0）](#39-webui-phase-1分组目标集管理v2220)
- [§40 WebUI Phase 2：告警管理增强（v2.22.0）](#40-webui-phase-2告警管理增强v2220)
- [§41 WebUI Phase 3：快速操作面板（v2.22.0）](#41-webui-phase-3快速操作面板v2220)
- [§42 v2.23.0 更新说明：APIKey 号池 + 健康检查认证 + 文档修正](#42-v2230-更新说明)
- [§43 v2.24.0 更新说明：Model-Aware Routing（模型感知路由）](#43-v2240-更新说明)
- [§44 v2.24.3 更新说明：多 API Key 共用同一 URL（Issue #6）](#44-v2243-更新说明)
- [§45 v2.24.5 更新说明：智能探活（Smart Probe）](#45-v2245-更新说明智能探活smart-probe)

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
| **协议自动转换** | Claude CLI 自动转换为 OpenAI 格式连接 Ollama，零配置启用（v2.6.0+）|
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
Restart=always
RestartSec=5s
StartLimitIntervalSec=60
StartLimitBurst=3
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

**自动重启配置说明**：

上述配置中的 `Restart=always` 确保 sproxy 进程在任何情况下退出（崩溃、被kill、自己退出）时都会自动重启，提高服务可用性。配合防抖动参数：
- `StartLimitIntervalSec=60`：60秒内
- `StartLimitBurst=3`：最多重启3次

如果60秒内连续失败3次，systemd会停止重启尝试，需手动执行 `sudo systemctl reset-failed sproxy && sudo systemctl start sproxy` 恢复。

**注意**：管理员通过 `systemctl stop` 显式停止服务时，不会触发自动重启。

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
  [--rpm <每分钟最多请求次数>] \
  [--max-tokens-per-request <单次请求最大token数>] \
  [--concurrent-requests <最大并发请求数>]
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

# 限制单次请求最大 token 数（防止单个请求消耗过多资源）
sproxy admin group set-quota trial --max-tokens-per-request 4096

# 限制并发请求数（防止单用户占用过多连接）
sproxy admin group set-quota trial --concurrent-requests 5
```

### 4.6 查看用量统计

```bash
# 查看所有用户最近 7 天的用量
sproxy admin stats --days 7

# 查看某个用户最近 30 天的用量
sproxy admin stats --user alice --days 30
```

### 4.7 批量导入分组和用户（v2.8.0）

如果需要一次性导入大量用户（如初始化部署、批量入职），可以使用批量导入功能：

```bash
# 从模板文件批量导入
sproxy admin import users.txt

# 先预览（不实际创建）
sproxy admin import --dry-run users.txt
```

**模板文件格式**（示例 `users.txt`）：

```
# 注释以 # 开头，空行忽略

[engineering llm=https://api.anthropic.com]
alice  Password123
bob    Password456 llm=https://api.openai.com   # 用户级 LLM 覆盖

[marketing]
charlie  Marketing789

[-]
dave  NoGroup_Pass
```

**格式说明**：

| 语法 | 说明 |
|------|------|
| `[分组名]` | 声明分组区块 |
| `[分组名 llm=URL]` | 声明分组区块并绑定组级 LLM |
| `用户名 密码` | 在当前分组下创建用户 |
| `用户名 密码 llm=URL` | 用户级 LLM 覆盖（不影响同组其他用户） |
| `[-]` | 无分组区块（下方用户不属于任何分组） |

**导入规则**：
- 已存在的分组/用户**跳过**，不报错，不修改
- 仅创建文件中新增的分组/用户
- `--dry-run` 会列出所有将要创建的内容，不实际写入数据库

> ⚠️ 模板文件含明文密码，导入完成后请妥善保管或删除文件。

也可以通过 Dashboard 的**批量导入**页面进行可视化操作（见 §10.9）。

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

### 5.4 反向代理与全局限流（推荐）

生产环境建议在 sproxy 前部署反向代理（nginx 或 Caddy），提供以下功能：
- **全局限流**：防止 DDoS 攻击耗尽系统资源
- **TLS 终止**：统一处理 HTTPS 证书
- **负载均衡**：多 sproxy 实例分流（可选）

#### nginx 配置示例

```nginx
# /etc/nginx/conf.d/pairproxy.conf
upstream sproxy_backend {
    server 127.0.0.1:9000;
    # 多实例时添加更多 server 行
}

# 全局限流配置
limit_req_zone $binary_remote_addr zone=global:10m rate=100r/s;
limit_req_zone $binary_remote_addr zone=login:10m rate=5r/m;

server {
    listen 443 ssl http2;
    server_name proxy.company.com;

    ssl_certificate /etc/ssl/certs/proxy.crt;
    ssl_certificate_key /etc/ssl/private/proxy.key;

    # 全局限流（100 req/s，允许突发 200）
    limit_req zone=global burst=200 nodelay;

    # 登录接口单独限流（5 req/min）
    location /api/auth/login {
        limit_req zone=login burst=10 nodelay;
        proxy_pass http://sproxy_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 其他接口
    location / {
        proxy_pass http://sproxy_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # 流式响应支持
        proxy_buffering off;
        proxy_read_timeout 300s;
    }
}
```

#### Caddy 配置示例

```caddy
# /etc/caddy/Caddyfile
proxy.company.com {
    # 全局限流（100 req/s）
    rate_limit {
        zone global {
            key {remote_host}
            events 100
            window 1s
        }
    }

    # 登录接口限流（5 req/min）
    @login path /api/auth/login
    rate_limit @login {
        zone login {
            key {remote_host}
            events 5
            window 1m
        }
    }

    reverse_proxy localhost:9000 {
        # 流式响应支持
        flush_interval -1
    }
}
```

#### 限流参数建议

| 场景 | 建议值 | 说明 |
|------|--------|------|
| 全局限流 | 100 req/s | 根据服务器性能调整 |
| 登录接口 | 5 req/min | 防止暴力破解 |
| burst 缓冲 | 2x rate | 允许短时突发 |

**验证限流生效**：
```bash
# 快速发送请求测试
for i in {1..150}; do curl -s http://proxy.company.com/health & done
# 应该看到部分请求返回 429 Too Many Requests
```

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
Restart=always
RestartSec=5s
StartLimitIntervalSec=60
StartLimitBurst=3

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

**自动重启配置说明**：
上述配置中的 `Restart=always` 确保 cproxy 进程在任何情况下退出（崩溃、被kill、自己退出）时都会自动重启，提高服务可用性。配合防抖动参数：
- `StartLimitIntervalSec=60`：60秒内
- `StartLimitBurst=3`：最多重启3次

如果60秒内连续失败3次，systemd会停止重启尝试，需手动执行 `systemctl --user reset-failed cproxy && systemctl --user start cproxy` 恢复。
**注意**：用户通过 `systemctl --user stop` 显式停止服务时，不会触发自动重启。

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

## 8. LLM Target 动态管理

### 8.1 理念说明

PairProxy 支持两种方式管理 LLM 目标端点：

| 管理方式 | 适用场景 | 操作方式 | 生效时机 |
|---------|---------|---------|---------|
| **配置文件管理** | 核心生产端点、基础设施配置 | 编辑 `sproxy.yaml` | 重启 sproxy 服务 |
| **数据库管理** | 临时测试端点、业务层动态调整 | CLI 命令或 WebUI | 立即生效，无需重启 |

**设计哲学**：

- **配置文件**：作为"基础设施即代码"（IaC）的一部分，适合纳入 Git 版本控制，由运维团队统一管理
- **数据库**：提供运行时灵活性，适合业务团队快速测试新模型或临时调整路由策略

**同步机制**：

- sproxy 启动时会将配置文件中的 `llm.targets` 同步到数据库（`llm_targets` 表）
- 配置文件中的端点标记为 `source=config`，删除时需要从配置文件移除后重启
- CLI/WebUI 添加的端点标记为 `source=database`，可直接通过命令删除

---

### 8.2 配置文件管理（运维团队）

#### 8.2.1 编辑配置文件

编辑 `sproxy.yaml` 中的 `llm.targets` 部分：

```yaml
llm:
  targets:
    # 主生产端点（Anthropic 官方）
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Production"
      weight: 10
      enabled: true

    # 备用端点（自建 Claude 代理）
    - url: "https://claude-proxy.internal.company.com"
      api_key: "${INTERNAL_PROXY_KEY}"
      provider: "anthropic"
      name: "Internal Proxy"
      weight: 5
      enabled: true

    # Ollama 本地模型（协议自动转换）
    - url: "http://ollama-server.internal:11434"
      api_key: "ollama"
      provider: "ollama"
      name: "Ollama Local"
      weight: 1
      enabled: true
```

**字段说明**：

- `url`：端点地址（必填）
- `api_key`：API 密钥，支持 `${ENV_VAR}` 环境变量引用（必填）
- `provider`：提供商类型，可选 `anthropic`、`openai`、`ollama`（必填）
- `name`：端点显示名称（可选，默认使用 URL）
- `weight`：负载均衡权重，数值越大分配流量越多（可选，默认 1）
- `enabled`：是否启用（可选，默认 true）

#### 8.2.2 版本控制

建议将 `sproxy.yaml` 纳入 Git 管理：

```bash
# 提交配置变更
git add sproxy.yaml
git commit -m "feat: add Ollama local endpoint"
git push origin main

# 部署到生产环境
ssh prod-server
cd /opt/pairproxy
git pull
sudo systemctl restart sproxy
```

#### 8.2.3 重启服务使配置生效

```bash
# Linux systemd
sudo systemctl restart sproxy

# 手动启动的进程
pkill sproxy
./sproxy start --config sproxy.yaml
```

---

### 8.3 数据库管理（业务团队）

#### 8.3.1 CLI 命令管理

**查看所有端点**：

```bash
./sproxy admin llm targets
```

输出示例：

```
ID  Name                    URL                                      Provider   Weight  Enabled  Source    Bindings
1   Anthropic Production    https://api.anthropic.com                anthropic  10      true     config    15
2   Internal Proxy          https://claude-proxy.internal.company... anthropic  5       true     config    8
3   Ollama Local            http://ollama-server.internal:11434      ollama     1       true     config    2
4   Test Endpoint           http://test-llm.dev:8080                 anthropic  1       true     database  0
```

**添加新端点**：

```bash
./sproxy admin llm target add \
  --url "http://test-llm.dev:8080" \
  --api-key "test-key-123" \
  --provider "anthropic" \
  --name "Test Endpoint" \
  --weight 1
```

**更新端点**：

```bash
# 修改权重
./sproxy admin llm target update 4 --weight 5

# 修改 API Key
./sproxy admin llm target update 4 --api-key "new-key-456"

# 修改名称
./sproxy admin llm target update 4 --name "Updated Test Endpoint"
```

**禁用/启用端点**：

```bash
# 禁用端点（不删除，停止分配流量）
./sproxy admin llm target disable 4

# 重新启用
./sproxy admin llm target enable 4
```

**删除端点**：

```bash
# 删除数据库端点（source=database）
./sproxy admin llm target delete 4

# 尝试删除配置文件端点会报错
./sproxy admin llm target delete 1
# Error: cannot delete config-sourced target, remove from sproxy.yaml and restart
```

#### 8.3.2 WebUI 管理

访问 `http://your-sproxy:9000/dashboard/llm`，可通过图形界面：

- 查看所有端点及健康状态
- 添加/编辑/删除数据库端点
- 启用/禁用端点
- 查看每个端点的用户绑定数量

---

### 8.4 常见场景

#### 场景 1：添加核心生产端点

**推荐方式**：配置文件管理

```yaml
# sproxy.yaml
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Official"
      weight: 10
      enabled: true
```

重启服务后生效。

#### 场景 2：临时添加测试端点

**推荐方式**：CLI/WebUI 管理

```bash
./sproxy admin llm target add \
  --url "http://test-model.dev:8080" \
  --api-key "test-key" \
  --provider "anthropic" \
  --name "Test Model" \
  --weight 1
```

立即生效，无需重启。测试完成后直接删除：

```bash
./sproxy admin llm target delete <id>
```

#### 场景 3：修改端点配置

**配置文件端点**：

1. 编辑 `sproxy.yaml`
2. 重启 sproxy
3. 配置文件的修改会覆盖数据库中的对应记录

**数据库端点**：

```bash
./sproxy admin llm target update <id> --weight 5 --api-key "new-key"
```

立即生效。

#### 场景 4：删除端点

**配置文件端点**：

1. 从 `sproxy.yaml` 中移除对应条目
2. 重启 sproxy
3. 数据库中的记录会被自动删除

**数据库端点**：

```bash
./sproxy admin llm target delete <id>
```

#### 场景 5：URL 冲突处理

如果配置文件和数据库中存在相同 URL 的端点：

- **启动时**：配置文件优先，数据库中的同 URL 记录会被更新为 `source=config`
- **运行时**：不允许通过 CLI/WebUI 添加与现有端点 URL 冲突的记录

---

### 8.5 故障排查

#### 问题 1：添加端点后未生效

**检查步骤**：

```bash
# 1. 确认端点已添加到数据库
./sproxy admin llm targets

# 2. 检查端点是否启用
# 输出中 Enabled 列应为 true

# 3. 检查负载均衡器是否识别
curl http://localhost:9000/metrics | grep llm_target_active
```

**常见原因**：

- 端点被禁用（`enabled=false`）
- 权重设置为 0
- 健康检查失败（端点不可达）

#### 问题 2：删除端点失败

**错误信息**：

```
Error: cannot delete config-sourced target, remove from sproxy.yaml and restart
```

**解决方法**：

该端点来自配置文件，需要：

1. 编辑 `sproxy.yaml`，移除对应条目
2. 重启 sproxy 服务

#### 问题 3：配置文件修改后未生效

**检查步骤**：

```bash
# 1. 确认配置文件语法正确
./sproxy admin validate --config sproxy.yaml

# 2. 确认已重启服务
sudo systemctl status sproxy

# 3. 检查日志
tail -f /var/log/sproxy/sproxy.log | grep "target sync"
```

#### 问题 4：端点健康检查失败

**检查步骤**：

```bash
# 1. 手动测试端点连通性
curl -v http://your-llm-endpoint/health

# 2. 检查 sproxy 日志
tail -f /var/log/sproxy/sproxy.log | grep "health check"

# 3. 查看端点状态
./sproxy admin llm targets
# 查看 Health 列状态
```

**常见原因**：

- 网络不通（防火墙/安全组）
- 端点服务未启动
- API Key 无效
- 健康检查路径配置错误

#### 问题 5：数据库与配置文件不一致

**症状**：

- 配置文件中删除了端点，但数据库中仍存在
- 配置文件中修改了端点，但数据库未更新

**解决方法**：

重启 sproxy 会自动同步：

```bash
sudo systemctl restart sproxy
```

同步逻辑：

- 配置文件中的端点会 upsert 到数据库（按 URL 匹配）
- 数据库中 `source=config` 但配置文件中不存在的端点会被删除
- `source=database` 的端点不受影响

---

## 9. 验证整条链路是否正常

完成以上配置后，通过以下步骤确认一切正常工作。

### 9.1 逐层检查

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

### 9.2 在管理后台确认请求被记录

管理员打开 Dashboard（`http://服务器IP:9000/dashboard/`），在**日志页面**应该能看到刚才那条请求的记录，包含用户名、模型、token 数等信息。

如果看到了记录，说明整条链路（Claude Code → cproxy → sproxy → Anthropic）完全正常。

---

## 10. Web Dashboard 使用指南

Dashboard 是内置的 Web 管理后台，管理员在浏览器访问 `http://服务器IP:9000/dashboard/` 后用 admin 密码登录。

### 10.1 概览页面

登录后默认显示今日汇总数据：

| 指标 | 含义 |
|------|------|
| Input Tokens | 所有用户今日提交给 Claude 的 token 总量（您的问题/代码） |
| Output Tokens | Claude 今日回复的 token 总量 |
| 请求次数 | 今日成功完成的请求数 |
| 活跃用户 | 今日至少发出一次请求的用户数 |
| 估算费用 | 按配置的定价计算的 USD 消耗（需在 sproxy.yaml 中配置 pricing） |
| 最近请求列表 | 最新的请求记录，点击可查看详情 |

### 10.2 用户页面

- 查看所有用户的用量排行（按 token 总量排序）
- 创建新用户：填写用户名、密码、分组
- 禁用/启用用户账号
- 重置用户密码

### 10.3 分组页面

- 查看所有分组及其配额设置
- 创建新分组并设置配额
- 配额字段留空表示该维度无限制

### 10.4 日志页面

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

### 10.5 审计页面（P2-3）

记录所有管理员操作，包括：

| 操作类型 | 触发场景 |
|---------|---------|
| `user.create` | 创建新用户 |
| `user.set_active` | 启用/禁用用户 |
| `user.reset_password` | 重置用户密码 |
| `group.create` | 创建分组 |
| `group.set_quota` | 更新分组配额 |

每条审计记录包含操作者（固定为 `admin`）、操作类型、操作对象、变更详情和时间戳，便于安全审计和追责。

### 10.6 概览趋势图表（F-10）

概览页面提供可视化趋势图表，帮助管理员快速了解用量走势：

| 图表 | 说明 |
|------|------|
| **Token 用量趋势** | 按日期显示输入/输出 token 的柱状图 |
| **API 费用趋势** | 按日期显示 USD 消耗的折线图 |
| **Top 5 用户** | 按 token 总量排名的横向条形图 |

时间范围可在页面右上角切换：**7天 / 30天 / 90天**，数据实时从 `/api/dashboard/trends` 加载。

> **前提**：图表需要 CDN 网络访问（加载 Chart.js）。内网环境可在 `layout.html` 中替换为本地资源。

### 10.7 用户自助用量页面（F-10）

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

### 10.8 告警页面（v2.8.0）

实时展示服务运行期间产生的 WARN / ERROR 日志，帮助管理员快速感知异常。

**访问方式**：Dashboard 顶部导航点击 **"告警"**，或直接访问 `http://服务器IP:9000/dashboard/alerts`。

**功能说明**：
- 通过 **Server-Sent Events (SSE)** 实时推送新日志，无需刷新页面
- 仅展示 WARN 及以上级别的日志，不包含 INFO/DEBUG
- 日志格式：`时间 [级别] 消息体`，便于快速扫描

**对应 API**（需管理员 Cookie 认证）：
```
GET /api/admin/alerts/stream    # SSE 流，长连接推送实时告警
```

### 10.9 批量导入页面（v2.8.0）

通过 WebUI 一次性从模板文件批量创建分组和用户，适合初始化部署或批量入职。

**访问方式**：Dashboard 顶部导航点击 **"批量导入"**，或直接访问 `http://服务器IP:9000/dashboard/import`。

**页面操作步骤**：
1. 粘贴或编辑模板内容（格式见下文）
2. 点击 **"预览"** 查看将要创建的内容（等同于 `--dry-run`）
3. 确认无误后点击 **"导入"** 执行

**模板文件格式**：

```
# 注释以 # 开头，空行忽略
#
# [分组名]              — 声明分组区块
# [分组名 llm=URL]      — 声明分组区块并绑定组级 LLM
# 用户名 密码           — 在当前分组下创建用户
# 用户名 密码 llm=URL   — 用户级 LLM 覆盖组默认值
# [-]                   — 无分组区块

[engineering llm=https://api.anthropic.com]
alice  Password123
bob    Password456 llm=https://api.openai.com

[marketing]
charlie  Marketing789

[-]
dave  NoGroup_Pass
```

**导入规则**：
- 已存在的分组/用户 **跳过**（保留原有数据，仅创建新增）
- 组已存在时不会重置其 LLM 绑定
- 用户级 `llm=URL` 覆盖该用户绑定，不影响同组其他用户

> ⚠️ 模板文件含明文密码，请在导入完成后妥善保管或删除。

---

## 11. 监控与告警

### 11.1 Prometheus 指标接入

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

### 11.2 Webhook 告警

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

### 11.3 动态配置热重载（SIGHUP，不重启服务）

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

### 10.5 数据库自动备份（推荐）

生产环境建议配置自动备份任务，防止数据库文件损坏时数据丢失。

#### 使用 cron 自动备份

**Linux/macOS 配置**：

```bash
# 编辑 crontab
crontab -e

# 添加以下行（每天凌晨 2 点备份，保留最近 7 天）
0 2 * * * /usr/local/bin/sproxy admin backup --output /backup/pairproxy_$(date +\%Y\%m\%d).db && find /backup -name "pairproxy_*.db" -mtime +7 -delete
```

**Windows 配置（任务计划程序）**：

```powershell
# 创建备份脚本 backup.ps1
$date = Get-Date -Format "yyyyMMdd"
$backupPath = "C:\backup\pairproxy_$date.db"
& "C:\Program Files\pairproxy\sproxy.exe" admin backup --output $backupPath

# 删除 7 天前的备份
Get-ChildItem "C:\backup\pairproxy_*.db" | Where-Object {$_.LastWriteTime -lt (Get-Date).AddDays(-7)} | Remove-Item

# 创建计划任务（管理员权限运行）
$action = New-ScheduledTaskAction -Execute "PowerShell.exe" -Argument "-File C:\backup\backup.ps1"
$trigger = New-ScheduledTaskTrigger -Daily -At 2am
Register-ScheduledTask -TaskName "PairProxy Backup" -Action $action -Trigger $trigger -User "SYSTEM"
```

#### 备份验证

定期验证备份文件完整性：

```bash
# 检查备份文件大小（应与原数据库接近）
ls -lh /backup/pairproxy_*.db

# 尝试打开备份文件（验证未损坏）
sqlite3 /backup/pairproxy_20260308.db "SELECT COUNT(*) FROM users;"
```

#### 恢复备份

```bash
# 1. 停止 sproxy
systemctl stop sproxy

# 2. 恢复备份文件
sproxy admin restore /backup/pairproxy_20260308.db

# 3. 启动 sproxy
systemctl start sproxy
```

#### 备份策略建议

| 场景 | 备份频率 | 保留时长 |
|------|---------|---------|
| 小团队（< 50 人） | 每天 1 次 | 7 天 |
| 中型团队（50-200 人） | 每天 2 次 | 14 天 |
| 大型团队（> 200 人） | 每 6 小时 | 30 天 |

**监控备份状态**：
```bash
# 检查最近备份时间
ls -lt /backup/pairproxy_*.db | head -1

# 如果超过 25 小时未备份，发送告警
```

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

  # ---- 健康检查增强（改进项3）----
  health_check_timeout: 3s              # 单次主动检查超时（默认 3s）
  health_check_failure_threshold: 3     # 连续失败多少次后熔断（默认 3）
  health_check_recovery_delay: 60s      # 熔断后自动恢复延迟（默认 60s；0=禁用）
  passive_failure_threshold: 3          # 被动熔断阈值（默认 3）

  # ---- 路由表主动发现（改进项4）----
  shared_secret: "${CLUSTER_SECRET}"    # 与 sproxy 集群通信的共享密钥
  routing_poll_interval: 60s            # 主动轮询路由表间隔（默认 60s；0=禁用）

  # ---- 请求级重试（改进项5）----
  retry:
    enabled: true                       # 是否启用非流式请求重试（默认 true）
    max_retries: 2                      # 最大重试次数（不含首次，默认 2）
    retry_on_status: [502, 503, 504]    # 触发重试的状态码（默认 [502,503,504]）


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
  # ---- 用量缓冲（改进项2）----
  usage_buffer:
    enabled: true             # 启用 worker 本地用量缓冲（默认 true）
    max_records_per_batch: 1000  # 每批最多上报条数（默认 1000）

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

# 语义路由配置（v2.18.0）
semantic_router:
  enabled: false                   # 默认 false，需显式启用
  classifier_timeout: 3s           # 分类器调用超时（默认 3s）
  classifier_model: "claude-haiku-3-5"  # 分类器使用的模型名（建议用低延迟模型）
  routes:                          # YAML 默认规则（DB 同名规则会覆盖）
    - name: code_tasks
      description: "Code generation, debugging, refactoring, or technical programming"
      target_urls:
        - "https://api.anthropic.com"
      priority: 10                 # 数值越大越优先
    # - name: general_chat
    #   description: "General conversation, simple Q&A"
    #   target_urls:
    #     - "https://haiku-endpoint.example.com"
    #   priority: 5
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
- **智能探活**（v2.24.5+）：自动发现并缓存每个 target 的最优健康检查策略，无需手动配置路径
- **主动健康检查**：为有 `health_check_path` 的目标定期 GET 检查（显式配置优先于智能探活）
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
  retry_on_status: [429]         # 触发 try-next 的额外状态码（默认空=仅重试5xx/连接错误）
  recovery_delay: 60s            # 熔断后自动恢复延迟（默认 60s，0=禁用自动恢复）

  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"    # 可选显示名（Dashboard 展示）
      weight: 70                  # 负载均衡权重（默认 1）
      # health_check_path 留空（或省略）→ 智能探活自动发现最优探测路径（推荐）
      # health_check_path: "/v1/models"   # 若已知路径，可显式指定跳过自动探测

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      name: "OpenAI GPT"
      weight: 30
      # health_check_path 留空 → 智能探活自动发现 GET /v1/models
```

**关键参数说明**：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `max_retries` | 2 | 首次失败后最多重试几次换目标 |
| `retry_on_status` | `[]` | 除5xx/连接错误外触发 try-next 的状态码，如 `[429]` 可实现配额耗尽自动切换 |
| `recovery_delay` | 60s | 熔断后多久自动重置为健康（0=不自动恢复） |
| `weight` | 1 | 负载均衡权重；权重越高被选中概率越大 |
| `health_check_path` | 空 | **显式**健康检查路径；留空 = 智能探活（推荐）；填写路径 = 跳过自动探测直接使用 |
| `name` | 空（显示 URL） | Dashboard 中的显示名称 |

### 15.3 被动熔断与自动恢复

s-proxy 对每个 LLM target 维护连续失败计数：

1. 每次请求成功 → 重置计数，若之前不健康则重新标记为健康
2. 每次请求失败（连接错误或 5xx）→ 计数 +1，达到 3 次 → 标记为不健康
3. 不健康后若 `recovery_delay > 0`：经过延迟后自动重置为健康（半开状态）
4. 半开后若真实请求再次失败 → 重新进入不健康状态

> **注意**：4xx 响应不触发熔断（客户端错误由客户端自身处理）。

### 15.4 智能探活（Smart Probe）

> 自 v2.24.5 起，当 `health_check_path` 留空时，所有 target 均自动进行主动健康检查，无需手动配置探测路径。

**工作原理**：

每个 target 的第一次健康检查时，系统按优先级依次尝试以下策略，找到第一个有效的后缓存（默认 2 小时）：

**通用 target（provider 非 anthropic）的探测顺序**：

| 顺序 | 策略 | 适合服务 | 视为"端点存在"的状态码 |
|------|------|----------|----------------------|
| 1 | `GET /health` | vLLM、sglang、自建代理 | 200 |
| 2 | `GET /v1/models` | OpenAI 兼容服务 | 200、401、403 |
| 3 | `POST /v1/chat/completions` | 通用兜底 | 200、401、403、400 |

**Anthropic provider target 的探测顺序**（provider-specific 策略优先）：

| 顺序 | 策略 | 适合服务 | 视为"端点存在"的状态码 |
|------|------|----------|----------------------|
| 1 | `GET /v1/models`（Anthropic 专用） | Anthropic 兼容（含华为云） | 200、401、403、400 |
| 2 | `POST /v1/messages`（Anthropic 专用） | Anthropic 兼容 | 200、401、403、400 |
| 3 | `GET /health` | vLLM、sglang、自建代理 | 200 |
| 4 | `GET /v1/models` | OpenAI 兼容服务 | 200、401、403 |
| 5 | `POST /v1/chat/completions` | 通用兜底 | 200、401、403、400 |

**两阶段语义**：

- **发现阶段**：401/403 表示"端点存在、有认证机制" → 记录为有效探测路径
- **正式心跳**：401/403 表示"API Key 无效" → 标记 target 为不健康

这意味着：即使是只返回 401 的服务（如使用错误 key 的华为云、小米），也能被成功发现探测路径；但一旦进入正式心跳周期，401 仍会触发熔断计数。

**缓存失效时机**：

| 触发条件 | 行为 |
|----------|------|
| 凭证（API Key）变更 | 立即清除缓存，下次重新探测 |
| 目标失去显式 `health_check_path`（路径被清除） | 立即清除缓存，下次重新探测 |
| 心跳返回连接错误 | 清除缓存，下次重新探测 |
| 心跳返回非预期状态码 | 清除缓存，下次重新探测 |
| TTL 到期（默认 2h） | 自然过期，下次重新探测 |

> **不变量**：`unreachable` 状态（连接拒绝/DNS 失败）**不会**被缓存。每次心跳周期均会重新尝试 Discover，确保服务恢复后能在下一个心跳周期内被重新探活，不存在"2h 锁死"问题。

**Discover 三种结果**：

| 结果 | 触发条件 | 含义 |
|------|----------|------|
| `method, unreachable=false` | 找到有效探测路径 | 缓存策略，立即执行初次心跳 |
| `nil, unreachable=true` | 硬连接失败（拒绝连接/DNS 错误） | 标记本次不健康，下次重新 Discover |
| `nil, unreachable=false` | 所有路径均超时，或 context 预算耗尽 | 不标记 unreachable，下次重新 Discover |

**各厂商实测结果**：

| 服务 | 自动发现的探测策略 |
|------|-------------------|
| Anthropic API | `GET /v1/models`（401 → 端点存在） |
| OpenAI API | `GET /v1/models`（200 → 健康） |
| 火山引擎 Ark（OpenAI 兼容） | `GET /v1/models`（200 → 健康） |
| 火山引擎 Ark（Anthropic 兼容） | `GET /v1/models`（401 → 端点存在） |
| 腾讯 LKEAP（OpenAI 兼容） | `GET /v1/models`（200 → 健康） |
| 腾讯 LKEAP（Anthropic 兼容） | `POST /v1/messages`（401 → 端点存在） |
| 华为云 ModelArts | `GET /v1/models`（401 → 端点存在） |
| 小米 MiMo | `GET /v1/models`（401 → 端点存在） |
| vLLM / sglang | `GET /health`（200 → 健康） |
| Ollama | `GET /health`（200 → 健康） |

> **注**：阿里云 DashScope（`/coding/v1` 路径）的 `/v1/models` 返回 404，所有内置策略均不匹配，当前无法主动探活，退化为纯被动熔断。

**何时手动指定 `health_check_path`**：

- 您有自建服务，已知健康检查路径（如 `/api/health`），希望跳过自动探测节省启动延迟
- 您需要使用与内置策略不同的路径（如 `/ping`）
- 您希望固定探测行为不受缓存 TTL 影响

```yaml
# 显式指定路径（优先于智能探活）
targets:
  - url: "https://my-custom-llm.company.com"
    api_key: "${MY_KEY}"
    provider: "openai"
    health_check_path: "/api/health"    # 显式指定，跳过自动探测
```

### 15.5 用户/分组 LLM 绑定

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

### 15.6 路由优先级

当请求到达时，路由目标按以下顺序决定：

1. **用户级绑定**（最高优先级）：若该用户有专属绑定且目标健康
2. **分组级绑定**：若用户所在分组有绑定且目标健康
3. **负载均衡**：从健康目标中按权重随机选取
4. **回退**：若均衡器无配置，简单轮询

### 15.7 结构化日志

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

### 15.8 滚动升级（Drain/Undrain）

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

### 16.9 协议自动转换（Claude CLI + Ollama）

**版本要求**: v2.6.0+

#### 16.9.1 功能概述

PairProxy 支持自动协议转换，允许 Claude Code（使用 Anthropic Messages API）无缝连接到 Ollama 或其他 OpenAI 兼容的后端，无需任何手动配置。

**使用场景**：
- 企业内部部署 Ollama 本地模型
- 使用 Claude Code 作为统一客户端访问不同后端
- 降低 API 成本，使用本地推理服务

**核心特性**：
- ✅ **零配置**：自动检测并转换，无需手动开关
- ✅ **双向转换**：Anthropic → OpenAI（请求）+ OpenAI → Anthropic（响应）
- ✅ **流式支持**：完整支持 SSE 流式响应
- ✅ **智能处理**：System 消息、结构化内容、finish_reason 自动映射
- ✅ **优雅降级**：转换失败时自动回退到原始请求

#### 16.9.2 配置示例

在 `sproxy.yaml` 中添加 Ollama target：

```yaml
llm:
  targets:
    # Anthropic API（用于 Claude 模型）
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      weight: 1

    # Ollama 本地服务（自动协议转换）
    - url: "http://localhost:11434"
      api_key: "ollama"              # Ollama 不需要真实 API Key
      provider: "ollama"              # 关键：设置 provider 为 ollama
      name: "Ollama Local"
      weight: 1
```

**重要**：必须设置 `provider: "ollama"` 或 `provider: "openai"`，sproxy 才会触发协议转换。

#### 16.9.3 工作原理

**自动检测条件**：
- 请求路径 = `/v1/messages`（Anthropic 格式）
- 目标 provider = `ollama` 或 `openai`

满足以上条件时，sproxy 自动执行：

1. **请求转换**（Anthropic → OpenAI）：
   - 路径：`/v1/messages` → `/v1/chat/completions`
   - System 字段：移动到 messages 数组第一条
   - Content 结构：提取 text 类型内容
   - 流式请求：自动注入 `stream_options.include_usage: true`

2. **响应转换**（OpenAI → Anthropic）：
   - 非流式：JSON 结构映射
   - 流式：SSE 事件实时转换
   - finish_reason 映射：
     - `stop` → `end_turn`
     - `length` → `max_tokens`
     - `content_filter` → `stop_sequence`

#### 16.9.4 使用步骤

**1. 启动 Ollama**

```bash
# 安装 Ollama（macOS/Linux）
curl -fsSL https://ollama.com/install.sh | sh

# 拉取模型
ollama pull llama3.2

# 启动服务（默认端口 11434）
ollama serve
```

**2. 配置 sproxy**

编辑 `sproxy.yaml`，添加上述 Ollama target，然后重启：

```bash
systemctl restart sproxy
```

**3. 绑定用户到 Ollama**

```bash
# 将用户 alice 绑定到 Ollama target
./sproxy admin llm bind alice --target http://localhost:11434

# 或者绑定整个分组
./sproxy admin llm bind --group engineering --target http://localhost:11434
```

**4. 使用 Claude Code**

无需任何客户端配置变更，Claude Code 继续使用 Anthropic API 格式，sproxy 自动转换：

```bash
# 开发者端无需任何操作，正常使用 Claude Code
# sproxy 会自动将请求转换为 OpenAI 格式发送给 Ollama
```

#### 16.9.5 验证转换是否生效

**查看 sproxy 日志**：

```bash
# 查看协议转换日志
journalctl -u sproxy -f | grep "protocol conversion"
```

**日志示例**（转换成功）：

```
INFO  sproxy  protocol conversion triggered  path=/v1/messages target_provider=ollama
DEBUG sproxy  converted request  from=anthropic to=openai message_count=2 has_system=true
INFO  sproxy  protocol conversion completed  direction=request→openai status=success
INFO  sproxy  protocol conversion completed  direction=response→anthropic status=success
```

**日志示例**（无需转换）：

```
DEBUG sproxy  protocol conversion skipped  reason=same_provider path=/v1/messages target_provider=anthropic
```

#### 16.9.6 故障排查

**问题：请求失败，日志显示 "conversion failed"**

**原因**：请求 body 格式异常或包含不支持的字段。

**解决**：
1. 检查日志中的详细错误信息
2. 协议转换失败时会自动回退到原始请求，不影响服务
3. 如果持续失败，检查 Ollama 服务是否正常运行

**问题：响应内容为空或格式错误**

**原因**：Ollama 返回的响应格式与标准 OpenAI API 不完全一致。

**解决**：
1. 升级 Ollama 到最新版本
2. 检查 Ollama 日志：`ollama logs`
3. 验证模型是否正确加载：`ollama list`

**问题：Token 统计不准确**

**原因**：Ollama 可能不返回 usage 信息。

**解决**：这是 Ollama 的限制，sproxy 会记录 `input_tokens=0, output_tokens=0`。如需准确统计，建议使用支持 usage 字段的 OpenAI 兼容服务。

#### 16.9.7 性能与限制

**性能影响**：
- 协议转换在内存中完成，延迟 <1ms
- 流式响应实时转换，无缓冲延迟
- 对吞吐量无明显影响

**已知限制**：
1. **System 消息限制**：仅支持单个 system 字段，多个 system 消息会合并
2. **Token 统计依赖后端**：如果 Ollama 不返回 usage，统计为 0

> **v2.8.0 已解决的旧限制**：图片内容块（Anthropic base64 image）现已自动转换为 OpenAI `image_url` 格式；模型名称可通过 `model_mapping` 配置自动映射；OpenAI 格式错误响应会自动转换为 Anthropic 格式。

#### 16.9.8 相关文档

- 完整设计文档：`docs/PROTOCOL_CONVERSION.md`

### 16.10 v2.8.0 协议转换进阶功能

v2.8.0 在 v2.6.0 的基础上大幅增强了协议转换能力，覆盖更多真实使用场景：

#### 16.10.1 图片内容块转换

**问题**：Claude CLI 发送带图片的请求（Anthropic `image` 类型）到 Ollama 时失败。

**v2.8.0 解决方案**：自动将 Anthropic `image` 内容块转换为 OpenAI `image_url` 格式：

```json
// Anthropic 输入
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/jpeg",
    "data": "<base64-data>"
  }
}

// OpenAI 输出（自动转换）
{
  "type": "image_url",
  "image_url": {
    "url": "data:image/jpeg;base64,<base64-data>"
  }
}
```

外部 URL 格式同样支持：`"type": "url"` 时自动提取 `url` 字段。

#### 16.10.2 模型名称映射 (model_mapping)

**问题**：Anthropic 模型名称（如 `claude-sonnet-4-6`）Ollama 不认识。

**v2.8.0 解决方案**：在 sproxy.yaml 中配置 `model_mapping`：

```yaml
llm:
  targets:
    - url: "http://localhost:11434"
      provider: "ollama"
      name: "Local Ollama"
      # 协议转换时的模型名称映射
      model_mapping:
        "claude-sonnet-4-6": "qwen2.5:32b"       # 精确匹配
        "claude-haiku-*": "phi3:mini"             # 前缀匹配（暂不支持 glob）
        "*": "llama3:8b"                           # 通配符兜底
```

**匹配规则**：
1. 精确匹配优先（完整模型名）
2. 无精确匹配时，使用 `"*"` 键的值作为默认映射
3. 无任何匹配时，原样传递模型名

#### 16.10.3 OpenAI 错误响应自动转换

**问题**：Ollama 返回 OpenAI 格式错误时，Claude 客户端无法正确解析。

**v2.8.0 解决方案**：自动检测 OpenAI 格式错误并转换为 Anthropic 格式：

```json
// OpenAI 错误（Ollama 返回）
{"error": {"message": "model not found", "type": "invalid_request_error"}}

// Anthropic 格式（自动转换）
{"type": "error", "error": {"type": "invalid_request_error", "message": "model not found"}}
```

#### 16.10.4 ID 前缀替换

**问题**：OpenAI 响应 ID 使用 `chatcmpl-xxx` 前缀，Claude 客户端期望 `msg_xxx`。

**v2.8.0 解决方案**：自动将 `chatcmpl-` 前缀替换为 `msg_`，流式和非流式响应均处理。

#### 16.10.5 Assistant Prefill 拒绝

**背景**：Anthropic API 支持 assistant prefill（消息列表末尾放一条 assistant 消息，让模型续写）。OpenAI/Ollama 不支持此功能。

**v2.8.0 行为**：检测到 messages 末尾有 assistant 消息时，对 OpenAI/Ollama targets 返回 HTTP 400：

```json
{"type": "error", "error": {"type": "invalid_request_error", "message": "assistant prefill is not supported for OpenAI/Ollama targets"}}
```

#### 16.10.6 Thinking 参数拒绝

**背景**：Anthropic API 的扩展思考模式（`thinking` 参数）OpenAI/Ollama 不支持。

**v2.8.0 行为**：检测到请求体包含 `thinking` 字段时，对 OpenAI/Ollama targets 返回 HTTP 400：

```json
{"type": "error", "error": {"type": "invalid_request_error", "message": "thinking parameter is not supported for OpenAI/Ollama targets"}}
```

#### 16.10.7 强制 LLM 绑定

**背景**：未配置 LLM target 的用户/分组发起请求时，路由不明确。

**v2.8.0 行为**：未找到任何 LLM target 时返回 HTTP 403：

```json
{"type": "error", "error": {"type": "permission_error", "message": "no LLM target configured for this user"}}
```

**解决方法**：通过 CLI 或 Dashboard 为用户/分组绑定 LLM target，或确保有可用的全局 target。

---

## 17. 用户对话内容跟踪

### 17.1 功能概述

对话内容跟踪允许管理员针对**特定用户**记录其与 LLM 之间的完整对话内容（输入消息和助手回复），以用于审计、问题排查或合规记录。

与 `log.debug_file`（记录所有用户的 HTTP 传输内容）不同，对话跟踪：
- **按用户粒度控制**：只跟踪指定用户，不影响其他用户
- **记录对话语义内容**：提取 messages 数组和助手回复文本，而非原始 HTTP 字节
- **通过 shell 命令管理**：无需修改配置文件，无需重启服务，变更立即生效
- **支持 Anthropic 和 OpenAI 格式**：流式和非流式响应均可捕获

> **安全说明**：HTTPS 加密不影响此功能。sproxy 作为 TLS 终端节点，在应用层同时持有请求和响应的明文内容。

### 17.2 存储位置

对话记录的根目录由配置项 `track.dir` 决定，与数据库路径无关，独立配置：

| 场景 | 默认值 | 说明 |
|------|--------|------|
| 所有模式 | `./track`（相对于进程 CWD） | 未显式配置时的统一默认值 |
| Peer/PostgreSQL 多节点 | 需显式配置共享路径 | 确保所有节点写入同一目录 |

**Peer 模式下若需要所有节点共享 track 数据，必须在 `sproxy.yaml` 中将所有节点配置为相同的共享存储绝对路径（如 NFS 挂载点）**，在任意节点执行 `admin track` 命令即对集群所有节点生效。若各节点使用本地路径，则 track 数据分散在各节点，互不可见：

```yaml
track:
  dir: "/data/pairproxy/track"   # 使用有写权限的绝对路径
```

目录结构：

```
<track.dir>/
  users/
    alice          ← 标记文件（空文件，存在即表示 alice 已启用跟踪）
  conversations/
    alice/
      2026-03-07T12-34-56Z-req-abc123.json
      2026-03-07T15-22-01Z-req-def456.json
```

每个 JSON 文件包含：

```json
{
  "request_id": "req-abc123",
  "username": "alice",
  "timestamp": "2026-03-07T12:34:56Z",
  "provider": "anthropic",
  "model": "claude-3-opus-20240229",
  "messages": [
    {"role": "user", "content": "请帮我审查这段代码"}
  ],
  "response": "这段代码有以下几个问题...",
  "input_tokens": 150,
  "output_tokens": 320
}
```

### 17.3 管理命令

所有命令均在 sproxy 运行时生效，**无需重启**：

```bash
# 启用对 alice 的跟踪（此后 alice 的所有对话均被记录）
sproxy admin track enable alice

# 停用跟踪（现有记录文件保留，不会被删除）
sproxy admin track disable alice

# 列出所有当前已启用跟踪的用户
sproxy admin track list

# 列出 alice 的对话记录文件（最新在前，含文件大小）
sproxy admin track show alice

# 删除 alice 的所有对话记录文件（跟踪状态不受影响）
sproxy admin track clear alice
```

**`show` 输出示例：**
```
Conversations for alice [tracking: ENABLED] — 3 record(s):
    1. 2026-03-07T15-22-01Z-req-def456.json  (2847 bytes)
    2. 2026-03-07T12-34-56Z-req-abc123.json  (1923 bytes)
    3. 2026-03-06T09-11-30Z-req-xyz789.json  (4102 bytes)

Location: /var/lib/pairproxy/track/conversations/alice
```

### 17.4 典型场景

#### 排查特定用户问题

```bash
# 1. 启用跟踪
sproxy admin track enable problemuser

# 2. 等待用户复现问题...

# 3. 查看记录
sproxy admin track show problemuser

# 4. 读取具体记录（直接 cat JSON 文件）
cat /var/lib/pairproxy/track/conversations/problemuser/2026-03-07T12-34-56Z-req-xxx.json

# 5. 排查完毕后停用并清理
sproxy admin track disable problemuser
sproxy admin track clear problemuser
```

#### 合规审计

```bash
# 启用对需审计用户的持续跟踪
sproxy admin track enable contractor1
sproxy admin track enable contractor2

# 定期查看跟踪状态
sproxy admin track list

# 月度导出（JSON 文件可直接归档或导入日志系统）
ls /var/lib/pairproxy/track/conversations/contractor1/
```

### 17.5 注意事项

- **磁盘空间**：长时间跟踪活跃用户会产生大量文件，建议定期使用 `track clear` 清理历史记录
- **隐私合规**：对话内容含用户数据，请确保符合所在地区的数据保护法规，并告知被跟踪用户
- **文件权限**：跟踪目录权限由 sproxy 进程的运行用户决定（默认 0755/0644）
- **无加密**：记录文件以明文 JSON 存储，如需加密请在文件系统层面处理

---

## 18. 可靠性增强（v2.5.0）

本节介绍 v2.5.0 引入的四项可靠性改进，无需额外操作即可生效（均有合理默认值）。

### 18.1 Worker 用量数据可靠性（改进项2）

**问题**：原有实现中，worker 节点的用量数据仅在心跳时实时上报，若 primary 短暂不可用，数据会丢失。

**改进**：worker 将用量记录写入本地 SQLite DB，每次心跳时批量读取未同步记录并上报给 primary。上报成功后标记为已同步（水印追踪），primary 宕机期间数据不丢失，恢复后自动补报。

**配置**（`sproxy.yaml`，worker 节点）：
```yaml
cluster:
  usage_buffer:
    enabled: true              # 默认 true
    max_records_per_batch: 1000  # 每批最多上报条数
```

**可观测性**：`Reporter.UsageReportFails()` 和 `Reporter.PendingRecords()` 可接入 Prometheus 告警。

---

### 18.2 健康检查优化（改进项3）

**改进**：新增可配置的健康检查参数，支持更精细的熔断控制。

**配置**（`cproxy.yaml`）：
```yaml
sproxy:
  health_check_timeout: 3s              # 单次检查超时（默认 3s）
  health_check_failure_threshold: 3     # 连续失败多少次后熔断（默认 3）
  health_check_recovery_delay: 60s      # 熔断后自动恢复延迟（默认 60s；0=禁用）
  passive_failure_threshold: 3          # 被动熔断阈值（默认 3）
```

**行为说明**：
- 主动健康检查（`health_check_path` 非空时）：连续失败 `failure_threshold` 次后熔断
- 被动熔断：请求失败（502/503/504）累计 `passive_failure_threshold` 次后熔断
- 熔断后经过 `recovery_delay` 自动恢复，或等待下次主动检查通过

---

### 18.3 路由表主动发现（改进项4）

**问题**：原有实现中，cproxy 只能通过响应头被动接收路由表更新，若长时间无请求则路由表可能过期。

**改进**：cproxy 新增主动轮询机制，定期向 sproxy 的 `/cluster/routing-poll` 端点查询路由表。若路由表无变化，sproxy 返回 304 Not Modified，不消耗带宽。

**配置**（`cproxy.yaml`）：
```yaml
sproxy:
  shared_secret: "${CLUSTER_SECRET}"  # 与 sproxy cluster.shared_secret 一致
  routing_poll_interval: 60s          # 轮询间隔（默认 60s；0=禁用）
```

> **注意**：`shared_secret` 为空时禁用主动轮询，cproxy 仍可通过响应头被动接收更新。

---

### 18.4 请求级重试（改进项5）

**改进**：非流式请求（`stream: false` 或无 stream 字段）在遇到可重试状态码时，自动切换到其他健康节点重试，对用户透明。

**配置**（`cproxy.yaml`）：
```yaml
sproxy:
  retry:
    enabled: true                     # 默认 true
    max_retries: 2                    # 最大重试次数（不含首次，默认 2）
    retry_on_status: [502, 503, 504]  # 触发重试的状态码
```

**行为说明**：
- 流式请求（`"stream": true`）**不走重试路径**，直接使用 ReverseProxy 转发
- 重试时优先选择未尝试过的健康节点（避免重复打到同一故障节点）
- 所有节点均失败时返回 502，响应体包含 `"all_targets_exhausted"` 错误码
- 每次失败会通知健康检查器（被动熔断计数）

---

## 19. 升级指南

> 详细的版本变更记录、数据库 Schema 迁移步骤和回滚方法见 [`docs/UPGRADE.md`](./UPGRADE.md)。

### 通用升级流程

```bash
# 1. 备份数据库
cp pairproxy.db pairproxy.db.bak

# 2. 停止 sproxy
systemctl stop sproxy   # 或 kill -TERM <pid>

# 3. 替换二进制
cp sproxy-new /usr/local/bin/sproxy
cp cproxy-new /usr/local/bin/cproxy

# 4. 启动（AutoMigrate 自动应用 Schema 变更）
systemctl start sproxy

# 5. 验证
curl http://localhost:9000/health
```

### v2.4.0 升级说明

- **无数据库变更**，直接替换二进制即可
- 首次启动后自动创建 `<track.dir>/` 目录（对话跟踪存储，默认 `./track`）
- 新增 `sproxy admin track` 系列命令，见 [§17](#17-用户对话内容跟踪)

### v2.9.0 升级说明

- **无数据库变更**，直接替换二进制即可
- 新增 `/keygen/` WebUI 端点，无需额外配置即可使用
- 新增 `/anthropic/` 和 `/v1/` 直连路由（混合模式），与旧版 cproxy 路由完全兼容
- 协议转换补全：`content_filter` finish_reason 现在正确映射为 `end_turn`；流式 `message_delta` 携带准确 `input_tokens`

### v2.13.0 升级说明

- **无 Breaking Change**，SQLite 配置无需任何修改
- 如需切换到 PostgreSQL：
  1. 在 `sproxy.yaml` 中设置 `database.driver: "postgres"` 并配置连接信息
  2. 启动 sproxy —— AutoMigrate 自动建表，无需手动操作
  3. PostgreSQL 模式下 ConfigSyncer 自动禁用（节点共享同一 DB）
- 如继续使用 SQLite：配置文件无需任何修改，行为与 v2.12.0 完全一致

---

## 20. Direct Proxy — sk-pp- API Key 直连（v2.9.0）

> **适用场景**：不想在本地运行 cproxy 进程，直接用 API Key 访问 sproxy。

### 20.1 概述

v2.9.0 引入 **Direct Proxy** 模式：用户通过 `sk-pp-` 前缀的 API Key 直接访问 sproxy，无需本地 cproxy 进程。sproxy 根据 Key 内嵌的用户名指纹识别用户身份，享受与 cproxy 模式完全相同的配额控制和用量统计。

**接入路径**：

| 路径 | 头格式 | 兼容客户端 |
|------|--------|-----------|
| `/v1/messages`、`/v1/chat/completions` 等 | `Authorization: Bearer sk-pp-...` | Claude Code、OpenAI SDK、curl |
| `/anthropic/v1/messages` 等 | `x-api-key: sk-pp-...` | Anthropic SDK、curl |

### 20.2 获取 API Key

#### 方式一：自助 WebUI（推荐）

1. 浏览器访问 `http://<sproxy-host>:9000/keygen/`
2. 输入用户名和密码登录
3. 页面显示您的 API Key 及 Claude Code / OpenCode 配置片段
4. 点击「复制」或「重新生成」管理 Key

#### 方式二：REST API

```bash
# 登录获取 Key 和 session token
curl -X POST http://localhost:9000/keygen/api/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"Password123"}'

# 响应
{
  "username": "alice",
  "key": "sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "token": "<session-jwt>",
  "expires_in": 3600
}

# 用 session token 重新生成 Key（旧 Key 立即失效）
curl -X POST http://localhost:9000/keygen/api/regenerate \
  -H "Authorization: Bearer <session-jwt>"
```

### 20.3 配置客户端

#### Claude Code

```bash
export ANTHROPIC_API_KEY="sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
export ANTHROPIC_BASE_URL="http://<sproxy-host>:9000"
claude "Hello"
```

#### OpenCode / 兼容 OpenAI 格式的客户端

```bash
export OPENAI_API_KEY="sk-pp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
export OPENAI_BASE_URL="http://<sproxy-host>:9000/v1"
```

#### curl 直接调用

```bash
# Anthropic 格式
curl http://localhost:9000/v1/messages \
  -H "Authorization: Bearer sk-pp-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-4-5","max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}'

# OpenAI 格式（自动协议转换到 Anthropic）
curl http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer sk-pp-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-4-5","messages":[{"role":"user","content":"Hi"}]}'
```

### 20.4 API Key 安全说明

- **Key 不存储在服务端**：sproxy 通过解析 Key 内嵌的用户名指纹识别用户，无需数据库查询（有 LRU 缓存）
- **重新生成立即生效**：旧 Key 因指纹不匹配而立即失效，无需通知服务端
- **Key 格式**：`sk-pp-` 前缀 + 48 位字母数字随机串（含用户名字符指纹）
- **保密要求**：请像保管密码一样保管您的 Key；怀疑泄漏时，通过 `/keygen/` WebUI 重新生成

### 20.5 管理员注意事项

- Direct Proxy 模式使用与 cproxy 相同的配额和用量统计，**无需额外配置**
- 用户必须是活跃状态（`is_active=true`）才能使用 Key
- 禁用用户（`sproxy admin user disable <name>`）后，其 Key 立即失效
- 用量记录在同一 `usage_logs` 表，Dashboard 和统计命令均可查看

---

## 21. v2.9.1 更新说明（Patch）

v2.9.1 是针对 v2.9.0 的 Bug Fix 小版本，无破坏性变更，直接替换二进制即可升级。

### 21.1 Bug 修复

#### 21.1.1 扩展思考参数静默剥离（重要）

**问题**：Claude CLI 开启扩展思考（Extended Thinking）后，所有请求均携带 `thinking` 参数。当用户被路由到 OpenAI/Ollama target 时，sproxy 之前版本会直接返回 HTTP 400，导致 Claude CLI **完全无法使用**。

**修复**：sproxy 现在会**静默剥离** `thinking` 参数后继续转换和转发请求。OpenAI/Ollama 不感知该参数，请求正常完成。

- 用户侧：无感知，Claude CLI 无需任何配置变更
- 日志：转换时仅记录一条 DEBUG 级别日志，不再产生 WARN

```
# 修复后的日志（DEBUG 级别，默认不显示）
DEBUG  sproxy  thinking parameter stripped for OpenAI/Ollama target  request_id=xxx
```

#### 21.1.2 Dashboard llm.html 模板 panic 修复

**问题**：在极端情况下（LLM 绑定记录的 `user_id` 和 `group_id` 均为 NULL），访问 Dashboard LLM 页面会触发 Go 模板引擎的 nil 指针解引用 panic，返回 500 错误并在日志中输出 `template execute error`。

**修复**：对 `group_id` 字段增加 nil 判断（`{{else if .GroupID}}`），空绑定记录安全渲染为空白，不再 panic。

#### 21.1.3 浏览器预取请求触发虚假告警

**问题**：浏览器在访问 Dashboard 时自动发起 `/favicon.ico`、`/robots.txt` 等请求，这些请求会落入 LLM 代理的 catch-all 路由，触发 auth 中间件的 `WARN "missing authentication header"` 告警，污染日志。

**修复**：在 catch-all 路由之前增加专属处理器拦截上述路径，返回正确响应，不再触发告警。

#### 21.1.4 鉴权失败日志补充诊断字段

`WARN "missing authentication header"` 日志现在额外包含 `path` 和 `method` 字段，便于定位真实鉴权失败时的请求来源。

```
# 修复前
WARN  missing authentication header  request_id=xxx  remote_addr=1.2.3.4:5678

# 修复后
WARN  missing authentication header  request_id=xxx  path=/v1/messages  method=POST  remote_addr=1.2.3.4:5678
```

### 21.2 新功能：Dashboard 清空日志

日志页（`/dashboard/logs`）新增**清空日志**功能，用于在调试或环境重置时一次性清除所有请求日志。

**操作步骤**：

1. 登录 Dashboard，进入「请求日志」页面
2. 点击右上角红色 **清空日志** 按钮
3. 在弹出的确认对话框中输入 `OK`（区分大小写）
4. 点击 **确认清空**

**注意事项**：

- 此操作**不可撤销**，所有 `usage_logs` 记录将被永久删除
- 操作会写入审计日志（`audit_logs`），记录删除条数
- 操作成功后页面顶部显示 flash 提示，包含已删除的记录数
- 输入非 `OK`（如 `ok`、`Ok`、带空格）均视为无效，操作不执行

### 21.3 升级方法

v2.9.1 与 v2.9.0 数据库 Schema 完全兼容，无需迁移：

```bash
# 1. 备份数据库（推荐）
./sproxy admin backup --output pairproxy_before_v291.db.bak

# 2. 停止服务
pkill sproxy   # Linux/macOS
# 或 Windows: taskkill /F /IM sproxy.exe

# 3. 替换二进制
curl -LO https://github.com/l17728/pairproxy/releases/download/v2.9.1/pairproxy-linux-amd64.tar.gz
tar -xzf pairproxy-linux-amd64.tar.gz

# 4. 重新启动
./sproxy start --config sproxy.yaml

# 5. 验证版本
./sproxy version
# 应输出：sproxy v2.9.1
```

---

## 22. v2.9.2 更新说明（Patch）

v2.9.2 是针对 v2.9.1 的 Bug Fix 小版本，无破坏性变更，直接替换二进制即可升级。

### 22.1 Bug 修复

#### 22.1.1 Dashboard「我的用量」页图表无限下推

**问题**：在「用户流量查看」（`/dashboard/my-usage`）页面，选择用户后，用量历史柱状图会不断向下撑大页面，无法正常显示。

**根本原因**：Chart.js 配置了 `responsive: true` + `maintainAspectRatio: false` 时，图表高度取决于父容器高度。原来的写法把 `height="200"` 直接写在 `<canvas>` 标签上，Chart.js 不以此作为约束，而是去量父容器——而父容器没有固定高度，于是图表撑大父容器，父容器再撑大图表，形成无限循环。

**修复**：用固定高度的 `<div style="position: relative; height: 200px;">` 包裹 `<canvas>`，给 Chart.js 一个固定的尺寸参考点，与 Overview 页面图表的实现方式保持一致。

**影响范围**：仅影响管理员使用的「用户流量查看」页面，功能行为（数据、交互）完全不变。

### 22.2 升级方法

v2.9.2 与 v2.9.1 数据库 Schema 完全兼容，无需迁移：

```bash
# 1. 备份数据库（推荐）
./sproxy admin backup --output pairproxy_before_v292.db.bak

# 2. 停止服务
pkill sproxy   # Linux/macOS
# 或 Windows: taskkill /F /IM sproxy.exe

# 3. 替换二进制
curl -LO https://github.com/l17728/pairproxy/releases/download/v2.9.2/pairproxy-linux-amd64.tar.gz
tar -xzf pairproxy-linux-amd64.tar.gz

# 4. 重新启动
./sproxy start --config sproxy.yaml

# 5. 验证版本
./sproxy version
# 应输出：sproxy v2.9.2
```

---

## §23 v2.9.3 更新说明

**版本**: v2.9.3 — 安全加固 patch

### 23.1 安全修复：Direct Proxy 禁用用户缓存未失效

**问题描述**

当管理员通过 `sproxy admin user disable <username>` 禁用某用户后，
若该用户的 `sk-pp-` API Key 仍在内存缓存（LRU KeyCache）中，
则在缓存 TTL（默认 1 小时）到期前，该用户仍可通过 Direct Proxy 正常访问。

**修复方案**

在每次缓存命中后，新增对 `IsUserActive` 的二次数据库校验（单次按主键索引查询，开销极低）：

- 用户仍活跃：正常放行
- 用户已被禁用：立即返回 `HTTP 401 account_disabled`，并驱逐缓存条目
- 数据库查询失败：返回 `HTTP 500 internal_error`（fail-closed 原则）

**效果**: 用户禁用后，下一次请求立即被拒绝，不再等待 TTL 自然过期。

### 23.2 安全修复：API Key 混淆存储

**问题描述**

上游 LLM Provider API Key（Anthropic `sk-ant-*`、OpenAI `sk-*` 等）
以明文形式存储在 SQLite 数据库的 `api_keys.encrypted_value` 字段，
获得数据库文件读取权限的攻击者可直接读取真实密钥。

**混淆算法**

对 API Key 的 body 部分（最后一个 `-` 之后的字符串）执行首尾字符交换：

```
sk-ant-api03-ABCDEFGH  →  sk-ant-api03-HBCDEFGA
sk-pp-abcdefghijklmn   →  sk-pp-nbcdefghijklma
ollama                 →  allamo  (无破折号时交换整体首尾)
```

- **对称操作**：加解密使用同一函数，无密钥依赖
- **自动迁移**：服务重启时已有的明文记录自动应用混淆并更新

**安全说明**

混淆不是加密，不能防御能够读写数据库的攻击者。
其价值在于防止被动数据泄露（数据库备份外泄、日志截屏等场景）。
如需更强保护，建议对数据库文件本身进行加密。

### 23.3 升级指南

```bash
# 1. 停止服务
pkill sproxy  # Linux/macOS
# Windows: 停止 Windows 服务

# 2. 备份数据库（重要）
./sproxy admin backup --output pairproxy_pre_v2.9.3.db.bak

# 3. 替换二进制
# 下载 v2.9.3 sproxy 并替换

# 4. 启动服务
./sproxy start
# 服务启动时自动迁移现有 API Key 至混淆格式

# 5. 验证版本
./sproxy version
# 应输出：sproxy v2.9.3
```

## §24 v2.9.4 更新说明

**版本**: v2.9.4 — Dockerfile 修复 patch

### 24.1 修复：Docker 镜像版本号始终显示 `dev`

**问题描述**

通过 Docker 镜像部署时，`./sproxy version` 输出的版本号始终为 `dev`，
无法通过版本号确认镜像是否为预期版本。

**根因**

`Dockerfile` 中 `-ldflags` 使用了错误的模块路径：

```dockerfile
# 错误（修复前）
-X github.com/pairproxy/pairproxy/internal/version.Version=${VERSION}

# 正确（修复后）
-X github.com/l17728/pairproxy/internal/version.Version=${VERSION}
```

Go 编译器在 `-X` 指定的包路径不存在时**静默跳过**，不报任何错误，
导致 `version.Version`、`version.Commit`、`version.BuiltAt` 三个变量
均保持默认值（`dev` / `unknown` / `unknown`）。

二进制发布包（`make release`）和 CLI 版本号不受影响，仅 Docker 镜像受影响。

### 24.2 修复：builder 基础镜像版本不存在

`Dockerfile` 使用的 `golang:1.25-alpine` 在 Docker Hub 上不存在（Go 最新稳定版为 1.24.x），
导致本地 `docker build` 会因 pull 失败而报错。已更正为 `golang:1.24-alpine`。

### 24.3 升级指南

此版本无数据库变更，直接替换镜像即可：

```bash
# 拉取最新镜像
docker pull ghcr.io/l17728/pairproxy:v2.9.4

# 或更新 docker-compose.yml 中的镜像 tag 后重启
docker compose up -d

# 验证版本
docker run --rm ghcr.io/l17728/pairproxy:v2.9.4 version
# 应输出：sproxy v2.9.4 (...)
```

非 Docker 部署用户无需升级，v2.9.3 二进制版本号完全正确。

## §25 v2.10.0 更新说明

**版本**: v2.10.0 — OtoA 双向协议转换（OpenAI 客户端透明访问 Anthropic 端点）

### 25.1 新功能：OtoA 协议转换

v2.10.0 实现了与 v2.6.0 方向相反的协议转换：**OpenAI 格式客户端 → Anthropic 端点**。

#### 使用场景

| 方向 | 客户端 | 目标后端 | 触发条件 |
|------|--------|----------|----------|
| AtoO（v2.6.0+）| Claude CLI（Anthropic 格式）| Ollama / OpenAI 兼容 | target `provider: ollama/openai` |
| **OtoA（v2.10.0）** | **OpenAI 格式客户端**（Cursor、Continue.dev 等）| **Anthropic API** | target `provider: anthropic`，请求路径 `/v1/chat/completions` |

#### OtoA 转换流程

```
OpenAI 客户端 → PairProxy → Anthropic API
/v1/chat/completions        /v1/messages
   (OpenAI JSON)   ↓ OtoA ↓   (Anthropic JSON)

请求转换:
  messages (含 system role) → system + messages
  tools (function) → Anthropic tool 格式
  stop / model 名称映射

响应转换:
  id: chatcmpl-xxx → msg_xxx
  finish_reason → stop_reason
  usage.prompt_tokens → input_tokens
  usage.completion_tokens → output_tokens
  tool_calls → tool_use content block

流式转换:
  Anthropic SSE → OpenAI SSE (AnthropicToOpenAIStreamConverter)
```

#### 配置示例

```yaml
llm:
  targets:
    # OtoA 场景：OpenAI 客户端访问 Anthropic
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"
      weight: 1
```

将 OpenAI 格式客户端指向 PairProxy，无需任何额外配置：

```bash
# Cursor / Continue.dev / 任意 OpenAI 兼容客户端
export OPENAI_API_KEY="<pairproxy-jwt>"
export OPENAI_BASE_URL="http://localhost:9000"

# 请求自动路由到 Anthropic 端点
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer <pairproxy-jwt>" \
  -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"Hello"}]}'
```

#### 防双计费保护

OtoA 转换中，Anthropic 后端会返回真实用量数据。系统使用 `otoaRecorded` 标志确保
token 用量只记录一次，避免从 OpenAI 响应格式重复统计。

### 25.2 内部重构：conversionDirection 枚举

替换原有 `bool` 标志，引入类型化枚举：

```go
type conversionDirection int

const (
    conversionNone conversionDirection = iota  // 不转换
    conversionAtoO                             // Anthropic → OpenAI（v2.6.0）
    conversionOtoA                             // OpenAI → Anthropic（v2.10.0）
)
```

`detectConversionDirection()` 统一判断转换方向，代理逻辑更清晰。

### 25.3 测试覆盖

本版本新增 45 个测试，覆盖：
- OtoA 请求转换（messages/tools/system/stop/model mapping）
- OtoA 非流式响应转换（id 前缀、finish_reason、usage 字段）
- OtoA 流式转换（`AnthropicToOpenAIStreamConverter` 全事件类型）
- 防双计费逻辑
- re-pick 路由修正（`effectivePath="/v1/messages"`）

### 25.4 升级指南

此版本无数据库变更，直接替换二进制即可：

```bash
# 下载最新版
# https://github.com/l17728/pairproxy/releases/tag/v2.10.0

# 停止旧进程，替换二进制，重启
./sproxy start --config sproxy.yaml

# 验证版本
./sproxy version
# 应输出：sproxy v2.10.0 (...)
```

## §26 v2.10.1 更新说明

**版本**: v2.10.1 — lint 修复 + 文档刷新

### 26.1 变更内容

此为 patch 版本，无新功能，无数据库变更。

#### Bug 修复

- **lint: singleCaseSwitch**（`internal/proxy/protocol_converter.go`）：`AnthropicToOpenAIStreamConverter` 中 `content_block_start` handler 的单 case switch 改写为 if，消除 gocritic 告警，CI 全绿。

#### 文档刷新

- `docs/manual.md`：版本号更新至 v2.10.1，新增本节
- `docs/TEST_REPORT.md`：测试数更新（1,870 RUN），覆盖率按包精确列出
- `docs/ACCEPTANCE_REPORT.md`：版本号、代码规模（57,579 行）、测试数、覆盖率表全量更新
- `README.md`：版本号、测试数、代码规模同步更新

### 26.2 升级指南

无数据库变更，直接替换二进制即可：

```bash
# https://github.com/l17728/pairproxy/releases/tag/v2.10.1

./sproxy start --config sproxy.yaml

./sproxy version
# 应输出：sproxy v2.10.1 (...)
```

OtoA 功能自动生效，无需任何配置变更。现有 AtoO 配置不受影响。

## §27 v2.12.0 更新说明

**版本**: v2.12.0 — Worker 节点一致性修复

本版本解决多 Worker 部署场景下的 15 个一致性缺陷，使 Worker 节点成为配置只读节点，
并通过定期从 Primary 拉取配置快照保持数据一致性。

---

### 27.1 新增功能概览

| 问题 | 分级 | 修复内容 |
|------|------|---------|
| 用户禁用后 Worker 仍可登录 | P0 | ConfigSyncer 30s 内同步 `IsActive=false`，同时删除 `refresh_tokens` |
| Worker 用户 ID 与 Primary 不一致 | P1 | 同步时保留 Primary 的 user_id |
| Worker 写操作未封锁 | P2 | 所有 POST/PUT/DELETE 返回 403 `worker_read_only` |
| Worker 统计范围不明确 | P3 | 响应头 `X-Node-Role: worker` + `X-Stats-Scope: local` |
| LLM 绑定/Target 不同步 | P1 | ConfigSyncer 同步 llm_targets 和 llm_bindings |
| CLI 写命令在 Worker 上失败 | P2 | help-all 和 cheatsheet 标注 `[primary-only]` |

---

### 27.2 Worker 节点配置同步（Phase 1）

#### 原理

Primary 节点新增内部 API：

```
GET /api/internal/config-snapshot
Authorization: Bearer <shared_secret>
```

响应格式：

```json
{
  "version": "2026-03-17T10:00:00Z",
  "users": [...],
  "groups": [...],
  "llm_targets": [...],
  "llm_bindings": [...]
}
```

Worker 节点的 `ConfigSyncer` 每 30 秒（与 reporter 同一间隔）拉取一次，
使用 GORM `Save()` 批量 upsert 到本地 SQLite DB。

#### 配置（Worker 节点 `sproxy.yaml`）

```yaml
cluster:
  role: "worker"
  primary: "http://sp1.company.com:9000"   # Primary 地址
  self_addr: "http://sp2.company.com:9000"
  shared_secret: "${CLUSTER_SECRET}"        # 与 Primary 一致
  report_interval: 30s                      # 同步间隔
```

配置正确后 Worker 启动日志会显示：

```
INFO  config_syncer  sync completed  users=5 groups=2 llm_targets=3 llm_bindings=5
```

#### 容错行为

- Primary 不可达时：Worker 继续使用本地 DB 数据提供服务，不中断请求处理
- Primary 返回非 200：本次同步跳过，等待下一轮，本地数据不受影响
- Primary 返回畸形 JSON：同上，计入 `PullFailures` 计数器（可通过指标观测）
- 用户被禁用时：同步删除该用户的所有 `refresh_tokens`，强制下线

---

### 27.3 Worker 写操作封锁（Phase 2）

#### Admin API 封锁

Worker 节点上所有写 API（POST/PUT/DELETE）返回 403：

```json
{
  "error": "worker_read_only",
  "message": "write operations not allowed on worker nodes; perform admin operations on the primary node"
}
```

受封锁的端点：

| 端点 | 方法 |
|------|------|
| `/api/admin/users` | POST |
| `/api/admin/users/:id/active` | PUT |
| `/api/admin/users/:id/password` | PUT |
| `/api/admin/groups` | POST |
| `/api/admin/groups/:id` | DELETE |
| `/api/admin/llm/bindings` | POST |
| `/api/admin/llm/bindings/:id` | DELETE |
| `/api/admin/llm/distribute` | POST |
| `/api/admin/llm/targets` | POST/PUT/DELETE |
| `/keygen/api/login` | POST |
| `/keygen/api/regenerate` | POST |
| 所有 Dashboard 写表单路由 | POST |

**只读端点保持可用**（GET `/api/admin/users` 等）。

#### Dashboard 只读横幅

Worker 节点 Dashboard 顶部会显示黄色提示横幅：

```
⚠️ Worker 节点（只读模式）— 用户和配额管理请在 Primary 节点 http://sp1.company.com:9000 执行
```

#### Worker 统计响应头

Worker 节点的统计端点（`/api/admin/stats/*`）会附加响应头：

```
X-Node-Role: worker
X-Stats-Scope: local
```

Dashboard 统计页在 Worker 模式会显示：

```
ℹ️ 显示本节点统计数据。全局汇总统计请访问 Primary 节点。
```

---

### 27.4 CLI Primary-only 标注（Phase 3）

`sproxy admin help-all` 输出中，所有写命令已标注 `[primary-only]`：

```
user add <username>        [primary-only]  新建用户
user disable <username>    [primary-only]  禁用用户
group add <name>           [primary-only]  新建分组
group set-quota <name>     [primary-only]  设置配额
llm bind <username>        [primary-only]  绑定 LLM
llm distribute             [primary-only]  均分用户到 LLM
...
```

在 Worker 节点执行这些命令会收到：

```
Error: 403 Forbidden: worker_read_only — write operations not allowed on worker nodes
```

---

### 27.5 新增测试（+33 RUN）

| 测试文件 | 新增数量 | 覆盖场景 |
|---------|---------|---------|
| `internal/cluster/config_syncer_test.go` | 8 | 快照拉取、幂等性、P0-2 Token 吊销、LLM同步、不可达/非200/畸形JSON |
| `internal/api/worker_readonly_test.go` | 7 | 写操作403、读操作200、统计响应头标注 |
| `internal/api/keygen_handler_test.go` | 2 | Worker写端点403、静态页200 |
| `internal/api/cluster_handler_test.go` | 补充 | `/api/internal/config-snapshot` 端点 |

所有测试使用内存 SQLite，无外部依赖，可在 CI 中无差异运行。

---

### 27.6 升级指南

此版本无数据库 Schema 变更（`config_syncer` 复用现有表），直接替换二进制即可。

```bash
# 停止旧进程
./sproxy stop  # 或 kill $(pgrep sproxy)

# 替换二进制（从 GitHub Releases 下载对应平台包）
# https://github.com/l17728/pairproxy/releases/tag/v2.12.0

# 启动
./sproxy start --config sproxy.yaml

# 验证版本
./sproxy version
# 应输出：sproxy v2.14.1 (...)
```

**Worker 节点配置补充**（若尚未配置 `cluster.shared_secret`）：

```yaml
cluster:
  role: "worker"
  primary: "http://sp1.company.com:9000"
  shared_secret: "your-32-char-secret"   # 与 Primary 的 cluster.shared_secret 一致
```

Primary 节点无需修改配置，`/api/internal/config-snapshot` 端点自动注册。

---

## 28. PostgreSQL 数据库支持（v2.13.0）

> **适用场景**：多节点集群部署、需要高并发写入、或希望所有节点实时共享同一数据库。

### 28.1 为什么需要 PostgreSQL 支持

SQLite 是单文件数据库，每个节点独立维护本地副本，依赖 ConfigSyncer 每 30 秒从 Primary 同步一次。这意味着：

- **30 秒一致性窗口**：用户刚创建或配额刚修改，Worker 节点最多 30 秒后才知晓
- **同步失败风险**：网络抖动时，Worker 可能使用过期配置（如已禁用用户仍能访问）
- **写并发瓶颈**：SQLite WAL 模式写操作仍需排他锁，高并发写入时存在性能瓶颈

PostgreSQL 解决方案：**所有节点共享同一个 PostgreSQL 实例**，直接读写相同数据，无一致性窗口，无需 ConfigSyncer 同步。

### 28.2 配置方法

在 `sproxy.yaml` 的 `database` 节新增配置：

```yaml
database:
  # 切换到 PostgreSQL
  driver: "postgres"

  # 方案 A：完整 DSN（推荐，便于注入 Secret）
  dsn: "${DATABASE_URL}"

  # 方案 B：独立字段（若 dsn 为空则从这些字段拼接）
  # host: "postgres.company.com"
  # port: 5432
  # user: "pairproxy"
  # password: "${DB_PASSWORD}"
  # dbname: "pairproxy"
  # sslmode: "require"    # disable | allow | prefer | require | verify-ca | verify-full

  # 连接池（可选，使用默认值即可）
  max_open_conns: 50       # PostgreSQL 默认 50（MVCC 支持高并发）
  max_idle_conns: 10
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m
```

> **SQLite 默认不变**：省略 `driver` 字段或设置 `driver: "sqlite"` 则使用 SQLite，行为与旧版完全一致。

### 28.3 默认值说明

| 参数 | SQLite 默认 | PostgreSQL 默认 |
|------|------------|----------------|
| `max_open_conns` | 25（文件库）/ 1（:memory:）| 50 |
| `max_idle_conns` | 10 | 10 |
| `conn_max_lifetime` | 1h | 1h |
| `conn_max_idle_time` | 10m | 10m |
| `port` | N/A | 5432 |
| `sslmode` | N/A | disable |

### 28.4 集群模式下的行为变化

**PostgreSQL 模式**：
- 所有节点（Primary + Workers）直接读写同一 PG 实例
- **ConfigSyncer 自动禁用**：不再需要 30 秒轮询同步
- 日志会输出：`"shared PostgreSQL detected — ConfigSyncer disabled"`
- **Worker 写操作封锁仍然保留**：防止多个 Worker 并发执行管理操作（如同时修改同一用户的配额）

**SQLite 模式（不变）**：
- 每个节点独立 SQLite 文件
- ConfigSyncer 每 30 秒从 Primary 同步配置快照
- Worker 写操作封锁（403 `worker_read_only`）

### 28.5 快速验证

```bash
# 1. 设置环境变量
export DATABASE_URL="host=localhost user=postgres password=test dbname=pairproxy port=5432 sslmode=disable"

# 2. 修改 sproxy.yaml
#    database:
#      driver: "postgres"
#      dsn: "${DATABASE_URL}"

# 3. 启动
./sproxy start --config sproxy.yaml

# 4. 验证日志
# 应看到：
# INFO  db  opening PostgreSQL database  dsn=host=localhost user=postgres password=*** dbname=pairproxy port=5432 sslmode=disable
# INFO  db  PostgreSQL database opened successfully  ...
# INFO  sproxy  shared PostgreSQL detected — ConfigSyncer disabled  ...
```

### 28.6 注意事项

- **纯 Go 驱动**：使用 `pgx/v5` 纯 Go 驱动，无 CGO 依赖，Windows 下直接可用
- **首次启动自动建表**：AutoMigrate 自动在 PostgreSQL 中创建所有表和索引
- **DSN 密码脱敏**：日志中 `password=***`，不会泄露真实密码
- **PostgreSQL 版本要求**：9.5+（支持 `CREATE INDEX IF NOT EXISTS`）
- **向后兼容**：不修改 `driver` 字段则行为与旧版完全一致

---

## §29 PostgreSQL 对等节点模式（v2.14.0）

> **适用版本**: v2.14.0+

### 29.1 概念与动机

在 PostgreSQL 模式下，所有节点直接读写同一个数据库，数据天然一致。此时传统的 Primary/Worker 区分已失去意义：
- Worker 的「写封锁」（403 `worker_read_only`）变成了不必要的限制
- Worker 向 Primary 推送用量（Reporter）变成了多余的操作
- Primary 内存中的 `PeerRegistry` 无法跨节点共享

**Peer 模式**解决了这些问题：所有节点完全对等，任意节点均可处理管理操作，通过数据库的 `peers` 表实现分布式节点发现。

| 机制 | SQLite 模式 | PostgreSQL Peer 模式 |
|------|------------|---------------------|
| 写封锁 | ✅ Worker 封锁（防孤立写） | ❌ 不需要（所有节点共享 DB） |
| ConfigSyncer | ✅ 30s 轮询 | ❌ 不需要（已在 v2.13.0 禁用） |
| Reporter 推送 | ✅ Worker→Primary 汇聚用量 | ❌ 不需要（直接写共享 DB） |
| Peer 发现 | Primary 内存维护 | ✅ 数据库 `peers` 表（分布式） |
| 管理操作 | 仅 Primary | ✅ 任意节点均可 |

### 29.2 启用 Peer 模式

#### 自动启用（推荐）

当 `database.driver = "postgres"` 且未显式设置 `cluster.role` 时，系统**自动**将角色设为 `"peer"`：

```yaml
database:
  driver: "postgres"
  dsn: "host=pg.company.com user=pairproxy password=secret dbname=pairproxy sslmode=disable"
  # cluster.role 不设置 → 自动设为 "peer"
```

启动日志会输出：
```
INFO  sproxy  database.driver=postgres detected, cluster.role auto-set to "peer"
INFO  sproxy  peer mode enabled — all nodes are equal, using PGPeerRegistry for discovery
```

#### 显式启用

```yaml
database:
  driver: "postgres"
  dsn: "host=pg.company.com user=pairproxy password=secret dbname=pairproxy sslmode=disable"
cluster:
  role: "peer"
  self_addr: "sproxy-1.company.com:9000"   # 本节点对外地址（供其他节点发现）
  self_weight: 50                           # 流量权重（0=使用默认值50）
```

> **注意**: `role: "peer"` 必须配合 `database.driver: "postgres"` 使用，否则启动时报错。

### 29.3 三种集群模式对比

```yaml
# 模式1: Standalone（默认，单节点无集群功能）
# cluster.role 不设置，database.driver 不设置

# 模式2: Primary + Worker（SQLite 经典集群）
# 节点1 (Primary)
cluster:
  role: "primary"
  self_addr: "sproxy-1:9000"
  shared_secret: "strong-secret"

# 节点2 (Worker)
cluster:
  role: "worker"
  primary_addr: "sproxy-1:9000"
  shared_secret: "strong-secret"

# 模式3: Peer（PostgreSQL 对等集群，v2.14.0 新增）
# 所有节点配置相同，指向同一 PG
database:
  driver: "postgres"
  dsn: "host=pg.company.com ..."
cluster:
  # role 不设置，自动为 "peer"
  self_addr: "sproxy-1:9000"   # 每个节点设置自己的地址
  self_weight: 50
```

### 29.4 PGPeerRegistry（节点发现机制）

Peer 模式使用 `peers` 数据库表进行节点注册和健康发现。

#### 生命周期

```
启动 → Heartbeat (UPSERT) → 写入 peers 表
         ↓
后台 ticker（每 30s）→ Heartbeat + EvictStale
         ↓
关闭 → Unregister → 将自身 is_active 设为 false
```

#### peers 表字段

| 字段 | 说明 |
|------|------|
| `id` / `addr` | 节点唯一标识，使用 `self_addr` |
| `weight` | 流量权重 |
| `is_active` | 是否在线（心跳超时或主动注销后设为 false） |
| `last_seen` | 最后一次心跳时间 |
| `registered_at` | 首次注册时间 |

#### 健康判定

- **staleTimeout** = 3 × heartbeatInterval（默认 30s × 3 = **90s**）
- `ListHealthy` 返回：`is_active = true AND last_seen > NOW() - 90s`
- `EvictStale` 将超时节点的 `is_active` 设为 false（不包括自身）

#### heartbeatInterval 配置

使用 `cluster.report_interval`（默认 30s）作为心跳间隔：

```yaml
cluster:
  report_interval: "30s"   # 心跳间隔，同时决定 staleTimeout（3×30s=90s）
```

### 29.5 路由端点（/cluster/routing）

Peer 模式下，`/cluster/routing` 端点从 `peers` 表读取健康节点列表，而非 Primary 的内存列表：

```bash
curl -H "Authorization: Bearer <shared_secret>" \
  http://sproxy-1:9000/cluster/routing
```

响应示例：
```json
{
  "peers": [
    {"id": "sproxy-1:9000", "addr": "sproxy-1:9000", "weight": 50, "is_active": true},
    {"id": "sproxy-2:9000", "addr": "sproxy-2:9000", "weight": 50, "is_active": true}
  ]
}
```

任意节点均可处理该请求，c-proxy 可配置任一 peer 地址拉取路由表。

### 29.6 快速部署示例（2 节点 Peer 集群）

**共享的 `sproxy.yaml` 模板**（两节点除 `self_addr` 外相同）：

```yaml
listen:
  port: 9000

database:
  driver: "postgres"
  dsn: "host=pg.company.com user=pairproxy password=<secret> dbname=pairproxy sslmode=disable"

auth:
  jwt_secret: "your-long-jwt-secret-at-least-32-chars"

cluster:
  # role: 不设置，自动使用 "peer"
  self_addr: "sproxy-1.company.com:9000"   # 节点2 改为 "sproxy-2.company.com:9000"
  self_weight: 50
  shared_secret: "cluster-shared-secret"

llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"

dashboard:
  enabled: true

admin:
  password_hash: "$2a$10$..."
```

**启动**：

```bash
# 节点1
ANTHROPIC_API_KEY=sk-ant-... ./sproxy start --config sproxy-1.yaml

# 节点2（同一 PG，不同 self_addr）
ANTHROPIC_API_KEY=sk-ant-... ./sproxy start --config sproxy-2.yaml
```

**验证**：
```bash
# 在节点1 创建用户
./sproxy admin user add alice --password Password123 --config sproxy-1.yaml

# 在节点2 立即查询（同一 PG，无需等待同步）
./sproxy admin user list --config sproxy-2.yaml
# 应能立即看到 alice

# 在节点2 执行管理操作（不再返回 403 worker_read_only）
./sproxy admin group add engineering --config sproxy-2.yaml
```

### 29.7 与 v2.13.0 Primary/Worker 模式的区别

| 特性 | Primary + Worker（v2.12.0~）| Peer 模式（v2.14.0） |
|------|---------------------------|---------------------|
| 数据库 | SQLite（各节点独立）或 PG（可选）| PostgreSQL（必须共享）|
| 配置同步 | ConfigSyncer 30s 轮询 | 不需要（共享 DB 天然一致）|
| 管理操作 | 仅 Primary | 任意节点 |
| Worker 写封锁 | 是（403）| 否 |
| 节点发现 | Primary 内存注册表 | DB `peers` 表 |
| 适用场景 | 单机或小规模 SQLite 部署 | 多节点高可用 PG 部署 |

### 29.8 向后兼容性

- **现有 `role: "primary"` 配置**：完全不受影响，行为与 v2.13.0 完全一致
- **现有 `role: "worker"` 配置**：完全不受影响
- **现有 PG 部署（未设 role）**：升级到 v2.14.0 后，`role` 自动设为 `"peer"`，行为变化：
  - Worker 写封锁**解除**（管理操作不再返回 403）
  - Reporter 推送**停止**（用量已直接写入共享 DB）
  - 节点发现改用 `peers` 表

  > 如需保持旧 Primary/Worker 行为，显式设置 `cluster.role: "primary"` 或 `"worker"` 即可。

### 29.9 注意事项

- `role: "peer"` 必须配合 `database.driver: "postgres"`，SQLite 下使用 peer 模式会在启动时报错
- Peer 模式不支持 ConfigSyncer，因为所有节点共享同一 DB
- `self_addr` 建议设置为其他节点可路由的地址（主机名或 IP，不含 `http://`）
- 心跳超时（90s）内宕机的节点会被 EvictStale 标记为 inactive，不影响 `ListHealthy` 结果

---

## 30. 已知问题与修复

### 30.1 v2.14.0 SQLite 集群 LLM Target URL 冲突（已修复于 v2.14.1）

**问题描述**：

在 v2.14.0 中，使用 SQLite 数据库的 Worker 节点在启动后，ConfigSyncer 从 Primary 同步 LLM targets 时会报错：

```
UNIQUE constraint failed: llm_targets.url (2067)
```

**影响范围**：

- 仅影响使用 SQLite 数据库的 Primary + Worker 集群模式
- PostgreSQL Peer Mode（§29）不受影响
- 单节点部署不受影响

**根本原因**：

`llm_targets` 表有两个唯一约束：`id`（主键）和 `url`（业务唯一键）。

1. Worker 节点启动时，从 `sproxy.yaml` 同步 targets 到本地 SQLite，生成 Worker 自己的 UUID 作为 `id`（例如 `id=worker-uuid-1, url=https://api.anthropic.com`）
2. Primary 节点启动时，从 `sproxy.yaml` 同步 targets 到本地 SQLite，生成 Primary 自己的 UUID 作为 `id`（例如 `id=primary-uuid-1, url=https://api.anthropic.com`）
3. ConfigSyncer 30s 后从 Primary 拉取快照，尝试 upsert 相同 URL 但不同 ID 的 target
4. v2.14.0 的 upsert 逻辑使用 `ON CONFLICT(id)` 检测冲突：
   - `primary-uuid-1` 在 Worker 本地不存在 → 不触发 UPDATE
   - GORM 走 INSERT 路径 → `url=https://api.anthropic.com` 已存在 → **UNIQUE constraint failed**

**修复方案（v2.14.1）**：

将 ConfigSyncer 中 LLM Targets 的冲突键从 `id` 改为 `url`：

```go
// internal/cluster/config_syncer.go:289
Clauses(clause.OnConflict{
    Columns: []clause.Column{{Name: "url"}},  // ← 改为 url
    DoUpdates: clause.AssignmentColumns([]string{
        "id", "provider", "name", "weight",  // ← id 加入更新列表
        "health_check_path", "model_mapping", "source",
        "is_editable", "is_active", "updated_at",
    }),
})
```

修复后，当 Worker 本地已有相同 `url` 但不同 `id` 的记录时：
- `ON CONFLICT(url)` 命中 → 走 UPDATE 路径
- Worker 本地记录的 `id` 更新为 Primary 的 `id`，其他字段也同步
- 不再触发 UNIQUE constraint 错误

**升级指南**：

1. **无需数据库迁移**：v2.14.1 的修复仅涉及代码逻辑，不改变表结构
2. **升级步骤**：
   ```bash
   # 1. 停止所有 Worker 节点
   systemctl stop sproxy-worker

   # 2. 升级 Worker 节点二进制
   wget https://github.com/l17728/pairproxy/releases/download/v2.14.1/sproxy-linux-amd64
   mv sproxy-linux-amd64 /usr/local/bin/sproxy
   chmod +x /usr/local/bin/sproxy

   # 3. 启动 Worker 节点
   systemctl start sproxy-worker

   # 4. 验证 ConfigSyncer 日志无错误
   journalctl -u sproxy-worker -f | grep "config sync"
   # 应看到：config sync: snapshot applied successfully
   ```

3. **回滚方案**：如需回滚到 v2.14.0，先清空 Worker 本地 SQLite 的 `llm_targets` 表：
   ```bash
   sqlite3 /path/to/pairproxy.db "DELETE FROM llm_targets WHERE source='config';"
   ```

**临时解决方案（v2.14.0 用户）**：

如果无法立即升级到 v2.14.1，可采用以下任一方案：

1. **方案 A：使用 PostgreSQL Peer Mode**（推荐）
   - 参考 §29 配置 PostgreSQL 共享数据库
   - 所有节点共享同一 DB，不存在 ID 冲突问题

2. **方案 B：手动清空 Worker 本地 targets**
   - 每次 Worker 重启前，清空本地 SQLite 的 `llm_targets` 表
   - ConfigSyncer 首次同步时会从 Primary 拉取完整快照

3. **方案 C：禁用 ConfigSyncer**（不推荐）
   - 在 Worker 配置中移除 `cluster.primary_addr`
   - 手动管理 Worker 节点的用户/分组/LLM 配置

**验证修复**：

运行回归测试验证修复有效：

```bash
go test ./internal/cluster/ -run TestConfigSyncer_LLMTargetURLConflictResolution -v
```

该测试模拟 Worker 预先存在相同 URL 但不同 ID 的 target，然后从 Primary 同步快照，验证：
- 无 UNIQUE constraint 错误
- Worker 本地记录的 ID 更新为 Primary 的 ID
- 其他字段正确同步
- 数据库中只有一条记录（无重复插入）

**相关文档**：

- 集群架构设计：`docs/CLUSTER_DESIGN.md` §ConfigSyncer
- 测试报告：`docs/TEST_REPORT.md` v2.14.1 章节
- 验收报告：`docs/ACCEPTANCE_REPORT.md` v2.14.1 章节

---

## 31. HMAC-SHA256 Keygen 算法（v2.15.0）

**版本**：v2.15.0  
**发布日期**：2026-03-18  
**影响范围**：所有使用 Direct Proxy（`sk-pp-` API Key）的用户

### 31.1 升级概述

v2.15.0 将 API Key 生成算法从指纹嵌入升级为 HMAC-SHA256，彻底消除碰撞风险。

**核心变更**：
- 算法：指纹嵌入 → HMAC-SHA256 + Base62 编码
- 确定性：相同用户名+密钥 → 相同 key（可重复生成）
- 无碰撞：密码学保证（碰撞概率 < 2^-143）
- 配置：新增必填字段 `auth.keygen_secret`（≥32 字符）

**破坏性变更**：
- ⚠️ 所有现有 `sk-pp-` key 立即失效
- ⚠️ 用户需重新登录 `/keygen/` 获取新 key
- ⚠️ 配置文件必须添加 `auth.keygen_secret` 字段

### 31.2 配置升级

**1. 生成 keygen_secret**

```bash
# 生成 32 字节随机密钥（推荐）
openssl rand -base64 32

# 或使用 uuidgen（至少 32 字符）
echo "$(uuidgen)$(uuidgen)" | tr -d '-'
```

**2. 更新 sproxy.yaml**

```yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"  # ← 新增必填项
  access_token_ttl: "24h"
  refresh_token_ttl: "168h"
```

**3. 设置环境变量**

```bash
# 方式 A：环境变量注入（推荐）
export KEYGEN_SECRET="your-generated-secret-here"

# 方式 B：直接写入配置文件（不推荐，明文存储）
auth:
  keygen_secret: "your-generated-secret-here"
```

**4. 验证配置**

```bash
./sproxy admin validate --config sproxy.yaml
# 应无错误输出
```

### 31.3 用户迁移流程

**管理员操作**：

1. **升级 sproxy 二进制**
   ```bash
   wget https://github.com/l17728/pairproxy/releases/download/v2.15.0/sproxy-linux-amd64
   mv sproxy-linux-amd64 /usr/local/bin/sproxy
   chmod +x /usr/local/bin/sproxy
   ```

2. **更新配置文件**（参考 §31.2）

3. **重启 sproxy**
   ```bash
   systemctl restart sproxy
   # 或
   ./sproxy start --config sproxy.yaml
   ```

4. **通知用户重新生成 key**
   - 发送邮件/Slack 通知：所有 Direct Proxy 用户需重新登录
   - 提供 keygen 页面链接：`https://your-sproxy-domain/keygen/`

**用户操作**：

1. **访问 keygen 页面**
   ```
   https://your-sproxy-domain/keygen/
   ```

2. **登录并获取新 key**
   - 输入用户名和密码
   - 复制新生成的 `sk-pp-` key

3. **更新环境变量**
   ```bash
   # Claude Code
   export ANTHROPIC_API_KEY="sk-pp-新key"
   export ANTHROPIC_BASE_URL="https://your-sproxy-domain/anthropic"

   # OpenAI 客户端
   export OPENAI_API_KEY="sk-pp-新key"
   export OPENAI_BASE_URL="https://your-sproxy-domain/v1"
   ```

4. **验证新 key**
   ```bash
   # 测试请求
   curl -X POST https://your-sproxy-domain/anthropic/v1/messages \
     -H "x-api-key: sk-pp-新key" \
     -H "Content-Type: application/json" \
     -d '{"model":"claude-3-5-sonnet-20241022","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}'
   ```

### 31.4 技术细节

**算法对比**：

| 特性 | 旧算法（指纹嵌入） | 新算法（HMAC-SHA256） |
|------|-------------------|---------------------|
| 确定性 | ❌ 随机生成 | ✅ 确定性（可重复） |
| 碰撞风险 | ⚠️ 存在（相同字符集） | ✅ 无碰撞（< 2^-143） |
| 验证性能 | O(1) 字符集匹配 | O(n) HMAC 重计算 |
| 缓存优化 | 无 | LRU + TTL（>95% 命中率） |
| 密钥管理 | 无 | 需要 keygen_secret |

**HMAC-SHA256 算法流程**：

```
生成：
1. HMAC-SHA256(secret, username) → 32 字节签名
2. Base62 编码签名 → ~43 字符
3. 填充/截断到 48 字符
4. 拼接前缀 "sk-pp-" → 54 字符 key

验证：
1. 格式检查（前缀 + 长度 + 字符集）
2. 查询 KeyCache（LRU + TTL）
3. 缓存未命中：遍历活跃用户，重新计算 HMAC
4. 比较计算出的 key 与提供的 key
5. 匹配则返回用户信息
```

**安全特性**：

- **密码学强度**：HMAC-SHA256 提供 256 位安全强度
- **碰撞概率**：48 字符 Base62 = 286 位熵，碰撞概率 < 2^-143
- **防暴力破解**：无法从 key 反推 username 或 secret
- **缓存二次校验**：用户禁用后立即失效，不等 TTL 过期

**性能优化**：

- **KeyCache**：LRU 缓存 + TTL，典型命中率 >95%
- **验证延迟**：缓存命中 <1ms，未命中 ~1μs × 用户数
- **扩展性**：1000 用户场景下，验证延迟 <1ms

### 31.5 故障排查

**问题 1：启动失败 "auth.keygen_secret is required"**

```bash
# 原因：配置文件缺少 keygen_secret 字段
# 解决：参考 §31.2 添加配置

# 验证配置
./sproxy admin validate --config sproxy.yaml
```

**问题 2：旧 key 无法使用**

```bash
# 原因：v2.15.0 算法不兼容旧 key
# 解决：用户重新登录 /keygen/ 获取新 key

# 管理员可查看日志确认
journalctl -u sproxy -f | grep "direct auth"
# 应看到：direct auth: no matching user
```

**问题 3：用户报告 "key 每次登录都不同"**

```bash
# 原因：HMAC 算法是确定性的，相同用户名+密钥应生成相同 key
# 可能原因：
# 1. keygen_secret 在重启间被修改
# 2. 用户名大小写不一致

# 验证 keygen_secret 一致性
grep keygen_secret /path/to/sproxy.yaml
echo $KEYGEN_SECRET

# 验证用户名
./sproxy admin user list | grep username
```

**问题 4：验证性能下降**

```bash
# 原因：KeyCache 未启用或 TTL 过短
# 解决：检查 cache 配置（默认已启用）

# 查看缓存命中率（日志）
journalctl -u sproxy | grep "direct auth: cache hit" | wc -l
journalctl -u sproxy | grep "direct auth: key validated" | wc -l
# 命中率 = cache hit / (cache hit + key validated)
```

### 31.6 回滚方案

**不支持回滚到 v2.14.x**：

- HMAC 算法与旧算法完全不兼容
- 回滚会导致所有 v2.15.0 生成的 key 失效
- 建议：充分测试后再升级生产环境

**紧急回滚步骤**（仅限测试环境）：

```bash
# 1. 停止 sproxy
systemctl stop sproxy

# 2. 恢复旧版本二进制
mv /usr/local/bin/sproxy.v2.14.1 /usr/local/bin/sproxy

# 3. 移除 keygen_secret 配置
sed -i '/keygen_secret/d' /path/to/sproxy.yaml

# 4. 启动 sproxy
systemctl start sproxy

# 5. 通知用户重新生成 key（旧算法）
```

### 31.7 相关文档

- **测试报告**：`docs/TEST_REPORT.md` v2.15.0 章节（2469 个测试全部通过）
- **验收报告**：`docs/ACCEPTANCE_REPORT.md` v2.15.0 章节
- **API 文档**：`docs/API.md` §Direct Proxy 章节
- **安全建议**：§13 安全建议（keygen_secret 管理）

---

## 32. 训练语料采集（Corpus）（v2.16.0）

### 32.1 功能概述

Corpus 模块在代理请求的热路径上异步采集 LLM 请求/响应对，输出为 JSONL 格式的训练语料文件，可直接用于模型蒸馏（distillation）或微调（fine-tuning）。

**核心特性**：
- 零阻塞：channel + worker goroutine 异步写入，不影响代理延迟
- 质量过滤：自动过滤错误响应、短回复、指定分组
- 多 Provider：支持 Anthropic / OpenAI / Ollama 三种 SSE 格式解析
- 文件轮转：按日期分目录，按大小自动轮转
- 双模型字段：记录 `model_requested`（客户端请求）和 `model_actual`（LLM 实际返回）

### 32.2 配置

在 `sproxy.yaml` 中添加 `corpus` 段：

```yaml
corpus:
  enabled: true                    # 默认 false
  path: "./corpus/"                # 输出目录
  instance_id: ""                  # 空 = 从监听端口自动推导
  max_file_size: "200MB"           # 单文件上限，触发轮转
  buffer_size: 1000                # channel 容量
  flush_interval: 5s               # 定时 flush 间隔
  min_output_tokens: 50            # 低于此值的响应被过滤
  exclude_groups:                  # 排除的分组（不采集）
    - "test"
    - "debug"
```

### 32.3 输出格式

文件路径：`<path>/<date>/sproxy_<instance>.jsonl`

示例：`./corpus/2026-03-21/sproxy_9000.jsonl`

每行一条 JSON 记录：

```json
{
  "id": "cr_1711036800_a1b2",
  "timestamp": "2026-03-21T16:00:00Z",
  "instance": "9000",
  "user": "alice",
  "group": "engineering",
  "model_requested": "claude-sonnet-4-20250514",
  "model_actual": "claude-sonnet-4-20250514",
  "target": "https://api.anthropic.com",
  "provider": "anthropic",
  "messages": [
    {"role": "user", "content": "What is 2+2?"},
    {"role": "assistant", "content": "The answer is 4."}
  ],
  "input_tokens": 100,
  "output_tokens": 200,
  "duration_ms": 1500
}
```

### 32.4 质量过滤

采集器在提交记录前应用以下过滤规则（按顺序）：

| 过滤条件 | 说明 | 配置项 |
|---------|------|--------|
| HTTP 状态码 ≥ 400 | 错误响应不采集 | 内置，不可关闭 |
| 排除分组 | 指定分组的请求不采集 | `exclude_groups` |
| 输出 token 不足 | 短回复不采集 | `min_output_tokens` |
| 空 assistant 文本 | 无有效输出不采集 | 内置，不可关闭 |

每条被过滤的记录都会输出 DEBUG 日志，包含过滤原因和记录 ID。

### 32.5 文件轮转

- **按日期**：每天 UTC 00:00 自动切换到新目录（`2026-03-21/`、`2026-03-22/`...）
- **按大小**：单文件超过 `max_file_size` 后自动轮转，文件名追加序号（`sproxy_9000_001.jsonl`）
- **优雅关闭**：sproxy 停止时 drain channel 中剩余记录，确保不丢数据

### 32.6 运维

```bash
# 查看今日语料文件
ls -lh ./corpus/$(date -u +%Y-%m-%d)/

# 统计今日采集记录数
wc -l ./corpus/$(date -u +%Y-%m-%d)/*.jsonl

# 验证 JSON 格式
head -1 ./corpus/2026-03-21/sproxy_9000.jsonl | python3 -m json.tool

# 清理 30 天前的语料
find ./corpus/ -type d -mtime +30 -exec rm -rf {} +
```

### 32.7 注意事项

- Corpus 文件包含完整对话内容（用户输入 + LLM 输出），注意数据安全和合规
- channel 满时记录会被丢弃（WARN 日志），可通过增大 `buffer_size` 缓解
- 建议在生产环境中配置 `exclude_groups` 排除测试/调试分组
- 与对话追踪（track）功能独立，两者可同时启用

---

## 33. LLM 故障转移增强：retry_on_status（v2.17.0）

**版本**：v2.17.0

### 33.1 功能概述

`retry_on_status` 让 sproxy 在 LLM 上游返回指定 HTTP 状态码时，自动切换到下一个可用 target，实现请求级故障转移。

**典型场景**：
- 3 个 GLM-4 集群，第一个返回 429（配额耗尽）→ 自动切换到第二个
- 不是降级，而是找到同等能力的可用端点
- 每次请求遍历所有 target 一次，找到可用的即停止

### 33.2 配置

在 `sproxy.yaml` 的 `llm:` 段添加：

```yaml
llm:
  max_retries: 2                 # 最多额外重试几次（不含首次）
  retry_on_status: [429]         # 触发 try-next 的状态码；默认空=关闭
  recovery_delay: 60s

  targets:
    - url: "https://glm-cluster-1.example.com"
      api_key: "${GLM_KEY_1}"
    - url: "https://glm-cluster-2.example.com"
      api_key: "${GLM_KEY_2}"
    - url: "https://glm-cluster-3.example.com"
      api_key: "${GLM_KEY_3}"
```

**参数说明**：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `retry_on_status` | `[]`（空） | 触发 try-next 的额外 HTTP 状态码列表。空=仅对 5xx 和连接错误重试（旧行为） |
| `max_retries` | `2` | 最多额外重试次数（不含首次尝试）。设为 `len(targets)-1` 可遍历全部 |

### 33.3 工作原理

```
请求到达
   ↓
[Target 1] → 429（配额耗尽）
   ↓ 触发 try-next，Target 1 加入 tried 列表
[Target 2] → 429（也耗尽）
   ↓ 触发 try-next，Target 2 加入 tried 列表
[Target 3] → 200（成功）✅
```

- 每个 target 最多尝试一次（tried 列表去重）
- `max_retries` 控制最多切换次数，设为 `N-1` 可遍历全部 N 个 target
- 全部耗尽时返回错误，包含最后一次的状态码

### 33.4 与被动熔断的配合

429 触发 try-next 的同时，`OnFailure` 回调会被调用，触发被动熔断计数。若同一 target 连续多次 429，该 target 会被健康检查器标记为不健康，后续请求直接跳过（双重保护）。

### 33.5 日志示例

```
WARN  llm request failed, retrying with next target
      attempt=1 max_retries=2 failed_target=https://glm-1.example.com
      next_target=https://glm-2.example.com reason="HTTP 429"

WARN  llm request failed, retrying with next target
      attempt=2 max_retries=2 failed_target=https://glm-2.example.com
      next_target=https://glm-3.example.com reason="HTTP 429"
```

全部耗尽时：
```
ERROR all 3 LLM targets exhausted (last target=https://glm-3.example.com, last status=429)
```

### 33.6 向后兼容

`retry_on_status` 默认为空列表，不配置时行为与 v2.16.0 完全一致：
- 只对 5xx 和连接错误重试
- 4xx（含 429）直接返回给客户端

### 33.7 注意事项

- `max_retries` 应设为 `len(targets) - 1`，否则可能在遍历完所有 target 前就停止重试
- 429 同时触发被动熔断，高频 429 会导致 target 被标记为不健康，需配合 `recovery_delay` 使用
- 流式请求（SSE）在建立连接前完成 try-next，切换对客户端透明
- 不建议将 400（Bad Request）加入 `retry_on_status`，换 target 无法解决请求参数问题

---

## 34. 语义路由（Semantic Router）（v2.18.0）

**版本**：v2.18.0

### 34.1 功能概述

语义路由根据请求 messages 的**语义意图**，在现有负载均衡候选池内做二次筛选，将请求路由到最合适的 LLM target。

**典型场景**：
- 代码生成/调试任务 → 路由到高代码能力模型（如 Claude Sonnet、DeepSeek Coder）
- 通用对话/简单 Q&A → 路由到低成本模型（如 Claude Haiku）
- 任何分类失败或超时 → 降级到完整候选池（LB 兜底），保证可用性

**核心设计**：
- 分类器 LLM 调用复用现有 LB（不需要单独配置分类器端点）
- 通过 context 标记防止分类器子请求递归触发语义路由
- 规则来自 YAML 配置 + 数据库（DB 同名规则优先于 YAML）
- 仅对无显式 LLM 绑定的用户生效（绑定用户直接走绑定 target）

### 34.2 配置

在 `sproxy.yaml` 中添加 `semantic_router` 段：

```yaml
semantic_router:
  enabled: true                    # 默认 false，需显式启用
  classifier_timeout: 3s           # 分类器调用超时（默认 3s）
  classifier_model: "claude-haiku-3-5"  # 分类器使用的模型名（默认 claude-haiku-3-5）

  # YAML 默认规则（DB 同名规则会覆盖这些）
  routes:
    - name: code_tasks
      description: "Requests involving code generation, debugging, refactoring, or technical programming"
      target_urls:
        - "https://api.anthropic.com"
        - "https://deepseek-api.example.com"
      priority: 10                 # 数值越大优先级越高

    - name: general_chat
      description: "General conversation, simple Q&A, or creative writing"
      target_urls:
        - "https://haiku-endpoint.example.com"
      priority: 5
```

**参数说明**：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `enabled` | `false` | 是否启用语义路由。false 时完全跳过，无性能影响 |
| `classifier_timeout` | `3s` | 分类器 LLM 调用超时。超时后降级到完整候选池 |
| `classifier_model` | `claude-haiku-3-5` | 分类器使用的模型名。建议用低延迟模型 |
| `routes[].name` | — | 规则唯一名称（DB 同名规则覆盖 YAML 规则） |
| `routes[].description` | — | 自然语言描述，送给分类器 LLM 做意图匹配 |
| `routes[].target_urls` | — | 匹配后缩窄的候选 target URL 列表 |
| `routes[].priority` | `0` | 数值越大越优先（降序排列后送入 prompt） |

### 34.3 工作原理

```
请求到达 → 认证 + 配额检查
   ↓
读取请求 body 中的 messages（最近 5 条）
   ↓
[SemanticRouter] 构建分类 prompt → 调用分类器 LLM
   ├─ 匹配规则 → 缩窄候选池（仅保留该规则的 target_urls）
   ├─ 无匹配 (-1) → 使用完整候选池
   └─ 超时/错误 → 使用完整候选池（降级）
   ↓
在最终候选池内，由 LB 选取 target 转发
```

**关键行为矩阵**：

| 场景 | 行为 |
|------|------|
| 分类器返回有效规则索引 | 使用该规则的 `target_urls` 缩窄候选池 |
| 分类器返回 -1（无匹配） | fallback，使用完整候选池 |
| 分类器调用失败 / 超时 | fallback，使用完整候选池 |
| 分类器子请求（防递归） | 跳过语义路由，直接使用完整候选池 |
| 无激活规则（DB 和 YAML 均为空） | 跳过语义路由 |
| `semantic_router.enabled: false` | 跳过语义路由 |
| 用户有 LLM 绑定（bindingResolver） | 跳过语义路由，直接使用绑定 target |

### 34.4 数据库规则管理（Admin CLI）

```bash
# 新增路由规则
./sproxy admin route add code_tasks \
  --description "Code generation and debugging tasks" \
  --targets "https://api.anthropic.com,https://deepseek.example.com" \
  --priority 10

# 列出所有规则
./sproxy admin route list

# 更新规则
./sproxy admin route update code_tasks \
  --description "Updated description" \
  --targets "https://api.anthropic.com"

# 删除规则
./sproxy admin route delete code_tasks

# 启用/禁用规则
./sproxy admin route enable code_tasks
./sproxy admin route disable code_tasks
```

### 34.5 REST API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/admin/semantic-routes` | 列出所有规则 |
| `POST` | `/api/admin/semantic-routes` | 创建规则 |
| `GET` | `/api/admin/semantic-routes/{id}` | 查询单个规则 |
| `PUT` | `/api/admin/semantic-routes/{id}` | 更新规则 |
| `DELETE` | `/api/admin/semantic-routes/{id}` | 删除规则 |
| `POST` | `/api/admin/semantic-routes/{id}/enable` | 启用规则 |
| `POST` | `/api/admin/semantic-routes/{id}/disable` | 禁用规则 |

**创建规则示例**：
```bash
curl -X POST http://localhost:9000/api/admin/semantic-routes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "code_tasks",
    "description": "Code generation, debugging, and refactoring",
    "target_urls": ["https://api.anthropic.com"],
    "priority": 10
  }'
```

写操作后规则会**自动热更新**到运行中的 SemanticRouter，无需重启服务。

### 34.6 数据库 Schema

```sql
CREATE TABLE semantic_routes (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    target_urls TEXT NOT NULL DEFAULT '[]',  -- JSON array of target URLs
    priority    INTEGER DEFAULT 0,
    is_active   INTEGER DEFAULT 1,
    source      TEXT DEFAULT 'database',     -- "config" | "database"
    created_at  DATETIME,
    updated_at  DATETIME
);
```

规则加载优先级：**数据库规则 > YAML 规则**（同名时 DB 覆盖 YAML）。

### 34.7 日志示例

分类成功：
```
INFO  semantic router: rule matched
      rule_name=code_tasks  rule_id=abc123  candidates=2

INFO  semantic router: candidate pool narrowed
      request_id=req-456  candidates=2
```

分类失败降级：
```
WARN  semantic router: classifier failed, fallback to full pool
      error="classifier http call: context deadline exceeded"
```

跳过语义路由：
```
DEBUG semantic router: skipped, binding resolver active
      request_id=req-789  user_id=user1
```

### 34.8 向后兼容

`semantic_router.enabled` 默认为 `false`，不配置时行为与 v2.17.0 完全一致。已有的用户绑定、负载均衡、provider 路由等逻辑不受影响。

### 34.9 注意事项

- 分类器调用会增加约 0.5~3s 延迟（取决于所用模型和网络），建议使用低延迟模型（如 `claude-haiku-3-5`）
- `classifier_timeout` 应设置为可接受的最大延迟，超时后自动降级
- `target_urls` 中的 URL 必须是已配置在 `llm.targets` 中的有效 target
- 仅对有 `messages` 字段的请求（如 `/v1/messages`、`/v1/chat/completions`）生效
- 分类器 prompt 仅使用对话的**最近 5 条消息**（`maxPromptMessages=5`），避免 prompt 过长；超长消息内容会被截断到 500 字符
- 分类器子请求通过 context 标记防递归，不会无限循环
- 数据库规则通过 REST API 或 CLI 修改后立即热更新，无需重启

---

## 35. LLM 目标运行时同步（v2.19.0）

**版本**：v2.19.0

### 35.1 问题背景

v2.19.0 之前，通过 WebUI 或 API 添加新 LLM target 后，运行中的 `llmBalancer` 和 `llmHC` 无法感知变更，导致新 target 永远显示 Healthy=false，无法被路由。

### 35.2 解决方案

每次通过 WebUI/API 执行 Create/Update/Delete/Enable/Disable 操作后，系统自动调用 `SyncLLMTargets()`，在同一请求内同步更新 `llmBalancer` 和 `llmHC`，无需重启。

### 35.3 新 target 入场策略

| 情况 | 初始状态 | 何时可路由 |
|------|---------|-----------|
| 有 `HealthCheckPath` | Healthy=false | 立即触发 `CheckTarget`，通过后秒级可用 |
| 无 `HealthCheckPath` | Healthy=true | 立即可路由，依赖被动熔断 |

有 `HealthCheckPath` 的新节点不会等待 30s 定时 ticker，而是在 Sync 时立即触发一次主动检查，避免坏节点消耗真实用户请求。

### 35.4 存量节点状态保留

Sync 操作不会重置已存在节点的运行时状态：

- 已熔断节点（Healthy=false）：Sync 后保留熔断状态
- 排水中节点（Draining=true）：Sync 后保留排水标志，不接受新流量
- 失败计数：Sync 后保留，再失败一次即可触发熔断

### 35.5 向后兼容

v2.19.0 对外部接口无变更，所有 WebUI/API 操作行为不变，仅内部增加了运行时同步逻辑。

---

**手册结束**

## 36. Group-Target Set v2.20.0

**版本**：v2.20.0

### 36.1 功能概述

Group-Target Set 是 PairProxy 的分组级 LLM 目标管理功能。管理员可为不同用户分组配置独立的 LLM 目标池，每个 Target Set 包含多个上游 LLM 目标，支持加权随机、轮询、优先级三种路由策略，与告警管理和健康监控深度集成。

### 36.2 核心概念

- GroupTargetSet：与某个 Group 绑定的目标集合，含路由策略
- GroupTargetSetMember：Target Set 内的单个 LLM 目标，含权重、优先级、健康状态
- 默认组：group_id=NULL 且 is_default=true，未绑定分组的请求使用此池
- Strategy：weighted_random（默认）、round_robin、priority
- RetryPolicy：try_next（默认）、fail_fast

### 36.3 数据模型

group_target_sets 表：id、group_id（NULL=默认组）、name（唯一）、strategy、retry_policy、is_default、created_at、updated_at。

group_target_set_members 表：id、target_set_id、target_url、weight（默认1）、priority（默认0）、is_active（默认true）、health_status（healthy/unhealthy/unknown）、consecutive_failures、created_at。

重要：is_active 字段使用原生 SQL INSERT 写入，绕过 GORM 零值替换（Bug 7 修复）。

### 36.4 API 接口

- GET/POST /api/admin/target-sets：列出/创建
- GET/PUT/DELETE /api/admin/target-sets/:id：查询/更新/删除
- GET/POST /api/admin/target-sets/:id/members：列出/添加 member
- PUT/DELETE /api/admin/target-sets/:id/members/:url：更新/移除 member

### 36.5 路由优先级

用户级绑定 → 分组级绑定 → Group-Target Set → 默认 Target Set → 全局负载均衡

### 36.6 Bug 7：GORM 零值陷阱

GORM Create 将 bool 零值 false 视为未设置，应用 default:true 标签，导致 IsActive=false 被静默写为 true。修复：AddMember 改用原生 SQL INSERT。凡含 gorm default:true 的 bool 字段，若需 Create 时写入 false，必须使用原生 SQL。

### 36.7 向后兼容

未配置任何 Target Set 时，系统退回全局 llmBalancer 路由，行为与 v2.19.0 完全一致。

---

## 37. 告警管理 Alert Manager v2.20.0

**版本**：v2.20.0

### 37.1 功能概述

Target Alert Manager 监控 Group-Target Set 各 LLM 目标，连续错误达阈值时创建告警，连续成功达恢复阈值时自动解除，通过 SSE 实时推送事件到 Dashboard。

### 37.2 告警生命周期

正常 → 连续错误 >= min_occurrences → alert_created → 连续成功 >= consecutive_successes → alert_resolved

### 37.3 数据模型

target_alerts 表：id、target_url、alert_type（error/health_check_failed）、severity（warning/error/critical）、status_code、error_message、affected_groups（JSON）、occurrence_count、last_occurrence、resolved_at、created_at。

### 37.4 配置

alert.enabled=true 启用，triggers.http_error.min_occurrences 设置触发阈值（默认3），recovery.consecutive_successes 设置恢复阈值（默认2）。

### 37.5 SSE 事件推送

订阅：GET /api/admin/alerts/stream。事件类型：alert_created（新告警）、alert_resolved（恢复）、target_health_changed（健康变化）。

### 37.6 disabled 模式安全性

alert.enabled=false 时，Start() 通过 sync.Once 安全关闭 done 通道，Stop() 不会阻塞。

### 37.7 向后兼容

alert.enabled 默认 false，不配置时行为与 v2.19.0 完全一致。

---

## 38. 目标健康监控 Target Health Monitor v2.20.0

**版本**：v2.20.0

### 38.1 功能概述

Target Health Monitor 对 Group-Target Set 所有 IsActive=true 的成员执行周期性主动 HTTP 健康检查，根据连续失败/成功次数更新 health_status，并触发 Alert Manager 事件。

### 38.2 检查流程

定时器（默认30s）→ ListAll() → ListMembers(IsActive=true) → 并发 HTTP GET → 成功达阈值 UpdateTargetHealth(true)；失败达阈值 UpdateTargetHealth(false) 并 RecordHealthCheckFail()。

### 38.3 配置

health_monitor.interval=30s，timeout=5s，failure_threshold=3，success_threshold=2，path=/health。

### 38.4 健康状态值

unknown（初始）、healthy（检查通过）、unhealthy（连续失败达阈值）。

### 38.5 注意事项

仅覆盖 Group-Target Set 成员，不覆盖 llm.targets 全局 target（后者由 internal/lb 负责）。检查路径默认 /health，可通过 health_monitor.path 调整。

### 38.6 向后兼容

未配置 health_monitor 时使用默认值，行为与 v2.19.0 完全一致。
---

## 39. WebUI Phase 1：分组目标集管理（v2.22.0）

**版本**：v2.22.0

**概述**：将后端的 Group-Target Set 功能完整暴露到 Dashboard WebUI，支持管理员直观地创建/编辑/删除目标集及其成员，取代命令行操作。

### 39.1 用户界面

LLM 页面新增 **"Target Sets"** 选项卡，采用**双栏式布局**：

**左栏 — 目标集列表**
- 按分组显示所有 Target Set
- 快速操作：编辑、删除按钮
- 支持搜索/过滤

**右栏 — 详情与成员管理**
- 目标集名称、ID、路由策略（weighted_random/round_robin/priority）
- 绑定分组显示
- 成员列表及其 URL、权重、优先级、健康状态
- 成员快速操作：修改、删除

### 39.2 API 端点

| 方法 | 端点 | 说明 |
|------|------|------|
| POST | `/dashboard/llm/targetsets` | 创建新目标集 |
| POST | `/dashboard/llm/targetsets/{id}/update` | 编辑目标集（名称、策略） |
| POST | `/dashboard/llm/targetsets/{id}/delete` | 删除目标集 |
| POST | `/dashboard/llm/targetsets/{id}/members` | 添加成员到目标集 |
| POST | `/dashboard/llm/targetsets/{id}/members/update` | 修改成员配置（权重、优先级） |
| POST | `/dashboard/llm/targetsets/{id}/members/delete` | 删除成员 |

### 39.3 操作流程

#### 创建目标集

1. 点击"新建 Target Set"
2. 填写：分组、集合名称、路由策略
3. 系统校验 ID 格式（`^[a-zA-Z0-9_-]+$`）
4. 提交后跳转到成员管理界面

#### 添加成员

1. 在"目标集详情"右栏点击"添加成员"
2. 输入上游 LLM 目标 URL、权重（默认1）、优先级（默认0）
3. 确认后自动刷新成员列表

#### 修改成员

1. 点击成员的"编辑"按钮，修改权重/优先级
2. 更新即刻生效，无需重启 sproxy

#### 删除成员

1. 点击"删除"按钮，确认无误
2. 删除后立即从路由池中移除

### 39.4 关键设计

**错误消息编码**：所有错误通过 `url.QueryEscape()` 编码再跳转，防止 URL 注入。

**审计日志**：每次操作（创建、编辑、删除）都记录到 audit_logs 表，包含操作人、时间、操作类型、目标资源 ID。

**只读保护**：Worker 节点 Dashboard 标签显示"只读模式"，禁止所有写操作（返回 403）。

**模态对话框**：成员编辑/删除操作通过 CSS 隐藏/显示的 HTML 模态框实现，使用 `data-*` 属性预填充表单字段。

### 39.5 配置示例

```yaml
# 无需额外配置，Phase 1 UI 自动启用（依赖于现有 Group-Target Set 数据库）
```

### 39.6 使用场景

- **蓝绿部署**：为不同用户组配置不同 LLM 目标池，逐步灰度切换
- **成本控制**：为成本敏感的团队绑定便宜的 LLM（如开源模型），为关键业务绑定高性能模型
- **容灾演练**：快速禁用故障 Target（is_active=false），自动从路由池移除

---

## 40. WebUI Phase 2：告警管理增强（v2.22.0）

**版本**：v2.22.0

**概述**：将后端的 Alert Manager 完整暴露到 Dashboard，全新设计 3 标签告警页面，支持实时流、活跃告警统计、历史查询、批量操作。

### 40.1 告警页面架构

Dashboard 告警页面采用**三标签设计**：

#### Live Tab（实时流）

- **功能**：实时流式推送告警事件（SSE 连接 `/api/admin/alerts/stream`）
- **过滤器**：All（全部）/ Error（错误）/ Warning（警告）
- **交互**：无需手动刷新，新事件自动追加到列表顶部
- **卸载**：用户切换标签时自动断开 SSE 连接

#### Active Tab（活跃告警）

- **功能**：当前未解除的告警概览
- **统计卡片**：
  - Critical 数量（红卡）
  - Error 数量（橙卡）
  - Warning 数量（黄卡）
- **批量操作**：
  - Checkbox 多选告警
  - 一键解除按钮（POST `/dashboard/alerts/resolve-batch`）
  - 视觉反馈：操作后卡片数值实时更新

#### History Tab（历史查询）

- **时间范围选择器**：7天 / 30天 / 90天（或自定义日期范围）
- **过滤条件**：
  - Level（All/Critical/Error/Warning）
  - Source（告警来源，如 "http_error" / "health_check_failed"）
- **分页**：50 条/页，Previous / Next 按钮
- **查询结果**：告警 ID、目标 URL、类型、发生时间、解除时间

### 40.2 数据源与 API

| API | 用途 | 说明 |
|-----|------|------|
| GET `/api/admin/alerts/stream` | Live 标签 | Server-Sent Events 实时推送 |
| GET `/dashboard/api/alerts/active` | Active 标签 | 返回 `{critical, error, warning}` 统计 |
| GET `/dashboard/api/alerts/history?since=...&until=...&level=...&source=...&offset=...&limit=50` | History 标签 | 分页查询历史告警 |
| POST `/dashboard/alerts/resolve` | 单个解除 | 解除指定 ID 告警 |
| POST `/dashboard/alerts/resolve-batch` | 批量解除 | 解除多个告警，请求体 `{"ids": [...]}` |

### 40.3 事件流（Live Tab）

SSE 连接推送的事件类型：

```json
{
  "event": "alert_created",
  "data": {
    "id": "alert-123",
    "target_url": "https://api.example.com/v1/messages",
    "severity": "error",
    "type": "http_error",
    "message": "Connection timeout",
    "timestamp": "2026-03-28T10:30:00Z"
  }
}
```

实时流支持**客户端过滤**：JavaScript 根据选中的 Level（All/Error/Warning）动态渲染事件，无需重连。

### 40.4 使用场景

- **值班告警**：开发者登录 Dashboard，Live 标签持续推送告警；Active 标签一览统计；一旦发现告警可直接批量标记为已解除
- **事后分析**：History 标签按时间范围、级别、来源查询过去 90 天告警，追踪故障根因
- **SLA 监控**：通过告警数量趋势了解系统健康度

### 40.5 配置

```yaml
alert:
  enabled: true                    # 启用告警管理（默认 false）
  triggers:
    http_error:
      min_occurrences: 3          # 连续错误 3 次触发告警
  recovery:
    consecutive_successes: 2      # 连续成功 2 次自动解除
```

---

## 41. WebUI Phase 3：快速操作面板（v2.22.0）

**版本**：v2.22.0

**概述**：Dashboard 主页新增"快速操作"摘要卡片区域，一眼洞察 LLM 健康、系统告警、用户/分组指标，支持异步加载与优雅降级。

### 41.1 快速操作面板布局

Dashboard 首页（Overview）新增 **Quick Operations** 区域，包含 **3 张卡片**：

#### 卡片 1：LLM 目标状态

```
┌─────────────────────────────────────┐
│  🚀 LLM 目标状态                     │
├─────────────────────────────────────┤
│  健康目标：     42 个                │
│  告警目标：     3 个                 │
│  目标集总数：   8 个                 │
│                                     │
│            查看详情 →               │
└─────────────────────────────────────┘
```

**数据来源**：
- 健康目标 = GroupTargetSetMember 中 health_status='healthy' 的数量
- 告警目标 = 有未解除告警（status='active'）的目标数量
- 目标集数量 = GroupTargetSet 总数

#### 卡片 2：系统告警

```
┌─────────────────────────────────────┐
│  ⚠️  系统告警                        │
├─────────────────────────────────────┤
│  未解除告警：   5 个     🔴 严重    │
│  ├─ Critical： 1 个                 │
│  ├─ Error：    2 个                 │
│  └─ Warning：  2 个                 │
│                                     │
│            查看详情 →               │
└─────────────────────────────────────┘
```

**数据来源**：
- 未解除告警 = TargetAlert 中 status='active' 的数量
- 按 severity 分类统计（critical, error, warning）
- 状态徽章：Critical≥1 时红色，Error≥1 时橙色，否则绿色

#### 卡片 3：用户/分组

```
┌─────────────────────────────────────┐
│  👥 用户/分组                       │
├─────────────────────────────────────┤
│  活跃用户：     127 个              │
│  用户总数：     150 个              │
│  分组总数：     12 个               │
│  新增用户（本周）：  8 个           │
│                                     │
│            查看详情 →               │
└─────────────────────────────────────┘
```

**数据来源**：
- 活跃用户 = User 中 is_active=true 的数量
- 用户总数 = User 总数
- 分组总数 = Group 总数
- 新增用户 = 本周创建的 User 数量（created_at >= 7 days ago）

### 41.2 异步加载机制

**页面加载流程**：

1. Dashboard 首页初始加载（HTML 渲染，无 Quick Operations 卡片内容）
2. 页面 onload 后，JavaScript 异步并发调用 3 个 API：
   - `GET /dashboard/api/quick-ops/llm-status`
   - `GET /dashboard/api/quick-ops/alerts`
   - `GET /dashboard/api/quick-ops/users`
3. 各 API 返回后，动态填充对应卡片内容
4. 若某 API 超时（3s）或错误，卡片显示"未配置"或"——"

**优点**：
- 非阻塞：Dashboard 主页快速加载，不依赖后端查询完成
- 容错：单个 API 失败不影响整页展示
- 实时：每次访问都刷新最新数据

### 41.3 API 端点

| 端点 | 返回数据 |
|------|---------|
| `GET /dashboard/api/quick-ops/llm-status` | `{healthy_count, alert_count, total_sets}` |
| `GET /dashboard/api/quick-ops/alerts` | `{total_active, critical, error, warning, status}` |
| `GET /dashboard/api/quick-ops/users` | `{active_users, total_users, total_groups, new_users_week}` |

### 41.4 HTML 实现

```html
<!-- overview.html 片段 -->
<section class="quick-operations">
  <!-- 卡片 1 -->
  <div class="card llm-status" id="llm-card">
    <h3>🚀 LLM 目标状态</h3>
    <div id="llm-content">加载中...</div>
  </div>

  <!-- 卡片 2 -->
  <div class="card alerts-status" id="alerts-card">
    <h3>⚠️ 系统告警</h3>
    <div id="alerts-content">加载中...</div>
  </div>

  <!-- 卡片 3 -->
  <div class="card users-status" id="users-card">
    <h3>👥 用户/分组</h3>
    <div id="users-content">加载中...</div>
  </div>
</section>

<script>
// 异步加载函数
async function loadQuickOps() {
  try {
    const llm = await fetch('/dashboard/api/quick-ops/llm-status').then(r => r.json());
    document.getElementById('llm-content').innerHTML = `
      健康目标：${llm.healthy_count} 个<br>
      告警目标：${llm.alert_count} 个<br>
      目标集总数：${llm.total_sets} 个
    `;
  } catch (e) {
    document.getElementById('llm-content').innerHTML = '加载失败';
  }
  // 类似处理 alerts 和 users...
}

window.addEventListener('load', loadQuickOps);
</script>
```

### 41.5 优雅降级

若尚未配置 Group-Target Set 或告警功能，卡片显示：

```
LLM 目标状态
├─ 健康目标：— 个
├─ 告警目标：— 个
└─ 目标集总数：未配置
```

用户体验无损，不会报错。

### 41.6 使用场景

- **晨会看板**：每天早上打开 Dashboard，快速了解系统健康度
- **值班接班**：入班首先查看 Quick Operations，了解昨晚是否有告警
- **性能监控**：看用户增长趋势，评估系统容量
- **故障应急**：告警卡片显示 Critical≥1 时，直接跳转到告警页面处理

---

## 42. v2.23.0 更新说明

**版本**：v2.23.0
**发布日期**：2026-04-04

本版本修复了三个 GitHub Issue，提升了 API Key 号池共享能力、健康检查覆盖范围和文档准确性。

---

### 42.1 Issue #2：API Key 号池共享修复

#### 问题描述

在 v2.22.0 及之前版本，同一 `provider` 类型（如 `openai`）只能在数据库中保存一个 API Key 记录。

这导致以下场景失效：
- 阿里云百炼 + 火山引擎 Ark 都是 `openai` 兼容协议，配置两个 target 时，后者的 Key 会覆盖前者
- 多个不同的 OpenAI 账号（不同 `api_key`）无法同时存在于号池中

#### 修复方案

UNIQUE 约束从 `(provider)` 改为 `(provider, encrypted_value)`：

```
之前：同一 provider 只能有 1 个 Key
  openai → sk-openai-key-A       ← 唯一

之后：同一 provider 可有多个不同 Key
  openai → sk-openai-key-A       ✅
  openai → sk-dashscope-key-B    ✅（不同 Key，独立存储）
  openai → sk-ark-key-C          ✅
```

#### 验证方式

```bash
# 配置多个 openai 兼容 target
llm:
  targets:
    - url: "https://dashscope.aliyuncs.com/compatible-mode/v1"
      api_key: "${DASHSCOPE_KEY}"
      provider: "openai"
    - url: "https://ark.cn-beijing.volces.com/api/v3"
      api_key: "${ARK_KEY}"
      provider: "openai"

# 启动后验证每个 Key 独立存在
sproxy admin apikey list
# 期望：看到 2 条记录，Key 不互相覆盖
```

---

### 42.2 Issue #3：`admin.key_encryption_key` 配置说明修正

#### 问题描述

文档将 `admin.key_encryption_key` 标注为"可选"，但实际上使用 `sproxy admin apikey` 系列命令时该字段是**必填的**。

#### 修正后的配置说明

```yaml
admin:
  password: "${ADMIN_PASSWORD}"       # Dashboard 登录密码（必填）
  key_encryption_key: "${KEY_ENC}"    # API Key 加密密钥（使用 admin apikey 命令时必填，≥32 字符）
```

| 字段 | 类型 | 必填条件 |
|------|------|---------|
| `admin.password` | string | 始终必填 |
| `admin.key_encryption_key` | string | 使用 `admin apikey add/list/delete` 时必填；不使用号池功能可省略 |

#### 错误提示改进

未配置时，系统现在会给出更清晰的错误信息：

```
Error: admin.key_encryption_key is not configured.
This field is required when using the API key management feature (admin apikey commands).
Please add it to your configuration: admin.key_encryption_key: "<32+ char secret>"
```

---

### 42.3 Issue #4：健康检查支持大厂 API 认证

#### 问题描述

Anthropic、OpenAI 等主流 LLM 提供商没有公开的 `/health` 端点。之前版本中，即使配置了健康检查路径（如 `/v1/models`），请求因缺少认证头而返回 401，导致 target 被错误标记为不健康。

#### 解决方案（v2.23.0）：认证注入

健康检查自动注入 provider 对应的认证头：

| Provider | 注入的请求头 |
|----------|-------------|
| `anthropic` | `x-api-key: <key>` + `anthropic-version: 2023-06-01` |
| `openai` / 其他 | `Authorization: Bearer <key>` |
| 无 provider / 本地 | 无（向后兼容） |

v2.23.0 修复后，手动配置 `health_check_path: "/v1/models"` 可使探活正常工作。

#### 进一步增强（v2.24.5）：智能探活完全解决

v2.24.5 根本性解决了此问题：即使不配置任何 `health_check_path`，系统也会自动发现并缓存最优探测路径（详见 §15.4）。

**关键修复**：发现阶段与心跳阶段语义分离——发现阶段 401/403 = "端点存在"，心跳阶段 401 = "key 无效"。华为云、小米等在 key 无效时仅返回 401 的服务，现已能被正确发现并持续探活。

#### 推荐配置（v2.24.5+）

```yaml
llm:
  targets:
    # 推荐：不配置 health_check_path，智能探活自动处理
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"

    # 火山引擎、腾讯、华为云、小米等 — 同样不需要配置路径
    - url: "https://ark.cn-beijing.volces.com/api/v3"
      api_key: "${ARK_KEY}"
      provider: "openai"

    # vLLM / sglang / Ollama — 智能探活发现 GET /health
    - url: "http://localhost:11434"
      api_key: "ollama"
```

#### 可观测性

启用 DEBUG 日志查看探活过程：

```bash
# 查看智能探活日志
journalctl -u sproxy -f | grep -E "probe:|smart probe:"

# 示例输出（首次探活）
INFO  health_checker  smart probe: discovering health check method  {"target": "anthropic-api", "addr": "https://api.anthropic.com"}
INFO  health_checker  probe: discovered working health check method  {"target": "anthropic-api", "method": "GET /v1/models", "status": 401}
DEBUG health_checker  smart probe: initial health check failed after discovery  {"target": "anthropic-api", "status": 401}

# 示例输出（key 有效时）
INFO  health_checker  probe: discovered working health check method  {"target": "openai-api", "method": "GET /v1/models", "status": 200}
DEBUG health_checker  smart probe: initial health check ok after discovery  {"target": "openai-api"}
```

---

### 42.4 升级步骤

1. **备份数据库**
   ```bash
   cp pairproxy.db pairproxy.db.bak.v2.22.0
   ```

2. **停止 sproxy**
   ```bash
   systemctl stop sproxy
   ```

3. **替换二进制**
   ```bash
   cp sproxy-v2.23.0 /usr/local/bin/sproxy
   ```

4. **（可选）更新配置**：为 LLM target 添加 `health_check_path: "/v1/models"` 和正确的 `provider` 字段

5. **启动 sproxy**（Schema 自动迁移）
   ```bash
   systemctl start sproxy
   ```

6. **验证**
   ```bash
   curl http://localhost:9000/health
   # 查看日志确认健康检查正常
   journalctl -u sproxy -f | grep -i health
   ```

---

### 42.5 不兼容变更

**无破坏性变更**。所有修改均向后兼容：

- v2.22.0 配置文件直接可用，无需修改
- 现有 API Key 数据库记录不受影响
- 未设置 `health_check_path` 的 target 健康检查行为不变

---

## 43. v2.24.0 更新说明

**版本**：v2.24.0
**类型**：功能版本（Model-Aware Routing）

### 43.1 新功能：Model-Aware Routing（模型感知路由）

v2.24.0 引入 **Model-Aware Routing** 功能，让网关能够根据请求中的模型名称智能路由到支持该模型的目标。

#### F1 — Config-as-Seed（配置即种子）

配置文件中的 LLM target 定义现在作为**初始种子**写入数据库：

- 首次启动时，配置中的 target 自动同步到数据库
- 若 target URL 已存在（通过 WebUI 添加或手动修改），**跳过**，不覆盖 WebUI 的修改
- 配置文件的作用是"提供初始值"，运行时由 WebUI 管理

```yaml
# sproxy.yaml
llm:
  targets:
    - url: "https://api.anthropic.com"
      provider: "anthropic"
      supported_models:
        - "claude-3-*"
        - "claude-2.1"
      auto_model: "claude-3-sonnet-20250219"
```

#### F2 — Per-Target Supported Models（按目标过滤模型）

每个 LLM target 可以声明自己支持哪些模型，网关在路由时自动过滤：

**supported_models 模式匹配规则**：

| 模式 | 含义 | 示例命中 |
|------|------|---------|
| `claude-3-sonnet-20250219` | 精确匹配 | `claude-3-sonnet-20250219` |
| `claude-3-*` | 前缀通配 | `claude-3-sonnet-*`、`claude-3-opus-*` |
| `*` | 全通配 | 任意模型 |
| `[]`（空） | 同 `*`，不限制 | 任意模型 |

**Fail-Open 策略**：当请求的模型不在任何 target 的 `supported_models` 中时，网关**不拒绝请求**，而是回退到所有健康 target，允许 LLM 自行决定是否接受。

路由决策流程：
```
请求模型: "claude-3-sonnet-20250219"
  │
  ├─ Level 1: Provider 过滤（anthropic / openai / ollama）
  │   └─ 过滤后无候选 → Fail-Open，使用全部健康 target
  │
  └─ Level 2: Model 过滤（supported_models 匹配）
      └─ 过滤后无候选 → Fail-Open，使用 Level 1 的候选集
```

#### F3 — Auto Mode（自动模型选择）

客户端发送 `"model": "auto"` 时，网关自动为每个目标选择合适的模型：

```json
// 客户端发送
{"model": "auto", "messages": [...]}

// 网关转发到 Anthropic target（改写 model 字段）
{"model": "claude-3-sonnet-20250219", "messages": [...]}

// 网关转发到 OpenAI target（改写 model 字段）
{"model": "gpt-4-turbo", "messages": [...]}
```

**auto_model 降级策略**：
1. 优先使用目标的 `auto_model` 字段
2. 若未配置，使用 `supported_models[0]`（第一个）
3. 若都为空，透传原始 `"auto"` 字符串

### 43.2 配置参考

#### sproxy.yaml 完整示例

```yaml
llm:
  targets:
    # Anthropic 集群
    - url: "https://api.anthropic.com/v1"
      name: "Anthropic Primary"
      provider: "anthropic"
      weight: 10
      supported_models:
        - "claude-3-opus-20250119"
        - "claude-3-sonnet-20250219"
        - "claude-3-haiku-*"
        - "claude-2.1"
      auto_model: "claude-3-sonnet-20250219"

    # OpenAI 集群
    - url: "https://api.openai.com/v1"
      name: "OpenAI Primary"
      provider: "openai"
      weight: 8
      supported_models:
        - "gpt-4-turbo"
        - "gpt-4-*"
        - "gpt-3.5-turbo"
      auto_model: "gpt-4-turbo"

    # 本地 Ollama（接受所有模型）
    - url: "http://localhost:11434"
      name: "Local Ollama"
      provider: "ollama"
      weight: 2
      supported_models: []   # 空 = 接受所有模型
      auto_model: "mistral"
```

#### API（动态管理）

```bash
# 创建 target（含模型过滤配置）
curl -X POST http://localhost:9000/api/admin/llm/targets \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://api.anthropic.com",
    "provider": "anthropic",
    "api_key_id": "key-abc",
    "supported_models": ["claude-3-*", "claude-2.1"],
    "auto_model": "claude-3-sonnet-20250219"
  }'

# 更新 target 模型配置
curl -X PUT http://localhost:9000/api/admin/llm/targets/{id} \
  -H "Content-Type: application/json" \
  -d '{
    "supported_models": ["claude-3-*"],
    "auto_model": "claude-3-opus-20250119"
  }'
```

#### CLI（管理命令）

```bash
# 添加 target（含模型过滤）
sproxy admin llm target add \
  --url https://api.anthropic.com \
  --provider anthropic \
  --api-key-id key-abc \
  --name "Anthropic Main" \
  --weight 10 \
  --supported-models "claude-3-*,claude-2.1" \
  --auto-model "claude-3-sonnet-20250219"

# 更新 target 模型配置
sproxy admin llm target update https://api.anthropic.com \
  --supported-models "claude-3-*" \
  --auto-model "claude-3-opus-20250119"
```

### 43.3 诊断与故障排查

**问题：请求到达了不支持该模型的 target**

日志中会出现：
```
WARN model-aware routing: no target supports requested model, falling back to provider-filtered candidates
     requested_model=llama3
     diagnosis="none of the available targets are configured to support 'llama3'"
     recovery_suggestion="add 'llama3' to supported_models or reconsider model choice"
```

解决方法：将模型添加到对应 target 的 `supported_models`，或使用空列表接受所有模型。

**问题：auto 模式没有改写模型**

日志中会出现：
```
WARN auto mode: target has no auto_model configured
     target=http://localhost:11434
     fallback=pass-through
```

解决方法：为该 target 设置 `auto_model` 字段。

**完整配置指南**：`.sisyphus/CONFIGURATION_GUIDE.md`（552 行）
**日志规范**：`.sisyphus/LOGGING_SPECIFICATION.md`

### 43.4 数据库变更

新增两列到 `llm_targets` 表（Schema 自动迁移，启动时自动执行）：

| 列名 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `supported_models` | TEXT | `'[]'` | JSON 数组，目标支持的模型列表 |
| `auto_model` | VARCHAR | `''` | auto 模式下使用的默认模型 |

### 43.5 升级指南

1. 替换二进制
   ```bash
   ./sproxy version  # 应输出：sproxy v2.24.0
   ```

2. **（可选）** 在 `sproxy.yaml` 中为各 target 添加 `supported_models` 和 `auto_model` 字段

3. 重启 sproxy（Schema 自动迁移，无需手动执行 SQL）

4. 验证：查看启动日志确认 `"seeding target from config"` 和 `"database migrations completed"`

### 43.6 不兼容变更

**无破坏性变更**。向后完全兼容：

- 未配置 `supported_models` 的 target 行为不变（接受所有请求）
- 未配置 `auto_model` 时，`"auto"` 模式透传（保持原行为）
- 现有配置文件无需修改即可升级

## 44. v2.24.3 更新说明

**版本**：v2.24.3  
**发布日期**：2026-04-07

### 概述

v2.24.3 实现 **Issue #6：多 API Key 共用同一 URL**，是 v2.7.0（动态 LLM Target 管理）以来最重要的功能升级。允许在一个端点 URL 配置多个不同的 API Key，每个 Key 为一个独立的 target，实现多号池共存和负载均衡。

**核心能力**
- ✅ 同一 URL、不同 API Key → 两个独立 target（号池拆分）
- ✅ 全套路由兼容：UUID-as-ID 精确匹配，避免 URL 碰撞
- ✅ 全数据库隔离：(url, api_key_id) 复合唯一约束
- ✅ Cleanup 精确性：删除指定 (url, key) 对，保留其他
- ✅ 向后兼容：默认一 URL 一 Key，多 Key 是可选
- ✅ 生产稳定性：380+ 测试覆盖

### 使用场景

**场景 1：多账户号池**
```yaml
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_KEY_ACCOUNT_A}"  # 账户 A
      name: "Anthropic Account A"
      weight: 2
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_KEY_ACCOUNT_B}"  # 账户 B（同 URL）
      name: "Anthropic Account B"
      weight: 1
```

结果：两个 target 均指向同一 URL，但用不同 API Key，轮流执行请求。

**场景 2：OpenAI 兼容商线路聚合**
```yaml
llm:
  targets:
    # 国内线路
    - url: "https://dashscope.aliyuncs.com/api"
      api_key: "${BAILIAN_KEY}"
      provider: "openai"
      name: "Alibaba Bailian"
    # 国外线路（同 URL 也可，不同 provider）
    - url: "https://api.openai.com"
      api_key: "${OPENAI_ACCOUNT_1}"
      provider: "openai"
      name: "OpenAI Account 1"
    - url: "https://api.openai.com"
      api_key: "${OPENAI_ACCOUNT_2}"
      provider: "openai"
      name: "OpenAI Account 2"
```

### 内部实现

#### DB Schema 变更

`llm_targets` 表：
- **新增** `api_key_id` 列（FK → `api_keys.id`）
- **变更** 唯一约束从 `UNIQUE(url)` → `UNIQUE(url, api_key_id)`

示意：
```
| id (PK) | url                     | api_key_id      | source  |
|---------|-------------------------|-----------------|---------|
| uuid-1  | https://api.anthropic   | key-account-a   | config  |
| uuid-2  | https://api.anthropic   | key-account-b   | config  |  ← 同 URL，不同 Key
| uuid-3  | https://api.openai.com  | key-openai-1    | config  |
```

#### API & CLI

同 v2.7.0+，支持 POST `/api/admin/llm/targets` 和 `sproxy admin llm target add`，自动处理 API Key 创建与 `api_key_id` 关联。

```bash
# 添加第二个 Key 到同一 URL
sproxy admin llm target add \
  --url "https://api.anthropic.com" \
  --api-key "sk-ant-key-account-B" \
  --name "Anthropic Account B" \
  --weight 1
```

#### 配置同步（Config → DB）

`syncConfigTargetsToDatabase()` 流程：
1. 逐条遍历 config 中的 target
2. 调用 `resolveAPIKeyID()` 创建/查找 APIKey 记录
3. 调用 `Upsert()` 按 **(url, api_key_id)** 复合键查重
   - 存在 → 更新其他字段
   - 不存在 → 创建新 target（新 UUID）
4. 清理：`DeleteConfigTargetsNotInList()` 按 **(url, api_key_id)** 精确删除不在新配置中的条目

**关键点**：即使两个 target URL 相同，因为 `api_key_id` 不同，也会产生两条独立记录。

#### 运行时路由

`lb.Target.ID` = `llm_targets.id`（UUID，不是 URL）  
`lb.Target.Addr` = 实际 URL

两个 account target 在 balancer 中：
```go
Target{ID: "uuid-1", Addr: "https://api.anthropic.com", Weight: 2, ...}
Target{ID: "uuid-2", Addr: "https://api.anthropic.com", Weight: 1, ...}
```

选路时：`Pick()` 按权重随机选 uuid-1 或 uuid-2 → 解析该 UUID 对应的 API Key → 转发请求。

### 测试覆盖

新增 4 个端到端测试（内部/proxy 和 internal/db）：

1. **TestSyncConfigTargets_SameURL_TwoKeys**（内核）  
   验证配置同步：同 URL 两个 Key → DB 中两个独立 target

2. **TestSyncConfigTargets_SameURL_TwoKeys_Cleanup**（精确性）  
   验证 cleanup：从两个 Key 缩减为一个，精确删除已移除的那个

3. **TestSyncLLMTargets_SameURL_TwoKeys_BothInBalancer**（运行时）  
   验证端到端：同 URL 两个 Key → balancer 有两个独立条目

4. **TestLLMTargetRepo_Upsert_SameURL_TwoAPIKeys**（仓库层）  
   验证 Upsert 按 (url, api_key_id) 复合键操作

全覆盖链路：配置文件 → DB 同步 → 内存 balancer → 请求路由

### 升级指南

1. 替换二进制
   ```bash
   ./sproxy version  # 应输出：sproxy v2.24.3
   ```

2. 重启 sproxy（Schema 自动迁移）

3. **（可选）** 添加多 Key 配置
   ```yaml
   llm:
     targets:
       - url: "https://api.anthropic.com"
         api_key: "${KEY_A}"
         weight: 2
       - url: "https://api.anthropic.com"
         api_key: "${KEY_B}"
         weight: 1
   ```

4. 验证：
   - 日志中确认迁移完成
   - 调用 `/api/admin/llm/targets` 查看两个 target 已创建
   - 多个请求分配到两个 Key（观察日志或 token 使用统计）

### 不兼容变更

**无**。完全向后兼容：

- 单 Key 配置行为完全相同（内部自动生成 UUID）
- 数据库 migration 是增量（新列，无删除或重命名）
- API 同 v2.7.0+，无新增必需字段

### FAQ

**Q: 如果 config 中的两个 target 都是同一 URL 和 Key，会怎样？**  
A: 第一条创建 target，第二条 Upsert 时发现 (url, key_id) 相同 → 更新其他字段（如 weight、name）。只产生一条 DB 记录。

**Q: 能否从 WebUI 添加多个 Key？**  
A: 是的。进入"LLM 管理"→"动态 Target"，按"添加"，输入 URL（同一个） → 系统自动创建新 target。

**Q: 能否在 target 级禁用其中一个 Key？**  
A: 是的。在 WebUI 或 CLI 上进行 disable，该 target 从 balancer 移除，其他 Key 仍可用。

**Q: 多个 Key 的配额/速率限制如何处理？**  
A: 每个 Key 独立走配额系统，互不影响。系统会分别统计每个 target 的 token 使用。

---

## 45. v2.24.5 更新说明：智能探活（Smart Probe）

**版本**：v2.24.5  
**类型**：功能增强（健康检查）

### 45.1 背景

Issue #4 在 v2.23.0 中已修复了健康检查认证注入问题，但仍要求用户手动配置 `health_check_path`。对于没有公开 `/health` 端点的大型 LLM 服务商（Anthropic、OpenAI、火山引擎、华为云等），用户需要查阅文档才知道应该配置什么路径。

此外，v2.23.0 的修复存在一个边界问题：在带有真实 API Key 的情况下，若服务对 `/v1/models` 返回 401（如华为云、小米，因为 Key 格式不同），系统会误判为"无合适探活路径"，导致这些服务仍然无法被主动探活。

### 45.2 新增功能：自动发现探活策略

v2.24.5 引入**智能探活（Smart Probe）**机制，核心特性：

#### 全自动：无需配置路径

所有 target 默认启用主动健康检查，无需配置 `health_check_path`：

```yaml
# v2.24.5 — 以下配置即可启用所有健康检查功能，无需额外字段
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
```

#### 策略自动发现与缓存

首次探活时按优先级尝试 5 种内置策略，找到第一个有效的后缓存（默认 2 小时），后续直接复用：

| 优先级 | 策略 | 视为"端点存在" |
|--------|------|---------------|
| 1（Anthropic 专用） | `GET /v1/models` | 200、401、403、400 |
| 2（Anthropic 专用） | `POST /v1/messages` | 200、401、403、400 |
| 3（通用） | `GET /health` | 200 |
| 4（通用） | `GET /v1/models` | 200、401、403 |
| 5（通用） | `POST /v1/chat/completions` | 200、401、403、400 |

> `provider=anthropic` 的 target 优先尝试 Anthropic 专用策略（优先级 1、2），然后再尝试通用策略（3、4、5）。

#### 发现阶段 vs 心跳阶段语义分离

这是此版本的核心修复：

| 阶段 | 401/403 含义 | 处理方式 |
|------|-------------|---------|
| **发现阶段**（首次探活） | "端点存在，有认证机制" | ✅ 记录为有效探测路径 |
| **心跳阶段**（后续定期） | "API Key 无效" | ❌ 记录为 failure，累计熔断 |

这确保了：华为云、小米等在 key 无效时返回 401 的服务，能被成功发现探测路径；一旦进入正式心跳周期，401 仍会正确触发熔断。

### 45.3 配置说明

#### 无需修改现有配置

- 已配置 `health_check_path` 的 target：沿用显式路径，不走智能探活（向后兼容）
- 未配置 `health_check_path` 的 target：自动启用智能探活（行为变更：之前依赖被动熔断，现在主动探活）

#### 可选：显式指定路径

若您已知路径且希望固定探测行为，可继续使用 `health_check_path`：

```yaml
targets:
  - url: "https://my-custom-llm.company.com"
    api_key: "${MY_KEY}"
    provider: "openai"
    health_check_path: "/api/health"   # 显式指定，优先于智能探活
```

### 45.4 可观测性

启用 `log.level: debug` 后可观察完整探活过程：

```bash
# 查看智能探活日志
journalctl -u sproxy -f | grep "probe:\|smart probe:"
```

**首次探活（缓存冷启动）**：
```
INFO  smart probe: discovering health check method  {target: anthropic-api, addr: https://api.anthropic.com, provider: anthropic}
INFO  probe: discovered working health check method  {target: anthropic-api, method: GET /v1/models (anthropic), status: 401}
DEBUG smart probe: initial health check failed after discovery  {target: anthropic-api, status: 401}
```
（401 表示 key 无效，正确标记为不健康；一旦替换为有效 key，下次心跳将返回 200 并标记为健康）

**缓存命中（正常心跳）**：
```
DEBUG health check ok  {target: openai-api}
```

**凭证更新后重新发现**：
```
INFO  credentials updated  {count: 2}
INFO  smart probe: discovering health check method  {target: anthropic-api, ...}
```

### 45.5 各厂商兼容性

| 服务 | 自动发现策略 | 首次探活状态码 | 有效 key 时健康状态 |
|------|------------|--------------|------------------|
| Anthropic API | `GET /v1/models` | 401 | ✅ 200 |
| OpenAI API | `GET /v1/models` | 200 | ✅ 200 |
| 火山引擎 Ark（OpenAI） | `GET /v1/models` | 200 | ✅ 200 |
| 火山引擎 Ark（Anthropic） | `GET /v1/models` | 401 | ✅ 200 |
| 腾讯 LKEAP（OpenAI） | `GET /v1/models` | 200 | ✅ 200 |
| 腾讯 LKEAP（Anthropic） | `POST /v1/messages` | 401 | ✅ 200 |
| 华为云 ModelArts | `GET /v1/models` | 401 | ✅ 200（key 正确时） |
| 小米 MiMo | `GET /v1/models` | 401 | ✅ 200（key 正确时） |
| vLLM / sglang | `GET /health` | 200 | ✅ 200 |
| Ollama | `GET /health` | 200 | ✅ 200 |
| 阿里云 DashScope（/coding/v1） | ❌ 无合适路径 | 404 | 退化为纯被动熔断 |

### 45.6 不兼容变更

**无破坏性变更**。唯一的行为变更：

- **之前**：未配置 `health_check_path` 的 target 仅依赖被动熔断（不主动探活）
- **之后**：所有 target 均参与主动健康检查（智能探活自动发现路径）

若某个 target 的服务在探活路径上有副作用（如收费），建议显式配置 `health_check_path: ""` 并在代码层面等待后续版本支持完全关闭主动探活的选项（当前版本无此开关）。
