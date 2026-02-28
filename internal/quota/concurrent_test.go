package quota

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// TestConcurrentCounter_TryAcquire：基本获取/释放
// ---------------------------------------------------------------------------

func TestConcurrentCounter_TryAcquire(t *testing.T) {
	c := NewConcurrentCounter()

	// 无限制（limit=0）：始终允许
	if !c.TryAcquire("user1", 0) {
		t.Error("limit=0 should always allow")
	}
	// 负数 limit：始终允许
	if !c.TryAcquire("user2", -1) {
		t.Error("limit<0 should always allow")
	}

	// 正限制：前 N 次允许，第 N+1 次拒绝
	const limit = 3
	for i := 0; i < limit; i++ {
		if !c.TryAcquire("user3", limit) {
			t.Errorf("try %d: should be allowed (limit=%d)", i+1, limit)
		}
	}
	if c.TryAcquire("user3", limit) {
		t.Errorf("try %d: should be denied (limit=%d)", limit+1, limit)
	}
	if c.Count("user3") != limit {
		t.Errorf("count = %d, want %d", c.Count("user3"), limit)
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentCounter_Release：释放后可重新获取
// ---------------------------------------------------------------------------

func TestConcurrentCounter_Release(t *testing.T) {
	c := NewConcurrentCounter()

	const limit = 2
	c.TryAcquire("u", limit)
	c.TryAcquire("u", limit)

	// 已满，第三次应失败
	if c.TryAcquire("u", limit) {
		t.Error("should be denied before release")
	}

	// 释放一个槽
	c.Release("u")
	if c.Count("u") != 1 {
		t.Errorf("count after release = %d, want 1", c.Count("u"))
	}

	// 现在应该允许
	if !c.TryAcquire("u", limit) {
		t.Error("should be allowed after release")
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentCounter_Release_NoUnderflow：多次 Release 不会下溢
// ---------------------------------------------------------------------------

func TestConcurrentCounter_Release_NoUnderflow(t *testing.T) {
	c := NewConcurrentCounter()
	// Release 一个从未 Acquire 的用户
	c.Release("ghost")
	if c.Count("ghost") != 0 {
		t.Errorf("count = %d, want 0 (no underflow)", c.Count("ghost"))
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentCounter_Concurrent：并发安全
// ---------------------------------------------------------------------------

func TestConcurrentCounter_Concurrent(t *testing.T) {
	c := NewConcurrentCounter()
	const limit = 5
	const goroutines = 20

	var wg sync.WaitGroup
	acquired := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			acquired[idx] = c.TryAcquire("shared", limit)
		}(i)
	}
	wg.Wait()

	count := 0
	for _, ok := range acquired {
		if ok {
			count++
		}
	}
	if count != limit {
		t.Errorf("acquired %d slots, want exactly %d", count, limit)
	}
	if c.Count("shared") != limit {
		t.Errorf("counter = %d, want %d", c.Count("shared"), limit)
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentCounter_MultiUser：不同用户计数互不影响
// ---------------------------------------------------------------------------

func TestConcurrentCounter_MultiUser(t *testing.T) {
	c := NewConcurrentCounter()
	const limit = 1

	c.TryAcquire("alice", limit)
	// alice 已满，但 bob 不受影响
	if !c.TryAcquire("bob", limit) {
		t.Error("bob should be allowed independently of alice")
	}
	if c.TryAcquire("alice", limit) {
		t.Error("alice should be denied (already at limit)")
	}
}
