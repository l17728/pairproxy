package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// newTestSProxySimple 创建一个简单的 SProxy（不需要 jwtMgr 参数版本）
func newTestSProxySimple(t *testing.T) (*SProxy, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret-key-for-proxy")
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

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: "https://api.anthropic.com", APIKey: "test-key", Provider: "anthropic"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	return sp, cancel
}

// ---------------------------------------------------------------------------
// Drain / Undrain / IsDraining / GetDrainStatus / ActiveRequests
// ---------------------------------------------------------------------------

func TestSProxy_DrainUndrain_Roundtrip(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 初始状态：不在排水模式
	if sp.IsDraining() {
		t.Error("should not be draining initially")
	}

	// Drain
	if err := sp.Drain(); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !sp.IsDraining() {
		t.Error("should be draining after Drain()")
	}

	// 二次 Drain 是幂等的
	if err := sp.Drain(); err != nil {
		t.Fatalf("second Drain: %v", err)
	}

	// Undrain
	if err := sp.Undrain(); err != nil {
		t.Fatalf("Undrain: %v", err)
	}
	if sp.IsDraining() {
		t.Error("should not be draining after Undrain()")
	}

	// 二次 Undrain 是幂等的
	if err := sp.Undrain(); err != nil {
		t.Fatalf("second Undrain: %v", err)
	}
}

func TestSProxy_GetDrainStatus(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	statusBefore := sp.GetDrainStatus()
	if statusBefore.Draining {
		t.Error("initial drain status should be false")
	}
	if statusBefore.ActiveRequests != 0 {
		t.Errorf("initial active requests = %d, want 0", statusBefore.ActiveRequests)
	}

	sp.Drain()
	statusAfter := sp.GetDrainStatus()
	if !statusAfter.Draining {
		t.Error("drain status should be true after Drain()")
	}
}

func TestSProxy_ActiveRequests_InitiallyZero(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	if n := sp.ActiveRequests(); n != 0 {
		t.Errorf("ActiveRequests() = %d, want 0 initially", n)
	}
}

// ---------------------------------------------------------------------------
// SetNotifier
// ---------------------------------------------------------------------------

func TestSProxy_SetNotifier(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zaptest.NewLogger(t)
	notifier := alert.NewNotifier(logger, "")
	// 不应 panic
	sp.SetNotifier(notifier)
	sp.SetNotifier(nil)
}

// ---------------------------------------------------------------------------
// StartActiveRequestsMonitor — threshold=0 时为 no-op
// ---------------------------------------------------------------------------

func TestStartActiveRequestsMonitor_ZeroThreshold_NoOp(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zaptest.NewLogger(t)
	ctx, monCancel := context.WithCancel(context.Background())
	defer monCancel()

	// threshold=0 → no-op，不应 panic
	StartActiveRequestsMonitor(ctx, sp, 0, nil, "node1", logger)
}

func TestStartActiveRequestsMonitor_NilNotifier_NoOp(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zaptest.NewLogger(t)
	ctx, monCancel := context.WithCancel(context.Background())
	defer monCancel()

	// notifier=nil → no-op
	StartActiveRequestsMonitor(ctx, sp, 10, nil, "node1", logger)
}

func TestStartActiveRequestsMonitor_EdgeTrigger_HighLoad(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 搭建 webhook 服务器捕获告警
	received := make(chan string, 5)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "notified"
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	logger := zaptest.NewLogger(t)
	notifier := alert.NewNotifier(logger, webhookSrv.URL)

	ctx, monCancel := context.WithCancel(context.Background())
	defer monCancel()

	// 使用极短 interval（内部可测试函数）
	startActiveRequestsMonitor(ctx, sp, 5, notifier, "test-node", logger, 20*time.Millisecond)

	// 将活跃请求数提升到阈值以上
	sp.activeRequests.Store(10)

	// 等待告警
	select {
	case <-received:
		// 成功收到 HighLoad 告警
	case <-time.After(500 * time.Millisecond):
		t.Error("expected HighLoad notification within 500ms")
	}

	// 恢复到阈值以下
	sp.activeRequests.Store(1)

	// 等待恢复告警
	select {
	case <-received:
		// 成功收到 LoadRecovered 告警
	case <-time.After(500 * time.Millisecond):
		t.Error("expected LoadRecovered notification within 500ms")
	}
}

func TestStartActiveRequestsMonitor_ContextCancel_Stops(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zaptest.NewLogger(t)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	notifier := alert.NewNotifier(logger, webhookSrv.URL)

	ctx, monCancel := context.WithCancel(context.Background())
	startActiveRequestsMonitor(ctx, sp, 5, notifier, "test-node", logger, 10*time.Millisecond)

	// 立即取消
	monCancel()
	time.Sleep(30 * time.Millisecond) // 让 goroutine 退出
}

// ---------------------------------------------------------------------------
// LLMTargetStatuses — 无均衡器时全部 healthy
// ---------------------------------------------------------------------------

func TestSProxy_LLMTargetStatuses_NoBalancer(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	statuses := sp.LLMTargetStatuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if !statuses[0].Healthy {
		t.Error("target should be healthy when no balancer is configured")
	}
	if statuses[0].Draining {
		t.Error("target should not be draining initially")
	}
}

// ---------------------------------------------------------------------------
// parseRoutingVersion — 各种输入
// ---------------------------------------------------------------------------

func TestParseRoutingVersion_Valid(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"42", 42},
		{"0", 0},
		{"9223372036854775807", 9223372036854775807},
		{"", 0},
		{"not-a-number", 0},
		{"-1", -1},
	}
	for _, tc := range cases {
		got := parseRoutingVersion(tc.input)
		if got != tc.want {
			t.Errorf("parseRoutingVersion(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// extractModel / extractModelFromBody
// ---------------------------------------------------------------------------

func TestExtractModel_FromHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Model", "claude-3-opus")

	model := extractModel(req)
	if model != "claude-3-opus" {
		t.Errorf("extractModel = %q, want 'claude-3-opus'", model)
	}
}

func TestExtractModel_NoHeader_ReturnsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	model := extractModel(req)
	if model != "" {
		t.Errorf("extractModel without header = %q, want empty", model)
	}
}

func TestExtractModelFromBody_ValidJSON(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	model := extractModelFromBody(body)
	if model != "gpt-4" {
		t.Errorf("extractModelFromBody = %q, want 'gpt-4'", model)
	}
}

func TestExtractModelFromBody_NoModel_ReturnsEmpty(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	model := extractModelFromBody(body)
	if model != "" {
		t.Errorf("extractModelFromBody without model = %q, want empty", model)
	}
}

func TestExtractModelFromBody_InvalidJSON_ReturnsEmpty(t *testing.T) {
	model := extractModelFromBody([]byte("not-json"))
	if model != "" {
		t.Errorf("extractModelFromBody with invalid JSON = %q, want empty", model)
	}
}

// ---------------------------------------------------------------------------
// SetMaxRetries / SetTransport
// ---------------------------------------------------------------------------

func TestSProxy_SetMaxRetries(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	sp.SetMaxRetries(5)
	if sp.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", sp.maxRetries)
	}
}

func TestSProxy_SetTransport(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	custom := &http.Transport{}
	sp.SetTransport(custom)
	if sp.transport != custom {
		t.Error("transport should have been updated")
	}
}

// ---------------------------------------------------------------------------
// NewSProxy — 无 target 应报错
// ---------------------------------------------------------------------------

func TestNewSProxy_NoTargets_ReturnsError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 不启动 writer（避免 goroutine 泄漏），NewSProxy 应在创建阶段就报错
	writer := db.NewUsageWriter(gormDB, logger, 10, time.Second)
	// 不调用 Start，也不需要 Wait

	_, err := NewSProxy(logger, jwtMgr, writer, nil)
	if err == nil {
		t.Fatal("NewSProxy with nil targets should return error")
	}
}

// ---------------------------------------------------------------------------
// SetBindingResolver
// ---------------------------------------------------------------------------

func TestSProxy_SetBindingResolver(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		return "https://custom-target.example.com", true
	})
	if sp.bindingResolver == nil {
		t.Error("bindingResolver should be set")
	}
	url, found := sp.bindingResolver("u1", "g1")
	if !found || url != "https://custom-target.example.com" {
		t.Error("bindingResolver should return the configured url")
	}
}

// ---------------------------------------------------------------------------
// NewSProxyWithCluster — (0% coverage)
// ---------------------------------------------------------------------------

// TestNewSProxyWithCluster_ReturnsNonNil verifies NewSProxyWithCluster creates a valid SProxy.
func TestNewSProxyWithCluster_ReturnsNonNil(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-cluster-secret")
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

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	// 构造 cluster manager
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "llm-1", Addr: "http://llm:8080", Weight: 1, Healthy: true},
	})
	clusterMgr := cluster.NewManager(logger, balancer, nil, "")

	sp, err := NewSProxyWithCluster(logger, jwtMgr, writer,
		[]LLMTarget{
			{URL: "https://api.anthropic.com", APIKey: "test-key", Provider: "anthropic"},
		},
		clusterMgr, "sp-1",
	)
	if err != nil {
		t.Fatalf("NewSProxyWithCluster: %v", err)
	}
	if sp == nil {
		t.Fatal("NewSProxyWithCluster returned nil")
	}
}

// TestNewSProxyWithCluster_NilClusterManager verifies nil clusterMgr is acceptable.
func TestNewSProxyWithCluster_NilClusterManager(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-nil-cluster-secret")
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

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	sp, err := NewSProxyWithCluster(logger, jwtMgr, writer,
		[]LLMTarget{
			{URL: "https://api.anthropic.com", APIKey: "key", Provider: "anthropic"},
		},
		nil, "standalone",
	)
	if err != nil {
		t.Fatalf("NewSProxyWithCluster with nil clusterMgr: %v", err)
	}
	if sp == nil {
		t.Fatal("NewSProxyWithCluster returned nil")
	}
}

// ---------------------------------------------------------------------------
// swapFirstLast 混淆函数测试
// ---------------------------------------------------------------------------

func TestSwapFirstLast(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty string", input: "", expected: ""},
		{name: "single char", input: "a", expected: "a"},
		{name: "two chars", input: "ab", expected: "ba"},
		{name: "normal key body", input: "abcdefgh", expected: "hbcdefga"},
		{name: "sk-pp- style", input: "sk-pp-abcdefghijklmnopqrstuvwxyz012345678901234567", expected: "7k-pp-abcdefghijklmnopqrstuvwxyz01234567890123456s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := swapFirstLast(tc.input)
			if got != tc.expected {
				t.Errorf("swapFirstLast(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// TestSwapFirstLast_Symmetric 验证函数是自己的逆（加解密使用同一函数）
func TestSwapFirstLast_Symmetric(t *testing.T) {
	samples := []string{
		"",
		"x",
		"ab",
		"sk-pp-abcdefghijklmnopqrstuvwxyz012345678901234567",
		"sk-pp-ABCDEFGHIJKLMNOPQRSTUVWXYZ012345678901234567",
	}
	for _, s := range samples {
		if got := swapFirstLast(swapFirstLast(s)); got != s {
			t.Errorf("double swapFirstLast(%q) = %q, want original", s, got)
		}
	}
}

func TestObfuscateKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty", input: "", expected: ""},
		{name: "no dash", input: "ollama", expected: "allamo"},
		{name: "sk-pp- key", input: "sk-pp-abcdefghijklmnopqrstuvwxyz012345678901234567", expected: "sk-pp-7bcdefghijklmnopqrstuvwxyz01234567890123456a"},
		{name: "anthropic key", input: "sk-ant-api03-AAABBBCCC", expected: "sk-ant-api03-CAABBBCCA"},
		{name: "openai key", input: "sk-XYZABC", expected: "sk-CYZABX"},
		{name: "dash at end", input: "prefix-", expected: "-refixp"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := obfuscateKey(tc.input)
			if got != tc.expected {
				t.Errorf("obfuscateKey(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// TestObfuscateKey_Symmetric 验证 obfuscateKey 是自己的逆
func TestObfuscateKey_Symmetric(t *testing.T) {
	samples := []string{
		"",
		"ollama",
		"sk-pp-abcdefghijklmnopqrstuvwxyz012345678901234567",
		"sk-ant-api03-AAABBBCCCDDDEEE",
		"sk-XYZABC123",
	}
	for _, s := range samples {
		if got := obfuscateKey(obfuscateKey(s)); got != s {
			t.Errorf("double obfuscateKey(%q) = %q, want original", s, got)
		}
	}
}
