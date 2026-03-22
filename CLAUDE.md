# PairProxy Gateway — Claude 管理助手指南

你是 PairProxy 网关的配置助手。通过 `sproxy` CLI 执行所有管理操作。

## 首要步骤：加载完整命令参考

**在执行任何管理操作之前，先运行以下命令获取所有 CLI 命令的完整语法和参数说明：**

```bash
./sproxy admin help-all
```

该命令以 Markdown 格式输出：
- 每条命令的完整语法和 flag 说明表
- 具体使用示例
- 自然语言触发短语（Natural language triggers）
- 快速对照表（自然语言 → shell 命令）

**当用户用自然语言描述操作需求时，执行流程：**
1. 运行 `./sproxy admin help-all` 读取命令参考
2. 在 "Quick-reference Cheatsheet" 或对应章节找到匹配命令
3. 按照语法组装并执行完整的 shell 命令

> 如果已在本次会话中执行过 `help-all`，无需重复执行，直接使用已加载的参考即可。

---

## 环境信息

- **配置文件**: `sproxy.yaml`（当前目录，或用 `--config` 指定）
- **Go binary**: `C:/Program Files/Go/bin/go.exe`（如未编译则先 build）
- **数据库**: SQLite，路径在 sproxy.yaml 的 `database.path` 字段
- **sproxy 命令**: 已编译时用 `./sproxy`，未编译时用 `go run ./cmd/sproxy`
- **多 Provider 支持**: sproxy 同时支持 Anthropic (`/v1/messages`) 和 OpenAI (`/v1/chat/completions`) 格式
- **协议转换**: 自动支持 Anthropic → OpenAI 协议转换（用于 Claude CLI → Ollama 场景）
- **语义路由 (v2.18.0)**: 根据请求 messages 语义意图缩窄 LLM 候选池；分类失败自动降级；规则支持 YAML + DB 双来源
- **训练语料采集 (v2.16.0)**: 异步采集 LLM 请求/响应对为 JSONL 训练语料，质量过滤，文件自动轮转
- **HMAC Keygen (v2.15.0)**: sk-pp- API Key 使用 HMAC-SHA256 生成，无碰撞漏洞，配置文件需添加 `auth.keygen_secret`（必填）
- **PostgreSQL Peer Mode (v2.14.0)**: PG 模式下所有节点完全对等，消除 Primary 单点故障
- **认证方式**: 两种头均可 — `X-PairProxy-Auth: <jwt>` 或 `Authorization: Bearer <jwt>`

## 命令速查

### 用户管理
```bash
./sproxy admin user list                                   # 列出所有用户
./sproxy admin user list --group <groupname>               # 按分组过滤
./sproxy admin user add <username> --password <pw>         # 新建用户
./sproxy admin user add <username> --password <pw> --group <group>
./sproxy admin user disable <username>                     # 禁用用户
./sproxy admin user enable <username>                      # 启用用户
./sproxy admin user reset-password <username> --password <newpw>
./sproxy admin user set-group <username> --group <name>    # 变更用户分组
./sproxy admin user set-group <username> --ungroup         # 从分组移除用户
```

### 分组与配额管理
```bash
./sproxy admin group list                                  # 列出所有分组（含配额）
./sproxy admin group add <name>                            # 新建分组
./sproxy admin group set-quota <name> \
  --daily <tokens> \                                       # 日 token 上限（0=不限）
  --monthly <tokens> \                                     # 月 token 上限（0=不限）
  --rpm <n>                                                # 每分钟请求上限（0=不限）
./sproxy admin group delete <name>                         # 删除分组（需无成员）
./sproxy admin group delete <name> --force                 # 强制删除（成员自动解绑）
```

### 配额用量查询
```bash
./sproxy admin quota status --user <username>              # 查看用户今日/本月用量 vs 配额
./sproxy admin quota status --group <name>                 # 查看分组今日/本月总用量 vs 配额
```

### 用户自助查询（F-10）
普通用户可通过 Dashboard 或 REST API 查询自己的配额和用量：

**Web 界面**：登录 Dashboard 后点击「我的用量」，或访问 `/dashboard/my-usage`

**REST API**（需用户 JWT 认证）：
```bash
# 查询配额状态
curl -H "Authorization: Bearer <user-jwt>" \
  http://localhost:9000/api/user/quota-status

# 查询用量历史（默认 30 天）
curl -H "Authorization: Bearer <user-jwt>" \
  "http://localhost:9000/api/user/usage-history?days=30"
```

**响应示例**：
```json
// GET /api/user/quota-status
{
  "daily_limit": 50000,
  "daily_used": 12345,
  "monthly_limit": 1000000,
  "monthly_used": 234567,
  "rpm_limit": 10
}

// GET /api/user/usage-history
{
  "history": [
    {"date": "2025-03-01", "input_tokens": 12345, "output_tokens": 3456, "total_tokens": 15801, "request_count": 12}
  ]
}
```

### LLM 目标管理
```bash
./sproxy admin llm targets                                 # 查看所有 LLM target 及健康状态、绑定数
./sproxy admin llm list                                    # 查看用户/分组绑定关系
./sproxy admin llm bind <username> --target <url>          # 将用户绑定到指定 LLM
./sproxy admin llm bind --group <name> --target <url>      # 将分组绑定到指定 LLM
./sproxy admin llm unbind <username>                       # 解除用户绑定
./sproxy admin llm distribute                              # 均分所有活跃用户到所有 target
```

### LLM Target 动态管理

**设计理念**：
- **配置文件（sproxy.yaml）**：定义初始 target 列表，服务启动时加载
- **数据库（SQLite）**：运行时动态管理，支持增删改查、启用/禁用，无需重启服务
- **优先级**：数据库中的 target 配置优先于配置文件（启动时合并，数据库记录覆盖同 URL 的配置文件条目）

**CLI 命令**：
```bash
# 查看所有 target（含健康状态、绑定数、启用状态）
./sproxy admin llm targets

# 添加新 target（立即生效，无需重启）
./sproxy admin llm target add \
  --url "https://api.example.com" \
  --api-key "sk-..." \
  --provider "anthropic" \
  --name "新节点" \
  --weight 1

# 更新 target 配置（支持部分更新）
./sproxy admin llm target update "https://api.example.com" \
  --api-key "sk-new-key" \
  --weight 2 \
  --name "更新后的名称"

# 禁用 target（保留配置，停止路由流量）
./sproxy admin llm target disable "https://api.example.com"

# 启用 target（恢复路由流量）
./sproxy admin llm target enable "https://api.example.com"

# 删除 target（需先解除所有用户/分组绑定）
./sproxy admin llm target delete "https://api.example.com"

# 强制删除 target（自动解绑所有用户/分组）
./sproxy admin llm target delete "https://api.example.com" --force
```

**使用示例**：
```bash
# 场景1: 临时下线维护节点
./sproxy admin llm target disable "https://api.node1.com"
# 维护完成后恢复
./sproxy admin llm target enable "https://api.node1.com"

# 场景2: 动态扩容（添加新节点）
./sproxy admin llm target add \
  --url "https://api.node3.com" \
  --api-key "${NODE3_KEY}" \
  --provider "anthropic" \
  --name "扩容节点3" \
  --weight 1
# 自动参与负载均衡，无需重启

# 场景3: 更新 API Key（密钥轮换）
./sproxy admin llm target update "https://api.node1.com" \
  --api-key "sk-new-rotated-key"

# 场景4: 调整权重（流量倾斜）
./sproxy admin llm target update "https://api.node1.com" --weight 3
./sproxy admin llm target update "https://api.node2.com" --weight 1
# node1 将获得 75% 流量，node2 获得 25%
```

**WebUI 使用**：
1. 访问 Dashboard → "LLM Targets" 页面
2. 查看所有 target 的实时状态（健康检查、绑定数、启用状态）
3. 点击 "Add Target" 按钮添加新节点（表单填写 URL、API Key、Provider 等）
4. 点击 target 行的 "Edit" 按钮修改配置
5. 使用 "Enable/Disable" 开关快速切换节点状态
6. 点击 "Delete" 按钮删除节点（需确认，若有绑定会提示）

**注意事项**：
- 动态添加的 target 仅存储在数据库中，不会写回 sproxy.yaml
- 禁用的 target 不参与负载均衡和健康检查，但保留配置和绑定关系
- 删除 target 前需先解除所有用户/分组绑定（或使用 `--force` 自动解绑）
- 配置文件中的 target 在每次启动时会同步到数据库（URL 相同则更新，不存在则插入）

### 统计与审计
```bash

### 批量导入分组和用户
```bash
./sproxy admin import users.txt                            # 从文件批量导入
./sproxy admin import --dry-run users.txt                  # 预览（不实际写入）
```

**模板格式（`users.txt`）**：
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
- 已存在的分组/用户跳过（保留原有数据，仅创建新增）
- 组已存在时不会重置其 LLM 绑定
- 用户级 `llm=URL` 覆盖该用户绑定，不影响同组其他用户
- 模板文件含明文密码，导入完成后应妥善保管或删除

### 统计与审计
```bash
./sproxy admin stats                                       # 全局统计（最近 7 天）
./sproxy admin stats --user <username>                     # 指定用户统计
./sproxy admin stats --days 30                             # 最近 30 天
./sproxy admin audit [--limit 100]                         # 查看最近管理操作记录
./sproxy admin token revoke <username>                     # 吊销用户的刷新 token
```

### 日志与数据库维护
```bash
./sproxy admin logs purge --before 2025-01-01              # 删除指定日期前的日志
./sproxy admin logs purge --days 90                        # 删除 90 天前的日志
./sproxy admin backup [--output <path>]                    # 备份数据库
./sproxy admin restore <backup-file>                       # 从备份文件恢复
./sproxy admin export --format csv --output logs.csv       # 导出日志为 CSV
./sproxy admin export --format json --from 2025-01-01      # 按日期范围导出
```

### 配置验证
```bash
./sproxy admin validate                                    # 验证 sproxy.yaml 是否有效
./sproxy admin validate --config /path/to/sproxy.yaml
```

### API Key 管理（LLM 提供商密钥）
```bash
./sproxy admin apikey list                                 # 列出已配置的 API keys
./sproxy admin apikey add <name> --value <value> --provider anthropic
./sproxy admin apikey revoke <name>
```

### 对话内容跟踪
```bash
./sproxy admin track enable <username>                     # 启用对指定用户的对话内容跟踪
./sproxy admin track disable <username>                    # 停用跟踪（历史记录保留）
./sproxy admin track list                                  # 列出所有已启用跟踪的用户
./sproxy admin track show <username>                       # 列出该用户的对话记录文件
./sproxy admin track clear <username>                      # 删除该用户的所有对话记录文件
```

### 语义路由管理（v2.18.0）
```bash
./sproxy admin semantic-router list --config sproxy.yaml           # 列出所有规则
./sproxy admin semantic-router status --config sproxy.yaml         # 查看运行状态
./sproxy admin semantic-router add \
  --name "code-tasks" \
  --description "编程、调试、代码审查类任务" \
  --targets "https://api.anthropic.com" \
  --priority 10                                                      # 创建规则
./sproxy admin semantic-router update <rule-id> --priority 5       # 更新规则优先级
./sproxy admin semantic-router enable <rule-id>                    # 启用规则
./sproxy admin semantic-router disable <rule-id>                   # 禁用规则
./sproxy admin semantic-router delete <rule-id>                    # 删除规则
```

### 训练语料采集管理（v2.16.0）
```bash
./sproxy admin corpus status --config sproxy.yaml                  # 查看采集状态
./sproxy admin corpus enable --config sproxy.yaml                  # 启用全局采集
./sproxy admin corpus disable --config sproxy.yaml                 # 禁用全局采集
./sproxy admin corpus list --config sproxy.yaml                    # 列出语料文件
./sproxy admin corpus enable --group engineering                   # 启用指定分组的采集
./sproxy admin corpus disable --group engineering                  # 禁用指定分组的采集
```

### sk-pp- API Key 管理（v2.15.0 HMAC 算法）
```bash
./sproxy admin keygen --user alice --config sproxy.yaml            # 为用户生成 sk-pp- Key
./sproxy admin keygen --verify sk-pp-... --config sproxy.yaml      # 验证 Key 有效性
```

### PostgreSQL Peer Mode 运维（v2.14.0+）
```bash
# Peer Mode 下所有节点对等，任意节点可查询路由表
curl -H "Authorization: Bearer $CLUSTER_SECRET" \
     http://sp-1:9000/cluster/routing | jq .

# 查看所有对等节点状态（PG 模式）
./sproxy admin peer list --config sproxy.yaml
```

## 常见场景处理

### 新用户入职
1. 创建用户: `./sproxy admin user add <name> --password <pw> --group <group>`
2. 如需专属 LLM: `./sproxy admin llm bind <name> --target <url>`
3. 验证: `./sproxy admin user list`

### 用户超额/限速
1. 快速查看剩余配额: `./sproxy admin quota status --user <name>`
2. 查看详细用量: `./sproxy admin stats --user <name>`
3. 查看分组配额上限: `./sproxy admin group list`
4. 调整配额: `./sproxy admin group set-quota <group> --daily <n>`
5. 或吊销 token 强制重新登录: `./sproxy admin token revoke <name>`

### 用户转组
1. 查看当前分组: `./sproxy admin user list`
2. 变更分组: `./sproxy admin user set-group <username> --group <newgroup>`
3. 验证: `./sproxy admin user list --group <newgroup>`

### 删除废弃分组
1. 查看成员: `./sproxy admin user list --group <name>`
2. 移出所有成员: `./sproxy admin user set-group <u> --ungroup`（逐一）或直接强制删除
3. 删除分组: `./sproxy admin group delete <name>`（或 `--force` 自动解绑）

### 数据库维护
1. 备份: `./sproxy admin backup --output pairproxy_$(date +%Y%m%d).db.bak`
2. 清理旧日志（保留近 90 天）: `./sproxy admin logs purge --days 90`
3. 恢复（需先停止 sproxy）: `./sproxy admin restore <backup-file>`

### 审计操作记录
- 查看最近操作: `./sproxy admin audit --limit 50`
- 注意: Dashboard 和 REST API 操作**及 CLI 操作**均记录在审计日志中

### 配置变更前验证
1. 编辑 sproxy.yaml 后先验证: `./sproxy admin validate`
2. 确认无错误后再重启服务

### LLM 目标故障/切换
1. 查看健康状态: `./sproxy admin llm targets`
2. 重新均分用户: `./sproxy admin llm distribute`
3. 验证分布: `./sproxy admin llm list`

### 语义路由配置（v2.18.0）
1. 查看当前状态: `./sproxy admin semantic-router status`
2. 查看现有规则: `./sproxy admin semantic-router list`
3. 添加新规则（描述要精准，分类器会据此判断意图）:
   ```
   ./sproxy admin semantic-router add --name "写作任务" \
     --description "文档撰写、邮件写作、内容创作类需求" \
     --targets "https://api.anthropic.com" --priority 20
   ```
4. 验证规则生效（查看日志中的 `semantic router: matched rule` 条目）

### 语料采集开关（v2.16.0）
1. 临时启用特定分组的采集: `./sproxy admin corpus enable --group research`
2. 查看已采集文件: `./sproxy admin corpus list`
3. 完成后禁用: `./sproxy admin corpus disable --group research`

### sk-pp- Key 轮换（v2.15.0）
1. 更新 `auth.keygen_secret`（`openssl rand -hex 32`）
2. 重启所有 sproxy 节点
3. 通知受影响用户重新生成 Key: `./sproxy admin keygen --user alice`

### 新增 LLM Target
1. 编辑 `sproxy.yaml`，在 `llm.targets` 添加新条目（需重启 sproxy）
2. 重启后均分: `./sproxy admin llm distribute`

### 查询某用户路由到哪个 LLM
1. `./sproxy admin llm list` → 找对应用户行
2. 若无用户绑定，检查其分组绑定: `./sproxy admin llm list`（看 GROUP 行）
3. 若无绑定，由负载均衡自动分配

## 协议转换（Claude CLI → Ollama）

**使用场景**: 企业部署 Ollama 本地模型，使用 Claude CLI 作为通用客户端

### 自动转换

当满足以下条件时，sproxy 自动进行协议转换：
- 请求路径为 `/v1/messages`（Anthropic 格式）
- 目标 LLM 的 `provider` 为 `"ollama"` 或 `"openai"`

### 配置示例

```yaml
llm:
  targets:
    # Ollama 本地部署（自动协议转换）
    - url: "http://localhost:11434"
      api_key: "ollama"
      provider: "ollama"
      name: "Ollama Local"
      weight: 1

    # Anthropic 官方（无需转换）
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"
      weight: 1
```

### 使用 Claude CLI

```bash
# 配置环境变量
export ANTHROPIC_API_KEY="<your-pairproxy-jwt>"
export ANTHROPIC_BASE_URL="http://localhost:9000"

# 直接使用（自动转换）
claude "What is 2+2?"
```

### 验证转换

```bash
# 查看日志确认协议转换
tail -f sproxy.log | grep "protocol conversion"

# 应看到类似日志：
# INFO  sproxy  protocol conversion required  from=anthropic to=openai target_provider=ollama
# INFO  sproxy  request converted successfully  new_path=/v1/chat/completions
```

**详细文档**: `docs/PROTOCOL_CONVERSION.md`

## 配置文件结构速查 (sproxy.yaml)

```yaml
server:
  addr: ":9000"
  jwt_secret: "..."

database:
  path: "./pairproxy.db"

llm:
  max_retries: 2              # 上游失败重试次数
  recovery_delay: 60s         # 熔断后自动恢复时间
  targets:
    - url: "https://api.anthropic.com"
      api_key: "sk-ant-..."
      provider: "anthropic"
      name: "主节点"
      weight: 1
      health_check_path: ""   # 空=被动检查

dashboard:
  enabled: true               # Web 管理界面
  admin_password: "..."

# 训练语料采集（v2.16.0+）
corpus:
  enabled: false
  output_dir: "./corpus"

# 语义路由（v2.18.0+）
semantic_router:
  enabled: false
  classifier_url: "http://localhost:9000"
  classifier_timeout: 5s

# sk-pp- Key 生成密钥（v2.15.0+ 必填）
auth:
  keygen_secret: "${KEYGEN_SECRET}"
```

## 注意事项

- **所有命令需要在项目目录下运行**（有 sproxy.yaml 的目录）
- **`--config` 可指定配置文件路径**，默认读 `./sproxy.yaml`
- **配额单位是 token 数**（Anthropic API 的 input+output tokens 之和）
- **LLM 绑定优先级**: 用户绑定 > 分组绑定 > 负载均衡自动选择
- **均分操作会覆盖现有用户级绑定**，分组绑定不受影响

## 测试要求

**每次开发新功能或修复 bug 时，必须完成以下测试：**

### 1. 单元测试 (UT)
```bash
# 运行所有单元测试
go test ./...

# 运行特定包的测试
go test ./internal/db/...
go test ./internal/proxy/...

# 查看测试覆盖率
go test -cover ./...
```

### 2. E2E 测试（三种方式全覆盖）

#### 方式1: httptest 自动化测试（必须）
```bash
# 快速自动化测试，适合 CI/CD
go test ./test/e2e/...
```
**用途**: 日常开发、快速验证、CI/CD 集成

#### 方式2: 真实进程集成测试（必须）
```bash
# 使用真实进程测试完整链路
go test -tags=integration ./test/e2e/...
```
**用途**: 真实环境验证、进程间通信测试

#### 方式3: 手动完整链路测试（推荐）
```bash
# 1. 启动 mockllm
./mockllm.exe --addr :11434 &

# 2. 启动 sproxy
./sproxy.exe start --config test-sproxy.yaml &

# 3. 启动 cproxy
./cproxy.exe start --config test-cproxy.yaml &

# 4. 登录
echo -e "testuser\ntestpass123" | ./cproxy.exe login --server http://localhost:9000

# 5. 运行测试
./mockagent.exe --url http://localhost:8080 --count 100 --concurrency 10

# 6. 清理
pkill -f "mockllm|sproxy|cproxy"
```
**用途**: 手动调试、压力测试、长时间稳定性测试

### 测试标准
- ✅ 所有单元测试必须通过
- ✅ 方式1 和方式2 的 E2E 测试必须通过
- ✅ 方式3 用于手动验证和压力测试
- ✅ 新功能必须添加对应的测试用例
- ✅ 测试覆盖率不低于现有水平

### 测试文档
- E2E 测试指南: `test/e2e/README.md`
- E2E 测试报告: `docs/E2E_TEST_REPORT.md`
- 测试覆盖率报告: `docs/TEST_COVERAGE_REPORT.md`

---

## 详细文档

- **CLI 完整参考（AI 首选）**: `./sproxy admin help-all`
- 完整手册: `docs/manual.md`
- API 参考: sproxy 运行时访问 `/dashboard/` 查看 Web 界面
