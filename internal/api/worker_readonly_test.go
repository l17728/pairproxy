package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupWorkerAdminTest 创建 Worker 节点配置的 AdminHandler（isWorkerNode=true）。
func setupWorkerAdminTest(t *testing.T) (*AdminHandler, *auth.Manager, *http.ServeMux) {
	t.Helper()
	handler, jwtMgr, mux := setupAdminTest(t, "")
	handler.SetWorkerMode(true)
	// 重新注册路由（SetWorkerMode 必须在 RegisterRoutes 之前或重新注册）
	mux2 := http.NewServeMux()
	handler.RegisterRoutes(mux2)
	handler.RegisterLLMRoutes(mux2)
	_ = mux
	return handler, jwtMgr, mux2
}

// TestWorkerBlocksWriteOperations 验证 Worker 节点拒绝所有写操作（POST/PUT/DELETE），返回 403。
func TestWorkerBlocksWriteOperations(t *testing.T) {
	handler, jwtMgr, mux := setupWorkerAdminTest(t)
	_ = handler
	tok := adminToken(t, jwtMgr)

	writePaths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/admin/users"},
		{http.MethodPut, "/api/admin/users/some-id/active"},
		{http.MethodPut, "/api/admin/users/some-id/password"},
		{http.MethodPost, "/api/admin/groups"},
		{http.MethodDelete, "/api/admin/groups/some-id"},
		{http.MethodPost, "/api/admin/llm/bindings"},
		{http.MethodDelete, "/api/admin/llm/bindings/some-id"},
		{http.MethodPost, "/api/admin/llm/distribute"},
	}

	for _, tc := range writePaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var req *http.Request
			if tc.method == http.MethodPost || tc.method == http.MethodPut {
				req = httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString("{}"))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Errorf("%s %s: status = %d, want 403 (worker_read_only)", tc.method, tc.path, rr.Code)
			}
		})
	}
}

// TestWorkerAllowsReadOperations 验证 Worker 节点允许 GET 读操作，返回 2xx。
func TestWorkerAllowsReadOperations(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	// 需要使用真实的 DB 以便 GET 端点正常工作
	handler, _, _ := setupAdminTest(t, "")
	handler.SetWorkerMode(true)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	tok := adminToken(t, jwtMgr)
	_ = tok

	readPaths := []string{
		"/api/admin/users",
		"/api/admin/groups",
	}

	tok2 := adminToken(t, handler.jwtMgr)

	for _, path := range readPaths {
		t.Run("GET "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tok2)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("GET %s: status = %d, want 200", path, rr.Code)
			}
		})
	}
}

// TestWorkerReadOnlyReturnsCorrectErrorBody 验证 Worker 403 响应体包含正确的 error code。
func TestWorkerReadOnlyReturnsCorrectErrorBody(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

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

	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, "", 0)
	handler.SetWorkerMode(true)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBufferString(`{"username":"x","password":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}

	body := rr.Body.String()
	if !containsStr(body, "worker_read_only") {
		t.Errorf("response body should contain 'worker_read_only', got: %s", body)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
