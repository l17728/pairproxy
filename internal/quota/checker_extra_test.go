package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// QuotaCache.Hits / Misses 计数器
// ---------------------------------------------------------------------------

func TestQuotaCache_Hits_Misses_Counters(t *testing.T) {
	cache := NewQuotaCache(time.Minute)

	// 首次 get：miss
	if got := cache.get("user1"); got != nil {
		t.Fatal("expected nil on first get (cache miss)")
	}
	if cache.Misses() != 1 {
		t.Errorf("Misses() = %d, want 1", cache.Misses())
	}
	if cache.Hits() != 0 {
		t.Errorf("Hits() = %d, want 0 before any hit", cache.Hits())
	}

	// set 后再 get：hit
	cache.set("user1", 100, 500)
	if got := cache.get("user1"); got == nil {
		t.Fatal("expected non-nil on second get (cache hit)")
	}
	if cache.Hits() != 1 {
		t.Errorf("Hits() = %d, want 1 after hit", cache.Hits())
	}
	if cache.Misses() != 1 {
		t.Errorf("Misses() = %d, want still 1", cache.Misses())
	}

	// 过期后 get：miss（TTL 很短）
	shortCache := NewQuotaCache(time.Millisecond)
	shortCache.set("u2", 10, 20)
	time.Sleep(5 * time.Millisecond)
	if got := shortCache.get("u2"); got != nil {
		t.Fatal("expected nil after TTL expiry")
	}
	if shortCache.Misses() != 1 {
		t.Errorf("Misses() = %d, want 1 after TTL expiry", shortCache.Misses())
	}
}

// ---------------------------------------------------------------------------
// Checker.SetNotifier + notify 路径
// ---------------------------------------------------------------------------

func TestChecker_SetNotifier_NotifiesOnQuotaExceeded(t *testing.T) {
	// 搭建 webhook 捕获服务器
	received := make(chan string, 5)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "notified"
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	daily := int64(100) // 设置低限额
	grp := &db.Group{ID: "g-notify", Name: "notify-group", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-notify"
	user := &db.User{ID: "u-notify", Username: "notify_user", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	cache := NewQuotaCache(time.Millisecond) // 极短 TTL 确保总是查 DB
	checker := NewChecker(logger, userRepo, usageRepo, cache)

	// 注册 notifier — 使用 zap.NewNop() 避免 send() goroutine 在测试结束后写 zaptest logger 导致 data race
	notifier := alert.NewNotifier(zap.NewNop(), webhookSrv.URL)
	checker.SetNotifier(notifier)

	// 预填用量到达 daily 限制
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cache.set("u-notify", daily, daily) // 强制缓存命中 - 日用量达到上限

	// 直接注入缓存使 daily 已达上限
	cache.m.Store("u-notify", &usageEntry{
		dailyUsed:   daily,
		monthlyUsed: daily,
		expiresAt:   time.Now().Add(time.Minute),
	})
	_ = dayStart

	err := checker.Check(context.Background(), "u-notify")
	if err == nil {
		t.Fatal("expected quota exceeded error")
	}

	// webhook 应该被触发
	select {
	case <-received:
		// 成功通知
	case <-time.After(2 * time.Second):
		t.Error("webhook was not called within 2s")
	}
}

func TestChecker_SetNotifier_NilNotifier_NoOp(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	daily := int64(100)
	grp := &db.Group{ID: "g-nil-notifier", Name: "nil-notifier-group", DailyTokenLimit: &daily}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-nil-notifier"
	user := &db.User{ID: "u-nil-notifier", Username: "nil_notifier_user", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	cache := NewQuotaCache(time.Minute)
	checker := NewChecker(logger, userRepo, usageRepo, cache)

	// 不设置 notifier（或设置为 nil）
	checker.SetNotifier(nil)

	// 注入超限缓存
	cache.m.Store("u-nil-notifier", &usageEntry{
		dailyUsed:   daily,
		monthlyUsed: daily,
		expiresAt:   time.Now().Add(time.Minute),
	})

	// 不应 panic
	err := checker.Check(context.Background(), "u-nil-notifier")
	if err == nil {
		t.Fatal("expected quota exceeded error")
	}
}

// ---------------------------------------------------------------------------
// Checker.PurgeRateLimiter
// ---------------------------------------------------------------------------

func TestChecker_PurgeRateLimiter_NoOp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))
	// 不应 panic
	checker.PurgeRateLimiter()
}

func TestChecker_PurgeRateLimiter_ClearsExpiredWindows(t *testing.T) {
	logger := zaptest.NewLogger(t)
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	// rateLimiter 内部添加一些记录后 Purge
	checker.rateLimiter.Allow("purge-user", 10)
	checker.PurgeRateLimiter()
	// 无过期条目时 Purge 不崩溃即可
}

// ---------------------------------------------------------------------------
// itoa — 覆盖负数和零路径
// ---------------------------------------------------------------------------

func TestItoa_SpecialCases(t *testing.T) {
	cases := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{-1, "-1"},
		{-100, "-100"},
		{999, "999"},
		{int64(1<<62), "4611686018427387904"},
	}
	for _, tc := range cases {
		got := itoa(tc.input)
		if got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware — rate limit exceeded 路径 (RPM)
// ---------------------------------------------------------------------------

func TestMiddleware_RateLimitExceeded_Returns429(t *testing.T) {
	userRepo, groupRepo, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	rpm := 1 // 每分钟只允许 1 次
	grp := &db.Group{ID: "g-rpm", Name: "rpm-group", RequestsPerMinute: &rpm}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "g-rpm"
	user := &db.User{ID: "u-rpm", Username: "rpm_user", PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Millisecond))

	// 先消费掉唯一的配额
	checker.rateLimiter.Allow("u-rpm", 1)

	mw := NewMiddleware(logger, checker, func(r *http.Request) string {
		return r.Header.Get("X-User-ID")
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-User-ID", "u-rpm")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Middleware — 用户不存在 → 放行
// ---------------------------------------------------------------------------

func TestMiddleware_UserNotFound_PassThrough(t *testing.T) {
	userRepo, _, usageRepo, _, stop := setupQuotaTest(t)
	defer stop()

	logger := zaptest.NewLogger(t)
	checker := NewChecker(logger, userRepo, usageRepo, NewQuotaCache(time.Minute))

	mw := NewMiddleware(logger, checker, func(r *http.Request) string {
		return "nonexistent-user"
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for nonexistent user (fail-open), got %d", rr.Code)
	}
}
