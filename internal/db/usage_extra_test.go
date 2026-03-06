package db

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func setupUsageRepoTest(t *testing.T) (*UsageRepo, *UsageWriter, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	writer := NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)

	repo := NewUsageRepo(gormDB, logger)
	return repo, writer, func() { cancel(); writer.Wait() }
}

// ---------------------------------------------------------------------------
// UsageWriter.QueueDepth — 反映 channel 中待处理记录数
// ---------------------------------------------------------------------------

func TestUsageWriter_QueueDepth_InitiallyZero(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := NewUsageWriter(gormDB, logger, 10, time.Minute)
	writer.Start(ctx)
	defer writer.Wait()
	cancel()

	if writer.QueueDepth() != 0 {
		t.Errorf("QueueDepth() = %d, want 0 initially", writer.QueueDepth())
	}
}

// ---------------------------------------------------------------------------
// UsageWriter.Flush — Flush 在 runLoop 未启动时直接排空 channel
// ---------------------------------------------------------------------------

func TestUsageWriter_Flush_EmptyChannel_NoOp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	// 不启动 runLoop，直接调用 Flush（channel 为空）
	writer := NewUsageWriter(gormDB, logger, 10, time.Minute)
	// 不调用 Start，直接 Flush 空 channel → 不应 panic
	writer.Flush()
}

func TestUsageWriter_Flush_DrainsPendingRecords(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	// 不启动 runLoop（不调用 Start），手动将记录推入 channel 并 Flush
	writer := NewUsageWriter(gormDB, logger, 50, 10*time.Minute)

	// 直接向 channel 写入（不走 Record() 方法，避免 runLoop 竞争）
	for i := 0; i < 3; i++ {
		writer.ch <- UsageRecord{
			RequestID:    "direct-flush-" + string(rune('a'+i)),
			UserID:       "direct-flush-user",
			InputTokens:  100,
			OutputTokens: 50,
			StatusCode:   200,
			CreatedAt:    time.Now(),
		}
	}

	// Flush 应排空 channel 并写入 DB
	writer.Flush()

	// 验证 DB 中有数据
	repo := NewUsageRepo(gormDB, logger)
	from := time.Now().Add(-time.Minute)
	to := time.Now().Add(time.Minute)
	in, out, err := repo.SumTokens("direct-flush-user", from, to)
	if err != nil {
		t.Fatalf("SumTokens: %v", err)
	}
	if in == 0 && out == 0 {
		t.Error("Flush should have written records to DB")
	}

	// 手动关闭 done 通道（因为没有启动 runLoop）
	close(writer.done)
}

// ---------------------------------------------------------------------------
// UsageRecord.TotalTokens
// ---------------------------------------------------------------------------

func TestUsageRecord_TotalTokens(t *testing.T) {
	r := UsageRecord{InputTokens: 300, OutputTokens: 150}
	if got := r.TotalTokens(); got != 450 {
		t.Errorf("TotalTokens() = %d, want 450", got)
	}
}

func TestUsageRecord_TotalTokens_Zero(t *testing.T) {
	r := UsageRecord{}
	if got := r.TotalTokens(); got != 0 {
		t.Errorf("TotalTokens() = %d, want 0 for zero record", got)
	}
}

// ---------------------------------------------------------------------------
// UsageRepo.UserStats — 按用户聚合用量
// ---------------------------------------------------------------------------

func TestUsageRepo_UserStats_BasicAggregation(t *testing.T) {
	repo, writer, stop := setupUsageRepoTest(t)
	defer stop()
	_ = writer

	now := time.Now()
	from := now.Add(-24 * time.Hour)
	to := now.Add(time.Hour)

	// 直接插入两个用户的记录
	logs := []UsageLog{
		{RequestID: "us1-r1", UserID: "userA", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, StatusCode: 200, CreatedAt: now.Add(-time.Hour), SourceNode: "local"},
		{RequestID: "us1-r2", UserID: "userA", InputTokens: 200, OutputTokens: 100, TotalTokens: 300, StatusCode: 200, CreatedAt: now.Add(-30 * time.Minute), SourceNode: "local"},
		{RequestID: "us1-r3", UserID: "userB", InputTokens: 500, OutputTokens: 200, TotalTokens: 700, StatusCode: 200, CreatedAt: now.Add(-10 * time.Minute), SourceNode: "local"},
	}
	for _, l := range logs {
		if err := repo.db.Create(&l).Error; err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}

	rows, err := repo.UserStats(from, to, 10)
	if err != nil {
		t.Fatalf("UserStats: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 user rows, got %d", len(rows))
	}

	// 按降序排列 - userB 用量最多应排第一
	if rows[0].UserID != "userB" {
		t.Errorf("first row should be userB (highest usage), got %q", rows[0].UserID)
	}

	// 验证 userA 的聚合
	var userARow *UserStatRow
	for i := range rows {
		if rows[i].UserID == "userA" {
			userARow = &rows[i]
			break
		}
	}
	if userARow == nil {
		t.Fatal("userA not found in UserStats result")
	}
	if userARow.TotalInput != 300 {
		t.Errorf("userA TotalInput = %d, want 300", userARow.TotalInput)
	}
	if userARow.RequestCount != 2 {
		t.Errorf("userA RequestCount = %d, want 2", userARow.RequestCount)
	}
}

func TestUsageRepo_UserStats_DefaultLimit(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	now := time.Now()
	// limit=0 时应使用默认值 50
	rows, err := repo.UserStats(now.Add(-24*time.Hour), now, 0)
	if err != nil {
		t.Fatalf("UserStats with limit=0: %v", err)
	}
	_ = rows // 只要不报错即可
}

// ---------------------------------------------------------------------------
// UsageRepo.SumCostUSD
// ---------------------------------------------------------------------------

func TestUsageRepo_SumCostUSD_NoData(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	from := time.Now().Add(-time.Hour)
	to := time.Now()

	total, err := repo.SumCostUSD(from, to)
	if err != nil {
		t.Fatalf("SumCostUSD: %v", err)
	}
	if total != 0 {
		t.Errorf("SumCostUSD with no data = %f, want 0", total)
	}
}

func TestUsageRepo_SumCostUSD_WithData(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	now := time.Now()
	logs := []UsageLog{
		{RequestID: "cost-r1", UserID: "u1", CostUSD: 0.5, TotalTokens: 100, StatusCode: 200, CreatedAt: now.Add(-30 * time.Minute), SourceNode: "local"},
		{RequestID: "cost-r2", UserID: "u1", CostUSD: 0.25, TotalTokens: 50, StatusCode: 200, CreatedAt: now.Add(-10 * time.Minute), SourceNode: "local"},
	}
	for _, l := range logs {
		if err := repo.db.Create(&l).Error; err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}

	total, err := repo.SumCostUSD(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SumCostUSD: %v", err)
	}
	// 允许浮点精度误差
	if total < 0.74 || total > 0.76 {
		t.Errorf("SumCostUSD = %f, want ~0.75", total)
	}
}

// ---------------------------------------------------------------------------
// UsageRepo.ListUnsynced + MarkSynced
// ---------------------------------------------------------------------------

func TestUsageRepo_ListUnsynced_Empty(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	rows, err := repo.ListUnsynced(50)
	if err != nil {
		t.Fatalf("ListUnsynced: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("ListUnsynced with no data = %d rows, want 0", len(rows))
	}
}

func TestUsageRepo_ListUnsynced_DefaultLimit(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	// limit=0 应使用默认值 200，不报错
	_, err := repo.ListUnsynced(0)
	if err != nil {
		t.Fatalf("ListUnsynced with limit=0: %v", err)
	}
}

func TestUsageRepo_MarkSynced_Empty_NoError(t *testing.T) {
	repo, _, stop := setupUsageRepoTest(t)
	defer stop()

	// 空列表应无错
	if err := repo.MarkSynced([]string{}); err != nil {
		t.Fatalf("MarkSynced with empty list: %v", err)
	}
}

// ---------------------------------------------------------------------------
// UserRepo.UpdatePassword
// ---------------------------------------------------------------------------

func TestUserRepo_UpdatePassword(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	userRepo := NewUserRepo(gormDB, logger)

	// 创建用户
	user := &User{ID: "up-u1", Username: "pass_user", PasswordHash: "old-hash", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 更新密码
	if err := userRepo.UpdatePassword("up-u1", "new-hash"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// 验证密码已更新
	fetched, err := userRepo.GetByUsername("pass_user")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if fetched.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want 'new-hash'", fetched.PasswordHash)
	}
}

// ---------------------------------------------------------------------------
// UserRepo.GetByID — 找到和未找到路径
// ---------------------------------------------------------------------------

func TestUserRepo_GetByID_Found(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	userRepo := NewUserRepo(gormDB, logger)

	user := &User{ID: "gbi-u1", Username: "gbi_user", PasswordHash: "h", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fetched, err := userRepo.GetByID("gbi-u1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched == nil {
		t.Fatal("expected non-nil user")
	}
	if fetched.Username != "gbi_user" {
		t.Errorf("Username = %q, want 'gbi_user'", fetched.Username)
	}
}

func TestUserRepo_GetByID_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	userRepo := NewUserRepo(gormDB, logger)

	fetched, err := userRepo.GetByID("nonexistent-id")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched != nil {
		t.Error("GetByID should return nil for nonexistent user")
	}
}

// ---------------------------------------------------------------------------
// UserRepo.UpdateLastLogin
// ---------------------------------------------------------------------------

func TestUserRepo_UpdateLastLogin(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	userRepo := NewUserRepo(gormDB, logger)

	user := &User{ID: "ull-u1", Username: "login_user", PasswordHash: "h", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create: %v", err)
	}

	loginTime := time.Now().Round(time.Second)
	if err := userRepo.UpdateLastLogin("ull-u1", loginTime); err != nil {
		t.Fatalf("UpdateLastLogin: %v", err)
	}
}

// ---------------------------------------------------------------------------
// APIKeyRepo.GetByID
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_GetByID(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	repo := NewAPIKeyRepo(gormDB, logger)

	// 创建一个 API Key
	key := &APIKey{
		ID:             "test-apikey-id",
		Name:           "test-key",
		EncryptedValue: "enc-value",
		Provider:       "anthropic",
	}
	if err := gormDB.Create(key).Error; err != nil {
		t.Fatalf("Create APIKey: %v", err)
	}

	// GetByID 应找到
	found, err := repo.GetByID("test-apikey-id")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found == nil {
		t.Fatal("expected non-nil APIKey")
	}
	if found.Name != "test-key" {
		t.Errorf("Name = %q, want 'test-key'", found.Name)
	}
}

func TestAPIKeyRepo_GetByID_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	repo := NewAPIKeyRepo(gormDB, logger)

	found, err := repo.GetByID("nonexistent-key-id")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found != nil {
		t.Error("GetByID should return nil for nonexistent key")
	}
}

// ---------------------------------------------------------------------------
// APIKeyRepo.GetByName — 未找到路径
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_GetByName_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	repo := NewAPIKeyRepo(gormDB, logger)

	found, err := repo.GetByName("no-such-key")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if found != nil {
		t.Error("GetByName should return nil for nonexistent key name")
	}
}
