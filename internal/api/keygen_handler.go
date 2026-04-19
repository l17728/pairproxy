package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/keygen"
)

// KeygenHandler 提供用户自助 API Key 生成和用量查看功能。
//
// 路由：
//   - GET  /keygen/                    — 静态 HTML 页面（用量中心）
//   - POST /keygen/api/login           — 用户名+密码登录，返回 key + session token
//   - POST /keygen/api/regenerate      — 用 session token 查看当前 key
//   - POST /keygen/api/change-password — 修改密码，旧 Key 立即失效，返回新 Key
//   - GET  /keygen/api/quota           — 查询自己的配额使用情况（需 Bearer token）
//   - GET  /keygen/api/history         — 查询自己的每日用量历史（需 Bearer token）
//   - GET  /keygen/api/logs            — 查询自己的请求日志分页（需 Bearer token）
//
// 与 Dashboard 完全独立：使用普通用户密码，不使用管理员密码。
// API Key 由用户自己的 PasswordHash 派生（HMAC-SHA256），改密码即自动轮换 Key。
type KeygenHandler struct {
	logger       *zap.Logger
	userRepo     *db.UserRepo
	usageRepo    *db.UsageRepo  // 用量查询（可选，nil 时跳过用量相关端点）
	groupRepo    *db.GroupRepo  // 分组配额查询（可选）
	jwtMgr       *auth.Manager
	keyCache     *keygen.KeyCache // 可选，改密后立即踢出旧 Key 缓存
	isWorkerNode bool
}

// NewKeygenHandler 创建 KeygenHandler。
func NewKeygenHandler(logger *zap.Logger, userRepo *db.UserRepo, jwtMgr *auth.Manager) *KeygenHandler {
	return &KeygenHandler{
		logger:   logger.Named("keygen_handler"),
		userRepo: userRepo,
		jwtMgr:   jwtMgr,
	}
}

// SetKeyCache 注入 API Key 缓存（改密后立即踢出旧 Key，不等 TTL 自然过期）。
func (h *KeygenHandler) SetKeyCache(cache *keygen.KeyCache) { h.keyCache = cache }

// SetUsageRepo 注入用量仓库（用于用量中心数据接口）。
func (h *KeygenHandler) SetUsageRepo(r *db.UsageRepo) { h.usageRepo = r }

// SetGroupRepo 注入分组仓库（用于查询配额限制）。
func (h *KeygenHandler) SetGroupRepo(r *db.GroupRepo) { h.groupRepo = r }

// SetWorkerMode 设置 Worker 节点模式；Worker 节点不允许写操作（API Key 生成/重置）。
//
// 注意：必须在 RegisterRoutes 之前调用，因为封锁逻辑在路由注册时分支，
// 而不是运行时中间件判断。调用顺序颠倒会导致封锁静默失效。
func (h *KeygenHandler) SetWorkerMode(isWorker bool) {
	h.isWorkerNode = isWorker
}

// RegisterRoutes 注册 /keygen/ 相关路由。
func (h *KeygenHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /keygen/", h.handleStaticPage)

	// 只读数据接口：Worker 节点也可用
	mux.HandleFunc("GET /keygen/api/quota", h.handleQuota)
	mux.HandleFunc("GET /keygen/api/history", h.handleHistory)
	mux.HandleFunc("GET /keygen/api/logs", h.handleLogs)

	if h.isWorkerNode {
		// Worker 节点：写端点返回 403，引导用户到 Primary 节点操作
		blockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.logger.Warn("blocked keygen write operation on worker node",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			writeKeygenError(w, http.StatusForbidden, "worker_read_only",
				"API key operations are not available on worker nodes; please use the primary node")
		})
		mux.Handle("POST /keygen/api/login", blockHandler)
		mux.Handle("POST /keygen/api/regenerate", blockHandler)
		mux.Handle("POST /keygen/api/change-password", blockHandler)
	} else {
		mux.HandleFunc("POST /keygen/api/login", h.handleLogin)
		mux.HandleFunc("POST /keygen/api/regenerate", h.handleRegenerate)
		mux.HandleFunc("POST /keygen/api/change-password", h.handleChangePassword)
	}
}

// parseBearer 从 Authorization 头提取并验证 Bearer token，返回 claims。
// 验证失败时直接写错误响应并返回 nil, false。
func (h *KeygenHandler) parseBearer(w http.ResponseWriter, r *http.Request) (*auth.JWTClaims, bool) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeKeygenError(w, http.StatusUnauthorized, "missing_token", "Authorization: Bearer <token> required")
		return nil, false
	}
	claims, err := h.jwtMgr.Parse(strings.TrimPrefix(authHeader, "Bearer "))
	if err != nil {
		writeKeygenError(w, http.StatusUnauthorized, "session_expired", "会话已过期，请重新登录")
		return nil, false
	}
	return claims, true
}

// keygenLoginRequest 登录请求体
type keygenLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// keygenLoginResponse 登录响应体
type keygenLoginResponse struct {
	Username  string `json:"username"`
	Key       string `json:"key"`
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

// keygenRegenerateResponse 重新生成 key 响应体
type keygenRegenerateResponse struct {
	Username string `json:"username"`
	Key      string `json:"key"`
	Message  string `json:"message"`
}

// keygenQuotaResponse 用量配额响应
type keygenQuotaResponse struct {
	DailyLimit    int64 `json:"daily_limit"`
	DailyUsed     int64 `json:"daily_used"`
	DailyRemain   int64 `json:"daily_remain"`
	MonthlyLimit  int64 `json:"monthly_limit"`
	MonthlyUsed   int64 `json:"monthly_used"`
	MonthlyRemain int64 `json:"monthly_remain"`
	RPMLimit      int   `json:"rpm_limit"`
}

func (h *KeygenHandler) handleStaticPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(keygenHTML))
}

func (h *KeygenHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req keygenLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("keygen login: invalid request body", zap.Error(err))
		writeKeygenError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		h.logger.Warn("keygen login: missing required fields",
			zap.Bool("has_username", req.Username != ""),
			zap.Bool("has_password", req.Password != ""),
		)
		writeKeygenError(w, http.StatusBadRequest, "missing_fields", "username and password required")
		return
	}

	// 查询用户（仅本地账户）
	user, err := h.userRepo.GetByUsernameAndProvider(req.Username, "local")
	if err != nil {
		h.logger.Error("keygen login: db error", zap.String("username", req.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "user lookup failed")
		return
	}
	if user == nil || !user.IsActive {
		h.logger.Warn("keygen login: user not found or inactive", zap.String("username", req.Username))
		writeKeygenError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}

	// 验证密码
	if !auth.VerifyPassword(h.logger, user.PasswordHash, req.Password) {
		h.logger.Warn("keygen login: wrong password", zap.String("username", req.Username))
		writeKeygenError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}

	// 生成 API Key（由用户自己的 PasswordHash 派生，改密码即自动轮换）
	apiKey, err := keygen.GenerateKey(req.Username, []byte(user.PasswordHash))
	if err != nil {
		h.logger.Error("keygen login: key generation failed",
			zap.String("username", req.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate API key")
		return
	}

	// 生成 session token（1小时有效）
	groupIDStr := ""
	if user.GroupID != nil {
		groupIDStr = *user.GroupID
	}
	token, err := h.jwtMgr.Sign(auth.JWTClaims{
		UserID:   user.ID,
		Username: req.Username,
		GroupID:  groupIDStr,
		Role:     "user",
	}, time.Hour)
	if err != nil {
		h.logger.Error("keygen login: token issue failed", zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "token_error", "session token generation failed")
		return
	}

	h.logger.Info("keygen login: success",
		zap.String("username", req.Username),
		zap.String("user_id", user.ID),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keygenLoginResponse{
		Username:  req.Username,
		Key:       apiKey,
		Token:     token,
		ExpiresIn: 3600,
	})
}

func (h *KeygenHandler) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.parseBearer(w, r)
	if !ok {
		return
	}

	// 从 DB 重新取用户以获取最新 PasswordHash（密码可能已被管理员重置）
	user, err := h.userRepo.GetByID(claims.UserID)
	if err != nil {
		h.logger.Error("keygen regenerate: user lookup failed",
			zap.String("user_id", claims.UserID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "user lookup failed")
		return
	}
	if user == nil || !user.IsActive {
		h.logger.Warn("keygen regenerate: user not found or inactive",
			zap.String("user_id", claims.UserID))
		writeKeygenError(w, http.StatusUnauthorized, "account_disabled", "user account not found or disabled")
		return
	}

	apiKey, err := keygen.GenerateKey(user.Username, []byte(user.PasswordHash))
	if err != nil {
		h.logger.Error("keygen regenerate: key generation failed",
			zap.String("username", user.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate API key")
		return
	}

	h.logger.Info("keygen regenerate: key derived",
		zap.String("username", user.Username),
		zap.String("user_id", user.ID),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keygenRegenerateResponse{
		Username: user.Username,
		Key:      apiKey,
		Message:  "Key 已获取（由密码派生，改密码可轮换）",
	})
}

// keygenChangePasswordRequest 修改密码请求体
type keygenChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// keygenChangePasswordResponse 修改密码响应体
type keygenChangePasswordResponse struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

func (h *KeygenHandler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.parseBearer(w, r)
	if !ok {
		return
	}

	var req keygenChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeKeygenError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		writeKeygenError(w, http.StatusBadRequest, "missing_fields", "old_password and new_password required")
		return
	}
	if req.OldPassword == req.NewPassword {
		writeKeygenError(w, http.StatusBadRequest, "same_password", "新密码不能与旧密码相同")
		return
	}

	// 从 DB 取用户（仅本地账户可修改密码）
	user, err := h.userRepo.GetByID(claims.UserID)
	if err != nil {
		h.logger.Error("keygen change-password: db error", zap.String("user_id", claims.UserID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "user lookup failed")
		return
	}
	if user == nil || !user.IsActive {
		writeKeygenError(w, http.StatusUnauthorized, "account_disabled", "账户不存在或已被禁用")
		return
	}
	if user.AuthProvider != "local" {
		writeKeygenError(w, http.StatusForbidden, "ldap_user", "LDAP 用户请通过 LDAP 管理平台修改密码")
		return
	}

	// 验证旧密码
	if !auth.VerifyPassword(h.logger, user.PasswordHash, req.OldPassword) {
		h.logger.Warn("keygen change-password: wrong old password", zap.String("username", user.Username))
		writeKeygenError(w, http.StatusUnauthorized, "wrong_password", "旧密码错误")
		return
	}

	// Hash 新密码并更新 DB
	newHash, err := auth.HashPassword(h.logger, req.NewPassword)
	if err != nil {
		writeKeygenError(w, http.StatusBadRequest, "invalid_password", "invalid new password")
		return
	}
	if err := h.userRepo.UpdatePassword(user.ID, newHash); err != nil {
		h.logger.Error("keygen change-password: update failed", zap.String("user_id", user.ID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "failed to update password")
		return
	}

	// 旧 Key 立即失效
	if h.keyCache != nil {
		h.keyCache.InvalidateByUserID(user.ID)
	}

	// 用新 PasswordHash 派生新 Key
	newKey, err := keygen.GenerateKey(user.Username, []byte(newHash))
	if err != nil {
		h.logger.Error("keygen change-password: key generation failed", zap.String("username", user.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate new API key")
		return
	}

	h.logger.Info("keygen change-password: success",
		zap.String("username", user.Username),
		zap.String("user_id", user.ID),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keygenChangePasswordResponse{
		Key:     newKey,
		Message: "密码已修改，新 Key 已生成，旧 Key 立即失效",
	})
}

// handleQuota GET /keygen/api/quota — 返回当前登录用户的配额使用情况。
func (h *KeygenHandler) handleQuota(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.parseBearer(w, r)
	if !ok {
		return
	}

	resp := keygenQuotaResponse{DailyRemain: -1, MonthlyRemain: -1}

	if h.usageRepo != nil {
		now := time.Now().UTC()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

		dailyIn, dailyOut, _ := h.usageRepo.SumTokens(claims.UserID, todayStart, now)
		monthlyIn, monthlyOut, _ := h.usageRepo.SumTokens(claims.UserID, monthStart, now)
		resp.DailyUsed = dailyIn + dailyOut
		resp.MonthlyUsed = monthlyIn + monthlyOut
	}

	if h.groupRepo != nil {
		user, err := h.userRepo.GetByID(claims.UserID)
		if err == nil && user != nil && user.GroupID != nil {
			if group, err := h.groupRepo.GetByID(*user.GroupID); err == nil {
				if group.DailyTokenLimit != nil {
					resp.DailyLimit = *group.DailyTokenLimit
				}
				if group.MonthlyTokenLimit != nil {
					resp.MonthlyLimit = *group.MonthlyTokenLimit
				}
				if group.RequestsPerMinute != nil {
					resp.RPMLimit = *group.RequestsPerMinute
				}
			}
		}
	}

	if resp.DailyLimit > 0 {
		resp.DailyRemain = resp.DailyLimit - resp.DailyUsed
	}
	if resp.MonthlyLimit > 0 {
		resp.MonthlyRemain = resp.MonthlyLimit - resp.MonthlyUsed
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleHistory GET /keygen/api/history?days=N — 返回当前用户每日用量历史。
func (h *KeygenHandler) handleHistory(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.parseBearer(w, r)
	if !ok {
		return
	}

	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	if h.usageRepo == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"history": []interface{}{}})
		return
	}

	now := time.Now()
	from := now.AddDate(0, 0, -days)

	rows, err := h.usageRepo.DailyTokens(from, now, claims.UserID)
	if err != nil {
		h.logger.Error("keygen history: failed", zap.String("user_id", claims.UserID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "failed to get usage history")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"history": rows})
}

// handleLogs GET /keygen/api/logs?page=N&page_size=N&days=N — 分页请求日志。
func (h *KeygenHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.parseBearer(w, r)
	if !ok {
		return
	}

	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	pageSize := 10
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil {
		switch ps {
		case 10, 20, 50:
			pageSize = ps
		}
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	type logEntry struct {
		CreatedAt    string  `json:"created_at"`
		Model        string  `json:"model"`
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		CostUSD      float64 `json:"cost_usd"`
		StatusCode   int     `json:"status_code"`
		IsStreaming  bool    `json:"is_streaming"`
		DurationMs   int64   `json:"duration_ms"`
	}
	type logsResp struct {
		Logs       []logEntry `json:"logs"`
		Total      int64      `json:"total"`
		Page       int        `json:"page"`
		PageSize   int        `json:"page_size"`
		TotalPages int        `json:"total_pages"`
	}

	if h.usageRepo == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logsResp{Logs: []logEntry{}, TotalPages: 1})
		return
	}

	now := time.Now()
	from := now.AddDate(0, 0, -days)

	filter := db.UsageFilter{
		UserID: claims.UserID,
		From:   &from,
		To:     &now,
	}

	total, err := h.usageRepo.CountLogs(filter)
	if err != nil {
		h.logger.Error("keygen logs: count failed", zap.String("user_id", claims.UserID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "failed to count logs")
		return
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize

	logs, err := h.usageRepo.Query(filter)
	if err != nil {
		h.logger.Error("keygen logs: query failed", zap.String("user_id", claims.UserID), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "failed to query logs")
		return
	}

	entries := make([]logEntry, 0, len(logs))
	for _, l := range logs {
		entries = append(entries, logEntry{
			CreatedAt:    l.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			Model:        l.Model,
			InputTokens:  l.InputTokens,
			OutputTokens: l.OutputTokens,
			CostUSD:      l.CostUSD,
			StatusCode:   l.StatusCode,
			IsStreaming:  l.IsStreaming,
			DurationMs:   l.DurationMs,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(logsResp{
		Logs:       entries,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	})
}

func writeKeygenError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

// keygenHTML 是内嵌的用量中心页面（避免静态文件依赖）。
const keygenHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>PairProxy 用量中心</title>
<script src="https://cdn.tailwindcss.com"></script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.0/dist/chart.umd.min.js"></script>
</head>
<body class="bg-gray-50 min-h-screen">

<!-- 登录界面 -->
<div id="loginScreen" class="min-h-screen flex items-center justify-center px-4">
  <div class="bg-white rounded-2xl shadow-lg border border-gray-100 p-8 w-full max-w-sm">
    <div class="text-center mb-6">
      <h1 class="text-2xl font-bold text-gray-800">PairProxy</h1>
      <p class="text-sm text-gray-500 mt-1">用量中心</p>
    </div>
    <div class="space-y-4">
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
        <input type="text" id="loginUsername" autocomplete="username"
          class="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500"
          placeholder="请输入用户名">
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
        <input type="password" id="loginPassword" autocomplete="current-password"
          class="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500"
          placeholder="请输入密码">
      </div>
      <div id="loginError" class="hidden text-red-600 text-sm bg-red-50 border border-red-200 rounded-lg px-3 py-2"></div>
      <button onclick="login()"
        class="w-full bg-indigo-600 text-white rounded-lg py-2.5 text-sm font-medium hover:bg-indigo-700 transition-colors">
        登 录
      </button>
    </div>
  </div>
</div>

<!-- 仪表板界面 -->
<div id="dashScreen" class="hidden">
  <!-- 顶栏 -->
  <header class="bg-white border-b border-gray-200 sticky top-0 z-10">
    <div class="max-w-5xl mx-auto px-4 h-14 flex items-center justify-between">
      <div class="flex items-center gap-2">
        <span class="font-bold text-gray-800">PairProxy</span>
        <span class="text-gray-300">|</span>
        <span class="text-sm text-gray-600">用量中心</span>
      </div>
      <div class="flex items-center gap-3">
        <span class="text-sm text-gray-600">欢迎，<strong id="welcomeName"></strong></span>
        <button onclick="logout()"
          class="text-sm text-gray-500 hover:text-gray-800 border border-gray-200 rounded-lg px-3 py-1.5 hover:bg-gray-50 transition-colors">
          退出登录
        </button>
      </div>
    </div>
  </header>

  <main class="max-w-5xl mx-auto px-4 py-6 space-y-6">

    <!-- API Key 卡片 -->
    <div class="bg-white rounded-xl shadow-sm border border-gray-100 p-6">
      <div class="flex items-start justify-between mb-3">
        <h2 class="text-sm font-semibold text-gray-700">我的 API Key</h2>
        <button id="copyBtn" onclick="copyKey()"
          class="text-xs text-indigo-600 border border-indigo-200 rounded-lg px-3 py-1.5 hover:bg-indigo-50 transition-colors">
          复制
        </button>
      </div>
      <div id="apiKeyDisplay"
        class="font-mono text-sm bg-gray-50 border border-gray-200 rounded-lg px-4 py-3 break-all text-gray-700 cursor-text">
      </div>
      <!-- 说明 -->
      <div class="mt-3 text-xs text-gray-500 space-y-1.5">
        <p>· Key 由您的密码派生，修改密码后自动更新，旧 Key 立即失效。</p>
        <p>· 使用 <strong>Claude Code</strong> 时，需将以下内容写入 <code class="bg-gray-100 px-1 rounded">~/.claude/settings.json</code>：</p>
        <div class="ml-3 space-y-1">
          <p class="text-gray-400">Windows：<code class="bg-gray-100 px-1 rounded text-gray-600">%USERPROFILE%\.claude\settings.json</code></p>
          <p class="text-gray-400">Linux / macOS：<code class="bg-gray-100 px-1 rounded text-gray-600">~/.claude/settings.json</code></p>
        </div>
        <pre id="ccSettingsSnippet" class="mt-2 text-xs bg-gray-900 text-green-400 rounded-lg px-4 py-3 overflow-x-auto whitespace-pre"></pre>
      </div>
    </div>

    <!-- 修改密码 -->
    <div class="bg-white rounded-xl shadow-sm border border-gray-100 p-6">
      <div class="mb-4">
        <h2 class="text-sm font-semibold text-gray-700">修改密码</h2>
        <p class="text-xs text-gray-400 mt-0.5">修改密码后，API Key 自动更新，旧 Key 立即失效</p>
      </div>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label class="block text-xs font-medium text-gray-700 mb-1">当前密码</label>
          <input type="password" id="oldPassword" autocomplete="current-password"
            class="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500">
        </div>
        <div>
          <label class="block text-xs font-medium text-gray-700 mb-1">新密码</label>
          <input type="password" id="newPassword" autocomplete="new-password"
            class="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500">
        </div>
        <div>
          <label class="block text-xs font-medium text-gray-700 mb-1">确认新密码</label>
          <input type="password" id="confirmPassword" autocomplete="new-password"
            class="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500">
        </div>
      </div>
      <div class="mt-4 flex items-center gap-3 flex-wrap">
        <button onclick="changePassword()"
          class="text-sm bg-red-600 text-white rounded-lg px-4 py-2 hover:bg-red-700 transition-colors">
          修改密码并更新 Key
        </button>
        <span id="changePwdError" class="hidden text-sm text-red-600"></span>
        <span id="changePwdSuccess" class="hidden text-sm text-green-600"></span>
      </div>
    </div>

    <!-- 配额卡片 -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div class="bg-white p-6 rounded-xl shadow-sm border border-gray-100">
        <p class="text-xs text-gray-500 uppercase tracking-wide mb-2">今日配额</p>
        <div class="flex items-end gap-2 mb-3">
          <p class="text-3xl font-bold text-indigo-600" id="dailyUsed">-</p>
          <p class="text-sm text-gray-400 mb-1">/ <span id="dailyLimit">-</span></p>
        </div>
        <div class="w-full bg-gray-200 rounded-full h-2">
          <div id="dailyProgress" class="bg-indigo-600 h-2 rounded-full transition-all" style="width:0%"></div>
        </div>
        <p class="text-xs text-gray-500 mt-2">剩余 <span id="dailyRemain">-</span> tokens</p>
      </div>
      <div class="bg-white p-6 rounded-xl shadow-sm border border-gray-100">
        <p class="text-xs text-gray-500 uppercase tracking-wide mb-2">本月配额</p>
        <div class="flex items-end gap-2 mb-3">
          <p class="text-3xl font-bold text-indigo-600" id="monthlyUsed">-</p>
          <p class="text-sm text-gray-400 mb-1">/ <span id="monthlyLimit">-</span></p>
        </div>
        <div class="w-full bg-gray-200 rounded-full h-2">
          <div id="monthlyProgress" class="bg-indigo-600 h-2 rounded-full transition-all" style="width:0%"></div>
        </div>
        <p class="text-xs text-gray-500 mt-2">剩余 <span id="monthlyRemain">-</span> tokens</p>
      </div>
    </div>

    <!-- 用量历史图表 -->
    <div class="bg-white rounded-xl shadow-sm border border-gray-100 p-6">
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-sm font-semibold text-gray-700">用量历史</h3>
        <select id="historyDaysSelect" onchange="loadHistory(parseInt(this.value))"
          class="text-xs border border-gray-300 rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500">
          <option value="7">最近 7 天</option>
          <option value="30" selected>最近 30 天</option>
          <option value="90">最近 90 天</option>
        </select>
      </div>
      <div style="position:relative;height:200px;">
        <canvas id="usageHistoryChart"></canvas>
      </div>
    </div>

    <!-- 最近请求 -->
    <div class="bg-white rounded-xl shadow-sm border border-gray-100 overflow-hidden">
      <div class="px-6 py-4 border-b border-gray-100 flex items-center justify-between flex-wrap gap-2">
        <h3 class="text-sm font-semibold text-gray-700">最近请求</h3>
        <div class="flex items-center gap-3">
          <label class="text-xs text-gray-500">时间范围：</label>
          <select id="logsDaysSelect" onchange="loadLogs(1)"
            class="text-xs border border-gray-300 rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500">
            <option value="7" selected>最近 7 天</option>
            <option value="14">最近 14 天</option>
            <option value="30">最近 30 天</option>
          </select>
          <label class="text-xs text-gray-500">每页：</label>
          <select id="logsPageSizeSelect" onchange="loadLogs(1)"
            class="text-xs border border-gray-300 rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500">
            <option value="10" selected>10 条</option>
            <option value="20">20 条</option>
            <option value="50">50 条</option>
          </select>
        </div>
      </div>
      <div class="overflow-x-auto">
        <table class="min-w-full divide-y divide-gray-100 text-sm">
          <thead class="bg-gray-50 text-xs text-gray-500 uppercase">
            <tr>
              <th class="px-4 py-3 text-left">时间</th>
              <th class="px-4 py-3 text-left">模型</th>
              <th class="px-4 py-3 text-right">输入</th>
              <th class="px-4 py-3 text-right">输出</th>
              <th class="px-4 py-3 text-right">费用($)</th>
              <th class="px-4 py-3 text-left">类型</th>
              <th class="px-4 py-3 text-left">状态</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-50 text-gray-700" id="logsBody">
            <tr><td colspan="7" class="px-4 py-8 text-center text-gray-400 text-sm">加载中...</td></tr>
          </tbody>
        </table>
      </div>
      <div id="logsPagination" class="hidden px-6 py-3 border-t border-gray-100 flex items-center justify-between text-xs text-gray-500">
        <span id="logsPageInfo">第 1 / 1 页，共 0 条</span>
        <div class="flex items-center gap-1">
          <button id="logsPrevBtn" onclick="goLogsPage(currentLogsPage - 1)"
            class="px-2 py-1 rounded border border-gray-200 hover:bg-gray-50 disabled:opacity-40 disabled:cursor-not-allowed">‹ 上一页</button>
          <span id="logsPageButtons" class="flex gap-1"></span>
          <button id="logsNextBtn" onclick="goLogsPage(currentLogsPage + 1)"
            class="px-2 py-1 rounded border border-gray-200 hover:bg-gray-50 disabled:opacity-40 disabled:cursor-not-allowed">下一页 ›</button>
        </div>
      </div>
    </div>

  </main>
</div>

<script>
const BASE = window.location.origin;
let sessionToken = '';
let currentKey = '';
let usageChart;
let currentLogsPage = 1;
let totalLogsPages = 1;

// ── 登录 ──────────────────────────────────────────────────────────────────────

async function login() {
  const username = document.getElementById('loginUsername').value.trim();
  const password = document.getElementById('loginPassword').value;
  const errEl = document.getElementById('loginError');
  errEl.classList.add('hidden');

  if (!username || !password) {
    errEl.textContent = '请填写用户名和密码';
    errEl.classList.remove('hidden');
    return;
  }

  try {
    const r = await fetch('/keygen/api/login', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username, password})
    });
    const data = await r.json();
    if (!r.ok) {
      errEl.textContent = data.message || '登录失败';
      errEl.classList.remove('hidden');
      return;
    }
    sessionToken = data.token;
    showDashboard(data.username, data.key);
  } catch (e) {
    errEl.textContent = '网络错误，请稍后重试';
    errEl.classList.remove('hidden');
  }
}

function showDashboard(username, key) {
  currentKey = key;
  document.getElementById('loginScreen').classList.add('hidden');
  document.getElementById('dashScreen').classList.remove('hidden');
  document.getElementById('welcomeName').textContent = username;
  updateKeyDisplay(key);
  loadQuota();
  loadHistory(parseInt(document.getElementById('historyDaysSelect').value));
  loadLogs(1);
}

function updateKeyDisplay(key) {
  currentKey = key;
  document.getElementById('apiKeyDisplay').textContent = key;
  document.getElementById('ccSettingsSnippet').textContent =
    '{\n  "env": {\n    "ANTHROPIC_AUTH_TOKEN": "' + key + '"\n  }\n}';
}

function logout() {
  sessionToken = ''; currentKey = '';
  document.getElementById('dashScreen').classList.add('hidden');
  document.getElementById('loginScreen').classList.remove('hidden');
  document.getElementById('loginPassword').value = '';
  document.getElementById('loginError').classList.add('hidden');
  if (usageChart) { usageChart.destroy(); usageChart = null; }
}

// ── API Key 复制 ──────────────────────────────────────────────────────────────

function copyKey() {
  const text = document.getElementById('apiKeyDisplay').textContent.trim();
  if (!text) return;
  const btn = document.getElementById('copyBtn');
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => flashCopied(btn)).catch(() => fallbackCopy(text, btn));
  } else {
    fallbackCopy(text, btn);
  }
}

function fallbackCopy(text, btn) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try { document.execCommand('copy'); flashCopied(btn); } catch(e) { btn.textContent = '复制失败'; }
  document.body.removeChild(ta);
}

function flashCopied(btn) {
  const orig = btn.textContent;
  btn.textContent = '已复制 ✓';
  btn.classList.add('bg-green-50', 'text-green-600', 'border-green-200');
  setTimeout(() => {
    btn.textContent = orig;
    btn.classList.remove('bg-green-50', 'text-green-600', 'border-green-200');
  }, 2000);
}

// ── 配额状态 ──────────────────────────────────────────────────────────────────

async function loadQuota() {
  try {
    const r = await fetch('/keygen/api/quota', {
      headers: {'Authorization': 'Bearer ' + sessionToken}
    });
    if (!r.ok) return;
    const d = await r.json();

    document.getElementById('dailyUsed').textContent = d.daily_used.toLocaleString();
    document.getElementById('dailyLimit').textContent = d.daily_limit === 0 ? '不限' : d.daily_limit.toLocaleString();
    document.getElementById('dailyRemain').textContent = d.daily_remain === -1 ? '不限' : d.daily_remain.toLocaleString();

    const dailyProg = document.getElementById('dailyProgress');
    if (d.daily_limit > 0) {
      const pct = Math.min(100, (d.daily_used / d.daily_limit) * 100);
      dailyProg.style.width = pct + '%';
      dailyProg.className = 'h-2 rounded-full transition-all ' + (pct > 90 ? 'bg-red-500' : 'bg-indigo-600');
    }

    document.getElementById('monthlyUsed').textContent = d.monthly_used.toLocaleString();
    document.getElementById('monthlyLimit').textContent = d.monthly_limit === 0 ? '不限' : d.monthly_limit.toLocaleString();
    document.getElementById('monthlyRemain').textContent = d.monthly_remain === -1 ? '不限' : d.monthly_remain.toLocaleString();

    const monthlyProg = document.getElementById('monthlyProgress');
    if (d.monthly_limit > 0) {
      const pct = Math.min(100, (d.monthly_used / d.monthly_limit) * 100);
      monthlyProg.style.width = pct + '%';
      monthlyProg.className = 'h-2 rounded-full transition-all ' + (pct > 90 ? 'bg-red-500' : 'bg-indigo-600');
    }
  } catch (e) {
    console.error('loadQuota failed:', e);
  }
}

// ── 用量历史图表 ───────────────────────────────────────────────────────────────

async function loadHistory(days) {
  try {
    const r = await fetch('/keygen/api/history?days=' + days, {
      headers: {'Authorization': 'Bearer ' + sessionToken}
    });
    if (!r.ok) return;
    const d = await r.json();
    updateChart(d.history || []);
  } catch (e) {
    console.error('loadHistory failed:', e);
  }
}

function updateChart(data) {
  const ctx = document.getElementById('usageHistoryChart');
  if (usageChart) usageChart.destroy();
  usageChart = new Chart(ctx, {
    type: 'bar',
    data: {
      labels: data.map(d => d.date ? d.date.substring(5) : ''),
      datasets: [{
        label: '输入 Token',
        data: data.map(d => d.input_tokens || 0),
        backgroundColor: 'rgba(99, 102, 241, 0.5)',
        borderColor: 'rgb(99, 102, 241)',
        borderWidth: 1
      }, {
        label: '输出 Token',
        data: data.map(d => d.output_tokens || 0),
        backgroundColor: 'rgba(34, 197, 94, 0.5)',
        borderColor: 'rgb(34, 197, 94)',
        borderWidth: 1
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        x: {stacked: true},
        y: {stacked: true, beginAtZero: true}
      },
      plugins: {legend: {display: true, position: 'top'}}
    }
  });
}

// ── 请求日志 ──────────────────────────────────────────────────────────────────

async function loadLogs(page) {
  if (page === undefined) page = 1;
  currentLogsPage = page;

  const days = parseInt(document.getElementById('logsDaysSelect').value) || 7;
  const pageSize = parseInt(document.getElementById('logsPageSizeSelect').value) || 10;
  const tbody = document.getElementById('logsBody');
  tbody.innerHTML = '<tr><td colspan="7" class="px-4 py-8 text-center text-gray-400">加载中...</td></tr>';

  try {
    const url = '/keygen/api/logs?days=' + days + '&page=' + page + '&page_size=' + pageSize;
    const r = await fetch(url, {headers: {'Authorization': 'Bearer ' + sessionToken}});
    if (!r.ok) {
      tbody.innerHTML = '<tr><td colspan="7" class="px-4 py-8 text-center text-red-400">加载失败</td></tr>';
      return;
    }
    const data = await r.json();
    totalLogsPages = data.total_pages || 1;
    renderLogsTable(data.logs);
    renderLogsPagination(data.page, data.total_pages, data.total, data.page_size);
  } catch (e) {
    console.error('loadLogs failed:', e);
    tbody.innerHTML = '<tr><td colspan="7" class="px-4 py-8 text-center text-red-400">请求失败</td></tr>';
  }
}

function renderLogsTable(logs) {
  const tbody = document.getElementById('logsBody');
  if (!logs || logs.length === 0) {
    tbody.innerHTML = '<tr><td colspan="7" class="px-4 py-8 text-center text-gray-400">暂无请求记录</td></tr>';
    return;
  }
  tbody.innerHTML = logs.map(l => ` + "`" + `
    <tr class="hover:bg-gray-50">
      <td class="px-4 py-3 text-gray-400 text-xs whitespace-nowrap">${l.created_at}</td>
      <td class="px-4 py-3 text-gray-500 text-xs">${l.model || '—'}</td>
      <td class="px-4 py-3 text-right text-xs">${l.input_tokens.toLocaleString()}</td>
      <td class="px-4 py-3 text-right text-xs">${l.output_tokens.toLocaleString()}</td>
      <td class="px-4 py-3 text-right text-xs text-amber-600">${l.cost_usd > 0 ? l.cost_usd.toFixed(4) : '<span class="text-gray-300">—</span>'}</td>
      <td class="px-4 py-3 text-xs">${l.is_streaming ? '<span class="text-blue-500">流式</span>' : '<span class="text-gray-400">同步</span>'}</td>
      <td class="px-4 py-3 text-xs">${l.status_code === 200
        ? '<span class="text-green-600 font-medium">✓ 200</span>'
        : '<span class="text-red-500 font-medium">✗ ' + l.status_code + '</span>'}</td>
    </tr>` + "`" + `).join('');
}

function renderLogsPagination(page, totalPages, total, pageSize) {
  const container = document.getElementById('logsPagination');
  container.classList.remove('hidden');

  document.getElementById('logsPageInfo').textContent =
    '第 ' + page + ' / ' + totalPages + ' 页，共 ' + total + ' 条';

  document.getElementById('logsPrevBtn').disabled = page <= 1;
  document.getElementById('logsNextBtn').disabled = page >= totalPages;

  const btns = document.getElementById('logsPageButtons');
  const start = Math.max(1, page - 2);
  const end = Math.min(totalPages, start + 4);
  let html = '';
  for (let i = start; i <= end; i++) {
    html += '<button onclick="goLogsPage(' + i + ')" class="px-2 py-1 rounded border ' +
      (i === page ? 'bg-indigo-600 text-white border-indigo-600' : 'border-gray-200 hover:bg-gray-50') +
      '">' + i + '</button>';
  }
  btns.innerHTML = html;
}

function goLogsPage(page) {
  if (page < 1 || page > totalLogsPages) return;
  loadLogs(page);
}

// ── 修改密码 ──────────────────────────────────────────────────────────────────

async function changePassword() {
  const oldPassword = document.getElementById('oldPassword').value;
  const newPassword = document.getElementById('newPassword').value;
  const confirmPassword = document.getElementById('confirmPassword').value;
  const errEl = document.getElementById('changePwdError');
  const okEl = document.getElementById('changePwdSuccess');
  errEl.classList.add('hidden');
  okEl.classList.add('hidden');

  if (!oldPassword || !newPassword || !confirmPassword) {
    errEl.textContent = '请填写所有密码字段';
    errEl.classList.remove('hidden');
    return;
  }
  if (newPassword !== confirmPassword) {
    errEl.textContent = '两次输入的新密码不一致';
    errEl.classList.remove('hidden');
    return;
  }
  if (newPassword === oldPassword) {
    errEl.textContent = '新密码不能与旧密码相同';
    errEl.classList.remove('hidden');
    return;
  }
  if (newPassword.length < 8) {
    errEl.textContent = '新密码至少 8 个字符';
    errEl.classList.remove('hidden');
    return;
  }

  try {
    const r = await fetch('/keygen/api/change-password', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + sessionToken},
      body: JSON.stringify({old_password: oldPassword, new_password: newPassword})
    });
    const data = await r.json();
    if (!r.ok) {
      errEl.textContent = data.message || '修改失败';
      errEl.classList.remove('hidden');
      return;
    }

    updateKeyDisplay(data.key);
    document.getElementById('oldPassword').value = '';
    document.getElementById('newPassword').value = '';
    document.getElementById('confirmPassword').value = '';

    okEl.textContent = '✓ 密码已修改，新 Key 已更新，请复制并更新您的配置';
    okEl.classList.remove('hidden');
  } catch (e) {
    errEl.textContent = '网络错误，请稍后重试';
    errEl.classList.remove('hidden');
  }
}

// ── 键盘快捷键 ─────────────────────────────────────────────────────────────────

document.getElementById('loginPassword').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') login();
});
document.getElementById('loginUsername').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') document.getElementById('loginPassword').focus();
});
</script>
</body>
</html>`
