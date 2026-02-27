package lb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
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
