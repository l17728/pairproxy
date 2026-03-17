package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// stopWriter cancels the context and waits for the UsageWriter goroutine to exit.
func stopWriter(cancel context.CancelFunc, w *UsageWriter) {
	cancel()
	w.Wait()
}

// openTestDB opens an in-memory SQLite database and runs migrations.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	logger := zaptest.NewLogger(t)
	db, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(logger, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// Task-05: DB connection and migration
// ---------------------------------------------------------------------------

func TestDBOpen(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open should succeed, got: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestAutoMigrate(t *testing.T) {
	db := openTestDB(t)

	// Verify all expected tables exist by performing a simple query on each.
	tables := []string{"groups", "users", "refresh_tokens", "usage_logs", "peers"}
	for _, tbl := range tables {
		var count int64
		if err := db.Table(tbl).Count(&count).Error; err != nil {
			t.Errorf("table %q not found or query failed: %v", tbl, err)
		}
	}
}

func TestWALMode(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var mode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatalf("PRAGMA query failed: %v", err)
	}
	// :memory: may return "memory" instead of "wal" because WAL is not supported
	// on in-memory DBs; accept both.
	if mode != "wal" && mode != "memory" {
		t.Errorf("journal_mode = %q, want wal or memory", mode)
	}
}

// ---------------------------------------------------------------------------
// Task-06: UserRepo / GroupRepo
// ---------------------------------------------------------------------------

func TestCreateAndGetUser(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	groupRepo := NewGroupRepo(db, logger)
	g := &Group{Name: "engineering"}
	if err := groupRepo.Create(g); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	if g.ID == "" {
		t.Fatal("group ID should be set after create")
	}

	userRepo := NewUserRepo(db, logger)
	u := &User{
		Username:     "alice",
		PasswordHash: "$2a$12$placeholder",
		GroupID:      &g.ID,
	}
	if err := userRepo.Create(u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if u.ID == "" {
		t.Fatal("user ID should be set after create")
	}

	found, err := userRepo.GetByUsername("alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if found == nil {
		t.Fatal("GetByUsername returned nil for existing user")
	}
	if found.Username != "alice" {
		t.Errorf("Username = %q, want %q", found.Username, "alice")
	}
	if found.GroupID == nil || *found.GroupID != g.ID {
		t.Errorf("GroupID = %v, want %q", found.GroupID, g.ID)
	}
	if found.Group.Name != "engineering" {
		t.Errorf("Group.Name = %q, want %q", found.Group.Name, "engineering")
	}
}

func TestGetUserNotFound(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	found, err := userRepo.GetByUsername("no-such-user")
	if err != nil {
		t.Fatalf("GetByUsername should not error for missing user, got: %v", err)
	}
	if found != nil {
		t.Error("GetByUsername should return nil for missing user")
	}
}

func TestDuplicateUsername(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	u1 := &User{Username: "bob", PasswordHash: "hash1"}
	if err := userRepo.Create(u1); err != nil {
		t.Fatalf("First create: %v", err)
	}

	u2 := &User{Username: "bob", PasswordHash: "hash2"}
	if err := userRepo.Create(u2); err == nil {
		t.Error("Second create with same username should return an error")
	}
}

func TestDisableUser(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	u := &User{Username: "carol", PasswordHash: "hash"}
	if err := userRepo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := userRepo.SetActive(u.ID, false); err != nil {
		t.Fatalf("SetActive false: %v", err)
	}

	found, err := userRepo.GetByID(u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found == nil {
		t.Fatal("GetByID returned nil")
	}
	if found.IsActive {
		t.Error("IsActive should be false after SetActive(false)")
	}
}

func TestUpdateLastLogin(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	u := &User{Username: "dave", PasswordHash: "hash"}
	if err := userRepo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	if err := userRepo.UpdateLastLogin(u.ID, now); err != nil {
		t.Fatalf("UpdateLastLogin: %v", err)
	}

	found, _ := userRepo.GetByID(u.ID)
	if found.LastLoginAt == nil {
		t.Fatal("LastLoginAt should be set")
	}
	if !found.LastLoginAt.Truncate(time.Second).Equal(now) {
		t.Errorf("LastLoginAt = %v, want %v", found.LastLoginAt, now)
	}
}

func TestGroupSetQuota(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	g := &Group{Name: "trial"}
	if err := groupRepo.Create(g); err != nil {
		t.Fatalf("Create: %v", err)
	}

	daily := int64(10000)
	monthly := int64(200000)
	if err := groupRepo.SetQuota(g.ID, &daily, &monthly, nil, nil, nil); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	found, err := groupRepo.GetByID(g.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found.DailyTokenLimit == nil || *found.DailyTokenLimit != daily {
		t.Errorf("DailyTokenLimit = %v, want %d", found.DailyTokenLimit, daily)
	}
	if found.MonthlyTokenLimit == nil || *found.MonthlyTokenLimit != monthly {
		t.Errorf("MonthlyTokenLimit = %v, want %d", found.MonthlyTokenLimit, monthly)
	}
}

// ---------------------------------------------------------------------------
// Task-07: UsageWriter / UsageRepo
// ---------------------------------------------------------------------------

func TestUsageBatchWrite(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute) // long interval; rely on bufferSize flush
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	const n = 150
	for i := range n {
		writer.Record(UsageRecord{
			RequestID:    uniqueRequestID(i),
			UserID:       "user-1",
			Model:        "claude-3-5-sonnet",
			InputTokens:  100,
			OutputTokens: 50,
			StatusCode:   200,
			CreatedAt:    time.Now(),
		})
	}
	// Cancel context to trigger graceful drain + flush, then wait.
	stopWriter(cancel, writer)

	var count int64
	if err := db.Model(&UsageLog{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != n {
		t.Errorf("expected %d usage_logs, got %d", n, count)
	}
}

func TestUsageIDempotent(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	r := UsageRecord{
		RequestID:    "req-idempotent-1",
		UserID:       "user-2",
		Model:        "claude-3-opus",
		InputTokens:  200,
		OutputTokens: 100,
		StatusCode:   200,
		CreatedAt:    time.Now(),
	}
	writer.Record(r)
	writer.Record(r) // duplicate
	stopWriter(cancel, writer)

	var count int64
	db.Model(&UsageLog{}).Where("request_id = ?", r.RequestID).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 record for duplicate request_id, got %d", count)
	}
}

func TestQueryUsage(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	base := time.Now().Add(-48 * time.Hour)
	// Records for user-A: 3 within range, 1 outside
	for i := range 3 {
		writer.Record(UsageRecord{
			RequestID:   uniqueRequestID(100 + i),
			UserID:      "user-A",
			Model:       "gpt-4o",
			InputTokens: 10,
			CreatedAt:   base.Add(time.Duration(i) * time.Hour),
		})
	}
	writer.Record(UsageRecord{
		RequestID: "req-outside-range",
		UserID:    "user-A",
		CreatedAt: base.Add(-24 * time.Hour), // before range
	})
	// Records for user-B (should not appear in user-A filter)
	writer.Record(UsageRecord{
		RequestID: "req-user-b",
		UserID:    "user-B",
		CreatedAt: base,
	})
	stopWriter(cancel, writer)

	repo := NewUsageRepo(db, logger)

	from := base.Add(-time.Hour)
	to := base.Add(4 * time.Hour)
	logs, err := repo.Query(UsageFilter{
		UserID: "user-A",
		From:   &from,
		To:     &to,
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 logs for user-A in range, got %d", len(logs))
	}
}

func TestSumTokens(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	now := time.Now()
	for i := range 5 {
		writer.Record(UsageRecord{
			RequestID:    uniqueRequestID(200 + i),
			UserID:       "user-sum",
			InputTokens:  100,
			OutputTokens: 50,
			CreatedAt:    now,
		})
	}
	stopWriter(cancel, writer)

	repo := NewUsageRepo(db, logger)
	in, out, err := repo.SumTokens("user-sum", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("SumTokens: %v", err)
	}
	if in != 500 {
		t.Errorf("inputSum = %d, want 500", in)
	}
	if out != 250 {
		t.Errorf("outputSum = %d, want 250", out)
	}
}

func TestListUnsyncedAndMarkSynced(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	for i := range 5 {
		writer.Record(UsageRecord{
			RequestID: uniqueRequestID(300 + i),
			UserID:    "user-sync",
			CreatedAt: time.Now(),
		})
	}
	stopWriter(cancel, writer)

	repo := NewUsageRepo(db, logger)

	unsynced, err := repo.ListUnsynced(10)
	if err != nil {
		t.Fatalf("ListUnsynced: %v", err)
	}
	if len(unsynced) != 5 {
		t.Errorf("expected 5 unsynced, got %d", len(unsynced))
	}

	ids := make([]string, 0, len(unsynced))
	for _, l := range unsynced {
		ids = append(ids, l.RequestID)
	}
	if err := repo.MarkSynced(ids); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	unsynced2, _ := repo.ListUnsynced(10)
	if len(unsynced2) != 0 {
		t.Errorf("expected 0 unsynced after MarkSynced, got %d", len(unsynced2))
	}
}

// uniqueRequestID generates a unique request ID for tests.
func uniqueRequestID(n int) string {
	return "req-test-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// ---------------------------------------------------------------------------
// Connection pool defaults
// ---------------------------------------------------------------------------

// TestOpenWithConfig_ConnectionPoolDefaults 验证 OpenWithConfig 根据数据库类型和路径
// 应用正确的默认 MaxOpenConns：
//   - SQLite :memory: → 1（内存库每连接独立实例，必须单连接）
//   - SQLite 文件库   → 25（WAL 模式允许最多 25 个并发连接）
func TestOpenWithConfig_ConnectionPoolDefaults(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("memory SQLite MaxOpenConns=1", func(t *testing.T) {
		db, err := OpenWithConfig(logger, config.DatabaseConfig{
			Driver: "sqlite",
			Path:   ":memory:",
		})
		if err != nil {
			t.Fatalf("OpenWithConfig: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("db.DB(): %v", err)
		}
		stats := sqlDB.Stats()
		if stats.MaxOpenConnections != 1 {
			t.Errorf("MaxOpenConnections = %d, want 1 for :memory: SQLite", stats.MaxOpenConnections)
		}
	})

	t.Run("file SQLite MaxOpenConns=25", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		db, err := OpenWithConfig(logger, config.DatabaseConfig{
			Driver: "sqlite",
			Path:   path,
		})
		if err != nil {
			t.Fatalf("OpenWithConfig: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("db.DB(): %v", err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		stats := sqlDB.Stats()
		if stats.MaxOpenConnections != 25 {
			t.Errorf("MaxOpenConnections = %d, want 25 for file SQLite", stats.MaxOpenConnections)
		}
	})
}

// TestOpenWithConfig_CustomMaxOpenConns 验证用户自定义 MaxOpenConns>0 时不被默认值覆盖。I-2
func TestOpenWithConfig_CustomMaxOpenConns(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db, err := OpenWithConfig(logger, config.DatabaseConfig{
		Driver:       "sqlite",
		Path:         ":memory:",
		MaxOpenConns: 7,
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	stats := sqlDB.Stats()
	assert.Equal(t, 7, stats.MaxOpenConnections)
}

// TestOpenWithConfig_MaxIdleConnsCapping 验证 maxIdle>maxOpen 时触发 WARN 日志并截断。I-1
func TestOpenWithConfig_MaxIdleConnsCapping(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	db, err := OpenWithConfig(logger, config.DatabaseConfig{
		Driver:       "sqlite",
		Path:         ":memory:",
		MaxOpenConns: 5,
		MaxIdleConns: 20, // 超过 maxOpen，应被截断
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	warnLogs := logs.FilterMessage("max_idle_conns exceeds max_open_conns, capping to max_open_conns")
	assert.Equal(t, 1, warnLogs.Len(), "should emit one WARN when maxIdle > maxOpen")

	stats := sqlDB.Stats()
	assert.Equal(t, 5, stats.MaxOpenConnections, "MaxOpenConns should remain 5")
}

// TestOpenWithConfig_CustomLifecycleParams 验证自定义 ConnMaxLifetime/ConnMaxIdleTime>0
// 时不被默认值覆盖（smoke test）。M-4
func TestOpenWithConfig_CustomLifecycleParams(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db, err := OpenWithConfig(logger, config.DatabaseConfig{
		Driver:          "sqlite",
		Path:            ":memory:",
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// 若生命周期参数被正确应用，DB 仍可正常 Ping
	assert.NoError(t, sqlDB.Ping())
}

// TestOpenWithConfig_InvalidSQLitePath 验证无效路径时错误被正确包装返回。M-5
// 注：glebarez/sqlite pure-Go 驱动对不存在的路径不会在 Open 阶段失败（懒加载），
// 故使用 Postgres 驱动 + 必定拒绝的端口来验证错误包装。
func TestOpenWithConfig_ErrorWrapping(t *testing.T) {
	logger := zaptest.NewLogger(t)
	// 127.0.0.1:1 几乎必定 connection refused，且 TCP 栈会立即返回
	_, err := OpenWithConfig(logger, config.DatabaseConfig{
		Driver: "postgres",
		DSN:    "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open database")
}
