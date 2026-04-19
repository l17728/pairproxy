package e2e_test

// llm_target_management_e2e_test.go — LLM Target 管理 E2E 测试
//
// 测试场景:
//   1. 配置文件同步（启动时）
//   2. CLI 添加/更新/删除数据库 target
//   3. CLI 拒绝修改配置文件来源的 target
//   4. URL 冲突检查
//   5. 配置文件变更后重启同步
//   6. WebUI 添加/更新/删除 target

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// Test Fixtures
// ---------------------------------------------------------------------------

// setupTestDB 创建测试数据库
func setupTestDB(t *testing.T) (*gorm.DB, *zap.Logger) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return gormDB, logger
}

// createTestAPIKey 创建测试 API Key
func createTestAPIKey(t *testing.T, gormDB *gorm.DB, logger *zap.Logger, id, name, provider string) *db.APIKey {
	apiKey := &db.APIKey{
		ID:             id,
		Name:           name,
		EncryptedValue: "encrypted-test-key",
		Provider:       provider,
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := gormDB.Create(apiKey).Error; err != nil {
		t.Fatalf("Create API key: %v", err)
	}
	return apiKey
}

// createTestAdmin 创建测试管理员
func createTestAdmin(t *testing.T, gormDB *gorm.DB, logger *zap.Logger) *db.User {
	passwordHash, err := auth.HashPassword(logger, "admin123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	admin := &db.User{
		ID:           "admin-id",
		Username:     "admin",
		PasswordHash: passwordHash,
		IsActive:     true,
		CreatedAt:    time.Now(),
	}
	if err := gormDB.Create(admin).Error; err != nil {
		t.Fatalf("Create admin: %v", err)
	}

	return admin
}

// ---------------------------------------------------------------------------
// Scenario 1: Config File Sync on Startup
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_ConfigFileSyncOnStartup(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "anthropic")

	// 模拟配置文件中的 targets
	configTargets := []config.LLMTarget{
		{
			URL:      "https://api.anthropic.com",
			APIKey:   "sk-ant-test",
			Provider: "anthropic",
			Name:     "Anthropic Official",
			Weight:   1,
		},
		{
			URL:      "http://ollama.local:11434",
			APIKey:   "ollama",
			Provider: "ollama",
			Name:     "Local Ollama",
			Weight:   2,
		},
	}

	// 同步配置文件 targets 到数据库
	for _, ct := range configTargets {
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        ct.URL,
			APIKeyID:   &apiKey.ID,
			Provider:   ct.Provider,
			Name:       ct.Name,
			Weight:     ct.Weight,
			Source:     "config",
			IsEditable: false,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := repo.Upsert(target); err != nil {
			t.Fatalf("Upsert config target: %v", err)
		}
	}

	// 验证数据库中的 targets
	targets, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	if len(targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(targets))
	}

	for _, target := range targets {
		if target.Source != "config" {
			t.Errorf("Expected source=config, got %s", target.Source)
		}
		if target.IsEditable {
			t.Error("Config-sourced target should not be editable")
		}
	}

	t.Log("Config file sync on startup: PASS")
}

// ---------------------------------------------------------------------------
// Scenario 2: CLI Add/Update/Delete Database Target
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_CLIAddUpdateDelete(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "ollama")

	// 1. CLI Add
	newTarget := &db.LLMTarget{
		ID:              uuid.NewString(),
		URL:             "http://localhost:11434",
		APIKeyID:        &apiKey.ID,
		Provider:        "ollama",
		Name:            "Local Ollama",
		Weight:          1,
		HealthCheckPath: "/health",
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := repo.Create(newTarget); err != nil {
		t.Fatalf("Create target: %v", err)
	}

	// 验证创建
	created, err := repo.GetByURL(newTarget.URL)
	if err != nil {
		t.Fatalf("GetByURL: %v", err)
	}
	if created.Source != "database" {
		t.Errorf("Expected source=database, got %s", created.Source)
	}
	if !created.IsEditable {
		t.Error("Database-sourced target should be editable")
	}

	// 2. CLI Update
	created.Name = "Updated Ollama"
	created.Weight = 2
	created.UpdatedAt = time.Now()

	if err := repo.Update(created); err != nil {
		t.Fatalf("Update target: %v", err)
	}

	// 验证更新
	updated, err := repo.GetByURL(created.URL)
	if err != nil {
		t.Fatalf("GetByURL after update: %v", err)
	}
	if updated.Name != "Updated Ollama" {
		t.Errorf("Expected name='Updated Ollama', got %s", updated.Name)
	}
	if updated.Weight != 2 {
		t.Errorf("Expected weight=2, got %d", updated.Weight)
	}

	// 3. CLI Delete
	if err := repo.Delete(created.ID); err != nil {
		t.Fatalf("Delete target: %v", err)
	}

	// 验证删除
	_, err = repo.GetByURL(created.URL)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("Expected ErrRecordNotFound, got %v", err)
	}

	t.Log("CLI add/update/delete: PASS")
}

// ---------------------------------------------------------------------------
// Scenario 3: CLI Reject Modifying Config-Sourced Target
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_CLIRejectConfigSourced(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "anthropic")

	// 创建配置文件来源的 target
	configTarget := &db.LLMTarget{
		ID:         uuid.NewString(),
		URL:        "https://api.anthropic.com",
		APIKeyID:   &apiKey.ID,
		Provider:   "anthropic",
		Name:       "Anthropic Official",
		Weight:     1,
		Source:     "config",
		IsEditable: false,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := repo.Upsert(configTarget); err != nil {
		t.Fatalf("Upsert config target: %v", err)
	}

	// CLI 可以修改和删除 config-sourced target（WebUI 层才拦截）
	target, err := repo.GetByURL(configTarget.URL)
	if err != nil {
		t.Fatalf("GetByURL: %v", err)
	}
	target.Name = "Modified via CLI"
	err = repo.Update(target)
	if err != nil {
		t.Errorf("Expected CLI to be able to update config-sourced target, got: %v", err)
	}

	// 删除也应该成功
	err = repo.Delete(target.ID)
	if err != nil {
		t.Errorf("Expected CLI to be able to delete config-sourced target, got: %v", err)
	}

	t.Log("CLI allow config-sourced modification: PASS")
}

// ---------------------------------------------------------------------------
// Scenario 4: URL Conflict Check
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_URLConflictCheck(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "ollama")

	// 创建第一个 target
	target1 := &db.LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://localhost:11434",
		APIKeyID:   &apiKey.ID,
		Provider:   "ollama",
		Name:       "Ollama 1",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := repo.Create(target1); err != nil {
		t.Fatalf("Create target1: %v", err)
	}

	// 尝试创建相同 URL 的 target（应该失败）
	target2 := &db.LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://localhost:11434", // 相同 URL
		APIKeyID:   &apiKey.ID,
		Provider:   "ollama",
		Name:       "Ollama 2",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err := repo.Create(target2)
	if err == nil {
		t.Error("Expected error when creating duplicate URL, got nil")
	}

	// 验证 URL 存在性检查
	exists, err := repo.URLExists("http://localhost:11434")
	if err != nil {
		t.Fatalf("URLExists: %v", err)
	}
	if !exists {
		t.Error("Expected URL to exist")
	}

	exists, err = repo.URLExists("http://nonexistent:11434")
	if err != nil {
		t.Fatalf("URLExists: %v", err)
	}
	if exists {
		t.Error("Expected URL to not exist")
	}

	t.Log("URL conflict check: PASS")
}

// ---------------------------------------------------------------------------
// Scenario 5: Config File Change and Restart Sync
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_ConfigChangeRestartSync(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "anthropic")

	// 初始配置文件 targets
	initialTargets := []string{
		"https://api.anthropic.com",
		"http://ollama1.local:11434",
	}

	for _, url := range initialTargets {
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        url,
			APIKeyID:   &apiKey.ID,
			Provider:   "anthropic",
			Name:       "Target " + url,
			Weight:     1,
			Source:     "config",
			IsEditable: false,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := repo.Upsert(target); err != nil {
			t.Fatalf("Upsert initial target: %v", err)
		}
	}

	// 验证初始状态
	targets, _ := repo.ListAll()
	if len(targets) != 2 {
		t.Errorf("Expected 2 initial targets, got %d", len(targets))
	}

	// 模拟配置文件变更（移除一个，添加一个）
	updatedURLs := []string{
		"https://api.anthropic.com", // 保留
		"http://ollama2.local:11434", // 新增
	}

	// 同步更新后的配置
	keepKeys := make([]db.ConfigTargetKey, 0, len(updatedURLs))
		for _, url := range updatedURLs {
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        url,
			APIKeyID:   &apiKey.ID,
			Provider:   "anthropic",
			Name:       "Target " + url,
			Weight:     1,
			Source:     "config",
			IsEditable: false,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := repo.Upsert(target); err != nil {
			t.Fatalf("Upsert updated target: %v", err)
		}
		keepKeys = append(keepKeys, db.ConfigTargetKey{URL: url, APIKeyID: &apiKey.ID})
	}

	// 删除不在新配置中的 config targets
	deleted, err := repo.DeleteConfigTargetsNotInList(keepKeys)
	if err != nil {
		t.Fatalf("DeleteConfigTargetsNotInList: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Expected 1 deleted target, got %d", deleted)
	}

	// 验证最终状态
	targets, _ = repo.ListAll()
	if len(targets) != 2 {
		t.Errorf("Expected 2 final targets, got %d", len(targets))
	}

	// 验证正确的 targets 存在
	urls := make(map[string]bool)
	for _, target := range targets {
		urls[target.URL] = true
	}
	if !urls["https://api.anthropic.com"] {
		t.Error("Expected api.anthropic.com to exist")
	}
	if !urls["http://ollama2.local:11434"] {
		t.Error("Expected ollama2.local to exist")
	}
	if urls["http://ollama1.local:11434"] {
		t.Error("Expected ollama1.local to be deleted")
	}

	t.Log("Config change and restart sync: PASS")
}

// ---------------------------------------------------------------------------
// Scenario 6: WebUI Add/Update/Delete Target
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_WebUIOperations(t *testing.T) {
	gormDB, logger := setupTestDB(t)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "ollama")

	// 创建管理员用户
	admin := createTestAdmin(t, gormDB, logger)

	// 创建 JWT 管理器
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// 创建审计日志仓库
	auditRepo := db.NewAuditRepo(logger, gormDB)

	// 创建 AdminLLMTargetHandler
	handler := api.NewAdminLLMTargetHandler(
		logger,
		jwtMgr,
		db.NewLLMTargetRepo(gormDB, logger),
		auditRepo,
		admin.PasswordHash,
		time.Hour,
	)

	// 创建 HTTP 路由
	mux := http.NewServeMux()

	// 简单的 admin 认证中间件
	requireAdmin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 简化：直接通过认证
			next.ServeHTTP(w, r)
		})
	}
	// 非 Worker 节点（Primary）：不封锁写操作
	noopWritable := func(next http.Handler) http.Handler { return next }

	handler.RegisterRoutes(mux, requireAdmin, noopWritable)

	// 创建测试服务器
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &http.Client{Timeout: 10 * time.Second}

	// 1. WebUI Add Target
	addReq := map[string]interface{}{
		"url":               "http://localhost:11434",
		"api_key_id":        apiKey.ID,
		"provider":          "ollama",
		"name":              "Local Ollama",
		"weight":            1,
		"health_check_path": "/health",
	}
	addBody, _ := json.Marshal(addReq)

	resp, err := client.Post(server.URL+"/api/admin/llm/targets", "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("POST /api/admin/llm/targets: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp.StatusCode)
	}

	var addResp db.LLMTarget
	if err := json.NewDecoder(resp.Body).Decode(&addResp); err != nil {
		t.Fatalf("Decode add response: %v", err)
	}

	targetID := addResp.ID
	if targetID == "" {
		t.Fatal("Expected non-empty target ID")
	}

	// 2. WebUI Get Target
	resp, err = client.Get(server.URL + "/api/admin/llm/targets/" + targetID)
	if err != nil {
		t.Fatalf("GET /api/admin/llm/targets/:id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var getResp db.LLMTarget
	if err := json.NewDecoder(resp.Body).Decode(&getResp); err != nil {
		t.Fatalf("Decode get response: %v", err)
	}

	if getResp.Name != "Local Ollama" {
		t.Errorf("Expected name='Local Ollama', got %s", getResp.Name)
	}

	// 3. WebUI Update Target
	updateReq := map[string]interface{}{
		"name":   "Updated Ollama",
		"weight": 2,
	}
	updateBody, _ := json.Marshal(updateReq)

	req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/admin/llm/targets/"+targetID, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/admin/llm/targets/:id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var updateResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&updateResp); err != nil {
		t.Fatalf("Decode update response: %v", err)
	}

	if updateResp["message"] != "Target updated successfully" {
		t.Errorf("Expected success message, got %v", updateResp["message"])
	}

	// 4. WebUI Enable/Disable Target
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/api/admin/llm/targets/"+targetID+"/disable", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/admin/llm/targets/:id/disable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 5. WebUI Delete Target
	req, _ = http.NewRequest(http.MethodDelete, server.URL+"/api/admin/llm/targets/"+targetID, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/admin/llm/targets/:id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 验证删除
	resp, err = client.Get(server.URL + "/api/admin/llm/targets/" + targetID)
	if err != nil {
		t.Fatalf("GET /api/admin/llm/targets/:id after delete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 after delete, got %d", resp.StatusCode)
	}

	t.Log("WebUI add/update/delete: PASS")
}

// ---------------------------------------------------------------------------
// Integration Test: Full Lifecycle
// ---------------------------------------------------------------------------

func TestLLMTargetManagement_FullLifecycle(t *testing.T) {
	gormDB, logger := setupTestDB(t)
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 创建测试 API Key
	apiKey := createTestAPIKey(t, gormDB, logger, "key-1", "test-key", "anthropic")

	// 1. 配置文件同步
	configTarget := &db.LLMTarget{
		ID:         uuid.NewString(),
		URL:        "https://api.anthropic.com",
		APIKeyID:   &apiKey.ID,
		Provider:   "anthropic",
		Name:       "Anthropic Official",
		Weight:     1,
		Source:     "config",
		IsEditable: false,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := repo.Upsert(configTarget); err != nil {
		t.Fatalf("Upsert config target: %v", err)
	}

	// 2. 数据库添加
	dbTarget := &db.LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://localhost:11434",
		APIKeyID:   &apiKey.ID,
		Provider:   "ollama",
		Name:       "Local Ollama",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := repo.Create(dbTarget); err != nil {
		t.Fatalf("Create db target: %v", err)
	}

	// 3. 列出所有 targets
	targets, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(targets))
	}

	// 4. 验证配置文件 target 可以被 CLI 修改（IsEditable 限制仅在 WebUI 层）
	// 重新从数据库加载以获取最新状态
	reloadedConfig, err := repo.GetByURL(configTarget.URL)
	if err != nil {
		t.Fatalf("GetByURL: %v", err)
	}
	reloadedConfig.Name = "Modified via CLI"
	if err := repo.Update(reloadedConfig); err != nil {
		t.Errorf("CLI should be able to update config-sourced target: %v", err)
	}

	// 5. 更新数据库 target
	dbTarget.Name = "Updated Ollama"
	dbTarget.UpdatedAt = time.Now()
	if err := repo.Update(dbTarget); err != nil {
		t.Fatalf("Update db target: %v", err)
	}

	// 6. 删除数据库 target
	if err := repo.Delete(dbTarget.ID); err != nil {
		t.Fatalf("Delete db target: %v", err)
	}

	// 7. 验证最终状态
	targets, _ = repo.ListAll()
	if len(targets) != 1 {
		t.Errorf("Expected 1 target after delete, got %d", len(targets))
	}
	if targets[0].Source != "config" {
		t.Errorf("Expected remaining target to be config-sourced, got %s", targets[0].Source)
	}

	t.Log("Full lifecycle: PASS")
}
