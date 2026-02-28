package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// 测试辅助：创建带数据的 in-memory DB
// ---------------------------------------------------------------------------

func setupQuotaTest(t *testing.T) (*db.UserRepo, *db.GroupRepo, *db.UsageRepo, *db.UsageWriter, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	return db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		writer,
		func() { cancel(); writer.Wait() }
}

// ---------------------------------------------------------------------------
// TestCheckerNoGroup — 无分组用户 → 无限制（不 block）
// ---------------------------------------------------------------------------

func TestCheckerNoGroup(t *testing.T) {
	userRepo, _, usageRepo, writer, stop := setupQuotaTest(t)
	defer stop()

	// 创建无分组用户
	user := &db.User{ID: "u1", Username: "alice", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	_ = writer // no usage

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	if err := checker.Check(context.Background(), "u1"); err != nil {
		t.Errorf("expected nil for ungrouped user, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerGroupNoLimits — 分组无配额 → 无限制
// ---------------------------------------------------------------------------

func TestCheckerGroupNoLimits(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	grp := &db.Group{ID: "g1", Name: "unlimited"} // DailyTokenLimit = nil
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g1"
	user := &db.User{ID: "u2", Username: "bob", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	if err := checker.Check(context.Background(), "u2"); err != nil {
		t.Errorf("expected nil for group with no limits, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerDailyLimitNotExceeded — 用量未超每日限制 → 放行
// ---------------------------------------------------------------------------

func TestCheckerDailyLimitNotExceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	daily := int64(1000)
	grp := &db.Group{ID: "g2", Name: "team", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g2"
	user := &db.User{ID: "u3", Username: "carol", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 写入 500 tokens（低于 1000 限额）
	writer.Record(db.UsageRecord{
		RequestID: "req-1", UserID: "u3",
		InputTokens: 300, OutputTokens: 200,
		StatusCode: 200, CreatedAt: time.Now(),
	})
	stop() // cancel + wait

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	if err := checker.Check(context.Background(), "u3"); err != nil {
		t.Errorf("expected nil (under limit), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerDailyLimitExceeded — 今日用量超出限制 → 429 ExceededError
// ---------------------------------------------------------------------------

func TestCheckerDailyLimitExceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	daily := int64(100)
	grp := &db.Group{ID: "g3", Name: "trial", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g3"
	user := &db.User{ID: "u4", Username: "dave", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 写入 200 tokens（超出 100 限额）
	writer.Record(db.UsageRecord{
		RequestID: "req-2", UserID: "u4",
		InputTokens: 120, OutputTokens: 80,
		StatusCode: 200, CreatedAt: time.Now(),
	})
	stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	err := checker.Check(context.Background(), "u4")
	if err == nil {
		t.Fatal("expected ExceededError, got nil")
	}
	var qErr *ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *ExceededError, got %T: %v", err, err)
	}
	if qErr.Kind != "daily" {
		t.Errorf("Kind = %q, want 'daily'", qErr.Kind)
	}
	if qErr.Current != 200 {
		t.Errorf("Current = %d, want 200", qErr.Current)
	}
	if qErr.Limit != 100 {
		t.Errorf("Limit = %d, want 100", qErr.Limit)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerMonthlyLimitExceeded — 本月用量超出月度限制
// ---------------------------------------------------------------------------

func TestCheckerMonthlyLimitExceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	monthly := int64(500)
	grp := &db.Group{ID: "g4", Name: "basic", MonthlyTokenLimit: &monthly}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g4"
	user := &db.User{ID: "u5", Username: "eve", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 本月两条记录共 600 tokens
	now := time.Now()
	writer.Record(db.UsageRecord{
		RequestID: "req-3a", UserID: "u5",
		InputTokens: 300, OutputTokens: 100,
		StatusCode: 200, CreatedAt: now.Add(-2 * 24 * time.Hour),
	})
	writer.Record(db.UsageRecord{
		RequestID: "req-3b", UserID: "u5",
		InputTokens: 150, OutputTokens: 50,
		StatusCode: 200, CreatedAt: now,
	})
	stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	err := checker.Check(context.Background(), "u5")
	if err == nil {
		t.Fatal("expected ExceededError, got nil")
	}
	var qErr *ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *ExceededError, got %T: %v", err, err)
	}
	if qErr.Kind != "monthly" {
		t.Errorf("Kind = %q, want 'monthly'", qErr.Kind)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerCacheHit — 第二次 Check 使用缓存（不查 DB）
// ---------------------------------------------------------------------------

func TestCheckerCacheHit(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	daily := int64(10000)
	grp := &db.Group{ID: "g5", Name: "premium", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g5"
	user := &db.User{ID: "u6", Username: "frank", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	cache := NewQuotaCache(time.Minute)
	checker := NewChecker(logger, userRepo, usageRepo, cache)

	// 第一次 Check — 写入缓存
	if err := checker.Check(context.Background(), "u6"); err != nil {
		t.Fatalf("first Check: %v", err)
	}

	// 手动向缓存写入"接近上限"的数据
	cache.set("u6", 9500, 9500)

	// 第二次 Check — 从缓存取（9500 < 10000，仍未超限）
	if err := checker.Check(context.Background(), "u6"); err != nil {
		t.Errorf("second Check with cache: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerCacheInvalidate — InvalidateCache 触发重新查 DB
// ---------------------------------------------------------------------------

func TestCheckerCacheInvalidate(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	daily := int64(100)
	grp := &db.Group{ID: "g6", Name: "tiny", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g6"
	user := &db.User{ID: "u7", Username: "grace", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	cache := NewQuotaCache(time.Minute)
	checker := NewChecker(logger, userRepo, usageRepo, cache)

	// 先以空 DB 检查（低用量，通过）
	if err := checker.Check(context.Background(), "u7"); err != nil {
		t.Fatalf("initial Check: %v", err)
	}

	// 写入超限 token
	writer.Record(db.UsageRecord{
		RequestID: "req-4", UserID: "u7",
		InputTokens: 80, OutputTokens: 60,
		StatusCode: 200, CreatedAt: time.Now(),
	})
	stop()

	// 不驱逐缓存 → 仍命中旧缓存 → 仍通过
	if err := checker.Check(context.Background(), "u7"); err != nil {
		t.Errorf("expected cache hit (old data), got %v", err)
	}

	// 驱逐缓存 → 重新查 DB → 应超限
	checker.InvalidateCache("u7")
	err := checker.Check(context.Background(), "u7")
	if err == nil {
		t.Fatal("expected ExceededError after cache invalidation, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestMiddleware429 — 超限时 Middleware 返回 429
// ---------------------------------------------------------------------------

func TestMiddleware429(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	daily := int64(50)
	grp := &db.Group{ID: "g7", Name: "mini", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g7"
	user := &db.User{ID: "u8", Username: "heidi", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	writer.Record(db.UsageRecord{
		RequestID: "req-5", UserID: "u8",
		InputTokens: 40, OutputTokens: 30,
		StatusCode: 200, CreatedAt: time.Now(),
	})
	stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	called := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := NewMiddleware(logger, checker, func(r *http.Request) string {
		return "u8"
	})
	handler := mw(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rr.Code)
	}
	if called {
		t.Error("next handler should not have been called")
	}
	if rr.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("X-RateLimit-Limit header should be set")
	}
}

// ---------------------------------------------------------------------------
// TestMiddlewarePassThrough — 未超限时放行
// ---------------------------------------------------------------------------

func TestMiddlewarePassThrough(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	daily := int64(10000)
	grp := &db.Group{ID: "g8", Name: "big", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g8"
	user := &db.User{ID: "u9", Username: "ivan", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	called := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := NewMiddleware(logger, checker, func(r *http.Request) string {
		return "u9"
	})
	handler := mw(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !called {
		t.Error("next handler should have been called")
	}
}

// ---------------------------------------------------------------------------
// TestMiddlewareNoUserID — 无 user_id 时放行（防御性）
// ---------------------------------------------------------------------------

func TestMiddlewareNoUserID(t *testing.T) {
	_, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, nil, usageRepo, NewQuotaCache(time.Minute))

	called := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := NewMiddleware(logger, checker, func(r *http.Request) string {
		return "" // 无 user_id
	})
	handler := mw(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("next handler should be called when userID is empty")
	}
}

// ---------------------------------------------------------------------------
// TestCheckerRequestSize_Allowed — 请求 max_tokens 未超限 → 放行
// ---------------------------------------------------------------------------

func TestCheckerRequestSize_Allowed(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	limit := int64(4096)
	grp := &db.Group{ID: "gs1", Name: "sized", MaxTokensPerRequest: &limit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gs1"
	user := &db.User{ID: "us1", Username: "sizeuser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// max_tokens=2048 < 4096 → 通过
	if err := checker.CheckRequestSize("us1", 2048); err != nil {
		t.Errorf("expected nil (under limit), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerRequestSize_Exceeded — 请求 max_tokens 超限 → ExceededError
// ---------------------------------------------------------------------------

func TestCheckerRequestSize_Exceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	limit := int64(1000)
	grp := &db.Group{ID: "gs2", Name: "small", MaxTokensPerRequest: &limit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gs2"
	user := &db.User{ID: "us2", Username: "biguser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	err := checker.CheckRequestSize("us2", 2000)
	if err == nil {
		t.Fatal("expected ExceededError, got nil")
	}
	var qErr *ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *ExceededError, got %T", err)
	}
	if qErr.Kind != "request_size" {
		t.Errorf("Kind = %q, want 'request_size'", qErr.Kind)
	}
	if qErr.Current != 2000 {
		t.Errorf("Current = %d, want 2000", qErr.Current)
	}
	if qErr.Limit != 1000 {
		t.Errorf("Limit = %d, want 1000", qErr.Limit)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerRequestSize_Zero — max_tokens=0 时跳过检查
// ---------------------------------------------------------------------------

func TestCheckerRequestSize_Zero(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	limit := int64(100)
	grp := &db.Group{ID: "gs3", Name: "strict", MaxTokensPerRequest: &limit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gs3"
	user := &db.User{ID: "us3", Username: "nomax", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// max_tokens=0 → 不指定，跳过检查
	if err := checker.CheckRequestSize("us3", 0); err != nil {
		t.Errorf("max_tokens=0 should skip check, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckerConcurrent_Allowed — 并发数未超限 → 放行
// ---------------------------------------------------------------------------

func TestCheckerConcurrent_Allowed(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	limit := 3
	grp := &db.Group{ID: "gc1", Name: "conc", ConcurrentRequests: &limit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gc1"
	user := &db.User{ID: "uc1", Username: "concuser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	release, err := checker.TryAcquireConcurrent("uc1")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	defer release()
}

// ---------------------------------------------------------------------------
// TestCheckerConcurrent_Exceeded — 并发数超限 → ExceededError
// ---------------------------------------------------------------------------

func TestCheckerConcurrent_Exceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	limit := 1
	grp := &db.Group{ID: "gc2", Name: "solo", ConcurrentRequests: &limit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gc2"
	user := &db.User{ID: "uc2", Username: "solouser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 第一次：占用唯一槽
	release1, err := checker.TryAcquireConcurrent("uc2")
	if err != nil {
		t.Fatalf("first acquire: expected nil, got %v", err)
	}

	// 第二次：超限
	_, err2 := checker.TryAcquireConcurrent("uc2")
	if err2 == nil {
		t.Fatal("second acquire: expected ExceededError, got nil")
	}
	var qErr *ExceededError
	if !errors.As(err2, &qErr) {
		t.Fatalf("expected *ExceededError, got %T", err2)
	}
	if qErr.Kind != "concurrent" {
		t.Errorf("Kind = %q, want 'concurrent'", qErr.Kind)
	}

	// 释放后可再次获取
	release1()
	release3, err3 := checker.TryAcquireConcurrent("uc2")
	if err3 != nil {
		t.Errorf("after release: expected nil, got %v", err3)
	}
	release3()
}

// ---------------------------------------------------------------------------
// TestCheckerConcurrent_NoLimit — 无并发限制时不拒绝
// ---------------------------------------------------------------------------

func TestCheckerConcurrent_NoLimit(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	// 分组无 ConcurrentRequests 限制
	grp := &db.Group{ID: "gc3", Name: "free"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "gc3"
	user := &db.User{ID: "uc3", Username: "freeuser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	for i := 0; i < 10; i++ {
		release, err := checker.TryAcquireConcurrent("uc3")
		if err != nil {
			t.Errorf("iter %d: expected nil (no limit), got %v", i, err)
		}
		defer release()
	}
}
