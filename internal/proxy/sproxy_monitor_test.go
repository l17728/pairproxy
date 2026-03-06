package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/alert"
)

// ---------------------------------------------------------------------------
// 测试辅助：构建最小 SProxy + 注册 webhook 收集告警
// ---------------------------------------------------------------------------

func newMonitorTestSProxy(t *testing.T) *SProxy {
	t.Helper()
	return &SProxy{}
}

type collectedEvents struct {
	count atomic.Int32
	kinds []string
	mu    atomic.Pointer[[]string]
}

func newEventCollector(t *testing.T) (*alert.Notifier, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	n := alert.NewNotifier(zaptest.NewLogger(t), srv.URL)
	return n, srv, &received
}

// ---------------------------------------------------------------------------
// TestMonitorFiresOnThresholdCrossing
// 活跃请求数从 0 升至 threshold 时，应触发一次 EventHighLoad。
// ---------------------------------------------------------------------------

func TestMonitorFiresOnThresholdCrossing(t *testing.T) {
	sp := newMonitorTestSProxy(t)
	n, _, received := newEventCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startActiveRequestsMonitor(ctx, sp, 5, n, "test-node", zaptest.NewLogger(t), 20*time.Millisecond)

	// 活跃请求数超过阈值
	sp.activeRequests.Store(5)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && received.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	if got := received.Load(); got != 1 {
		t.Errorf("expected 1 EventHighLoad, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestMonitorFiresRecovery
// 活跃请求数从超过阈值恢复到阈值以下时，应触发 EventLoadRecovered。
// ---------------------------------------------------------------------------

func TestMonitorFiresRecovery(t *testing.T) {
	sp := newMonitorTestSProxy(t)
	n, _, received := newEventCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 先置为超阈值状态
	sp.activeRequests.Store(10)
	startActiveRequestsMonitor(ctx, sp, 5, n, "test-node", zaptest.NewLogger(t), 20*time.Millisecond)

	// 等第一次 tick 触发 high_load
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && received.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}

	// 恢复到阈值以下
	sp.activeRequests.Store(2)

	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && received.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}

	if got := received.Load(); got < 2 {
		t.Errorf("expected high_load + load_recovered (2 events), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestMonitorNoStormOnSustainedHighLoad
// 持续超载时只触发一次 EventHighLoad，不产生告警风暴。
// ---------------------------------------------------------------------------

func TestMonitorNoStormOnSustainedHighLoad(t *testing.T) {
	sp := newMonitorTestSProxy(t)
	n, _, received := newEventCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sp.activeRequests.Store(10) // 持续超阈值
	startActiveRequestsMonitor(ctx, sp, 5, n, "test-node", zaptest.NewLogger(t), 20*time.Millisecond)

	// 等 3 个 tick 时间
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond) // 等 goroutine 退出

	if got := received.Load(); got != 1 {
		t.Errorf("expected exactly 1 EventHighLoad (no storm), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestMonitorDisabledWhenThresholdZero
// threshold=0 时不启动 goroutine，不发送任何告警。
// ---------------------------------------------------------------------------

func TestMonitorDisabledWhenThresholdZero(t *testing.T) {
	sp := newMonitorTestSProxy(t)
	n, _, received := newEventCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sp.activeRequests.Store(999)
	startActiveRequestsMonitor(ctx, sp, 0, n, "test-node", zaptest.NewLogger(t), 20*time.Millisecond)

	time.Sleep(100 * time.Millisecond)

	if got := received.Load(); got != 0 {
		t.Errorf("threshold=0 should disable monitor, but got %d events", got)
	}
}

// ---------------------------------------------------------------------------
// TestMonitorDisabledWhenNotifierNil
// notifier=nil 时不启动 goroutine（即使 threshold > 0）。
// ---------------------------------------------------------------------------

func TestMonitorDisabledWhenNotifierNil(t *testing.T) {
	sp := newMonitorTestSProxy(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sp.activeRequests.Store(999)
	// 不应 panic 也不应有任何副作用
	startActiveRequestsMonitor(ctx, sp, 5, nil, "test-node", zaptest.NewLogger(t), 20*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	// 没有 notifier，无法断言事件，但确认不 panic 即可
}
