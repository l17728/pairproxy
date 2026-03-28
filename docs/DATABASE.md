# Database Layer Documentation

## Overview

The database layer provides a comprehensive abstraction for managing all persistent data in pairproxy. It uses GORM as the ORM and SQLite as the default database backend.

## Architecture

### Core Components

- **Models** (`internal/db/models.go`): Define all database entities
- **Repositories** (`internal/db/*_repo.go`): Implement data access patterns
- **Migrations** (`internal/db/migrate.go`): Handle schema versioning

### Database Models

#### User Management
- `User`: User accounts with authentication credentials
- `Group`: User groups for multi-tenancy
- `RefreshToken`: JWT refresh token storage
- `APIKey`: API key authentication

#### Target Management
- `GroupTargetSet`: Collection of targets for a group
- `GroupTargetSetMember`: Individual target in a set with health status
- `TargetAlert`: Alert events for target health issues

#### Routing & LLM
- `LLMTarget`: LLM service endpoints
- `LLMBinding`: Binding between users/groups and LLM targets
- `SemanticRoute`: Semantic routing rules

#### Audit & Usage
- `UsageLog`: Request usage tracking
- `AuditLog`: Audit trail for administrative actions
- `Peer`: Peer node information

## Key Repositories

### GroupTargetSetRepo

Manages target sets and their members with health tracking.

**Important Implementation Details:**

- **IsActive Field Handling**: The `IsActive` field uses raw SQL insertion to properly handle boolean false values. GORM's default value handling would convert `false` to the model's default `true`, so we use:
  ```go
  r.db.Exec(
    "INSERT INTO group_target_set_members (..., is_active, ...) VALUES (..., ?, ...)",
    member.IsActive, ...
  )
  ```

- **Available Targets Query**: Filters members by:
  - `is_active = true`: Only active targets
  - `health_status = "healthy"`: Only healthy targets (for routing decisions)

### UsageLogRepo

Handles usage tracking with buffering for performance.

- Buffers writes to reduce database load
- Flushes periodically or when buffer is full
- Supports batch operations

### UserRepo

Manages user accounts and authentication.

- Supports soft deletes via `active` flag
- Tracks user creation and updates
- Handles group associations

## Database Operations

### Connection Management

```go
db, err := db.Open(logger, dbPath)
defer db.Close()
```

### Migrations

Migrations run automatically on startup:

```go
err := db.Migrate(logger, db)
```

### Transactions

For multi-step operations:

```go
tx := db.BeginTx(ctx, nil)
defer func() {
  if r := recover(); r != nil {
    tx.Rollback()
  }
}()
```

## Testing

### Test Database Setup

Tests use in-memory SQLite for isolation:

```go
testDB, err := db.Open(zap.NewNop(), ":memory:")
require.NoError(t, db.Migrate(zap.NewNop(), testDB))
```

### Key Test Patterns

1. **Isolation**: Each test gets a fresh database
2. **Cleanup**: Automatic via in-memory database lifecycle
3. **Fixtures**: Use helper functions to create test data

### Common Test Issues

**Boolean Field Handling**: When testing boolean fields like `IsActive`, ensure:
- Use raw SQL for insertion if GORM's default handling interferes
- Verify the actual database value, not just the struct value
- Test both `true` and `false` cases

## Performance Considerations

### Indexing

Key indexes for query performance:
- `idx_usage_user_date`: Usage log queries by user and date
- `idx_usage_user_id`: Usage log queries by user
- Foreign key indexes on relationships

### Query Optimization

- Use `Where` clauses to filter early
- Avoid `Preload` for large result sets
- Use pagination for list operations

### Connection Pooling

Configure via environment:
- `DB_MAX_OPEN_CONNS`: Maximum open connections (default: 1 for SQLite)
- `DB_MAX_IDLE_CONNS`: Maximum idle connections
- `DB_CONN_MAX_LIFETIME`: Connection lifetime

## Common Patterns

### Create with Validation

```go
func (r *Repo) Create(entity *Entity) error {
  if entity.ID == "" {
    return fmt.Errorf("ID cannot be empty")
  }
  entity.CreatedAt = time.Now()
  return r.db.Create(entity).Error
}
```

### Update with Timestamp

```go
func (r *Repo) Update(entity *Entity) error {
  entity.UpdatedAt = time.Now()
  return r.db.Model(entity).Updates(entity).Error
}
```

### Soft Delete

```go
func (r *Repo) Delete(id string) error {
  return r.db.Model(&Entity{}).Where("id = ?", id).Update("active", false).Error
}
```

## Troubleshooting

### Connection Issues

- Check database file permissions
- Verify database path is writable
- Check connection pool settings

### Query Issues

- Use `db.Debug()` to see generated SQL
- Check WHERE clause conditions
- Verify table and column names

### Migration Issues

- Check for conflicting migrations
- Verify schema changes are backward compatible
- Review migration logs for errors

## Future Improvements

- [ ] Add query result caching
- [ ] Implement connection pooling for PostgreSQL
- [ ] Add database backup/restore utilities
- [ ] Implement audit log retention policies
