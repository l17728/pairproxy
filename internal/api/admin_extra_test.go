package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupFullAdminTest 创建带预填数据的完整管理员测试环境
func setupFullAdminTest(t *testing.T) (*http.ServeMux, string, *db.UserRepo, *db.GroupRepo) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "full-test-secret-key")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, "", time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}

	return mux, tok, userRepo, groupRepo
}

// ---------------------------------------------------------------------------
// TestAdminStatsUsers — handleStatsUsers (0% 覆盖)
// ---------------------------------------------------------------------------

func TestAdminStatsUsers_Empty(t *testing.T) {
	mux, tok, _, _ := setupFullAdminTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp []interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
}

func TestAdminStatsUsers_WithUsageData(t *testing.T) {
	// 使用 setupExportTest（已有预填数据的环境）
	mux, tok := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/users?days=3650", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v; body: %s", err, rr.Body.String())
	}
	// export-user 应该出现在统计中（如果日期范围能匹配）
	t.Logf("stats/users returned %d rows", len(resp))
}

func TestAdminStatsUsers_Unauthorized(t *testing.T) {
	mux, _, _, _ := setupFullAdminTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/users", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without auth", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminLogin — 覆盖 handleLogin 更多分支
// ---------------------------------------------------------------------------

func TestAdminLogin_MissingBody(t *testing.T) {
	_, _, mux := setupAdminTest(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/login", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	// 无 body 应返回 400 或 401
	if rr.Code == http.StatusOK {
		t.Error("expected non-200 for missing body")
	}
}

// ---------------------------------------------------------------------------
// TestAdminListUsers — userToResponse 覆盖（包含 groupID 字段）
// ---------------------------------------------------------------------------

func TestAdminListUsers_WithGroup(t *testing.T) {
	mux, tok, userRepo, groupRepo := setupFullAdminTest(t)

	// 创建分组和用户
	grp := &db.Group{ID: "list-g1", Name: "list-group"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "list-g1"
	user := &db.User{ID: "list-u1", Username: "list_user", PasswordHash: "h", IsActive: true, GroupID: &gid}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	found := false
	for _, u := range resp {
		if u["id"] == "list-u1" {
			found = true
			if u["username"] != "list_user" {
				t.Errorf("username mismatch: %v", u["username"])
			}
			if u["group_id"] != "list-g1" {
				t.Errorf("group_id mismatch: %v", u["group_id"])
			}
		}
	}
	if !found {
		t.Error("list-u1 not found in users response")
	}
}

// ---------------------------------------------------------------------------
// TestAdminCreateGroup — handleCreateGroup 重复名称错误路径
// ---------------------------------------------------------------------------

func TestAdminCreateGroup_DuplicateName(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	body := `{"name":"dup-group"}`
	// 第一次创建
	req1 := httptest.NewRequest(http.MethodPost, "/api/admin/groups", strings.NewReader(body))
	req1.Header.Set("Authorization", "Bearer "+tok)
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusCreated && rr1.Code != http.StatusOK {
		t.Fatalf("first create status = %d, body: %s", rr1.Code, rr1.Body.String())
	}

	// 第二次创建（同名）应失败
	req2 := httptest.NewRequest(http.MethodPost, "/api/admin/groups", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer "+tok)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusCreated || rr2.Code == http.StatusOK {
		t.Errorf("duplicate group creation should fail, got %d", rr2.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminRevokeAPIKey — 未配置时返回 501
// ---------------------------------------------------------------------------

func TestAdminRevokeAPIKey_NotConfigured(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/api-keys/key-id", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when apiKeyRepo not configured, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminSetUserActive — 无效 JSON 返回 400
// ---------------------------------------------------------------------------

func TestAdminSetUserActive_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPut, "/api/admin/users/some-user-id/active", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminUndrain — undrainFn nil 返回 501
// ---------------------------------------------------------------------------

func TestAdminUndrain_NotConfigured(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/undrain", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when undrainFn not configured, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminSetGroupQuota — 无效 JSON 返回 400
// ---------------------------------------------------------------------------

func TestAdminSetGroupQuota_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPut, "/api/admin/groups/some-group/quota", strings.NewReader("{bad-json}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminResetPassword — 无效 JSON 返回 400
// ---------------------------------------------------------------------------

func TestAdminResetPassword_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux := setupAdminTest(t, "")
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPut, "/api/admin/users/some-user/password", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON body, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestClusterHandler_RegisterRoutes — 覆盖 ClusterHandler.RegisterRoutes
// ---------------------------------------------------------------------------

func TestClusterHandler_RegisterRoutes_Registers(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)
	mux := http.NewServeMux()

	// RegisterRoutes 不应 panic
	handler.RegisterRoutes(mux)

	// 验证注册了路由（通过发送请求确认路由可达）
	req := httptest.NewRequest(http.MethodGet, "/cluster/routing", nil)
	req.Header.Set("Authorization", "Bearer "+testClusterSecret)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// 有注册则能收到响应（200 或其他，不是 404）
	if rr.Code == http.StatusNotFound {
		t.Error("RegisterRoutes should have registered /cluster/routing")
	}
}
