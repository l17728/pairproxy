package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/router"
)

// setupSemanticRouteTest 创建语义路由 handler 测试环境
func setupSemanticRouteTest(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "sr-test-secret")
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

	repo := db.NewSemanticRouteRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	// 创建一个 SemanticRouter（空规则），用于热更新测试
	sr := router.NewSemanticRouter(logger, nil, nil, 3*time.Second, "")

	handler := NewAdminSemanticRouteHandler(
		logger, jwtMgr, repo, auditRepo, sr, "admin-hash", time.Hour,
	)

	// 构建 mux，使用简单的 admin middleware（跳过密码检查，只要有 admin token 即可）
	adminHandler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		auditRepo, "", time.Hour,
	)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, adminHandler.RequireAdmin, adminHandler.RequireWritableNode)

	tok, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}

	return mux, tok
}

func TestSemanticRoute_CreateAndList(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	// Create
	body := `{"name":"code-gen","description":"Generate code","target_urls":["http://llm-a:8080"],"priority":10}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Create status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var created semanticRouteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created.Name != "code-gen" {
		t.Errorf("Name = %q, want %q", created.Name, "code-gen")
	}
	if created.Priority != 10 {
		t.Errorf("Priority = %d, want 10", created.Priority)
	}
	if !created.IsActive {
		t.Error("expected IsActive=true")
	}
	if len(created.TargetURLs) != 1 || created.TargetURLs[0] != "http://llm-a:8080" {
		t.Errorf("TargetURLs = %v", created.TargetURLs)
	}

	// List
	req = httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("List status = %d, want 200", rr.Code)
	}
	var list []*semanticRouteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list count = %d, want 1", len(list))
	}
	if list[0].Name != "code-gen" {
		t.Errorf("list[0].Name = %q", list[0].Name)
	}
}

func TestSemanticRoute_GetByID(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	// Create a route first
	body := `{"name":"qa","description":"Question answering","target_urls":["http://qa:8080"],"priority":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var created semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Get by ID
	req = httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Get status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var got semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
}

func TestSemanticRoute_GetByID_NotFound(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("Get nonexistent status = %d, want 404", rr.Code)
	}
}

func TestSemanticRoute_Update(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	// Create
	body := `{"name":"update-me","description":"before","target_urls":["http://a:8080"],"priority":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var created semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Update
	updateBody := `{"description":"after","priority":99}`
	req = httptest.NewRequest(http.MethodPut, "/api/admin/semantic-routes/"+created.ID, bytes.NewBufferString(updateBody))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Update status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var updated semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &updated)
	if updated.Description != "after" {
		t.Errorf("Description = %q, want %q", updated.Description, "after")
	}
	if updated.Priority != 99 {
		t.Errorf("Priority = %d, want 99", updated.Priority)
	}
}

func TestSemanticRoute_Delete(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	// Create
	body := `{"name":"delete-me","description":"bye","target_urls":["http://a:8080"],"priority":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var created semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/semantic-routes/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("Delete status = %d, want 204", rr.Code)
	}

	// Verify gone
	req = httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("after delete, Get status = %d, want 404", rr.Code)
	}
}

func TestSemanticRoute_EnableDisable(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	// Create
	body := `{"name":"toggle","description":"test toggle","target_urls":["http://a:8080"],"priority":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var created semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Disable
	req = httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes/"+created.ID+"/disable", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("Disable status = %d, want 204", rr.Code)
	}

	// Verify disabled
	req = httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var got semanticRouteResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.IsActive {
		t.Error("expected IsActive=false after disable")
	}

	// Enable
	req = httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes/"+created.ID+"/enable", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("Enable status = %d, want 204", rr.Code)
	}

	// Verify enabled
	req = httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.IsActive {
		t.Error("expected IsActive=true after enable")
	}
}

func TestSemanticRoute_CreateValidation(t *testing.T) {
	mux, tok := setupSemanticRouteTest(t)

	tests := []struct {
		name string
		body string
	}{
		{"missing name", `{"description":"d","target_urls":["http://a:8080"]}`},
		{"missing description", `{"name":"n","target_urls":["http://a:8080"]}`},
		{"missing target_urls", `{"name":"n","description":"d"}`},
		{"empty target_urls", `{"name":"n","description":"d","target_urls":[]}`},
		{"invalid json", `not json`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/admin/semantic-routes", bytes.NewBufferString(tc.body))
			req.Header.Set("Authorization", "Bearer "+tok)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSemanticRoute_Unauthorized(t *testing.T) {
	mux, _ := setupSemanticRouteTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/semantic-routes", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without auth", rr.Code)
	}
}
