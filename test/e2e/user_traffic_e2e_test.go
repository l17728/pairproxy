// Package e2e_test 包含"用户流量查看"功能的 E2E 测试。
//
// 测试覆盖：
//   - GET /api/admin/active-users 接口：权限控制、活跃用户过滤、days 参数
//   - GET /api/user/quota-status?username=xxx 管理员查询指定用户
//   - GET /api/user/usage-history?username=xxx 管理员查询指定用户历史
//   - 权限隔离：普通用户无法通过 ?username 查看他人数据
package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// 测试环境构建
// ---------------------------------------------------------------------------

type userTrafficEnv struct {
	server     *httptest.Server
	client     *http.Client
	jwtMgr     *auth.Manager
	gormDB     *gorm.DB
	adminToken string
}

// setupUserTrafficEnv 构建完整的 Dashboard + API 测试环境
func setupUserTrafficEnv(t *testing.T) *userTrafficEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	jwtMgr, err := auth.NewManager(logger, "e2e-traffic-secret")
	if err != nil {
		t.Fatalf("auth.NewManager: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	hash, _ := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)

	// 注册 dashboard 路由（含 /dashboard/* 页面）
	dashHandler := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	// 注册 API 路由（含 /api/admin/* 和 /api/user/*）
	adminHandler := api.NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	userHandler := api.NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	mux := http.NewServeMux()
	dashHandler.RegisterRoutes(mux)
	adminHandler.RegisterRoutes(mux)
	userHandler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	adminToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign admin token: %v", err)
	}

	return &userTrafficEnv{
		server:     server,
		client:     &http.Client{Timeout: 5 * time.Second},
		jwtMgr:     jwtMgr,
		gormDB:     gormDB,
		adminToken: adminToken,
	}
}

// adminGet 以管理员 cookie 发起 GET 请求
func (e *userTrafficEnv) adminGet(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", e.server.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.adminToken})
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// adminGetBearer 以管理员 Bearer token 发起 GET 请求（用于 /api/user/* 端点）
func (e *userTrafficEnv) adminGetBearer(t *testing.T, path string) *http.Response {
	t.Helper()
	return e.userGet(t, path, e.adminToken)
}

// userGet 以普通用户 Bearer token 发起 GET 请求
func (e *userTrafficEnv) userGet(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", e.server.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// mustDecode JSON 解码 body，失败则 Fatal
func mustDecode(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("json decode: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 测试：GET /api/admin/active-users
// ---------------------------------------------------------------------------

func TestE2E_ActiveUsers_AdminCanGetList(t *testing.T) {
	env := setupUserTrafficEnv(t)
	logger := zaptest.NewLogger(t)
	userRepo := db.NewUserRepo(env.gormDB, logger)

	// 创建 2 个用户并写入最近的用量记录
	u1 := &db.User{Username: "alice", PasswordHash: "x", IsActive: true}
	u2 := &db.User{Username: "bob", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(u1); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if err := userRepo.Create(u2); err != nil {
		t.Fatalf("Create bob: %v", err)
	}

	now := time.Now()
	for i, uid := range []string{u1.ID, u2.ID} {
		if err := env.gormDB.Create(&db.UsageLog{
			RequestID:    fmt.Sprintf("req-active-%d", i),
			UserID:       uid,
			InputTokens:  100,
			OutputTokens: 50,
			CreatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
		}).Error; err != nil {
			t.Fatalf("Create usage: %v", err)
		}
	}

	resp := env.adminGet(t, "/api/admin/active-users?days=30")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	mustDecode(t, resp.Body, &users)

	if len(users) != 2 {
		t.Errorf("got %d active users, want 2", len(users))
	}

	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	if !strings.Contains(strings.Join(names, ","), "alice") || !strings.Contains(strings.Join(names, ","), "bob") {
		t.Errorf("expected alice and bob in list, got %v", names)
	}
	t.Logf("active users: %v", names)
}

func TestE2E_ActiveUsers_FilterByDays(t *testing.T) {
	env := setupUserTrafficEnv(t)
	logger := zaptest.NewLogger(t)
	userRepo := db.NewUserRepo(env.gormDB, logger)

	// recent: 活动在 10 天内；old: 活动在 40 天前
	recent := &db.User{Username: "recent_user", PasswordHash: "x", IsActive: true}
	old := &db.User{Username: "old_user", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(recent); err != nil {
		t.Fatalf("Create recent: %v", err)
	}
	if err := userRepo.Create(old); err != nil {
		t.Fatalf("Create old: %v", err)
	}

	now := time.Now()
	env.gormDB.Create(&db.UsageLog{
		RequestID: "req-recent", UserID: recent.ID,
		InputTokens: 100, CreatedAt: now.Add(-10 * 24 * time.Hour),
	})
	env.gormDB.Create(&db.UsageLog{
		RequestID: "req-old", UserID: old.ID,
		InputTokens: 100, CreatedAt: now.Add(-40 * 24 * time.Hour),
	})

	// ?days=30 只应返回 recent_user
	resp := env.adminGet(t, "/api/admin/active-users?days=30")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var users []struct{ Username string `json:"username"` }
	mustDecode(t, resp.Body, &users)

	if len(users) != 1 || users[0].Username != "recent_user" {
		t.Errorf("days=30: expected [recent_user], got %+v", users)
	}
	t.Logf("days=30 users: %+v", users)

	// ?days=60 应返回 2 个用户
	resp2 := env.adminGet(t, "/api/admin/active-users?days=60")
	defer resp2.Body.Close()
	var users2 []struct{ Username string `json:"username"` }
	mustDecode(t, resp2.Body, &users2)

	if len(users2) != 2 {
		t.Errorf("days=60: expected 2 users, got %d", len(users2))
	}
	t.Logf("days=60 users: %+v", users2)
}

func TestE2E_ActiveUsers_UnauthorizedWithoutAdminToken(t *testing.T) {
	env := setupUserTrafficEnv(t)

	// 不带 token
	req, _ := http.NewRequest("GET", env.server.URL+"/api/admin/active-users", nil)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	t.Logf("unauthorized response: %d (correct)", resp.StatusCode)
}

// ---------------------------------------------------------------------------
// 测试：管理员通过 ?username 查询指定用户配额状态
// ---------------------------------------------------------------------------

func TestE2E_AdminQueryQuotaStatus_SpecificUser(t *testing.T) {
	env := setupUserTrafficEnv(t)
	logger := zaptest.NewLogger(t)
	userRepo := db.NewUserRepo(env.gormDB, logger)

	// 创建有分组配额的用户
	grp := &db.Group{ID: "grp1", Name: "grp1"}
	daily := int64(5000)
	grp.DailyTokenLimit = &daily
	db.NewGroupRepo(env.gormDB, logger).Create(grp)

	grpID := "grp1"
	user := &db.User{Username: "charlie", PasswordHash: "x", IsActive: true, GroupID: &grpID}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create charlie: %v", err)
	}

	// 为 charlie 写入今日用量（使用本地时间，与 handler 的 now.Location() 保持一致）
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, now.Location())
	if err := env.gormDB.Create(&db.UsageLog{
		RequestID:    "req-charlie-today",
		UserID:       user.ID,
		InputTokens:  800,
		OutputTokens: 200,
		TotalTokens:  1000,
		CreatedAt:    today,
	}).Error; err != nil {
		t.Fatalf("Create usage: %v", err)
	}

	resp := env.adminGetBearer(t, "/api/user/quota-status?username=charlie")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var quota struct {
		DailyUsed  int64 `json:"daily_used"`
		DailyLimit int64 `json:"daily_limit"`
	}
	mustDecode(t, resp.Body, &quota)

	if quota.DailyUsed != 1000 {
		t.Errorf("daily_used = %d, want 1000", quota.DailyUsed)
	}
	if quota.DailyLimit != 5000 {
		t.Errorf("daily_limit = %d, want 5000", quota.DailyLimit)
	}
	t.Logf("charlie quota: used=%d, limit=%d", quota.DailyUsed, quota.DailyLimit)
}

func TestE2E_AdminQueryQuotaStatus_UserNotFound(t *testing.T) {
	env := setupUserTrafficEnv(t)

	resp := env.adminGetBearer(t, "/api/user/quota-status?username=nonexistent")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	t.Logf("nonexistent user response: %d (correct)", resp.StatusCode)
}

// ---------------------------------------------------------------------------
// 测试：管理员通过 ?username 查询指定用户用量历史
// ---------------------------------------------------------------------------

func TestE2E_AdminQueryUsageHistory_SpecificUser(t *testing.T) {
	env := setupUserTrafficEnv(t)
	logger := zaptest.NewLogger(t)
	userRepo := db.NewUserRepo(env.gormDB, logger)

	user := &db.User{Username: "dave", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create dave: %v", err)
	}

	// 写入最近 5 天的用量记录
	now := time.Now()
	for i := 0; i < 5; i++ {
		day := now.AddDate(0, 0, -i).Truncate(24 * time.Hour).Add(time.Hour)
		if err := env.gormDB.Create(&db.UsageLog{
			RequestID:    fmt.Sprintf("req-dave-day%d", i),
			UserID:       user.ID,
			InputTokens:  100 * (i + 1),
			OutputTokens: 50 * (i + 1),
			TotalTokens:  150 * (i + 1),
			CreatedAt:    day,
		}).Error; err != nil {
			t.Fatalf("Create usage day%d: %v", i, err)
		}
	}

	resp := env.adminGetBearer(t, "/api/user/usage-history?username=dave&days=7")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result struct {
		History []struct {
			Date         string `json:"date"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
		} `json:"history"`
	}
	mustDecode(t, resp.Body, &result)

	if len(result.History) == 0 {
		t.Fatal("expected usage history, got empty")
	}

	// 汇总所有天的 input tokens，应等于 100+200+300+400+500=1500
	var totalInput int
	for _, row := range result.History {
		totalInput += row.InputTokens
	}
	if totalInput != 1500 {
		t.Errorf("total input tokens = %d, want 1500", totalInput)
	}
	t.Logf("dave usage history: %d days, total_input=%d", len(result.History), totalInput)
}

// ---------------------------------------------------------------------------
// 测试：权限隔离 - 普通用户无法通过 ?username 查看他人数据
// ---------------------------------------------------------------------------

func TestE2E_RegularUserCannotViewOthersUsage(t *testing.T) {
	env := setupUserTrafficEnv(t)
	logger := zaptest.NewLogger(t)
	userRepo := db.NewUserRepo(env.gormDB, logger)

	// 创建两个用户
	userA := &db.User{Username: "user_a", PasswordHash: "x", IsActive: true}
	userB := &db.User{Username: "user_b", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(userA); err != nil {
		t.Fatalf("Create user_a: %v", err)
	}
	if err := userRepo.Create(userB); err != nil {
		t.Fatalf("Create user_b: %v", err)
	}

	// 只给 user_b 写入今日用量（user_a 没有用量）
	today := time.Now().Truncate(24 * time.Hour).Add(time.Hour)
	if err := env.gormDB.Create(&db.UsageLog{
		RequestID:    "req-b-only",
		UserID:       userB.ID,
		InputTokens:  999,
		OutputTokens: 1,
		TotalTokens:  1000,
		CreatedAt:    today,
	}).Error; err != nil {
		t.Fatalf("Create usage for user_b: %v", err)
	}

	// 生成 user_a 的 token
	tokenA, err := env.jwtMgr.Sign(auth.JWTClaims{
		UserID:   userA.ID,
		Username: "user_a",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token for user_a: %v", err)
	}

	// user_a 尝试用 ?username=user_b 查询 user_b 的配额（应被忽略，返回 user_a 自己的 0 用量）
	resp := env.userGet(t, "/api/user/quota-status?username=user_b", tokenA)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var quota struct {
		DailyUsed int64 `json:"daily_used"`
	}
	mustDecode(t, resp.Body, &quota)

	// user_a 没有任何用量，所以应该是 0（而不是 user_b 的 1000）
	if quota.DailyUsed == 1000 {
		t.Errorf("security violation: user_a can see user_b's usage (daily_used=1000)")
	}
	if quota.DailyUsed != 0 {
		t.Errorf("daily_used = %d, want 0 (user_a's own usage)", quota.DailyUsed)
	}
	t.Logf("user_a daily_used=%d (correctly shows own data, not user_b's 1000)", quota.DailyUsed)
}
