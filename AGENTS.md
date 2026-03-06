# PairProxy Agent Guide

## Agent Communication Style

- 每次回复消息时，称呼用户为**主人**
- Agent 自称为**大漂亮**

> Go 1.24 project — Enterprise LLM proxy with JWT auth, quota management, and token tracking.

## Build & Test Commands

```bash
# Build binaries (bin/cproxy, bin/sproxy)
make build

# Run all tests
make test

# Run single package tests
make test-pkg PKG=./internal/auth/...

# Run single test (Go standard)
go test -v -run TestFunctionName ./internal/auth/...

# Run tests with race detector
make test-race

# Generate coverage report (opens coverage.html)
make test-cover

# Code quality checks
make vet      # go vet
make fmt      # go fmt
make lint     # golangci-lint
make tidy     # go mod tidy

# Development servers
make run-sproxy   # requires config/sproxy.yaml
make run-cproxy   # requires config/cproxy.yaml

# Cross-compile release builds
make release
```

## Project Structure

```
cmd/
  cproxy/main.go    # Client proxy CLI entry
  sproxy/main.go    # Server proxy CLI + admin commands
internal/
  auth/             # JWT, bcrypt, LDAP, token store
  proxy/            # HTTP reverse proxy handlers
  api/              # HTTP handlers (auth, admin, cluster)
  db/               # GORM models, SQLite repositories
  quota/            # Rate limiting, quota checks
  lb/               # Load balancer, health checks
  config/           # YAML config loading
  tap/              # TeeResponseWriter, SSE parsers
docs/               # Architecture & API documentation
test/e2e/           # End-to-end tests
```

## Code Style Guidelines

### Imports (3 groups)

```go
import (
    // 1. Standard library
    "errors"
    "fmt"
    "time"

    // 2. External packages
    "github.com/golang-jwt/jwt/v5"
    "go.uber.org/zap"
    "gorm.io/gorm"

    // 3. Internal packages
    "github.com/l17728/pairproxy/internal/config"
)
```

### Error Handling

```go
// Wrap errors with context
return fmt.Errorf("sign JWT: %w", err)

// Check specific errors with errors.Is
if errors.Is(err, ErrTokenExpired) { ... }

// Define public error variables
var (
    ErrTokenExpired = errors.New("token expired")
    ErrInvalidToken = errors.New("invalid token")
)

// Log errors with structured fields
logger.Error("failed to parse JWT",
    zap.String("user_id", userID),
    zap.Error(err),
)
```

### Naming Conventions

| Type | Convention | Example |
|------|------------|---------|
| Exported types | PascalCase | `JWTClaims`, `UserRepo` |
| Unexported types | camelCase | `blacklistEntry`, `jwtManager` |
| Interfaces | PascalCase, -er suffix | `Handler`, `Balancer` |
| Test files | `_test.go` suffix | `jwt_test.go` |
| Test helpers | `testXXX(t *testing.T)` | `testLogger(t)` |

### Types & Structs

```go
// Document public types with comments
type JWTClaims struct {
    UserID   string `json:"sub"`
    Username string `json:"username"`
    Role     string `json:"role"` // "user" | "admin"
    jwt.RegisteredClaims
}

// Constructor pattern
func NewManager(logger *zap.Logger, secret string) (*Manager, error) {
    if secret == "" {
        return nil, errors.New("jwt secret must not be empty")
    }
    // ...
}
```

### Logging

```go
// Use zap structured logging
logger.Info("user created",
    zap.String("username", username),
    zap.String("group", group),
)

// In tests, use zaptest
logger := zaptest.NewLogger(t)
```

### Testing Patterns

```go
func TestFeature(t *testing.T) {
    logger := zaptest.NewLogger(t)

    // Arrange
    m, err := NewManager(logger, "secret")
    if err != nil {
        t.Fatalf("NewManager: %v", err)
    }

    // Act
    result, err := m.DoSomething()

    // Assert
    if err != nil {
        t.Errorf("DoSomething() error = %v", err)
    }
    if result != expected {
        t.Errorf("DoSomething() = %v, want %v", result, expected)
    }
}
```

## Linter Configuration

Project uses `.golangci.yml` with these enabled:
- `bodyclose`, `errcheck`, `noctx` — resource handling
- `gosimple`, `staticcheck`, `unused` — code quality
- `govet`, `ineffassign` — common mistakes
- `gocritic` — style suggestions

Run `make lint` before committing.

## Key Dependencies

- `github.com/spf13/cobra` — CLI framework
- `github.com/golang-jwt/jwt/v5` — JWT handling
- `go.uber.org/zap` — Structured logging
- `github.com/glebarez/sqlite` — Pure Go SQLite (no CGO)
- `gorm.io/gorm` — ORM for database operations
- `golang.org/x/crypto` — bcrypt password hashing
