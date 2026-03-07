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

### 新增 LLM Target
1. 编辑 `sproxy.yaml`，在 `llm.targets` 添加新条目（需重启 sproxy）
2. 重启后均分: `./sproxy admin llm distribute`

### 查询某用户路由到哪个 LLM
1. `./sproxy admin llm list` → 找对应用户行
2. 若无用户绑定，检查其分组绑定: `./sproxy admin llm list`（看 GROUP 行）
3. 若无绑定，由负载均衡自动分配

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
