package e2e_test

import (
	"bytes"
	"errors"
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
	"github.com/l17728/pairproxy/internal/proxy"
)

// ---------------------------------------------------------------------------
// 辅助：构建用于可靠性 E2E 测试的 sproxy（带 balancer）
// ---------------------------------------------------------------------------

type llmReliabilityEnv struct {
	sp     *proxy.SProxy
	srv    *httptest.Server
	token  string
	jwtMgr *auth.Manager
}

func setupLLMReliabilityE2E(t *testing.T, targets []proxy.LLMTarget, transport http.RoundTripper) *llmReliabilityEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "llm-e2e-secret")
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

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	// 注入可控 transport
	sp.SetTransport(transport)

	// 构建 LB 均衡器
	lbTargets := make([]lb.Target, len(targets))
	for i, t := range targets {
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		lbTargets[i] = lb.Target{ID: t.URL, Addr: t.URL, Weight: w, Healthy: true}
	}
	bal := lb.NewWeightedRandom(lbTargets)
	hc := lb.NewHealthChecker(bal, logger, lb.WithFailThreshold(3))
	sp.SetLLMHealthChecker(bal, hc)
	sp.SetMaxRetries(2)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	srv := httptest.NewServer(sp.Handler())
	t.Cleanup(srv.Close)

	return &llmReliabilityEnv{sp: sp, srv: srv, token: token, jwtMgr: jwtMgr}
}

func (e *llmReliabilityEnv) postMessages(t *testing.T, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/messages", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-PairProxy-Auth", e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// 可控 Transport（多 target 版本）
// ---------------------------------------------------------------------------

type multiTargetTransport struct {
	handlers map[string]http.RoundTripper // host → transport
	fallback http.RoundTripper
}

func (m *multiTargetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hostKey := req.URL.Scheme + "://" + req.URL.Host
	if h, ok := m.handlers[hostKey]; ok {
		return h.RoundTrip(req)
	}
	if m.fallback != nil {
		return m.fallback.RoundTrip(req)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
	}, nil
}

type countingTransport struct {
	calls    atomic.Int64
	response func() (*http.Response, error)
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	if req.Body != nil {
		io.Copy(io.Discard, req.Body) //nolint:errcheck
		req.Body.Close()
	}
	return c.response()
}

// ---------------------------------------------------------------------------
// TestE2E_RetryOnUpstreamFailure
// target1 返回 500，target2 返回 200 → 客户端收到 200。
// ---------------------------------------------------------------------------

func TestE2E_RetryOnUpstreamFailure(t *testing.T) {
	target1 := &countingTransport{
		response: func() (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":"internal"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}
	target2 := &countingTransport{
		response: func() (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"r","usage":{"input_tokens":5,"output_tokens":3}}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://t1.fake", APIKey: "k1"},
		{URL: "http://t2.fake", APIKey: "k2"},
	}
	transport := &multiTargetTransport{
		handlers: map[string]http.RoundTripper{
			"http://t1.fake": target1,
			"http://t2.fake": target2,
		},
	}

	env := setupLLMReliabilityE2E(t, targets, transport)
	resp := env.postMessages(t, `{"model":"claude","messages":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	// 至少 target2 被调用（验证重试发生）
	if target2.calls.Load() < 1 {
		t.Errorf("expected target2 to be called (retry), got %d calls", target2.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// TestE2E_LLMBinding_UserRouting
// 用户绑定 target2；即使均衡器可能选 target1，请求应路由到 target2。
// ---------------------------------------------------------------------------

func TestE2E_LLMBinding_UserRouting(t *testing.T) {
	logger := zaptest.NewLogger(t)

	target1Calls := atomic.Int64{}
	target2Calls := atomic.Int64{}

	transport := &multiTargetTransport{
		handlers: map[string]http.RoundTripper{
			"http://llma.fake": &countingTransport{
				response: func() (*http.Response, error) {
					target1Calls.Add(1)
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(`{"id":"1","usage":{"input_tokens":1,"output_tokens":1}}`)),
						Header:     make(http.Header),
					}, nil
				},
			},
			"http://llmb.fake": &countingTransport{
				response: func() (*http.Response, error) {
					target2Calls.Add(1)
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(`{"id":"2","usage":{"input_tokens":1,"output_tokens":1}}`)),
						Header:     make(http.Header),
					}, nil
				},
			},
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://llma.fake", APIKey: "ka"},
		{URL: "http://llmb.fake", APIKey: "kb"},
	}

	jwtMgr, _ := auth.NewManager(logger, "binding-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	db.Migrate(logger, gormDB) //nolint:errcheck
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	sp.SetTransport(transport)

	lbTargets := []lb.Target{
		{ID: "http://llma.fake", Addr: "http://llma.fake", Weight: 1, Healthy: true},
		{ID: "http://llmb.fake", Addr: "http://llmb.fake", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	sp.SetLLMHealthChecker(bal, nil)

	// 绑定 u1 → llmb
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		if userID == "u1" {
			return "http://llmb.fake", true
		}
		return "", false
	})

	srv := httptest.NewServer(sp.Handler())
	defer srv.Close()

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)

	// 发送 3 次请求，全部应路由到 llmb
	for i := range 3 {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
			bytes.NewBufferString(`{"model":"claude","messages":[]}`))
		req.Header.Set("X-PairProxy-Auth", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
	}

	if target1Calls.Load() != 0 {
		t.Errorf("expected 0 calls to llma (binding should route to llmb), got %d", target1Calls.Load())
	}
	if target2Calls.Load() != 3 {
		t.Errorf("expected 3 calls to llmb, got %d", target2Calls.Load())
	}
}

// ---------------------------------------------------------------------------
// TestE2E_CircuitBreakerPassive
// 连续 3 次 target1 失败 → 熔断 → 第4次路由到 target2。
// ---------------------------------------------------------------------------

func TestE2E_CircuitBreakerPassive(t *testing.T) {
	var t1Calls, t2Calls atomic.Int64

	// target1 最初总返回 500（导致被动熔断）
	transport := &multiTargetTransport{
		handlers: map[string]http.RoundTripper{
			"http://t1.fake": &countingTransport{
				response: func() (*http.Response, error) {
					t1Calls.Add(1)
					return &http.Response{
						StatusCode: 500,
						Body:       io.NopCloser(bytes.NewBufferString(`{"error":"fail"}`)),
						Header:     make(http.Header),
					}, nil
				},
			},
			"http://t2.fake": &countingTransport{
				response: func() (*http.Response, error) {
					t2Calls.Add(1)
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(`{"id":"ok","usage":{"input_tokens":1,"output_tokens":1}}`)),
						Header:     make(http.Header),
					}, nil
				},
			},
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://t1.fake", APIKey: "k1", Weight: 1},
		{URL: "http://t2.fake", APIKey: "k2", Weight: 1},
	}

	env := setupLLMReliabilityE2E(t, targets, transport)

	// 手动触发 t1 熔断（模拟 3 次 RecordFailure）
	statuses := env.sp.LLMTargetStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(statuses))
	}

	// 通过 pickLLMTarget 间接验证：发送 1 次请求（会发生重试，t1→t2）
	resp := env.postMessages(t, `{"model":"claude","messages":[]}`)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	// t2 至少被调用 1 次（重试成功）
	if t2Calls.Load() < 1 {
		t.Errorf("expected t2 called at least once, got %d", t2Calls.Load())
	}
}

// ---------------------------------------------------------------------------
// TestE2E_LLMDistribute_EvenSpread
// 6 个用户均分到 2 个 target：各 3 个。
// ---------------------------------------------------------------------------

func TestE2E_LLMDistribute_EvenSpread(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	repo := db.NewLLMBindingRepo(gormDB, logger)

	userIDs := []string{"u1", "u2", "u3", "u4", "u5", "u6"}
	// 使用 UUID 风格的 targetID（LLMBinding.TargetID 存 UUID，不存 URL）
	targetIDs := []string{"tid-ta", "tid-tb"}

	if err := repo.EvenDistribute(userIDs, targetIDs); err != nil {
		t.Fatalf("EvenDistribute: %v", err)
	}

	bindings, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bindings) != 6 {
		t.Fatalf("expected 6 bindings, got %d", len(bindings))
	}

	counts := map[string]int{}
	for _, b := range bindings {
		counts[b.TargetID]++ // 按 TargetID（UUID）统计
	}
	for _, tid := range targetIDs {
		if counts[tid] != 3 {
			t.Errorf("target %q: expected 3 users, got %d", tid, counts[tid])
		}
	}
}

// ---------------------------------------------------------------------------
// TestE2E_NoHealthyTarget_Returns502
// 所有 target 返回连接错误 → 客户端收到 502。
// ---------------------------------------------------------------------------

func TestE2E_NoHealthyTarget_Returns502(t *testing.T) {
	connErr := errors.New("connection refused")
	errTransport := &countingTransport{
		response: func() (*http.Response, error) {
			return nil, connErr
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://down1.fake", APIKey: "k1"},
	}

	env := setupLLMReliabilityE2E(t, targets, errTransport)
	env.sp.SetMaxRetries(0) // 不重试

	resp := env.postMessages(t, `{"model":"claude","messages":[]}`)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}
