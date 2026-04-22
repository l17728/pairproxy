# PairProxy Changelog

## [v3.0.0] - 2026-04-19

### 🚀 Major Version Release

#### AtoO 协议转换路径修复（Breaking Improvement）

**问题**：Anthropic → OpenAI 协议转换时，`convertedPath` 固定为 `/v1/chat/completions`，完全忽略 target URL 中的 base path（如 `/v2`、`/openai/v1`），导致此类端点返回 404。

**修复**：
- `internal/proxy/protocol_converter.go` — `convertAnthropicToOpenAIRequest` 返回路径后缀 `/chat/completions`（不含版本前缀）
- `internal/proxy/sproxy.go` — Director 按 target base path 动态拼接最终路径：有 base path 时使用 `basePath + /chat/completions`，无 base path 时保持 `/v1/chat/completions` 兼容标准 OpenAI 端点

**效果**：
| target_url | 最终请求路径 |
|---|---|
| `https://api.openai.com` | `/v1/chat/completions` |
| `https://api.example.com/v2` | `/v2/chat/completions` |
| `https://api.example.com/openai/v1` | `/openai/v1/chat/completions` |

#### CLI 可修改 config-sourced LLM Target

**变更**：通过命令行（`sproxy admin llm target update/delete`）现在可以修改或删除来自配置文件的 LLM target（`source=config`），WebUI 界面仍保持拦截。

**修改文件**：
- `internal/db/llmtarget_repo.go` — `Update()` / `Delete()` 移除 `IsEditable` 拦截
- `internal/api/admin_llm_target_handler.go` — API handler 移除 `IsEditable` 拦截
- `cmd/sproxy/admin_llm_target.go` — CLI 命令移除 client 侧 `IsEditable` 检查

#### Dashboard 绑定关系列表修复

- 修复绑定关系列表不显示数据（`html/template` HTML 转义破坏 JSON.parse）
- 修复搜索过滤时表格列宽抖动
- 绑定关系列表支持分页、搜索、用户下拉框

#### 代码国际化

- 将 Go 源文件及设计文档中的日文注释全部替换为中文或英文

---

## [v2.24.8] - 2026-04-16

### 🐛 Bug Fixes

#### Direct Proxy 配额中间件未挂载（v2.24.8）

**问题**：通过 `sk-pp-` API Key 直连的请求绕过了 `QuotaMiddleware`，用户配置的 daily/monthly token 上限和 RPM 限制均不生效。

**根因**：`DirectProxyHandler.buildChain()` 构建中间件链时未包含 `QuotaMiddleware`，而普通 JWT 路径通过 `sp.Handler()` 中已有该中间件。

**修复**：
- `internal/proxy/direct_handler.go` — `NewDirectProxyHandler` 新增 `quotaChecker *quota.Checker` 参数；`buildChain` 在 KeyAuth 和 core handler 之间插入 `quota.NewMiddleware`
- `cmd/sproxy/main.go` — 传入运行时的 `quotaChecker` 实例

#### 改密后旧 API Key 仍有效（v2.24.8）

**问题**：用户通过 Keygen WebUI 修改密码后，由旧版共享 `keygenSecret` 派生的旧 Key 仍然可以访问接口（安全漏洞）。

**根因**：`ValidateWithLegacySecret` 作为兜底校验路径，即使用户已修改密码，旧的 legacy Key 依然通过 HMAC 校验。

**修复**：
- `internal/db/models.go` — `User` 新增 `LegacyKeyRevoked bool` 字段（`gorm:"default:false"`）
- `internal/db/user_repo.go` — `UpdatePassword` 原子写入 `legacy_key_revoked=true`（使用 `Updates(map[string]any{...})`，防止 GORM 跳过 false 零值）
- `internal/keygen/validator.go` — `ValidateWithLegacySecret` 跳过 `LegacyKeyRevoked=true` 的用户
- `internal/proxy/db_adapter.go` — `ListActive()` 传递该字段

#### `RecoveryMiddleware` 吞掉 `http.ErrAbortHandler`（v2.24.8）

**问题**：客户端主动断连时，Go 运行时抛出 `http.ErrAbortHandler` panic，但 `RecoveryMiddleware` 的 `recover()` 捕获后当作普通错误处理，返回 500 并写日志，而非静默忽略。

**修复**：`internal/proxy/middleware.go` — recover 后检测到 `rec == http.ErrAbortHandler` 时立即 re-panic，让 Go net/http 层按预期处理断连。

#### Model Mapping 在透传模式下不生效（v2.24.8）

**问题**：配置 `model_mapping`（如 `{"*": "MiniMax-2.5"}`）后，同协议透传（passthrough，无需协议转换）的请求不做模型重写，导致上游报"模型不存在"。

**根因**：`mapModelName()` 仅在协议转换（`conversionAtoO` / `conversionOtoA`）分支内调用，`conversionNone` 路径没有 model rewrite 逻辑。

**修复**：`internal/proxy/sproxy.go` — 在 `conversionNone` 分支末尾增加显式 model 重写块（读取 `modelMappingForURL`，调用 `mapModelName` + `rewriteModelInBody`）。

#### Keygen WebUI 配置说明错误（v2.24.8）

- 修正 `settings.json` 路径描述：Windows 用 `%USERPROFILE%\.claude\settings.json`，Linux/macOS 用 `~/.claude/settings.json`
- 删除 `ANTHROPIC_BASE_URL` 配置项（用户不需要暴露服务地址）
- 修正 Claude Code 配置片段格式为 `{"env":{"ANTHROPIC_API_KEY":"<key>"}}`

### ✨ New Features

#### Dashboard 用户管理页分页、过滤与排序（v2.24.8）

**后端**（`internal/dashboard/handler.go`）：

- `handleUserStats` 支持 `page`、`page_size`、`username`（子串过滤）、`group_id`、`sort_by`、`sort_order` 参数
- 返回结构变更：`{total, page, page_size, total_pages, users}`（分页响应体）
- 提取 `getFullUserStats(forceRefresh bool)` 缓存辅助函数；过滤/排序/分页在全量缓存上按请求参数实时计算
- 修复缓存污染 Bug：`filtered` 始终通过 `make([]userStatsResponse, 0, len(all))` 构建新切片，避免 `sort.SliceStable` 修改缓存原始切片

**前端**（`internal/dashboard/templates/users.html`）：

- 完全 JS 驱动的表格渲染，含 XSS 安全的 `esc()` 辅助函数
- 用户名搜索框（350ms 防抖）+ 用户组下拉过滤
- 每页显示条数选择器（20 / 50 / 100 / 200）
- 分页控件：首页/上页/下页/尾页 + 带省略号的页码按钮
- 列标题点击排序（再次点击切换正反序）
- 列表项操作（启用/禁用/删除）内联表单保留

### 🔧 改动详情

| 模块 | 变更 |
|------|------|
| `internal/proxy/direct_handler.go` | 新增 `quotaChecker *quota.Checker` 参数；`buildChain` 插入 `quota.NewMiddleware` |
| `cmd/sproxy/main.go` | 传入 `quotaChecker` 到 `NewDirectProxyHandler` |
| `internal/proxy/middleware.go` | `RecoveryMiddleware` re-panic `http.ErrAbortHandler` |
| `internal/proxy/sproxy.go` | `conversionNone` 分支增加 model mapping 重写逻辑 |
| `internal/db/models.go` | `User` 新增 `LegacyKeyRevoked bool` |
| `internal/db/user_repo.go` | `UpdatePassword` 原子写入 `legacy_key_revoked=true` |
| `internal/keygen/validator.go` | `ValidateWithLegacySecret` 跳过 `LegacyKeyRevoked=true` 用户 |
| `internal/proxy/db_adapter.go` | `ListActive()` 传递 `LegacyKeyRevoked` 字段 |
| `internal/api/keygen_handler.go` | 修正 settings.json 路径说明；删除 ANTHROPIC_BASE_URL；修正配置片段格式 |
| `internal/dashboard/handler.go` | 分页 API；`getFullUserStats` 缓存辅助；缓存污染 Bug 修复 |
| `internal/dashboard/templates/users.html` | 全 JS 渲染；过滤/排序/分页控件 |

---

## [v2.24.7] - 2026-04-14

### ✨ New Features

#### Direct Proxy API Key — Per-User Password Hash Derivation (v2.24.7)

**Breaking change for Direct Proxy users (sk-pp- Key holders)**：API Key 生成策略已变更，**已有 sk-pp- Key 全部失效**，用户需重新获取。

- **旧机制**：`HMAC-SHA256(username, keygenSecret)` — 所有用户的 Key 依赖同一个管理员 secret；轮换 secret 导致所有人 Key 同时失效
- **新机制**：`HMAC-SHA256(username, user.PasswordHash)` — 每个用户的 Key 由自己的 bcrypt 密码哈希派生；改密码自动轮换 Key，互不影响
- `auth.keygen_secret` 配置字段保留（兼容旧 YAML），不再读取或校验，可安全删除
- LDAP 用户（无本地 PasswordHash）无法持有 sk-pp- Key

#### 用户自助修改密码并更新 Key（Keygen WebUI）

新增 `POST /keygen/api/change-password` 端点：

- 用户通过 Keygen WebUI（`/keygen/`）自行修改密码
- 修改成功后立即：① 更新数据库密码哈希 ② 清空旧 Key 缓存 ③ 返回新派生的 API Key
- 客户端复制新 Key 后即可继续使用，无需管理员介入
- LDAP 用户调用此端点返回 403（本地账号限定功能）
- Worker 节点调用此端点返回 503（写操作仅限 Primary）

#### Admin 重置密码后立即清空 Key 缓存

管理员通过 `POST /api/admin/users/{id}/reset-password` 重置用户密码后，`AdminHandler` 现在会主动调用 `KeyCache.InvalidateByUserID(id)` 清除该用户的旧 Key，使旧 Key 即时失效，无需等待缓存 TTL。

### 🔧 改动详情

| 模块 | 变更 |
|------|------|
| `internal/keygen/validator.go` | `UserEntry` 新增 `PasswordHash` 字段；`ValidateAndGetUser` 签名去掉 `secret []byte`，改用 `u.PasswordHash` |
| `internal/keygen/cache.go` | 新增 `InvalidateByUserID(userID string)` 方法 |
| `internal/proxy/db_adapter.go` | `ListActive()` 填充 `UserEntry.PasswordHash` |
| `internal/proxy/keyauth_middleware.go` | 去掉 `keygenSecret` 参数 |
| `internal/proxy/direct_handler.go` | 去掉 `keygenSecret` 参数 |
| `internal/api/keygen_handler.go` | 去掉 `keygenSecret`，新增 `SetKeyCache()`，新增 `handleChangePassword`，WebUI 更新 |
| `internal/api/admin_handler.go` | 新增 `SetKeyCache()`，密码重置后调用 `InvalidateByUserID` |
| `internal/config/config.go` | 移除 `keygen_secret` 校验要求（字段保留但已弃用） |
| `cmd/sproxy/main.go` | 调整各 handler 初始化：移除 `keygenSecret`，调用 `SetKeyCache()` |

---

## [v2.24.6] - 2026-04-10

### 🐛 Bug Fixes

#### 智能探活（Smart Probe）行为修复

- **`WithTimeout` 未同步更新 prober 客户端**：调用 `WithTimeout` 仅更新了 `hc.client` 和 `hc.timeout`，但 `hc.prober` 持有自己独立的 `http.Client`，导致智能探活仍使用旧超时值。修复：`WithTimeout` 现同时重建 `hc.prober = NewProber(d, h.logger)`，确保三者超时一致。

- **空白 API Key 被当作有效凭证**：`injectCredential` 原先检查 `cred.APIKey == ""`，空格或制表符组成的 key 会通过检查并注入无效的 `Authorization` header，导致向上游发出带噪声认证头的请求。修复：先 `strings.TrimSpace()` 再判空，空白字符串视为"无凭证"。

- **`UpdateHealthPaths` 不触发缓存失效**：目标从显式健康检查路径改为全局路径时，旧的 probe 缓存仍存在，导致继续使用已失效的探活策略。修复：`UpdateHealthPaths` 现遍历旧路径集合，对不再拥有显式路径的目标调用 `probeCache.invalidate()`，与 `UpdateCredentials` 行为对齐。

- **`buildProbeURL` 未过滤查询参数和 Fragment**：当目标地址（`addr`）包含 `?query` 或 `#fragment` 时，探活 URL 会被拼接成无效路径（如 `/v1?key=val/models`）。修复：使用 `url.Parse` 提取路径，并在构建 addrBase 前用 `strings.IndexAny(addrBase, "?#")` 截断。

#### Discover 状态机修复

- **端点超时 vs 连接拒绝区分**：原 Discover 将所有 `definitivelyUnhealthy()` 错误均视为连接拒绝（`unreachable=true`），但 HTTP 请求超时也会触发此分支，导致临时超时的目标被错误地标记为不可达并缓存。修复：新增 `isEndpointTimeout()` helper，在 Discover 循环内区分两类失败：端点超时 → `continue` 尝试下一路径；硬连接失败（拒绝/DNS）→ 返回 `unreachable=true`。

- **所有路径均超时时错误标记不可达**：若 Discover 期间所有探活路径均超时（无 HTTP 响应），原实现会返回 `nil, false`，但语义不清晰。修复：通过 `gotHTTPResponse` 标志明确区分三种结果——有响应但不匹配（服务在线）vs 所有超时（网络不确定）vs 硬失败（连接拒绝），后两者均不缓存。

- **Context 取消被误认为 unreachable**：Discover 循环的上层 context 超时（预算耗尽）会导致 `ctx.Err() != nil`，若处理不当会与硬连接失败混淆。修复：循环顶部检查 `ctx.Err()`，设置 `budgetExhausted = true` 并 break，post-loop 返回 `nil, false`（非 unreachable）。

#### 并发安全与生命周期修复

- **`Start()` 重复调用导致 `close(stopCh)` panic**：`loop()` 退出时关闭 `stopCh`，若再次调用 `Start()` 使用同一已关闭的 channel，recovery goroutine 的 `select` 会在关闭的 channel 上立即返回，错误退出。修复：`Start()` 首行重建 `stopCh = make(chan struct{})`。

- **`recoveryDelay` goroutine 无法及时响应关闭信号**：进程关闭时，等待 `recoveryDelay`（可达数分钟）的 goroutine 会阻塞 `wg.Wait()`。修复：使用 `select { case <-time.After(delay): case <-hc.stopCh: return }` 替代 `time.Sleep`，关闭信号到达后毫秒级退出。

- **`checkAll()` 在锁释放后读取 `globalHealthPath`**：`hc.healthPath` 快照原在锁外读取，存在数据竞争。修复：在持有 `hc.mu` 时将其赋给局部变量 `globalHealthPath`，之后使用该快照。

### 🧪 测试覆盖率提升

新增 12 个单元/集成测试（`probe_unit_test.go`、`probe_real_test.go`）：

- `TestInjectCredential_WhitespaceKey` — 纯空格 key 不产生认证头
- `TestInjectCredential_TabWhitespaceKey` — 制表符 key 不产生认证头
- `TestBuildProbeURL_QueryParams` — 含查询参数、Fragment 的地址正确构建探活 URL
- `TestWithTimeout_UpdatesProberClient` — WithTimeout 同步更新 prober 的 HTTP 客户端超时
- `TestWithTimeout_Default` — 默认超时下 hc.client 与 prober 一致
- `TestIsEndpointTimeout` — 6 种错误类型的分类正确性
- `TestUpdateHealthPaths_InvalidatesCache` — 失去显式路径的目标缓存被清除
- `TestUpdateHealthPaths_NoInvalidationForNewTargets` — 新增目标不触发无效缓存清除
- `TestDiscover_CtxExpiry_NotUnreachable` — 预取消 context → nil,false 而非 unreachable
- `TestIsEndpointTimeout_ContextCanceled` — context.Canceled 文档化语义
- `TestProbe_Discover_ContinuesPastEndpointTimeout` — 端点超时后继续尝试下一路径
- `TestProbe_Discover_ConnectionRefusedIsUnreachable` — 连接拒绝 → unreachable=true
- `TestProbe_Discover_AllMethodsTimeout_NotUnreachable` — 全部超时 → unreachable=false

### 📊 Tests

- 主模块测试：**2,090+** 个（含新增 12 个 lb 包测试，0 FAIL）

---

## [v2.24.4] - 2026-04-09

### 🐛 Bug Fixes

#### SQLite 时区字符串比较 Bug（UsageLog 时间过滤全面修复）
- **根因**：SQLite 对 `time.Time` 以 RFC3339 字符串存储，字符串比较是字典序，混合时区格式（`"2026-04-09T12:00:00Z"` vs `"2026-04-09T20:00:00+08:00"`）导致过滤结果完全错误
- **修复 1 — 存储归一化**：`UsageLog.BeforeCreate` GORM hook，所有写入前强制 `CreatedAt.UTC()`，确保数据库中全部为 UTC 格式
- **修复 2 — 查询归一化**：`usage_repo.go` 新增 `toUTC()` helper，所有带时间范围的方法（`Query`、`SumTokens`、`GlobalSumTokens`、`UserStats`、`ExportLogs`、`SumCostUSD`、`DailyTokens`、`DailyCost`、`DeleteBefore`）入口处统一转 UTC
- **修复 3 — 测试数据归一化**：`test/e2e/sproxy_e2e_test.go` 和 `f10_features_e2e_test.go` 种子数据改用 `time.Now().UTC()`

#### reportgen 静默错误丢弃修复
- **`generator.go`**：原所有查询调用均 `_, _ =`，查询失败无任何日志；改为新增 `warnQueryErr()` 辅助函数，失败时输出 `WARNING: <QueryName> failed: <err>` 到 stderr
- **`queries.go`**：`loadMaps()` 中 `Scan()` 错误被静默丢弃；改为 `fmt.Fprintf(os.Stderr, "WARNING: loadMaps scan ...")` 并 `continue`
- **`integration_test.go`**：`sql.Open()`、`NewQuerier()` 错误均被 `_, _` 丢弃；改为 `t.Fatalf` 处理；schema 循环改用现有 `mustExec()` helper

### 🧪 测试覆盖率提升

#### reportgen 新增 26 个测试（`queries_extra_test.go`）
原 39 个查询函数中仅 2 个（`QueryKPI`、`QueryModelDistribution`）有双驱动测试，36 个零覆盖。
新增文件覆盖：
- `QueryTopUsers` — 按 token 数量排名前 N 用户
- `QueryGroupComparison` — 分组 token 汇总对比
- `QueryStreamingRatio` — 流式 vs 非流式请求比例
- `QueryDailyTrend` — 每日 token + 费用趋势
- `QueryEngagement` — 用户活跃度（活跃用户数）
- `QueryQuotaUsage` — 配额使用率查询
- `QueryGroupTokenDistribution` — 分组 token 分布
- `QueryModelTokenBoxPlots` — 模型 token 箱线图数据
- `QuerySourceNodeDist` — 请求来源节点分布
- `QueryUpstreamStats` — 上游节点统计
- `QueryStatusCodeDist` — HTTP 状态码分布
- `QueryPeakRPM` — 峰值请求速率（RPM）
- `QuerySlowRequests` — 慢请求列表
- `QueryErrorRequests` — 错误请求列表

### 📊 Tests
- 主模块测试：**2,078** 个（所有 25 个包，0 FAIL）
- reportgen 测试：**43** 个（含新增 queries_extra_test.go + integration_test.go）
- 总计：**2,121** 个测试，全部通过

---

## [v2.24.3] - 2026-04-08

### 🐛 Bug Fixes

#### Issue #6: 多 API Key 共用同一 URL（正式关闭）
- **根因修复**：`generateID()` 从 `time.Now().UnixNano()` base36 改为 `uuid.NewString()`
  - 原实现在同一纳秒内创建多个 target 时产生相同 ID，触发 `UNIQUE constraint failed: llm_targets.id`
  - 复合唯一约束 `(url, api_key_id)` 现可正常工作：同一 URL 配多个不同 API Key 完全可用

#### 复合约束一致性（举一反三全树收敛）
- **#28/#35**: `GetByGroupID()` → `ListByGroupID()`：修复一对多关系被建模为一对一的问题
- **#30**: `GetDefault()` → `ListDefaults()`：支持多个全局默认目标集
- **#31/#36**: `FindForUser()` 改用防御性 `Find()` + slice 长度检查，记录数据完整性违规
- **#32**: `FindByProviderAndValue()` 改用防御性 `Find()`，多结果时返回明确错误

### ✨ reportgen 工具增强

#### 新功能：命令行 LLM 参数
- **参数新增**：`-llm-url`、`-llm-key`、`-llm-model` 三个新参数
  - `-llm-url`: LLM 端点 URL（如 `http://localhost:9000`）
  - `-llm-key`: LLM API Key（Bearer token）
  - `-llm-model`: LLM 模型名（默认 `gpt-4o-mini`）
- **优先级**：若指定 `-llm-url` 和 `-llm-key`，优先使用；否则从数据库查询配置
- **使用场景**：无需修改数据库，直接指定 LLM 端点，便于本地开发和临时配置

#### 新功能：API 兼容性支持
- 验证并优化 LLM API 调用路径
  - OpenAI 兼容的端点使用 `/v1/chat/completions` API
  - Anthropic native 端点使用 `/v1/messages` API
- 正确的请求头处理：OpenAI (`Authorization: Bearer`) vs Anthropic (`x-api-key`)

#### 可靠性改进：全面容错机制
- **数据库查询容错**：查询失败时自动跳过，继续处理其他操作
- **LLM 连接容错**：
  - 连接失败或网络超时 → 降级为纯规则分析，仍可生成有意义的报告
  - HTTP 错误（如 429 限流）→ 区分识别，提供针对性日志提示
- **LLM 调用保护**：Panic 恢复机制，异常不影响主流程
- **模板容错**：模板文件缺失 → 自动使用内置最小模板渲染，确保报告可用性
- **数据容错**：无数据时段自动检测并生成提示洞察（"📭 暂无数据"）
- **错误日志优化**：区分不同故障类型，提供有针对性的修复建议

#### 版本兼容性
- 完整支持 v2.15.0+ 数据库架构，自动适配不同版本
  - 所有查询仅使用 v2.15.0 时已存在的列
  - 不依赖 v2.20.0+ 新增表（GroupTargetSet 等）

### 🧪 Tests
- 新增 2077 个测试用例覆盖（较 v2.24.2 新增约 50 个）
- 新增并发竞态测试（10 goroutine 并发创建、2 goroutine 并发绑定）
- 新增 NULL 值处理回归测试（验证复合约束 NULL != NULL 的 SQLite 行为）
- 新增 Issue #6 回归测试：同 URL 不同 Key 均可成功创建

---

## [v2.24.2] - 2026-04-07

### ✨ New Features

#### Reportgen PostgreSQL Support
- **数据库支持扩展**: reportgen 工具现已支持 SQLite 和 PostgreSQL 两种数据库
- **灵活的连接方式**:
  - SQLite: `-db <path>` (原有方式，保持兼容)
  - PostgreSQL DSN: `-pg-dsn "postgres://user:pass@host:5432/dbname"`
  - PostgreSQL 独立字段: `-pg-host -pg-port -pg-user -pg-password -pg-dbname -pg-sslmode`
- **数据库抽象层**: 实现 `Querier` 结构体自动处理 SQL 方言差异
  - `rebind()`: 自动转换占位符 `?` → `$1, $2, ...` (PostgreSQL)
  - `sqlDate()`, `sqlHour()`, `sqlDow()`, `sqlYearMonth()` 等辅助函数处理日期/时间函数差异
  - SQLite 特定优化: WAL 模式启用
- **完全向后兼容**: 现有 SQLite 工作流无需任何改动

### 📚 Documentation
- 更新 reportgen README 文档，补充 PostgreSQL 使用示例和参数说明
- 新增 PostgreSQL 连接配置详解和常见问题排查

---

## [v2.24.1] - 2026-04-05

### 🔧 CI/CD
- **Reportgen 发布自动化**: GitHub Release 流水线新增 reportgen 工具的交叉编译步骤，推送 `v*` tag 后自动构建并发布以下平台的预编译二进制：
  - `reportgen-<version>-linux-amd64.tar.gz`
  - `reportgen-<version>-linux-arm64.tar.gz`
  - `reportgen-<version>-darwin-amd64.tar.gz`
  - `reportgen-<version>-darwin-arm64.tar.gz`
  - `reportgen-<version>-windows-amd64.zip`
  - `reportgen-<version>-windows-arm64.zip`
- 所有产物纳入统一 `SHA256SUMS.txt` 校验文件

### 📚 Documentation
- 更新 CHANGELOG、Release Notes、测试报告、验收报告、API 手册、reportgen 使用手册至 v2.24.1

---

## [v2.24.0] - 2026-04-04

### ✨ New Features

#### Model-Aware Routing (F1/F2/F3)

**F1 — Config-as-Seed（配置即种子）**
- 配置文件中的 LLM target 在首次启动时自动写入数据库（种子化）
- 数据库中已存在的记录（WebUI 修改过的）不会被覆盖
- `LLMTargetRepo` 新增 `Seed()` 方法，带存在性检查，防止重复写入

**F2 — Per-Target Supported Models（目标级模型声明）**
- 每个 LLM target 可通过 `supported_models` 字段声明支持的模型列表
- 支持精确匹配、前缀通配（`claude-3-*`）、全通配（`*`）三种模式
- 空列表 = 接受所有模型（向后兼容）
- 新增 `auto_model` 字段：当请求模型不在支持列表时自动替换为指定模型
- 降级策略：`auto_model` > `supported_models[0]` > 透传（空字符串）
- 两级 Fail-Open 策略：
  - Level 1：按 Provider 过滤，若结果为空则使用所有健康 target
  - Level 2：按模型过滤，若结果为空则使用所有健康 target（Fail-Open）

**F3 — REST API & CLI 支持**
- `POST /api/admin/llm/targets` 和 `PUT /api/admin/llm/targets/{url}` 新增 `supported_models`、`auto_model` 字段
- CLI `sproxy admin llm target add/update` 新增 `--supported-models`、`--auto-model` 参数

---

## [v2.23.0] - 2026-04-04

### 🐛 Bug Fixes

#### Issue #2: API Key 号池共享修复
- **问题**：同一 provider（如 `openai`）只能存储一个 API Key，导致多 Key 号池共享失效
- **根因**：`resolveAPIKeyID` 仅按 `provider` 字段唯一化，同类型多 Key 互相覆盖
- **修复**：UNIQUE 约束从 `(provider)` 改为 `(provider, encrypted_value)`，支持同 provider 多 Key 共存
- **影响**：百炼、火山引擎等 OpenAI 兼容 provider 现在可配置多个不同 API Key，实现真正的号池共享
- **向后兼容**：现有 DB 记录自动过渡，无需手动迁移

#### Issue #3: `admin.key_encryption_key` 文档与行为一致性修复
- **问题**：文档将 `admin.key_encryption_key` 标记为可选，但使用 `admin apikey` 命令时实为必填
- **修复**：UPGRADE.md 和 config/README.md 明确标注「使用 API Key 管理功能时必填」
- **改进**：错误消息更具体，提示用户配置该字段

#### Issue #4: 健康检查支持大厂 API 认证
- **问题**：Anthropic、OpenAI 等没有 `/health` 端点的 LLM 提供商，健康检查因缺少认证头而失败（401）
- **解决方案**：实现 provider 感知的认证注入，使用推理 API 替代 `/health` 端点
- **支持的 Provider**：
  - Anthropic Claude：注入 `x-api-key` + `anthropic-version` 头
  - OpenAI / Codex：注入 `Authorization: Bearer` 头
  - 阿里云百炼（DashScope）：Bearer token
  - 火山引擎（Ark）：Bearer token
  - 华为云 MaaS：框架就绪，后续支持 AKSK 签名
  - vLLM / sglang：向后兼容，无需认证
- **新增**：`TargetCredential` 结构体、`WithCredentials` 选项、`injectAuth()` 方法
- **可观测性**：DEBUG 日志追踪认证注入，INFO 日志追踪 credential 更新

### 🔧 Data Race Fix
- **问题**：`HealthChecker` 测试在 CI 中间歇性出现 data race（Go `-race` 检测）
- **根因**：`WaitGroup` 未追踪主循环 goroutine 本身（`loop()`），仅追踪子任务
- **修复**：在 `Start()` 中添加 `wg.Add(1)`，在 `loop()` 开头添加 `defer wg.Done()`
- **教训**：WaitGroup 必须追踪所有长生命周期 goroutine（主循环 + 子任务），详见 `docs/GO_CONCURRENCY_TEACHING_MATERIAL.md`

### 📚 Documentation
- 新增 `docs/GO_CONCURRENCY_TEACHING_MATERIAL.md`：Go 并发编程教材（含 Mermaid 流程图、WaitGroup 模式、GitHub 工作流）
- 新增 `docs/CONCURRENCY_GUIDELINES.md`：并发编程规范与检查清单
- 更新 `CLAUDE.md`：新增并发测试必须遵守的规范章节

---

## [v2.22.0] - 2026-03-28

### ✨ New Features

#### WebUI Expansion - Phase 1: Group-Target Set Management
- **Target Set Management UI**: Create, update, delete Group-Target Sets with full member management
  - Dual-panel layout for viewing target set list and details
  - Add/remove/update members with inline weight editing
  - Automatic group binding and strategy configuration
  - Full audit logging of all target set operations
  - Member permissions validation (read-only for Worker nodes)

#### WebUI Expansion - Phase 2: Alert Management Enhancement
- **Alert Management Dashboard** with 3 tabs:
  - **Live Tab**: Real-time event streaming with level filtering (error/warn/all)
  - **Active Tab**: Active alerts with batch resolution capability
    - Severity statistics cards (Critical/Error/Warning)
    - Single and batch alert resolution with audit tracking
  - **History Tab**: 90-day alert history with advanced filtering
    - Time range selection (7/30/90 days)
    - Level and source filtering
    - Pagination support (50 items per page)

#### WebUI Expansion - Phase 3: Quick Operations Panel
- **Dashboard Quick Operations Section** on overview page
  - LLM Target Status card: health count, active alerts, target set count
  - System Alerts card: unresolved alert statistics and severity distribution
  - Users/Groups card: active user count, total groups, new users today
  - Async data loading from existing APIs (non-blocking)
  - Quick navigation links to management pages

### 🐛 Bug Fixes

- **Critical**: Fixed template scope issue in target set details panel where `$.SelectedSetID` was incorrectly accessed
- **Critical**: Fixed member delete/update routes to use POST form fields instead of URL path segments (prevents 404 errors)
- **Critical**: Fixed unencoded error messages in redirect URLs that caused malformed HTTP Location headers
- **Important**: Fixed batch alert resolve flash message containing literal space character
- **Important**: Fixed edit target set modal that didn't populate current values
- **Important**: Added ID format validation for target sets (alphanumeric, dash, underscore only)
- **Important**: Fixed redundant time import and custom itoa function

### 🔧 Technical Improvements

- **Code Quality**: All handler implementations follow existing project patterns
  - Middleware chain composition (requireSession + requireWritableNode)
  - Flash message pattern via URL query parameters
  - Audit logging via auditRepo.Create()
  - GORM repository pattern for data access

- **Template Improvements**:
  - Tab-based navigation using URL query parameters (?tab=targetsets)
  - Modal dialog patterns with hidden CSS class toggling
  - Responsive Tailwind CSS grid layouts
  - Named templates for organizing Tab content

- **Data Integrity**:
  - Proper null pointer handling for optional GroupID field
  - N+1 query prevention through batch member loading
  - Cascading delete for target set members
  - Type-safe form field conversions

### 📚 Documentation Updates

- Updated API.md with new dashboard endpoints:
  - `POST /dashboard/llm/targetsets` - Create target set
  - `POST /dashboard/llm/targetsets/{id}/update` - Update target set
  - `POST /dashboard/llm/targetsets/{id}/delete` - Delete target set
  - `POST /dashboard/llm/targetsets/{id}/members` - Add member
  - `POST /dashboard/llm/targetsets/{id}/members/update` - Update member
  - `POST /dashboard/llm/targetsets/{id}/members/delete` - Remove member
  - `POST /dashboard/alerts/resolve` - Resolve single alert
  - `POST /dashboard/alerts/resolve-batch` - Resolve multiple alerts

- Updated manual.md with new UI workflows:
  - Target Set Management workflow
  - Alert Management workflow (live/active/history tabs)
  - Quick Operations panel usage guide

### ✅ Backward Compatibility

- **No Breaking Changes**: All existing APIs and functionality remain unchanged
- **Worker Node Support**: Read-only mode properly enforced for new features
- **Database Schema**: No migrations required; GroupTargetSet and related tables already exist in v2.19+

### 📋 Testing

- All implementations follow existing test patterns:
  - Table-driven test structure
  - In-memory SQLite for integration tests
  - httptest for HTTP handler testing
  - Testify assertions and require patterns
  - Full audit logging verification

### 🎯 Known Limitations

- Alert resolution handlers currently log the action but don't modify in-memory event state (future enhancement)
- LLM target health status requires separate API integration (placeholder in quick ops panel)
- Quick operations panel uses cached data (5-minute TTL for user stats)

### 🚀 Deployment Notes

- No database migrations required
- No configuration changes needed
- Fully backward compatible with v2.19.x deployments
- All new features are optional (repo dependencies check for nil before use)
- Worker nodes automatically enforced read-only mode for new features

---

## [v2.19.0] - 2026-03-15

(See previous releases for v2.19 and earlier changes)
