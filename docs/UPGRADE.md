# PairProxy 升级指南

> 当前版本：**v3.0.2** | 更新日期：2026-04-20

本文档描述各版本间的升级步骤、数据库 Schema 变更、回滚方法及不兼容变更。

---

## 通用升级流程

1. **备份数据库**

   ```bash
   cp pairproxy.db pairproxy.db.bak
   ```

2. **停止 s-proxy**

   ```bash
   # systemd
   systemctl stop sproxy

   # 或直接发送 SIGTERM
   kill -TERM <sproxy-pid>
   ```

3. **替换二进制文件**

   ```bash
   cp sproxy-new /usr/local/bin/sproxy
   cp cproxy-new /usr/local/bin/cproxy
   ```

4. **启动 s-proxy**（`db.AutoMigrate` 会自动应用 Schema 变更）

   ```bash
   systemctl start sproxy
   # 或
   sproxy start -c sproxy.yaml
   ```

5. **验证启动**

   ```bash
   curl http://localhost:9000/health
   # 期望: {"status":"ok", ...}
   ```

6. **逐步升级客户端**（cproxy 向后兼容，可分批升级）

---

## 版本变更记录

### v3.0.2 — LLM 目标稳定性 + 直连认证错误格式修复

**数据库 Schema 变更**

无。直接替换二进制重启即可。

**问题修复**

| # | 修复内容 |
|---|---------|
| 1 | **LLM 目标消失问题**：API Key 解析失败时目标从负载均衡器中消失（原因：`resolveAPIKey` 报错后直接 `continue` 跳过目标，下次 `SyncLLMTargets` 原子替换后目标彻底丢失）。现改为保留目标但强制标记为 unhealthy，ERROR 日志明确指出需要修复哪个 API Key |
| 2 | **绑定目标拒绝日志区分**：之前"未找到目标"和"目标不健康"使用同一条日志，难以区分。现分三种情况独立记录：`not found in balancer`、`is unhealthy`、`already tried` |
| 3 | **直连认证错误格式**：`sk-pp-` Key 无效时，客户端（Claude Code）收到通用 `{"error":"..."}` 格式，无法识别为终态认证错误而持续重试。现改为按协议返回对应格式：`/v1/messages` 和 `/anthropic/*` 返回 Anthropic 格式 `{"type":"error","error":{"type":"authentication_error",...}}`；`/v1/chat/completions` 返回 OpenAI 格式 |
| 4 | **`/v1/` 路由缺失 `x-api-key` 检测**：使用 Anthropic 风格 `x-api-key: sk-pp-...` 请求 `/v1/chat/completions` 时，路由器未识别为直连请求，返回 `missing_auth` 而非正确的认证错误 |
| 5 | **Dashboard 用户统计缓存**：群组变更、用户创建、激活状态变更后统计缓存未失效 |

**升级步骤**

```bash
# 无 Schema 变更，直接替换二进制重启
systemctl restart sproxy
```

---

### v3.0.1 — API Key 存储格式修复 + 健康检查假阳性修复 + LLM 目标同步状态

**数据库 Schema 变更**

- `api_keys` 表新增 `key_scheme` 列（`TEXT DEFAULT 'obfuscated'`）：标识 API Key 的存储格式，`"aes"` 表示 AES-256-GCM 加密，`"obfuscated"` 表示 config-sync 混淆存储。
- `llm_targets` 表新增 `is_synced` 列（`BOOLEAN DEFAULT true`）：标识目标是否已加载到内存。

GORM AutoMigrate 会在启动时自动添加这两列，无需手动执行 SQL。

**不兼容变更**

无。

**升级后注意事项（api_keys.key_scheme）**

`v3.0.1` 之前通过 Admin API 或 `sproxy admin apikey add` 创建的 API Key，升级后 `key_scheme` 列值为空（`NULL`/`""`），系统会自动走兼容路径（先试 AES，失败回退混淆）。

如需消除兼容路径的日志（极少数情况），可手动标记：

```sql
-- 将 Admin API 创建的 key 标记为 aes（Auto- 前缀是 config-sync 自动生成的）
UPDATE api_keys SET key_scheme = 'aes' WHERE name NOT LIKE 'Auto-%';
```

**升级步骤**

```bash
# 1. 备份数据库
cp pairproxy.db pairproxy.db.bak
# 2. 替换二进制，重启即可（AutoMigrate 自动加列）
systemctl restart sproxy
```

---

### v3.0.0 — AtoO 路径修复 + CLI 管理增强 + Dashboard 修复

**数据库 Schema 变更**

无新增 Schema 变更（与 v2.24.8 兼容，直接替换二进制即可）。

**不兼容变更**

- AtoO 协议转换路径行为变更：target URL 中的 base path（如 `/v2`）现在会被正确拼接到请求路径中。若已有 target URL 以 base path 结尾（如 `https://api.openai.com/v1`），请确认实际需要的路径为 `/v1/chat/completions`——此时无需修改，系统会正确处理；若 target URL 为根域名（如 `https://api.openai.com`），行为与之前完全一致。

**升级步骤**

```bash
# 1. 备份数据库
cp pairproxy.db pairproxy.db.bak

# 2. 替换二进制
cp sproxy-v3.0.0 /usr/local/bin/sproxy
cp cproxy-v3.0.0 /usr/local/bin/cproxy

# 3. 重启（无 Schema 迁移，直接重启即可）
systemctl restart sproxy
```

**v2.24.8 → v3.0.0 验证清单**

- [ ] `GET /health` 返回 `{"status":"ok"}`
- [ ] Anthropic → OpenAI 协议转换正常（检查带 base path 的 target）
- [ ] CLI `sproxy admin llm target update` 可修改 config-sourced target
- [ ] Dashboard 绑定关系列表正常显示

---

### v2.24.8 — 配额直连修复 + 旧 Key 吊销 + Model Mapping 透传 + 用户管理分页

**数据库 Schema 变更**

| 表 | 变更 |
|----|------|
| `users` | 新增 `legacy_key_revoked` 列（`BOOLEAN NOT NULL DEFAULT false`） |

`db.AutoMigrate` 在启动时自动添加此列，无需手动操作。

**升级影响评估**

| 影响项 | 说明 |
|--------|------|
| sk-pp- Key 可用性 | 无影响。仅当用户主动修改密码后，旧 legacy key 才被标记为无效 |
| 配额生效 | **行为变更**：原先直连路径不检查配额，升级后将严格按配置限制。如有超额用户，升级后请求会被拒绝 |
| Model Mapping | 透传模式现在也会应用 model_mapping，若配置了映射规则，实际发送的模型名称将被替换 |
| Dashboard API | 响应格式变更（见下表），若有外部脚本调用 `/dashboard/api/user-stats`，需适配新格式 |

**Dashboard API 响应格式变更**

```diff
- [{"id":"...","username":"alice",...}, ...]
+ {
+   "total": 42,
+   "page": 1,
+   "page_size": 20,
+   "total_pages": 3,
+   "users": [{"id":"...","username":"alice",...}, ...]
+ }
```

**升级步骤**

1. 备份数据库（推荐）
2. 替换二进制，重启服务（AutoMigrate 自动添加 `legacy_key_revoked` 列）
3. 验证：`curl http://localhost:9000/health` 返回 `{"status":"ok",...}`
4. 如已配置配额限制，告知用户从即日起配额开始生效

**回滚说明**

降级到 v2.24.7 时：
- `legacy_key_revoked` 列保留在数据库中，旧版本会忽略该列
- 配额不再对直连路径生效
- Model mapping 在透传模式下不再应用

---

### v2.24.4 — SQLite 时区 Bug 修复 + reportgen 错误日志 + 测试覆盖率提升

**数据库 Schema 变更**

无新增 Schema 变更。

**代码修复（无需迁移）**

| 修复 | 说明 |
|------|------|
| `UsageLog.BeforeCreate` GORM hook | 所有 UsageLog 写入前强制 `CreatedAt.UTC()`，解决 SQLite 字典序时区比较错误 |
| `toUTC()` — `usage_repo.go` | 9 个带时间过滤的方法（`Query`、`SumTokens` 等）入口统一转 UTC，确保查询边界与存储格式一致 |
| `warnQueryErr()` — `generator.go` | 所有查询失败从静默 `_, _` 改为 `WARNING: ...` 输出到 stderr |
| `loadMaps` Scan 错误 — `queries.go` | 扫描错误从静默丢弃改为 `WARNING:` 日志 + continue |
| E2E 测试种子数据时区 | `seedTokens`、`TestUserQuotaStatusE2E` 改用 `time.Now().UTC()` |

**升级验证**

```bash
# 1. 验证时区过滤正确
sproxy admin report --from 2026-04-01 --to 2026-04-09
# 期望: 正确的 token 统计数据，而非 0

# 2. 验证 reportgen 查询警告
./reportgen -db ./pairproxy.db -from 2026-01-01 -to 2026-04-09 2>&1 | grep WARNING
# 若数据正常，不应看到 WARNING

# 3. 验证所有测试通过
go test ./...
cd tools/reportgen && go test ./...
```

**回滚注意**

降级到 v2.24.3 时：
- `BeforeCreate` hook 消失后，新写入的 UsageLog 可能以本地时区格式存储
- 查询时区边界不再归一化，非 UTC 环境下过滤结果可能偏差
- 数据功能不受影响，仅统计查询受影响

---

### v2.24.3 — Issue #6 正式关闭 + 复合约束全面修复 + reportgen 容错增强

**数据库 Schema 变更**

无新增 Schema 变更（复合唯一约束已在 v2.24.0 引入）。

**代码修复（无需迁移）**

| 修复 | 说明 |
|------|------|
| `generateID()` 改用 UUID | 原 `time.Now().UnixNano()` 在同一纳秒内会生成重复 ID，导致同 URL 多 Key 创建失败 |
| `GetByGroupID()` → `ListByGroupID()` | 分组目标集支持一对多（一个 group 可有多个 target set） |
| `GetDefault()` → `ListDefaults()` | 支持多个全局默认目标集 |
| `FindForUser()` 防御性重写 | 使用 `Find()` + slice 检查，避免 `First()` 歧义 |
| `FindByProviderAndValue()` 防御性重写 | 多结果时返回明确错误而非静默返回第一条 |

**reportgen 工具增强**

新增 LLM 命令行参数支持：

```bash
./reportgen \
  -db ./pairproxy.db \
  -from 2026-01-01 -to 2026-04-01 \
  -llm-url http://localhost:9000 \
  -llm-key "your-api-key" \
  -llm-model gpt-4o-mini
```

- `-llm-url`：LLM 端点 URL（可选，若指定则优先于数据库配置）
- `-llm-key`：LLM API Key，Bearer token（可选）
- `-llm-model`：LLM 模型名（默认 `gpt-4o-mini`）

**容错机制**

- **数据库查询容错**：查询失败时自动跳过，继续处理
- **LLM 连接容错**：连接失败 → 降级为纯规则分析，仍可生成有意义的报告
- **LLM 调用保护**：Panic 恢复，异常不影响主流程
- **模板容错**：模板缺失 → 使用内置最小模板
- **无数据处理**：自动检测并生成提示洞察
- **API 兼容性**：OpenAI (`/v1/chat/completions`) 和 Anthropic (`/v1/messages`) 自动判断

**向后兼容性**

完全向后兼容，无数据迁移，无配置变更。新参数均为可选。

**升级验证**

```bash
# 1. 验证同 URL 多 Key 可用
sproxy admin llm target add --url https://api.anthropic.com --provider anthropic --api-key-id <key-A-id>
sproxy admin llm target add --url https://api.anthropic.com --provider anthropic --api-key-id <key-B-id>
sproxy admin llm target list  # 应显示两条记录

# 2. 测试 reportgen 新参数
reportgen -db ./pairproxy.db -from 2026-03-01 -to 2026-04-01 -llm-url http://localhost:9000 -llm-key "test"
# 应生成报告，即使 LLM 连接失败也能降级输出纯规则分析
```

---

### v2.24.5 — 智能探活（Smart Probe）

**无数据库 Schema 变更**。纯代码功能增强。

**新增功能**

健康检查系统升级为**智能探活**模式，无需手动配置 `health_check_path`：

- 新增 `internal/lb/probe.go`：`ProbeMethod`、`ProbeCache`、`Prober` 实现
- `HealthChecker` 自动为所有未配置显式路径的 target 执行策略发现与缓存
- 发现阶段与心跳阶段语义分离：401 在发现阶段 = "端点存在"，在心跳阶段 = "key 无效"
- 华为云、小米等在 key 无效时仅返回 401 的服务，现已支持主动探活

**行为变更**

| 场景 | v2.24.4 及之前 | v2.24.5 |
|------|--------------|---------|
| 未配置 `health_check_path` | 仅被动熔断 | **主动智能探活** |
| 已配置 `health_check_path` | 直接使用该路径 | 同左（向后兼容） |
| Anthropic/OpenAI 等 target | 需手动配置 `/v1/models` | **全自动** |
| 华为云 / 小米 | 探活失败（401 被拒绝） | **修复，自动发现** |

**向后兼容性**

- ✅ 已配置 `health_check_path` 的 target：行为完全不变
- ✅ 无配置的 target：升级后自动获得主动健康检查能力
- ✅ 无 Schema 变更，无配置文件变更

**升级验证**

```bash
# 1. 验证服务正常
curl http://localhost:9000/health

# 2. 查看智能探活日志（需 log.level: debug）
journalctl -u sproxy -f | grep "smart probe:\|probe:"

# 期望看到类似以下内容：
# INFO  smart probe: discovering health check method  {target: anthropic-api, ...}
# INFO  probe: discovered working health check method  {method: GET /v1/models, status: 401}

# 3. 验证 Dashboard 中 target 健康状态正常更新
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:9000/api/admin/llm/targets | jq '.[] | {url, healthy}'
```

**回滚说明**

降级到 v2.24.4 时：
- 未配置 `health_check_path` 的 target 回退到纯被动熔断（与 v2.24.4 行为一致）
- `probe.go` 中的代码会被移除，但无任何数据库或配置依赖

---

---

### v2.23.0 — APIKey 号池修复 + 健康检查认证 + 文档修正

**数据库 Schema 变更**

| 变更 | 说明 |
|------|------|
| `api_keys` 表：UNIQUE 约束变更 | 从 `(provider)` 改为 `(provider, encrypted_value)`，允许同 provider 多 Key 共存 |

Schema 变更由 `db.AutoMigrate` 自动完成，**无需手动操作**。

**向后兼容性**

- 现有 `api_keys` 记录保持原样，无数据丢失
- 旧格式 `'Auto-created for {provider}'` 的 Name 记录成为孤立记录（不影响功能）
- 降级到 v2.22.0 时，`encrypted_value` 列仍存在但 UNIQUE 约束不同；旧版本读写不受影响

**新增配置支持（无需修改现有配置）**

健康检查现在自动从 `llm.targets[*].api_key` 和 `llm.targets[*].provider` 读取认证信息，无需额外配置。

**升级验证**

```bash
# 1. 验证服务正常
curl http://localhost:9000/health

# 2. 验证健康检查对 Anthropic target 生效
# 查看日志是否出现 DEBUG 级别的 "injecting Anthropic auth" 或 "injecting Bearer auth"
grep -i "auth\|health" /var/log/sproxy.log | tail -20

# 3. 验证多 Key 号池（如配置了同 provider 多 target）
sproxy admin apikey list
# 应看到每个 target 的 Key 独立存在，不互相覆盖
```

**回滚说明**

降级到 v2.22.0 时：
- `api_keys` 表的 UNIQUE 约束回退（旧代码不执行新约束），功能正常
- 健康检查无认证注入，Anthropic/OpenAI 等 target 会返回 401（与 v2.22.0 行为一致）

---

### v2.18.0 — 语义路由 Semantic Router

**无数据库 Schema 变更**（SQLite 和 PostgreSQL 均适用）。

**新增数据库表（自动 AutoMigrate）**

| 表名 | 说明 |
|------|------|
| `semantic_router_rules` | 语义路由规则（name, description, targets, priority, enabled, source） |

**新增配置字段（可选，省略则禁用语义路由）**

```yaml
semantic_router:
  enabled: true                         # 启用语义路由
  classifier_url: "http://localhost:9000"  # 分类器端点（默认本机）
  classifier_timeout: 5s                # 分类超时
  rules:                                # YAML 内嵌规则（DB 规则优先，热更新）
    - name: "code-tasks"
      description: "编程、调试、代码审查类任务"
      targets:
        - "https://api.anthropic.com"
      priority: 10
```

**行为说明**

- 语义路由仅对**无 LLM 绑定**的用户生效，有绑定的用户不受影响
- 分类失败（超时/错误）时自动降级为完整候选池，不中断请求
- 规则来源：YAML 配置（静态）+ DB（动态热更新），DB 优先

**升级验证**

```bash
# 验证基本功能
curl http://localhost:9000/health

# 检查语义路由状态
sproxy admin semantic-router status

# 列出规则
sproxy admin semantic-router list
```

**回滚说明**

降级到 v2.17.0 时，`semantic_router_rules` 表会被保留但不使用，配置中的 `semantic_router` 字段会被忽略。无数据兼容性问题。

---

### v2.17.0 — LLM 故障转移增强

**无数据库 Schema 变更**，直接替换二进制即可。

**新增配置字段（可选）**

```yaml
llm:
  retry_on_status: [429, 500, 502, 503, 504]   # 触发换节点重试的状态码
  try_next_on_429: true                          # 429 时立即切换到下一个 target
```

**升级验证**

```bash
# 检查 retry 配置
grep retry_on_status sproxy.yaml
```

**回滚说明**

降级到 v2.16.0 时，新增配置字段会被忽略，无兼容性问题。

---

### v2.16.0 — 训练语料采集 Corpus

**无数据库 Schema 变更**，直接替换二进制。

**新增文件系统目录（启动时自动创建）**

```
<corpus_output_dir>/
├── corpus-2026-03-22.jsonl
└── corpus-2026-03-23.jsonl
```

**新增配置字段（可选）**

```yaml
corpus:
  enabled: false                        # 默认禁用
  output_dir: "./corpus"                # JSONL 输出目录
  max_file_size_mb: 100                 # 文件轮转大小（MB）
  min_response_tokens: 10               # 过滤短回复
  excluded_groups: []                   # 排除分组（隐私保护）
```

**新增 CLI 命令**

```bash
sproxy admin corpus status
sproxy admin corpus enable
sproxy admin corpus disable
sproxy admin corpus list
```

**回滚说明**

降级到 v2.15.0 时，`corpus_output_dir` 目录会被保留但不再使用，配置中的 `corpus` 字段会被忽略。无数据库兼容性问题。

---

### v2.24.7 — Per-User API Key Derivation + 自助改密码

**无数据库 Schema 变更**，但 API Key 生成算法改变，**所有已有 sk-pp- Key 全部失效**，需通知用户重新获取。

**算法变更**

| 版本 | Key 派生公式 |
|------|-------------|
| v2.15.0 – v2.24.6 | `HMAC-SHA256(username, keygenSecret)` |
| v2.24.7+ | `HMAC-SHA256(username, user.PasswordHash)` |

**配置变更**

`auth.keygen_secret` 字段已弃用，不再读取或验证。可从配置文件删除，也可保留（会被忽略）：

```yaml
auth:
  # keygen_secret 已弃用（v2.24.7+），可删除或保留（不影响启动）
  # keygen_secret: "${KEYGEN_SECRET}"
```

**升级步骤**

1. 替换二进制，重启服务（无需任何配置改动）
2. **通知所有 Direct Proxy 用户**（sk-pp- Key 用户）重新获取 Key：
   - 用户访问 `https://sproxy.company.com/keygen/`，登录后即可看到新 Key
   - 或管理员生成：`sproxy admin keygen --user <username>`

**新增功能**

- 用户可通过 Keygen WebUI 自助修改密码，改密后立即获得新 Key，无需管理员介入
- 管理员重置用户密码后，旧 Key 立即失效（KeyCache 主动清除）

**回滚说明**

降级到 v2.24.6 或 v2.15.0 时，需在 `sproxy.yaml` 中重新添加 `auth.keygen_secret`，否则旧版本配置验证失败。

---

### v2.15.0 — HMAC-SHA256 Keygen

> ⚠️ 已被 v2.24.7 覆盖：从 v2.14.x 升级时，直接跳至 v2.24.7 升级步骤，无需分两步。

**历史说明**：v2.15.0 引入 HMAC-SHA256 算法（替换旧指纹算法）并要求 `auth.keygen_secret` 必填。v2.24.7 进一步将派生密钥改为 per-user PasswordHash，`keygen_secret` 因此变为弃用字段。

**升级步骤（仅当目标版本为 v2.15.0–v2.24.6 时参考）**

1. 生成 keygen secret：
   ```bash
   openssl rand -hex 32
   ```
2. 将其设置为环境变量 `KEYGEN_SECRET`，并在 `sproxy.yaml` 中引用 `${KEYGEN_SECRET}`
3. 替换二进制，重启服务
4. **通知所有 Direct Proxy 用户**（sk-pp- Key 用户）重新生成 Key：
   ```bash
   sproxy admin keygen --user <username>   # 管理员生成
   # 或用户从 Dashboard 自助获取
   ```

**回滚说明**

降级到 v2.14.0 时，需移除 `auth.keygen_secret` 配置字段（旧版本不识别此字段会报错）。已失效的 sk-pp- Key 无法恢复，需重新下发旧算法生成的 Key。

---

### v2.14.0 — PostgreSQL Peer Mode

**需要已完成 v2.13.0 PostgreSQL 迁移**。

**配置变更**

```yaml
cluster:
  mode: "peer"    # 代替 primary/worker 角色区分
  # 所有节点使用相同配置
```

**新增数据库表（自动 AutoMigrate）**

| 表名 | 说明 |
|------|------|
| `pg_peer_registry` | 分布式节点发现注册表 |

**回滚说明**

降级到 v2.13.0 时，`pg_peer_registry` 表会被保留但不使用，`cluster.mode` 字段会被忽略。无数据兼容性问题。

---

### v2.13.0 — PostgreSQL 数据库支持

**重大升级**：可选从 SQLite 迁移到 PostgreSQL（彻底解决 Worker 一致性窗口问题）。

SQLite 迁移步骤为可选项，现有 SQLite 部署可继续使用无需变更。

**若选择迁移到 PostgreSQL**

1. 创建 PG 数据库和用户：
   ```sql
   CREATE DATABASE pairproxy;
   CREATE USER pairproxy WITH PASSWORD 'strong-password';
   GRANT ALL PRIVILEGES ON DATABASE pairproxy TO pairproxy;
   ```

2. 更新 `sproxy.yaml`：
   ```yaml
   database:
     driver: postgres
     dsn: "${PG_DSN}"
   ```

3. 迁移数据（如需保留历史数据，使用 `sproxy admin migrate-to-pg` 命令）

4. 重启 sproxy（AutoMigrate 自动在 PG 创建所有表）

**回滚说明**

若迁移后需回滚：将 `database.driver` 改回 `sqlite` 并指定原 `database.path`，PG 中写入的数据不会自动同步回 SQLite，建议回滚前先从备份恢复。

---

### v2.5.0 — 可靠性增强（用量可靠性 + 健康检查 + 路由发现 + 请求重试）

**无数据库 Schema 变更**，滚动升级，最小化停机时间。

**集群升级顺序**（推荐）

1. 先升级 worker 节点（sp-2, sp-3）
2. 再升级 primary 节点（sp-1）
3. 最后分批升级 cproxy 客户端

这样可以最小化停机时间，primary 停机期间由 worker 承载流量。

**升级时是否中断请求？**

取决于升级方式：

- **快速升级**（直接 `systemctl stop sproxy`）：**会中断正在处理的请求**
  - 流式请求（SSE）会断开
  - 等待 LLM 响应的请求会失败
  - 停机时间：通常 < 5s

- **优雅升级**（使用排水模式）：**零请求中断**
  ```bash
  ./sproxy admin drain enter          # 停止接受新请求
  ./sproxy admin drain wait --timeout 60s  # 等待活跃请求完成
  systemctl stop sproxy               # 安全停止
  ```
  - 详细步骤见本文档"滚动升级（零停机）"章节

**推荐**：生产环境使用排水模式，开发/测试环境可直接停止。

**新增配置字段**（均有合理默认值，不填写也能正常运行）

`cproxy.yaml` 新增：
```yaml
sproxy:
  health_check_timeout: 3s
  health_check_failure_threshold: 3
  health_check_recovery_delay: 60s
  passive_failure_threshold: 3
  shared_secret: ""          # 空=禁用路由主动轮询
  routing_poll_interval: 60s
  retry:
    enabled: true
    max_retries: 2
    retry_on_status: [502, 503, 504]
```

`sproxy.yaml` 新增（worker 节点）：
```yaml
cluster:
  usage_buffer:
    enabled: true
    max_records_per_batch: 1000
```

**Worker 节点数据库配置**（v2.5.0 新增要求）

如果 worker 节点之前没有配置数据库，需要在 sproxy.yaml 中添加：
```yaml
database:
  path: "/var/lib/pairproxy/worker.db"
```

这是因为 v2.5.0 的 usage_buffer 功能需要 worker 本地数据库来缓存用量记录。

**启用路由表主动发现**

需要在 cproxy.yaml 和 sproxy.yaml 中都配置相同的 shared_secret：

cproxy.yaml:
```yaml
sproxy:
  shared_secret: "your-cluster-secret"
  routing_poll_interval: 60s
```

sproxy.yaml (primary):
```yaml
cluster:
  shared_secret: "your-cluster-secret"
```

⚠️ 两边必须配置相同的密钥，否则鉴权失败。如果只配置一边：
- 只配置 cproxy：轮询请求会因鉴权失败返回 401
- 只配置 sproxy：cproxy 不会启动轮询（shared_secret 为空时禁用）

**cproxy 升级**

- v2.4.0 cproxy 可以继续使用，完全兼容 v2.5.0 sproxy
- 但建议升级以享受新功能：
  - 请求级重试（提升可用性）
  - 路由表主动发现（更快感知节点变化）
  - 健康检查优化（更精准的熔断控制）
- 升级顺序建议：先升级 sproxy，再升级 cproxy（避免 cproxy 轮询不存在的端点）

**升级验证**

1. 验证基本功能：
   ```bash
   curl http://localhost:9000/health
   ```

2. 验证路由表主动发现（查看 cproxy 日志）：
   ```bash
   # 应该看到类似日志：
   # INFO routing poll: sending routing update
   ```

3. 验证请求级重试（模拟节点故障）：
   ```bash
   # 停止一个 worker 节点，发送请求，应该自动重试到其他节点
   ```

4. 验证 worker 用量上报（查看 primary 日志）：
   ```bash
   # 应该看到类似日志：
   # INFO usage records received from peer
   ```

**回滚说明**

降级到 v2.4.0 时，新增配置字段会被忽略，无兼容性问题。

---

### v2.4.0 — 用户对话内容追踪

**无数据库 Schema 变更**，直接替换二进制即可，零停机升级。

**新增文件系统目录**（启动时自动创建，无需手动操作）

```
<db_dir>/track/
├── users/          # 追踪状态标记文件
└── conversations/  # 按用户分目录存储的 JSON 对话记录
```

目录位置：数据库文件（`database.path`）同级目录下的 `track/` 子目录。例如数据库在 `./pairproxy.db`，则追踪目录为 `./track/`。

**新增 CLI 命令**（无需配置文件变更）

```bash
sproxy admin track enable <username>
sproxy admin track disable <username>
sproxy admin track list
sproxy admin track show <username>
sproxy admin track clear <username>
```

**回滚说明**

降级到 v2.3.0 时，`track/` 目录会被保留但不再使用，可手动删除。无数据库兼容性问题。

---

### P3 升级（F-1 ~ F-7）

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `api_keys` | 系统级 API Key（加密存储，F-5） |
| `api_key_assignments` | API Key 分配记录（用户级/分组级，F-5） |

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 | 说明 |
|------|------|------|--------|------|
| `users` | `auth_provider` | TEXT | `'local'` | 认证来源（F-4） |
| `users` | `external_id` | TEXT | `''` | 外部系统 ID（LDAP uid，F-4） |
| `groups` | `max_tokens_per_request` | INTEGER | NULL | 单次最大 token 数（F-3） |
| `groups` | `concurrent_requests` | INTEGER | NULL | 最大并发请求数（F-3） |

所有新列均有默认值，AutoMigrate 自动添加，存量数据完全兼容。

**配置文件新增字段（均为可选，有合理默认值）**

| 字段路径 | 默认值 | 说明 |
|----------|--------|------|
| `auth.provider` | `"local"` | 认证提供者，`"local"` 或 `"ldap"` |
| `auth.ldap.*` | 见示例 | LDAP 配置（provider="ldap" 时生效） |
| `llm.targets[].provider` | `"anthropic"` | LLM 类型（F-1） |
| `cluster.alert_webhooks` | `[]` | 多 Webhook 告警（F-6） |

**配置文件新增字段（条件必填）**

| 字段路径 | 默认值 | 说明 |
|----------|--------|------|
| `admin.key_encryption_key` | `""` | API Key 加密密钥，**使用 `admin apikey` 命令时必填**（F-5） |

---

### Phase 6 → P2 升级

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `audit_logs` | 管理员操作审计日志（P2-3） |

AutoMigrate 会自动创建新表，**无需手动执行 SQL**。

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 |
|------|------|------|--------|
| `groups` | `requests_per_minute` | INTEGER | NULL |
| `usage_logs` | `cost_usd` | REAL | 0 |
| `usage_logs` | `source_node` | TEXT | `'local'` |
| `usage_logs` | `synced` | INTEGER | 0 |

这些列均有默认值，存量数据自动兼容。

**配置文件变更（P2 无破坏性变更）**

无需修改配置文件，所有新特性均自动启用。

---

### Phase 5 → Phase 6 升级

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 |
|------|------|------|--------|
| `groups` | `requests_per_minute` | INTEGER | NULL |
| `usage_logs` | `cost_usd` | REAL | 0 |

**新增配置字段（可选）**

```yaml
pricing:
  default_input_per_1k: 0.003   # USD/1K input tokens
  default_output_per_1k: 0.015  # USD/1K output tokens
  models:
    claude-3-5-sonnet-20241022:
      input_per_1k: 0.003
      output_per_1k: 0.015

cluster:
  alert_webhook: "https://hooks.slack.com/..."  # 可选
```

---

### Phase 4 → Phase 5 升级

**新增 Admin Dashboard**

在 `sproxy.yaml` 中启用（可选）：

```yaml
dashboard:
  enabled: true

admin:
  username: admin
  password_hash: "$2a$12$..."  # bcrypt hash
```

生成 admin 密码哈希：

```bash
sproxy admin hash-password
```

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `peers` | 集群节点注册表 |

---

### Phase 1 → Phase 2 升级

**无破坏性变更**，新增 `usage_logs` 表（首次启动时自动创建）。

---

## 回滚方法

### 回滚到上一版本二进制

1. 停止服务
2. 恢复旧二进制
3. **如果新版本添加了数据库列**，旧版本通常仍可运行（GORM 只读未知列不报错）
4. **如果新版本添加了数据库表**，旧版本不会报错（表不存在时 AutoMigrate 报错可忽略）
5. 启动旧版本服务

### 完整回滚（含数据库）

如果需要完整回滚数据库：

```bash
# 停止服务
systemctl stop sproxy

# 恢复备份
cp pairproxy.db.bak pairproxy.db

# 恢复旧二进制
cp sproxy-old /usr/local/bin/sproxy

# 启动
systemctl start sproxy
```

> ⚠️ 完整回滚会丢失回滚点之后的所有用量记录。

---

## 不兼容变更清单

### v2.15.0 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|---------|
| `auth.keygen_secret` 新增必填字段 | 缺少此字段时启动报错 | 升级前必须配置 |
| sk-pp- Key 算法从指纹嵌入改为 HMAC-SHA256 | 已有 Key 立即失效 | 通知用户重新获取 |

### v2.13.0 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|---------|
| PostgreSQL 支持（可选迁移） | SQLite 部署不受影响 | 按需迁移 |
| `database.driver` 新配置字段 | 默认 `sqlite`，无需修改已有配置 | 无 |

### P2 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `NewAdminHandler` 签名新增 `auditRepo` 参数 | 仅影响直接调用此函数的自定义代码 | 传入 `db.NewAuditRepo(logger, database)` |
| `dashboard.NewHandler` 签名新增 `auditRepo` 参数 | 同上 | 同上 |

### Phase 6 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `GroupRepo.SetQuota` 新增第4参数 `rpm *int` | 自定义代码需更新调用处 | 传入 `nil` 保持不限制 |
| `UsageLog.CostUSD` 新增列 | 旧版本读取不报错 | 无需处理 |

### 集群（Phase 3）引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `NewCProxy` 第4参数从 `[]SProxyTarget` 改为 `lb.Balancer` | 需更新调用处 | 使用 `lb.NewWeightedRandom(targets)` |
| 内部 API 需要 `cluster.shared_secret` | worker 节点心跳需认证 | 在 sproxy.yaml 中配置 `cluster.shared_secret` |

---

## 常见升级问题

### Q: AutoMigrate 是否安全？

是。GORM AutoMigrate 只会 **新增** 列和表，不会删除或修改现有列。

### Q: 能否零停机升级？

**单节点**：需要短暂停机（通常 < 5s，包含 AutoMigrate 时间）。

**多节点集群**：先升级 primary，再逐步升级 worker。由于 c-proxy 支持多 target 负载均衡，worker 升级期间流量自动切走。

### Q: 如何检查数据库是否正常迁移？

```bash
sqlite3 pairproxy.db ".tables"
# 应包含: audit_logs groups peers refresh_tokens usage_logs users
```

---

## 滚动升级（零停机）

多节点集群支持通过排水（Drain）机制实现零停机滚动升级。

### 滚动升级原理

排水模式允许节点优雅下线：
1. 节点进入排水模式后，不再接受新请求
2. 正在处理的请求继续完成
3. 当活跃请求数归零后，可安全停止节点
4. 升级完成后恢复正常模式

### 单节点升级流程

```bash
# 1. 备份数据库
cp pairproxy.db pairproxy.db.bak

# 2. 进入排水模式
./sproxy admin drain enter

# 3. 等待活跃请求归零
./sproxy admin drain wait --timeout 60s

# 4. 停止服务
systemctl stop sproxy

# 5. 替换二进制
cp sproxy-new /usr/local/bin/sproxy

# 6. 启动服务
systemctl start sproxy

# 7. 验证
curl http://localhost:9000/health
```

### 多节点集群升级流程

假设有 primary (sp-1) 和多个 worker (sp-2, sp-3)：

```bash
# ===== 升级 worker 节点（逐个进行）=====

# 在 sp-2 上执行：
./sproxy admin drain enter
./sproxy admin drain wait --timeout 120s
systemctl stop sproxy
cp sproxy-new /usr/local/bin/sproxy
systemctl start sproxy
curl http://localhost:9000/health

# 在 sp-3 上重复相同步骤...

# ===== 升级 primary 节点 =====

# 在 sp-1 上执行：
./sproxy admin drain enter
./sproxy admin drain wait --timeout 120s
systemctl stop sproxy
cp sproxy-new /usr/local/bin/sproxy
systemctl start sproxy
curl http://localhost:9000/health
```

### 排水命令详解

| 命令 | 说明 |
|------|------|
| `sproxy admin drain enter` | 进入排水模式 |
| `sproxy admin drain exit` | 退出排水模式 |
| `sproxy admin drain status` | 查看排水状态和活跃请求数 |
| `sproxy admin drain wait --timeout 60s` | 等待活跃请求归零 |

### 通过 REST API 操作

```bash
# 进入排水模式
curl -X POST http://localhost:9000/api/admin/drain \
  -H "Authorization: Bearer <admin-token>"

# 查看状态
curl http://localhost:9000/api/admin/drain/status \
  -H "Authorization: Bearer <admin-token>"

# 退出排水模式
curl -X POST http://localhost:9000/api/admin/undrain \
  -H "Authorization: Bearer <admin-token>"
```

### 通过 Dashboard 操作

访问 `/dashboard/` → LLM 管理 → 节点列表 → 点击 "Drain" 按钮

### 注意事项

1. **确保至少有一个健康节点**：排水期间其他节点应能承接流量
2. **设置合理的超时**：`drain wait --timeout` 避免无限等待
3. **长连接请求**：SSE 流式请求可能需要较长时间完成
4. **自动恢复**：节点重启后自动退出排水模式

---

### v2.20.0 — Group-Target Set、告警管理、目标健康监控

**新增数据库表（自动 AutoMigrate）**

- group_target_sets：分组目标集
- group_target_set_members：目标集成员（含 is_active、health_status）
- target_alerts：告警事件持久化

**配置变更**

新增可选配置节（不配置时行为与 v2.19.0 完全一致）：alert.enabled、alert.triggers、alert.recovery、health_monitor.interval、health_monitor.failure_threshold 等。

**重要修复（Bug 7）**

AddMember 改用原生 SQL INSERT，修复 GORM 零值陷阱导致 IsActive=false 被写为 true 的问题。升级后已有数据无需迁移。

**回滚**

回滚到 v2.19.0 时，新增的三张表不影响旧版本运行。如需清理：DROP TABLE IF EXISTS target_alerts; DROP TABLE IF EXISTS group_target_set_members; DROP TABLE IF EXISTS group_target_sets;