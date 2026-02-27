package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/cluster"
	"github.com/pairproxy/pairproxy/internal/db"
)

// ClusterHandler 处理 sp-2 → sp-1 的内部 API 请求。
type ClusterHandler struct {
	logger       *zap.Logger
	registry     *cluster.PeerRegistry
	usageWriter  *db.UsageWriter
	sharedSecret string // Bearer token，空串表示不鉴权（仅测试用）
}

// NewClusterHandler 创建 ClusterHandler。
func NewClusterHandler(
	logger *zap.Logger,
	registry *cluster.PeerRegistry,
	usageWriter *db.UsageWriter,
	sharedSecret string,
) *ClusterHandler {
	return &ClusterHandler{
		logger:       logger.Named("cluster_handler"),
		registry:     registry,
		usageWriter:  usageWriter,
		sharedSecret: sharedSecret,
	}
}

// RegisterRoutes 注册内部 API 路由。
// mux: 已有的 *http.ServeMux（或兼容接口）
func (h *ClusterHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/internal/register", h.handleRegister)
	mux.HandleFunc("/api/internal/usage", h.handleUsageReport)
	mux.HandleFunc("/cluster/routing", h.handleGetRouting)
}

// ---------------------------------------------------------------------------
// POST /api/internal/register
// ---------------------------------------------------------------------------

// handleRegister 处理 sp-2 的心跳注册请求。
func (h *ClusterHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload cluster.RegisterPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if payload.ID == "" || payload.Addr == "" {
		http.Error(w, "id and addr are required", http.StatusBadRequest)
		return
	}

	h.registry.Register(payload.ID, payload.Addr, payload.SourceNode, payload.Weight)

	h.logger.Debug("peer heartbeat",
		zap.String("id", payload.ID),
		zap.String("addr", payload.Addr),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ---------------------------------------------------------------------------
// POST /api/internal/usage
// ---------------------------------------------------------------------------

// handleUsageReport 处理 sp-2 上报的 usage 批量记录。
func (h *ClusterHandler) handleUsageReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload cluster.UsageReportPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	for _, rec := range payload.Records {
		h.usageWriter.Record(rec)
	}

	h.logger.Debug("usage records received from peer",
		zap.String("source_node", payload.SourceNode),
		zap.Int("count", len(payload.Records)),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ---------------------------------------------------------------------------
// GET /cluster/routing
// ---------------------------------------------------------------------------

// handleGetRouting 返回当前路由表（供调试或监控使用）。
func (h *ClusterHandler) handleGetRouting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.registry == nil {
		http.Error(w, "not in cluster mode", http.StatusNotFound)
		return
	}

	peers := h.registry.Peers()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"peers": peers,
	})
}

// checkAuth 验证请求中的 Bearer token（shared secret）。
// sharedSecret 为空时跳过鉴权（仅测试环境）。
func (h *ClusterHandler) checkAuth(r *http.Request) bool {
	if h.sharedSecret == "" {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	expected := "Bearer " + h.sharedSecret
	return strings.TrimSpace(authHeader) == expected
}
