package metrics

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/db"
)

// testUsageRepoWrapper 包装 UsageRepo 以暴露底层数据库
type testUsageRepoWrapper struct {
	*db.UsageRepo
	db *gorm.DB
}

// setupMetricsTestWithLoggerExt 创建带 logger 的测试环境
func setupMetricsTestWithLoggerExt(t *testing.T) (*Handler, *testUsageRepoWrapper, *zap.Logger) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	usageRepo := db.NewUsageRepo(database, logger)
	userRepo := db.NewUserRepo(database, logger)
	h := NewHandler(logger, usageRepo, userRepo)
	return h, &testUsageRepoWrapper{UsageRepo: usageRepo, db: database}, logger
}

// TestHandlerExt_SetDBPath 测试设置数据库路径
func TestHandlerExt_SetDBPath(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 创建临时数据库文件
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create test db file: %v", err)
	}

	h.SetDBPath(dbPath)

	// 清除缓存以强制重新收集
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_database_size_bytes") {
		t.Error("expected database_size_bytes metric when DBPath is set")
	}
}

// TestHandlerExt_SetDBPath_NonExistent 测试设置不存在的数据库路径
func TestHandlerExt_SetDBPath_NonExistent(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	h.SetDBPath("/non/existent/path/test.db")

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	// 应该正常响应，但不包含数据库大小指标
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestHandlerExt_SetQuotaCacheStats 测试设置配额缓存统计
func TestHandlerExt_SetQuotaCacheStats(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 使用 mock 实现
	mockQC := &mockQuotaCacheStatsForTest{hits: 100, misses: 20}
	h.SetQuotaCacheStats(mockQC)

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_quota_cache_hits_total") {
		t.Error("expected quota_cache_hits_total metric")
	}
	if !strings.Contains(body, "pairproxy_quota_cache_misses_total") {
		t.Error("expected quota_cache_misses_total metric")
	}
}

// TestHandlerExt_SetReporterStats 测试设置心跳统计
func TestHandlerExt_SetReporterStats(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	mockRP := &mockReporterStatsForTest{failures: 5, latency: 150}
	h.SetReporterStats(mockRP)

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_usage_report_failures_total") {
		t.Error("expected usage_report_failures_total metric")
	}
	if !strings.Contains(body, "pairproxy_usage_report_latency_ms") {
		t.Error("expected usage_report_latency_ms metric")
	}
}

// TestHandlerExt_SetReporterStats_NegativeLatency 测试心跳延迟为负值
func TestHandlerExt_SetReporterStats_NegativeLatency(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	mockRP := &mockReporterStatsForTest{failures: 0, latency: -1}
	h.SetReporterStats(mockRP)

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	// 负延迟应该不输出 latency 指标
	if strings.Contains(body, "pairproxy_usage_report_latency_ms") {
		t.Error("should not output latency metric when latency is negative")
	}
}

// TestHandlerExt_CacheExpiry 测试缓存过期
func TestHandlerExt_CacheExpiry(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 第一次调用
	req1 := httptest.NewRequest("GET", "/metrics", nil)
	w1 := httptest.NewRecorder()
	h.handleMetrics(w1, req1)

	// 手动使缓存过期
	h.cache.mu.Lock()
	h.cache.expiresAt = h.cache.expiresAt.Add(-60 * time.Second)
	h.cache.mu.Unlock()

	// 再次调用应该重新收集
	req2 := httptest.NewRequest("GET", "/metrics", nil)
	w2 := httptest.NewRecorder()
	h.handleMetrics(w2, req2)

	// 两次响应应该仍然成功
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Errorf("both requests should succeed, got %d and %d", w1.Code, w2.Code)
	}
}

// TestHandlerExt_ConcurrentRequests 测试并发请求
func TestHandlerExt_ConcurrentRequests(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	const numRequests = 10
	done := make(chan bool, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/metrics", nil)
			w := httptest.NewRecorder()
			h.handleMetrics(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			done <- true
		}()
	}

	for i := 0; i < numRequests; i++ {
		<-done
	}
}

// TestHandlerExt_LatencyTrackerIntegration 测试延迟追踪器集成
func TestHandlerExt_LatencyTrackerIntegration(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 设置全局延迟追踪器
	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)
	defer SetGlobalLatencyTracker(nil)

	// 记录一些延迟
	tracker.ObserveProxyLatency(100)
	tracker.ObserveProxyLatency(200)
	tracker.ObserveLLMLatency(150)
	tracker.ObserveLLMLatency(250)

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_proxy_latency_ms") {
		t.Error("expected proxy_latency_ms metric")
	}
	if !strings.Contains(body, "pairproxy_llm_latency_ms") {
		t.Error("expected llm_latency_ms metric")
	}
}

// TestHandlerExt_LatencyTrackerEmpty 测试空延迟追踪器
func TestHandlerExt_LatencyTrackerEmpty(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 设置空的延迟追踪器
	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)
	defer SetGlobalLatencyTracker(nil)

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	// 空追踪器不应该输出延迟指标
	if strings.Contains(body, "pairproxy_proxy_latency_ms{quantile=\"avg\"}") {
		t.Error("should not output proxy latency when no observations")
	}
}

// TestHandlerExt_WithMultipleUsers 测试多用户场景
func TestHandlerExt_WithMultipleUsers(t *testing.T) {
	h, testRepo, logger := setupMetricsTestWithLoggerExt(t)

	// 插入多个用户的用量记录
	writer := db.NewUsageWriter(testRepo.db, logger, 10, time.Minute)

	for i := 0; i < 5; i++ {
		writer.Record(db.UsageRecord{
			RequestID:    "req-multi-" + string(rune('0'+i)),
			UserID:       "user-multi-" + string(rune('0'+i)),
			Model:        "claude-3",
			InputTokens:  100 * (i + 1),
			OutputTokens: 50 * (i + 1),
			StatusCode:   200,
		})
	}
	writer.Flush()

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	// 应该有活跃用户指标
	if !strings.Contains(body, "pairproxy_active_users_today") {
		t.Error("expected active_users metric")
	}
}

// TestHandlerExt_WithErrors 测试包含错误的请求
func TestHandlerExt_WithErrors(t *testing.T) {
	h, testRepo, logger := setupMetricsTestWithLoggerExt(t)

	writer := db.NewUsageWriter(testRepo.db, logger, 10, time.Minute)

	// 成功请求
	writer.Record(db.UsageRecord{
		RequestID:  "req-err-1",
		UserID:     "user-err-1",
		StatusCode: 200,
	})
	// 失败请求
	writer.Record(db.UsageRecord{
		RequestID:  "req-err-2",
		UserID:     "user-err-2",
		StatusCode: 500,
	})
	writer.Flush()

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `status="success"`) {
		t.Error("expected success status metric")
	}
	if !strings.Contains(body, `status="error"`) {
		t.Error("expected error status metric")
	}
}

// TestHandlerExt_CostUSD 测试费用计算
func TestHandlerExt_CostUSD(t *testing.T) {
	h, testRepo, logger := setupMetricsTestWithLoggerExt(t)

	writer := db.NewUsageWriter(testRepo.db, logger, 10, time.Minute)

	// 设置费用计算函数
	writer.SetCostFunc(func(model string, input, output int) float64 {
		return float64(input+output) * 0.001 // 简单的费用计算
	})

	writer.Record(db.UsageRecord{
		RequestID:    "req-cost-1",
		UserID:       "user-cost-1",
		Model:        "claude-3",
		InputTokens:  1000,
		OutputTokens: 500,
		StatusCode:   200,
	})
	writer.Flush()

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_cost_usd_today") {
		t.Error("expected cost_usd_today metric")
	}
}

// TestHandlerExt_MonthlyMetrics 测试月度指标
func TestHandlerExt_MonthlyMetrics(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "pairproxy_tokens_month") {
		t.Error("expected tokens_month metric")
	}
	if !strings.Contains(body, "pairproxy_requests_month") {
		t.Error("expected requests_month metric")
	}
}

// TestHandlerExt_HistogramBuckets 测试直方图桶输出
func TestHandlerExt_HistogramBuckets(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)
	defer SetGlobalLatencyTracker(nil)

	// 添加多个观察值以填充不同桶
	tracker.ObserveProxyLatency(50)   // 0-100ms
	tracker.ObserveProxyLatency(200)  // 100-500ms
	tracker.ObserveProxyLatency(800)  // 500-1000ms
	tracker.ObserveProxyLatency(2000) // 1000-5000ms

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	// 检查 histogram bucket 输出格式
	if !strings.Contains(body, "pairproxy_proxy_latency_ms_bucket{le=") {
		t.Error("expected histogram bucket format")
	}
	if !strings.Contains(body, `le="+Inf"`) {
		t.Error("expected +Inf bucket")
	}
}

// TestHandlerExt_GetMetrics_CacheHit 测试缓存命中
func TestHandlerExt_GetMetrics_CacheHit(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 第一次调用填充缓存
	body1, err := h.getMetrics()
	if err != nil {
		t.Fatalf("getMetrics: %v", err)
	}

	// 第二次调用应该使用缓存
	body2, err := h.getMetrics()
	if err != nil {
		t.Fatalf("getMetrics: %v", err)
	}

	// 两次结果应该相同
	if string(body1) != string(body2) {
		t.Error("cached response should be identical")
	}
}

// TestHandlerExt_GetMetrics_CacheMiss 测试缓存未命中
func TestHandlerExt_GetMetrics_CacheMiss(t *testing.T) {
	h, _, _ := setupMetricsTestWithLoggerExt(t)

	// 第一次调用
	body1, err := h.getMetrics()
	if err != nil {
		t.Fatalf("getMetrics: %v", err)
	}

	// 使缓存过期
	h.cache.mu.Lock()
	h.cache.expiresAt = time.Now().Add(-time.Second)
	h.cache.mu.Unlock()

	// 第二次调用应该重新收集
	body2, err := h.getMetrics()
	if err != nil {
		t.Fatalf("getMetrics: %v", err)
	}

	// 结果可能不同（时间变化），但都应该成功
	if len(body1) == 0 || len(body2) == 0 {
		t.Error("metrics body should not be empty")
	}
}

// TestHandlerExt_AllCombinations 测试所有可选统计的组合
func TestHandlerExt_AllCombinations(t *testing.T) {
	h, testRepo, logger := setupMetricsTestWithLoggerExt(t)

	// 设置所有可选统计
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	os.WriteFile(dbPath, []byte("test"), 0644)
	h.SetDBPath(dbPath)
	h.SetQuotaCacheStats(&mockQuotaCacheStatsForTest{hits: 50, misses: 10})
	h.SetReporterStats(&mockReporterStatsForTest{failures: 3, latency: 200})

	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)
	defer SetGlobalLatencyTracker(nil)
	tracker.ObserveProxyLatency(100)
	tracker.ObserveLLMLatency(200)

	// 添加一些数据
	writer := db.NewUsageWriter(testRepo.db, logger, 10, time.Minute)
	writer.Record(db.UsageRecord{
		RequestID:    "req-comb-1",
		UserID:       "user-comb-1",
		Model:        "claude-3",
		InputTokens:  100,
		OutputTokens: 50,
		StatusCode:   200,
	})
	writer.Flush()

	// 清除缓存
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	expectedMetrics := []string{
		"pairproxy_tokens_today",
		"pairproxy_database_size_bytes",
		"pairproxy_quota_cache_hits_total",
		"pairproxy_usage_report_failures_total",
		"pairproxy_proxy_latency_ms",
		"pairproxy_llm_latency_ms",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(body, m) {
			t.Errorf("expected metric %q in output", m)
		}
	}
}

// mockQuotaCacheStatsForTest 实现 QuotaCacheStats 接口用于测试
type mockQuotaCacheStatsForTest struct {
	hits   int64
	misses int64
}

func (m *mockQuotaCacheStatsForTest) Hits() int64   { return m.hits }
func (m *mockQuotaCacheStatsForTest) Misses() int64 { return m.misses }

// mockReporterStatsForTest 实现 ReporterStats 接口用于测试
type mockReporterStatsForTest struct {
	failures int64
	latency  int64
}

func (m *mockReporterStatsForTest) HeartbeatFailures() int64 { return m.failures }
func (m *mockReporterStatsForTest) LastLatencyMs() int64     { return m.latency }
