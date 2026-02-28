# PairProxy 用户手册

**版本 v1.1.0**

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
10. [监控与告警](#10-监控与告警)
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
curl http://localhost:9000/health
# 应返回：{"status":"ok","service":"sproxy"}
```

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
  self_addr: "http://sp2.company.com:9000"    # 本节点自身的地址
  self_weight: 50
  report_interval: 30s

dashboard:
  enabled: false    # worker 不开启 Dashboard

log:
  level: "info"
```

> **重要**：所有节点必须使用**完全相同**的 `JWT_SECRET`，否则用户的登录状态在不同节点间不互认。

worker 节点启动后会自动向 primary 注册，cproxy 会在下一次请求后自动感知到新节点并开始分流。

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

> **注意**：cproxy 默认在前台运行，**关闭这个终端窗口后 cproxy 会停止**。请在专用的终端窗口或后台方式运行（见 6.4 节）。

### 6.4 让 cproxy 在后台持续运行

cproxy 必须保持运行状态，Claude Code 才能正常工作。以下是各平台的推荐方案：

**macOS — 使用 launchd（推荐，系统级后台服务）**：

```bash
# 创建 launchd plist 文件
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.pairproxy.cproxy.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.pairproxy.cproxy</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/cproxy</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/cproxy.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/cproxy.log</string>
</dict>
</plist>
EOF

# 加载并启动
launchctl load ~/Library/LaunchAgents/com.pairproxy.cproxy.plist

# 查看运行状态
launchctl list | grep cproxy

# 查看日志
tail -f /tmp/cproxy.log
```

**Linux — 使用 systemd user service**：

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

systemctl --user enable --now cproxy
systemctl --user status cproxy
```

**Windows — 使用计划任务（开机自动运行）**：

在 PowerShell 中执行（以管理员身份运行）：

```powershell
$action = New-ScheduledTaskAction -Execute "C:\pairproxy\cproxy.exe" -Argument "start"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0
Register-ScheduledTask -TaskName "PairProxy cproxy" -Action $action -Trigger $trigger -Settings $settings -RunLevel Highest
Start-ScheduledTask -TaskName "PairProxy cproxy"
```

**最简单的方式（任意平台）—— 使用 tmux 或 screen 保活**：

```bash
# 安装 tmux（Linux/macOS）
# Ubuntu: sudo apt install tmux
# macOS: brew install tmux

tmux new-session -d -s cproxy 'cproxy start'

# 查看日志
tmux attach -t cproxy
# 按 Ctrl+B 然后按 D 退出（不会停止 cproxy）
```

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
curl http://proxy.company.com:9000/health
```

返回 `{"status":"ok","service":"sproxy"}` 则正常。

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

---

## 11. 升级指南

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
# 如果用 launchd（macOS）：
launchctl unload ~/Library/LaunchAgents/com.pairproxy.cproxy.plist
launchctl load ~/Library/LaunchAgents/com.pairproxy.cproxy.plist

# 如果用 systemd（Linux）：
systemctl --user restart cproxy
```

升级后无需重新登录，本地已保存的令牌继续有效。

---

## 12. 常见问题与排查

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

**解决**：参考第 6.4 节，将 cproxy 配置为后台服务（使用 launchd、systemd 或计划任务）。

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

# sproxy 调试模式
LOG_LEVEL=debug sproxy start --config /etc/pairproxy/sproxy.yaml
```

调试日志会显示每条请求的详细转发过程，帮助定位问题。

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

### 13.3 定期维护

- **员工离职**：立即执行 `sproxy admin user disable <用户名>` 和 `sproxy admin token revoke <用户名>`
- **定期审查**：每月查看 `sproxy admin stats --days 30`，留意异常高用量的账号
- **数据库备份**：定期备份 SQLite 文件（默认路径 `/var/lib/pairproxy/pairproxy.db`）：
  ```bash
  cp /var/lib/pairproxy/pairproxy.db /backup/pairproxy-$(date +%Y%m%d).db
  ```

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
  primary: "http://proxy.company.com:9000"  # sproxy 地址
  request_timeout: 300s                     # 单次请求超时，建议 ≥ 5 分钟

auth:
  refresh_threshold: 30m   # 令牌剩余有效期低于此值时自动续期

log:
  level: "info"   # 正常使用 info；遇到问题时改为 debug
```

### 14.2 sproxy 配置（`sproxy.yaml`）完整字段说明

```yaml
# 监听配置
listen:
  host: "0.0.0.0"   # 0.0.0.0 表示监听所有网卡（允许外部访问）
  port: 9000

# 上游 LLM 配置
llm:
  lb_strategy: "round_robin"   # 多个 target 时的负载均衡策略：round_robin / random / weighted
  request_timeout: 300s        # 向 Anthropic 发请求的超时时间
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY_1}"  # 从环境变量读取（不要写明文）
      weight: 1                          # 权重，仅 weighted 策略时有效
    # 可以配置多个 target，实现多 Key 轮询
    # - url: "https://api.anthropic.com"
    #   api_key: "${ANTHROPIC_API_KEY_2}"
    #   weight: 1

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

# admin 配置
admin:
  password_hash: "${ADMIN_PASSWORD_HASH}"  # admin 密码的 bcrypt hash

# 集群配置
cluster:
  role: "primary"              # primary（主节点）或 worker（工作节点）
  self_addr: ""                # 本节点的外部访问地址（集群模式必填，单机可留空）
  self_weight: 50              # 本节点权重（1~100，影响 cproxy 分配给本节点的流量比例）
  primary: ""                  # worker 专用：主节点地址
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
