package dashboard

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
)

//go:embed templates
var templateFS embed.FS

// Handler Dashboard HTTP 处理器（服务端渲染）
type Handler struct {
	logger            *zap.Logger
	jwtMgr            *auth.Manager
	userRepo          *db.UserRepo
	groupRepo         *db.GroupRepo
	usageRepo         *db.UsageRepo
	auditRepo         *db.AuditRepo
	tokenRepo         *db.RefreshTokenRepo           // 可选，token 吊销
	adminPasswordHash string
	tokenTTL          time.Duration
	llmBindingRepo    *db.LLMBindingRepo             // 可选，LLM 绑定管理
	llmHealthFn       func() []proxy.LLMTargetStatus // 可选，查询 LLM 健康状态
	drainFn           func() error                   // 可选，进入排水模式
	undrainFn         func() error                   // 可选，退出排水模式
	drainStatusFn     func() proxy.DrainStatus       // 可选，查询排水状态
}

// SetTokenRepo 设置 RefreshTokenRepo（用于 token 吊销操作）
func (h *Handler) SetTokenRepo(repo *db.RefreshTokenRepo) { h.tokenRepo = repo }

// NewHandler 创建 Dashboard Handler
func NewHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	usageRepo *db.UsageRepo,
	auditRepo *db.AuditRepo,
	adminPasswordHash string,
	tokenTTL time.Duration,
) *Handler {
	return &Handler{
		logger:            logger.Named("dashboard"),
		jwtMgr:            jwtMgr,
		userRepo:          userRepo,
		groupRepo:         groupRepo,
		usageRepo:         usageRepo,
		auditRepo:         auditRepo,
		adminPasswordHash: adminPasswordHash,
		tokenTTL:          tokenTTL,
	}
}

// RegisterRoutes 注册 dashboard 路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// 无需认证
	mux.HandleFunc("GET /dashboard/login", h.handleLoginPage)
	mux.HandleFunc("POST /dashboard/login", h.handleLoginSubmit)
	mux.HandleFunc("GET /dashboard/logout", h.handleLogout)

	// 需要 session
	mux.Handle("GET /dashboard", h.requireSession(http.HandlerFunc(h.handleOverview)))
	mux.Handle("GET /dashboard/overview", h.requireSession(http.HandlerFunc(h.handleOverview)))
	mux.Handle("GET /dashboard/users", h.requireSession(http.HandlerFunc(h.handleUsersPage)))
	mux.Handle("POST /dashboard/users", h.requireSession(http.HandlerFunc(h.handleCreateUser)))
	mux.Handle("POST /dashboard/users/{id}/active", h.requireSession(http.HandlerFunc(h.handleToggleActive)))
	mux.Handle("POST /dashboard/users/{id}/password", h.requireSession(http.HandlerFunc(h.handleResetPassword)))
	mux.Handle("POST /dashboard/users/{id}/group", h.requireSession(http.HandlerFunc(h.handleSetUserGroup)))
	mux.Handle("POST /dashboard/users/{id}/revoke-tokens", h.requireSession(http.HandlerFunc(h.handleRevokeUserTokens)))
	mux.Handle("GET /dashboard/groups", h.requireSession(http.HandlerFunc(h.handleGroupsPage)))
	mux.Handle("POST /dashboard/groups", h.requireSession(http.HandlerFunc(h.handleCreateGroup)))
	mux.Handle("POST /dashboard/groups/{id}/quota", h.requireSession(http.HandlerFunc(h.handleSetQuota)))
	mux.Handle("POST /dashboard/groups/{id}/delete", h.requireSession(http.HandlerFunc(h.handleDeleteGroup)))
	mux.Handle("GET /dashboard/logs", h.requireSession(http.HandlerFunc(h.handleLogsPage)))
	mux.Handle("GET /dashboard/audit", h.requireSession(http.HandlerFunc(h.handleAuditPage)))
	mux.Handle("GET /dashboard/my-usage", h.requireSession(http.HandlerFunc(h.handleMyUsagePage)))

	// LLM 管理（可选，需设置 llmBindingRepo）
	mux.Handle("GET /dashboard/llm", h.requireSession(http.HandlerFunc(h.handleLLMPage)))
	mux.Handle("POST /dashboard/llm/bindings", h.requireSession(http.HandlerFunc(h.handleLLMCreateBinding)))
	mux.Handle("POST /dashboard/llm/bindings/{id}/delete", h.requireSession(http.HandlerFunc(h.handleLLMDeleteBinding)))
	mux.Handle("POST /dashboard/llm/distribute", h.requireSession(http.HandlerFunc(h.handleLLMDistribute)))

	// 排水控制（可选，需设置 drainFn）
	mux.Handle("POST /dashboard/drain/enter", h.requireSession(http.HandlerFunc(h.handleDrainEnter)))
	mux.Handle("POST /dashboard/drain/exit", h.requireSession(http.HandlerFunc(h.handleDrainExit)))

	// Trends API（F-10 WebUI 增强）
	mux.Handle("GET /api/dashboard/trends", h.requireSession(http.HandlerFunc(h.handleTrendsAPI)))
}

// SetLLMDeps 设置 LLM 绑定相关依赖（可选；不设置则 LLM 页面显示空状态）。
func (h *Handler) SetLLMDeps(repo *db.LLMBindingRepo, healthFn func() []proxy.LLMTargetStatus) {
	h.llmBindingRepo = repo
	h.llmHealthFn = healthFn
}

// SetDrainFunctions 设置排水控制函数（可选；不设置则 drain 按钮不显示）。
func (h *Handler) SetDrainFunctions(
	drainFn func() error,
	undrainFn func() error,
	drainStatusFn func() proxy.DrainStatus,
) {
	h.drainFn = drainFn
	h.undrainFn = undrainFn
	h.drainStatusFn = drainStatusFn
}

// ---------------------------------------------------------------------------
// 模板渲染
// ---------------------------------------------------------------------------

// templateFuncs 注册模板辅助函数
var templateFuncs = template.FuncMap{
	"deref": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
}

// renderPage 渲染 layout + 指定 page 模板（page 文件名如 "overview.html"）
func (h *Handler) renderPage(w http.ResponseWriter, page string, data interface{}) {
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(
		templateFS,
		"templates/layout.html",
		"templates/"+page,
	)
	if err != nil {
		h.logger.Error("template parse error", zap.String("page", page), zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("template execute error", zap.String("page", page), zap.Error(err))
	}
}

// renderLogin 渲染独立的登录页面（无 nav layout）
func (h *Handler) renderLogin(w http.ResponseWriter, errMsg string) {
	tmpl, err := template.New("").ParseFS(templateFS, "templates/login.html")
	if err != nil {
		h.logger.Error("login template parse error", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.ExecuteTemplate(w, "login", map[string]string{"Error": errMsg})
}

// ---------------------------------------------------------------------------
// 通用数据结构
// ---------------------------------------------------------------------------

type baseData struct {
	Flash string
	Error string
}

// ---------------------------------------------------------------------------
// 认证 middleware
// ---------------------------------------------------------------------------

func (h *Handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(api.AdminCookieName)
		if err != nil {
			http.Redirect(w, r, "/dashboard/login", http.StatusFound)
			return
		}
		claims, err := h.jwtMgr.Parse(c.Value)
		if err != nil || claims.Role != "admin" {
			http.Redirect(w, r, "/dashboard/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// 登录 / 登出
// ---------------------------------------------------------------------------

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	h.renderLogin(w, "")
}

func (h *Handler) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, "表单解析失败")
		return
	}
	password := r.FormValue("password")
	if password == "" || h.adminPasswordHash == "" ||
		!auth.VerifyPassword(h.logger, h.adminPasswordHash, password) {
		h.logger.Warn("dashboard login failed")
		h.renderLogin(w, "密码错误")
		return
	}

	token, err := h.jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, h.tokenTTL)
	if err != nil {
		h.renderLogin(w, "服务器内部错误")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     api.AdminCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.tokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	h.logger.Info("admin dashboard login successful")
	http.Redirect(w, r, "/dashboard/overview", http.StatusFound)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     api.AdminCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/dashboard/login", http.StatusFound)
}

// ---------------------------------------------------------------------------
// 概览页
// ---------------------------------------------------------------------------

type overviewPageData struct {
	baseData
	Stats          db.GlobalStats
	SuccessRatePct int
	ActiveUsers    int
	CostToday      float64
	RecentLogs     []db.UsageLog
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	from := now.Truncate(24 * time.Hour)

	stats, err := h.usageRepo.GlobalSumTokens(from, now)
	if err != nil {
		h.logger.Error("overview: GlobalSumTokens failed", zap.Error(err))
	}

	costToday, err := h.usageRepo.SumCostUSD(from, now)
	if err != nil {
		h.logger.Error("overview: SumCostUSD failed", zap.Error(err))
	}

	// 活跃用户 = 今日有请求的用户数（去重）
	userRows, _ := h.usageRepo.UserStats(from, now, 200)
	activeUsers := len(userRows)

	recentLogs, _ := h.usageRepo.Query(db.UsageFilter{Limit: 10})

	var pct int
	if stats.RequestCount > 0 {
		pct = int(float64(stats.RequestCount-stats.ErrorCount) / float64(stats.RequestCount) * 100)
	}

	h.renderPage(w, "overview.html", overviewPageData{
		baseData: baseData{
			Flash: r.URL.Query().Get("flash"),
			Error: r.URL.Query().Get("error"),
		},
		Stats:          stats,
		SuccessRatePct: pct,
		ActiveUsers:    activeUsers,
		CostToday:      costToday,
		RecentLogs:     recentLogs,
	})
}

// ---------------------------------------------------------------------------
// 用户管理页
// ---------------------------------------------------------------------------

type usersPageData struct {
	baseData
	Users  []db.User
	Groups []db.Group
}

func (h *Handler) handleUsersPage(w http.ResponseWriter, r *http.Request) {
	users, _ := h.userRepo.ListByGroup("")
	groups, _ := h.groupRepo.List()
	h.renderPage(w, "users.html", usersPageData{
		baseData: baseData{
			Flash: r.URL.Query().Get("flash"),
			Error: r.URL.Query().Get("error"),
		},
		Users:  users,
		Groups: groups,
	})
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=表单解析失败", http.StatusFound)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	groupIDStr := r.FormValue("group_id")

	if username == "" || password == "" {
		http.Redirect(w, r, "/dashboard/users?error=用户名和密码不能为空", http.StatusFound)
		return
	}

	hash, err := auth.HashPassword(h.logger, password)
	if err != nil {
		http.Redirect(w, r, "/dashboard/users?error=密码无效", http.StatusFound)
		return
	}

	var groupID *string
	if groupIDStr != "" {
		groupID = &groupIDStr
	}

	user := &db.User{
		Username:     username,
		PasswordHash: hash,
		GroupID:      groupID,
		IsActive:     true,
		CreatedAt:    time.Now(),
	}
	if err := h.userRepo.Create(user); err != nil {
		h.logger.Error("dashboard: create user failed", zap.String("username", username), zap.Error(err))
		http.Redirect(w, r, "/dashboard/users?error=创建失败，用户名可能已存在", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: user created", zap.String("username", username))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"group_id": groupID, "is_active": true}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "user.create", username, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	http.Redirect(w, r, "/dashboard/users?flash=用户已创建", http.StatusFound)
}

func (h *Handler) handleToggleActive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=表单解析失败", http.StatusFound)
		return
	}
	active := r.FormValue("active") == "true"
	if err := h.userRepo.SetActive(id, active); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=操作失败", http.StatusFound)
		return
	}
	if detailBytes, jerr := json.Marshal(map[string]bool{"active": active}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "user.set_active", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	action := "已启用"
	if !active {
		action = "已禁用"
	}
	http.Redirect(w, r, "/dashboard/users?flash=用户"+action, http.StatusFound)
}

func (h *Handler) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=表单解析失败", http.StatusFound)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		http.Redirect(w, r, "/dashboard/users?error=密码不能为空", http.StatusFound)
		return
	}
	hash, err := auth.HashPassword(h.logger, password)
	if err != nil {
		http.Redirect(w, r, "/dashboard/users?error=密码无效", http.StatusFound)
		return
	}
	if err := h.userRepo.UpdatePassword(id, hash); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=重置失败", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: password reset", zap.String("user_id", id))
	if aerr := h.auditRepo.Create("admin", "user.reset_password", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/users?flash=密码已重置", http.StatusFound)
}

func (h *Handler) handleSetUserGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=表单解析失败", http.StatusFound)
		return
	}
	groupIDStr := r.FormValue("group_id")
	var groupID *string
	if groupIDStr != "" {
		groupID = &groupIDStr
	}
	if err := h.userRepo.SetGroup(id, groupID); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=更新分组失败", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: user group updated", zap.String("user_id", id), zap.Any("group_id", groupID))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"group_id": groupID}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "user.set_group", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	http.Redirect(w, r, "/dashboard/users?flash=分组已更新", http.StatusFound)
}

func (h *Handler) handleRevokeUserTokens(w http.ResponseWriter, r *http.Request) {
	if h.tokenRepo == nil {
		http.Redirect(w, r, "/dashboard/users?error=Token吊销功能未配置", http.StatusFound)
		return
	}
	id := r.PathValue("id")
	if err := h.tokenRepo.RevokeAllForUser(id); err != nil {
		http.Redirect(w, r, "/dashboard/users?error=吊销失败", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: revoked all tokens for user", zap.String("user_id", id))
	if aerr := h.auditRepo.Create("admin", "token.revoke_all", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/users?flash=Token已吊销", http.StatusFound)
}

// ---------------------------------------------------------------------------
// 分组管理页
// ---------------------------------------------------------------------------

type groupsPageData struct {
	baseData
	Groups []db.Group
}

func (h *Handler) handleGroupsPage(w http.ResponseWriter, r *http.Request) {
	groups, _ := h.groupRepo.List()
	h.renderPage(w, "groups.html", groupsPageData{
		baseData: baseData{
			Flash: r.URL.Query().Get("flash"),
			Error: r.URL.Query().Get("error"),
		},
		Groups: groups,
	})
}

func (h *Handler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/groups?error=表单解析失败", http.StatusFound)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Redirect(w, r, "/dashboard/groups?error=分组名不能为空", http.StatusFound)
		return
	}

	g := &db.Group{
		Name:              name,
		DailyTokenLimit:   parseOptionalInt64(r.FormValue("daily_limit")),
		MonthlyTokenLimit: parseOptionalInt64(r.FormValue("monthly_limit")),
		RequestsPerMinute: parseOptionalInt(r.FormValue("rpm")),
		CreatedAt:         time.Now(),
	}
	if err := h.groupRepo.Create(g); err != nil {
		h.logger.Error("dashboard: create group failed", zap.String("name", name), zap.Error(err))
		http.Redirect(w, r, "/dashboard/groups?error=创建失败，名称可能已存在", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: group created", zap.String("name", name))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"daily_limit": g.DailyTokenLimit, "monthly_limit": g.MonthlyTokenLimit, "rpm": g.RequestsPerMinute}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "group.create", name, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	http.Redirect(w, r, "/dashboard/groups?flash=分组已创建", http.StatusFound)
}

func (h *Handler) handleSetQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/groups?error=表单解析失败", http.StatusFound)
		return
	}
	daily := parseOptionalInt64(r.FormValue("daily_limit"))
	monthly := parseOptionalInt64(r.FormValue("monthly_limit"))
	rpm := parseOptionalInt(r.FormValue("rpm"))
	maxTokens := parseOptionalInt64(r.FormValue("max_tokens"))
	concurrent := parseOptionalInt(r.FormValue("concurrent"))
	if err := h.groupRepo.SetQuota(id, daily, monthly, rpm, maxTokens, concurrent); err != nil {
		http.Redirect(w, r, "/dashboard/groups?error=更新失败", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: group quota updated", zap.String("group_id", id))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{
		"daily_limit":   daily,
		"monthly_limit": monthly,
		"rpm":           rpm,
		"max_tokens":    maxTokens,
		"concurrent":    concurrent,
	}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "group.set_quota", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	http.Redirect(w, r, "/dashboard/groups?flash=配额已更新", http.StatusFound)
}

func (h *Handler) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/groups?error=表单解析失败", http.StatusFound)
		return
	}
	force := r.FormValue("force") == "true"
	if err := h.groupRepo.Delete(id, force); err != nil {
		h.logger.Error("dashboard: delete group failed", zap.String("group_id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/groups?error=删除失败："+err.Error(), http.StatusFound)
		return
	}
	h.logger.Info("dashboard: group deleted", zap.String("group_id", id), zap.Bool("force", force))
	if detailBytes, jerr := json.Marshal(map[string]interface{}{"force": force}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "group.delete", id, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	http.Redirect(w, r, "/dashboard/groups?flash=分组已删除", http.StatusFound)
}

// ---------------------------------------------------------------------------
// 日志页
// ---------------------------------------------------------------------------

type logsPageData struct {
	baseData
	Logs         []db.UsageLog
	FilterUserID string
	Limit        int
}

func (h *Handler) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	limit := 100

	logs, _ := h.usageRepo.Query(db.UsageFilter{UserID: userID, Limit: limit})
	h.renderPage(w, "logs.html", logsPageData{
		baseData: baseData{
			Flash: r.URL.Query().Get("flash"),
			Error: r.URL.Query().Get("error"),
		},
		Logs:         logs,
		FilterUserID: userID,
		Limit:        limit,
	})
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

// parseOptionalInt64 将表单字符串解析为 *int64，空字符串或解析失败返回 nil
func parseOptionalInt64(s string) *int64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

// parseOptionalInt 将表单字符串解析为 *int，空字符串或解析失败返回 nil
func parseOptionalInt(s string) *int {
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

// ---------------------------------------------------------------------------
// 审计日志页（P2-3）
// ---------------------------------------------------------------------------

type auditPageData struct {
	baseData
	Logs  []db.AuditLog
	Limit int
}

func (h *Handler) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	limit := 200
	logs, _ := h.auditRepo.ListRecent(limit)
	h.renderPage(w, "audit.html", auditPageData{
		baseData: baseData{
			Flash: r.URL.Query().Get("flash"),
			Error: r.URL.Query().Get("error"),
		},
		Logs:  logs,
		Limit: limit,
	})
}

// ---------------------------------------------------------------------------
// Trends API（F-10 WebUI 增强）
// ---------------------------------------------------------------------------

type trendsResponse struct {
	DailyTokens []db.DailyTokenRow `json:"daily_tokens"`
	DailyCost   []db.DailyCostRow  `json:"daily_cost"`
	TopUsers    []db.UserStatRow   `json:"top_users"`
}

func (h *Handler) handleTrendsAPI(w http.ResponseWriter, r *http.Request) {
	// 解析 days 参数（默认 7 天）
	daysStr := r.URL.Query().Get("days")
	days := 7
	if daysStr != "" {
		if parsed, err := strconv.Atoi(daysStr); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	now := time.Now()
	from := now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
	to := now

	// 查询按天聚合的 token 用量
	dailyTokens, err := h.usageRepo.DailyTokens(from, to, "")
	if err != nil {
		h.logger.Error("failed to get daily tokens", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 查询按天聚合的费用
	dailyCost, err := h.usageRepo.DailyCost(from, to, "")
	if err != nil {
		h.logger.Error("failed to get daily cost", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 查询 Top 5 用户
	topUsers, err := h.usageRepo.UserStats(from, to, 5)
	if err != nil {
		h.logger.Error("failed to get top users", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trendsResponse{
		DailyTokens: dailyTokens,
		DailyCost:   dailyCost,
		TopUsers:    topUsers,
	})
}

// ---------------------------------------------------------------------------
// 用户自助查询页面（F-10 WebUI 增强）
// ---------------------------------------------------------------------------

func (h *Handler) handleMyUsagePage(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "my-usage.html", baseData{
		Flash: r.URL.Query().Get("flash"),
		Error: r.URL.Query().Get("error"),
	})
}
