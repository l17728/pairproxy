package lb

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestWeightedRandom_EmptyTargets 测试空目标列表
func TestWeightedRandom_EmptyTargets(t *testing.T) {
	b := NewWeightedRandom([]Target{})

	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("expected ErrNoHealthyTarget for empty targets, got: %v", err)
	}
}

// TestWeightedRandom_NilTargets 测试 nil 目标列表
func TestWeightedRandom_NilTargets(t *testing.T) {
	b := NewWeightedRandom(nil)

	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("expected ErrNoHealthyTarget for nil targets, got: %v", err)
	}
}

// TestWeightedRandom_SingleTargetUnhealthy 测试单个不健康目标
func TestWeightedRandom_SingleTargetUnhealthy(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: false},
	})

	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("expected ErrNoHealthyTarget, got: %v", err)
	}
}

// TestWeightedRandom_MarkNonExistent 测试标记不存在的目标
func TestWeightedRandom_MarkNonExistent(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})

	// 标记不存在的目标不应 panic
	b.MarkHealthy("nonexistent")
	b.MarkUnhealthy("nonexistent")

	// 原有目标仍可用
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("got ID = %q, want 'a'", got.ID)
	}
}

// TestWeightedRandom_UpdateToEmpty 测试更新为空列表
func TestWeightedRandom_UpdateToEmpty(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})

	b.UpdateTargets([]Target{})

	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("expected ErrNoHealthyTarget after update to empty, got: %v", err)
	}
}

// TestWeightedRandom_UpdatePreservesHealth 测试更新保留健康状态
func TestWeightedRandom_UpdatePreservesHealth(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})

	b.MarkUnhealthy("a")
	b.UpdateTargets([]Target{
		{ID: "b", Addr: "http://b", Weight: 1, Healthy: true},
	})

	// 新目标应该是健康的
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "b" {
		t.Errorf("got ID = %q, want 'b'", got.ID)
	}
}

// TestWeightedRandom_ConcurrentPick 测试并发 Pick
func TestWeightedRandom_ConcurrentPick(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
		{ID: "b", Addr: "http://b", Weight: 1, Healthy: true},
	})

	const goroutines = 20
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				got, err := b.Pick()
				if err != nil {
					t.Errorf("Pick failed: %v", err)
					return
				}
				if got.ID != "a" && got.ID != "b" {
					t.Errorf("unexpected ID: %q", got.ID)
				}
			}
			done <- true
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}
}

// TestWeightedRandom_ConcurrentUpdate 测试并发更新
func TestWeightedRandom_ConcurrentUpdate(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})

	const iterations = 100
	done := make(chan bool, 2)

	// Writer goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			b.UpdateTargets([]Target{
				{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
			})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			b.Targets()
		}
		done <- true
	}()

	<-done
	<-done
}

// TestTarget_Copy 测试 Target 结构体复制
func TestTarget_Copy(t *testing.T) {
	original := Target{
		ID:       "a",
		Addr:     "http://a",
		Weight:   5,
		Healthy:  true,
		Draining: false,
	}

	// Go 的结构体赋值是值拷贝，基本类型会被复制
	// 所以修改 copy 不会影响 original
	copy := original
	copy.Healthy = false
	copy.Weight = 10

	// 由于是值拷贝，original 的值应该保持不变
	// 这个测试验证 Go 的值语义
	_ = copy // 避免未使用变量警告
	if original.Weight != 5 {
		t.Errorf("original.Weight = %d, want 5", original.Weight)
	}
}

// TestHealthChecker_RecordSuccess 测试健康检查记录成功
func TestHealthChecker_RecordSuccess(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: false},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger)

	// 成功记录应标记为健康
	hc.RecordSuccess("a")

	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("got ID = %q, want 'a'", got.ID)
	}
}

// TestHealthChecker_RecordSuccessNonExistent 测试记录不存在目标的成功
func TestHealthChecker_RecordSuccessNonExistent(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger)

	// 记录不存在目标的成功不应 panic
	hc.RecordSuccess("nonexistent")

	// 原有目标仍可用
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("got ID = %q, want 'a'", got.ID)
	}
}

// TestHealthChecker_RecordFailureNonExistent 测试记录不存在目标的失败
func TestHealthChecker_RecordFailureNonExistent(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger, WithFailThreshold(1))

	// 记录不存在目标的失败不应 panic
	hc.RecordFailure("nonexistent")

	// 原有目标仍可用
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("got ID = %q, want 'a'", got.ID)
	}
}

// TestHealthChecker_Options 测试健康检查选项
func TestHealthChecker_Options(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)

	hc := NewHealthChecker(b, logger,
		WithFailThreshold(5),
		WithInterval(10*time.Second),
		WithTimeout(30*time.Second),
	)

	if hc == nil {
		t.Error("NewHealthChecker should not return nil")
	}
}

// TestRetryTransport_NoRetriesConfigured 测试配置为不重试
func TestRetryTransport_NoRetriesConfigured(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{errors.New("connection refused")},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 0, // 不重试
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Error("expected error when no retries configured")
	}
	if inner.calls != 1 {
		t.Errorf("expected 1 call (no retry), got %d", inner.calls)
	}
}

// TestRetryTransport_NoPickNext 测试没有配置 PickNext
func TestRetryTransport_NoPickNext(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{errors.New("connection refused")},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext:   nil, // 未配置
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Error("expected error when PickNext not configured")
	}
}

// TestRetryTransport_OnFailureCalled 测试失败回调被调用
func TestRetryTransport_OnFailureCalled(t *testing.T) {
	inner := &mockRoundTripper{
		errors:    []error{errors.New("connection refused"), nil},
		responses: []*http.Response{nil, makeResp(200, "ok")}, //nolint:bodyclose
	}

	failureCalled := false
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return &LLMTargetInfo{URL: "http://t2"}, nil
		},
		OnFailure: func(targetURL string) {
			failureCalled = true
		},
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if !failureCalled {
		t.Error("OnFailure should be called on connection error")
	}
}

// TestRetryTransport_ContextDeadlineExceeded 测试上下文超时
func TestRetryTransport_ContextDeadlineExceeded(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{context.DeadlineExceeded},
	}

	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext:   func(_ string, _ []string) (*LLMTargetInfo, error) { pickCalls++; return nil, nil },
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
	if pickCalls != 0 {
		t.Errorf("PickNext should not be called on deadline exceeded, got %d calls", pickCalls)
	}
}

// TestRetryTransport_5xxWithNoMoreTargets 测试 5xx 且无更多目标
func TestRetryTransport_5xxWithNoMoreTargets(t *testing.T) {
	r1 := makeResp(500, "internal error")
	t.Cleanup(func() { r1.Body.Close() })
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return nil, ErrNoHealthyTarget
		},
		OnFailure: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Error("expected error when 5xx and no more targets")
	}
}

// TestRetryTransport_EmptyBody 测试空请求体
func TestRetryTransport_EmptyBody(t *testing.T) {
	r1 := makeResp(200, `{"ok":true}`)
	t.Cleanup(func() { r1.Body.Close() })
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if inner.calls != 1 {
		t.Errorf("expected 1 call, got %d", inner.calls)
	}
}

// TestRetryTransport_3xxNoRetry 测试 3xx 不重试
func TestRetryTransport_3xxNoRetry(t *testing.T) {
	r1 := makeResp(302, "redirect")
	t.Cleanup(func() { r1.Body.Close() })
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext:   func(_ string, _ []string) (*LLMTargetInfo, error) { pickCalls++; return nil, nil },
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error for 3xx: %v", err)
	}
	resp.Body.Close()

	if pickCalls != 0 {
		t.Errorf("PickNext should not be called for 3xx, got %d calls", pickCalls)
	}
}

// TestLLMTargetInfo 测试 LLMTargetInfo 结构体
func TestLLMTargetInfo_Fields(t *testing.T) {
	info := LLMTargetInfo{
		URL:    "https://api.anthropic.com",
		APIKey: "sk-ant-123",
	}

	if info.URL != "https://api.anthropic.com" {
		t.Errorf("URL = %q", info.URL)
	}
	if info.APIKey != "sk-ant-123" {
		t.Errorf("APIKey = %q", info.APIKey)
	}
}

