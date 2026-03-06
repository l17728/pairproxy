package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
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
		h.logger.Warn("peer register: invalid request body",
			zap.String("remote_addr", r.RemoteAddr),
			zap.Error(err),
		)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if payload.ID == "" || payload.Addr == "" {
		h.logger.Warn("peer register: missing required fields id/addr",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("id", payload.ID),
			zap.String("addr", payload.Addr),
		)
		http.Error(w, "id and addr are required", http.StatusBadRequest)
		return
	}

	h.registry.Register(payload.ID, payload.Addr, payload.SourceNode, payload.Weight)

	h.logger.Debug("peer heartbeat received",
		zap.String("id", payload.ID),
		zap.String("addr", payload.Addr),
		zap.Int("weight", payload.Weight),
		zap.String("source_node", payload.SourceNode),
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
		h.logger.Warn("usage report: invalid request body",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("source_node", payload.SourceNode),
			zap.Error(err),
		)
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
		h.logger.Warn("cluster routing query: registry not initialized (not in cluster mode)",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "not in cluster mode", http.StatusNotFound)
		return
	}

	peers := h.registry.Peers()
	h.logger.Debug("cluster routing queried",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("peer_count", len(peers)),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"peers": peers,
	})
}

// checkAuth 验证请求中的 Bearer token（shared secret）。
//
// 安全策略（fail-closed）：
//   - sharedSecret 为空时拒绝所有请求并记录 WARN 日志，防止配置缺失时意外放行。
//   - 生产环境必须在 cluster.shared_secret 中配置非空密钥。
func (h *ClusterHandler) checkAuth(r *http.Request) bool {
	if h.sharedSecret == "" {
		// fail-closed：未配置共享密钥，拒绝所有内部 API 请求。
		// 若确实希望禁用认证（如单机测试），请在配置中显式设置 shared_secret: "test-only"。
		h.logger.Warn("cluster shared_secret not configured; rejecting internal API request (fail-closed)",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("path", r.URL.Path),
		)
		return false
	}
	authHeader := r.Header.Get("Authorization")
	expected := "Bearer " + h.sharedSecret
	ok := strings.TrimSpace(authHeader) == expected
	if !ok {
		h.logger.Warn("cluster API authentication failed: invalid or missing shared secret",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("path", r.URL.Path),
		)
	}
	return ok
}
