package db

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
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
