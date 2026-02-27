package quota

import (
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter()

	// 前 3 次请求应通过（limit=3）
	for i := 0; i < 3; i++ {
		allowed, count := rl.Allow("u1", 3)
		if !allowed {
			t.Errorf("request %d: expected allowed, got denied (count=%d)", i+1, count)
		}
		if count != i+1 {
			t.Errorf("request %d: count = %d, want %d", i+1, count, i+1)
		}
	}

	// 第 4 次应被拒绝
	allowed, count := rl.Allow("u1", 3)
	if allowed {
		t.Error("4th request should be denied")
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestRateLimiterDifferentUsers(t *testing.T) {
	rl := NewRateLimiter()

	// u1 达到限额
	rl.Allow("u1", 1)
	denied, _ := rl.Allow("u1", 1)
	if denied {
		t.Error("u1 should be denied after limit")
	}

	// u2 不受 u1 影响
	allowed, _ := rl.Allow("u2", 1)
	if !allowed {
		t.Error("u2 should be allowed (independent window)")
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	rl := &RateLimiter{
		windows: make(map[string][]time.Time),
		window:  50 * time.Millisecond, // 非常短的窗口用于测试
	}

	// 消耗限额
	rl.Allow("u3", 1)
	denied, _ := rl.Allow("u3", 1)
	if denied {
		t.Error("u3 should be denied after consuming limit")
	}

	// 等待窗口过期
	time.Sleep(60 * time.Millisecond)

	// 现在应该允许
	allowed, count := rl.Allow("u3", 1)
	if !allowed {
		t.Error("u3 should be allowed after window expiry")
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRateLimiterResetAt(t *testing.T) {
	rl := NewRateLimiter()

	// 无历史记录时 ResetAt ≈ now
	before := time.Now()
	resetAt := rl.ResetAt("unknown")
	if resetAt.Before(before) {
		t.Error("ResetAt for unknown user should be >= now")
	}

	// 有记录时 ResetAt ≈ 1 分钟后
	rl.Allow("u4", 10)
	resetAt = rl.ResetAt("u4")
	expected := time.Now().Add(59 * time.Second)
	if resetAt.Before(expected) {
		t.Errorf("ResetAt = %v, expected >= %v", resetAt, expected)
	}
}

func TestRateLimiterPurge(t *testing.T) {
	rl := &RateLimiter{
		windows: make(map[string][]time.Time),
		window:  10 * time.Millisecond,
	}

	rl.Allow("u5", 10)
	if len(rl.windows) == 0 {
		t.Fatal("expected entry before purge")
	}

	// 等窗口过期，再 Purge
	time.Sleep(20 * time.Millisecond)
	rl.Purge()
	if len(rl.windows) != 0 {
		t.Errorf("expected empty windows after purge, got %d entries", len(rl.windows))
	}
}
