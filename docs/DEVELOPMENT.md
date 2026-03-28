# Development Guide

## Prerequisites

- Go 1.21 or later
- Git
- SQLite 3 (usually pre-installed)
- Make (optional, for build automation)
- Docker (optional, for containerized development)

## Environment Setup

### 1. Clone Repository

```bash
git clone https://github.com/l17728/pairproxy.git
cd pairproxy
```

### 2. Install Dependencies

```bash
go mod download
go mod tidy
```

### 3. Verify Installation

```bash
go version
go env
```

## Project Structure

```
pairproxy/
├── cmd/
│   └── pairproxy/          # Main application entry point
├── internal/
│   ├── db/                 # Database layer
│   │   ├── models.go       # Data models
│   │   ├── *_repo.go       # Repository implementations
│   │   └── *_repo_test.go  # Repository tests
│   ├── proxy/              # Proxy logic
│   │   ├── handler.go      # HTTP handlers
│   │   ├── router.go       # Request routing
│   │   └── *_test.go       # Unit tests
│   ├── alert/              # Health monitoring & alerts
│   └── config/             # Configuration management
├── test/
│   ├── integration/        # Integration tests
│   └── e2e/               # End-to-end tests
├── docs/                   # Documentation
├── config/                 # Configuration templates
├── go.mod                  # Go module definition
├── go.sum                  # Go module checksums
├── Makefile               # Build automation
└── README.md              # Project README
```

## Building

### Build Binary

```bash
go build -o pairproxy ./cmd/pairproxy
```

### Build with Version Info

```bash
VERSION=$(git describe --tags --always)
go build -ldflags "-X main.Version=$VERSION" -o pairproxy ./cmd/pairproxy
```

### Using Makefile

```bash
make build          # Build binary
make test           # Run tests
make test-coverage  # Run tests with coverage
make lint           # Run linter
make fmt            # Format code
make clean          # Clean build artifacts
```

## Running Locally

### Development Mode

```bash
# Create development config
cat > cproxy.yaml << EOF
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://localhost:9000"

log:
  level: "debug"
  format: "text"

database:
  path: "./dev.db"

quota:
  enabled: false
EOF

# Run application
go run ./cmd/pairproxy
```

### With Environment Variables

```bash
export CPROXY_LISTEN_PORT=9000
export CPROXY_LOG_LEVEL=debug
go run ./cmd/pairproxy
```

### With Docker

```bash
# Build image
docker build -t pairproxy:dev .

# Run container
docker run -p 8080:8080 \
  -v $(pwd)/cproxy.yaml:/etc/pairproxy/cproxy.yaml \
  -v $(pwd)/dev.db:/var/lib/pairproxy/dev.db \
  pairproxy:dev
```

## Testing

### Run All Tests

```bash
go test ./...
```

### Run Specific Package Tests

```bash
go test ./internal/db -v
go test ./internal/proxy -v
```

### Run Specific Test

```bash
go test ./internal/db -v -run TestGroupTargetSetRepo_Create
```

### Run with Coverage

```bash
go test ./... -cover
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Run Benchmarks

```bash
go test -bench=. -benchmem ./internal/db
```

### Watch Mode (with entr)

```bash
find . -name "*.go" | entr go test ./...
```

## Code Quality

### Format Code

```bash
go fmt ./...
```

### Lint Code

```bash
# Install golangci-lint
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Run linter
golangci-lint run ./...
```

### Static Analysis

```bash
go vet ./...
```

### Security Scanning

```bash
# Install gosec
go install github.com/securego/gosec/v2/cmd/gosec@latest

# Run security scan
gosec ./...
```

## Debugging

### Debug with Delve

```bash
# Install delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug application
dlv debug ./cmd/pairproxy

# In dlv prompt:
# (dlv) break main.main
# (dlv) continue
# (dlv) next
# (dlv) print variable_name
# (dlv) quit
```

### Debug Tests

```bash
dlv test ./internal/db -- -test.run TestName
```

### Print Debugging

```go
import "fmt"

fmt.Printf("Debug: %+v\n", value)
```

### Logging

```go
import "go.uber.org/zap"

logger.Debug("message", zap.String("key", "value"))
logger.Info("message", zap.Int("count", 42))
logger.Error("message", zap.Error(err))
```

## Database Development

### Create Migration

```bash
# Create new migration file
touch internal/db/migrations/001_initial_schema.sql
```

### Run Migrations

```bash
go run ./cmd/pairproxy migrate
```

### Database Shell

```bash
# SQLite
sqlite3 dev.db

# PostgreSQL
psql -U user -d pairproxy
```

### Reset Database

```bash
# SQLite
rm dev.db

# PostgreSQL
dropdb pairproxy
createdb pairproxy
```

## Git Workflow

### Create Feature Branch

```bash
git checkout -b feature/my-feature
```

### Commit Changes

```bash
git add internal/db/models.go
git commit -m "feat: add new model

Description of changes.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>"
```

### Push Changes

```bash
git push -u origin feature/my-feature
```

### Create Pull Request

```bash
gh pr create --title "Add new feature" --body "Description"
```

### Update from Main

```bash
git fetch origin
git rebase origin/main
```

## Common Development Tasks

### Add New Repository

1. Create model in `internal/db/models.go`
2. Create repository in `internal/db/new_repo.go`
3. Add tests in `internal/db/new_repo_test.go`
4. Register in database initialization

### Add New API Endpoint

1. Create handler in `internal/proxy/handler.go`
2. Add route in `internal/proxy/router.go`
3. Add tests in `internal/proxy/handler_test.go`
4. Document in API documentation

### Add New Configuration Option

1. Add field to config struct in `internal/config/config.go`
2. Add environment variable mapping
3. Add validation logic
4. Document in `docs/CONFIGURATION.md`

### Add New Alert Type

1. Define alert type in `internal/alert/types.go`
2. Implement alert handler in `internal/alert/handler.go`
3. Add tests in `internal/alert/handler_test.go`
4. Update alert documentation

## Performance Profiling

### CPU Profiling

```bash
# Run with CPU profile
go test -cpuprofile=cpu.prof ./internal/db
go tool pprof cpu.prof

# In pprof prompt:
# (pprof) top
# (pprof) list FunctionName
# (pprof) web
```

### Memory Profiling

```bash
# Run with memory profile
go test -memprofile=mem.prof ./internal/db
go tool pprof mem.prof
```

### Trace Analysis

```bash
# Run with trace
go test -trace=trace.out ./internal/db
go tool trace trace.out
```

## IDE Setup

### VS Code

1. Install Go extension
2. Install recommended extensions:
   - Go
   - REST Client
   - SQLite

3. Create `.vscode/settings.json`:
```json
{
  "go.lintOnSave": "package",
  "go.lintTool": "golangci-lint",
  "go.lintArgs": ["--fast"],
  "go.useLanguageServer": true,
  "[go]": {
    "editor.formatOnSave": true,
    "editor.codeActionsOnSave": {
      "source.organizeImports": true
    }
  }
}
```

### GoLand / IntelliJ IDEA

1. Open project
2. Configure Go SDK: Settings → Go → Go Modules
3. Enable code inspections: Settings → Editor → Inspections
4. Configure run configurations for tests

### Vim/Neovim

```bash
# Install vim-go
git clone https://github.com/fatih/vim-go.git ~/.vim/pack/plugins/start/vim-go

# Install gopls
go install golang.org/x/tools/gopls@latest
```

## Documentation

### Generate API Documentation

```bash
# Install swag
go install github.com/swaggo/swag/cmd/swag@latest

# Generate docs
swag init -g cmd/pairproxy/main.go
```

### Generate Code Documentation

```bash
# Generate godoc
godoc -http=:6060
# Visit http://localhost:6060/pkg/github.com/l17728/pairproxy/
```

## Troubleshooting Development

### Module Issues

```bash
# Clear module cache
go clean -modcache

# Verify dependencies
go mod verify

# Update dependencies
go get -u ./...
```

### Build Issues

```bash
# Clean build cache
go clean -cache

# Rebuild
go build ./cmd/pairproxy
```

### Test Issues

```bash
# Run with verbose output
go test -v ./...

# Run with race detector
go test -race ./...

# Run with timeout
go test -timeout 30s ./...
```

### Database Issues

```bash
# Check database integrity
sqlite3 dev.db "PRAGMA integrity_check;"

# Rebuild database
rm dev.db
go run ./cmd/pairproxy migrate
```

## Performance Tips

1. **Use connection pooling**: Configure `max_open_conns` appropriately
2. **Index frequently queried fields**: Add database indexes
3. **Cache results**: Use in-memory caching for frequently accessed data
4. **Batch operations**: Group database operations
5. **Use prepared statements**: Reuse query plans
6. **Profile regularly**: Use pprof to identify bottlenecks

## Security Best Practices

1. **Never commit secrets**: Use environment variables
2. **Validate input**: Always validate user input
3. **Use parameterized queries**: Prevent SQL injection
4. **Hash passwords**: Use bcrypt for password hashing
5. **Sanitize output**: Prevent XSS attacks
6. **Use HTTPS**: Always use TLS in production
7. **Keep dependencies updated**: Run `go get -u` regularly

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## Resources

- [Go Documentation](https://golang.org/doc/)
- [GORM Documentation](https://gorm.io/)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [Effective Go](https://golang.org/doc/effective_go)
