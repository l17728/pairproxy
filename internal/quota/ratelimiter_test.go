package quota

import (
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter()
	limits := []WindowLimit{{Window: time.Minute, Limit: 3}}

	// 前 3 次请求应通过
	for i := 0; i < 3; i++ {
		allowed, _, count, _ := rl.AllowWindows("u1", limits)
		if !allowed {
			t.Errorf("request %d: expected allowed, got denied (count=%d)", i+1, count)
		}
		if count != 0 {
			t.Errorf("request %d: exceeded count should be 0 when allowed, got %d", i+1, count)
		}
	}

	// 第 4 次应被拒绝
	allowed, exceeded, count, _ := rl.AllowWindows("u1", limits)
	if allowed {
		t.Error("4th request should be denied")
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if exceeded.Limit != 3 {
		t.Errorf("exceeded.Limit = %d, want 3", exceeded.Limit)
	}
}

func TestRateLimiterDifferentUsers(t *testing.T) {
	rl := NewRateLimiter()
	limits := []WindowLimit{{Window: time.Minute, Limit: 1}}

	// u1 达到限额
	rl.AllowWindows("u1", limits)
	denied, _, _, _ := rl.AllowWindows("u1", limits)
	if denied {
		t.Error("u1 should be denied after limit")
	}

	// u2 不受 u1 影响
	allowed, _, _, _ := rl.AllowWindows("u2", limits)
	if !allowed {
		t.Error("u2 should be allowed (independent window)")
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	rl := NewRateLimiter()
	shortWindow := 50 * time.Millisecond
	limits := []WindowLimit{{Window: shortWindow, Limit: 1}}

	// 消耗限额
	rl.AllowWindows("u3", limits)
	denied, _, _, _ := rl.AllowWindows("u3", limits)
	if denied {
		t.Error("u3 should be denied after consuming limit")
	}

	// 等待窗口过期
	time.Sleep(60 * time.Millisecond)

	// 现在应该允许
	allowed, _, _, _ := rl.AllowWindows("u3", limits)
	if !allowed {
		t.Error("u3 should be allowed after window expiry")
	}
}

func TestRateLimiterResetAt(t *testing.T) {
	rl := NewRateLimiter()
	limits := []WindowLimit{{Window: time.Minute, Limit: 1}}

	// 消耗限额，获取 resetAt
	rl.AllowWindows("u4", limits)
	_, _, _, resetAt := rl.AllowWindows("u4", limits)

	expected := time.Now().Add(59 * time.Second)
	if resetAt.Before(expected) {
		t.Errorf("resetAt = %v, expected >= %v", resetAt, expected)
	}
}

func TestRateLimiterBoundaryExact(t *testing.T) {
	rl := NewRateLimiter()
	const limit = 5
	limits := []WindowLimit{{Window: time.Minute, Limit: limit}}

	// 恰好允许 limit 个请求
	for i := 0; i < limit; i++ {
		allowed, _, _, _ := rl.AllowWindows("boundary", limits)
		if !allowed {
			t.Errorf("request %d should be allowed, got denied", i+1)
		}
	}

	// 第 limit+1 个必须被拒
	allowed, _, count, _ := rl.AllowWindows("boundary", limits)
	if allowed {
		t.Errorf("request %d should be denied, got allowed", limit+1)
	}
	if count != limit {
		t.Errorf("count on denial = %d, want %d", count, limit)
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	const (
		limit      = 10
		goroutines = 20
	)
	rl := NewRateLimiter()
	limits := []WindowLimit{{Window: time.Minute, Limit: limit}}

	allowed := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			ok, _, _, _ := rl.AllowWindows("concurrent", limits)
			allowed <- ok
		}()
	}

	allowedCount := 0
	for i := 0; i < goroutines; i++ {
		if <-allowed {
			allowedCount++
		}
	}

	if allowedCount > limit {
		t.Errorf("concurrent AllowWindows() allowed %d > limit %d", allowedCount, limit)
	}
}

func TestRateLimiterPurge(t *testing.T) {
	rl := NewRateLimiter()
	shortWindow := 10 * time.Millisecond
	limits := []WindowLimit{{Window: shortWindow, Limit: 10}}

	rl.AllowWindows("u5", limits)
	if len(rl.windows) == 0 {
		t.Fatal("expected entry before purge")
	}

	// 等窗口过期，再 Purge
	time.Sleep(20 * time.Millisecond)
	rl.Purge(shortWindow)
	if len(rl.windows) != 0 {
		t.Errorf("expected empty windows after purge, got %d entries", len(rl.windows))
	}
}

func TestRateLimiterMultiWindow(t *testing.T) {
	rl := NewRateLimiter()
	limits := []WindowLimit{
		{Window: time.Minute, Limit: 10},
		{Window: 15 * time.Minute, Limit: 20},
	}

	// 10 次请求用完 1 分钟限额
	for i := 0; i < 10; i++ {
		allowed, _, _, _ := rl.AllowWindows("mw", limits)
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 第 11 次被 1 分钟窗口拦截
	allowed, exceeded, count, _ := rl.AllowWindows("mw", limits)
	if allowed {
		t.Error("11th request should be denied by 1m window")
	}
	if exceeded.Window != time.Minute {
		t.Errorf("exceeded window = %v, want 1m", exceeded.Window)
	}
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
}

func TestRateLimiterMultiWindowSecondExceeded(t *testing.T) {
	rl := NewRateLimiter()
	// 1m limit is generous; 15m limit is tight
	limits := []WindowLimit{
		{Window: time.Minute, Limit: 100},
		{Window: 15 * time.Minute, Limit: 3},
	}

	for i := 0; i < 3; i++ {
		allowed, _, _, _ := rl.AllowWindows("mw2", limits)
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 4th blocked by 15m window
	allowed, exceeded, _, _ := rl.AllowWindows("mw2", limits)
	if allowed {
		t.Error("4th request should be denied by 15m window")
	}
	if exceeded.Window != 15*time.Minute {
		t.Errorf("exceeded window = %v, want 15m", exceeded.Window)
	}
}

func TestRateLimiterEmptyLimits(t *testing.T) {
	rl := NewRateLimiter()
	allowed, _, _, _ := rl.AllowWindows("u", []WindowLimit{})
	if !allowed {
		t.Error("empty limits should always allow")
	}
}
