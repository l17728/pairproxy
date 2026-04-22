package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupLLMTargetHandler 创建测试用的 AdminLLMTargetHandler
func setupLLMTargetHandler(t *testing.T) (*AdminLLMTargetHandler, *gorm.DB, func()) {
	t.Helper()

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
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	handler := NewAdminLLMTargetHandler(logger, jwtMgr, llmTargetRepo, auditRepo, "admin-hash", 24*time.Hour)

	cleanup := func() {
		// No explicit close needed for in-memory DB
	}

	return handler, gormDB, cleanup
}

// TestListLLMTargets 测试列出所有 LLM targets
func TestListLLMTargets(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建测试数据
	logger := zaptest.NewLogger(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建 API Key
	apiKey := &db.APIKey{
		ID:             "test-key-id-1",
		Name:           "test-key-1",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	apiKeyID := apiKey.ID
	target1 := &db.LLMTarget{
		ID:              "target-1",
		URL:             "http://test1.local",
		APIKeyID:        &apiKeyID,
		Provider:        "anthropic",
		Name:            "Test 1",
		Weight:          1,
		HealthCheckPath: "/health",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target1); err != nil {
		t.Fatalf("failed to create target1: %v", err)
	}

	target2 := &db.LLMTarget{
		ID:              "target-2",
		URL:             "http://test2.local",
		APIKeyID:        &apiKeyID,
		Provider:        "openai",
		Name:            "Test 2",
		Weight:          2,
		HealthCheckPath: "",
		Source:          "config",
		IsEditable:      false,
		IsActive:        false,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target2); err != nil {
		t.Fatalf("failed to create target2: %v", err)
	}

	// 创建请求
	req := httptest.NewRequest("GET", "/api/admin/llm/targets", nil)
	w := httptest.NewRecorder()

	// 执行请求
	handler.handleListTargets(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Targets []map[string]interface{} `json:"targets"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(resp.Targets))
	}
}

// TestCreateLLMTarget 测试创建 LLM target
func TestCreateLLMTarget(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建 API Key
	apiKey := &db.APIKey{
		ID:             "test-key-create",
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	reqBody := map[string]interface{}{
		"url":               "http://new-target.local",
		"api_key_id":        apiKey.ID,
		"provider":          "anthropic",
		"name":              "New Target",
		"weight":            1,
		"health_check_path": "/health",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/admin/llm/targets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.handleCreateTarget(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp db.LLMTarget
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID == "" {
		t.Error("expected non-empty id in response")
	}
	if resp.URL != "http://new-target.local" {
		t.Errorf("expected url http://new-target.local, got %s", resp.URL)
	}
}

// TestCreateLLMTarget_DuplicateURL 测试创建重复 URL 的 target
func TestCreateLLMTarget_DuplicateURL(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)

	// 创建 API Key
	apiKey := &db.APIKey{
		ID:             "test-key-dup",
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	// 创建已存在的 target
	repo := db.NewLLMTargetRepo(gormDB, logger)
	existing := &db.LLMTarget{
		ID:         "existing-target",
		URL:        "http://existing.local",
		APIKeyID:   &apiKey.ID,
		Provider:   "anthropic",
		Name:       "Existing",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := repo.Create(existing); err != nil {
		t.Fatalf("failed to create existing target: %v", err)
	}

	// 尝试创建重复 URL
	reqBody := map[string]interface{}{
		"url":        "http://existing.local",
		"api_key_id": apiKey.ID,
		"provider":   "anthropic",
		"name":       "Duplicate",
		"weight":     1,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/admin/llm/targets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.handleCreateTarget(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

// TestGetLLMTarget 测试获取单个 LLM target
func TestGetLLMTarget(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)

	// 创建测试数据
	apiKey := &db.APIKey{
		ID:             "test-key-get",
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID:              "get-target-id",
		URL:             "http://test.local",
		APIKeyID:        &apiKey.ID,
		Provider:        "anthropic",
		Name:            "Test Target",
		Weight:          1,
		HealthCheckPath: "/health",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/admin/llm/targets/"+target.ID, nil)
	req.SetPathValue("id", target.ID)
	w := httptest.NewRecorder()

	handler.handleGetTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp db.LLMTarget
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != target.ID {
		t.Errorf("expected id %s, got %s", target.ID, resp.ID)
	}
	if resp.URL != target.URL {
		t.Errorf("expected url %s, got %s", target.URL, resp.URL)
	}
}

// TestUpdateLLMTarget 测试更新 LLM target
func TestUpdateLLMTarget(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	logger := zaptest.NewLogger(t)

	// 创建测试数据
	apiKey := &db.APIKey{
		ID:             "test-key-update",
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	target := &db.LLMTarget{
		ID:              "update-target-id",
		URL:             "http://test.local",
		APIKeyID:        &apiKey.ID,
		Provider:        "anthropic",
		Name:            "Old Name",
		Weight:          1,
		HealthCheckPath: "/health",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	// 更新请求
	reqBody := map[string]interface{}{
		"name":   "New Name",
		"weight": 2,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("PUT", "/api/admin/llm/targets/"+target.ID, bytes.NewReader(body))
	req.SetPathValue("id", target.ID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.handleUpdateTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证更新
	updated, err := repo.GetByID(target.ID)
	if err != nil {
		t.Fatalf("failed to get updated target: %v", err)
	}
	if updated.Name != "New Name" {
		t.Errorf("expected name 'New Name', got %s", updated.Name)
	}
	if updated.Weight != 2 {
		t.Errorf("expected weight 2, got %d", updated.Weight)
	}
}

// TestUpdateLLMTarget_ConfigSourced 测试通过 CLI（admin API）更新配置文件来源的 target 应成功。
// WebUI 禁止修改 config-sourced target，但 CLI 不受限制，允许运维人员强制覆盖。
func TestUpdateLLMTarget_ConfigSourced(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建配置文件来源的 target
	apiKeyID := "fixed-id-" + t.Name()
	apiKey := &db.APIKey{
		ID:             apiKeyID,
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	repo := db.NewLLMTargetRepo(gormDB, zaptest.NewLogger(t))
	target := &db.LLMTarget{
		ID:              "fixed-id-" + t.Name(),
		URL:             "http://config.local",
		APIKeyID:        &apiKeyID,
		Provider:        "anthropic",
		Name:            "Config Target",
		Weight:          1,
		HealthCheckPath: "",
		Source:          "config",
		IsEditable:      false,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	// GORM gotcha: boolean false 需要显式更新
	if err := gormDB.Model(&db.LLMTarget{}).Where("id = ?", target.ID).
		Update("is_editable", false).Error; err != nil {
		t.Fatalf("failed to set is_editable=false: %v", err)
	}

	// CLI（admin API）应允许更新 config-sourced target
	reqBody := map[string]interface{}{
		"name": "New Name",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("PUT", "/api/admin/llm/targets/"+target.ID, bytes.NewReader(body))
	req.SetPathValue("id", target.ID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.handleUpdateTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 (CLI may update config-sourced targets), got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteLLMTarget 测试删除 LLM target
func TestDeleteLLMTarget(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建测试数据
	apiKeyID := "fixed-id-" + t.Name()
	apiKey := &db.APIKey{
		ID:             apiKeyID,
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	repo := db.NewLLMTargetRepo(gormDB, zaptest.NewLogger(t))
	target := &db.LLMTarget{
		ID:              "fixed-id-" + t.Name(),
		URL:             "http://test.local",
		APIKeyID:        &apiKeyID,
		Provider:        "anthropic",
		Name:            "Test Target",
		Weight:          1,
		HealthCheckPath: "",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/admin/llm/targets/"+target.ID, nil)
	req.SetPathValue("id", target.ID)
	w := httptest.NewRecorder()

	handler.handleDeleteTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证删除
	_, err := repo.GetByID(target.ID)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("expected record not found, got %v", err)
	}
}

// TestEnableDisableLLMTarget 测试启用/禁用 LLM target
func TestEnableDisableLLMTarget(t *testing.T) {
	handler, gormDB, cleanup := setupLLMTargetHandler(t)
	defer cleanup()

	// 创建测试数据
	apiKeyID := "fixed-id-" + t.Name()
	apiKey := &db.APIKey{
		ID:             apiKeyID,
		Name:           "test-key",
		EncryptedValue: "encrypted",
		Provider:       "anthropic",
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	repo := db.NewLLMTargetRepo(gormDB, zaptest.NewLogger(t))
	target := &db.LLMTarget{
		ID:              "fixed-id-" + t.Name(),
		URL:             "http://test.local",
		APIKeyID:        &apiKeyID,
		Provider:        "anthropic",
		Name:            "Test Target",
		Weight:          1,
		HealthCheckPath: "",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	// 测试禁用
	req := httptest.NewRequest("POST", "/api/admin/llm/targets/"+target.ID+"/disable", nil)
	req.SetPathValue("id", target.ID)
	w := httptest.NewRecorder()

	handler.handleDisableTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证禁用
	updated, err := repo.GetByID(target.ID)
	if err != nil {
		t.Fatalf("failed to get updated target: %v", err)
	}
	if updated.IsActive {
		t.Error("expected target to be disabled")
	}

	// 测试启用
	req = httptest.NewRequest("POST", "/api/admin/llm/targets/"+target.ID+"/enable", nil)
	req.SetPathValue("id", target.ID)
	w = httptest.NewRecorder()

	handler.handleEnableTarget(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证启用
	updated, err = repo.GetByID(target.ID)
	if err != nil {
		t.Fatalf("failed to get updated target: %v", err)
	}
	if !updated.IsActive {
		t.Error("expected target to be enabled")
	}
}
