# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Quick Commands

### Build & Test
```bash
make build              # Build cproxy and sproxy to bin/
make build-dev         # Build all binaries (including mockllm/mockagent) to release/
make release           # Cross-platform release packages to dist/
make test              # Run all tests
make test-race         # Run with race detector
make test-cover        # Generate coverage.html report
make test-pkg PKG=./internal/quota/...  # Test specific package
```

### Code Quality
```bash
make fmt               # Format with gofmt/goimports
make vet               # Run go vet
make lint              # Run golangci-lint
make tidy              # Update go.mod/go.sum
```

### Development
```bash
make run-sproxy        # Run sproxy with example config
make run-cproxy        # Run cproxy with example config
make bcrypt-hash       # Generate admin password hash
```

## Architecture Overview

**PairProxy** is an enterprise-grade LLM API gateway for Claude Code. Two-tier architecture:

```
Claude Code → cproxy (local:8080) → sproxy (server:9000) → Anthropic/OpenAI/Ollama
```

### Core Components

| Component | Purpose |
|-----------|---------|
| **cproxy** | Local proxy: intercepts requests, injects JWT, auto-refreshes tokens, load balances across sproxy instances |
| **sproxy** | Central gateway: JWT auth, quota management, token tracking, load balancing, protocol conversion, web dashboard, clustering |
| **internal/auth** | JWT issuance/refresh (24h access, 7d refresh), bcrypt hashing, LDAP/AD support, sk-pp- API Key generation (v2.15.0+) |
| **internal/quota** | Per-user/group daily/monthly limits, RPM rate limiting, sliding window algorithm, fail-open design |
| **internal/lb** | Weighted random balancer, active/passive health checks, circuit breaker, configurable retry (v2.17.0+) |
| **internal/db** | SQLite (default) or PostgreSQL, GORM ORM, async writes with buffering |
| **internal/proxy** | HTTP handlers, middleware, Anthropic ↔ OpenAI protocol conversion, streaming support |
| **internal/tap** | SSE stream parsing, zero-buffering token counting (input/output) |
| **internal/cluster** | Primary+Worker (SQLite) or Peer Mode (PostgreSQL, v2.14.0+) |
| **internal/router** | Semantic intent-based LLM target routing (v2.18.0+) |
| **internal/corpus** | Training data collection as JSONL (v2.16.0+) |
| **internal/track** | Full conversation recording per user |

### Key Features
- Zero-config client experience (set 2 env vars)
- Real-time token tracking via SSE parsing
- Multi-tenant with quotas, rate limiting, LDAP integration
- Protocol interoperability (Anthropic ↔ OpenAI auto-conversion)
- High availability (clustering, health checks, circuit breakers)
- Enterprise features (audit logs, metrics, webhooks, OpenTelemetry)
- Advanced routing (semantic intent-based, v2.18.0)
- Training data collection (corpus, v2.16.0)

## Code Style & Conventions

### Basics
- **Go version**: 1.24.0
- **Package names**: Lowercase, no underscores
- **Imports**: Standard library → external → internal
- **Comments**: Match existing file style (English or Chinese)

### Naming
- **Types**: PascalCase (`LLMTarget`, `Checker`)
- **Functions/Variables**: camelCase (`checkQuota`, `llmBalancer`)
- **Test functions**: `TestXxx` pattern
- **Error variables**: `ErrXxx` (`ErrNoLLMBinding`)

### Error Handling
- Wrap with context: `fmt.Errorf("context: %w", err)`
- Never ignore errors (add `//nolint:errcheck` with comment if intentional)
- **Fail-open**: Quota/database errors should not block requests (log and bypass)

### Logging (Zap)
- Create package logger: `logger.Named("subsystem")`
- **DEBUG**: Per-request details (disabled in prod)
- **INFO**: Lifecycle events
- **WARN**: Recoverable errors (DB write failure, health check failure)
- **ERROR**: Non-recoverable errors requiring manual intervention
- Always add context: `zap.String("user_id", id), zap.Error(err)`

## Testing Requirements

### Unit Tests
```bash
go test ./...                    # All tests
go test ./internal/quota/...     # Specific package
go test -cover ./...             # Coverage
```

### E2E Tests (3 types required)

**1. httptest automation** (daily development, CI/CD):
```bash
go test ./test/e2e/...
```

**2. Real process integration** (complete chain):
```bash
go test -tags=integration ./test/e2e/...
```

**3. Manual end-to-end** (debugging, stress testing):
```bash
./mockllm.exe --addr :11434 &
./sproxy.exe start --config test-sproxy.yaml &
./cproxy.exe start --config test-cproxy.yaml &
echo -e "testuser\ntestpass123" | ./cproxy.exe login --server http://localhost:9000
./mockagent.exe --url http://localhost:8080 --count 100 --concurrency 10
```

### Standards
- All unit tests must pass
- E2E types 1 & 2 must pass before merge
- New features require corresponding tests
- Run `make test-race` before merging concurrent changes
- No external assert frameworks (use standard `testing` package)

### Test Design Rules (防回归 checklist)
- **Once-set semantics**: 测试"写入后不被覆盖"的逻辑时，后续输入必须携带**不同的值**，相同值无法区分"写一次"和"写多次"
- **Provider symmetry**: 每个 provider 路径（anthropic / openai / ollama）需独立覆盖：正常流、malformed 容错、非流式场景
- **Exported API**: 新增 exported 方法时，同 PR 内必须包含对应单元测试
- **If-err-return 插入**: 在已有条件块前插入 `if err != nil { return }` 后，必须确认原有条件块结构完整，立即 `go build` 验证
- **Goroutine 生命周期**: 凡是测试中启动的 goroutine（含 `hc.Start`、`writer.Start` 等），测试结束前必须 `cancel()` + `Wait()`，否则 goroutine 会在 zaptest logger 失效后继续写入，触发 data race
- **共享变量保护**: HTTP handler goroutine 写、测试主 goroutine 读的变量必须用 mutex 保护；读取前先调用 `srv.Close()` 确保所有 handler 已退出
- **异步断言时序**: 不要在 `Start()` 后立即断言异步副作用（如健康状态变化），应等待最终状态或用 `Eventually` 轮询
- **bodyclose lint**: 测试中 `http.Response` 即使不读 body 也必须 `defer resp.Body.Close()`，否则 `bodyclose` linter 报错
- **gosimple lint**: `if x != nil && len(x) != 0` 应简化为 `if len(x) != 0`，nil slice 的 len 为 0

### Concurrency Testing (v2.22.0+ Critical Requirements)

**WaitGroup Synchronization** — All long-lived goroutines must be tracked:
```go
// ✅ CORRECT: Track main loop AND children
func (hc *HealthChecker) Start(ctx context.Context) {
    hc.wg.Add(1)          // ← Track loop itself
    go hc.loop(ctx)
}

func (hc *HealthChecker) loop(ctx context.Context) {
    defer hc.wg.Done()    // ← Must match Add(1)
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            hc.spawnChildren()  // These also Add(1)
        }
    }
}

func (hc *HealthChecker) Wait() { hc.wg.Wait() }

// In tests:
ctx, cancel := context.WithCancel(context.Background())
hc.Start(ctx)
cancel()      // Signal all goroutines to stop
hc.Wait()     // Wait for all to actually finish (REQUIRED)
```

**Race Condition Debugging** — Correct flow: understand → design → verify:
1. Run with `-race` to identify unsynchronized concurrent access
2. Design ONE structural fix (never use `time.Sleep()` as a fix)
3. Verify with `go test ./internal/lb -race -count=10`

**Test Cleanup Checklist**:
- [ ] Long-lived goroutines use `context.WithCancel()` + `defer cancel()`
- [ ] Before test returns: explicit `cancel()` then `hc.Wait()`
- [ ] Async notifiers use `zap.NewNop()` not `zaptest.NewLogger()`
- [ ] HTTP servers have `defer srv.Close()` before async operations exit
- [ ] All `-race` test runs pass (min 10 iterations with `-count=10`)

**Common mistakes**:
- ❌ Forgetting `wg.Add(1)` for main loop goroutine
- ❌ Using `time.Sleep()` instead of proper synchronization
- ❌ Injecting `zaptest.NewLogger` into notifiers in async contexts
- ❌ Not calling `Wait()` after `cancel()`
- ❌ Testing only once with `-race` (race detection is probabilistic)

See `memory/concurrency_waitgroup_patterns.md` and `memory/concurrency_race_debugging.md` for detailed examples.

## Configuration

### YAML Format
- Snake_case keys, `${ENV_VAR}` for secrets
- Example configs: `config/*.yaml.example`
- Load via: `config.Load(path)` from `internal/config`
- Secrets required: `JWT_SECRET`, API keys, `KEYGEN_SECRET` (v2.15.0+)

### Key Config Sections (sproxy.yaml)
```yaml
server:
  addr: ":9000"
  jwt_secret: "..."

database:
  path: "./pairproxy.db"
  driver: "sqlite"  # or "postgres"

llm:
  max_retries: 2
  recovery_delay: 60s
  targets:
    - url: "https://api.anthropic.com"
      api_key: "sk-ant-..."
      provider: "anthropic"
      weight: 1

dashboard:
  enabled: true
  admin_password: "..."

# v2.16.0+
corpus:
  enabled: false
  output_dir: "./corpus"

# v2.18.0+
semantic_router:
  enabled: false
  classifier_url: "http://localhost:9000"

# v2.15.0+ (required for sk-pp- keys)
auth:
  keygen_secret: "${KEYGEN_SECRET}"
```

## API Conventions

- **REST endpoints**: `/api/admin/*`, `/api/internal/*`, `/api/user/*`
- **Auth headers**: `X-PairProxy-Auth: <jwt>` or `Authorization: Bearer <jwt>`
- **Pagination**: Query params `page` (default 1) and `page_size` (default 100)
- **Error responses**: JSON with `error` and `message` fields
- **Health check**: `GET /health` returns status + uptime + DB connectivity
- **Metrics**: `GET /metrics` (Prometheus format, cache-refreshed every 30s)

## Database (GORM)

- **Drivers**: SQLite (default) or PostgreSQL
- **Migration**: `db.Migrate(logger, gormDB)` on startup
- **Async writes**: `UsageWriter` with buffer + flush interval
- **Cluster modes**:
  - SQLite: Primary + Workers (30s sync window, primary-only writes)
  - PostgreSQL: Peer Mode (v2.14.0+, all nodes equal, shared DB)

## Protocol Support

- **Anthropic**: `/v1/messages` (default)
- **OpenAI**: `/v1/chat/completions`
- **Ollama**: `/v1/chat/completions` (OpenAI-compatible)
- **Auto-conversion**: Anthropic ↔ OpenAI interoperability (Claude CLI → Ollama)

## Common Patterns

### Error Propagation (Fail-Open)
```go
user, err := s.userRepo.GetByID(userID)
if err != nil {
    s.logger.Warn("failed to get user, bypassing", zap.Error(err))
    return nil  // fail-open: don't block request
}
```

### Test Helper
```go
func setupTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    logger := zaptest.NewLogger(t)
    db, err := db.Open(logger, ":memory:")
    if err != nil {
        t.Fatalf("db.Open: %v", err)
    }
    if err := db.Migrate(logger, db); err != nil {
        t.Fatalf("db.Migrate: %v", err)
    }
    return db
}
```

### Health Check Pattern
```go
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if err := h.checkDB(); err != nil {
        http.Error(w, "database unavailable", http.StatusServiceUnavailable)
        return
    }
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

## Admin CLI Reference

For complete CLI command reference, run:
```bash
./sproxy admin help-all
```

This outputs all commands with syntax, flags, examples, and natural language triggers. Key command categories:
- User management: `admin user add/list/disable/enable/reset-password/set-group`
- Group & quota: `admin group add/list/set-quota/delete`
- LLM targets: `admin llm targets/list/bind/unbind/distribute/target add/update/enable/disable/delete`
- Quota status: `admin quota status`
- Semantic routing: `admin semantic-router list/status/add/update/enable/disable/delete` (v2.18.0+)
- Corpus collection: `admin corpus status/enable/disable/list` (v2.16.0+)
- sk-pp- keys: `admin keygen --user <name>` (v2.15.0+)
- Stats & audit: `admin stats/audit/token revoke`
- Database: `admin backup/restore/logs purge/export`
- Validation: `admin validate`

## Version-Specific Features

- **v2.18.0**: Semantic Router (intent-based LLM target routing)
- **v2.16.0**: Corpus Collection (training data collection as JSONL)
- **v2.15.0**: sk-pp- API Key generation (HMAC-SHA256, requires `auth.keygen_secret`)
- **v2.14.0**: PostgreSQL Peer Mode (all nodes equal, shared DB)
- **v2.17.0**: Configurable retry on specific HTTP status codes
- **v2.13.0**: PostgreSQL support

## CI/CD

### CI Workflow (`.github/workflows/ci.yml`)
- Matrix: Go 1.24
- Checks: build, vet, test-race, lint
- Coverage: Upload artifact

### Release Workflow (`.github/workflows/release.yml`)
- Tag: `git tag v1.2.3 && git push origin v1.2.3`
- Auto-builds: Cross-compile 5 platforms (Linux/macOS/Windows × amd64/arm64)
- Docker: Multi-arch image `ghcr.io/l17728/pairproxy`

## 解题复盘机制

每次修复 bug 或经历多轮尝试才解决的问题，完成后必须做一次复盘，将过程沉淀为经验。

**复盘的核心动作**：
1. **记录有效路径**：把最终奏效的解决方案用简洁的步骤写下来，而不是描述走过的弯路
2. **归因根本原因**：追问"为什么会出现这个问题"，而不是止步于"怎么修的"
3. **提炼可复用规律**：把这次的教训抽象成下次可以直接套用的判断原则
4. **更新知识库**：将结论补充到 `CLAUDE.md`（决策原则类）或 `docs/TROUBLESHOOTING.md`（操作排查类），让后来的自己和协作者直接受益

**判断是否需要复盘**：凡是满足以下任一条件的问题，都值得复盘：
- 尝试了两种以上方案才解决
- 问题根因和第一直觉判断不符
- 修复过程中发现了原本不了解的系统行为
- 同类问题在项目中已经出现过一次

**复盘不是写事故报告**，不需要面面俱到，一段话说清楚"踩了什么坑、为什么踩、以后怎么避免"即可。价值在于把一次性的痛苦转化成长期有效的判断力。

## 举一反三原则（Bug 发现即普查）

发现一个 bug，必须同步完成三件事，不得只修当前触发点：

### 1. 溯源同类风险

修复前先问：**同样的根因在项目中还有哪些地方？**

- 搜索相同 API 调用、相同字段定义、相同代码模式
- 列出所有潜在受影响点，逐一确认是否存在同样问题
- 示例：发现 GORM Create 忽略 bool 零值，则搜索所有含 default:true 的 bool 字段，逐一审查其 Create 路径

### 2. 补充覆盖全场景的测试

每个受影响点都需要测试，不仅是当前触发的那个：

- false 路径：验证应为 false 的值确实存储为 false
- true 路径：验证正常路径未被破坏
- 端到端过滤：验证依赖该字段的查询/过滤逻辑结果正确
- 测试命名包含场景描述，便于回归时快速定位

### 3. 沉淀为不可绕过的规则

将结论写入以下位置，防止同类问题再次发生：

-  顶部注释：Bug 模式索引，编号累加
- ：根因分析、修复策略、举一反三表格、强制测试要求
- （本文件）：决策原则，写成可直接执行的规则

### 触发标准

以下情况必须触发举一反三流程：

- 根因是某个 API/框架的隐式行为（如 GORM 零值、context 取消语义）
- 同一模式在代码库中有多处使用
- 问题在测试中未被覆盖，靠手动或偶发才发现
- 已在 LEARNINGS.md 中出现过同类记录

**原则**：一次痛苦只允许发生一次。发现问题的成本已经付出，不把它转化为系统性防护是对这笔成本的浪费。

## Important Notes

- All commands run from project directory (where sproxy.yaml exists)
- Use `--config` flag to specify alternate config path
- Quota units are tokens (Anthropic API input+output sum)
- LLM binding priority: user > group > load balancer
- `make fmt` before commit
- `make test-race` before merging concurrent changes
- Configuration changes reflected in `config/*.yaml.example`
- CLI commands documented in this file and via `./sproxy admin help-all`
- Public APIs require godoc comments
- Breaking changes tracked in `docs/UPGRADE.md`
