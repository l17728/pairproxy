package db

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// openTestRepoForExport 创建内存 SQLite 数据库供测试使用。
func openTestRepoForExport(t *testing.T) (*UsageRepo, func()) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewUsageRepo(gormDB, logger)
	return repo, func() {
		sqlDB, _ := gormDB.DB()
		sqlDB.Close()
	}
}

// insertLog 直接写入一条 UsageLog，bypass 异步 writer。
func insertLog(t *testing.T, repo *UsageRepo, requestID, userID string, in, out int, at time.Time) {
	t.Helper()
	log := UsageLog{
		RequestID:    requestID,
		UserID:       userID,
		InputTokens:  in,
		OutputTokens: out,
		TotalTokens:  in + out,
		StatusCode:   200,
		IsStreaming:  false,
		SourceNode:   "local",
		CreatedAt:    at,
	}
	if err := repo.db.Create(&log).Error; err != nil {
		t.Fatalf("insertLog: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestExportLogs_Empty：无数据时回调不被调用，无 error
// ---------------------------------------------------------------------------

func TestExportLogs_Empty(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 31, 23, 59, 59, 0, time.UTC)

	called := 0
	err := repo.ExportLogs(from, to, func(l UsageLog) error {
		called++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 0 {
		t.Errorf("callback called %d times, want 0", called)
	}
}

// ---------------------------------------------------------------------------
// TestExportLogs_AllInRange：所有记录在范围内
// ---------------------------------------------------------------------------

func TestExportLogs_AllInRange(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	base := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		insertLog(t, repo, "req-"+string(rune('a'+i)), "user-1", 100+i, 50+i, base.Add(time.Duration(i)*time.Minute))
	}

	from := base.Add(-time.Hour)
	to := base.Add(time.Hour)

	var collected []UsageLog
	err := repo.ExportLogs(from, to, func(l UsageLog) error {
		collected = append(collected, l)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collected) != 10 {
		t.Errorf("collected %d rows, want 10", len(collected))
	}
}

// ---------------------------------------------------------------------------
// TestExportLogs_Pagination：超过 pageSize 时分批查询，全部返回
// ---------------------------------------------------------------------------

func TestExportLogs_Pagination(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	// 插入 1100 条（> 默认 pageSize 500 × 2）
	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1100; i++ {
		id := "req-" + time.Now().Format("150405.000000") + string(rune('A'+i%26))
		// 每条用唯一的 requestID
		log := UsageLog{
			RequestID:   "pagtest-" + string([]byte{byte('a' + i/26), byte('a' + i%26)}),
			UserID:      "user-pag",
			InputTokens: i + 1,
			TotalTokens: i + 1,
			StatusCode:  200,
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
			SourceNode:  "local",
		}
		_ = id
		if err := repo.db.Create(&log).Error; err != nil {
			t.Fatalf("insert log %d: %v", i, err)
		}
	}

	from := base.Add(-time.Hour)
	to := base.Add(time.Hour * 24)

	count := 0
	err := repo.ExportLogs(from, to, func(l UsageLog) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1100 {
		t.Errorf("exported %d rows, want 1100", count)
	}
}

// ---------------------------------------------------------------------------
// TestExportLogs_CallbackError：回调返回 error 时立即停止
// ---------------------------------------------------------------------------

func TestExportLogs_CallbackError(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertLog(t, repo, "err-req-"+string(rune('a'+i)), "user-err", 10, 5, base.Add(time.Duration(i)*time.Minute))
	}

	from := base.Add(-time.Hour)
	to := base.Add(time.Hour)

	called := 0
	errStop := &exportStopErr{}
	err := repo.ExportLogs(from, to, func(l UsageLog) error {
		called++
		if called >= 3 {
			return errStop
		}
		return nil
	})
	if err != errStop {
		t.Errorf("expected errStop, got %v", err)
	}
	if called != 3 {
		t.Errorf("callback called %d times, want 3", called)
	}
}

type exportStopErr struct{}

func (e *exportStopErr) Error() string { return "export stopped by callback" }

// ---------------------------------------------------------------------------
// TestExportLogs_TimeFilter：范围外的记录不被导出
// ---------------------------------------------------------------------------

func TestExportLogs_TimeFilter(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	base := time.Date(2024, 4, 15, 0, 0, 0, 0, time.UTC)

	// 范围内 3 条
	for i := 0; i < 3; i++ {
		insertLog(t, repo, "in-"+string(rune('a'+i)), "user-f", 10, 5, base.Add(time.Duration(i)*time.Hour))
	}
	// 范围外 2 条（更早）
	insertLog(t, repo, "before-1", "user-f", 10, 5, base.Add(-24*time.Hour))
	insertLog(t, repo, "before-2", "user-f", 10, 5, base.Add(-48*time.Hour))

	from := base.Add(-time.Hour)
	to := base.Add(10 * time.Hour)

	count := 0
	err := repo.ExportLogs(from, to, func(_ UsageLog) error { count++; return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("exported %d rows, want 3", count)
	}
}

// ---------------------------------------------------------------------------
// TestExportLogs_OrderAsc：记录按 created_at ASC 顺序返回
// ---------------------------------------------------------------------------

func TestExportLogs_OrderAsc(t *testing.T) {
	repo, cleanup := openTestRepoForExport(t)
	defer cleanup()

	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	// 逆序插入
	for i := 4; i >= 0; i-- {
		insertLog(t, repo, "ord-"+string(rune('a'+i)), "user-ord", i*10, i*5, base.Add(time.Duration(i)*time.Minute))
	}

	from := base.Add(-time.Hour)
	to := base.Add(time.Hour)

	var ts []time.Time
	_ = repo.ExportLogs(from, to, func(l UsageLog) error {
		ts = append(ts, l.CreatedAt)
		return nil
	})

	for i := 1; i < len(ts); i++ {
		if ts[i].Before(ts[i-1]) {
			t.Errorf("records not in ASC order at index %d: %v before %v", i, ts[i], ts[i-1])
		}
	}
}
