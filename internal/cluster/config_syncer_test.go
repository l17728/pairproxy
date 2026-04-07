package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// TestConfigSyncer_PullAndUpsert 验证 ConfigSyncer 能从 Primary 拉取快照并正确写入本地 DB。
func TestConfigSyncer_PullAndUpsert(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// 构造 Primary 快照
	snap := ConfigSnapshot{
		Version: time.Now(),
		Users: []db.User{
			{ID: "u1", Username: "alice", PasswordHash: "hash1", IsActive: true},
		},
		Groups: []db.Group{
			{ID: "g1", Name: "engineers"},
		},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/config-snapshot" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 验证用户已同步
	user, err := userRepo.GetByUsername("alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user == nil {
		t.Fatal("expected alice to be synced, but got nil")
	}
	if user.ID != "u1" {
		t.Errorf("expected user.ID=u1, got %s", user.ID)
	}

	// 验证分组已同步
	groups, err := groupRepo.List()
	if err != nil {
		t.Fatalf("groupRepo.List: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "engineers" {
		t.Errorf("expected 1 group 'engineers', got %v", groups)
	}
}

// TestConfigSyncer_UserDisabledPropagates 验证 Primary 禁用用户后，Worker 同步时会将用户标记为 inactive
// 并删除其 refresh_tokens（P0-2 修复）。
func TestConfigSyncer_UserDisabledPropagates(t *testing.T) {
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
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)
	tokenRepo := db.NewRefreshTokenRepo(gormDB, logger)

	// 预先在 Worker DB 创建用户（active）
	err = userRepo.Create(&db.User{
		ID:           "u1",
		Username:     "bob",
		PasswordHash: "hash1",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	// 预先创建 refresh token
	if err := tokenRepo.Create(&db.RefreshToken{
		JTI:       "tok123",
		UserID:    "u1",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Create token: %v", err)
	}

	// Primary 快照：bob 已被禁用
	snap := ConfigSnapshot{
		Version: time.Now(),
		Users: []db.User{
			{ID: "u1", Username: "bob", PasswordHash: "hash1", IsActive: false},
		},
		Groups:      []db.Group{},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 验证用户已被标记为 inactive
	user, err := userRepo.GetByUsername("bob")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user == nil {
		t.Fatal("user not found after sync")
	}
	if user.IsActive {
		t.Error("expected bob to be inactive after sync, but IsActive=true")
	}

	// 验证 refresh token 已被删除（P0-2）
	tok, err := tokenRepo.GetByJTI("tok123")
	if err != nil {
		t.Fatalf("GetByJTI: %v", err)
	}
	if tok != nil {
		t.Error("expected refresh token to be deleted for disabled user, but it still exists")
	}
}

// TestConfigSyncer_PrimaryUnreachable 验证 Primary 不可达时 syncer 不崩溃，Worker 继续使用本地数据。
func TestConfigSyncer_PrimaryUnreachable(t *testing.T) {
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
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	// 预先在 Worker DB 创建用户
	err = userRepo.Create(&db.User{
		ID:       "u1",
		Username: "charlie",
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  "http://127.0.0.1:19999", // 不可达地址
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 验证本地数据未受影响（charlie 仍在 DB 中）
	user, err := userRepo.GetByUsername("charlie")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user == nil {
		t.Error("expected charlie to remain in DB after failed sync")
	}

	// 验证 PullFailures 计数器已递增（Primary 不可达应计入失败）
	if syncer.PullFailures() == 0 {
		t.Error("expected PullFailures > 0 after unreachable primary, got 0")
	}
}

// TestConfigSyncer_IdempotentUpsert 验证多次同步相同快照是幂等的（不产生重复数据）。
func TestConfigSyncer_IdempotentUpsert(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	snap := ConfigSnapshot{
		Version: time.Now(),
		Users: []db.User{
			{ID: "u1", Username: "dave", PasswordHash: "hash1", IsActive: true},
		},
		Groups:      []db.Group{{ID: "g1", Name: "ops"}},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	// 服务器记录调用次数
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 多次同步后验证没有重复数据
	users, err := userRepo.ListByGroup("")
	if err != nil {
		t.Fatalf("ListByGroup: %v", err)
	}
	count := 0
	for _, u := range users {
		if u.Username == "dave" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 'dave', got %d (idempotency broken)", count)
	}

	groups, err := groupRepo.List()
	if err != nil {
		t.Fatalf("groupRepo.List: %v", err)
	}
	opsCount := 0
	for _, g := range groups {
		if g.Name == "ops" {
			opsCount++
		}
	}
	if opsCount != 1 {
		t.Errorf("expected exactly 1 group 'ops', got %d (idempotency broken)", opsCount)
	}
}

// TestConfigSyncer_LLMTargetsAndBindingsSynced 验证 LLM Targets 和 Bindings 能正确同步至 Worker（P1-4/P1-5）。
func TestConfigSyncer_LLMTargetsAndBindingsSynced(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userID := "u1"
	snap := ConfigSnapshot{
		Version: time.Now(),
		Users: []db.User{
			{ID: userID, Username: "eve", PasswordHash: "hash1", IsActive: true},
		},
		Groups: []db.Group{},
		LLMTargets: []*db.LLMTarget{
			{
				ID:       "t1",
				URL:      "https://api.example.com",
				Provider: "anthropic",
				Name:     "Test Target",
				Weight:   1,
				IsActive: true,
				Source:   "config",
			},
		},
		LLMBindings: []db.LLMBinding{
			{ID: "b1", TargetID: "t1", UserID: &userID},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 验证 LLM Target 已同步（P1-5）
	target, err := llmTargetRepo.GetByURL("https://api.example.com")
	if err != nil {
		t.Fatalf("GetByURL: %v", err)
	}
	if target == nil {
		t.Fatal("expected LLM target to be synced, but got nil")
	}
	if target.Name != "Test Target" {
		t.Errorf("target.Name = %q, want \"Test Target\"", target.Name)
	}

	// 验证 LLM Binding 已同步（P1-4）
	bindings, err := llmBindingRepo.List()
	if err != nil {
		t.Fatalf("llmBindingRepo.List: %v", err)
	}
	found := false
	for _, b := range bindings {
		if b.ID == "b1" && b.TargetID == "t1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected LLM binding b1 to be synced, bindings: %v", bindings)
	}
}

// TestConfigSyncer_PrimaryNon200 验证 Primary 返回非 200 状态码时 syncer 不崩溃，
// PullFailures 计数器递增，本地数据不受影响。
func TestConfigSyncer_PrimaryNon200(t *testing.T) {
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
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	if err := userRepo.Create(&db.User{ID: "u1", Username: "frank", IsActive: true}); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Primary 始终返回 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 本地数据不受影响
	user, err := userRepo.GetByUsername("frank")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user == nil {
		t.Error("expected frank to remain in DB")
	}
	// 失败计数器递增
	if syncer.PullFailures() == 0 {
		t.Error("expected PullFailures > 0 after non-200 responses, got 0")
	}
}

// TestConfigSyncer_MalformedJSON 验证 Primary 返回畸形 JSON 时 syncer 不崩溃，
// PullFailures 计数器递增，本地数据不受影响。
func TestConfigSyncer_MalformedJSON(t *testing.T) {
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
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	if err := userRepo.Create(&db.User{ID: "u1", Username: "grace", IsActive: true}); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Primary 返回非法 JSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	defer srv.Close()

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	// 本地数据不受影响
	user, err := userRepo.GetByUsername("grace")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user == nil {
		t.Error("expected grace to remain in DB after malformed JSON")
	}
	// 失败计数器递增
	if syncer.PullFailures() == 0 {
		t.Error("expected PullFailures > 0 after malformed JSON, got 0")
	}
}

// TestConfigSyncer_LLMTargetURLConflictResolution 验证 v2.14.1 修复：
// Worker 和 Primary 对同一 URL 生成不同 ID 时，ConfigSyncer 应使用 ON CONFLICT(url)
// 而非 ON CONFLICT(id)，避免 UNIQUE constraint failed: llm_targets.url 错误。
//
// 场景：
// 1. Worker 启动时从 sproxy.yaml 同步 targets 到本地 SQLite（生成 worker-uuid）
// 2. Primary 启动时从 sproxy.yaml 同步 targets 到本地 SQLite（生成 primary-uuid）
// 3. ConfigSyncer 从 Primary 拉取快照，尝试 upsert 相同 URL 但不同 ID 的 target
// 4. 预期：Worker 本地记录的 ID 更新为 Primary 的 ID，其他字段也同步，无 UNIQUE 错误
func TestConfigSyncer_LLMTargetURLConflictResolution(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)

	// 模拟 Worker 启动时从配置文件同步的 target（生成 worker-uuid）
	workerTarget := &db.LLMTarget{
		ID:       "worker-uuid-12345",
		URL:      "https://api.anthropic.com",
		Provider: "anthropic",
		Name:     "Worker Local Target",
		Weight:   1,
		IsActive: true,
		Source:   "config",
	}
	if err := llmTargetRepo.Create(workerTarget); err != nil {
		t.Fatalf("pre-populate worker target: %v", err)
	}

	// 验证 Worker 本地记录存在
	workerRecord, err := llmTargetRepo.GetByURL("https://api.anthropic.com")
	if err != nil {
		t.Fatalf("GetByURL before sync: %v", err)
	}
	if workerRecord.ID != "worker-uuid-12345" {
		t.Errorf("expected worker ID 'worker-uuid-12345', got %q", workerRecord.ID)
	}
	if workerRecord.Name != "Worker Local Target" {
		t.Errorf("expected worker name 'Worker Local Target', got %q", workerRecord.Name)
	}

	// 模拟 Primary 的快照（相同 URL，不同 ID 和其他字段）
	primaryTarget := &db.LLMTarget{
		ID:       "primary-uuid-67890",
		URL:      "https://api.anthropic.com",
		Provider: "anthropic",
		Name:     "Primary Synced Target",
		Weight:   2,
		IsActive: true,
		Source:   "config",
	}
	snap := ConfigSnapshot{
		Version:     time.Now(),
		Users:       []db.User{},
		Groups:      []db.Group{},
		LLMTargets:  []*db.LLMTarget{primaryTarget},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "test-secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	syncer.Start(ctx)

	// 等待至少一次成功同步
	time.Sleep(500 * time.Millisecond)

	// 取消 ctx 后等待 syncer 退出，再检查结果（避免 ctx 超时后的失败计入）
	cancel()
	syncer.Wait()

	// 验证同步成功（至少一次成功）
	if syncer.LastSyncAt() == 0 {
		t.Error("expected LastSyncAt > 0, got 0 (sync failed)")
	}

	// 验证 Worker 本地记录已更新为 Primary 的值
	syncedRecord, err := llmTargetRepo.GetByURL("https://api.anthropic.com")
	if err != nil {
		t.Fatalf("GetByURL after sync: %v", err)
	}

	// 关键验证：ID 应更新为 Primary 的 ID
	if syncedRecord.ID != "primary-uuid-67890" {
		t.Errorf("expected ID updated to 'primary-uuid-67890', got %q", syncedRecord.ID)
	}

	// 验证其他字段也同步
	if syncedRecord.Name != "Primary Synced Target" {
		t.Errorf("expected Name updated to 'Primary Synced Target', got %q", syncedRecord.Name)
	}
	if syncedRecord.Weight != 2 {
		t.Errorf("expected Weight updated to 2, got %d", syncedRecord.Weight)
	}
	if syncedRecord.Provider != "anthropic" {
		t.Errorf("expected Provider 'anthropic', got %q", syncedRecord.Provider)
	}
	if !syncedRecord.IsActive {
		t.Error("expected IsActive = true, got false")
	}
	if syncedRecord.Source != "config" {
		t.Errorf("expected Source 'config', got %q", syncedRecord.Source)
	}

	// 验证数据库中只有一条记录（没有重复插入）
	allTargets, err := llmTargetRepo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(allTargets) != 1 {
		t.Errorf("expected 1 target in DB, got %d (duplicate insert?)", len(allTargets))
	}
}
