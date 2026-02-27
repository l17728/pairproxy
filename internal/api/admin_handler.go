package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
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
	adminPasswordHash string,
	tokenTTL time.Duration,
) *AdminHandler {
	return &AdminHandler{
		logger:            logger.Named("admin_handler"),
		jwtMgr:            jwtMgr,
		userRepo:          userRepo,
		groupRepo:         groupRepo,
		usageRepo:         usageRepo,
		adminPasswordHash: adminPasswordHash,
		tokenTTL:          tokenTTL,
	}
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
	writeJSON(w, http.StatusCreated, groupToResponse(*g))
}

type setQuotaRequest struct {
	DailyTokenLimit   *int64 `json:"daily_token_limit"`
	MonthlyTokenLimit *int64 `json:"monthly_token_limit"`
	RequestsPerMinute *int   `json:"requests_per_minute"`
}

func (h *AdminHandler) handleSetGroupQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := h.groupRepo.SetQuota(id, req.DailyTokenLimit, req.MonthlyTokenLimit, req.RequestsPerMinute); err != nil {
		h.logger.Error("set group quota failed", zap.String("group_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to update quota")
		return
	}
	h.logger.Info("admin updated group quota",
		zap.String("group_id", id),
		zap.Any("daily", req.DailyTokenLimit),
		zap.Any("monthly", req.MonthlyTokenLimit),
		zap.Any("rpm", req.RequestsPerMinute),
	)
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
