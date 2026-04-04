# PairProxy Agent Guide

Go-based LLM proxy service: enterprise rate limiting, token tracking, multi-tenant management for Claude Code and other LLM clients.

## Build & Test Commands

```bash
make build              # Build cproxy + sproxy to bin/
make build-dev          # Build all binaries (incl. mockllm/mockagent) to release/
make test               # Run all tests
make test-race          # Run with race detector (required before merging concurrent changes)
make test-cover         # Generate coverage.html report
make test-pkg PKG=./internal/quota/...  # Run single package tests (with -v)
make fmt                # Format with gofmt/goimports (run before every commit)
make vet                # Run go vet
make lint               # Run golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
make tidy               # Update go.mod/go.sum
```

### Run a single test function
```bash
go test -v -run TestCheckerNoGroup ./internal/quota/
```

## Code Style Guidelines

### Basics
- **Go version**: 1.24.0 (per go.mod)
- **Package names**: Lowercase, no underscores, short and descriptive
- **Imports**: Grouped — standard library, then external, then internal (`github.com/l17728/pairproxy/internal/...`)
- **Comments**: Match existing file style (English or Chinese mixed — check surrounding code)
- **Line length**: No hard limit; keep readable (<120 chars preferred)

### Naming Conventions
- **Types**: PascalCase (`LLMTarget`, `Checker`, `ExceededError`)
- **Functions/Variables**: camelCase (`checkQuota`, `llmBalancer`, `apiKeyResolver`)
- **Test functions**: `TestXxx` pattern, descriptive (`TestCheckerNoGroup`, `TestHealthChecker_Anthropic_Auth`)
- **Error variables**: `ErrXxx` sentinel pattern (`ErrNoLLMBinding`, `ErrBoundTargetUnavailable`)
- **Test helpers**: `setupXxxTest(t *testing.T)` or `newXxxForTest(...)` — always call `t.Helper()`

### Error Handling
- **Wrap with context**: `fmt.Errorf("context: %w", err)`
- **Never ignore errors**: Add `//nolint:errcheck` with comment only if intentional
- **Sentinel errors**: Define as `var ErrXxx = errors.New("...")` at package level
- **Fail-open**: Quota/database errors must NOT block requests — log warning and bypass:
  ```go
  user, err := s.userRepo.GetByID(userID)
  if err != nil {
      s.logger.Warn("failed to get user, bypassing", zap.Error(err))
      return nil // fail-open
  }
  ```

### Logging (Zap)
- **Create package logger**: `logger.Named("subsystem")` (e.g., `logger.Named("quota_checker")`)
- **DEBUG**: Per-request details (token counts, SSE parsing) — disabled in prod
- **INFO**: Lifecycle events (start, shutdown, token reload)
- **WARN**: Recoverable errors (DB write failure, health check failure)
- **ERROR**: Non-recoverable errors requiring manual intervention
- **Always add context fields**: `zap.String("user_id", id), zap.Error(err)`

### Testing
- **Frameworks**: Standard `testing` package + `github.com/stretchr/testify` (assert/require)
- **Test files**: `xxx_test.go` in same package for white-box; `_test` suffix package for black-box
- **Test helpers**: Pattern `setupXxxTest(t *testing.T)` with `t.Helper()`, returning cleanup functions:
  ```go
  func setupQuotaTest(t *testing.T) (*db.UserRepo, ..., context.CancelFunc) {
      t.Helper()
      // ... setup in-memory DB
      return repos..., func() { cancel(); writer.Wait() }
  }
  ```
- **No real network/FS**: Use `httptest.NewServer` for HTTP mocks, `:memory:` for SQLite
- **Goroutine lifecycle**: Every goroutine started in tests must be tracked via `sync.WaitGroup`; call `cancel()` + `Wait()` before test returns
- **Race detection**: Run `make test-race` before merging; use `-count=10` for probabilistic race detection

### Linting (.golangci.yml)
Enabled linters: `bodyclose`, `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`, `noctx`, `gocritic`
- `bodyclose`: Always `defer resp.Body.Close()` on `http.Response` in tests
- `noctx`: HTTP requests must carry context; use `//nolint:noctx` only for proxy forwarding
- Test files exempt from: `errcheck`, `noctx`

### Configuration
- **YAML format**: Snake_case keys, `${ENV_VAR}` expansion for secrets
- **Example configs**: `config/*.yaml.example` — always update when adding config fields
- **Environment variables**: Required for secrets (`JWT_SECRET`, API keys, `KEYGEN_SECRET`)

### API Conventions
- **REST endpoints**: `/api/admin/*`, `/api/internal/*`, `/api/user/*`
- **JWT auth**: `X-PairProxy-Auth` or `Authorization: Bearer <token>`
- **Pagination**: Query params `page` (default 1) and `page_size` (default 100)
- **Error responses**: JSON with `error` and `message` fields

## Architecture

| Layer | Folder | Purpose |
|-------|--------|---------|
| CLI entrypoints | `cmd/cproxy/`, `cmd/sproxy/` | Cobra commands for user-facing binaries |
| Core proxy logic | `internal/proxy/` | HTTP handlers, middleware, protocol conversion |
| Auth & JWT | `internal/auth/` | Token issuance/refresh, bcrypt, sk-pp- keygen |
| Quota management | `internal/quota/` | Per-user/group daily/monthly limits, RPM rate limiting |
| Database | `internal/db/` | GORM models, repositories (User, Group, Usage, LLMTarget) |
| Load balancing | `internal/lb/` | `WeightedRandomBalancer`, health checks, circuit breaker |
| Config | `internal/config/` | YAML loader, validation, env var expansion |
| Dashboard | `internal/dashboard/` | Web UI (Go templates + Tailwind, embedded in binary) |
| Tracking | `internal/track/` | Full conversation recording per user |
| Corpus | `internal/corpus/` | Training data collection (JSONL, v2.16.0+) |
| Semantic router | `internal/router/` | Intent-based LLM target selection (v2.18.0+) |
| API handlers | `internal/api/` | REST endpoints for admin/user/cluster/keygen |
| Cluster | `internal/cluster/` | Primary+Worker (SQLite) or Peer Mode (PostgreSQL) |
| Keygen | `internal/keygen/` | HMAC-SHA256 sk-pp- API Key generation/validation |
| SSE parsing | `internal/tap/` | TeeResponseWriter + Anthropic/OpenAI SSE parsers |

## Key Design Decisions

- **Protocol support**: Anthropic (`/v1/messages`), OpenAI (`/v1/chat/completions`), Ollama — auto-conversion between formats
- **Cluster modes**: SQLite (primary + workers, 30s sync) or PostgreSQL (peer mode, all nodes equal)
- **Direct Proxy**: `sk-pp-` API Keys for headerless access (HMAC-SHA256, 48-char Base62)
- **Version injection**: Binaries embed `Version`, `Commit`, `BuiltAt` via ldflags from `internal/version`

## Pre-commit Checklist

- `make fmt` — format code
- `make vet` — static analysis
- `make test` (or `make test-race` for concurrent changes) — all tests pass
- New features have tests (unit or integration)
- Configuration changes reflected in `config/*.yaml.example`
- Public APIs have godoc comments
