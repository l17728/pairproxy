package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// AdminCookieName 管理员 session cookie 名称（供 dashboard 包共享）
const AdminCookieName = "pairproxy_admin"

// adminUserID admin JWT 固定 UserID
const adminUserID = "__admin__"

// AdminHandler 处理管理员 REST API：用户/分组管理 + 统计查询
type AdminHandler struct {
	logger            *zap.Logger
	jwtMgr            *auth.Manager
	userRepo          *db.UserRepo
	groupRepo         *db.GroupRepo
	usageRepo         *db.UsageRepo
	auditRepo         *db.AuditRepo
	apiKeyRepo        *db.APIKeyRepo                // 可选，F-5 多 API Key 管理
	encryptFn         func(string) (string, error)  // 可选，加密 API Key 明文
	adminPasswordHash string
	tokenTTL          time.Duration
}

// NewAdminHandler 创建 AdminHandler
func NewAdminHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	usageRepo *db.UsageRepo,
	auditRepo *db.AuditRepo,
	adminPasswordHash string,
	tokenTTL time.Duration,
) *AdminHandler {
	return &AdminHandler{
		logger:            logger.Named("admin_handler"),
		jwtMgr:            jwtMgr,
		userRepo:          userRepo,
		groupRepo:         groupRepo,
		usageRepo:         usageRepo,
		auditRepo:         auditRepo,
		adminPasswordHash: adminPasswordHash,
		tokenTTL:          tokenTTL,
	}
}

// SetAPIKeyRepo 设置 API Key 仓库（可选；不设置则 api-keys 端点返回 501）。
func (h *AdminHandler) SetAPIKeyRepo(repo *db.APIKeyRepo, encryptFn func(string) (string, error)) {
	h.apiKeyRepo = repo
	h.encryptFn = encryptFn
}

// RegisterRoutes 注册管理员路由到 mux
func (h *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	// 登录（无需认证）
	mux.HandleFunc("POST /api/admin/login", h.handleLogin)

	// 用户管理
	mux.Handle("GET /api/admin/users", h.RequireAdmin(http.HandlerFunc(h.handleListUsers)))
	mux.Handle("POST /api/admin/users", h.RequireAdmin(http.HandlerFunc(h.handleCreateUser)))
	mux.Handle("PUT /api/admin/users/{id}/active", h.RequireAdmin(http.HandlerFunc(h.handleSetUserActive)))
	mux.Handle("PUT /api/admin/users/{id}/password", h.RequireAdmin(http.HandlerFunc(h.handleResetPassword)))

	// 分组管理
	mux.Handle("GET /api/admin/groups", h.RequireAdmin(http.HandlerFunc(h.handleListGroups)))
	mux.Handle("POST /api/admin/groups", h.RequireAdmin(http.HandlerFunc(h.handleCreateGroup)))
	mux.Handle("PUT /api/admin/groups/{id}/quota", h.RequireAdmin(http.HandlerFunc(h.handleSetGroupQuota)))

	// 统计查询
	mux.Handle("GET /api/admin/stats/summary", h.RequireAdmin(http.HandlerFunc(h.handleStatsSummary)))
	mux.Handle("GET /api/admin/stats/users", h.RequireAdmin(http.HandlerFunc(h.handleStatsUsers)))
	mux.Handle("GET /api/admin/stats/logs", h.RequireAdmin(http.HandlerFunc(h.handleStatsLogs)))

	// 审计日志（P2-3）
	mux.Handle("GET /api/admin/audit", h.RequireAdmin(http.HandlerFunc(h.handleListAudit)))

	// 数据导出（F-2）
	mux.Handle("GET /api/admin/export", h.RequireAdmin(http.HandlerFunc(h.handleExport)))

	// API Key 管理（F-5）
	mux.Handle("GET /api/admin/api-keys", h.RequireAdmin(http.HandlerFunc(h.handleListAPIKeys)))
	mux.Handle("POST /api/admin/api-keys", h.RequireAdmin(http.HandlerFunc(h.handleCreateAPIKey)))
	mux.Handle("POST /api/admin/api-keys/{id}/assign", h.RequireAdmin(http.HandlerFunc(h.handleAssignAPIKey)))
	mux.Handle("DELETE /api/admin/api-keys/{id}", h.RequireAdmin(http.HandlerFunc(h.handleRevokeAPIKey)))
}

// RequireAdmin 中间件：验证 Bearer token 或 cookie 中携带有效的管理员 JWT
func (h *AdminHandler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			if c, err := r.Cookie(AdminCookieName); err == nil {
				token = c.Value
			}
		}
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
			return
		}
		claims, err := h.jwtMgr.Parse(token)
		if err != nil || claims.Role != "admin" {
			h.logger.Warn("admin auth failed",
				zap.String("path", r.URL.Path),
				zap.Error(err),
			)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or insufficient privileges")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// POST /api/admin/login
// ---------------------------------------------------------------------------

type adminLoginRequest struct {
	Password string `json:"password"`
}

type adminLoginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

func (h *AdminHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password is required")
		return
	}
	if h.adminPasswordHash == "" || !auth.VerifyPassword(h.logger, h.adminPasswordHash, req.Password) {
		h.logger.Warn("admin login: invalid password")
		writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "incorrect password")
		return
	}

	token, err := h.jwtMgr.Sign(auth.JWTClaims{
		UserID:   adminUserID,
		Username: "admin",
		Role:     "admin",
	}, h.tokenTTL)
	if err != nil {
		h.logger.Error("admin login: sign token failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "token generation failed")
		return
	}

	h.logger.Info("admin logged in")
	writeJSON(w, http.StatusOK, adminLoginResponse{
		Token:     token,
		ExpiresIn: int64(h.tokenTTL.Seconds()),
	})
}

// ---------------------------------------------------------------------------
// User management
// ---------------------------------------------------------------------------

type userResponse struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	GroupID     *string `json:"group_id"`
	GroupName   string  `json:"group_name,omitempty"`
	IsActive    bool    `json:"is_active"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at,omitempty"`
}

func userToResponse(u db.User) userResponse {
	resp := userResponse{
		ID:        u.ID,
		Username:  u.Username,
		GroupID:   u.GroupID,
		GroupName: u.Group.Name,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.UTC().Format(time.RFC3339)
		resp.LastLoginAt = &s
	}
	return resp
}

func (h *AdminHandler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	groupID := r.URL.Query().Get("group_id")
	users, err := h.userRepo.ListByGroup(groupID)
	if err != nil {
		h.logger.Error("list users failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list users")
		return
	}
	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, userToResponse(u))
	}
	writeJSON(w, http.StatusOK, resp)
}

type createUserRequest struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	GroupID  *string `json:"group_id"`
	IsActive *bool   `json:"is_active"`
}

func (h *AdminHandler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}

	hash, err := auth.HashPassword(h.logger, req.Password)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid password")
		return
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	user := &db.User{
		Username:     req.Username,
		PasswordHash: hash,
		GroupID:      req.GroupID,
		IsActive:     isActive,
		CreatedAt:    time.Now(),
	}
	if err := h.userRepo.Create(user); err != nil {
		h.logger.Error("create user failed", zap.String("username", req.Username), zap.Error(err))
		writeJSONError(w, http.StatusConflict, "conflict", "username already exists or invalid group")
		return
	}

	h.logger.Info("admin created user", zap.String("username", req.Username))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"group_id": req.GroupID, "is_active": isActive}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "user.create", req.Username, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	writeJSON(w, http.StatusCreated, userToResponse(*user))
}

type setActiveRequest struct {
	Active bool `json:"active"`
}

func (h *AdminHandler) handleSetUserActive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setActiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := h.userRepo.SetActive(id, req.Active); err != nil {
		h.logger.Error("set user active failed", zap.String("user_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to update user")
		return
	}
	h.logger.Info("admin updated user active status",
		zap.String("user_id", id),
		zap.Bool("active", req.Active),
	)
	if detailBytes, jerr := json.Marshal(map[string]bool{"active": req.Active}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "user.set_active", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

func (h *AdminHandler) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password is required")
		return
	}
	hash, err := auth.HashPassword(h.logger, req.Password)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid password")
		return
	}
	if err := h.userRepo.UpdatePassword(id, hash); err != nil {
		h.logger.Error("reset password failed", zap.String("user_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to update password")
		return
	}
	h.logger.Info("admin reset user password", zap.String("user_id", id))
	if aerr := h.auditRepo.Create("admin", "user.reset_password", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Group management
// ---------------------------------------------------------------------------

type groupResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DailyTokenLimit   *int64 `json:"daily_token_limit"`
	MonthlyTokenLimit *int64 `json:"monthly_token_limit"`
	RequestsPerMinute *int   `json:"requests_per_minute"`
	CreatedAt         string `json:"created_at"`
}

func groupToResponse(g db.Group) groupResponse {
	return groupResponse{
		ID:                g.ID,
		Name:              g.Name,
		DailyTokenLimit:   g.DailyTokenLimit,
		MonthlyTokenLimit: g.MonthlyTokenLimit,
		RequestsPerMinute: g.RequestsPerMinute,
		CreatedAt:         g.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (h *AdminHandler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.groupRepo.List()
	if err != nil {
		h.logger.Error("list groups failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list groups")
		return
	}
	resp := make([]groupResponse, 0, len(groups))
	for _, g := range groups {
		resp = append(resp, groupToResponse(g))
	}
	writeJSON(w, http.StatusOK, resp)
}

type createGroupRequest struct {
	Name              string `json:"name"`
	DailyTokenLimit   *int64 `json:"daily_token_limit"`
	MonthlyTokenLimit *int64 `json:"monthly_token_limit"`
	RequestsPerMinute *int   `json:"requests_per_minute"`
}

func (h *AdminHandler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	g := &db.Group{
		Name:              req.Name,
		DailyTokenLimit:   req.DailyTokenLimit,
		MonthlyTokenLimit: req.MonthlyTokenLimit,
		RequestsPerMinute: req.RequestsPerMinute,
		CreatedAt:         time.Now(),
	}
	if err := h.groupRepo.Create(g); err != nil {
		h.logger.Error("create group failed", zap.String("name", req.Name), zap.Error(err))
		writeJSONError(w, http.StatusConflict, "conflict", "group name already exists")
		return
	}
	h.logger.Info("admin created group", zap.String("name", req.Name))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"daily_limit": req.DailyTokenLimit, "monthly_limit": req.MonthlyTokenLimit, "rpm": req.RequestsPerMinute}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "group.create", req.Name, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	writeJSON(w, http.StatusCreated, groupToResponse(*g))
}

type setQuotaRequest struct {
	DailyTokenLimit     *int64 `json:"daily_token_limit"`
	MonthlyTokenLimit   *int64 `json:"monthly_token_limit"`
	RequestsPerMinute   *int   `json:"requests_per_minute"`
	MaxTokensPerRequest *int64 `json:"max_tokens_per_request"`
	ConcurrentRequests  *int   `json:"concurrent_requests"`
}

func (h *AdminHandler) handleSetGroupQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := h.groupRepo.SetQuota(id, req.DailyTokenLimit, req.MonthlyTokenLimit, req.RequestsPerMinute, req.MaxTokensPerRequest, req.ConcurrentRequests); err != nil {
		h.logger.Error("set group quota failed", zap.String("group_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to update quota")
		return
	}
	h.logger.Info("admin updated group quota",
		zap.String("group_id", id),
		zap.Any("daily", req.DailyTokenLimit),
		zap.Any("monthly", req.MonthlyTokenLimit),
		zap.Any("rpm", req.RequestsPerMinute),
		zap.Any("max_tokens_per_request", req.MaxTokensPerRequest),
		zap.Any("concurrent_requests", req.ConcurrentRequests),
	)
	if detailBytes, jerr := json.Marshal(map[string]interface{}{
		"daily_limit":            req.DailyTokenLimit,
		"monthly_limit":          req.MonthlyTokenLimit,
		"rpm":                    req.RequestsPerMinute,
		"max_tokens_per_request": req.MaxTokensPerRequest,
		"concurrent_requests":    req.ConcurrentRequests,
	}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "group.set_quota", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

type statsSummaryResponse struct {
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	TotalTokens       int64   `json:"total_tokens"`
	RequestCount      int64   `json:"request_count"`
	ErrorCount        int64   `json:"error_count"`
	SuccessRate       float64 `json:"success_rate"` // 0..1
	CostUSD           float64 `json:"cost_usd"`     // 估算费用（USD）
	From              string  `json:"from"`
	To                string  `json:"to"`
}

func (h *AdminHandler) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 1)
	now := time.Now()
	from := now.AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
	to := now

	stats, err := h.usageRepo.GlobalSumTokens(from, to)
	if err != nil {
		h.logger.Error("stats summary failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get stats")
		return
	}

	costUSD, err := h.usageRepo.SumCostUSD(from, to)
	if err != nil {
		h.logger.Warn("stats summary: failed to get cost_usd", zap.Error(err))
		// non-fatal: cost 失败不阻断统计响应
	}

	var successRate float64
	if stats.RequestCount > 0 {
		successRate = float64(stats.RequestCount-stats.ErrorCount) / float64(stats.RequestCount)
	}
	writeJSON(w, http.StatusOK, statsSummaryResponse{
		TotalInputTokens:  stats.TotalInput,
		TotalOutputTokens: stats.TotalOutput,
		TotalTokens:       stats.TotalInput + stats.TotalOutput,
		RequestCount:      stats.RequestCount,
		ErrorCount:        stats.ErrorCount,
		SuccessRate:       successRate,
		CostUSD:           costUSD,
		From:              from.UTC().Format(time.RFC3339),
		To:                to.UTC().Format(time.RFC3339),
	})
}

type userStatsResponse struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username,omitempty"`
	TotalInput   int64  `json:"total_input_tokens"`
	TotalOutput  int64  `json:"total_output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	RequestCount int64  `json:"request_count"`
}

func (h *AdminHandler) handleStatsUsers(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	now := time.Now()
	from := now.AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
	to := now

	rows, err := h.usageRepo.UserStats(from, to, 50)
	if err != nil {
		h.logger.Error("user stats failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get user stats")
		return
	}

	resp := make([]userStatsResponse, 0, len(rows))
	for _, row := range rows {
		item := userStatsResponse{
			UserID:       row.UserID,
			TotalInput:   row.TotalInput,
			TotalOutput:  row.TotalOutput,
			TotalTokens:  row.TotalInput + row.TotalOutput,
			RequestCount: row.RequestCount,
		}
		if u, err := h.userRepo.GetByID(row.UserID); err == nil && u != nil {
			item.Username = u.Username
		}
		resp = append(resp, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

type logEntryResponse struct {
	ID           uint   `json:"id"`
	RequestID    string `json:"request_id"`
	UserID       string `json:"user_id"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	StatusCode   int    `json:"status_code"`
	DurationMs   int64  `json:"duration_ms"`
	IsStreaming  bool   `json:"is_streaming"`
	CreatedAt    string `json:"created_at"`
}

func (h *AdminHandler) handleStatsLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	userID := q.Get("user_id")
	limit := 50
	if s := q.Get("limit"); s != "" {
		if l, err := strconv.Atoi(s); err == nil && l > 0 {
			limit = l
		}
	}

	logs, err := h.usageRepo.Query(db.UsageFilter{UserID: userID, Limit: limit})
	if err != nil {
		h.logger.Error("stats logs failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get logs")
		return
	}

	resp := make([]logEntryResponse, 0, len(logs))
	for _, l := range logs {
		resp = append(resp, logEntryResponse{
			ID:           l.ID,
			RequestID:    l.RequestID,
			UserID:       l.UserID,
			Model:        l.Model,
			InputTokens:  l.InputTokens,
			OutputTokens: l.OutputTokens,
			TotalTokens:  l.TotalTokens,
			StatusCode:   l.StatusCode,
			DurationMs:   l.DurationMs,
			IsStreaming:  l.IsStreaming,
			CreatedAt:    l.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseDays 解析 URL 参数 "days"，默认值为 defaultVal
func parseDays(r *http.Request, defaultVal int) int {
	if s := r.URL.Query().Get("days"); s != "" {
		if d, err := strconv.Atoi(s); err == nil && d > 0 {
			return d
		}
	}
	return defaultVal
}

// ---------------------------------------------------------------------------
// 审计日志（P2-3）
// ---------------------------------------------------------------------------

type auditLogResponse struct {
	ID        uint   `json:"id"`
	Operator  string `json:"operator"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

// GET /api/admin/audit?limit=100
func (h *AdminHandler) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if l, err := strconv.Atoi(s); err == nil && l > 0 {
			limit = l
		}
	}
	logs, err := h.auditRepo.ListRecent(limit)
	if err != nil {
		h.logger.Error("list audit failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list audit logs")
		return
	}
	resp := make([]auditLogResponse, 0, len(logs))
	for _, l := range logs {
		resp = append(resp, auditLogResponse{
			ID:        l.ID,
			Operator:  l.Operator,
			Action:    l.Action,
			Target:    l.Target,
			Detail:    l.Detail,
			CreatedAt: l.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// 数据导出（F-2）
// ---------------------------------------------------------------------------

// exportCSVHeaders CSV 列头（与 exportLogToCSVRecord 顺序一致）
var exportCSVHeaders = []string{
	"id", "request_id", "user_id", "model",
	"input_tokens", "output_tokens", "total_tokens",
	"is_streaming", "status_code", "duration_ms",
	"cost_usd", "source_node", "upstream_url", "created_at",
}

// GET /api/admin/export?format=csv|json&from=2024-01-01&to=2024-01-31
//
// 响应为流式文件下载（Content-Disposition: attachment）。
// format=json  → NDJSON（每行一个 JSON 对象），便于大文件增量处理
// format=csv   → CSV，首行为列头（含 UTF-8 BOM，兼容 Excel）
func (h *AdminHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "format must be csv or json")
		return
	}

	// 解析时间范围（默认近 30 天）
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
	to := now

	if s := q.Get("from"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be YYYY-MM-DD")
			return
		}
		from = t.UTC()
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "to must be YYYY-MM-DD")
			return
		}
		// 包含当天的最后一刻
		to = t.UTC().Add(24*time.Hour - time.Nanosecond)
	}

	h.logger.Info("export requested",
		zap.String("format", format),
		zap.Time("from", from),
		zap.Time("to", to),
	)

	// 设置下载响应头
	filename := fmt.Sprintf("pairproxy-export-%s-to-%s.%s",
		from.Format("2006-01-02"), to.Format("2006-01-02"), format)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		// 写 UTF-8 BOM（兼容 Excel 直接打开）
		if _, err := fmt.Fprint(w, "\xEF\xBB\xBF"); err != nil {
			h.logger.Warn("export csv: failed to write BOM", zap.Error(err))
			return
		}
		cw := csv.NewWriter(w)
		if err := cw.Write(exportCSVHeaders); err != nil {
			h.logger.Warn("export csv: failed to write header", zap.Error(err))
			return
		}
		exported := 0
		err := h.usageRepo.ExportLogs(from, to, func(l db.UsageLog) error {
			if werr := cw.Write(exportLogToCSVRecord(l)); werr != nil {
				return werr
			}
			exported++
			if exported%500 == 0 {
				cw.Flush()
			}
			return nil
		})
		cw.Flush()
		if err != nil {
			h.logger.Error("export csv: failed", zap.Error(err))
		} else {
			h.logger.Info("export csv complete", zap.Int("rows", exported))
		}
	} else {
		// NDJSON（Newline Delimited JSON）
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		enc := json.NewEncoder(w)
		exported := 0
		err := h.usageRepo.ExportLogs(from, to, func(l db.UsageLog) error {
			if werr := enc.Encode(exportLogToJSON(l)); werr != nil {
				return werr
			}
			exported++
			if exported%500 == 0 {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return nil
		})
		if err != nil {
			h.logger.Error("export json: failed", zap.Error(err))
		} else {
			h.logger.Info("export json complete", zap.Int("rows", exported))
		}
	}
}

// exportLogToJSON 将 UsageLog 转为导出 JSON 对象。
func exportLogToJSON(l db.UsageLog) map[string]interface{} {
	return map[string]interface{}{
		"id":            l.ID,
		"request_id":    l.RequestID,
		"user_id":       l.UserID,
		"model":         l.Model,
		"input_tokens":  l.InputTokens,
		"output_tokens": l.OutputTokens,
		"total_tokens":  l.TotalTokens,
		"is_streaming":  l.IsStreaming,
		"status_code":   l.StatusCode,
		"duration_ms":   l.DurationMs,
		"cost_usd":      l.CostUSD,
		"source_node":   l.SourceNode,
		"upstream_url":  l.UpstreamURL,
		"created_at":    l.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// exportLogToCSVRecord 将 UsageLog 转为 CSV 行（与 exportCSVHeaders 对应）。
func exportLogToCSVRecord(l db.UsageLog) []string {
	return []string{
		strconv.FormatUint(uint64(l.ID), 10),
		l.RequestID,
		l.UserID,
		l.Model,
		strconv.Itoa(l.InputTokens),
		strconv.Itoa(l.OutputTokens),
		strconv.Itoa(l.TotalTokens),
		strconv.FormatBool(l.IsStreaming),
		strconv.Itoa(l.StatusCode),
		strconv.FormatInt(l.DurationMs, 10),
		fmt.Sprintf("%.6f", l.CostUSD),
		l.SourceNode,
		l.UpstreamURL,
		l.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// F-5: API Key 管理
// ---------------------------------------------------------------------------

type createAPIKeyRequest struct {
	Name     string `json:"name"`
	Value    string `json:"value"`    // 明文 API Key 值（由 encryptFn 加密后存储）
	Provider string `json:"provider"` // "anthropic" | "openai" | "ollama"
}

type apiKeyResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
}

func apiKeyToResponse(k db.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:        k.ID,
		Name:      k.Name,
		Provider:  k.Provider,
		IsActive:  k.IsActive,
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// GET /api/admin/api-keys
func (h *AdminHandler) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if h.apiKeyRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "api key management not configured")
		return
	}
	keys, err := h.apiKeyRepo.List()
	if err != nil {
		h.logger.Error("list api keys failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list api keys")
		return
	}
	resp := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, apiKeyToResponse(k))
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/admin/api-keys
func (h *AdminHandler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.apiKeyRepo == nil || h.encryptFn == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "api key management not configured")
		return
	}
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name and value are required")
		return
	}
	encrypted, err := h.encryptFn(req.Value)
	if err != nil {
		h.logger.Error("encrypt api key failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to encrypt api key")
		return
	}
	key, err := h.apiKeyRepo.Create(req.Name, encrypted, req.Provider)
	if err != nil {
		h.logger.Error("create api key failed", zap.String("name", req.Name), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create api key")
		return
	}
	h.logger.Info("admin created api key", zap.String("name", req.Name), zap.String("provider", key.Provider))
	writeJSON(w, http.StatusCreated, apiKeyToResponse(*key))
}

type assignAPIKeyRequest struct {
	UserID  *string `json:"user_id"`  // 分配给用户（优先）
	GroupID *string `json:"group_id"` // 分配给分组（兜底）
}

// POST /api/admin/api-keys/{id}/assign
func (h *AdminHandler) handleAssignAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.apiKeyRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "api key management not configured")
		return
	}
	id := r.PathValue("id")
	var req assignAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.UserID == nil && req.GroupID == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id or group_id required")
		return
	}
	if err := h.apiKeyRepo.Assign(id, req.UserID, req.GroupID); err != nil {
		h.logger.Error("assign api key failed", zap.String("key_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to assign api key")
		return
	}
	h.logger.Info("admin assigned api key",
		zap.String("key_id", id),
		zap.Any("user_id", req.UserID),
		zap.Any("group_id", req.GroupID),
	)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/admin/api-keys/{id}
func (h *AdminHandler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.apiKeyRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "api key management not configured")
		return
	}
	id := r.PathValue("id")
	if err := h.apiKeyRepo.Revoke(id); err != nil {
		h.logger.Error("revoke api key failed", zap.String("key_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to revoke api key")
		return
	}
	h.logger.Info("admin revoked api key", zap.String("key_id", id))
	w.WriteHeader(http.StatusNoContent)
}
