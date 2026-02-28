package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupAdminTest 创建用于 admin handler 集成测试的测试环境
func setupAdminTest(t *testing.T, adminPasswordHash string) (*AdminHandler, *auth.Manager, *http.ServeMux) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
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

	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, adminPasswordHash, time.Hour)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return handler, jwtMgr, mux
}

// adminToken 签发一个 admin JWT
func adminToken(t *testing.T, jwtMgr *auth.Manager) string {
	t.Helper()
	tok, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}
	return tok
}

// ---------------------------------------------------------------------------
// TestAdminLogin — 登录成功 + 密码错误
// ---------------------------------------------------------------------------

func TestAdminLogin(t *testing.T) {
	hash, err := auth.HashPassword(zaptest.NewLogger(t), "supersecret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	_, _, mux := setupAdminTest(t, hash)

	t.Run("correct password", func(t *testing.T) {
		body := `{"password":"supersecret"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var resp adminLoginResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Token == "" {
			t.Error("expected non-empty token")
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		body := `{"password":"wrong"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("empty password", func(t *testing.T) {
		body := `{"password":""}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAdminRequireAdmin — 无 token 返回 401
// ---------------------------------------------------------------------------

func TestAdminRequireAdmin(t *testing.T) {
	_, _, mux := setupAdminTest(t, "hash")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminUserCRUD — 创建用户 + 列表 + 禁用 + 重置密码
// ---------------------------------------------------------------------------

func TestAdminUserCRUD(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	authHeader := "Bearer " + tok

	// 创建用户
	t.Run("create user", func(t *testing.T) {
		body := `{"username":"alice","password":"pw123456"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBufferString(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
		}
		var resp userResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Username != "alice" {
			t.Errorf("username = %q, want alice", resp.Username)
		}
		if !resp.IsActive {
			t.Error("expected IsActive = true")
		}
	})

	// 列出用户
	t.Run("list users", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
		var users []userResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &users); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(users) == 0 {
			t.Error("expected at least one user")
		}
	})

	// 重复创建同名用户 → 409
	t.Run("duplicate username returns 409", func(t *testing.T) {
		body := `{"username":"alice","password":"other"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBufferString(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAdminGroupCRUD — 分组创建 + 配额更新
// ---------------------------------------------------------------------------

func TestAdminGroupCRUD(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)
	authHeader := "Bearer " + tok

	var groupID string

	// 创建分组
	t.Run("create group", func(t *testing.T) {
		daily := int64(5000)
		body, _ := json.Marshal(createGroupRequest{Name: "engineering", DailyTokenLimit: &daily})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/groups", bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
		}
		var resp groupResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Name != "engineering" {
			t.Errorf("name = %q, want engineering", resp.Name)
		}
		if resp.DailyTokenLimit == nil || *resp.DailyTokenLimit != 5000 {
			t.Errorf("daily_token_limit = %v, want 5000", resp.DailyTokenLimit)
		}
		groupID = resp.ID
	})

	// 列出分组
	t.Run("list groups", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil)
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	// 设置配额
	t.Run("set quota", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group created")
		}
		newDaily := int64(9999)
		body, _ := json.Marshal(setQuotaRequest{DailyTokenLimit: &newDaily})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/groups/%s/quota", groupID), bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204; body: %s", rr.Code, rr.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// TestAdminStatsSummary — 统计摘要
// ---------------------------------------------------------------------------

func TestAdminStatsSummary(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/summary?days=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp statsSummaryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.From == "" || resp.To == "" {
		t.Error("expected From and To to be set")
	}
}

// ---------------------------------------------------------------------------
// TestAdminStatsLogs — 日志查询（空 DB）
// ---------------------------------------------------------------------------

func TestAdminStatsLogs(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/logs?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var logs []logEntryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &logs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 空 DB → 空数组（不是 null）
	if logs == nil {
		t.Error("expected non-nil (possibly empty) slice")
	}
}

// ---------------------------------------------------------------------------
// TestAdminAudit — 审计日志端点（P2-3）
// ---------------------------------------------------------------------------

func TestAdminAuditEndpoint(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)
	authHeader := "Bearer " + tok

	// GET /api/admin/audit on empty DB → 200 with empty array.
	t.Run("empty audit log returns 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil)
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var resp []auditLogResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp == nil {
			t.Error("expected non-nil slice")
		}
	})

	// Create a user → expect an audit entry for user.create.
	t.Run("create user writes audit record", func(t *testing.T) {
		body := `{"username":"bob","password":"pw123456"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBufferString(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status = %d, want 201; body: %s", rr.Code, rr.Body.String())
		}

		// Now retrieve audit log.
		req2 := httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil)
		req2.Header.Set("Authorization", authHeader)
		rr2 := httptest.NewRecorder()
		mux.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusOK {
			t.Fatalf("audit list: status = %d; body: %s", rr2.Code, rr2.Body.String())
		}
		var logs []auditLogResponse
		if err := json.Unmarshal(rr2.Body.Bytes(), &logs); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(logs) == 0 {
			t.Fatal("expected at least one audit record after user creation")
		}
		found := false
		for _, l := range logs {
			if l.Action == "user.create" && l.Target == "bob" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("audit log missing user.create for bob; got: %+v", logs)
		}
	})

	// Create group → expect group.create audit entry.
	t.Run("create group writes audit record", func(t *testing.T) {
		body := `{"name":"devops"}`
		req := httptest.NewRequest(http.MethodPost, "/api/admin/groups", bytes.NewBufferString(body))
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create group: status = %d; body: %s", rr.Code, rr.Body.String())
		}

		req2 := httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil)
		req2.Header.Set("Authorization", authHeader)
		rr2 := httptest.NewRecorder()
		mux.ServeHTTP(rr2, req2)
		var logs []auditLogResponse
		_ = json.Unmarshal(rr2.Body.Bytes(), &logs)

		found := false
		for _, l := range logs {
			if l.Action == "group.create" && l.Target == "devops" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("audit log missing group.create for devops; got: %+v", logs)
		}
	})

	// GET /api/admin/audit without auth → 401.
	t.Run("audit list requires auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAdminCookieAuth — cookie 认证（供 dashboard 使用）
// ---------------------------------------------------------------------------

func TestAdminCookieAuth(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "adminpass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil)
	req.AddCookie(&http.Cookie{Name: AdminCookieName, Value: tok})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (cookie auth should work)", rr.Code)
	}
}
