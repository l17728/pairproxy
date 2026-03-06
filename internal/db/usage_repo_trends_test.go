package db

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func TestUsageRepo_DailyTokens(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := NewUsageRepo(gormDB, logger)

	// 插入测试数据（3 天）
	now := time.Now()
	day1 := now.AddDate(0, 0, -2).Truncate(24 * time.Hour)
	day2 := now.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	day3 := now.Truncate(24 * time.Hour)

	logs := []UsageLog{
		{RequestID: "r1", UserID: "u1", InputTokens: 100, OutputTokens: 50, CreatedAt: day1.Add(time.Hour)},
		{RequestID: "r2", UserID: "u1", InputTokens: 200, OutputTokens: 100, CreatedAt: day1.Add(2 * time.Hour)},
		{RequestID: "r3", UserID: "u2", InputTokens: 300, OutputTokens: 150, CreatedAt: day2.Add(time.Hour)},
		{RequestID: "r4", UserID: "u1", InputTokens: 400, OutputTokens: 200, CreatedAt: day3.Add(time.Hour)},
	}

	for _, log := range logs {
		if err := gormDB.Create(&log).Error; err != nil {
			t.Fatalf("Create log: %v", err)
		}
	}

	// 测试全局聚合
	from := day1
	to := day3.Add(24 * time.Hour)
	rows, err := repo.DailyTokens(from, to, "")
	if err != nil {
		t.Fatalf("DailyTokens: %v", err)
	}

	if len(rows) != 3 {
		t.Errorf("expected 3 days, got %d", len(rows))
	}

	// 验证第一天（u1 两条记录）
	if rows[0].InputTokens != 300 || rows[0].OutputTokens != 150 {
		t.Errorf("day1: input=%d output=%d, want 300/150", rows[0].InputTokens, rows[0].OutputTokens)
	}

	// 测试用户过滤
	userRows, err := repo.DailyTokens(from, to, "u1")
	if err != nil {
		t.Fatalf("DailyTokens(u1): %v", err)
	}

	if len(userRows) != 2 {
		t.Errorf("expected 2 days for u1, got %d", len(userRows))
	}
}

func TestUsageRepo_DailyCost(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := NewUsageRepo(gormDB, logger)

	now := time.Now()
	day1 := now.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	day2 := now.Truncate(24 * time.Hour)

	logs := []UsageLog{
		{RequestID: "r1", UserID: "u1", CostUSD: 0.01, CreatedAt: day1.Add(time.Hour)},
		{RequestID: "r2", UserID: "u1", CostUSD: 0.02, CreatedAt: day1.Add(2 * time.Hour)},
		{RequestID: "r3", UserID: "u2", CostUSD: 0.05, CreatedAt: day2.Add(time.Hour)},
	}

	for _, log := range logs {
		if err := gormDB.Create(&log).Error; err != nil {
			t.Fatalf("Create log: %v", err)
		}
	}

	from := day1
	to := day2.Add(24 * time.Hour)
	rows, err := repo.DailyCost(from, to, "")
	if err != nil {
		t.Fatalf("DailyCost: %v", err)
	}

	if len(rows) != 2 {
		t.Errorf("expected 2 days, got %d", len(rows))
	}

	// 验证第一天费用
	if rows[0].CostUSD < 0.029 || rows[0].CostUSD > 0.031 {
		t.Errorf("day1 cost = %.4f, want ~0.03", rows[0].CostUSD)
	}

	// 测试用户过滤
	userRows, err := repo.DailyCost(from, to, "u1")
	if err != nil {
		t.Fatalf("DailyCost(u1): %v", err)
	}

	if len(userRows) != 1 {
		t.Errorf("expected 1 day for u1, got %d", len(userRows))
	}
}
