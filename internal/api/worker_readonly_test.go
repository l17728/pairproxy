package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupWorkerAdminTest 创建 Worker 节点配置的 AdminHandler（isWorkerNode=true）。
func setupWorkerAdminTest(t *testing.T) (*AdminHandler, *http.ServeMux) {
	t.Helper()
	handler, _, _ := setupAdminTest(t, "")
	handler.SetWorkerMode(true)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	handler.RegisterLLMRoutes(mux)
	return handler, mux
}

// TestWorkerBlocksWriteOperations 验证 Worker 节点拒绝所有写操作（POST/PUT/DELETE），返回 403。
func TestWorkerBlocksWriteOperations(t *testing.T) {
	handler, mux := setupWorkerAdminTest(t)
	tok := adminToken(t, handler.jwtMgr)

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
	handler, mux := setupWorkerAdminTest(t)
	tok := adminToken(t, handler.jwtMgr)

	readPaths := []string{
		"/api/admin/users",
		"/api/admin/groups",
	}

	for _, path := range readPaths {
		t.Run("GET "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
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
	handler, mux := setupWorkerAdminTest(t)
	tok := adminToken(t, handler.jwtMgr)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBufferString(`{"username":"x","password":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if !containsStr(rr.Body.String(), "worker_read_only") {
		t.Errorf("response body should contain 'worker_read_only', got: %s", rr.Body.String())
	}
}

// TestWorkerStatsHeadersSet 验证 Worker 节点统计端点自动附加
// X-Node-Role: worker 和 X-Stats-Scope: local 响应头（P3-2）。
func TestWorkerStatsHeadersSet(t *testing.T) {
	handler, mux := setupWorkerAdminTest(t)
	tok := adminToken(t, handler.jwtMgr)

	statsPaths := []string{
		"/api/admin/stats/summary",
		"/api/admin/stats/users",
		"/api/admin/stats/logs",
	}

	for _, path := range statsPaths {
		t.Run("GET "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("GET %s: status = %d, want 200", path, rr.Code)
			}
			if got := rr.Header().Get("X-Node-Role"); got != "worker" {
				t.Errorf("GET %s: X-Node-Role = %q, want \"worker\"", path, got)
			}
			if got := rr.Header().Get("X-Stats-Scope"); got != "local" {
				t.Errorf("GET %s: X-Stats-Scope = %q, want \"local\"", path, got)
			}
		})
	}
}

// TestPrimaryNodeStatsNoWorkerHeaders 验证 Primary 节点统计端点不附加 Worker 响应头。
func TestPrimaryNodeStatsNoWorkerHeaders(t *testing.T) {
	handler, _, mux := setupAdminTest(t, "") // Primary 模式（isWorkerNode=false）
	tok := adminToken(t, handler.jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/summary", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Node-Role"); got != "" {
		t.Errorf("Primary node should not set X-Node-Role, got %q", got)
	}
	if got := rr.Header().Get("X-Stats-Scope"); got != "" {
		t.Errorf("Primary node should not set X-Stats-Scope, got %q", got)
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

