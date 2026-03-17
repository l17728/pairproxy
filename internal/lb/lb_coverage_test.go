package lb

// lb_coverage_test.go — 补充覆盖 Pick（后备路径）、RoundTrip 未覆盖分支、
// checkOneWithPath 非 200 和请求创建失败路径。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Pick — 后备路径（所有节点总权重 > 0 但随机值恰好落到末尾时兜底返回）
// 通过设置大量均匀权重节点反复 Pick，触发后备分支（理论上的边界情况）
// ---------------------------------------------------------------------------

func TestCoverage_Pick_FallbackPath(t *testing.T) {
	// 创建大量节点，使加权随机结果覆盖所有路径
	targets := make([]Target, 50)
	for i := range targets {
		targets[i] = Target{
			ID:      string(rune('a' + i%26)),
			Addr:    "http://node",
			Weight:  1,
			Healthy: true,
		}
	}
	b := NewWeightedRandom(targets)

	// 大量 Pick 调用，验证永不返回 error
	for i := 0; i < 10000; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick() failed at iter %d: %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() returned nil at iter %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Pick — 单个节点（覆盖 r<0 立即返回路径）
// ---------------------------------------------------------------------------

func TestCoverage_Pick_SingleNodeAlwaysReturned(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "only", Addr: "http://only", Weight: 1, Healthy: true},
	})
	for i := 0; i < 100; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got.ID != "only" {
			t.Errorf("iter %d: got %q, want 'only'", i, got.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 5xx 且 PickNext=nil → 返回错误
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_5xx_NoPickNext(t *testing.T) {
	r1 := makeResp(503, `{"error":"service_unavailable"}`)
	defer r1.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 0, // 不重试
		PickNext:   nil,
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error for 5xx with no PickNext, got nil")
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 连接错误且 PickNext=nil → 返回错误
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_ConnError_NoPickNext(t *testing.T) {
	connErr := errors.New("connection refused")
	inner := &mockRoundTripper{
		errors: []error{connErr},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 0,
		PickNext:   nil,
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error for conn error with no PickNext")
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 5xx 且 PickNext 无可用节点（返回 ErrNoHealthyTarget）
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_5xx_PickNextExhausted(t *testing.T) {
	r1 := makeResp(500, `{"error":"internal"}`)
	defer r1.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return nil, ErrNoHealthyTarget
		},
		OnFailure: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error when PickNext exhausted")
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 连接错误后 PickNext 无可用节点
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_ConnError_PickNextExhausted(t *testing.T) {
	connErr := errors.New("dial timeout")
	inner := &mockRoundTripper{
		errors: []error{connErr},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return nil, ErrNoHealthyTarget
		},
		OnFailure: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error when PickNext exhausted after conn error")
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 请求 body 为 nil（无需 buffer）
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_NilBody(t *testing.T) {
	r1 := makeResp(200, `{"ok":true}`)
	defer r1.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 0,
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodGet, "http://target1/v1/models", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// RoundTrip — context.DeadlineExceeded 不重试
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_DeadlineExceeded_NoRetry(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{context.DeadlineExceeded},
	}
	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			pickCalls++
			return &LLMTargetInfo{URL: "http://t2"}, nil
		},
		Logger: zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if pickCalls != 0 {
		t.Errorf("PickNext should not be called on DeadlineExceeded, got %d calls", pickCalls)
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — OnFailure 在 5xx 时被调用
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_OnFailureCalled(t *testing.T) {
	r1 := makeResp(500, `{}`)
	defer r1.Body.Close()
	r2 := makeResp(200, `{"ok":true}`)
	defer r2.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1, r2},
	}

	failureCalled := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return &LLMTargetInfo{URL: "http://t2", APIKey: ""}, nil
		},
		OnFailure: func(_ string) { failureCalled++ },
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	resp.Body.Close()

	if failureCalled != 1 {
		t.Errorf("OnFailure called %d times, want 1", failureCalled)
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — 重试时 PickNext 返回无 APIKey → 不设 Authorization 头
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_RetryNoAPIKey(t *testing.T) {
	connErr := errors.New("conn refused")
	r2 := makeResp(200, `{"ok":true}`)
	defer r2.Body.Close()
	inner := &mockRoundTripper{
		errors:    []error{connErr, nil},
		responses: []*http.Response{nil, r2},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			// 无 APIKey
			return &LLMTargetInfo{URL: "http://t2", APIKey: ""}, nil
		},
		OnFailure: func(_ string) {},
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", bytes.NewBufferString(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// RoundTrip — 所有重试后最后为 5xx（无 err）
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_All5xx_Exhausted(t *testing.T) {
	r1 := makeResp(500, `{}`)
	defer r1.Body.Close()
	r2 := makeResp(502, `{}`)
	defer r2.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1, r2},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return &LLMTargetInfo{URL: "http://t2"}, nil
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
		t.Fatal("expected error when all targets exhausted with 5xx")
	}
}

// ---------------------------------------------------------------------------
// checkOneWithPath — 非 200 状态码
// ---------------------------------------------------------------------------

func TestCoverage_CheckOneWithPath_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	b := NewWeightedRandom([]Target{
		{ID: "target-503", Addr: srv.URL, Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithFailThreshold(1),
	)

	// 直接调用 checkOneWithPath
	target := Target{ID: "target-503", Addr: srv.URL, Weight: 1, Healthy: true}
	hc.checkOneWithPath(target, "/health")

	// 失败计数应达到阈值，节点标记不健康
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Errorf("target should be unhealthy after non-200 response, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// checkOneWithPath — 请求创建失败（无效 URL）
// ---------------------------------------------------------------------------

func TestCoverage_CheckOneWithPath_InvalidURL(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "bad-url", Addr: "://invalid", Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	hc := NewHealthChecker(b, logger,
		WithFailThreshold(1),
	)

	// 无效 URL 导致请求创建失败
	target := Target{ID: "bad-url", Addr: "://invalid", Weight: 1, Healthy: true}
	hc.checkOneWithPath(target, "/health")

	// 失败计数应达到阈值
	if _, err := b.Pick(); err != ErrNoHealthyTarget {
		t.Errorf("target should be unhealthy after failed request creation, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RoundTrip — body read 覆盖（body 存在时缓冲）
// ---------------------------------------------------------------------------

func TestCoverage_RoundTrip_BodyBuffered(t *testing.T) {
	r1 := makeResp(200, `{"ok":true}`)
	defer r1.Body.Close()
	inner := &mockRoundTripper{
		responses: []*http.Response{r1},
	}

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 0,
		Logger:     zaptest.NewLogger(t),
	}

	body := bytes.NewBufferString(`{"model":"claude","messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages",
		io.NopCloser(body))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	resp.Body.Close()
	if len(inner.bodies) != 1 {
		t.Errorf("expected 1 body read, got %d", len(inner.bodies))
	}
}
