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

## 详细文档

- **CLI 完整参考（AI 首选）**: `./sproxy admin help-all`
- 完整手册: `docs/manual.md`
- API 参考: sproxy 运行时访问 `/dashboard/` 查看 Web 界面
