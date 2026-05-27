package proxy

// proxy_coverage_test.go — 补充 internal/proxy 包中 0% 或低覆盖率函数的测试用例。
// 不修改任何已有测试文件，仅新增本文件。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/track"
)

// ─────────────────────────────────────────────────────────────────────────────
// db_adapter.go: NewDBUserLister / ListActive / IsUserActive
// ─────────────────────────────────────────────────────────────────────────────

func setupAdapterDB(t *testing.T) *db.UserRepo {
	t.Helper()
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return db.NewUserRepo(gormDB, logger)
}

func TestNewDBUserLister_NotNil(t *testing.T) {
	repo := setupAdapterDB(t)
	lister := NewDBUserLister(repo)
	if lister == nil {
		t.Fatal("NewDBUserLister returned nil")
	}
	if lister.repo != repo {
		t.Error("DBUserLister.repo not set correctly")
	}
}

func TestDBUserLister_ListActive_Empty(t *testing.T) {
	repo := setupAdapterDB(t)
	lister := NewDBUserLister(repo)

	entries, err := lister.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestDBUserLister_ListActive_ReturnsActiveUsers(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	repo := db.NewUserRepo(gormDB, logger)

	// 创建两个用户：一个激活，一个禁用
	uActive := &db.User{
		ID:           "u-active",
		Username:     "active_user",
		PasswordHash: "hash",
		IsActive:     true,
	}
	uInactive := &db.User{
		ID:           "u-inactive",
		Username:     "inactive_user",
		PasswordHash: "hash",
		IsActive:     true, // 先创建再禁用（GORM default:true 问题）
	}
	if err := repo.Create(uActive); err != nil {
		t.Fatalf("Create active: %v", err)
	}
	if err := repo.Create(uInactive); err != nil {
		t.Fatalf("Create inactive: %v", err)
	}
	// 禁用第二个用户
	if err := gormDB.Model(&db.User{}).Where("id = ?", "u-inactive").Update("is_active", false).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}

	lister := NewDBUserLister(repo)
	entries, err := lister.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 active entry, got %d", len(entries))
	}
	if entries[0].Username != "active_user" {
		t.Errorf("expected 'active_user', got %q", entries[0].Username)
	}
	if entries[0].ID != "u-active" {
		t.Errorf("expected ID 'u-active', got %q", entries[0].ID)
	}
	if !entries[0].IsActive {
		t.Error("entry.IsActive should be true")
	}
}

func TestDBUserLister_IsUserActive_ActiveUser(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	repo := db.NewUserRepo(gormDB, logger)

	u := &db.User{ID: "u1", Username: "alice", PasswordHash: "h", IsActive: true}
	if err := repo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	lister := NewDBUserLister(repo)
	active, err := lister.IsUserActive("u1")
	if err != nil {
		t.Fatalf("IsUserActive: %v", err)
	}
	if !active {
		t.Error("IsUserActive should return true for active user")
	}
}

func TestDBUserLister_IsUserActive_InactiveUser(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	repo := db.NewUserRepo(gormDB, logger)

	u := &db.User{ID: "u2", Username: "bob", PasswordHash: "h", IsActive: true}
	if err := repo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 禁用用户
	if err := gormDB.Model(&db.User{}).Where("id = ?", "u2").Update("is_active", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}

	lister := NewDBUserLister(repo)
	active, err := lister.IsUserActive("u2")
	if err != nil {
		t.Fatalf("IsUserActive: %v", err)
	}
	if active {
		t.Error("IsUserActive should return false for inactive user")
	}
}

func TestDBUserLister_IsUserActive_UserNotFound(t *testing.T) {
	repo := setupAdapterDB(t)
	lister := NewDBUserLister(repo)

	// 查询不存在的用户，应返回 false（不报错）
	active, err := lister.IsUserActive("nonexistent-id")
	if err != nil {
		t.Fatalf("IsUserActive for nonexistent user should not error: %v", err)
	}
	if active {
		t.Error("IsUserActive should return false for nonexistent user")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: ptrToString
// ─────────────────────────────────────────────────────────────────────────────

func TestPtrToString_Nil(t *testing.T) {
	got := ptrToString(nil)
	if got != "" {
		t.Errorf("ptrToString(nil) = %q, want empty string", got)
	}
}

func TestPtrToString_NonNil(t *testing.T) {
	s := "hello"
	got := ptrToString(&s)
	if got != "hello" {
		t.Errorf("ptrToString(&%q) = %q, want %q", s, got, s)
	}
}

func TestPtrToString_EmptyString(t *testing.T) {
	s := ""
	got := ptrToString(&s)
	if got != "" {
		t.Errorf("ptrToString(&\"\") = %q, want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: SetConvTracker / SetConfigAndDB
// ─────────────────────────────────────────────────────────────────────────────

func TestSProxy_SetConvTracker_NonNil(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	tracker, err := track.New(t.TempDir())
	if err != nil {
		t.Fatalf("track.New: %v", err)
	}

	// 不应 panic
	sp.SetConvTracker(tracker)
	// 验证已设置
	if sp.convTracker.Load() == nil {
		t.Error("convTracker should be set after SetConvTracker")
	}
}

func TestSProxy_SetConvTracker_Nil(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 先设置为非 nil
	tracker, err := track.New(t.TempDir())
	if err != nil {
		t.Fatalf("track.New: %v", err)
	}
	sp.SetConvTracker(tracker)

	// 再设置为 nil
	sp.SetConvTracker(nil)
	if sp.convTracker.Load() != nil {
		t.Error("convTracker should be nil after SetConvTracker(nil)")
	}
}

func TestSProxy_SetConfigAndDB(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: "https://api.example.com", Provider: "anthropic"},
			},
		},
	}

	// 不应 panic，且字段应被设置
	sp.SetConfigAndDB(cfg, gormDB)
	if sp.cfg != cfg {
		t.Error("sp.cfg should be set after SetConfigAndDB")
	}
	if sp.db != gormDB {
		t.Error("sp.db should be set after SetConfigAndDB")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: SyncConfigTargets — 公共入口（需要先调用 SetConfigAndDB）
// ─────────────────────────────────────────────────────────────────────────────

func TestSProxy_SyncConfigTargets_NilConfigAndDB(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 未调用 SetConfigAndDB，应返回错误
	err := sp.SyncConfigTargets()
	if err == nil {
		t.Error("SyncConfigTargets without config and db should return error")
	}
}

func TestSProxy_SyncConfigTargets_WithConfigAndDB(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: "http://sync-target.test", APIKey: "sk-test", Provider: "anthropic", Name: "Sync Test", Weight: 1},
			},
		},
	}

	sp.SetConfigAndDB(cfg, gormDB)

	if err := sp.SyncConfigTargets(); err != nil {
		t.Fatalf("SyncConfigTargets: %v", err)
	}

	// 验证 target 已同步到 DB
	repo := db.NewLLMTargetRepo(gormDB, logger)
	dbTargets, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(dbTargets) != 1 {
		t.Fatalf("expected 1 target in DB, got %d", len(dbTargets))
	}
	if dbTargets[0].URL != "http://sync-target.test" {
		t.Errorf("unexpected URL: %s", dbTargets[0].URL)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: LoadAllTargets — 公共入口
// ─────────────────────────────────────────────────────────────────────────────

func TestSProxy_LoadAllTargets_NilDB(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// sp.db 为 nil，应返回错误
	_, err := sp.LoadAllTargets()
	if err == nil {
		t.Error("LoadAllTargets without db should return error")
	}
}

func TestSProxy_LoadAllTargets_WithDB(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sp := &SProxy{
		db:     gormDB,
		logger: logger,
	}

	targets, err := sp.LoadAllTargets()
	if err != nil {
		t.Fatalf("LoadAllTargets: %v", err)
	}
	// 空 DB 应返回空切片
	if len(targets) != 0 {
		t.Errorf("expected 0 targets from empty DB, got %d", len(targets))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: SetDB — 覆盖 error 分支（接口返回 err 时 warn）
// ─────────────────────────────────────────────────────────────────────────────

// 直接用真实 in-memory DB 测试 SetDB 的成功路径（75% → 100%）
func TestSProxy_SetDB_Success(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// 不应 panic，sqlDB 应被设置
	sp.SetDB(gormDB)
	if sp.sqlDB == nil {
		t.Error("sqlDB should be set after SetDB with valid DB")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: resolveAPIKey — 覆盖 nil apiKeyID 分支
// ─────────────────────────────────────────────────────────────────────────────

func TestSProxy_ResolveAPIKey_NilID(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sp := &SProxy{db: gormDB, logger: logger}

	// nil apiKeyID → 返回 ("", nil)
	key, err := sp.resolveAPIKey(nil)
	if err != nil {
		t.Fatalf("resolveAPIKey(nil): %v", err)
	}
	if key != "" {
		t.Errorf("resolveAPIKey(nil) = %q, want empty", key)
	}
}

func TestSProxy_ResolveAPIKey_EmptyID(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sp := &SProxy{db: gormDB, logger: logger}

	emptyID := ""
	key, err := sp.resolveAPIKey(&emptyID)
	if err != nil {
		t.Fatalf("resolveAPIKey(&\"\"): %v", err)
	}
	if key != "" {
		t.Errorf("resolveAPIKey(&\"\") = %q, want empty", key)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: llmTargetInfoForURL / providerForURL / modelMappingForURL
// ─────────────────────────────────────────────────────────────────────────────

func makeSProxyWithTargets(targets []LLMTarget) *SProxy {
	logger := zap.NewNop()
	return &SProxy{
		logger:  logger,
		targets: targets,
	}
}

func TestLLMTargetInfoForURL_Found(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", APIKey: "sk-ant", Provider: "anthropic"},
		{URL: "https://api.openai.com", APIKey: "sk-oai", Provider: "openai"},
	})

	info := sp.llmTargetInfoForURL("https://api.anthropic.com")
	if info == nil {
		t.Fatal("expected non-nil LLMTargetInfo")
	}
	if info.URL != "https://api.anthropic.com" {
		t.Errorf("URL = %q, want anthropic URL", info.URL)
	}
	if info.APIKey != "sk-ant" {
		t.Errorf("APIKey = %q, want 'sk-ant'", info.APIKey)
	}
}

func TestLLMTargetInfoForURL_NotFound_ReturnsFallback(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", APIKey: "sk-ant", Provider: "anthropic"},
	})

	// URL 不在列表中，返回只含 URL 的 fallback
	info := sp.llmTargetInfoForURL("https://unknown.example.com")
	if info == nil {
		t.Fatal("expected non-nil fallback LLMTargetInfo")
	}
	if info.URL != "https://unknown.example.com" {
		t.Errorf("fallback URL = %q, want original URL", info.URL)
	}
	if info.APIKey != "" {
		t.Errorf("fallback APIKey should be empty, got %q", info.APIKey)
	}
}

func TestProviderForURL_Found(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", Provider: "anthropic"},
	})

	got := sp.providerForURL("https://api.anthropic.com")
	if got != "anthropic" {
		t.Errorf("providerForURL = %q, want 'anthropic'", got)
	}
}

func TestProviderForURL_NotFound(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", Provider: "anthropic"},
	})

	got := sp.providerForURL("https://unknown.example.com")
	if got != "" {
		t.Errorf("providerForURL for unknown URL = %q, want empty", got)
	}
}

func TestModelMappingForURL_Found(t *testing.T) {
	mapping := map[string]string{"claude-3-opus-20240229": "llama3"}
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "http://ollama.local", Provider: "ollama", ModelMapping: mapping},
	})

	got := sp.modelMappingForURL("http://ollama.local")
	if got == nil {
		t.Fatal("expected non-nil model mapping")
	}
	if got["claude-3-opus-20240229"] != "llama3" {
		t.Errorf("mapping['claude-3-opus-20240229'] = %q, want 'llama3'", got["claude-3-opus-20240229"])
	}
}

func TestModelMappingForURL_NotFound(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", Provider: "anthropic"},
	})

	got := sp.modelMappingForURL("https://unknown.example.com")
	if got != nil {
		t.Errorf("modelMappingForURL for unknown URL should be nil, got %v", got)
	}
}

func TestModelMappingForURL_NilMapping(t *testing.T) {
	sp := makeSProxyWithTargets([]LLMTarget{
		{URL: "https://api.anthropic.com", Provider: "anthropic", ModelMapping: nil},
	})

	got := sp.modelMappingForURL("https://api.anthropic.com")
	if got != nil {
		t.Errorf("modelMappingForURL with nil mapping should be nil, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: preferredProvidersByPath
// ─────────────────────────────────────────────────────────────────────────────

func TestPreferredProvidersByPath_ChatCompletions(t *testing.T) {
	got := preferredProvidersByPath("/v1/chat/completions")
	if got == nil {
		t.Fatal("expected non-nil map for /v1/chat/completions")
	}
	if !got["openai"] {
		t.Error("openai should be preferred for /v1/chat/completions")
	}
	if !got["ollama"] {
		t.Error("ollama should be preferred for /v1/chat/completions")
	}
	if got["anthropic"] {
		t.Error("anthropic should NOT be preferred for /v1/chat/completions")
	}
}

func TestPreferredProvidersByPath_Messages(t *testing.T) {
	got := preferredProvidersByPath("/v1/messages")
	if got == nil {
		t.Fatal("expected non-nil map for /v1/messages")
	}
	if !got["anthropic"] {
		t.Error("anthropic should be preferred for /v1/messages")
	}
	if !got[""] {
		t.Error("empty provider should be preferred for /v1/messages")
	}
}

func TestPreferredProvidersByPath_Unknown(t *testing.T) {
	got := preferredProvidersByPath("/some/unknown/path")
	if got != nil {
		t.Errorf("expected nil for unknown path, got %v", got)
	}
}

func TestPreferredProvidersByPath_ChatCompletionsWithSuffix(t *testing.T) {
	// HasPrefix 应匹配带后缀的路径
	got := preferredProvidersByPath("/v1/chat/completions?model=gpt-4")
	if got == nil {
		t.Fatal("expected non-nil for path with query params")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: buildRetryTransport — 无均衡器时返回原始 transport
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildRetryTransport_NoBalancer_ReturnsTransport(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 无 llmBalancer → 应返回 sp.transport
	result := sp.buildRetryTransport("user1", "group1", "/v1/messages", "")
	if result != sp.transport {
		t.Error("buildRetryTransport without balancer should return sp.transport")
	}
}

func TestBuildRetryTransport_WithBalancer_ReturnsRetryTransport(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	// 设置均衡器
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true},
	})
	sp.llmBalancer = balancer

	result := sp.buildRetryTransport("user1", "group1", "/v1/messages", "")
	if result == nil {
		t.Fatal("buildRetryTransport with balancer should return non-nil transport")
	}
	// 应返回 RetryTransport，而非 sp.transport
	if result == sp.transport {
		t.Error("buildRetryTransport with balancer should return a RetryTransport, not the base transport")
	}
}

func TestBuildRetryTransport_ZeroMaxRetries_DefaultsToTwo(t *testing.T) {
	sp, cancel := newTestSProxySimple(t)
	defer cancel()

	sp.maxRetries = 0 // 触发默认值分支
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true},
	})
	sp.llmBalancer = balancer

	result := sp.buildRetryTransport("user1", "group1", "/v1/messages", "")
	if result == nil {
		t.Fatal("buildRetryTransport should return non-nil transport even with zero maxRetries")
	}
	rt, ok := result.(*lb.RetryTransport)
	if !ok {
		t.Fatalf("expected *lb.RetryTransport, got %T", result)
	}
	if rt.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2 (default)", rt.MaxRetries)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sproxy.go: weightedPickExcluding — provider fallback 分支
// ─────────────────────────────────────────────────────────────────────────────

func newSProxyWithBalancer(t *testing.T, targets []LLMTarget) (*SProxy, func()) {
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
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)

	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		cancel()
		t.Fatalf("NewSProxy: %v", err)
	}

	// 设置均衡器
	lbTargets := make([]lb.Target, len(targets))
	for i, tgt := range targets {
		lbTargets[i] = lb.Target{
			ID:      tgt.URL,
			Addr:    tgt.URL,
			Weight:  1,
			Healthy: true,
		}
	}
	balancer := lb.NewWeightedRandom(lbTargets)
	sp.llmBalancer = balancer

	cleanup := func() {
		cancel()
		writer.Wait()
	}
	return sp, cleanup
}

func TestWeightedPickExcluding_NoPreferredProvider_FallbackToAll(t *testing.T) {
	// 使用 openai target 但请求路径是 /v1/messages（preferred: anthropic）
	// 由于没有 anthropic target，应 fallback 到全量健康 target
	sp, cleanup := newSProxyWithBalancer(t, []LLMTarget{
		{URL: "https://openai.example.com", APIKey: "key", Provider: "openai", Weight: 1},
	})
	defer cleanup()

	info, err := sp.weightedPickExcluding("/v1/messages", "", nil)
	if err != nil {
		t.Fatalf("weightedPickExcluding: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil LLMTargetInfo")
	}
	if info.URL != "https://openai.example.com" {
		t.Errorf("URL = %q, want 'https://openai.example.com'", info.URL)
	}
}

func TestWeightedPickExcluding_AllTried_ReturnsError(t *testing.T) {
	sp, cleanup := newSProxyWithBalancer(t, []LLMTarget{
		{URL: "https://api.anthropic.com", APIKey: "key", Provider: "anthropic", Weight: 1},
	})
	defer cleanup()

	// 将唯一 target 标记为已尝试
	tried := map[string]bool{"https://api.anthropic.com": true}
	_, err := sp.weightedPickExcluding("/v1/messages", "", tried)
	if err == nil {
		t.Error("expected error when all targets are tried")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// protocol_converter.go: Header / WriteHeader（AnthropicToOpenAIStreamConverter）
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicToOpenAIStreamConverter_Header(t *testing.T) {
	underlying := httptest.NewRecorder()
	underlying.Header().Set("X-Test", "value")

	conv := NewAnthropicToOpenAIStreamConverter(underlying, zap.NewNop(), "req-1", "gpt-4")

	// Header() 应委托给底层 ResponseWriter
	hdr := conv.Header()
	if hdr == nil {
		t.Fatal("Header() should not return nil")
	}
	if hdr.Get("X-Test") != "value" {
		t.Errorf("Header()['X-Test'] = %q, want 'value'", hdr.Get("X-Test"))
	}
}

func TestAnthropicToOpenAIStreamConverter_WriteHeader(t *testing.T) {
	underlying := httptest.NewRecorder()
	conv := NewAnthropicToOpenAIStreamConverter(underlying, zap.NewNop(), "req-2", "gpt-4")

	// WriteHeader 应委托给底层
	conv.WriteHeader(http.StatusCreated)
	if underlying.Code != http.StatusCreated {
		t.Errorf("underlying status code = %d, want %d", underlying.Code, http.StatusCreated)
	}
}

func TestAnthropicToOpenAIStreamConverter_HeaderModification(t *testing.T) {
	underlying := httptest.NewRecorder()
	conv := NewAnthropicToOpenAIStreamConverter(underlying, zap.NewNop(), "req-3", "gpt-4")

	// 通过 conv.Header() 设置头，应反映在底层 ResponseWriter 中
	conv.Header().Set("Content-Type", "text/event-stream")
	if underlying.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type not propagated to underlying writer")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// protocol_converter.go: floatToInt — 覆盖非 float64 分支
// ─────────────────────────────────────────────────────────────────────────────

func TestFloatToInt_Float64(t *testing.T) {
	got := floatToInt(float64(42))
	if got != 42 {
		t.Errorf("floatToInt(42.0) = %d, want 42", got)
	}
}

func TestFloatToInt_Zero(t *testing.T) {
	got := floatToInt(float64(0))
	if got != 0 {
		t.Errorf("floatToInt(0.0) = %d, want 0", got)
	}
}

func TestFloatToInt_NonFloat64_String(t *testing.T) {
	// 非 float64 类型（如字符串）应返回 0
	got := floatToInt("not-a-float")
	if got != 0 {
		t.Errorf("floatToInt(string) = %d, want 0", got)
	}
}

func TestFloatToInt_NonFloat64_Int(t *testing.T) {
	// int 类型不是 float64，应返回 0
	got := floatToInt(int(99))
	if got != 0 {
		t.Errorf("floatToInt(int) = %d, want 0", got)
	}
}

func TestFloatToInt_Nil(t *testing.T) {
	got := floatToInt(nil)
	if got != 0 {
		t.Errorf("floatToInt(nil) = %d, want 0", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// protocol_converter.go: extractToolResultContent — 各分支
// ─────────────────────────────────────────────────────────────────────────────

func TestExtractToolResultContent_Nil(t *testing.T) {
	got := extractToolResultContent(nil)
	if got != "" {
		t.Errorf("extractToolResultContent(nil) = %q, want empty", got)
	}
}

func TestExtractToolResultContent_String(t *testing.T) {
	got := extractToolResultContent("hello world")
	if got != "hello world" {
		t.Errorf("extractToolResultContent(string) = %q, want 'hello world'", got)
	}
}

func TestExtractToolResultContent_SliceOfInterfaces(t *testing.T) {
	// []interface{} 分支 → extractTextContent
	content := []interface{}{
		map[string]interface{}{"type": "text", "text": "result text"},
	}
	got := extractToolResultContent(content)
	if got != "result text" {
		t.Errorf("extractToolResultContent([]interface{}) = %q, want 'result text'", got)
	}
}

func TestExtractToolResultContent_OtherType(t *testing.T) {
	// 未知类型 → fmt.Sprintf
	got := extractToolResultContent(12345)
	if got == "" {
		t.Error("extractToolResultContent(int) should return non-empty string")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// keyauth_middleware.go: safePrefix — 各分支
// ─────────────────────────────────────────────────────────────────────────────

func TestSafePrefix_ShortString_ReturnsFull(t *testing.T) {
	// len(s) <= n → 返回完整字符串
	got := safePrefix("abc", 10)
	if got != "abc" {
		t.Errorf("safePrefix(%q, 10) = %q, want %q", "abc", got, "abc")
	}
}

func TestSafePrefix_ExactLength_ReturnsFull(t *testing.T) {
	got := safePrefix("hello", 5)
	if got != "hello" {
		t.Errorf("safePrefix(%q, 5) = %q, want %q", "hello", got, "hello")
	}
}

func TestSafePrefix_LongString_ReturnsTruncated(t *testing.T) {
	// len(s) > n → 返回前 n 字符 + "..."
	got := safePrefix("sk-pp-abcdefghij", 6)
	if got != "sk-pp-..." {
		t.Errorf("safePrefix(%q, 6) = %q, want 'sk-pp-...'", "sk-pp-abcdefghij", got)
	}
}

func TestSafePrefix_EmptyString(t *testing.T) {
	got := safePrefix("", 5)
	if got != "" {
		t.Errorf("safePrefix(\"\", 5) = %q, want empty", got)
	}
}

func TestSafePrefix_ZeroN(t *testing.T) {
	// n=0: 任何非空字符串都会被截断为 "..."
	got := safePrefix("abc", 0)
	if got != "..." {
		t.Errorf("safePrefix(%q, 0) = %q, want '...'", "abc", got)
	}
}
