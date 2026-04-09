# Testing Guide

## Overview

This guide covers testing strategies, patterns, and best practices for pairproxy.

## Test Structure

```
test/
├── integration/     # Integration tests
├── e2e/            # End-to-end tests
└── unit/           # Unit tests (co-located with source)

internal/
├── db/
│   ├── models.go
│   ├── *_repo.go
│   └── *_repo_test.go  # Unit tests
├── proxy/
│   ├── handler.go
│   └── handler_test.go
└── ...

tools/reportgen/
├── queries.go
├── integration_test.go    # SQLite + PostgreSQL 双驱动集成测试
└── queries_extra_test.go  # 26 个查询函数覆盖测试（v2.24.4 新增）
```

## Running Tests

### All Tests (主模块)
```bash
go test ./...
```

### reportgen 测试（tools/reportgen）
```bash
cd tools/reportgen
go test ./...
# 输出: ok  github.com/l17728/pairproxy/tools/reportgen  (43 个测试)
```

### 运行特定 reportgen 测试
```bash
cd tools/reportgen
go test -v -run TestQueryTopUsers
go test -v -run TestIntegration_QueryKPI_SQLite
```

### Specific Package
```bash
go test ./internal/db -v
```

### Specific Test
```bash
go test ./internal/db -v -run TestGroupTargetSetRepo_GetAvailableTargetsForGroup
```

### With Coverage
```bash
go test ./... -cover
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Test Categories

### Unit Tests

Located alongside source code with `_test.go` suffix.

**Characteristics:**
- Fast execution
- Isolated from external dependencies
- Test single functions/methods
- Use mocks for dependencies

**Example:**
```go
func TestGroupTargetSetRepo_Create(t *testing.T) {
  testDB := setupTestDB(t)
  repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

  set := &GroupTargetSet{
    ID:       uuid.New().String(),
    Name:     "test-set",
    Strategy: "weighted_random",
  }

  err := repo.Create(set)
  require.NoError(t, err)

  retrieved, err := repo.GetByID(set.ID)
  require.NoError(t, err)
  assert.Equal(t, set.Name, retrieved.Name)
}
```

### Integration Tests

Located in `test/integration/` directory.

**Characteristics:**
- Test multiple components together
- Use real database (in-memory SQLite)
- Test workflows and interactions
- Slower than unit tests

**Example:**
```go
func TestE2EFullChainNonStreaming(t *testing.T) {
  // Setup
  server := setupTestServer(t)
  defer server.Close()

  // Execute
  resp, err := http.Post(server.URL+"/v1/messages", "application/json", body)

  // Assert
  require.NoError(t, err)
  assert.Equal(t, http.StatusOK, resp.StatusCode)
}
```

### End-to-End Tests

Located in `test/e2e/` directory.

**Characteristics:**
- Test complete user workflows
- May use external services
- Longest execution time
- Validate business logic

## Database Testing

### Test Database Setup

```go
func setupTestDB(t *testing.T) *gorm.DB {
  testDB, err := Open(zap.NewNop(), ":memory:")
  require.NoError(t, err)
  require.NoError(t, Migrate(zap.NewNop(), testDB))
  return testDB
}
```

### Key Patterns

#### 1. Boolean Field Testing

**Issue**: GORM's default value handling can interfere with boolean false values.

**Solution**: Use raw SQL for insertion:
```go
// ❌ Wrong - IsActive=false becomes true
repo.AddMember(setID, &GroupTargetSetMember{
  IsActive: false,
})

// ✅ Correct - Use raw SQL
db.Exec(
  "INSERT INTO group_target_set_members (..., is_active, ...) VALUES (..., ?, ...)",
  false,
)
```

#### 2. Fixture Creation

```go
func createTestUser(t *testing.T, repo *UserRepo, groupID string) *User {
  user := &User{
    ID:       uuid.New().String(),
    Username: "testuser",
    GroupID:  groupID,
  }
  require.NoError(t, repo.Create(user))
  return user
}
```

#### 3. Assertion Patterns

```go
// Check existence
assert.NotNil(t, result)

// Check count
assert.Len(t, results, 2)

// Check values
assert.Equal(t, expected, actual)

// Check errors
require.NoError(t, err)
assert.Error(t, err)
```

## Common Test Issues

### Issue: Boolean Fields Not Persisting

**Symptom**: `IsActive=false` is stored as `true`

**Root Cause**: GORM treats false as a zero value and applies the model's default

**Solution**: Use raw SQL or explicit field selection

### Issue: Test Database Not Cleaning Up

**Symptom**: Tests interfere with each other

**Root Cause**: Shared database state

**Solution**: Use in-memory database per test

### Issue: Flaky Tests

**Symptom**: Tests pass sometimes, fail other times

**Root Cause**: Race conditions, timing issues

**Solution**:
- Use `require` for critical assertions
- Avoid sleep/timing dependencies
- Use channels for synchronization

## Test Utilities

### Assertion Libraries

- `testify/assert`: Soft assertions (continue on failure)
- `testify/require`: Hard assertions (stop on failure)

### Logging in Tests

```go
t.Logf("Debug info: %+v", value)
t.Errorf("Error: %v", err)
```

### Cleanup

```go
defer func() {
  // Cleanup code
}()
```

## Mocking Strategies

### Interface Mocking

```go
type MockTargetRepo struct {
  mock.Mock
}

func (m *MockTargetRepo) GetByID(id string) (*Target, error) {
  args := m.Called(id)
  return args.Get(0).(*Target), args.Error(1)
}
```

### HTTP Mocking

```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  w.WriteHeader(http.StatusOK)
  json.NewEncoder(w).Encode(response)
}))
defer server.Close()
```

## Performance Testing

### Benchmarking

```go
func BenchmarkGetAvailableTargets(b *testing.B) {
  repo := setupBenchDB()

  b.ResetTimer()
  for i := 0; i < b.N; i++ {
    repo.GetAvailableTargetsForGroup("")
  }
}
```

Run with:
```bash
go test -bench=. -benchmem ./internal/db
```

## CI/CD Integration

### GitHub Actions

Tests run automatically on:
- Pull requests
- Commits to main
- Scheduled runs

### Local Pre-commit

```bash
#!/bin/bash
go test ./... || exit 1
go fmt ./...
```

## Best Practices

1. **Test Names**: Use descriptive names that explain what is being tested
   ```go
   // ✅ Good
   func TestGroupTargetSetRepo_GetAvailableTargetsForGroup_FiltersInactiveTargets(t *testing.T)

   // ❌ Bad
   func TestGetTargets(t *testing.T)
   ```

2. **Arrange-Act-Assert**: Structure tests clearly
   ```go
   // Arrange
   setup := setupTestDB(t)

   // Act
   result := operation()

   // Assert
   assert.Equal(t, expected, result)
   ```

3. **One Assertion Per Test**: When possible
   ```go
   // ✅ Good - focused test
   func TestCreate_ValidInput_Succeeds(t *testing.T)
   func TestCreate_EmptyID_ReturnsError(t *testing.T)

   // ❌ Bad - multiple concerns
   func TestCreate(t *testing.T)
   ```

4. **Use Table-Driven Tests**: For multiple scenarios
   ```go
   tests := []struct {
     name    string
     input   string
     want    string
     wantErr bool
   }{
     {"valid", "input", "output", false},
     {"invalid", "", "", true},
   }

   for _, tt := range tests {
     t.Run(tt.name, func(t *testing.T) {
       // test logic
     })
   }
   ```

5. **Avoid Test Interdependencies**: Each test should be independent
   ```go
   // ✅ Good - independent
   func TestA(t *testing.T) { setupTestDB(t) }
   func TestB(t *testing.T) { setupTestDB(t) }

   // ❌ Bad - dependent
   func TestA(t *testing.T) { globalDB.Create(...) }
   func TestB(t *testing.T) { globalDB.Query(...) } // depends on A
   ```

## Debugging Tests

### Verbose Output
```bash
go test -v ./internal/db
```

### Print Debugging
```go
t.Logf("Value: %+v", value)
```

### Run Single Test
```bash
go test -run TestName ./package
```

### Debug with Delve
```bash
dlv test ./internal/db -- -test.run TestName
```

## Test Coverage Goals

- **Critical paths**: 90%+ coverage
- **Business logic**: 80%+ coverage
- **Utilities**: 70%+ coverage
- **Overall**: 75%+ coverage

## Future Improvements

- [ ] Add property-based testing with gopter
- [ ] Implement mutation testing
- [ ] Add performance regression testing
- [ ] Create test data factories
- [ ] Add contract testing for APIs
