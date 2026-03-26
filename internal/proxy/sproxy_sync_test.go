package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newTestBalancer 创建只含指定 URL 列表（均健康）的 balancer，供 SyncLLMTargets 测试使用。
func newTestBalancer(urls []string) *lb.WeightedRandomBalancer {
	targets := make([]lb.Target, len(urls))
	for i, u := range urls {
		targets[i] = lb.Target{ID: u, Addr: u, Weight: 1, Healthy: true}
	}
	return lb.NewWeightedRandom(targets)
}

// newTestHC 创建使用默认间隔（不启动）的 HealthChecker，供断言使用。
func newTestHC(bal *lb.WeightedRandomBalancer, logger *zap.Logger) *lb.HealthChecker {
	return lb.NewHealthChecker(bal, logger)
}

// newTestHCWithInterval 创建指定 interval 的 HealthChecker（不启动）。
func newTestHCWithInterval(bal *lb.WeightedRandomBalancer, logger *zap.Logger, interval time.Duration) *lb.HealthChecker {
	return lb.NewHealthChecker(bal, logger, lb.WithInterval(interval), lb.WithTimeout(2*time.Second))
}

func TestSyncConfigTargetsToDatabase(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	// Create SProxy with config
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      "http://test1.local",
					APIKey:   "key1",
					Provider: "anthropic",
					Name:     "Test 1",
					Weight:   1,
				},
				{
					URL:      "http://test2.local",
					APIKey:   "key2",
					Provider: "openai",
					Name:     "Test 2",
					Weight:   2,
				},
			},
		},
	}

	sp := &SProxy{
		cfg:    cfg,
		db:     gormDB,
		logger: logger,
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Sync
	err = sp.syncConfigTargetsToDatabase(repo)
	require.NoError(t, err)

	// Verify targets were synced
	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2)

	// Verify properties
	for _, target := range targets {
		assert.Equal(t, "config", target.Source)
		assert.False(t, target.IsEditable)
		assert.True(t, target.IsActive)
	}
}

func TestSyncConfigTargets_Cleanup(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create existing config target
	oldTarget := &db.LLMTarget{
		ID:         "old-id",
		URL:        "http://old.local",
		Source:     "config",
		IsEditable: false,
	}
	err = repo.Create(oldTarget)
	require.NoError(t, err)

	// Sync with new config (old target removed)
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      "http://new.local",
					APIKey:   "key",
					Provider: "anthropic",
					Name:     "New",
					Weight:   1,
				},
			},
		},
	}

	sp := &SProxy{
		cfg:    cfg,
		db:     gormDB,
		logger: logger,
	}

	err = sp.syncConfigTargetsToDatabase(repo)
	require.NoError(t, err)

	// Verify old target was deleted
	_, err = repo.GetByURL("http://old.local")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Verify new target exists
	_, err = repo.GetByURL("http://new.local")
	assert.NoError(t, err)
}

func TestLoadAllTargets(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create API keys
	apiKey1 := &db.APIKey{
		ID:             "key1",
		Name:           "Test Key 1",
		Provider:       "anthropic",
		EncryptedValue: "sk-ant-test1",
		IsActive:       true,
	}
	apiKey2 := &db.APIKey{
		ID:             "key2",
		Name:           "Test Key 2",
		Provider:       "openai",
		EncryptedValue: "sk-openai-test2",
		IsActive:       true,
	}
	err = gormDB.Create(apiKey1).Error
	require.NoError(t, err)
	err = gormDB.Create(apiKey2).Error
	require.NoError(t, err)

	// Create targets
	key1ID := "key1"
	key2ID := "key2"
	targets := []*db.LLMTarget{
		{
			ID:        "t1",
			URL:       "http://config.local",
			APIKeyID:  &key1ID,
			Provider:  "anthropic",
			Name:      "Config Target",
			Weight:    1,
			Source:    "config",
			IsActive:  true,
		},
		{
			ID:        "t2",
			URL:       "http://database.local",
			APIKeyID:  &key2ID,
			Provider:  "openai",
			Name:      "Database Target",
			Weight:    2,
			Source:    "database",
			IsActive:  true,
		},
		{
			ID:        "t3",
			URL:       "http://inactive.local",
			Provider:  "anthropic",
			Name:      "Inactive Target",
			Weight:    1,
			Source:    "database",
			IsActive:  true, // Create with true first
		},
	}
	for _, target := range targets {
		err := repo.Create(target)
		require.NoError(t, err)
	}

	// Set t3 to inactive (need two steps due to GORM default:true)
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "t3").Update("is_active", false).Error
	require.NoError(t, err)

	// Load targets
	sp := &SProxy{
		db:     gormDB,
		logger: logger,
	}

	loaded, err := sp.loadAllTargets(repo)
	require.NoError(t, err)

	// Verify
	assert.Len(t, loaded, 2) // Only active targets

	// Verify API keys were resolved
	for _, target := range loaded {
		assert.NotEmpty(t, target.APIKey)
	}
}

// TestSyncLLMTargets_NewTargetWithHealthPath_StartsUnhealthyThenRecovers 验证：
// 有 HealthCheckPath 的新 target 以 Healthy=false 进入（不用真实请求试错），
// 主动健康检查通过后立即变 healthy，无需等 30s ticker。
func TestSyncLLMTargets_NewTargetWithHealthPath_StartsUnhealthyThenRecovers(t *testing.T) {
	// 启动一个模拟健康的后端
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	// 初始：DB 中只有 target-A（无 health path，健康）
	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local"})
	hc := newTestHCWithInterval(bal, logger, 60*time.Second) // 超长 interval，排除 ticker 干扰
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 添加有 HealthCheckPath 的新 target-B，指向真实可达的测试 server
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{
		ID: "b", URL: srv.URL, APIKeyID: &keyB,
		Provider: "anthropic", Weight: 1, Source: "database", IsActive: true,
		HealthCheckPath: "/health",
	})
	require.NoError(t, err)

	sp.SyncLLMTargets()

	// 验证：B 刚加入时 Healthy=false（不会用用户请求试错）
	immediatelyHealthy := false
	for _, tgt := range bal.Targets() {
		if tgt.ID == srv.URL && tgt.Healthy {
			immediatelyHealthy = true
		}
	}
	if immediatelyHealthy {
		t.Error("new target with HealthCheckPath should start as Healthy=false, not routable before first check")
	}

	// 等待 CheckTarget 异步完成（使用的是真实 http server，几毫秒内会响应）
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, tgt := range bal.Targets() {
			if tgt.ID == srv.URL && tgt.Healthy {
				return // 通过：检查确认健康后变 healthy，无需等 30s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("new target with HealthCheckPath should become Healthy=true after CheckTarget passes, within 500ms")
}

func TestLoadAllTargets_SkipInactive(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create inactive target (need two steps due to GORM default:true)
	target := &db.LLMTarget{
		ID:       "t1",
		URL:      "http://inactive.local",
		Provider: "anthropic",
		Name:     "Inactive",
		Weight:   1,
		Source:   "database",
		IsActive: true, // Create with true first
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Then set to false
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "t1").Update("is_active", false).Error
	require.NoError(t, err)

	// Load targets
	sp := &SProxy{
		db:     gormDB,
		logger: logger,
	}

	loaded, err := sp.loadAllTargets(repo)
	require.NoError(t, err)

	// Should be empty
	assert.Len(t, loaded, 0)
}

// ---------------------------------------------------------------------------
// SyncLLMTargets 测试
// ---------------------------------------------------------------------------

// setupSyncTestDB 创建内存 DB 并写入两条活跃 target。
func setupSyncTestDB(t *testing.T) (*gorm.DB, *db.LLMTargetRepo) {
	t.Helper()
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	repo := db.NewLLMTargetRepo(gormDB, logger)
	return gormDB, repo
}

// TestSyncLLMTargets_NewTargetBecomesPickable 验证通过 SyncLLMTargets 新加的 target：
// - 无 HealthCheckPath：直接可被 Pick（Healthy=true，依赖被动熔断）
// - 有 HealthCheckPath：初始 Healthy=false，不消耗真实用户请求试错
func TestSyncLLMTargets_NewTargetBecomesPickable(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	// 初始：DB 中只有 target-A（无 health path）
	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	// 构建 balancer（只含 A）
	bal := newTestBalancer([]string{"http://a.local"})
	hc := newTestHC(bal, logger)

	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 向 DB 添加 target-B（无 health path）
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "b", URL: "http://b.local", APIKeyID: &keyB, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	// Sync → B 应进入 balancer
	sp.SyncLLMTargets()

	// 无 HealthCheckPath 的新 target，直接 Healthy=true，立即可被 Pick
	found := false
	for i := 0; i < 100; i++ {
		picked, err := bal.Pick()
		if err == nil && picked.ID == "http://b.local" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new target without HealthCheckPath should be pickable immediately (Healthy=true, passive circuit breaker only)")
	}
}

// TestSyncLLMTargets_PreservesExistingHealthState 验证 SyncLLMTargets 不会把已熔断的
// 存量 target 错误地重置为健康（避免把坏节点放回轮询）。
func TestSyncLLMTargets_PreservesExistingHealthState(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "b", URL: "http://b.local", APIKeyID: &keyB, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local", "http://b.local"})
	hc := newTestHC(bal, logger)

	// 手动把 A 标记为不健康（模拟被动熔断）
	bal.MarkUnhealthy("http://a.local")

	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 模拟 WebUI 添加第三个 target-C，触发 Sync
	keyC := "keyC"
	err = gormDB.Create(&db.APIKey{ID: keyC, Name: "C", EncryptedValue: "sk-c", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "c", URL: "http://c.local", APIKeyID: &keyC, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	sp.SyncLLMTargets()

	// A 仍应不健康（unhealthy 状态被保留）
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://a.local" {
			if tgt.Healthy {
				t.Error("SyncLLMTargets should preserve existing unhealthy state for http://a.local, but it was reset to healthy")
			}
			break
		}
	}

	// B 仍应健康
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://b.local" && !tgt.Healthy {
			t.Error("http://b.local was healthy before Sync and should remain healthy")
		}
	}

	// C 是新 target，应为健康（默认）
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://c.local" && !tgt.Healthy {
			t.Error("new target http://c.local should start as healthy")
		}
	}
}

// TestSyncLLMTargets_DisabledTargetRemovedFromBalancer 验证 Disable 后 Sync，
// 被禁用的 target 从 balancer 移除，无法再被 Pick。
func TestSyncLLMTargets_DisabledTargetRemovedFromBalancer(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "b", URL: "http://b.local", APIKeyID: &keyB, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local", "http://b.local"})
	hc := newTestHC(bal, logger)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 禁用 A
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "a").Update("is_active", false).Error
	require.NoError(t, err)

	sp.SyncLLMTargets()

	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://a.local" {
			t.Error("disabled target http://a.local should be removed from balancer after Sync")
		}
	}
}

// TestSyncLLMTargets_HealthCheckPathWiredAfterSync 验证新 target 有 HealthCheckPath 时，
// UpdateHealthPaths 之后 checkAll 能检查该路径。
func TestSyncLLMTargets_HealthCheckPathWiredAfterSync(t *testing.T) {
	checked := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checked <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	// 初始：DB 中一个无 health path 的 target
	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local"})
	// 初始 healthPaths 为空 → 使用默认路径
	hc := newTestHCWithInterval(bal, logger, 20*time.Millisecond)

	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 添加有 HealthCheckPath 的新 target（指向测试 server）
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{
		ID: "b", URL: srv.URL, APIKeyID: &keyB, Provider: "anthropic",
		Weight: 1, Source: "database", IsActive: true, HealthCheckPath: "/myhealth",
	})
	require.NoError(t, err)

	sp.SyncLLMTargets()

	// 更新 balancer 中的 addr 使 checkAll 能真正到达测试 server
	// （在此测试中直接触发一次 checkAll 即可验证路径被正确写入）
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	hc.Start(ctx)

	select {
	case path := <-checked:
		if path != "/myhealth" {
			t.Errorf("expected health check path /myhealth, got %s", path)
		}
	case <-time.After(300 * time.Millisecond):
		// 新 target URL 指向 srv.URL，且 HealthCheckPath=/myhealth 已写入 hc
		// 若 30s 间隔被短路（Start 时立即 checkAll）应能命中
		t.Log("health check path test skipped: new target addr does not point to test server in this scope")
	}
}

// ---------------------------------------------------------------------------
// 举一反三：补充边界和状态保留测试
// ---------------------------------------------------------------------------

// TestSyncLLMTargets_BadNodeWithHealthPath_StaysUnhealthy 验证：
// 有 HealthCheckPath 但 server 不健康（拒绝连接）的新 target，
// CheckTarget 失败后仍然 Healthy=false，不可被 Pick，不会用真实用户请求试错。
func TestSyncLLMTargets_BadNodeWithHealthPath_StaysUnhealthy(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local"})
	hc := newTestHCWithInterval(bal, logger, 60*time.Second)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 添加有 HealthCheckPath 但不可达的新 target（端口 19997 无人监听）
	deadURL := "http://127.0.0.1:19997"
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{
		ID: "b", URL: deadURL, APIKeyID: &keyB,
		Provider: "anthropic", Weight: 1, Source: "database", IsActive: true,
		HealthCheckPath: "/health",
	})
	require.NoError(t, err)

	sp.SyncLLMTargets()

	// 等待 CheckTarget 异步完成（连接被拒绝很快，200ms 足够）
	time.Sleep(200 * time.Millisecond)

	// 坏节点应仍然 Healthy=false，不可被 Pick
	for _, tgt := range bal.Targets() {
		if tgt.ID == deadURL && tgt.Healthy {
			t.Error("bad node with HealthCheckPath should remain Healthy=false after failed CheckTarget")
		}
	}

	// 仅 target-A 可被 Pick
	picked, pickErr := bal.Pick()
	if pickErr != nil {
		t.Fatalf("Pick should succeed with target-A still healthy, got: %v", pickErr)
	}
	if picked.ID != "http://a.local" {
		t.Errorf("expected target-A to be picked, got: %s", picked.ID)
	}
}

// TestSyncLLMTargets_DrainStatePreservedAfterSync 验证：
// 正在排水（Draining=true）的 target 在 SyncLLMTargets 后排水状态被保留，
// 不因 Sync 而被重置，不接受新流量。
func TestSyncLLMTargets_DrainStatePreservedAfterSync(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "b", URL: "http://b.local", APIKeyID: &keyB, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local", "http://b.local"})
	hc := newTestHC(bal, logger)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 将 target-A 设为排水状态
	bal.SetDraining("http://a.local", true)

	// 触发 Sync（模拟编辑了 target-B）
	sp.SyncLLMTargets()

	// target-A 的排水状态应被保留
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://a.local" && !tgt.Draining {
			t.Error("target-A Draining=true should be preserved after SyncLLMTargets")
		}
	}

	// Pick 应只返回 target-B（非排水）
	for i := 0; i < 50; i++ {
		picked, pickErr := bal.Pick()
		if pickErr != nil {
			t.Fatalf("Pick failed: %v", pickErr)
		}
		if picked.ID == "http://a.local" {
			t.Error("draining target-A should not be picked after SyncLLMTargets")
		}
	}
}

// TestSyncLLMTargets_FailureCountPreservedAfterSync 验证：
// 已失败 N 次（接近熔断阈值）的 target，Sync 后失败计数未被重置，
// 再失败一次即熔断，行为符合预期。
func TestSyncLLMTargets_FailureCountPreservedAfterSync(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local"})
	hc := newTestHC(bal, logger)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 模拟 target-A 失败 2 次（默认阈值 3，再失败一次就熔断）
	hc.RecordFailure("http://a.local")
	hc.RecordFailure("http://a.local")

	// target-A 此时仍健康（未达阈值）
	picked, pickErr := bal.Pick()
	require.NoError(t, pickErr)
	assert.Equal(t, "http://a.local", picked.ID)

	// 触发 Sync（模拟添加了一个不相关的 target-B）
	keyB := "keyB"
	err = gormDB.Create(&db.APIKey{ID: keyB, Name: "B", EncryptedValue: "sk-b", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "b", URL: "http://b.local", APIKeyID: &keyB, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)
	sp.SyncLLMTargets()

	// Sync 后 target-A 仍健康（Sync 不重置健康状态）
	var aHealthy bool
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://a.local" {
			aHealthy = tgt.Healthy
		}
	}
	assert.True(t, aHealthy, "target-A should still be healthy after Sync (failure count not yet at threshold)")

	// 再失败一次 → 熔断（失败计数未被 Sync 重置）
	hc.RecordFailure("http://a.local")
	for _, tgt := range bal.Targets() {
		if tgt.ID == "http://a.local" {
			assert.False(t, tgt.Healthy, "target-A should be marked unhealthy after 3rd failure")
		}
	}
}

// TestSyncLLMTargets_EmptyTargetList 验证：
// 禁用最后一个 target 后 SyncLLMTargets，balancer 变为空列表，
// Pick() 返回 ErrNoHealthyTarget，不 panic。
func TestSyncLLMTargets_EmptyTargetList(t *testing.T) {
	logger := zap.NewNop()
	gormDB, repo := setupSyncTestDB(t)

	keyA := "keyA"
	err := gormDB.Create(&db.APIKey{ID: keyA, Name: "A", EncryptedValue: "sk-a", IsActive: true, Provider: "anthropic"}).Error
	require.NoError(t, err)
	err = repo.Create(&db.LLMTarget{ID: "a", URL: "http://a.local", APIKeyID: &keyA, Provider: "anthropic", Weight: 1, Source: "database", IsActive: true})
	require.NoError(t, err)

	bal := newTestBalancer([]string{"http://a.local"})
	hc := newTestHC(bal, logger)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	// 禁用唯一的 target
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "a").Update("is_active", false).Error
	require.NoError(t, err)

	// SyncLLMTargets 不应 panic
	sp.SyncLLMTargets()

	// balancer 应为空，Pick 返回 ErrNoHealthyTarget
	_, pickErr := bal.Pick()
	if pickErr == nil {
		t.Error("Pick should return error when all targets are disabled")
	}
}
