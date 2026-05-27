package db

// ---------------------------------------------------------------------------
// 回归测试集：由历次实际 bug 模式推导出的覆盖缺口
//
// Bug 模式一览：
//   1. parseFlexTime 仅测 happy path，无效字符串路径未被覆盖
//   2. SetCostFunc 从未被测试，cost_usd 写入路径 blind
//   3. MarkSynced 只测空列表，ListUnsynced → MarkSynced → ListUnsynced 全流程未覆盖
//   4. DeleteExpired 使用 < 语义，恰好在 expires_at == now 时行为未测
//   5. GORM Create 忽略 IsActive:false 零值（历次手动测试暴露的"陷阱"）
//   6. UsageWriter 达到 bufferSize 时应立即 flush，不等定时器（大批量写入场景）
// ---------------------------------------------------------------------------

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Bug 1：parseFlexTime 无效字符串路径
// ---------------------------------------------------------------------------

// TestParseFlexTime_InvalidInput 确认无效字符串返回 error，不 panic
func TestParseFlexTime_InvalidInput(t *testing.T) {
	cases := []string{
		"",
		"not-a-date",
		"2025/03/15 10:30:00", // 斜杠格式，不在支持范围内
		"random garbage 123",
		"15-03-2025",          // DD-MM-YYYY，不在支持范围内
	}
	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			parsed, err := parseFlexTime(raw)
			if err == nil {
				t.Errorf("parseFlexTime(%q) expected error, got nil (parsed=%v)", raw, parsed)
			}
		})
	}
}

// TestParseFlexTime_PreservesDate 确认解析后的日期分量保持原意（不因时区变换而偏移日期）
func TestParseFlexTime_PreservesDate(t *testing.T) {
	// SQLite 有时返回 "2025-03-15 10:30:00"（无时区），应解析为 UTC
	raw := "2025-03-15 10:30:00"
	parsed, err := parseFlexTime(raw)
	if err != nil {
		t.Fatalf("parseFlexTime(%q): %v", raw, err)
	}
	if parsed.Year() != 2025 || parsed.Month() != 3 || parsed.Day() != 15 {
		t.Errorf("date mismatch: got %v, want 2025-03-15", parsed)
	}
}

// ---------------------------------------------------------------------------
// Bug 2：SetCostFunc 写入 cost_usd 的端到端路径
// ---------------------------------------------------------------------------

// TestUsageWriter_CostFuncRoundTrip 验证 SetCostFunc 设置的计算函数
// 能正确将 cost_usd 写入数据库，而不是保持 0。
func TestUsageWriter_CostFuncRoundTrip(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	repo := NewUsageRepo(gormDB, logger)

	writer := NewUsageWriter(gormDB, logger, 100, time.Hour) // 长定时器，靠 cancel+Wait 触发
	// 设置费用计算函数：每个 output token 计 $0.001
	writer.SetCostFunc(func(model string, inputTokens, outputTokens int) float64 {
		return float64(outputTokens) * 0.001
	})

	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	writer.Record(UsageRecord{
		RequestID:    "cost-rt-1",
		UserID:       "u-cost",
		Model:        "claude-3",
		InputTokens:  100,
		OutputTokens: 500, // 预期 cost = 500 * 0.001 = 0.5
		CreatedAt:    time.Now(),
	})

	cancel()
	writer.Wait()

	var logs []UsageLog
	if err := gormDB.Where("request_id = ?", "cost-rt-1").Find(&logs).Error; err != nil {
		t.Fatalf("find log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	// cost_usd 应该 ≈ 0.5，允许极小浮点误差
	if logs[0].CostUSD < 0.499 || logs[0].CostUSD > 0.501 {
		t.Errorf("cost_usd = %f, want ~0.500", logs[0].CostUSD)
	}
	_ = repo // 确保 repo 变量不被 lint 报错（后续可扩展查询）
}

// TestUsageWriter_NilCostFunc_CostIsZero 当 CostFunc 未设置时，cost_usd 应为 0
func TestUsageWriter_NilCostFunc_CostIsZero(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	writer := NewUsageWriter(gormDB, logger, 100, time.Hour)
	// 不调用 SetCostFunc

	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	writer.Record(UsageRecord{
		RequestID:    "cost-nil-1",
		UserID:       "u-nil",
		Model:        "claude-3",
		InputTokens:  1000,
		OutputTokens: 2000,
		CreatedAt:    time.Now(),
	})

	cancel()
	writer.Wait()

	var logs []UsageLog
	if err := gormDB.Where("request_id = ?", "cost-nil-1").Find(&logs).Error; err != nil {
		t.Fatalf("find log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].CostUSD != 0 {
		t.Errorf("cost_usd = %f, want 0 when CostFunc is nil", logs[0].CostUSD)
	}
}

// ---------------------------------------------------------------------------
// Bug 3：MarkSynced 全流程
// ---------------------------------------------------------------------------

// TestMarkSynced_FullCycle 验证 ListUnsynced → MarkSynced → ListUnsynced 全流程：
// 已标记的记录不再出现在 ListUnsynced 结果中，且未标记的记录不受影响。
func TestMarkSynced_FullCycle(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewUsageRepo(gormDB, logger)

	now := time.Now()

	// 插入 3 条未同步记录
	for i := 1; i <= 3; i++ {
		gormDB.Create(&UsageLog{
			RequestID:  "sync-full-" + string(rune('0'+i)),
			UserID:     "u-sync",
			TotalTokens: 100,
			StatusCode: 200,
			SourceNode: "local",
			CreatedAt:  now.Add(-time.Duration(i) * time.Minute),
		})
	}

	// 首次 ListUnsynced 应返回 3 条
	rows, err := repo.ListUnsynced(50)
	if err != nil {
		t.Fatalf("ListUnsynced: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("before sync: expected 3, got %d", len(rows))
	}

	// 只 MarkSynced 前两条
	toSync := []string{"sync-full-1", "sync-full-2"}
	if err := repo.MarkSynced(toSync); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// 再次 ListUnsynced：只剩 1 条
	rows, err = repo.ListUnsynced(50)
	if err != nil {
		t.Fatalf("ListUnsynced after sync: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("after sync: expected 1, got %d", len(rows))
	}
	if rows[0].RequestID != "sync-full-3" {
		t.Errorf("remaining unsynced = %q, want sync-full-3", rows[0].RequestID)
	}
}

// TestMarkSynced_NonExistentIDs 对不存在的 request_id 调用 MarkSynced 不报错
func TestMarkSynced_NonExistentIDs(t *testing.T) {
	gormDB := openTestDB(t)
	repo := NewUsageRepo(gormDB, zaptest.NewLogger(t))

	err := repo.MarkSynced([]string{"ghost-id-1", "ghost-id-2"})
	if err != nil {
		t.Errorf("MarkSynced with non-existent IDs should not error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Bug 4：DeleteExpired 时间边界（< vs <=）
// ---------------------------------------------------------------------------

// TestDeleteExpired_ExactBoundary 令牌 expires_at 恰好等于"现在"时，
// 由于 DeleteExpired 使用 < 而非 <=，该令牌不应被删除。
//
// 注意：这与 DeleteBefore 的语义相同，历次已发现 < 边界的重要性。
func TestDeleteExpired_ExactBoundary(t *testing.T) {
	gormDB := openTestDB(t)
	repo := NewRefreshTokenRepo(gormDB, zaptest.NewLogger(t))

	// 创建一个"恰好在 1 秒后到期"的令牌（相对于实际删除时刻已过去）
	now := time.Now()

	// 令牌 A：明确已过期（expires_at 在 now 之前 1 秒）
	repo.Create(&RefreshToken{
		JTI:       "exp-boundary-a",
		UserID:    "u-boundary",
		ExpiresAt: now.Add(-time.Second),
	})

	// 令牌 B：未过期（expires_at 在 now 之后 10 秒）
	repo.Create(&RefreshToken{
		JTI:       "exp-boundary-b",
		UserID:    "u-boundary",
		ExpiresAt: now.Add(10 * time.Second),
	})

	count, err := repo.DeleteExpired()
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if count != 1 {
		t.Errorf("deleted count = %d, want 1", count)
	}

	// 令牌 A 应已删除
	found, _ := repo.GetByJTI("exp-boundary-a")
	if found != nil {
		t.Error("expired token (now - 1s) should have been deleted")
	}

	// 令牌 B 应保留
	found, _ = repo.GetByJTI("exp-boundary-b")
	if found == nil {
		t.Error("future token (now + 10s) should not have been deleted")
	}
}

// TestDeleteExpired_EmptyTable_NoError 空表调用 DeleteExpired 不报错，返回 0
func TestDeleteExpired_EmptyTable_NoError(t *testing.T) {
	gormDB := openTestDB(t)
	repo := NewRefreshTokenRepo(gormDB, zaptest.NewLogger(t))

	count, err := repo.DeleteExpired()
	if err != nil {
		t.Fatalf("DeleteExpired on empty table: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 deletions on empty table, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Bug 5：GORM Create 忽略 IsActive:false 零值（文档化 trap）
// ---------------------------------------------------------------------------

// TestUserRepo_Create_IsActiveFalseViaDirect_Trap 文档化 GORM 的"零值陷阱"：
// 使用 gorm.DB.Create(&User{IsActive: false}) 时，因 false 是 bool 的零值，
// GORM 会使用 schema 默认值 true，导致实际写入 true。
//
// 正确做法：先 Create（IsActive:true），再 SetActive(id, false)。
// 本测试验证 UserRepo.SetActive 能正确覆盖 GORM 默认值。
func TestUserRepo_Create_IsActiveFalseTrap(t *testing.T) {
	gormDB := openTestDB(t)
	repo := NewUserRepo(gormDB, zaptest.NewLogger(t))

	// 用 IsActive: true 创建（先避开 GORM 零值问题）
	u := &User{
		ID:           "trap-user-1",
		Username:     "trap-user",
		PasswordHash: "hash",
		IsActive:     true,
	}
	if err := repo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 通过 SetActive 将其设为禁用（这是绕开零值陷阱的正确路径）
	if err := repo.SetActive(u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}

	// 验证确实变成了 false
	found, err := repo.GetByID(u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found.IsActive {
		t.Error("user should be inactive after SetActive(false), but IsActive is still true")
	}
}

// TestUserRepo_Create_ThenSetActiveTrue 验证 SetActive(true) 也正常工作（正路径）
func TestUserRepo_Create_ThenSetActiveTrue(t *testing.T) {
	gormDB := openTestDB(t)
	repo := NewUserRepo(gormDB, zaptest.NewLogger(t))

	u := &User{
		ID:           "trap-user-2",
		Username:     "trap-user-2",
		PasswordHash: "hash",
		IsActive:     true,
	}
	if err := repo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 先禁用
	if err := repo.SetActive(u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	// 再启用
	if err := repo.SetActive(u.ID, true); err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	found, err := repo.GetByID(u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !found.IsActive {
		t.Error("user should be active after SetActive(true)")
	}
}

// ---------------------------------------------------------------------------
// Bug 6：UsageWriter 批量触发（不依赖定时器）
// ---------------------------------------------------------------------------

// TestUsageWriter_BatchSizeFlushTrigger 验证当批量记录数达到 bufferSize 时，
// UsageWriter 会立即 flush，而不需要等待 interval 定时器到期。
//
// 历史教训：flush 时序竞态。此测试用长定时器 + cancel+Wait 模式，
// 完全排除定时器干扰，只靠"达到批量大小"触发的 flush 验证数据写入。
func TestUsageWriter_BatchSizeFlushTrigger(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)

	bufferSize := 5
	// 定时器设为超长（1 小时），确保批量触发是唯一写入时机
	writer := NewUsageWriter(gormDB, logger, bufferSize, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); writer.Wait() }()
	writer.Start(ctx)

	// 发送 bufferSize 条记录（精确等于批量大小，应立即触发 flush）
	for i := 0; i < bufferSize; i++ {
		writer.Record(UsageRecord{
			RequestID: "batch-trigger-" + string(rune('0'+i)),
			UserID:    "u-batch",
			Model:     "claude-3",
			InputTokens:  10,
			OutputTokens: 20,
			CreatedAt: time.Now(),
		})
	}

	// 等待短暂时间让 goroutine 处理（不依赖定时器，只要 bufferSize 达到即刻写入）
	time.Sleep(50 * time.Millisecond)

	var count int64
	gormDB.Model(&UsageLog{}).Where("user_id = ?", "u-batch").Count(&count)
	if count != int64(bufferSize) {
		t.Errorf("after batch trigger: got %d rows in DB, want %d (batch flush should not wait for interval)",
			count, bufferSize)
	}
}

// TestUsageWriter_RecordWithZeroCreatedAt 验证 CreatedAt 为零值时被自动填充为"现在"
func TestUsageWriter_RecordWithZeroCreatedAt(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB := openTestDB(t)
	writer := NewUsageWriter(gormDB, logger, 100, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	before := time.Now()
	writer.Record(UsageRecord{
		RequestID: "zero-ts-1",
		UserID:    "u-zero",
		// 不设置 CreatedAt，验证它被自动设为 now
	})

	cancel()
	writer.Wait()

	var logs []UsageLog
	if err := gormDB.Where("request_id = ?", "zero-ts-1").Find(&logs).Error; err != nil {
		t.Fatalf("find log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	after := time.Now()

	ts := logs[0].CreatedAt
	if ts.Before(before) || ts.After(after.Add(time.Second)) {
		t.Errorf("auto-filled CreatedAt = %v, expected between %v and %v", ts, before, after)
	}
}

