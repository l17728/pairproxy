# PairProxy — 实现任务清单（TODO）

> **约定**：每个 Task 对应一个独立的 AI coding session，完成后需通过验收标准。
> **依赖**：`Deps` 列出必须先完成的 Task ID。

---

## Phase 1 — 核心代理（最小可运行）

### Task-01：初始化项目结构
- **文件**：`go.mod`、`go.sum`、目录骨架
- **内容**：
  - `go mod init github.com/yourorg/pairproxy`
  - 引入依赖（见下方依赖清单）
  - 创建 `cmd/cproxy/`、`cmd/sproxy/`、`internal/` 各子目录的占位 `.go` 文件
- **依赖清单**：
  ```
  github.com/spf13/cobra          # CLI 框架
  github.com/spf13/viper          # YAML 配置 + 环境变量
  github.com/golang-jwt/jwt/v5    # JWT
  golang.org/x/crypto             # bcrypt
  gorm.io/gorm                    # ORM
  github.com/glebarez/sqlite      # 纯 Go SQLite 驱动（无 CGO，跨平台）
  github.com/google/uuid          # UUID 生成
  go.uber.org/zap                 # 结构化日志
  ```
- **验收**：`go build ./...` 通过，`go vet ./...` 无警告

---

### Task-02：配置包
- **文件**：`internal/config/config.go`、`internal/config/loader.go`
- **内容**：
  - 定义 `CProxyConfig` 和 `SProxyConfig` 完整结构体（参考 plan.md 第七节）
  - `LoadCProxyConfig(path string) (*CProxyConfig, error)`：读取 YAML，支持 `${ENV_VAR}` 替换
  - `LoadSProxyConfig(path string) (*SProxyConfig, error)`：同上
  - 跨平台默认配置路径：`os.UserConfigDir() + "/pairproxy/cproxy.yaml"`
- **Deps**：Task-01
- **验收**：
  - `TestLoadCProxyConfig`：从 YAML 文件加载配置，验证字段值
  - `TestLoadSProxyConfig`：同上
  - `TestEnvVarSubstitution`：`${ANTHROPIC_API_KEY}` 被正确替换

---

### Task-03：认证包——密码与 JWT
- **文件**：`internal/auth/password.go`、`internal/auth/jwt.go`、`internal/auth/blacklist.go`
- **内容**：
  - `password.go`：`HashPassword(plain string) (string, error)`、`VerifyPassword(hash, plain string) bool`（bcrypt cost=12）
  - `jwt.go`：`SignToken(claims JWTClaims, secret string, ttl time.Duration) (string, error)`、`ParseToken(tokenStr, secret string) (*JWTClaims, error)`
  - `JWTClaims`：包含 `UserID`、`Username`、`GroupID`、`Role`、`JTI`（随机 UUID）
  - `blacklist.go`：内存黑名单 `Blacklist`（`sync.Map`），`Add(jti string, expiry time.Time)`、`IsBlocked(jti string) bool`、后台定期清理过期条目
- **Deps**：Task-01
- **验收**：
  - `TestPasswordRoundTrip`：hash → verify 返回 true
  - `TestPasswordWrongInput`：错误密码返回 false
  - `TestJWTSignAndParse`：签发后解析，字段一致
  - `TestJWTExpired`：TTL=1ms，sleep 后解析返回过期错误
  - `TestBlacklist`：添加 JTI 后 IsBlocked 返回 true，过期后自动清理

---

### Task-04：本地 Token 存储（c-proxy 用）
- **文件**：`internal/auth/token_store.go`
- **内容**：
  - `TokenFile` 结构体：`AccessToken`、`RefreshToken`、`ExpiresAt`、`ServerAddr`
  - `Load(dir string) (*TokenFile, error)`：从 `<dir>/token.json` 读取
  - `Save(dir string, tf *TokenFile) error`：写入文件，设置权限 0600（Windows 忽略权限错误）
  - `DefaultTokenDir() string`：返回 `os.UserConfigDir() + "/pairproxy"`（跨平台）
  - `IsAccessTokenValid(tf *TokenFile) bool`：检查未过期（提前 30min 视为过期）
- **Deps**：Task-01
- **验收**：
  - `TestTokenStoreRoundTrip`：Save 后 Load，字段一致
  - `TestTokenExpiry`：IsAccessTokenValid 在不同时间返回正确结果

---

### Task-05：数据库包——连接与迁移
- **文件**：`internal/db/db.go`、`internal/db/models.go`、`internal/db/migrate.go`
- **内容**：
  - `Open(path string) (*gorm.DB, error)`：使用 `github.com/glebarez/sqlite`，设置 WAL 模式、`busy_timeout=5000`、连接池（max open=1 writer + 多 reader）
  - 所有 GORM 模型定义（参考 plan.md 第五节）：`User`、`Group`、`RefreshToken`、`UsageLog`（含 `Synced` 字段）、`Peer`（sp-1 专用）
  - `AutoMigrate(db *gorm.DB) error`：执行 GORM AutoMigrate + 手动创建索引
- **Deps**：Task-01
- **验收**：
  - `TestDBOpen`：打开内存数据库（`:memory:`）不报错
  - `TestAutoMigrate`：迁移后所有表和索引存在
  - `TestWALMode`：PRAGMA journal_mode 返回 "wal"

---

### Task-06：用户/分组仓库
- **文件**：`internal/db/user_repo.go`
- **内容**：
  - `UserRepo` 接口实现（参考 spec/interfaces.md）
  - `CreateUser`、`GetUserByUsername`、`SetActive`、`UpdateLastLogin`
  - `GroupRepo` 接口实现：`CreateGroup`、`GetGroupByID`、`SetQuota`
- **Deps**：Task-05
- **验收**：
  - `TestCreateAndGetUser`：创建后能查到，密码 hash 存储
  - `TestDuplicateUsername`：第二次创建同名用户返回错误
  - `TestDisableUser`：SetActive(false) 后查询 IsActive=false

---

### Task-07：用量仓库（异步批量写）
- **文件**：`internal/db/usage_repo.go`
- **内容**：
  - `UsageWriter`：内部 channel（buffer=500），后台单 goroutine 每 5s 或积累 200 条批量 INSERT
  - `Record(r UsageRecord)`：非阻塞发送到 channel（满则丢弃并记录警告日志）
  - `Flush()`：强制立即写入（用于 graceful shutdown）
  - `QueryUsage(filter UsageFilter) ([]UsageLog, error)`：支持按 user_id、日期范围过滤
  - `SumTokens(userID string, from, to time.Time) (input, output int64, err error)`
- **Deps**：Task-05
- **验收**：
  - `TestUsageBatchWrite`：发送 1000 条记录，Flush 后 DB 中全部存在
  - `TestUsageIDempotent`：相同 request_id 写两次，DB 中只有一条
  - `TestQueryUsage`：按用户和日期过滤返回正确子集

---

### Task-08：s-proxy 核心处理器
- **文件**：`internal/proxy/sproxy.go`、`internal/proxy/middleware.go`
- **内容**：
  - `RequestIDMiddleware`：生成 UUID 写入 context 和响应头 `X-Request-ID`
  - `AuthMiddleware(jwtMgr JWTManager)`：验证 `X-PairProxy-Auth`，提取 claims 写入 context；sp-1 角色才做配额检查（后续 Task 补充）
  - `RecoveryMiddleware`：panic 恢复，返回 500
  - `SProxyHandler`：读取 context 中 claims → 删除 `X-PairProxy-Auth` → 注入真实 `Authorization` → `httputil.ReverseProxy` 转发到 LLM
  - 保留所有原始请求头（除被替换的两个）、保留 `anthropic-version` 等头
- **Deps**：Task-03、Task-07
- **验收**：
  - `TestAuthMiddlewareValidJWT`：有效 JWT → 请求通过，claims 在 context 中
  - `TestAuthMiddlewareNoHeader`：无头 → 401
  - `TestAuthMiddlewareExpired`：过期 JWT → 401
  - `TestAuthMiddlewareBlacklisted`：黑名单 JTI → 401
  - `TestHeaderReplacement`：转发到 mock LLM，检查 Authorization 头为真实 key，X-PairProxy-Auth 不存在

---

### Task-09：c-proxy 核心处理器
- **文件**：`internal/proxy/cproxy.go`
- **内容**：
  - `CProxyHandler`：接收请求 → 检查本地 JWT（调用 TokenStore）→ 删除原始 `Authorization` → 注入 `X-PairProxy-Auth: <access_token>` → 通过 Balancer 选目标 s-proxy → `httputil.ReverseProxy` 转发
  - 无有效 token → 返回 401，body=`{"error":"not_authenticated","hint":"run 'cproxy login' first"}`
  - 读取响应中的 `X-Routing-Version`/`X-Routing-Update` 并更新路由表（Task-17 完善）
  - 保留 SSE streaming 所需的 `Flush()` 支持
- **Deps**：Task-03、Task-04
- **验收**：
  - `TestCProxyInjectsJWT`：有效 token → 请求到 mock s-proxy，X-PairProxy-Auth 存在，Authorization 不存在
  - `TestCProxyNoToken`：无 token 文件 → 返回 401 含提示信息
  - `TestCProxyPreservesSSE`：streaming 响应完整转发，无截断

---

### Task-10：Auth HTTP 处理器
- **文件**：`internal/api/auth_handler.go`
- **内容**：
  - `POST /auth/login`：验证 username/password → 签发 access_token + refresh_token（写入 refresh_tokens 表）→ 返回 JSON
  - `POST /auth/refresh`：验证 refresh_token（查 DB 是否 revoked）→ 签发新 access_token
  - `POST /auth/logout`：将当前 access_token JTI 加入黑名单，删除 refresh_token
- **Deps**：Task-03、Task-06
- **验收**：
  - `TestLoginSuccess`：正确凭据 → 200，返回两个 token
  - `TestLoginWrongPassword`：→ 401
  - `TestLoginUnknownUser`：→ 401（不暴露用户是否存在）
  - `TestRefreshSuccess`：有效 refresh_token → 新 access_token
  - `TestRefreshRevoked`：已撤销 refresh_token → 401
  - `TestLogout`：logout 后用旧 access_token 请求 → 401

---

### Task-11：c-proxy CLI 入口
- **文件**：`cmd/cproxy/main.go`
- **内容**（cobra 子命令）：
  - `cproxy login --server <url>`：提示输入 username/password → 调用 `/auth/login` → 保存 token.json
  - `cproxy start [--config <path>]`：加载配置，启动 HTTP 监听，打印监听地址和 token 有效期
  - `cproxy status`：显示当前 token 状态、路由表、各 s-proxy 健康状态
  - `cproxy logout`：删除本地 token.json，调用 `/auth/logout`
- **Deps**：Task-04、Task-09、Task-10
- **验收**：`cproxy --help`、`cproxy login --help` 显示正确帮助信息

---

### Task-12：s-proxy CLI 入口
- **文件**：`cmd/sproxy/main.go`
- **内容**（cobra 子命令）：
  - `sproxy start [--config <path>]`：加载配置，启动 HTTP 监听，打印角色（primary/worker）
  - `sproxy admin ...`：占位，后续 Task 补充
- **Deps**：Task-08、Task-10
- **验收**：`sproxy --help` 显示正确帮助信息；`sproxy start` 能响应 `GET /health` 返回 200

---

## Phase 2 — Token 统计

### Task-13：Anthropic SSE 解析器
- **文件**：`internal/tap/anthropic_parser.go`
- **内容**：
  - `AnthropicSSEParser`：逐字节维护行缓冲（`lineBuffer`），识别 SSE 行
  - 解析事件：`message_start`（提取 `message.usage.input_tokens`）、`message_delta`（提取 `usage.output_tokens`）、`message_stop`（触发完成回调）
  - `Feed(data []byte)`：处理任意大小的字节块（可能跨行）
  - `OnComplete(func(inputTokens, outputTokens int))`：回调注册
  - 非 streaming 响应（Content-Type 非 text/event-stream）：`ParseNonStreaming(body []byte) (input, output int, err error)`
- **Deps**：Task-01
- **验收**：
  - `TestSSEParserSingleFeed`：一次性喂入完整 SSE 序列，回调收到正确 token 数
  - `TestSSEParserChunked`：按 1 字节逐块喂入，结果相同（边界鲁棒性）
  - `TestSSEParserNoMessageStop`：未收到 message_stop，回调不触发（不崩溃）
  - `TestNonStreamingParse`：解析普通 JSON 响应体，提取正确 usage

---

### Task-14：TeeResponseWriter
- **文件**：`internal/tap/tee_writer.go`
- **内容**：
  - `TeeResponseWriter`：实现 `http.ResponseWriter` + `http.Flusher`
  - `Write(p []byte)`：同时写入原始 Writer 并 Feed 给 SSEParser
  - `Flush()`：透传给原始 Writer
  - `WriteHeader(statusCode int)`：记录 statusCode，透传
  - 流结束时（通过 context Done 或显式 Close）：触发 UsageSink.Record()（异步，不阻塞）
- **Deps**：Task-13、Task-07
- **验收**：
  - `TestTeeWriterBytesUnchanged`：原始 Writer 收到的字节与输入完全一致
  - `TestTeeWriterParserReceivesAll`：SSEParser 收到所有字节
  - `TestTeeWriterFlushPropagates`：Flush 调用透传到底层 Writer
  - `TestTeeWriterUsageRecorded`：完整 SSE 序列后，UsageSink 收到正确 UsageRecord

---

### Task-15：接入 TeeResponseWriter 到 s-proxy 请求处理
- **文件**：修改 `internal/proxy/sproxy.go`
- **内容**：
  - 在 `SProxyHandler` 中用 `TeeResponseWriter` 包装 `http.ResponseWriter`
  - 将 `UsageWriter.Record` 作为 UsageSink 传入
  - 处理非 streaming 响应：在 `ModifyResponse` 钩子中解析 usage，然后 Record
- **Deps**：Task-14
- **验收**（集成测试）：
  - `TestFullStreamingFlow`：c-proxy → s-proxy → mock LLM（返回标准 Anthropic SSE），最终 usage_logs 中写入正确记录，响应完整到达 c-proxy

---

## Phase 3 — 负载均衡与集群

### Task-16：负载均衡器——加权随机
- **文件**：`internal/lb/balancer.go`、`internal/lb/weighted.go`
- **内容**：
  - `Target` 结构体、`Balancer` 接口（参考 spec/interfaces.md）
  - `WeightedRandomBalancer`：按权重随机选择，跳过 unhealthy 节点
  - `UpdateTargets([]Target)`：原子替换目标列表
  - `MarkHealthy(id)`、`MarkUnhealthy(id)`
- **Deps**：Task-01
- **验收**：
  - `TestWeightedDistribution`：100 次采样，分布符合权重比例（允许 ±10% 误差）
  - `TestSkipUnhealthy`：唯一 healthy 节点被持续选中
  - `TestAllUnhealthy`：返回 `ErrNoHealthyTarget`

---

### Task-17：健康检查器
- **文件**：`internal/lb/health.go`
- **内容**：
  - `HealthChecker`：后台 goroutine，每 `interval` 秒对所有 target 发 `GET /health`
  - 2xx → MarkHealthy；超时或非 2xx → MarkUnhealthy
  - 被动熔断：请求失败回调（由 proxy 层调用），连续 3 次失败 → 立即 MarkUnhealthy
  - `Start(ctx context.Context)`、`Stop()`
- **Deps**：Task-16
- **验收**：
  - `TestHealthCheckerDetectsDown`：mock 服务器停止后，checker 在下次检查后标记 unhealthy
  - `TestHealthCheckerRecovery`：mock 服务器恢复后，重新标记 healthy
  - `TestPassiveCircuitBreaker`：连续 3 次请求失败回调 → unhealthy，无需等到下次主动检查

---

### Task-18：路由表与磁盘缓存
- **文件**：`internal/cluster/routing.go`
- **内容**：
  - `RoutingTable`：version (int64)、entries ([]RoutingEntry: addr, weight, healthy)
  - `Load(dir string) (*RoutingTable, error)`：从 `routing-cache.json` 读取
  - `Save(dir string, rt *RoutingTable) error`：写入文件
  - `ToBalancerTargets() []Target`：转换为 lb.Target 列表
  - `ParseRoutingHeader(headerVal string) (*RoutingTable, error)`：解析 Base64+JSON
  - `EncodeRoutingHeader(rt *RoutingTable) string`：编码为 Base64+JSON
- **Deps**：Task-16、Task-04
- **验收**：
  - `TestRoutingTablePersistence`：Save 后 Load，version 和 entries 一致
  - `TestRoutingHeaderRoundTrip`：Encode → Parse，结果相同
  - `TestRoutingTableApplyToBalancer`：UpdateTargets 后，Balancer 使用新权重

---

### Task-19：响应头路由更新（c-proxy 侧）
- **文件**：修改 `internal/proxy/cproxy.go`
- **内容**：
  - 在 `ModifyResponse` 钩子中读取 `X-Routing-Version` 和 `X-Routing-Update`
  - 若版本号 > 本地版本：解析 → 更新内存 Balancer → 保存磁盘缓存 → 从响应头删除这两个字段（不暴露给 Claude Code）
  - 版本号 ≤ 本地：忽略（幂等）
- **Deps**：Task-18、Task-09
- **验收**：
  - `TestRoutingUpdatePropagation`：mock s-proxy 返回 version=2 的路由头，c-proxy 更新 Balancer 且从响应中删除该头

---

### Task-20：s-proxy 路由表下发（sp-1 侧）
- **文件**：`internal/cluster/manager.go`，修改 `internal/proxy/sproxy.go`
- **内容**：
  - `ClusterManager`（sp-1 only）：管理路由表版本，`GetCurrentRouting() *RoutingTable`
  - 在每个响应的 `ModifyResponse` 钩子中注入路由头（仅当版本有更新时；或始终注入，由 c-proxy 幂等处理）
  - 注入后清理：确保不重复注入（c-proxy 已处理后无需再次推送，用 "已确认版本" 机制可选）
- **Deps**：Task-18
- **验收**：
  - `TestRoutingHeaderInjected`：sp-1 的响应包含 X-Routing-Version 和 X-Routing-Update

---

### Task-21：Peer 注册（sp-2 自动注册到 sp-1）
- **文件**：`internal/cluster/peer_registry.go`，`internal/api/cluster_handler.go`
- **内容**：
  - `POST /api/internal/register`：sp-1 接收注册请求，写入 `peers` 表，更新路由表 version++
  - sp-2 启动逻辑（在 `cmd/sproxy/main.go`）：若 `cluster.role=worker` 且 `cluster.primary` 已配置，向 primary 发送注册请求；每 60s 发送心跳（同一接口，sp-1 更新 last_seen）
  - `GET /cluster/routing`：返回当前路由表 JSON（c-proxy 兜底轮询用）
  - `GET /health`：返回 `{"status":"ok","role":"primary|worker","active_req":N}`
- **Deps**：Task-05、Task-16、Task-20
- **验收**：
  - `TestPeerRegistration`：sp-2 发注册请求，sp-1 DB 中出现对应 peer 记录，路由表 version 增加
  - `TestPeerHeartbeat`：重复注册更新 last_seen，不重复创建记录
  - `TestClusterRoutingEndpoint`：GET /cluster/routing 返回包含所有 peer 的路由表

---

### Task-22：用量上报（sp-2 → sp-1）
- **文件**：`internal/cluster/reporter.go`，`internal/api/cluster_handler.go`
- **内容**：
  - `UsageReporter`（sp-2 only）：后台 goroutine，每 `report_interval` 秒：
    1. `SELECT * FROM usage_logs WHERE synced=0 LIMIT 200`
    2. `POST http://sp-1/api/internal/usage-report` 发送批量数据
    3. 成功 → `UPDATE usage_logs SET synced=1 WHERE request_id IN (...)`
    4. 失败 → 指数退避重试（10s, 20s, 40s, 最大 300s）
  - `POST /api/internal/usage-report`（sp-1）：批量 `INSERT OR IGNORE`，返回接收数量
- **Deps**：Task-07、Task-21
- **验收**：
  - `TestUsageReporterBatch`：sp-2 有 50 条 unsynced 记录，mock sp-1 接收后，sp-2 本地记录标记 synced=1
  - `TestUsageReportIdempotent`：相同 request_id 上报两次，sp-1 只存一条
  - `TestUsageReportRetry`：sp-1 首次请求失败，第二次成功，最终同步完成

---

## Phase 4 — 企业功能

### Task-23：配额检查中间件（sp-1 only）
- **文件**：`internal/quota/checker.go`、`internal/quota/cache.go`
- **内容**：
  - `QuotaCache`：`sync.Map`，key=userID，value=`{daily_used, monthly_used, expires_at}`，TTL=60s
  - `QuotaChecker.Check(ctx context.Context, userID string) error`：查缓存 → miss 时从 DB 聚合 → 缓存结果 → 超限返回 `ErrQuotaExceeded`
  - 仅在 `sproxy.yaml` 中 `cluster.role=primary` 时，将此中间件加入处理链
  - 超限响应：`HTTP 429`，body=`{"error":"quota_exceeded","limit":N,"used":M,"reset_at":"..."}`
- **Deps**：Task-07、Task-06
- **验收**：
  - `TestQuotaWithinLimit`：用量 < 限额，请求通过
  - `TestQuotaExceededDaily`：today 用量超日限额 → 429
  - `TestQuotaCacheHit`：第二次检查不查 DB（mock DB 验证只调用一次）
  - `TestQuotaNoLimit`：group 无配额设置 → 始终通过

---

### Task-24：JWT 撤销——Admin CLI
- **文件**：`internal/api/admin_handler.go`，修改 `cmd/sproxy/main.go`
- **内容**：
  - `POST /api/admin/token/revoke`（需 admin 鉴权）：将用户所有 refresh_tokens 标记 revoked=1，access_token JTI 加入内存黑名单
  - Admin 鉴权：Basic Auth（admin username + bcrypt password from config），返回短期 admin session token
  - `sproxy admin token revoke <username>`：调用上述 API
- **Deps**：Task-03、Task-06、Task-10
- **验收**：
  - `TestTokenRevokeFlow`：revoke 后，旧 access_token 返回 401，旧 refresh_token 换新 token 失败

---

### Task-25：用户/分组 Admin CLI
- **文件**：修改 `cmd/sproxy/main.go`
- **内容**（全部调用 admin API）：
  - `sproxy admin user add <username> --group <group> [--password <pwd>]`
  - `sproxy admin user list [--group <group>] [--format table|json]`
  - `sproxy admin user disable/enable <username>`
  - `sproxy admin user reset-password <username>`
  - `sproxy admin group add <name> [--daily-limit N] [--monthly-limit N]`
  - `sproxy admin group list`
  - `sproxy admin group set-quota <name> --daily N --monthly N`
  - `sproxy admin peer list`（显示已注册节点、权重、last_seen）
- **Deps**：Task-24
- **验收**：各命令 `--help` 显示正确，`sproxy admin user add test-user --group eng` 在 DB 中创建用户

---

## Phase 5 — Dashboard

### Task-26：统计查询 API
- **文件**：`internal/api/stats_handler.go`
- **内容**：
  - `GET /api/stats/summary`：今日总 tokens、总请求数、活跃用户数
  - `GET /api/stats/users?from=&to=&limit=&offset=`：各用户用量排行
  - `GET /api/stats/usage?user_id=&from=&to=&group_by=day|model`：时间序列/模型分布
  - `GET /api/stats/logs?user_id=&limit=50`：最近请求日志
  - 所有接口返回 JSON，支持 admin session token 鉴权
- **Deps**：Task-07、Task-24
- **验收**：
  - `TestStatsSummary`：插入已知数据，summary 返回正确聚合值
  - `TestStatsUsersRanking`：返回按 total_tokens 降序的用户列表

---

### Task-27：Dashboard Web UI
- **文件**：`internal/dashboard/handler.go`、`internal/dashboard/templates/`（layout/overview/users/groups/stats.html）
- **内容**：
  - Go HTML template，使用 Tailwind CSS（CDN）、Chart.js（CDN），嵌入二进制（`//go:embed`）
  - 页面路由：`/dashboard`（概览）、`/dashboard/users`、`/dashboard/groups`、`/dashboard/stats`
  - Admin session cookie 鉴权，未登录跳转 `/dashboard/login`
  - 概览页：今日 token 用量、活跃用户数、最近 10 条日志
  - 用量页：折线图（按天）、用户排行表
- **Deps**：Task-26
- **验收**：
  - `GET /dashboard` 返回 200，包含 `<html>` 标签
  - 浏览器访问渲染正常（手动验收）

---

## Phase 6 — 完善与打包

### Task-28：告警 Webhook
- **文件**：`internal/cluster/alerter.go`
- **内容**：
  - sp-1 在 `active_req` 超过 `alert_threshold` 时，向 `alert_webhook` POST JSON 消息
  - 去抖：同一阈值事件 10min 内不重复告警
  - Webhook payload：`{"event":"threshold_exceeded","active_req":N,"threshold":M,"node":"sp-1","timestamp":"..."}`
- **Deps**：Task-21
- **验收**：
  - `TestAlertFires`：active_req 超限，mock webhook 收到请求
  - `TestAlertDebounce`：连续超限，10min 内只触发一次

---

### Task-29：跨平台验证与构建脚本
- **文件**：`Makefile`、`.github/workflows/build.yml`（可选）
- **内容**：
  ```makefile
  build-linux:    GOOS=linux   GOARCH=amd64 go build -o dist/cproxy-linux   ./cmd/cproxy
  build-windows:  GOOS=windows GOARCH=amd64 go build -o dist/cproxy.exe     ./cmd/cproxy
  build-darwin:   GOOS=darwin  GOARCH=amd64 go build -o dist/cproxy-darwin  ./cmd/cproxy
  # sproxy 同上
  test:           go test ./... -race -count=1
  lint:           golangci-lint run
  ```
  - 验证所有平台 build 通过（纯 Go 驱动无 CGO，理论上 zero-issue）
- **验收**：`make build-linux build-windows build-darwin` 全部成功，生成 6 个可执行文件

---

### Task-30：集成测试与 E2E
- **文件**：`tests/integration/`
- **内容**：
  - 启动真实 sp-1、mock LLM，运行完整流程：login → cproxy start → 发送 streaming 请求 → 验证 usage_logs
  - sp-2 注册流程：启动 sp-2 → 验证 sp-1 路由表更新 → c-proxy 接收到新路由
  - 配额超限流程：设置低配额 → 发请求 → 验证 429
- **Deps**：所有 Phase 1-5 Task
- **验收**：`go test ./tests/integration/ -tags=integration` 通过
