package quota

// 补充覆盖未覆盖分支：
//   - Check: userID 不存在 → bypass；user 不在 DB → bypass；RPM=0 跳过速率限制；DB SumTokens 错误 → fail-open
//   - CheckRequestSize: 用户不存在/DB 错误 → fail-open；limit=0 → 无限制
//   - TryAcquireConcurrent: 用户不存在/DB 错误 → fail-open；limit=0 → 无限制
//   - getUsage: 缓存未命中时查 DB 并写入缓存

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// Check — 用户不存在（未知 userID）→ 放行（fail-open）
// ---------------------------------------------------------------------------

func TestCoverage_Check_UserNotFound(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// "ghost-user" 从未创建，GetByID 返回 nil, nil
	if err := checker.Check(context.Background(), "ghost-user"); err != nil {
		t.Errorf("unknown user should be bypassed (fail-open), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Check — RPM 限制为 0 时跳过速率限制检查
// ---------------------------------------------------------------------------

func TestCoverage_Check_RPM_Zero_Skipped(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	rpm := 0 // 0 表示不限速
	grp := &db.Group{ID: "g-rpm0", Name: "no-rpm", RequestsPerMinute: &rpm}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-rpm0"
	user := &db.User{ID: "u-rpm0", Username: "rpm0user", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 即使连续发送 1000 次，RPM=0 表示不限速，全部放行
	for i := 0; i < 1000; i++ {
		if err := checker.Check(context.Background(), "u-rpm0"); err != nil {
			t.Errorf("RPM=0 should not block; iter=%d err=%v", i, err)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Check — RPM 非 nil、非零但未超限
// ---------------------------------------------------------------------------

func TestCoverage_Check_RPM_NotExceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	rpm := 1000
	grp := &db.Group{ID: "g-rpmhigh", Name: "high-rpm", RequestsPerMinute: &rpm}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-rpmhigh"
	user := &db.User{ID: "u-rpmhigh", Username: "rpmhigh", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 第一次请求在 1000 RPM 限额内
	if err := checker.Check(context.Background(), "u-rpmhigh"); err != nil {
		t.Errorf("expected nil (under RPM limit), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckRequestSize — requestedMaxTokens <= 0 → 跳过
// ---------------------------------------------------------------------------

func TestCoverage_CheckRequestSize_Negative(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 负数 → 直接返回 nil（相当于未指定）
	if err := checker.CheckRequestSize("any-user", -1); err != nil {
		t.Errorf("negative max_tokens should be skipped, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckRequestSize — 用户不存在 → fail-open
// ---------------------------------------------------------------------------

func TestCoverage_CheckRequestSize_UserNotFound(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 用户不存在 → fail-open
	if err := checker.CheckRequestSize("ghost", 5000); err != nil {
		t.Errorf("unknown user should be bypassed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckRequestSize — group.MaxTokensPerRequest = nil → 无限制
// ---------------------------------------------------------------------------

func TestCoverage_CheckRequestSize_NoLimit(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	// 分组没有 MaxTokensPerRequest 限制
	grp := &db.Group{ID: "g-nolimit", Name: "nolimit"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-nolimit"
	user := &db.User{ID: "u-nolimit", Username: "nolimituser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 任意大的 max_tokens 都应通过
	if err := checker.CheckRequestSize("u-nolimit", 1_000_000); err != nil {
		t.Errorf("no limit should pass, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckRequestSize — MaxTokensPerRequest = 0 → 无限制（边界）
// ---------------------------------------------------------------------------

func TestCoverage_CheckRequestSize_LimitZero(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	zero := int64(0)
	grp := &db.Group{ID: "g-zero-limit", Name: "zerolimit", MaxTokensPerRequest: &zero}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-zero-limit"
	user := &db.User{ID: "u-zero-limit", Username: "zerolimituser", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// limit=0 视为无限制
	if err := checker.CheckRequestSize("u-zero-limit", 99999); err != nil {
		t.Errorf("limit=0 should be treated as unlimited, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckRequestSize — 用户无分组 → fail-open
// ---------------------------------------------------------------------------

func TestCoverage_CheckRequestSize_NoGroup(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	user := &db.User{ID: "u-nogrp-rs", Username: "nogrprs", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	if err := checker.CheckRequestSize("u-nogrp-rs", 9999); err != nil {
		t.Errorf("user without group should be fail-open, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TryAcquireConcurrent — 用户不存在 → noop, nil
// ---------------------------------------------------------------------------

func TestCoverage_TryAcquireConcurrent_UserNotFound(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	release, err := checker.TryAcquireConcurrent("ghost-concurrent")
	if err != nil {
		t.Errorf("unknown user should be fail-open, got %v", err)
	}
	// release 是 noop，调用不应 panic
	release()
}

// ---------------------------------------------------------------------------
// TryAcquireConcurrent — 用户无分组 → noop, nil
// ---------------------------------------------------------------------------

func TestCoverage_TryAcquireConcurrent_NoGroup(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	user := &db.User{ID: "u-nogrp-cc", Username: "nogrpcc", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	release, err := checker.TryAcquireConcurrent("u-nogrp-cc")
	if err != nil {
		t.Errorf("user without group should be fail-open, got %v", err)
	}
	release()
}

// ---------------------------------------------------------------------------
// TryAcquireConcurrent — limit=0 → noop, nil（视为无限制）
// ---------------------------------------------------------------------------

func TestCoverage_TryAcquireConcurrent_LimitZero(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	zero := 0
	grp := &db.Group{ID: "g-cc-zero", Name: "cc-zero", ConcurrentRequests: &zero}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-cc-zero"
	user := &db.User{ID: "u-cc-zero", Username: "cczero", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// limit=0 → noop，任意并发均放行
	for i := 0; i < 5; i++ {
		release, err := checker.TryAcquireConcurrent("u-cc-zero")
		if err != nil {
			t.Errorf("iter %d: limit=0 should not block, got %v", i, err)
		}
		defer release()
	}
}

// ---------------------------------------------------------------------------
// getUsage — 缓存未命中时从 DB 查询并写回缓存
// ---------------------------------------------------------------------------

func TestCoverage_GetUsage_CacheMissAndSet(t *testing.T) {
	userRepo, groupRepo, usageRepo, writer, stop := setupQuotaTest(t)

	daily := int64(50000)
	grp := &db.Group{ID: "g-getusage", Name: "getusage", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-getusage"
	user := &db.User{ID: "u-getusage", Username: "getusage", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	writer.Record(db.UsageRecord{
		RequestID: "req-gu1", UserID: "u-getusage",
		InputTokens: 100, OutputTokens: 50,
		StatusCode: 200, CreatedAt: time.Now(),
	})
	stop() // flush

	logger := zaptest.NewLogger(t)
	cache := NewQuotaCache(time.Minute)
	checker := NewChecker(logger, userRepo, usageRepo, cache)

	// 首次查询：缓存未命中 → 应查 DB
	if err := checker.Check(context.Background(), "u-getusage"); err != nil {
		t.Errorf("expected nil (under limit), got %v", err)
	}

	// 缓存命中计数应该从 0 开始（首次是 miss）
	if cache.Misses() == 0 {
		t.Error("expected at least one cache miss on first call")
	}

	// 第二次查询：缓存命中
	if err := checker.Check(context.Background(), "u-getusage"); err != nil {
		t.Errorf("expected nil on second call, got %v", err)
	}

	if cache.Hits() == 0 {
		t.Error("expected at least one cache hit on second call")
	}
}

// ---------------------------------------------------------------------------
// ExceededError.Error() — 格式化字符串覆盖
// ---------------------------------------------------------------------------

func TestCoverage_ExceededError_Message(t *testing.T) {
	resetAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	e := &ExceededError{
		Kind:    "daily",
		Current: 500,
		Limit:   100,
		ResetAt: resetAt,
	}
	msg := e.Error()
	if msg == "" {
		t.Error("ExceededError.Error() returned empty string")
	}
	// 检查包含关键信息
	for _, want := range []string{"daily", "500", "100"} {
		found := false
		for i := 0; i+len(want) <= len(msg); i++ {
			if msg[i:i+len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ExceededError.Error() = %q, expected to contain %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Check — 仅有 RPM 限制（无 token 限额）
// ---------------------------------------------------------------------------

func TestCoverage_Check_OnlyRPMLimit_Exceeded(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	rpm := 1
	grp := &db.Group{ID: "g-only-rpm", Name: "only-rpm", RequestsPerMinute: &rpm}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-only-rpm"
	user := &db.User{ID: "u-only-rpm", Username: "onlyrpm", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// 先消耗掉唯一 RPM 配额
	checker.rateLimiter.Allow("u-only-rpm", 1)

	// 第二次应触发 rate_limit ExceededError
	err := checker.Check(context.Background(), "u-only-rpm")
	if err == nil {
		t.Fatal("expected rate_limit ExceededError, got nil")
	}
	var qErr *ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *ExceededError, got %T", err)
	}
	if qErr.Kind != "rate_limit" {
		t.Errorf("Kind = %q, want 'rate_limit'", qErr.Kind)
	}
}
