// Package metrics 提供 Prometheus 格式的监控指标端点。
// 实现不依赖 prometheus/client_golang，手动生成文本格式以避免额外依赖。
package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/db"
)

// Handler 提供 GET /metrics 端点（Prometheus text format）
type Handler struct {
	logger    *zap.Logger
	usageRepo *db.UsageRepo
	userRepo  *db.UserRepo

	// 30 秒缓存，避免频繁查 DB
	cache struct {
		mu        sync.Mutex
		body      []byte
		expiresAt time.Time
	}
}

// NewHandler 创建 metrics Handler
func NewHandler(logger *zap.Logger, usageRepo *db.UsageRepo, userRepo *db.UserRepo) *Handler {
	return &Handler{
		logger:    logger.Named("metrics"),
		usageRepo: usageRepo,
		userRepo:  userRepo,
	}
}

// RegisterRoutes 注册 /metrics 路由（无需认证，供 Prometheus 抓取）
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /metrics", h.handleMetrics)
}

func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	body, err := h.getMetrics()
	if err != nil {
		h.logger.Error("metrics: failed to collect", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// getMetrics 返回 Prometheus 格式的指标文本（30 秒缓存）
func (h *Handler) getMetrics() ([]byte, error) {
	h.cache.mu.Lock()
	defer h.cache.mu.Unlock()

	if time.Now().Before(h.cache.expiresAt) && len(h.cache.body) > 0 {
		return h.cache.body, nil
	}

	body, err := h.collect()
	if err != nil {
		return nil, err
	}
	h.cache.body = body
	h.cache.expiresAt = time.Now().Add(30 * time.Second)
	return body, nil
}

// collect 从 DB 收集当前指标
func (h *Handler) collect() ([]byte, error) {
	now := time.Now()
	todayStart := now.Truncate(24 * time.Hour)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	// 今日全局统计
	today, err := h.usageRepo.GlobalSumTokens(todayStart, now)
	if err != nil {
		return nil, fmt.Errorf("today stats: %w", err)
	}

	// 本月全局统计
	month, err := h.usageRepo.GlobalSumTokens(monthStart, now)
	if err != nil {
		return nil, fmt.Errorf("month stats: %w", err)
	}

	// 今日估算费用
	todayCost, err := h.usageRepo.SumCostUSD(todayStart, now)
	if err != nil {
		return nil, fmt.Errorf("today cost: %w", err)
	}

	// 活跃用户数（今日有请求的不重复用户）
	activeUsers, err := h.usageRepo.UserStats(todayStart, now, 1000)
	if err != nil {
		return nil, fmt.Errorf("active users: %w", err)
	}

	var buf []byte
	w := func(format string, args ...interface{}) {
		buf = append(buf, fmt.Sprintf(format, args...)...)
	}

	// --- tokens_total ---
	w("# HELP pairproxy_tokens_total Total tokens processed\n")
	w("# TYPE pairproxy_tokens_total counter\n")
	w("pairproxy_tokens_today{type=\"input\"} %d\n", today.TotalInput)
	w("pairproxy_tokens_today{type=\"output\"} %d\n", today.TotalOutput)

	// --- requests_total ---
	w("# HELP pairproxy_requests_today Total requests today\n")
	w("# TYPE pairproxy_requests_today gauge\n")
	w("pairproxy_requests_today{status=\"success\"} %d\n", today.RequestCount-today.ErrorCount)
	w("pairproxy_requests_today{status=\"error\"} %d\n", today.ErrorCount)

	// --- active users ---
	w("# HELP pairproxy_active_users_today Unique users with at least one request today\n")
	w("# TYPE pairproxy_active_users_today gauge\n")
	w("pairproxy_active_users_today %d\n", len(activeUsers))

	// --- estimated cost ---
	w("# HELP pairproxy_cost_usd_today Estimated LLM cost today in USD\n")
	w("# TYPE pairproxy_cost_usd_today gauge\n")
	w("pairproxy_cost_usd_today %.6f\n", todayCost)

	// --- monthly ---
	w("# HELP pairproxy_tokens_month Total tokens this calendar month\n")
	w("# TYPE pairproxy_tokens_month gauge\n")
	w("pairproxy_tokens_month{type=\"input\"} %d\n", month.TotalInput)
	w("pairproxy_tokens_month{type=\"output\"} %d\n", month.TotalOutput)

	w("# HELP pairproxy_requests_month Total requests this calendar month\n")
	w("# TYPE pairproxy_requests_month gauge\n")
	w("pairproxy_requests_month{status=\"success\"} %d\n", month.RequestCount-month.ErrorCount)
	w("pairproxy_requests_month{status=\"error\"} %d\n", month.ErrorCount)

	h.logger.Debug("metrics collected",
		zap.Int64("today_requests", today.RequestCount),
		zap.Int64("today_tokens", today.TotalTokens),
		zap.Float64("today_cost_usd", todayCost),
	)

	return buf, nil
}
