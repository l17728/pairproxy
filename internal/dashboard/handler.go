package dashboard

import (
	"embed"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/api"
	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
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
	adminPasswordHash string
	tokenTTL          time.Duration
}

// NewHandler 创建 Dashboard Handler
func NewHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	usageRepo *db.UsageRepo,
	adminPasswordHash string,
	tokenTTL time.Duration,
) *Handler {
	return &Handler{
		logger:            logger.Named("dashboard"),
		jwtMgr:            jwtMgr,
		userRepo:          userRepo,
		groupRepo:         groupRepo,
		usageRepo:         usageRepo,
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
	mux.Handle("GET /dashboard/groups", h.requireSession(http.HandlerFunc(h.handleGroupsPage)))
	mux.Handle("POST /dashboard/groups", h.requireSession(http.HandlerFunc(h.handleCreateGroup)))
	mux.Handle("POST /dashboard/groups/{id}/quota", h.requireSession(http.HandlerFunc(h.handleSetQuota)))
	mux.Handle("GET /dashboard/logs", h.requireSession(http.HandlerFunc(h.handleLogsPage)))
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
	http.Redirect(w, r, "/dashboard/users?flash=密码已重置", http.StatusFound)
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
	if err := h.groupRepo.SetQuota(id, daily, monthly, rpm); err != nil {
		http.Redirect(w, r, "/dashboard/groups?error=更新失败", http.StatusFound)
		return
	}
	h.logger.Info("dashboard: group quota updated", zap.String("group_id", id))
	http.Redirect(w, r, "/dashboard/groups?flash=配额已更新", http.StatusFound)
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
