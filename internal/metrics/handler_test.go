package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/pairproxy/pairproxy/internal/db"
)

// setupMetricsTest 创建内存数据库和 metrics Handler
func setupMetricsTest(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	logger := zap.NewNop()
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
	return h, database
}

func TestMetricsHandlerBasic(t *testing.T) {
	h, _ := setupMetricsTest(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}

	body := w.Body.String()
	expectedMetrics := []string{
		"pairproxy_tokens_today",
		"pairproxy_requests_today",
		"pairproxy_active_users_today",
		"pairproxy_cost_usd_today",
		"pairproxy_tokens_month",
		"pairproxy_requests_month",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(body, m) {
			t.Errorf("expected metric %q in output, body:\n%s", m, body)
		}
	}
}

func TestMetricsHandlerPrometheusFormat(t *testing.T) {
	h, _ := setupMetricsTest(t)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	lines := strings.Split(body, "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Each non-comment line should be metric name{labels} value
		// Comment lines start with #
		if strings.HasPrefix(line, "#") {
			if !strings.HasPrefix(line, "# HELP ") && !strings.HasPrefix(line, "# TYPE ") {
				t.Errorf("unexpected comment line: %q", line)
			}
		}
	}
}

func TestMetricsHandlerCaching(t *testing.T) {
	h, _ := setupMetricsTest(t)

	// First call
	req1 := httptest.NewRequest("GET", "/metrics", nil)
	w1 := httptest.NewRecorder()
	h.handleMetrics(w1, req1)
	body1 := w1.Body.String()

	// Second call immediately — should use cache
	req2 := httptest.NewRequest("GET", "/metrics", nil)
	w2 := httptest.NewRecorder()
	h.handleMetrics(w2, req2)
	body2 := w2.Body.String()

	if body1 != body2 {
		t.Errorf("cached response should be identical: got different bodies")
	}

	// Cache should be populated
	h.cache.mu.Lock()
	hasCache := len(h.cache.body) > 0
	h.cache.mu.Unlock()
	if !hasCache {
		t.Error("expected cache to be populated after first call")
	}
}

func TestMetricsHandlerWithData(t *testing.T) {
	h, database := setupMetricsTest(t)
	_ = database

	// Insert test usage records via writer
	logger := zap.NewNop()
	writer := db.NewUsageWriter(database, logger, 10, 0)
	writer.Record(db.UsageRecord{
		RequestID:    "req-1",
		UserID:       "user-1",
		Model:        "claude-3-5-sonnet",
		InputTokens:  100,
		OutputTokens: 50,
		StatusCode:   200,
	})
	writer.Flush()

	// Clear cache to force re-collect
	h.cache.mu.Lock()
	h.cache.body = nil
	h.cache.mu.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.handleMetrics(w, req)

	body := w.Body.String()
	// Should contain non-zero values now
	if !strings.Contains(body, "pairproxy_tokens_today") {
		t.Errorf("expected token metrics in output")
	}
	if !strings.Contains(body, "pairproxy_requests_today") {
		t.Errorf("expected request metrics in output")
	}
}

func TestMetricsHandlerOnlyGET(t *testing.T) {
	h, _ := setupMetricsTest(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// POST should not match the GET /metrics route (returns 405)
	req := httptest.NewRequest("POST", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Go 1.22 pattern matching: "GET /metrics" only matches GET
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /metrics, got %d", w.Code)
	}
}
