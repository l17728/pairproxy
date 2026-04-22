package dashboard

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/eventlog"
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
	llmTargetRepo     *db.LLMTargetRepo              // 可选，LLM 目标管理
	apiKeyRepo        *db.APIKeyRepo                 // 可选，API Key 管理
	groupTargetSetRepo *db.GroupTargetSetRepo        // 可选，Group-Target Set 管理
	llmHealthFn       func() []proxy.LLMTargetStatus // 可选，查询 LLM 健康状态
	drainFn           func() error                   // 可选，进入排水模式
	undrainFn         func() error                   // 可选，退出排水模式
	drainStatusFn     func() proxy.DrainStatus       // 可选，查询排水状态
	eventLog          *eventlog.Log                  // 可选，内存告警事件缓冲区
	isWorkerNode      bool                           // true = Worker 节点，只读模式
	llmSyncFn         func()                         // 可选，目标变更后同步 balancer/HC

	// 用户统计缓存（5 分钟 TTL，PRD NFR-缓存要求）
	userStatsCacheMu  sync.Mutex
	userStatsCacheVal []userStatsResponse
	userStatsCacheExp time.Time
}

// SetTokenRepo 设置 RefreshTokenRepo（用于 token 吊销操作）
func (h *Handler) SetTokenRepo(repo *db.RefreshTokenRepo) { h.tokenRepo = repo }

// SetLLMTargetRepo 设置 LLMTargetRepo（用于 LLM 目标管理）
func (h *Handler) SetLLMTargetRepo(repo *db.LLMTargetRepo) { h.llmTargetRepo = repo }

// SetAPIKeyRepo 设置 APIKeyRepo（用于 API Key 管理）
func (h *Handler) SetAPIKeyRepo(repo *db.APIKeyRepo) { h.apiKeyRepo = repo }

// SetGroupTargetSetRepo 设置 GroupTargetSetRepo（用于 Group-Target Set 管理）
func (h *Handler) SetGroupTargetSetRepo(repo *db.GroupTargetSetRepo) { h.groupTargetSetRepo = repo }

// SetEventLog 设置内存事件日志（用于 /dashboard/alerts 页面）
func (h *Handler) SetEventLog(log *eventlog.Log) { h.eventLog = log }

// SetWorkerMode 设置 Worker 节点只读模式（Worker 节点调用此方法后，所有写操作返回 403）
func (h *Handler) SetWorkerMode(isWorker bool) { h.isWorkerNode = isWorker }

// SetLLMSyncFn 设置目标变更后的同步回调（可选）。
// 每次通过 Dashboard 增删改 LLM target 成功后会调用此函数，
// 使 llmBalancer 和 llmHC 立即感知变更，无需重启进程。
func (h *Handler) SetLLMSyncFn(fn func()) { h.llmSyncFn = fn }

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

	// 写操作中间件链（requireSession + requireWritableNode）
	rw := func(hf http.Handler) http.Handler {
		return h.requireSession(h.requireWritableNode(hf))
	}

	// 需要 session（读操作）
	mux.Handle("GET /dashboard", h.requireSession(http.HandlerFunc(h.handleOverview)))
	mux.Handle("GET /dashboard/overview", h.requireSession(http.HandlerFunc(h.handleOverview)))
	mux.Handle("GET /dashboard/users", h.requireSession(http.HandlerFunc(h.handleUsersPage)))
	mux.Handle("GET /dashboard/groups", h.requireSession(http.HandlerFunc(h.handleGroupsPage)))
	mux.Handle("GET /dashboard/logs", h.requireSession(http.HandlerFunc(h.handleLogsPage)))
	mux.Handle("GET /dashboard/audit", h.requireSession(http.HandlerFunc(h.handleAuditPage)))
	mux.Handle("GET /dashboard/my-usage", h.requireSession(http.HandlerFunc(h.handleMyUsagePage)))
	mux.Handle("GET /dashboard/llm", h.requireSession(http.HandlerFunc(h.handleLLMPage)))
	mux.Handle("GET /dashboard/import", h.requireSession(http.HandlerFunc(h.handleImportPage)))
	mux.Handle("GET /dashboard/alerts", h.requireSession(http.HandlerFunc(h.handleAlertsPage)))
	mux.Handle("GET /api/dashboard/events", h.requireSession(http.HandlerFunc(h.handleEventsAPI)))
	mux.Handle("GET /api/dashboard/trends", h.requireSession(http.HandlerFunc(h.handleTrendsAPI)))
	mux.Handle("GET /dashboard/api/user-stats", h.requireSession(http.HandlerFunc(h.handleUserStats)))
	mux.Handle("GET /api/dashboard/active-users", h.requireSession(http.HandlerFunc(h.handleDashboardActiveUsers)))
	mux.Handle("GET /api/dashboard/user-quota", h.requireSession(http.HandlerFunc(h.handleDashboardUserQuota)))
	mux.Handle("GET /api/dashboard/user-history", h.requireSession(http.HandlerFunc(h.handleDashboardUserHistory)))
	mux.Handle("GET /api/dashboard/user-logs", h.requireSession(http.HandlerFunc(h.handleDashboardUserLogs)))

	// 需要 session + 可写节点（写操作）
	mux.Handle("POST /dashboard/users", rw(http.HandlerFunc(h.handleCreateUser)))
	mux.Handle("POST /dashboard/users/{id}/active", rw(http.HandlerFunc(h.handleToggleActive)))
	mux.Handle("POST /dashboard/users/{id}/password", rw(http.HandlerFunc(h.handleResetPassword)))
	mux.Handle("POST /dashboard/users/{id}/group", rw(http.HandlerFunc(h.handleSetUserGroup)))
	mux.Handle("POST /dashboard/users/{id}/revoke-tokens", rw(http.HandlerFunc(h.handleRevokeUserTokens)))
	mux.Handle("POST /dashboard/groups", rw(http.HandlerFunc(h.handleCreateGroup)))
	mux.Handle("POST /dashboard/groups/{id}/quota", rw(http.HandlerFunc(h.handleSetQuota)))
	mux.Handle("POST /dashboard/groups/{id}/delete", rw(http.HandlerFunc(h.handleDeleteGroup)))
	mux.Handle("POST /dashboard/logs/purge-all", rw(http.HandlerFunc(h.handleLogsPurgeAll)))
	mux.Handle("POST /dashboard/llm/bindings", rw(http.HandlerFunc(h.handleLLMCreateBinding)))
	mux.Handle("POST /dashboard/llm/bindings/{id}/delete", rw(http.HandlerFunc(h.handleLLMDeleteBinding)))
	mux.Handle("POST /dashboard/llm/distribute", rw(http.HandlerFunc(h.handleLLMDistribute)))
	mux.Handle("POST /dashboard/llm/targets", rw(http.HandlerFunc(h.handleLLMCreateTarget)))
	mux.Handle("POST /dashboard/llm/targets/{id}/update", rw(http.HandlerFunc(h.handleLLMUpdateTarget)))
	mux.Handle("POST /dashboard/llm/targets/{id}/delete", rw(http.HandlerFunc(h.handleLLMDeleteTarget)))
	mux.Handle("POST /dashboard/llm/targetsets", rw(http.HandlerFunc(h.handleTargetSetCreate)))
	mux.Handle("POST /dashboard/llm/targetsets/{id}/update", rw(http.HandlerFunc(h.handleTargetSetUpdate)))
	mux.Handle("POST /dashboard/llm/targetsets/{id}/delete", rw(http.HandlerFunc(h.handleTargetSetDelete)))
	mux.Handle("POST /dashboard/llm/targetsets/{id}/members", rw(http.HandlerFunc(h.handleTargetSetAddMember)))
	mux.Handle("POST /dashboard/llm/targetsets/{id}/members/update", rw(http.HandlerFunc(h.handleTargetSetUpdateMember)))
	mux.Handle("POST /dashboard/llm/targetsets/{id}/members/delete", rw(http.HandlerFunc(h.handleTargetSetRemoveMember)))
	mux.Handle("POST /dashboard/alerts/resolve", rw(http.HandlerFunc(h.handleAlertResolve)))
	mux.Handle("POST /dashboard/alerts/resolve-batch", rw(http.HandlerFunc(h.handleAlertResolveBatch)))
	mux.Handle("POST /dashboard/drain/enter", rw(http.HandlerFunc(h.handleDrainEnter)))
	mux.Handle("POST /dashboard/drain/exit", rw(http.HandlerFunc(h.handleDrainExit)))
	mux.Handle("POST /dashboard/import", rw(http.HandlerFunc(h.handleImportSubmit)))
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

// RegisterAdminLLMTargetRoutes 注册 LLM target 管理 API 路由（委托给 AdminLLMTargetHandler）
func (h *Handler) RegisterAdminLLMTargetRoutes(mux *http.ServeMux, llmTargetHandler interface {
	RegisterRoutes(mux *http.ServeMux, requireAdmin func(http.Handler) http.Handler, requireWritableNode func(http.Handler) http.Handler)
}) {
	llmTargetHandler.RegisterRoutes(mux, h.requireSession, h.requireWritableNode)
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
	// username 从 id→username 映射中查找用户名；找不到时回退显示原始 id。
	"username": func(id string, m map[string]string) string {
		if m != nil {
			if name, ok := m[id]; ok && name != "" {
				return name
			}
		}
		return id
	},
}

// buildUserMap 查询全量用户列表并返回 id→username 映射。
// 失败时返回空 map（不阻断页面渲染，页面会回退显示 id）。
func (h *Handler) buildUserMap() map[string]string {
	users, err := h.userRepo.ListByGroup("")
	m := make(map[string]string, len(users))
	if err != nil {
		h.logger.Warn("buildUserMap: failed to list users", zap.Error(err))
		return m
	}
	for _, u := range users {
		m[u.ID] = u.Username
	}
	return m
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
	Flash        string
	Error        string
	IsWorkerNode bool
}

// newBase 创建 baseData，自动填充 IsWorkerNode 和 flash/error 查询参数。
func (h *Handler) newBase(r *http.Request) baseData {
	return baseData{
		Flash:        r.URL.Query().Get("flash"),
		Error:        r.URL.Query().Get("error"),
		IsWorkerNode: h.isWorkerNode,
	}
}

// newBaseErr 创建带错误消息的 baseData（用于服务器端验证失败）。
func (h *Handler) newBaseErr(err string) baseData {
	return baseData{Error: err, IsWorkerNode: h.isWorkerNode}
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

// requireWritableNode 中间件：在 Worker 节点上拒绝所有写操作（返回 403 JSON）。
// 需链在 requireSession 之后使用。
func (h *Handler) requireWritableNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.isWorkerNode {
			h.logger.Warn("blocked write operation on worker node (dashboard)",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"worker_read_only","message":"write operations are not allowed on worker nodes; perform admin operations on the primary node"}`))
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
	UserMap        map[string]string // id → username，用于模板显示用户名
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
		baseData:       h.newBase(r),
		Stats:          stats,
		SuccessRatePct: pct,
		ActiveUsers:    activeUsers,
		CostToday:      costToday,
		RecentLogs:     recentLogs,
		UserMap:        h.buildUserMap(),
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
		baseData: h.newBase(r),
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
	h.invalidateUserStatsCache()
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
	h.invalidateUserStatsCache()
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
	h.invalidateUserStatsCache()
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
		baseData: h.newBase(r),
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
	Logs            []db.UsageLog
	FilterUserID    string
	FilterUsername  string // 对应 FilterUserID 的用户名，用于筛选框回显
	Limit           int
	UserMap         map[string]string // id → username，用于模板显示用户名
}

func (h *Handler) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	// 支持按用户名筛选（兼容旧的 user_id 参数）
	username := r.URL.Query().Get("username")
	userID := r.URL.Query().Get("user_id")

	// 优先使用 username 参数，将其解析为 user_id 用于数据库查询
	displayUsername := username
	if username != "" && userID == "" {
		if users, err := h.userRepo.ListByUsername(username); err == nil && len(users) == 1 {
			userID = users[0].ID
		} else {
			// 用户名不存在或有歧义，返回空结果（不报错）
			userID = "__not_found__"
		}
	}

	limit := 100
	logs, _ := h.usageRepo.Query(db.UsageFilter{UserID: userID, Limit: limit})

	userMap := h.buildUserMap()
	// 若用 user_id 参数筛选，回填对应用户名用于表单显示
	if displayUsername == "" && userID != "" {
		if name, ok := userMap[userID]; ok {
			displayUsername = name
		}
	}

	h.renderPage(w, "logs.html", logsPageData{
		baseData:       h.newBase(r),
		Logs:           logs,
		FilterUserID:   userID,
		FilterUsername: displayUsername,
		Limit:          limit,
		UserMap:        userMap,
	})
}

// handleLogsPurgeAll POST /dashboard/logs/purge-all
// 清空所有使用日志。需要在表单中提交 confirm=OK 作为二次确认。
func (h *Handler) handleLogsPurgeAll(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/logs?error=invalid+form", http.StatusSeeOther)
		return
	}
	if r.FormValue("confirm") != "OK" {
		http.Redirect(w, r, "/dashboard/logs?error=请输入+OK+以确认清空", http.StatusSeeOther)
		return
	}

	n, err := h.usageRepo.DeleteBefore(time.Now().Add(time.Second))
	if err != nil {
		h.logger.Error("purge all logs failed", zap.Error(err))
		http.Redirect(w, r, "/dashboard/logs?error="+err.Error(), http.StatusSeeOther)
		return
	}

	_ = h.auditRepo.Create("admin", "logs.purge_all", "usage_logs", fmt.Sprintf("deleted %d records", n))
	h.logger.Info("all usage logs purged via dashboard", zap.Int64("deleted", n))
	http.Redirect(w, r, "/dashboard/logs?flash=日志已清空，共删除"+fmt.Sprintf("%d", n)+"条记录", http.StatusSeeOther)
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
		baseData: h.newBase(r),
		Logs:  logs,
		Limit: limit,
	})
}

// ---------------------------------------------------------------------------
// Trends API（F-10 WebUI 增强）
// ---------------------------------------------------------------------------

// topUserEntry 在 UserStatRow 基础上附加了解析后的用户名，供前端图表使用。
type topUserEntry struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	TotalInput   int64  `json:"total_input"`
	TotalOutput  int64  `json:"total_output"`
	RequestCount int64  `json:"request_count"`
}

type trendsResponse struct {
	DailyTokens []db.DailyTokenRow `json:"daily_tokens"`
	DailyCost   []db.DailyCostRow  `json:"daily_cost"`
	TopUsers    []topUserEntry     `json:"top_users"`
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

	// 查询 Top 5 用户，附加用户名
	rawTopUsers, err := h.usageRepo.UserStats(from, to, 5)
	if err != nil {
		h.logger.Error("failed to get top users", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	userMap := h.buildUserMap()
	topUsers := make([]topUserEntry, len(rawTopUsers))
	for i, u := range rawTopUsers {
		name := u.UserID
		if n, ok := userMap[u.UserID]; ok && n != "" {
			name = n
		}
		topUsers[i] = topUserEntry{
			UserID:       u.UserID,
			Username:     name,
			TotalInput:   u.TotalInput,
			TotalOutput:  u.TotalOutput,
			RequestCount: u.RequestCount,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(trendsResponse{
		DailyTokens: dailyTokens,
		DailyCost:   dailyCost,
		TopUsers:    topUsers,
	}); err != nil {
		h.logger.Error("failed to encode trends response", zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// 用户自助查询页面（F-10 WebUI 增强）
// ---------------------------------------------------------------------------

func (h *Handler) handleMyUsagePage(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "my-usage.html", h.newBase(r))
}

// ---------------------------------------------------------------------------
// 用户 Token 用量统计 API（dashboard-user-token-stats PRD）
// GET /dashboard/api/user-stats
// ---------------------------------------------------------------------------

// userStatsResponse 单个用户的用量统计响应体
type userStatsResponse struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	GroupID      string `json:"group_id"`   // 空字符串表示无分组
	GroupName    string `json:"group_name"`
	TotalInput   int64  `json:"total_input"`
	TotalOutput  int64  `json:"total_output"`
	TotalTokens  int64  `json:"total_tokens"`
	AvgDaily     int64  `json:"avg_daily"`    // 总 Tokens / 实际使用天数
	AvgMonthly   int64  `json:"avg_monthly"`  // 总 Tokens / 使用月数
	DaysActive   int    `json:"days_active"`
	MonthsActive int    `json:"months_active"`
	FirstUsedAt  string `json:"first_used_at"` // YYYY-MM-DD；无记录时为空字符串
	LastUsedAt   string `json:"last_used_at"`  // YYYY-MM-DD；无记录时为空字符串
	IsActive     bool   `json:"is_active"`
}

// userStatsPageResponse 带分页信息的用户统计响应体
type userStatsPageResponse struct {
	Total      int                `json:"total"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	TotalPages int                `json:"total_pages"`
	Users      []userStatsResponse `json:"users"`
}

const userStatsCacheTTL = 5 * time.Minute

// invalidateUserStatsCache 清除用户统计缓存，使下次请求强制重新查询 DB。
func (h *Handler) invalidateUserStatsCache() {
	h.userStatsCacheMu.Lock()
	defer h.userStatsCacheMu.Unlock()
	h.userStatsCacheVal = nil
	h.userStatsCacheExp = time.Time{}
}

// getFullUserStats 从缓存或 DB 获取全量用户统计列表。
// forceRefresh=true 时穿透缓存重新查询。
func (h *Handler) getFullUserStats(forceRefresh bool) ([]userStatsResponse, error) {
	if !forceRefresh {
		h.userStatsCacheMu.Lock()
		if h.userStatsCacheVal != nil && time.Now().Before(h.userStatsCacheExp) {
			cached := h.userStatsCacheVal
			h.userStatsCacheMu.Unlock()
			h.logger.Debug("user stats cache hit")
			return cached, nil
		}
		h.userStatsCacheMu.Unlock()
	}

	usageStats, err := h.usageRepo.GetUserAllTimeStats()
	if err != nil {
		return nil, fmt.Errorf("get usage stats: %w", err)
	}
	users, err := h.userRepo.ListByGroup("")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	statMap := make(map[string]db.UserAllTimeStat, len(usageStats))
	for _, s := range usageStats {
		statMap[s.UserID] = s
	}

	resp := make([]userStatsResponse, 0, len(users))
	for _, u := range users {
		stat := statMap[u.ID]

		var avgDaily, avgMonthly int64
		if stat.DaysActive > 0 {
			avgDaily = stat.TotalTokens / int64(stat.DaysActive)
		}
		months := stat.MonthsActive
		if months < 1 {
			months = 1
		}
		if stat.TotalTokens > 0 {
			avgMonthly = stat.TotalTokens / int64(months)
		}

		var firstUsed, lastUsed string
		if !stat.FirstUsedAt.IsZero() {
			firstUsed = stat.FirstUsedAt.Format("2006-01-02")
		}
		if !stat.LastUsedAt.IsZero() {
			lastUsed = stat.LastUsedAt.Format("2006-01-02")
		}

		groupID := ""
		groupName := ""
		if u.GroupID != nil {
			groupID = *u.GroupID
			groupName = u.Group.Name
		}

		resp = append(resp, userStatsResponse{
			UserID:       u.ID,
			Username:     u.Username,
			GroupID:      groupID,
			GroupName:    groupName,
			TotalInput:   stat.TotalInput,
			TotalOutput:  stat.TotalOutput,
			TotalTokens:  stat.TotalTokens,
			AvgDaily:     avgDaily,
			AvgMonthly:   avgMonthly,
			DaysActive:   stat.DaysActive,
			MonthsActive: stat.MonthsActive,
			FirstUsedAt:  firstUsed,
			LastUsedAt:   lastUsed,
			IsActive:     u.IsActive,
		})
	}

	h.userStatsCacheMu.Lock()
	h.userStatsCacheVal = resp
	h.userStatsCacheExp = time.Now().Add(userStatsCacheTTL)
	h.userStatsCacheMu.Unlock()

	h.logger.Debug("user stats cache refreshed", zap.Int("users", len(resp)))
	return resp, nil
}

// handleUserStats 返回分页+过滤+排序后的用户统计列表（JSON）。
//
// 查询参数：
//   - page       int    页码（默认 1）
//   - page_size  int    每页条数（默认 20，最大 200）
//   - username   string 用户名模糊过滤（不区分大小写，空表示不过滤）
//   - group_id   string 分组 ID 精确过滤（空表示不过滤）
//   - sort_by    string 排序字段（默认 total_tokens）
//   - sort_order string asc|desc（默认 desc）
//   - _          any    携带此参数时强制穿透缓存
func (h *Handler) handleUserStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	forceRefresh := q.Has("_")

	// 分页参数
	page := 1
	pageSize := 20
	if v := q.Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			page = p
		}
	}
	if v := q.Get("page_size"); v != "" {
		if ps, err := strconv.Atoi(v); err == nil && ps > 0 && ps <= 200 {
			pageSize = ps
		}
	}

	// 过滤参数
	usernameFilter := strings.ToLower(q.Get("username"))
	groupIDFilter := q.Get("group_id")

	// 排序参数
	sortBy := q.Get("sort_by")
	sortOrder := q.Get("sort_order")
	validSortFields := map[string]bool{
		"username": true, "group_name": true,
		"total_input": true, "total_output": true, "total_tokens": true,
		"avg_daily": true, "avg_monthly": true,
		"first_used_at": true, "last_used_at": true,
	}
	if !validSortFields[sortBy] {
		sortBy = "total_tokens"
	}
	if sortOrder != "asc" {
		sortOrder = "desc"
	}

	// 获取全量缓存数据
	all, err := h.getFullUserStats(forceRefresh)
	if err != nil {
		h.logger.Error("failed to get user stats", zap.Error(err))
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	// 过滤：始终构建新切片，避免对缓存底层数组的 in-place 排序
	filtered := make([]userStatsResponse, 0, len(all))
	for _, s := range all {
		if usernameFilter != "" && !strings.Contains(strings.ToLower(s.Username), usernameFilter) {
			continue
		}
		if groupIDFilter != "" && s.GroupID != groupIDFilter {
			continue
		}
		filtered = append(filtered, s)
	}

	// 排序（在过滤后的独立切片上，不影响缓存）
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		var less bool
		switch sortBy {
		case "username":
			less = strings.ToLower(a.Username) < strings.ToLower(b.Username)
		case "group_name":
			less = strings.ToLower(a.GroupName) < strings.ToLower(b.GroupName)
		case "total_input":
			less = a.TotalInput < b.TotalInput
		case "total_output":
			less = a.TotalOutput < b.TotalOutput
		case "avg_daily":
			less = a.AvgDaily < b.AvgDaily
		case "avg_monthly":
			less = a.AvgMonthly < b.AvgMonthly
		case "first_used_at":
			less = a.FirstUsedAt < b.FirstUsedAt
		case "last_used_at":
			less = a.LastUsedAt < b.LastUsedAt
		default: // total_tokens
			less = a.TotalTokens < b.TotalTokens
		}
		if sortOrder == "asc" {
			return less
		}
		return !less
	})

	// 分页
	total := len(filtered)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	var pageUsers []userStatsResponse
	if start < total {
		pageUsers = filtered[start:end]
	} else {
		pageUsers = []userStatsResponse{}
	}

	resp := userStatsPageResponse{
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
		Users:      pageUsers,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("failed to encode user stats", zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// 批量导入
// ---------------------------------------------------------------------------

// dashImportUser 代表导入文件中的一个用户条目。
type dashImportUser struct {
	Username    string
	Password    string
	LLMOverride string // "" = 使用所在组的默认 LLM 绑定
}

// dashImportSection 代表导入文件中的一个分组区块。
type dashImportSection struct {
	GroupName string // "" = 无分组
	LLMTarget string // "" = 不设置组级 LLM 绑定
	Users     []dashImportUser
}

// dashImportResult 保存一次批量导入的执行结果。
type dashImportResult struct {
	GroupsCreated int
	GroupsSkipped int
	UsersCreated  int
	UsersSkipped  int
	BindingsSet   int
	SkipDetails   []string
	ParseError    string
	DryRun        bool
	Done          bool // true = 已执行导入（区分初始表单 vs 结果展示）
}

// importPageData 是批量导入页面的模板数据。
type importPageData struct {
	baseData
	Content string           // 回填原始文本，方便修改重试
	Result  dashImportResult
}

// parseImportContent 解析批量导入文本内容，返回各分组区块及其用户列表。
// 与 CLI parseImportFile 逻辑一致，但接受字符串而非文件路径。
func parseImportContent(content string) ([]dashImportSection, error) {
	var sections []dashImportSection
	// 文件头部（无分组头之前）隐式属于无分组区块
	current := dashImportSection{}

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// 分组头：[...]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections = append(sections, current)
			inner := line[1 : len(line)-1]
			parts := strings.Fields(inner)
			groupName := ""
			llmTarget := ""
			if len(parts) > 0 && parts[0] != "-" {
				groupName = parts[0]
			}
			for _, p := range parts[1:] {
				if strings.HasPrefix(p, "llm=") {
					llmTarget = strings.TrimPrefix(p, "llm=")
				}
			}
			current = dashImportSection{GroupName: groupName, LLMTarget: llmTarget}
			continue
		}

		// 用户行：用户名 密码 [llm=URL]
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("第 %d 行格式错误：需要 '用户名 密码 [llm=URL]'，实际内容：%q", lineNo, line)
		}
		u := dashImportUser{
			Username: fields[0],
			Password: fields[1],
		}
		for _, f := range fields[2:] {
			if strings.HasPrefix(f, "llm=") {
				u.LLMOverride = strings.TrimPrefix(f, "llm=")
			}
		}
		current.Users = append(current.Users, u)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取内容失败：%w", err)
	}
	sections = append(sections, current)
	return sections, nil
}

func (h *Handler) handleImportPage(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "import.html", importPageData{
		baseData: h.newBase(r),
	})
}

func (h *Handler) handleImportSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		// 普通 form 也 OK，multipart 解析失败时回退
		h.logger.Debug("multipart parse failed, falling back to urlencoded form", zap.Error(err))
		_ = r.ParseForm()
	}

	// 优先读上传文件，无则读 textarea
	content := ""
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		data, readErr := io.ReadAll(file)
		if readErr != nil {
			h.logger.Warn("import: failed to read uploaded file", zap.Error(readErr))
		} else {
			content = string(data)
		}
	}
	if content == "" {
		content = r.FormValue("content")
	}
	if strings.TrimSpace(content) == "" {
		h.renderPage(w, "import.html", importPageData{
			baseData: h.newBaseErr("请上传文件或粘贴导入内容"),
			Content:  content,
			Result:   dashImportResult{Done: true},
		})
		return
	}

	dryRun := r.FormValue("dry_run") == "on"

	sections, err := parseImportContent(content)
	if err != nil {
		h.logger.Warn("import: failed to parse content", zap.Error(err), zap.Bool("dry_run", dryRun))
		h.renderPage(w, "import.html", importPageData{
			baseData: h.newBaseErr(""),
			Content:  content,
			Result:   dashImportResult{Done: true, DryRun: dryRun, ParseError: err.Error()},
		})
		return
	}

	result := dashImportResult{DryRun: dryRun, Done: true}

	if dryRun {
		h.logger.Info("import dry-run started (no changes will be applied)")
	} else {
		h.logger.Info("import started")
	}

	for _, sec := range sections {
		var groupID *string

		if sec.GroupName != "" {
			existing, err := h.groupRepo.GetByName(sec.GroupName)
			if err != nil {
				h.logger.Warn("import: lookup group failed", zap.String("group", sec.GroupName), zap.Error(err))
			}
			if existing != nil {
				result.GroupsSkipped++
				result.SkipDetails = append(result.SkipDetails, fmt.Sprintf("分组 %q 已存在，跳过", sec.GroupName))
				id := existing.ID
				groupID = &id
			} else {
				g := &db.Group{Name: sec.GroupName}
				if !dryRun {
					if err := h.groupRepo.Create(g); err != nil {
						h.logger.Warn("import: create group failed", zap.String("group", sec.GroupName), zap.Error(err))
						result.SkipDetails = append(result.SkipDetails, fmt.Sprintf("分组 %q 创建失败：%v", sec.GroupName, err))
						continue
					}
					if sec.LLMTarget != "" && h.llmBindingRepo != nil {
						if err := h.llmBindingRepo.Set(sec.LLMTarget, nil, &g.ID); err != nil {
							h.logger.Warn("import: set group LLM binding failed",
								zap.String("group", sec.GroupName), zap.Error(err))
						} else {
							result.BindingsSet++
						}
					}
				} else if sec.LLMTarget != "" {
					result.BindingsSet++ // dry-run 预览计数
				}
				result.GroupsCreated++
				id := g.ID
				groupID = &id
			}
		}

		for _, u := range sec.Users {
			existingUsers, err := h.userRepo.ListByUsername(u.Username)
			if err != nil {
				h.logger.Warn("import: lookup user failed", zap.String("user", u.Username), zap.Error(err))
			}
			if len(existingUsers) > 0 {
				result.UsersSkipped++
				result.SkipDetails = append(result.SkipDetails, fmt.Sprintf("用户 %q 已存在，跳过", u.Username))
				continue
			}

			if !dryRun {
				hash, err := auth.HashPassword(h.logger, u.Password)
				if err != nil {
					h.logger.Warn("import: hash password failed", zap.String("user", u.Username), zap.Error(err))
					result.SkipDetails = append(result.SkipDetails, fmt.Sprintf("用户 %q 密码处理失败：%v", u.Username, err))
					continue
				}
				newUser := &db.User{
					Username:     u.Username,
					PasswordHash: hash,
					GroupID:      groupID,
					IsActive:     true,
				}
				if err := h.userRepo.Create(newUser); err != nil {
					h.logger.Warn("import: create user failed", zap.String("user", u.Username), zap.Error(err))
					result.SkipDetails = append(result.SkipDetails, fmt.Sprintf("用户 %q 创建失败：%v", u.Username, err))
					continue
				}
				if u.LLMOverride != "" && h.llmBindingRepo != nil {
					if err := h.llmBindingRepo.Set(u.LLMOverride, &newUser.ID, nil); err != nil {
						h.logger.Warn("import: set user LLM binding failed",
							zap.String("user", u.Username), zap.Error(err))
					} else {
						result.BindingsSet++
					}
				}
			} else if u.LLMOverride != "" {
				result.BindingsSet++ // dry-run 预览计数
			}
			result.UsersCreated++
		}
	}

	if !dryRun {
		summary := fmt.Sprintf("groups_created=%d groups_skipped=%d users_created=%d users_skipped=%d bindings=%d",
			result.GroupsCreated, result.GroupsSkipped, result.UsersCreated, result.UsersSkipped, result.BindingsSet)
		if aerr := h.auditRepo.Create("admin", "import", "bulk", summary); aerr != nil {
			h.logger.Warn("import: audit log failed", zap.Error(aerr))
		}
	}

	h.logger.Info("import completed",
		zap.Bool("dry_run", dryRun),
		zap.Int("groups_created", result.GroupsCreated),
		zap.Int("groups_skipped", result.GroupsSkipped),
		zap.Int("users_created", result.UsersCreated),
		zap.Int("users_skipped", result.UsersSkipped),
		zap.Int("bindings_set", result.BindingsSet),
	)

	h.renderPage(w, "import.html", importPageData{
		Content: content,
		Result:  result,
	})
}

// ── 告警与错误日志 ────────────────────────────────────────────────────────────

// handleAlertsPage 渲染告警页；实际数据由客户端 JS 通过 handleEventsAPI 获取。
func (h *Handler) handleAlertsPage(w http.ResponseWriter, r *http.Request) {
	activeTab := r.URL.Query().Get("tab")
	if activeTab == "" {
		activeTab = "live"
	}

	data := map[string]interface{}{
		"Flash":       r.URL.Query().Get("flash"),
		"Error":       r.URL.Query().Get("error"),
		"IsWorkerNode": h.isWorkerNode,
		"ActiveTab":   activeTab,
	}
	h.renderPage(w, "alerts.html", data)
}

// eventsResponse 是 /api/dashboard/events 的 JSON 响应结构。
type eventsResponse struct {
	Events []eventlog.Event `json:"events"`
	Total  int              `json:"total"`
}

// handleEventsAPI 返回告警事件列表。
//
// 支持的查询参数：
//
//	level=error|warn|all  (默认 all)
//	limit=N               (默认 100，最大 500)
//	since=<RFC3339>       (只返回该时间点之后的事件)
func (h *Handler) handleEventsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.eventLog == nil {
		_ = json.NewEncoder(w).Encode(eventsResponse{Events: []eventlog.Event{}, Total: 0})
		return
	}

	q := r.URL.Query()

	// 解析 since 参数
	var since time.Time
	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			since = t
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			since = t
		} else {
			h.logger.Debug("events API: invalid 'since' parameter, ignoring",
				zap.String("since", s),
			)
		}
	}

	// 获取事件（按 since 或取最近 N 条）
	var events []eventlog.Event
	if !since.IsZero() {
		events = h.eventLog.Since(since)
	} else {
		limit := 100
		if l := q.Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				if n > 500 {
					n = 500
				}
				limit = n
			}
		}
		events = h.eventLog.Recent(limit)
	}

	// 客户端过滤：level=error|warn
	level := q.Get("level")
	if level == "error" || level == "warn" {
		filtered := events[:0]
		want := eventlog.Level(level)
		for _, e := range events {
			if e.Level == want {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	if events == nil {
		events = []eventlog.Event{}
	}

	// 返回最新在前（reverse）
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	_ = json.NewEncoder(w).Encode(eventsResponse{Events: events, Total: len(events)})
}

// ---------------------------------------------------------------------------
// Group-Target Set Handlers (v2.20)
// ---------------------------------------------------------------------------

// handleTargetSetCreate 创建新的 target set
func (h *Handler) handleTargetSetCreate(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	id := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	groupID := strings.TrimSpace(r.FormValue("group_id"))
	strategy := strings.TrimSpace(r.FormValue("strategy"))

	if id == "" || name == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=id+name+required", http.StatusSeeOther)
		return
	}

	// Validate ID format: alphanumeric, dash, underscore only
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id+format", http.StatusSeeOther)
		return
	}

	// Handle optional group_id: empty = default (global) target set
	var groupIDPtr *string
	isDefault := false
	if groupID == "" {
		groupIDPtr = nil
		isDefault = true
	} else {
		groupIDPtr = &groupID
		isDefault = false
	}

	set := &db.GroupTargetSet{
		ID:        id,
		Name:      name,
		GroupID:   groupIDPtr,
		Strategy:  strategy,
		IsDefault: isDefault,
	}

	if err := h.groupTargetSetRepo.Create(set); err != nil {
		h.logger.Error("failed to create target set", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=failed+to+create+target+set", http.StatusSeeOther)
		return
	}

	detail := map[string]string{"id": id, "name": name, "groupID": groupID}
	detailBytes, _ := json.Marshal(detail)
	if aerr := h.auditRepo.Create("admin", "targetset.create", id, string(detailBytes)); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&flash=target+set+created", http.StatusSeeOther)
}

// handleTargetSetUpdate 更新 target set 信息
func (h *Handler) handleTargetSetUpdate(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id", http.StatusSeeOther)
		return
	}

	set, err := h.groupTargetSetRepo.GetByID(id)
	if err != nil || set == nil {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=target+set+not+found", http.StatusSeeOther)
		return
	}

	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		set.Name = name
	}
	if groupID := strings.TrimSpace(r.FormValue("group_id")); groupID != "" {
		set.GroupID = &groupID
	}
	if strategy := strings.TrimSpace(r.FormValue("strategy")); strategy != "" {
		set.Strategy = strategy
	}

	if err := h.groupTargetSetRepo.Update(set); err != nil {
		h.logger.Error("failed to update target set", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=failed+to+update+target+set", http.StatusSeeOther)
		return
	}

	if aerr := h.auditRepo.Create("admin", "targetset.update", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&flash=target+set+updated", http.StatusSeeOther)
}

// handleTargetSetDelete 删除 target set
func (h *Handler) handleTargetSetDelete(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id", http.StatusSeeOther)
		return
	}

	if err := h.groupTargetSetRepo.Delete(id); err != nil {
		h.logger.Error("failed to delete target set", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=failed+to+delete+target+set", http.StatusSeeOther)
		return
	}

	if aerr := h.auditRepo.Create("admin", "targetset.delete", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&flash=target+set+deleted", http.StatusSeeOther)
}

// handleTargetSetAddMember 添加成员到 target set
func (h *Handler) handleTargetSetAddMember(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id", http.StatusSeeOther)
		return
	}

	targetURL := strings.TrimSpace(r.FormValue("target_url"))
	if targetURL == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&error=target_url+required", http.StatusSeeOther)
		return
	}

	weight := 1
	if w := r.FormValue("weight"); w != "" {
		if parsed, err := strconv.Atoi(w); err == nil && parsed > 0 {
			weight = parsed
		}
	}

	member := &db.GroupTargetSetMember{
		TargetURL: targetURL,
		Weight:    weight,
		IsActive:  true,
	}

	if err := h.groupTargetSetRepo.AddMember(id, member); err != nil {
		h.logger.Error("failed to add member", zap.String("setID", id), zap.String("targetURL", targetURL), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&error=failed+to+add+member", http.StatusSeeOther)
		return
	}

	if aerr := h.auditRepo.Create("admin", "targetset.add_member", id, targetURL); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&flash=member+added", http.StatusSeeOther)
}

// handleTargetSetUpdateMember 更新 target set 成员权重
func (h *Handler) handleTargetSetUpdateMember(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	memberID := strings.TrimSpace(r.FormValue("target_url"))
	if id == "" || memberID == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id", http.StatusSeeOther)
		return
	}

	weight := 1
	if w := r.FormValue("weight"); w != "" {
		if parsed, err := strconv.Atoi(w); err == nil && parsed > 0 {
			weight = parsed
		}
	}

	priority := 0
	if p := r.FormValue("priority"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			priority = parsed
		}
	}

	if err := h.groupTargetSetRepo.UpdateMember(id, memberID, weight, priority); err != nil {
		h.logger.Error("failed to update member", zap.String("setID", id), zap.String("memberID", memberID), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&error=failed+to+update+member", http.StatusSeeOther)
		return
	}

	if aerr := h.auditRepo.Create("admin", "targetset.update_member", id, memberID); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&flash=member+updated", http.StatusSeeOther)
}

// handleTargetSetRemoveMember 从 target set 删除成员
func (h *Handler) handleTargetSetRemoveMember(w http.ResponseWriter, r *http.Request) {
	if h.groupTargetSetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=target+set+feature+not+enabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	memberID := strings.TrimSpace(r.FormValue("target_url"))
	if id == "" || memberID == "" {
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&error=invalid+id", http.StatusSeeOther)
		return
	}

	if err := h.groupTargetSetRepo.RemoveMember(id, memberID); err != nil {
		h.logger.Error("failed to remove member", zap.String("setID", id), zap.String("memberID", memberID), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&error=failed+to+remove+member", http.StatusSeeOther)
		return
	}

	if aerr := h.auditRepo.Create("admin", "targetset.remove_member", id, memberID); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/llm?tab=targetsets&selected="+id+"&flash=member+removed", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Alert Handlers (v2.20)
// ---------------------------------------------------------------------------

// handleAlertResolve 解决单条告警
func (h *Handler) handleAlertResolve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/alerts?tab=active&error=invalid+form", http.StatusSeeOther)
		return
	}

	eventID := strings.TrimSpace(r.FormValue("event_id"))
	if eventID == "" {
		http.Redirect(w, r, "/dashboard/alerts?tab=active&error=event_id+required", http.StatusSeeOther)
		return
	}

	// 记录审计日志
	if aerr := h.auditRepo.Create("admin", "alert.resolve", eventID, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	http.Redirect(w, r, "/dashboard/alerts?tab=active&flash=alert+resolved", http.StatusSeeOther)
}

// handleAlertResolveBatch 批量解决告警
func (h *Handler) handleAlertResolveBatch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/alerts?tab=active&error=invalid+form", http.StatusSeeOther)
		return
	}

	eventIDs := r.Form["event_ids"]
	if len(eventIDs) == 0 {
		http.Redirect(w, r, "/dashboard/alerts?tab=active&error=no+events+selected", http.StatusSeeOther)
		return
	}

	// 记录审计日志
	if aerr := h.auditRepo.Create("admin", "alert.resolve_batch", strconv.Itoa(len(eventIDs)), ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	import_url := "/dashboard/alerts?tab=active&flash=" + strconv.Itoa(len(eventIDs)) + "+alerts+resolved"
	http.Redirect(w, r, import_url, http.StatusSeeOther)
}
