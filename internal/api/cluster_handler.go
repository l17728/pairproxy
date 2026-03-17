package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
)

// ClusterHandler 处理 sp-2 → sp-1 的内部 API 请求。
type ClusterHandler struct {
	logger         *zap.Logger
	registry       *cluster.PeerRegistry
	manager        *cluster.Manager // 用于路由表轮询端点（改进项4）
	usageWriter    *db.UsageWriter
	sharedSecret   string // Bearer token，空串表示不鉴权（仅测试用）
	userRepo       *db.UserRepo
	groupRepo      *db.GroupRepo
	llmTargetRepo  *db.LLMTargetRepo
	llmBindingRepo *db.LLMBindingRepo
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

// SetConfigRepos 注入用于生成配置快照的仓库。
// Primary 节点必须调用此方法以启用 GET /api/internal/config-snapshot 端点。
func (h *ClusterHandler) SetConfigRepos(
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	llmTargetRepo *db.LLMTargetRepo,
	llmBindingRepo *db.LLMBindingRepo,
) {
	h.userRepo = userRepo
	h.groupRepo = groupRepo
	h.llmTargetRepo = llmTargetRepo
	h.llmBindingRepo = llmBindingRepo
}

// SetManager 设置集群管理器（用于路由表轮询端点）。
func (h *ClusterHandler) SetManager(m *cluster.Manager) {
	h.manager = m
}

// RegisterRoutes 注册内部 API 路由。
// mux: 已有的 *http.ServeMux（或兼容接口）
func (h *ClusterHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/internal/register", h.handleRegister)
	mux.HandleFunc("/api/internal/usage", h.handleUsageReport)
	mux.HandleFunc("/api/internal/config-snapshot", h.handleConfigSnapshot)
	mux.HandleFunc("/cluster/routing", h.handleGetRouting)
	mux.HandleFunc("/cluster/routing-poll", h.handleRoutingPoll) // 改进项4
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

// ---------------------------------------------------------------------------
// GET /api/internal/config-snapshot
// ---------------------------------------------------------------------------

// handleConfigSnapshot 返回 Primary 的配置快照（users/groups/llm_targets/llm_bindings）。
// Worker 节点通过此端点同步配置数据到本地 DB，实现多节点一致性。
func (h *ClusterHandler) handleConfigSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.userRepo == nil || h.groupRepo == nil || h.llmTargetRepo == nil || h.llmBindingRepo == nil {
		h.logger.Warn("config snapshot: repos not initialized (not in primary mode or SetConfigRepos not called)",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "config snapshot not available on this node", http.StatusServiceUnavailable)
		return
	}

	users, err := h.userRepo.ListAll()
	if err != nil {
		h.logger.Error("config snapshot: failed to list users", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	groups, err := h.groupRepo.List()
	if err != nil {
		h.logger.Error("config snapshot: failed to list groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	targets, err := h.llmTargetRepo.ListAll()
	if err != nil {
		h.logger.Error("config snapshot: failed to list llm targets", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	bindings, err := h.llmBindingRepo.List()
	if err != nil {
		h.logger.Error("config snapshot: failed to list llm bindings", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	snap := cluster.ConfigSnapshot{
		Version:     time.Now(),
		Users:       users,
		Groups:      groups,
		LLMTargets:  targets,
		LLMBindings: bindings,
	}

	h.logger.Debug("config snapshot served",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("users", len(users)),
		zap.Int("groups", len(groups)),
		zap.Int("targets", len(targets)),
		zap.Int("bindings", len(bindings)),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snap)
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

// ---------------------------------------------------------------------------
// GET /cluster/routing-poll（改进项4）
// ---------------------------------------------------------------------------

// handleRoutingPoll 供 c-proxy 主动轮询路由表更新。
// 若客户端版本已是最新，返回 304 Not Modified；否则返回编码后的路由表。
func (h *ClusterHandler) handleRoutingPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.manager == nil {
		h.logger.Warn("routing poll: manager not initialized (not in cluster mode)",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "not in cluster mode", http.StatusNotFound)
		return
	}

	// 读取客户端当前版本
	clientVersionStr := r.Header.Get("X-Routing-Version")
	var clientVersion int64
	if clientVersionStr != "" {
		if v, err := strconv.ParseInt(clientVersionStr, 10, 64); err == nil {
			clientVersion = v
		}
	}

	rt := h.manager.CurrentTable()

	if rt.Version <= clientVersion {
		// 客户端版本已是最新，无需更新
		h.logger.Debug("routing poll: client up to date",
			zap.String("remote_addr", r.RemoteAddr),
			zap.Int64("client_version", clientVersion),
			zap.Int64("server_version", rt.Version),
		)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	encoded, err := rt.Encode()
	if err != nil {
		h.logger.Error("routing poll: failed to encode routing table",
			zap.Error(err),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("routing poll: sending routing update",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int64("client_version", clientVersion),
		zap.Int64("server_version", rt.Version),
		zap.Int("entries", len(rt.Entries)),
	)

	// 使用与普通请求相同的响应头格式，c-proxy 可复用 processRoutingHeaders 逻辑
	w.Header().Set("X-Routing-Version", strconv.FormatInt(rt.Version, 10))
	w.Header().Set("X-Routing-Update", encoded)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
