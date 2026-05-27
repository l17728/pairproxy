package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

// ---------------------------------------------------------------------------
// 测试辅助：mockTransport 可控的 http.RoundTripper
// ---------------------------------------------------------------------------

type mockTransport struct {
	calls     atomic.Int64
	responses []*http.Response // 按调用顺序返回
	errors    []error
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	idx := int(m.calls.Add(1)) - 1
	if req.Body != nil {
		io.Copy(io.Discard, req.Body) //nolint:errcheck
		req.Body.Close()
	}
	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
	}, nil
}

func makeTestResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

// newReliabilityTestSProxy 创建用于可靠性测试的 SProxy。
func newReliabilityTestSProxy(t *testing.T, targets []LLMTarget, transport http.RoundTripper) (*SProxy, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	sp.transport = transport

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return sp, token
}

// ---------------------------------------------------------------------------
// TestSProxy_RetryOnUpstreamFailure
// 第一个 target 返回 500，第二个 target 返回 200 → 请求应成功。
// ---------------------------------------------------------------------------

func TestSProxy_RetryOnUpstreamFailure(t *testing.T) {
	r1 := makeTestResp(500, `{"error":"internal"}`)
	r2 := makeTestResp(200, `{"id":"msg_1","content":[],"usage":{"input_tokens":10,"output_tokens":5}}`)
	t.Cleanup(func() { r1.Body.Close(); r2.Body.Close() })
	mt := &mockTransport{
		responses: []*http.Response{r1, r2},
	}

	targets := []LLMTarget{
		{URL: "http://llm1.local", APIKey: "key1"},
		{URL: "http://llm2.local", APIKey: "key2"},
	}
	sp, token := newReliabilityTestSProxy(t, targets, mt)

	// 配置均衡器（使两个 target 均可用）
	lbTargets := []lb.Target{
		{ID: "http://llm1.local", Addr: "http://llm1.local", Weight: 1, Healthy: true},
		{ID: "http://llm2.local", Addr: "http://llm2.local", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	sp.SetLLMHealthChecker(bal, nil)
	sp.SetMaxRetries(1)

	req := httptest.NewRequest(http.MethodPost, "http://llm1.local/v1/messages",
		bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()

	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if mt.calls.Load() != 2 {
		t.Errorf("expected 2 transport calls (1 fail + 1 retry), got %d", mt.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// TestSProxy_BindingRoutesCorrectly
// 用户绑定 target B；即使默认 LB 会选 target A，请求应路由到 B。
// ---------------------------------------------------------------------------

func TestSProxy_BindingRoutesCorrectly(t *testing.T) {
	var gotHost string
	mt := &mockTransport{}
	// 覆盖 RoundTrip 以记录请求目标
	origRT := mt
	_ = origRT

	recordingTransport := &recordingRoundTripper{
		inner: mt,
		onRequest: func(req *http.Request) {
			gotHost = req.URL.Host
		},
	}

	targets := []LLMTarget{
		{URL: "http://target-a.local", APIKey: "keyA"},
		{URL: "http://target-b.local", APIKey: "keyB"},
	}
	sp, token := newReliabilityTestSProxy(t, targets, recordingTransport)

	lbTargets := []lb.Target{
		{ID: "http://target-a.local", Addr: "http://target-a.local", Weight: 1, Healthy: true},
		{ID: "http://target-b.local", Addr: "http://target-b.local", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	sp.SetLLMHealthChecker(bal, nil)
	// 绑定 u1 → target-b
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		if userID == "u1" {
			return "http://target-b.local", true
		}
		return "", false
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()

	sp.Handler().ServeHTTP(rr, req)

	if gotHost != "target-b.local" {
		t.Errorf("expected request to target-b.local, got %q", gotHost)
	}
}

// ---------------------------------------------------------------------------
// TestSProxy_BindingFallbackOnUnhealthy
// 绑定 target 不健康 → 拒绝请求（503），不 fall through 到其他 target。
// 设计说明：LLM 分配必须由管理员明确配置；当绑定的 target 不可用时，应通知管理员
// 而非静默切换到其他 LLM（那会改变用户实际使用的模型）。
// ---------------------------------------------------------------------------

func TestSProxy_BindingFallbackOnUnhealthy(t *testing.T) {
	mt := &mockTransport{}

	targets := []LLMTarget{
		{URL: "http://target-a.local", APIKey: "keyA"},
		{URL: "http://target-b.local", APIKey: "keyB"},
	}
	sp, token := newReliabilityTestSProxy(t, targets, mt)

	lbTargets := []lb.Target{
		{ID: "http://target-a.local", Addr: "http://target-a.local", Weight: 1, Healthy: false}, // 不健康
		{ID: "http://target-b.local", Addr: "http://target-b.local", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	sp.SetLLMHealthChecker(bal, nil)
	// 绑定 u1 → target-a（但 target-a 不健康）
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		if userID == "u1" {
			return "http://target-a.local", true
		}
		return "", false
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()

	sp.Handler().ServeHTTP(rr, req)

	// 应拒绝请求（503），不 fall through 到 target-b
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (bound target unavailable should reject, not fallback)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestSProxy_PassiveCircuitBreaker
// 被动熔断：连续失败 → 健康检查器 MarkUnhealthy → 后续请求不再路由到该 target。
// ---------------------------------------------------------------------------

func TestSProxy_PassiveCircuitBreaker(t *testing.T) {
	logger := zaptest.NewLogger(t)

	targets := []LLMTarget{
		{URL: "http://t1.local", APIKey: "k1"},
		{URL: "http://t2.local", APIKey: "k2"},
	}
	jwtMgr, _ := auth.NewManager(logger, "secret")

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	db.Migrate(logger, gormDB) //nolint:errcheck
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	lbTargets := []lb.Target{
		{ID: "http://t1.local", Addr: "http://t1.local", Weight: 1, Healthy: true},
		{ID: "http://t2.local", Addr: "http://t2.local", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	hc := lb.NewHealthChecker(bal, logger, lb.WithFailThreshold(3))
	sp.SetLLMHealthChecker(bal, hc)

	// 模拟 t1 连续失败 3 次 → 触发熔断
	hc.RecordFailure("http://t1.local")
	hc.RecordFailure("http://t1.local")
	hc.RecordFailure("http://t1.local")

	// 验证 t1 已被标记为不健康
	statuses := sp.LLMTargetStatuses()
	for _, s := range statuses {
		if s.URL == "http://t1.local" && s.Healthy {
			t.Error("expected t1.local to be unhealthy after 3 failures")
		}
		if s.URL == "http://t2.local" && !s.Healthy {
			t.Error("expected t2.local to remain healthy")
		}
	}

	// pickLLMTarget 应只返回 t2
	info, err := sp.pickLLMTarget("/v1/messages", "u1", "", "", nil, "")
	if err != nil {
		t.Fatalf("pickLLMTarget: %v", err)
	}
	if info.URL != "http://t2.local" {
		t.Errorf("expected t2.local after circuit breaker, got %q", info.URL)
	}
}

// ---------------------------------------------------------------------------
// recordingRoundTripper — 记录请求目标的 transport 包装器
// ---------------------------------------------------------------------------

type recordingRoundTripper struct {
	inner     http.RoundTripper
	onRequest func(req *http.Request)
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.onRequest != nil {
		r.onRequest(req)
	}
	return r.inner.RoundTrip(req)
}
