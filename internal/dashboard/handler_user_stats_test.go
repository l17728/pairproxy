package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// 测试辅助
// ---------------------------------------------------------------------------

type userStatsTestEnv struct {
	mux    *http.ServeMux
	cookie *http.Cookie
	gormDB *gorm.DB
	uRepo  *db.UserRepo
	gRepo  *db.GroupRepo
}

func setupUserStatsEnv(t *testing.T) *userStatsTestEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("auth.NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "__admin__", Username: "admin", Role: "admin"}, time.Hour)
	cookie := &http.Cookie{Name: api.AdminCookieName, Value: token}

	return &userStatsTestEnv{
		mux:    mux,
		cookie: cookie,
		gormDB: gormDB,
		uRepo:  userRepo,
		gRepo:  groupRepo,
	}
}

// insertUsageLog 直接写入 usage_logs 表（绕开异步 writer，保证测试确定性）
func insertUsageLog(t *testing.T, gormDB *gorm.DB, l db.UsageLog) {
	t.Helper()
	if l.RequestID == "" {
		t.Fatal("insertUsageLog: RequestID must not be empty")
	}
	if err := gormDB.Create(&l).Error; err != nil {
		t.Fatalf("insertUsageLog: %v", err)
	}
}

// doUserStatsReq 发起一次 GET /dashboard/api/user-stats 请求并解码 JSON。
// 返回分页响应中的 users 列表（向后兼容测试断言）。
func doUserStatsReq(t *testing.T, env *userStatsTestEnv) []map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/user-stats?page_size=200", nil)
	req.AddCookie(env.cookie)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	// 新格式：{"total":N,"page":1,"page_size":200,"total_pages":M,"users":[...]}
	var page struct {
		Users []map[string]interface{} `json:"users"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	return page.Users
}

// ---------------------------------------------------------------------------
// TestHandleUserStats — GET /dashboard/api/user-stats
// ---------------------------------------------------------------------------

// TestHandleUserStats_Empty 空用户库返回 [] 而非 null/错误
func TestHandleUserStats_Empty(t *testing.T) {
	env := setupUserStatsEnv(t)
	resp := doUserStatsReq(t, env)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

// TestHandleUserStats_RequiresAuth 未认证请求返回非 200
func TestHandleUserStats_RequiresAuth(t *testing.T) {
	env := setupUserStatsEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/user-stats", nil)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Error("unauthenticated request should not return 200")
	}
}

// TestHandleUserStats_ReturnsAllUsers 所有用户（含零用量）都出现在响应中，且字段完整
func TestHandleUserStats_ReturnsAllUsers(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "u1", Username: "alice", IsActive: true})
	env.uRepo.Create(&db.User{ID: "u2", Username: "bob", IsActive: true})

	resp := doUserStatsReq(t, env)
	if len(resp) != 2 {
		t.Fatalf("expected 2 users, got %d", len(resp))
	}
	requiredFields := []string{
		"user_id", "username", "group_name",
		"total_input", "total_output", "total_tokens",
		"avg_daily", "avg_monthly", "days_active", "months_active",
		"first_used_at", "last_used_at", "is_active",
	}
	for _, u := range resp {
		for _, field := range requiredFields {
			if _, ok := u[field]; !ok {
				t.Errorf("user %v missing field %q", u["username"], field)
			}
		}
	}
}

// TestHandleUserStats_TokenCounts 有用量记录时 total_input/output/tokens 正确
func TestHandleUserStats_TokenCounts(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "u-stats", Username: "carol", IsActive: true})

	now := time.Now()
	insertUsageLog(t, env.gormDB, db.UsageLog{
		RequestID: "s1", UserID: "u-stats", Model: "claude-3",
		InputTokens: 300, OutputTokens: 700, CreatedAt: now.Add(-24 * time.Hour),
	})
	insertUsageLog(t, env.gormDB, db.UsageLog{
		RequestID: "s2", UserID: "u-stats", Model: "claude-3",
		InputTokens: 200, OutputTokens: 300, CreatedAt: now,
	})

	resp := doUserStatsReq(t, env)
	if len(resp) != 1 {
		t.Fatalf("expected 1 user, got %d", len(resp))
	}
	u := resp[0]
	if u["username"] != "carol" {
		t.Errorf("username = %v, want carol", u["username"])
	}
	if int64(u["total_input"].(float64)) != 500 {
		t.Errorf("total_input = %v, want 500", u["total_input"])
	}
	if int64(u["total_output"].(float64)) != 1000 {
		t.Errorf("total_output = %v, want 1000", u["total_output"])
	}
	if int64(u["total_tokens"].(float64)) != 1500 {
		t.Errorf("total_tokens = %v, want 1500", u["total_tokens"])
	}
	// 两条记录跨 2 天 → days_active = 2
	if int(u["days_active"].(float64)) != 2 {
		t.Errorf("days_active = %v, want 2", u["days_active"])
	}
}

// TestHandleUserStats_AvgCalculation 平均日用量 = total_tokens / days_active
func TestHandleUserStats_AvgCalculation(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "u-avg", Username: "dave", IsActive: true})

	// 3 条记录分属 3 个不同日期（days_active=3），每条 500+500=1000，合计 3000
	for i := 0; i < 3; i++ {
		insertUsageLog(t, env.gormDB, db.UsageLog{
			RequestID:    "avg-" + string(rune('0'+i)),
			UserID:       "u-avg",
			InputTokens:  500,
			OutputTokens: 500,
			CreatedAt:    time.Now().AddDate(0, 0, -i),
		})
	}

	resp := doUserStatsReq(t, env)
	if len(resp) != 1 {
		t.Fatalf("expected 1, got %d", len(resp))
	}
	u := resp[0]
	// avg_daily = 3000 / 3 = 1000
	if int64(u["avg_daily"].(float64)) != 1000 {
		t.Errorf("avg_daily = %v, want 1000", u["avg_daily"])
	}
}

// TestHandleUserStats_IsActiveField is_active 字段正确反映用户状态
func TestHandleUserStats_IsActiveField(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "ua", Username: "active-user", IsActive: true})

	// GORM 会忽略 bool 零值，需要在 Create 后单独 SetActive
	env.uRepo.Create(&db.User{ID: "ui", Username: "inactive-user", IsActive: true})
	env.uRepo.SetActive("ui", false)

	resp := doUserStatsReq(t, env)
	byName := map[string]map[string]interface{}{}
	for _, u := range resp {
		byName[u["username"].(string)] = u
	}
	if byName["active-user"]["is_active"] != true {
		t.Errorf("active-user.is_active = %v, want true", byName["active-user"]["is_active"])
	}
	if byName["inactive-user"]["is_active"] != false {
		t.Errorf("inactive-user.is_active = %v, want false", byName["inactive-user"]["is_active"])
	}
}

// TestHandleUserStats_CacheBehavior 5 分钟内重复调用使用缓存（结果一致）
func TestHandleUserStats_CacheBehavior(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "cache-u", Username: "cacheuser", IsActive: true})

	r1 := doUserStatsReq(t, env)
	r2 := doUserStatsReq(t, env) // 第二次应命中缓存

	if len(r1) != len(r2) {
		t.Errorf("cache: first call returned %d items, second returned %d", len(r1), len(r2))
	}
	if len(r1) != 1 {
		t.Errorf("expected 1 user, got %d", len(r1))
	}
	if r1[0]["username"] != r2[0]["username"] {
		t.Errorf("cache: username mismatch: %v vs %v", r1[0]["username"], r2[0]["username"])
	}
}

// TestHandleUserStats_ZeroUsageUser 无用量记录的用户返回零值，日期字段为空字符串
func TestHandleUserStats_ZeroUsageUser(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "no-usage", Username: "newbie", IsActive: true})

	resp := doUserStatsReq(t, env)
	if len(resp) != 1 {
		t.Fatalf("expected 1, got %d", len(resp))
	}
	u := resp[0]
	if u["total_tokens"].(float64) != 0 {
		t.Errorf("total_tokens = %v, want 0 for user with no usage", u["total_tokens"])
	}
	if u["first_used_at"].(string) != "" {
		t.Errorf("first_used_at = %q, want empty string", u["first_used_at"])
	}
	if u["last_used_at"].(string) != "" {
		t.Errorf("last_used_at = %q, want empty string", u["last_used_at"])
	}
}

// TestHandleUserStats_GroupName 无分组用户返回 group_name=""
func TestHandleUserStats_GroupName(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "g-none", Username: "ungrouped", IsActive: true})

	resp := doUserStatsReq(t, env)
	if len(resp) == 0 {
		t.Fatal("expected at least 1 user")
	}
	for _, u := range resp {
		if u["username"] == "ungrouped" {
			if u["group_name"].(string) != "" {
				t.Errorf("ungrouped group_name = %q, want empty", u["group_name"])
			}
		}
	}
}

// TestHandleUserStats_DateFields 首次/最后使用日期格式为 YYYY-MM-DD
func TestHandleUserStats_DateFields(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(&db.User{ID: "u-date", Username: "datey", IsActive: true})

	day := time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
	insertUsageLog(t, env.gormDB, db.UsageLog{
		RequestID: "dt1", UserID: "u-date", InputTokens: 100, CreatedAt: day,
	})

	resp := doUserStatsReq(t, env)
	if len(resp) != 1 {
		t.Fatalf("expected 1, got %d", len(resp))
	}
	u := resp[0]
	first := u["first_used_at"].(string)
	last := u["last_used_at"].(string)
	if first != "2025-03-15" {
		t.Errorf("first_used_at = %q, want 2025-03-15", first)
	}
	if last != "2025-03-15" {
		t.Errorf("last_used_at = %q, want 2025-03-15", last)
	}
}



// ---------------------------------------------------------------------------
// Fix 11: dashboard usage_handler uses ListByUsername — 409 on ambiguous username
// ---------------------------------------------------------------------------

// TestDashboardUserQuota_AmbiguousUsername_Returns409 verifies that when two users
// share the same username (different auth providers), the dashboard user-quota
// endpoint returns 409 Conflict with an ambiguity message.
func TestDashboardUserQuota_AmbiguousUsername_Returns409(t *testing.T) {
	env := setupUserStatsEnv(t)

	extID := "ldap-alice"
	if err := env.uRepo.Create(&db.User{
		Username: "alice", PasswordHash: "h1", AuthProvider: "local", IsActive: true,
	}); err != nil {
		t.Fatalf("create local alice: %v", err)
	}
	if err := env.uRepo.Create(&db.User{
		Username: "alice", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID, IsActive: true,
	}); err != nil {
		t.Fatalf("create ldap alice: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/user-quota?username=alice", nil)
	req.AddCookie(env.cookie)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("ambiguous username: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if msg, _ := body["error"].(string); msg == "" {
		t.Error("response should contain error message for ambiguous username")
	}
}

// TestDashboardUserHistory_AmbiguousUsername_Returns409 verifies the same
// ambiguity detection for the usage-history endpoint.
func TestDashboardUserHistory_AmbiguousUsername_Returns409(t *testing.T) {
	env := setupUserStatsEnv(t)

	extID := "ldap-bob"
	if err := env.uRepo.Create(&db.User{
		Username: "bob", PasswordHash: "h1", AuthProvider: "local", IsActive: true,
	}); err != nil {
		t.Fatalf("create local bob: %v", err)
	}
	if err := env.uRepo.Create(&db.User{
		Username: "bob", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID, IsActive: true,
	}); err != nil {
		t.Fatalf("create ldap bob: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/user-history?username=bob", nil)
	req.AddCookie(env.cookie)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("ambiguous username: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
}
