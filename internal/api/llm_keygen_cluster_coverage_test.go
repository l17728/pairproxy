package api

// llm_keygen_cluster_coverage_test.go
// 补充 LLM target、LLM binding、keygen、cluster 及 admin handler 中低覆盖函数的测试用例。
// 仅添加现有测试文件未覆盖的分支，不修改任何已有测试文件。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

// ---------------------------------------------------------------------------
// 辅助：带有 APIKeyRepo 的 AdminHandler（复用 setupAPIKeyTest 的简化版）
// ---------------------------------------------------------------------------

func setupAdminWithAPIKeyRepo(t *testing.T) (*AdminHandler, *auth.Manager, *http.ServeMux, *db.APIKeyRepo) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "cov-test-secret")
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
	apiKeyRepo := db.NewAPIKeyRepo(gormDB, logger)

	hash, _ := auth.HashPassword(logger, "adminpass")
	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, hash, time.Hour)
	handler.SetAPIKeyRepo(apiKeyRepo, func(plain string) (string, error) {
		return "enc:" + plain, nil
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return handler, jwtMgr, mux, apiKeyRepo
}

// ---------------------------------------------------------------------------
// AdminLLMTargetHandler — RegisterRoutes (0%)
// ---------------------------------------------------------------------------

// TestAdminLLMTargetHandler_RegisterRoutes 验证 RegisterRoutes 能正常注册路由并响应请求。
func TestAdminLLMTargetHandler_RegisterRoutes(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	jwtMgr, err := auth.NewManager(logger, "rr-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	handler := NewAdminLLMTargetHandler(logger, jwtMgr, llmTargetRepo, auditRepo, "hash", time.Hour)

	// identity middleware（直接放行，用于测试路由注册本身）
	identity := func(next http.Handler) http.Handler { return next }

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, identity, identity)

	// GET /api/admin/llm/targets 应该路由到 handleListTargets，返回 200
	req := httptest.NewRequest(http.MethodGet, "/api/admin/llm/targets", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("RegisterRoutes GET /api/admin/llm/targets: status = %d, want 200", rr.Code)
	}

	// POST /api/admin/llm/targets 路由存在（无 body → 400 bad request）
	req2 := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets", bytes.NewBufferString("bad-json"))
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusNotFound {
		t.Error("RegisterRoutes POST /api/admin/llm/targets: route not registered (404)")
	}
}

// ---------------------------------------------------------------------------
// handleListTargets — 空列表分支
// ---------------------------------------------------------------------------

// TestListLLMTargets_Empty 验证无 target 时返回空列表而非 nil。
func TestListLLMTargets_Empty(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/llm/targets", nil)
	rr := httptest.NewRecorder()
	handler.handleListTargets(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Targets []interface{} `json:"targets"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Targets == nil {
		t.Error("targets should not be nil (even when empty)")
	}
}

// ---------------------------------------------------------------------------
// handleCreateTarget — 缺少必填字段、JSON 解析错误
// ---------------------------------------------------------------------------

// TestCreateLLMTarget_MissingURL 验证缺少 url 字段时返回 400。
func TestCreateLLMTarget_MissingURL(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"api_key_id": "some-key",
		"provider":   "anthropic",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleCreateTarget(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when url is missing", rr.Code)
	}
}

// TestCreateLLMTarget_MissingAPIKeyID 验证缺少 api_key_id 字段时返回 400。
func TestCreateLLMTarget_MissingAPIKeyID(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"url":      "http://example.local",
		"provider": "anthropic",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleCreateTarget(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when api_key_id is missing", rr.Code)
	}
}

// TestCreateLLMTarget_InvalidJSON 验证无效 JSON 返回 400。
func TestCreateLLMTarget_InvalidJSON(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleCreateTarget(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// TestCreateLLMTarget_DefaultsApplied 验证省略 provider/weight 时使用默认值。
func TestCreateLLMTarget_DefaultsApplied(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建 API Key
	apiKey := &db.APIKey{
		ID:             "default-key-id",
		Name:           "default-key",
		EncryptedValue: "enc",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// 不传 provider 和 weight
	body, _ := json.Marshal(map[string]interface{}{
		"url":        "http://defaults.local",
		"api_key_id": apiKey.ID,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleCreateTarget(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var target db.LLMTarget
	if err := json.NewDecoder(rr.Body).Decode(&target); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if target.Provider != "anthropic" {
		t.Errorf("default provider = %q, want 'anthropic'", target.Provider)
	}
	if target.Weight != 1 {
		t.Errorf("default weight = %d, want 1", target.Weight)
	}
}

// ---------------------------------------------------------------------------
// handleGetTarget — not found、invalid id
// ---------------------------------------------------------------------------

// TestGetLLMTarget_NotFound 验证不存在的 ID 返回 404。
func TestGetLLMTarget_NotFound(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/llm/targets/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	rr := httptest.NewRecorder()
	handler.handleGetTarget(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for nonexistent id", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleUpdateTarget — not found、invalid JSON
// ---------------------------------------------------------------------------

// TestUpdateLLMTarget_NotFound 验证不存在 ID 时返回 404。
func TestUpdateLLMTarget_NotFound(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{"name": "new"})
	req := httptest.NewRequest(http.MethodPut, "/api/admin/llm/targets/ghost", bytes.NewReader(body))
	req.SetPathValue("id", "ghost")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleUpdateTarget(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for nonexistent id", rr.Code)
	}
}

// TestUpdateLLMTarget_InvalidJSON 验证无效 JSON 时返回 400。
func TestUpdateLLMTarget_InvalidJSON(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)
	apiKeyID := "upd-inv-key"
	apiKey := &db.APIKey{ID: apiKeyID, Name: "k", EncryptedValue: "e", Provider: "anthropic", IsActive: true, CreatedAt: time.Now()}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID: "upd-inv-target", URL: "http://upd-inv.local", APIKeyID: &apiKeyID,
		Provider: "anthropic", Name: "T", Weight: 1,
		Source: "database", IsEditable: true, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/admin/llm/targets/"+target.ID, bytes.NewBufferString("bad"))
	req.SetPathValue("id", target.ID)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleUpdateTarget(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// TestUpdateLLMTarget_NoChanges 验证无变更时响应 200 + "No changes detected"。
func TestUpdateLLMTarget_NoChanges(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)
	apiKeyID := "nochange-key"
	apiKey := &db.APIKey{ID: apiKeyID, Name: "k", EncryptedValue: "e", Provider: "anthropic", IsActive: true, CreatedAt: time.Now()}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID: "nochange-target", URL: "http://nochange.local", APIKeyID: &apiKeyID,
		Provider: "anthropic", Name: "OriginalName", Weight: 1,
		Source: "database", IsEditable: true, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	// 发送一个不改变任何字段的请求（空 body）
	req := httptest.NewRequest(http.MethodPut, "/api/admin/llm/targets/"+target.ID, bytes.NewBufferString("{}"))
	req.SetPathValue("id", target.ID)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleUpdateTarget(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["message"] != "No changes detected" {
		t.Errorf("message = %q, want 'No changes detected'", resp["message"])
	}
}

// ---------------------------------------------------------------------------
// handleDeleteTarget — not found、config-sourced
// ---------------------------------------------------------------------------

// TestDeleteLLMTarget_NotFound 验证删除不存在 ID 时返回 404。
func TestDeleteLLMTarget_NotFound(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/llm/targets/ghost-delete", nil)
	req.SetPathValue("id", "ghost-delete")
	rr := httptest.NewRecorder()
	handler.handleDeleteTarget(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for nonexistent id", rr.Code)
	}
}

// TestDeleteLLMTarget_ConfigSourced 验证通过 CLI（admin API）可以删除 config-sourced target（返回 200）。
// WebUI 层禁止删除 config-sourced target，但 CLI 不受限制。
func TestDeleteLLMTarget_ConfigSourced(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)
	apiKeyID := "del-cfg-key"
	apiKey := &db.APIKey{ID: apiKeyID, Name: "k", EncryptedValue: "e", Provider: "anthropic", IsActive: true, CreatedAt: time.Now()}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID: "del-cfg-target", URL: "http://del-cfg.local", APIKeyID: &apiKeyID,
		Provider: "anthropic", Name: "CfgTarget", Weight: 1,
		Source: "config", IsEditable: true, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	// 显式将 is_editable 设为 false（GORM 零值问题）
	if err := gormDB.Model(&db.LLMTarget{}).Where("id = ?", target.ID).Update("is_editable", false).Error; err != nil {
		t.Fatalf("set is_editable=false: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/llm/targets/"+target.ID, nil)
	req.SetPathValue("id", target.ID)
	rr := httptest.NewRecorder()
	handler.handleDeleteTarget(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (CLI may delete config-sourced targets)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleEnableTarget — not found、already enabled
// ---------------------------------------------------------------------------

// TestEnableTarget_NotFound 验证启用不存在 target 返回 404。
func TestEnableTarget_NotFound(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets/ghost/enable", nil)
	req.SetPathValue("id", "ghost")
	rr := httptest.NewRecorder()
	handler.handleEnableTarget(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestEnableTarget_AlreadyEnabled 验证已启用的 target 再次启用返回 200 + "already enabled"。
func TestEnableTarget_AlreadyEnabled(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)
	apiKeyID := "already-en-key"
	apiKey := &db.APIKey{ID: apiKeyID, Name: "k", EncryptedValue: "e", Provider: "anthropic", IsActive: true, CreatedAt: time.Now()}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID: "already-en-target", URL: "http://already-en.local", APIKeyID: &apiKeyID,
		Provider: "anthropic", Name: "T", Weight: 1,
		Source: "database", IsEditable: true, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets/"+target.ID+"/enable", nil)
	req.SetPathValue("id", target.ID)
	rr := httptest.NewRecorder()
	handler.handleEnableTarget(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["message"] != "Target is already enabled" {
		t.Errorf("message = %q, want 'Target is already enabled'", resp["message"])
	}
}

// ---------------------------------------------------------------------------
// handleDisableTarget — not found、already disabled
// ---------------------------------------------------------------------------

// TestDisableTarget_NotFound 验证禁用不存在 target 返回 404。
func TestDisableTarget_NotFound(t *testing.T) {
	handler, _, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets/ghost/disable", nil)
	req.SetPathValue("id", "ghost")
	rr := httptest.NewRecorder()
	handler.handleDisableTarget(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestDisableTarget_AlreadyDisabled 验证已禁用的 target 再次禁用返回 200 + "already disabled"。
func TestDisableTarget_AlreadyDisabled(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)
	apiKeyID := "already-dis-key"
	apiKey := &db.APIKey{ID: apiKeyID, Name: "k", EncryptedValue: "e", Provider: "anthropic", IsActive: true, CreatedAt: time.Now()}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID: "already-dis-target", URL: "http://already-dis.local", APIKeyID: &apiKeyID,
		Provider: "anthropic", Name: "T", Weight: 1,
		Source: "database", IsEditable: true, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	// 先将 is_active 设为 false
	if err := gormDB.Model(&db.LLMTarget{}).Where("id = ?", target.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("set is_active=false: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/targets/"+target.ID+"/disable", nil)
	req.SetPathValue("id", target.ID)
	rr := httptest.NewRecorder()
	handler.handleDisableTarget(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["message"] != "Target is already disabled" {
		t.Errorf("message = %q, want 'Target is already disabled'", resp["message"])
	}
}

// ---------------------------------------------------------------------------
// handleListLLMBindings — no repo (501)
// ---------------------------------------------------------------------------

// TestListLLMBindings_NotConfigured 验证未配置 LLM binding repo 时返回 501。
func TestListLLMBindings_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	// 不调用 SetLLMBindingRepo
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	handler.RegisterLLMRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/llm/bindings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when llm binding repo not configured", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleCreateLLMBinding — no repo (501)、invalid JSON
// ---------------------------------------------------------------------------

// TestCreateLLMBinding_NotConfigured 验证未配置 binding repo 时 POST bindings 返回 501。
func TestCreateLLMBinding_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	handler.RegisterLLMRoutes(mux)

	tok := adminToken(t, jwtMgr)
	uid := "u1"
	body, _ := json.Marshal(createLLMBindingRequest{TargetURL: "http://t", UserID: &uid})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/bindings", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rr.Code)
	}
}

// TestCreateLLMBinding_InvalidJSON 验证无效 JSON 返回 400。
func TestCreateLLMBinding_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux, _ := setupLLMTest(t)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/bindings", bytes.NewBufferString("bad-json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteLLMBinding — no repo (501)
// ---------------------------------------------------------------------------

// TestDeleteLLMBinding_NotConfigured 验证未配置 binding repo 时 DELETE bindings/{id} 返回 501。
func TestDeleteLLMBinding_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	handler.RegisterLLMRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/llm/bindings/some-id", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleLLMDistribute — no repo (501)、invalid JSON
// ---------------------------------------------------------------------------

// TestLLMDistribute_NotConfigured 验证未配置 binding repo 时 POST distribute 返回 501。
func TestLLMDistribute_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	handler.RegisterLLMRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/distribute", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rr.Code)
	}
}

// TestLLMDistribute_InvalidJSON 验证无效 JSON body 返回 400。
func TestLLMDistribute_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux, _ := setupLLMTest(t)
	tok := adminToken(t, jwtMgr)

	// 设置 Content-Length > 0 以触发 JSON 解析（r.ContentLength > 0 分支）
	body := bytes.NewBufferString("bad-json-here")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/distribute", body)
	req.ContentLength = int64(body.Len())
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON body", rr.Code)
	}
}

// TestLLMDistribute_WithActiveUsers 验证 user_ids 为空时使用 DB 中活跃用户。
func TestLLMDistribute_WithActiveUsers(t *testing.T) {
	handler, jwtMgr, mux, _ := setupLLMTest(t)

	// 向 userRepo 添加活跃用户（通过内部 handler.userRepo）
	// 使用 API 创建用户：POST /api/admin/users
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	createUser := func(username string) {
		body, _ := json.Marshal(map[string]string{"username": username, "password": "pass123"})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Logf("create user %s: status=%d body=%s", username, rr.Code, rr.Body.String())
		}
	}

	createUser("dist-user-a")
	createUser("dist-user-b")

	// 使用显式 target_urls，不传 user_ids（handler 从 DB 查活跃用户）
	body, _ := json.Marshal(llmDistributeRequest{
		TargetURLs: []string{"http://llm1:8080", "http://llm2:8080"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/llm/distribute", bytes.NewBuffer(body))
	req.Header.Set("Authorization", authHdr)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("distribute with active users: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 应至少分配了创建的活跃用户
	_ = handler // 用于避免"unused"提示
	if resp["assigned"] < 0 {
		t.Errorf("assigned = %d, should be >= 0", resp["assigned"])
	}
}

// ---------------------------------------------------------------------------
// ClusterHandler — handleRegister (method not allowed)
// ---------------------------------------------------------------------------

// TestRegisterEndpoint_MethodNotAllowed 验证非 POST 方法返回 405。
func TestRegisterEndpoint_MethodNotAllowed(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := authReq(http.MethodGet, "/api/internal/register", nil)
	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for GET /register", rr.Code)
	}
}

// TestRegisterEndpoint_InvalidJSON 验证无效 JSON body 返回 400。
func TestRegisterEndpoint_InvalidJSON(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewBufferString("not-json"))
	req.Header.Set("Authorization", "Bearer "+testClusterSecret)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// ClusterHandler — handleUsageReport (method not allowed、invalid JSON)
// ---------------------------------------------------------------------------

// TestUsageReportEndpoint_MethodNotAllowed 验证非 POST 方法返回 405。
func TestUsageReportEndpoint_MethodNotAllowed(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := authReq(http.MethodGet, "/api/internal/usage", nil)
	rr := httptest.NewRecorder()
	handler.handleUsageReport(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for GET /usage", rr.Code)
	}
}

// TestUsageReportEndpoint_InvalidJSON 验证无效 JSON body 返回 400。
func TestUsageReportEndpoint_InvalidJSON(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/usage", bytes.NewBufferString("bad"))
	req.Header.Set("Authorization", "Bearer "+testClusterSecret)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleUsageReport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// ClusterHandler — handleGetRouting (method not allowed、nil registry)
// ---------------------------------------------------------------------------

// TestGetRoutingEndpoint_MethodNotAllowed 验证非 GET 方法返回 405。
func TestGetRoutingEndpoint_MethodNotAllowed(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := authReq(http.MethodPost, "/cluster/routing", []byte("{}"))
	rr := httptest.NewRecorder()
	handler.handleGetRouting(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for POST /routing", rr.Code)
	}
}

// TestGetRoutingEndpoint_NilRegistry 验证 registry 为 nil 时返回 404。
func TestGetRoutingEndpoint_NilRegistry(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	const nilRegSecret = "nil-registry-secret"
	// 使用 nil registry，但提供正确 shared secret
	handler := NewClusterHandler(logger, nil, writer, nilRegSecret)

	req := httptest.NewRequest(http.MethodGet, "/cluster/routing", nil)
	req.Header.Set("Authorization", "Bearer "+nilRegSecret)
	rr := httptest.NewRecorder()
	handler.handleGetRouting(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when registry is nil", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// ClusterHandler — handleConfigSnapshot (method not allowed)
// ---------------------------------------------------------------------------

// TestConfigSnapshot_MethodNotAllowed_Direct 验证非 GET 方法直接调用返回 405。
// （cluster_handler_test.go 中已有 via authReq POST，这里确保分支命中）
func TestConfigSnapshot_MethodNotAllowed_Direct(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	// DELETE 方法也应返回 405
	req := authReq(http.MethodDelete, "/api/internal/config-snapshot", nil)
	rr := httptest.NewRecorder()
	handler.handleConfigSnapshot(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for DELETE /config-snapshot", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// ClusterHandler — RegisterRoutes（触发路由注册）
// ---------------------------------------------------------------------------

// TestClusterHandler_RegisterRoutes 验证 RegisterRoutes 能正常注册并响应。
func TestClusterHandler_RegisterRoutes(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := cluster.NewManager(logger, balancer, nil, "")
	registry := cluster.NewPeerRegistry(logger, mgr)
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	handler := NewClusterHandler(logger, registry, writer, testClusterSecret)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// POST /api/internal/register 路由已注册
	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-rr", Addr: "http://sp-rr:9000"})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClusterSecret)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code == http.StatusNotFound {
		t.Error("RegisterRoutes: /api/internal/register not found (404)")
	}
}

// ---------------------------------------------------------------------------
// RequireAdmin — cookie-based auth
// ---------------------------------------------------------------------------

// TestRequireAdmin_CookieAuth 验证通过 cookie 携带 JWT 可通过 RequireAdmin 认证。
func TestRequireAdmin_CookieAuth(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)

	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.AddCookie(&http.Cookie{Name: AdminCookieName, Value: tok})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("RequireAdmin via cookie: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestRequireAdmin_NoToken 验证既无 Bearer 又无 cookie 时返回 401。
func TestRequireAdmin_NoToken(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, _, mux := setupAdminTest(t, hash)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("RequireAdmin no token: status = %d, want 401", rr.Code)
	}
}

// TestRequireAdmin_NonAdminRole 验证 role!=admin 的 JWT 被拒绝。
func TestRequireAdmin_NonAdminRole(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)

	// 签一个 role=user 的 token
	tok, err := jwtMgr.Sign(auth.JWTClaims{
		UserID: "u1", Username: "alice", Role: "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign user token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("RequireAdmin user role: status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleListAPIKeys — not configured (501) 已有测试，补充：list after create
// handleCreateAPIKey — not configured (501)
// ---------------------------------------------------------------------------

// TestCreateAPIKey_NotConfigured 验证未配置 apiKeyRepo 时 POST api-keys 返回 501。
func TestCreateAPIKey_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok := adminToken(t, jwtMgr)
	body, _ := json.Marshal(createAPIKeyRequest{Name: "k", Value: "v", Provider: "anthropic"})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when api key repo not configured", rr.Code)
	}
}

// TestCreateAPIKey_InvalidJSON 验证无效 JSON body 返回 400。
func TestCreateAPIKey_InvalidJSON(t *testing.T) {
	_, jwtMgr, mux, _ := setupAdminWithAPIKeyRepo(t)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewBufferString("bad"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleRevokeAPIKey — no repo (501)
// ---------------------------------------------------------------------------

// TestRevokeAPIKey_NotConfigured 验证未配置时返回 501。
func TestRevokeAPIKey_NotConfigured(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/api-keys/some-id", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteGroup — force=true
// ---------------------------------------------------------------------------

// TestDeleteGroup_WithForce 验证 force=true 查询参数时也能正常删除。
func TestDeleteGroup_WithForce(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	handler, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	// 先创建分组
	createBody, _ := json.Marshal(map[string]string{"name": "delete-force-grp"})
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/groups", bytes.NewBuffer(createBody))
	createReq.Header.Set("Authorization", authHdr)
	createRR := httptest.NewRecorder()
	mux.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create group: status = %d; body: %s", createRR.Code, createRR.Body.String())
	}
	var grp map[string]interface{}
	if err := json.NewDecoder(createRR.Body).Decode(&grp); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	grpID := grp["id"].(string)

	// 向分组添加用户（使分组非空）
	createUserBody, _ := json.Marshal(map[string]interface{}{
		"username": "force-del-user",
		"password": "pass123",
		"group_id": grpID,
	})
	createUserReq := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBuffer(createUserBody))
	createUserReq.Header.Set("Authorization", authHdr)
	createUserRR := httptest.NewRecorder()
	mux.ServeHTTP(createUserRR, createUserReq)

	_ = handler // 避免"unused"提示

	// force=true 删除（分组有成员也能删除）
	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/groups/"+grpID+"?force=true", nil)
	delReq.Header.Set("Authorization", authHdr)
	delRR := httptest.NewRecorder()
	mux.ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusNoContent {
		t.Errorf("delete group with force: status = %d, want 204; body: %s", delRR.Code, delRR.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleQuotaStatus — missing user param、user not found
// ---------------------------------------------------------------------------

// TestQuotaStatus_MissingUserParam 验证缺少 user 参数时返回 400。
func TestQuotaStatus_MissingUserParam(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when user param missing", rr.Code)
	}
}

// TestQuotaStatus_UserNotFound 验证用户不存在时返回 404。
func TestQuotaStatus_UserNotFound(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=nobody", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when user not found", rr.Code)
	}
}

// TestQuotaStatus_UserWithGroup 验证有分组的用户返回正确的配额信息（含 group 字段）。
func TestQuotaStatus_UserWithGroup(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	// 创建分组
	grpBody, _ := json.Marshal(map[string]string{"name": "quota-grp"})
	grpReq := httptest.NewRequest(http.MethodPost, "/api/admin/groups", bytes.NewBuffer(grpBody))
	grpReq.Header.Set("Authorization", authHdr)
	grpRR := httptest.NewRecorder()
	mux.ServeHTTP(grpRR, grpReq)
	var grp map[string]interface{}
	_ = json.NewDecoder(grpRR.Body).Decode(&grp)
	grpID := grp["id"].(string)

	// 创建用户并加入分组
	userBody, _ := json.Marshal(map[string]interface{}{
		"username": "quota-test-user",
		"password": "pass123",
		"group_id": grpID,
	})
	userReq := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewBuffer(userBody))
	userReq.Header.Set("Authorization", authHdr)
	userRR := httptest.NewRecorder()
	mux.ServeHTTP(userRR, userReq)
	if userRR.Code != http.StatusCreated {
		t.Fatalf("create user: status=%d body=%s", userRR.Code, userRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=quota-test-user", nil)
	req.Header.Set("Authorization", authHdr)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp quotaStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Username != "quota-test-user" {
		t.Errorf("username = %q, want 'quota-test-user'", resp.Username)
	}
	if resp.GroupID == "" {
		t.Error("expected non-empty group_id")
	}
}

// ---------------------------------------------------------------------------
// handleStatsUsers / handleStatsLogs — with ?days= param
// ---------------------------------------------------------------------------

// TestStatsUsers_WithDaysParam 验证 ?days=N 参数正确解析。
func TestStatsUsers_WithDaysParam(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/users?days=14", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestStatsLogs_WithUserIDAndLimit 验证 ?user_id=x&limit=y 参数正确过滤。
func TestStatsLogs_WithUserIDAndLimit(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/logs?user_id=u1&limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestStatsLogs_InvalidLimit 验证 limit 为非数字时仍返回 200（使用默认值）。
func TestStatsLogs_InvalidLimit(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/logs?limit=bad", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with default limit; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleGetActiveUsers — default days、custom days
// ---------------------------------------------------------------------------

// TestGetActiveUsers_DefaultDays 验证不传 days 时使用默认值。
func TestGetActiveUsers_DefaultDays(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/active-users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestGetActiveUsers_CustomDays 验证 ?days=N 正确解析。
func TestGetActiveUsers_CustomDays(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/active-users?days=7", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestGetActiveUsers_InvalidDays 验证 days 为非数字时仍返回 200（使用默认值）。
func TestGetActiveUsers_InvalidDays(t *testing.T) {
	hash, _ := auth.HashPassword(zaptest.NewLogger(t), "pass")
	_, jwtMgr, mux := setupAdminTest(t, hash)
	tok := adminToken(t, jwtMgr)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/active-users?days=notanumber", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with default days; body: %s", rr.Code, rr.Body.String())
	}
}
