package auth

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Blacklist.StartCleanup — 后台 goroutine 可被 context cancel 停止
// ---------------------------------------------------------------------------

func TestBlacklist_StartCleanup_StopsOnContextCancel(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	ctx, cancel := context.WithCancel(context.Background())
	bl.StartCleanup(ctx)

	// 马上取消，goroutine 应停止
	cancel()
	// 等待一小段时间确保 goroutine 退出（不阻塞测试）
	time.Sleep(20 * time.Millisecond)
}

func TestBlacklist_StartCleanup_GoroutineOnce(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 多次调用 StartCleanup，goroutine 只应启动一次（sync.Once 保证），不应 panic
	bl.StartCleanup(ctx)
	bl.StartCleanup(ctx)
	bl.StartCleanup(ctx)
}

// ---------------------------------------------------------------------------
// Manager.StartCleanup — 委托给 Blacklist.StartCleanup
// ---------------------------------------------------------------------------

func TestManager_StartCleanup_Delegates(t *testing.T) {
	logger := testLogger(t)
	m, err := NewManager(logger, "test-secret-key-for-cleanup-32ch")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 不应 panic；goroutine 只启动一次
	m.StartCleanup(ctx)
	m.StartCleanup(ctx)
	cancel()
	time.Sleep(10 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Blacklist cleanup 详细路径
// ---------------------------------------------------------------------------

func TestBlacklist_Cleanup_RemovesOnlyExpired(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	// 过去已过期
	bl.Add("expired-jti", time.Now().Add(-100*time.Millisecond))
	// 未来仍有效
	bl.Add("valid-jti", time.Now().Add(10*time.Minute))

	bl.cleanup()

	bl.mu.RLock()
	_, hasExpired := bl.entries["expired-jti"]
	_, hasValid := bl.entries["valid-jti"]
	bl.mu.RUnlock()

	if hasExpired {
		t.Error("expired entry should have been removed by cleanup")
	}
	if !hasValid {
		t.Error("valid entry should remain after cleanup")
	}
}

func TestBlacklist_Cleanup_EmptyMap_NoOp(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)
	// 空 map 上调用 cleanup 不应 panic
	bl.cleanup()
}

func TestBlacklist_IsBlocked_LazyExpiry(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	// 直接写入已过期记录（绕过 Add 的时间判断）
	bl.mu.Lock()
	bl.entries["lazy-jti"] = blacklistEntry{expiry: time.Now().Add(-time.Second)}
	bl.mu.Unlock()

	// IsBlocked 应通过懒过期判断返回 false
	if bl.IsBlocked("lazy-jti") {
		t.Error("expired JTI should not be considered blocked (lazy expiry)")
	}
}
