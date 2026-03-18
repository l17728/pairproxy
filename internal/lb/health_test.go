package lb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/alert"
)

// makeBalancer 创建只有一个目标的 Balancer，初始健康状态可配置。
func makeBalancer(healthy bool) *WeightedRandomBalancer {
	return NewWeightedRandom([]Target{
		{ID: "sp-1", Addr: "", Weight: 1, Healthy: healthy},
	})
}

// ---------------------------------------------------------------------------
// 被动熔断测试
// ---------------------------------------------------------------------------

func TestPassiveCircuitBreaker(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(3))

	// 前 2 次失败不应触发熔断
	hc.RecordFailure("sp-1")
	hc.RecordFailure("sp-1")
	if _, err := b.Pick(); err != nil {
		t.Errorf("should still be healthy after 2 failures, got err: %v", err)
	}

	// 第 3 次失败触发熔断
	hc.RecordFailure("sp-1")
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Errorf("after 3 failures want ErrNoHealthyTarget, got: %v", err)
	}
}

func TestPassiveRecovery(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(2))

	hc.RecordFailure("sp-1")
	hc.RecordFailure("sp-1")
	// 应已熔断
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Fatalf("expected circuit open, got: %v", err)
	}

	// 恢复后应能正常选取
	hc.RecordSuccess("sp-1")
	if _, err := b.Pick(); err != nil {
		t.Errorf("after RecordSuccess want healthy, got: %v", err)
	}
}

func TestFailureCountResetOnSuccess(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(3))

	hc.RecordFailure("sp-1")
	hc.RecordFailure("sp-1")
	hc.RecordSuccess("sp-1") // 重置计数

	// 再失败 2 次不应触发熔断（因为计数已重置）
	hc.RecordFailure("sp-1")
	hc.RecordFailure("sp-1")
	if _, err := b.Pick(); err != nil {
		t.Errorf("counter should have reset; want healthy, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 主动健康检查测试
// ---------------------------------------------------------------------------

func TestActiveHealthCheckOK(t *testing.T) {
	// 启动一个始终返回 200 的 mock 服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := NewWeightedRandom([]Target{
		{ID: "srv", Addr: srv.URL, Weight: 1, Healthy: false}, // 初始不健康
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithInterval(50*time.Millisecond),
		WithTimeout(2*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	hc.Start(ctx)

	// 等待至少一次检查完成
	time.Sleep(100 * time.Millisecond)

	if _, err := b.Pick(); err != nil {
		t.Errorf("target should be healthy after active check, got: %v", err)
	}
}

func TestActiveHealthCheckFail(t *testing.T) {
	// 启动一个始终返回 500 的 mock 服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewWeightedRandom([]Target{
		{ID: "srv", Addr: srv.URL, Weight: 1, Healthy: true}, // 初始健康
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithInterval(30*time.Millisecond),
		WithTimeout(2*time.Second),
		WithFailThreshold(2),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	hc.Start(ctx)

	// 等待至少 2 次检查完成（failThreshold=2）
	time.Sleep(150 * time.Millisecond)

	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Errorf("target should be unhealthy after consecutive 500s, got: %v", err)
	}
}

func TestActiveHealthCheckUnreachable(t *testing.T) {
	// 不存在的地址
	b := NewWeightedRandom([]Target{
		{ID: "dead", Addr: "http://127.0.0.1:19999", Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithInterval(30*time.Millisecond),
		WithTimeout(100*time.Millisecond),
		WithFailThreshold(2),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	hc.Start(ctx)

	time.Sleep(400 * time.Millisecond)

	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Errorf("unreachable target should be marked unhealthy, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// recordFailure — 告警路径（SetNotifier + notifier != nil）
// ---------------------------------------------------------------------------

// TestRecordFailure_AlertOnThreshold 验证失败次数达到阈值时触发告警。
func TestRecordFailure_AlertOnThreshold(t *testing.T) {
	// 搭建 webhook 服务器捕获告警
	received := make(chan string, 5)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "notified"
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(2))

	// SetNotifier 覆盖
	notifier := alert.NewNotifier(logger, webhookSrv.URL)
	hc.SetNotifier(notifier)

	// 第 1 次失败，未到阈值，不触发告警
	hc.RecordFailure("sp-1")
	select {
	case <-received:
		t.Error("should not alert before threshold")
	case <-time.After(50 * time.Millisecond):
		// 正常：未触发告警
	}

	// 第 2 次失败，达到阈值，应触发告警
	hc.RecordFailure("sp-1")
	select {
	case <-received:
		// 成功捕获到告警
	case <-time.After(500 * time.Millisecond):
		t.Error("expected alert notification after reaching threshold, but none received")
	}
}

// TestSetNotifier_NilNotifier 验证 SetNotifier(nil) 不会 panic。
func TestSetNotifier_NilNotifier(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(1))

	// nil notifier 不触发告警，不应 panic
	hc.SetNotifier(nil)
	hc.RecordFailure("sp-1")
}

// TestRecordFailure_AlertNodeRecovered 验证节点恢复时 notifier 也触发 EventNodeRecovered。
func TestRecordFailure_AlertNodeRecovered(t *testing.T) {
	received := make(chan string, 5)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "notified"
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(1))
	// 使用 zap.NewNop() 给 notifier，避免 send() goroutine 在测试结束后写 zaptest logger 导致 data race
	notifier := alert.NewNotifier(zap.NewNop(), webhookSrv.URL)
	hc.SetNotifier(notifier)

	// 触发熔断（会发告警 EventNodeDown）
	hc.RecordFailure("sp-1")
	select {
	case <-received:
		// EventNodeDown 告警收到
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected EventNodeDown alert")
	}

	// 恢复（会发告警 EventNodeRecovered）
	hc.RecordSuccess("sp-1")
	select {
	case <-received:
		// EventNodeRecovered 告警收到
	case <-time.After(500 * time.Millisecond):
		t.Error("expected EventNodeRecovered alert after RecordSuccess")
	}
}

// ---------------------------------------------------------------------------
// WithRecoveryDelay — 自动恢复路径
// ---------------------------------------------------------------------------

// TestWithRecoveryDelay_AutoRecover 验证 recoveryDelay > 0 时，节点在 delay 后自动恢复。
func TestWithRecoveryDelay_AutoRecover(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	recoveryDelay := 100 * time.Millisecond
	hc := NewHealthChecker(b, logger,
		WithFailThreshold(1),
		WithRecoveryDelay(recoveryDelay),
	)

	// 失败达阈值，节点变为不健康
	hc.RecordFailure("sp-1")
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Fatalf("expected circuit open, got: %v", err)
	}

	// 等待 recoveryDelay 过后，节点应自动恢复
	time.Sleep(recoveryDelay + 50*time.Millisecond)

	if _, err := b.Pick(); err != nil {
		t.Errorf("target should auto-recover after recoveryDelay, got: %v", err)
	}
}

// TestWithRecoveryDelay_RecoverySkippedIfAlreadyRecovered 验证若节点已被 RecordSuccess 恢复，
// 自动恢复 goroutine 不会重复操作（失败计数已被重置，goroutine 会 skip）。
func TestWithRecoveryDelay_RecoverySkippedIfAlreadyRecovered(t *testing.T) {
	b := makeBalancer(true)
	logger := zaptest.NewLogger(t)
	recoveryDelay := 100 * time.Millisecond
	hc := NewHealthChecker(b, logger,
		WithFailThreshold(1),
		WithRecoveryDelay(recoveryDelay),
	)

	// 触发熔断
	hc.RecordFailure("sp-1")
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Fatalf("expected circuit open")
	}

	// 在自动恢复前手动调用 RecordSuccess 重置计数
	hc.RecordSuccess("sp-1")
	if _, err := b.Pick(); err != nil {
		t.Fatalf("should be healthy after RecordSuccess: %v", err)
	}

	// 等待自动恢复 goroutine 完成（不应 panic 也不应 double-mark）
	time.Sleep(recoveryDelay + 50*time.Millisecond)
	// 节点仍健康即可
	if _, err := b.Pick(); err != nil {
		t.Errorf("target should remain healthy: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WithHealthPath — 验证 healthPath 字段被设置（通过主动检查间接验证）
// ---------------------------------------------------------------------------

// TestWithHealthPath_CustomPath 验证使用 WithHealthPath 后，主动检查访问自定义路径。
func TestWithHealthPath_CustomPath(t *testing.T) {
	customPath := "/custom-health"
	checked := make(chan struct{}, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == customPath {
			w.WriteHeader(http.StatusOK)
			checked <- struct{}{}
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := NewWeightedRandom([]Target{
		{ID: "target", Addr: srv.URL, Weight: 1, Healthy: false},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithInterval(30*time.Millisecond),
		WithTimeout(2*time.Second),
		WithHealthPath(customPath), // 覆盖默认 /health
	)

	// 验证 healthPath 字段被正确设置
	if hc.healthPath != customPath {
		t.Errorf("healthPath = %q, want %q", hc.healthPath, customPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	hc.Start(ctx)

	// 等待至少一次检查，然后额外等一点让 health checker 处理响应
	select {
	case <-checked:
		// 自定义路径被访问
	case <-time.After(300 * time.Millisecond):
		t.Error("custom health path was not checked")
	}
	// 等待 health checker goroutine 处理响应并更新健康状态
	time.Sleep(50 * time.Millisecond)

	if _, err := b.Pick(); err != nil {
		t.Errorf("target should be healthy after custom path check, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WithHealthPaths — 验证仅有 path 的节点被主动检查
// ---------------------------------------------------------------------------

// TestWithHealthPaths_OnlyConfiguredTargetsChecked 验证使用 WithHealthPaths 后，
// 仅有显式 path 的 target 被主动检查。
func TestWithHealthPaths_OnlyConfiguredTargetsChecked(t *testing.T) {
	checkedTargets := make(chan string, 10)

	// srv-a 有显式 path，srv-b 没有
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedTargets <- "a"
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedTargets <- "b"
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: srvA.URL, Weight: 1, Healthy: true},
		{ID: "b", Addr: srvB.URL, Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithInterval(30*time.Millisecond),
		WithTimeout(2*time.Second),
		WithHealthPaths(map[string]string{
			"a": "/health", // 只为 a 配置主动检查路径
			// b 不配置 path，不参与主动检查
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	hc.Start(ctx)

	// 等待检查完成
	time.Sleep(150 * time.Millisecond)

	// 收集所有被检查的 target
	seenTargets := map[string]bool{}
	done := false
	for !done {
		select {
		case id := <-checkedTargets:
			seenTargets[id] = true
		default:
			done = true
		}
	}

	// a 应被检查，b 不应被检查
	if !seenTargets["a"] {
		t.Error("target 'a' should have been actively checked")
	}
	if seenTargets["b"] {
		t.Error("target 'b' should NOT be actively checked (no path configured)")
	}
}

// ---------------------------------------------------------------------------
// Pick — 所有节点 draining+unhealthy 混合时兜底返回 ErrNoHealthyTarget
// ---------------------------------------------------------------------------

// TestPick_AllDrainingAndUnhealthyMixed 验证排水和不健康节点混合时，Pick 返回 ErrNoHealthyTarget。
func TestPick_AllDrainingAndUnhealthyMixed(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true, Draining: true},   // 排水
		{ID: "b", Addr: "http://b", Weight: 1, Healthy: false, Draining: false}, // 不健康
		{ID: "c", Addr: "http://c", Weight: 1, Healthy: false, Draining: true},  // 排水且不健康
	})

	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("Pick() with all draining/unhealthy targets should return ErrNoHealthyTarget, got: %v", err)
	}
}
