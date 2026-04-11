package proxy

// issue2_e2e_test.go — Issue #2 完整场景端到端回归测试
//
// Issue #2 用户报告：API Key 按 provider 管理，全系统只能有 3 个 key（anthropic/openai/ollama 各一个）。
// 用户需求：同时接入多个 OpenAI 兼容服务（百炼、火山等），每个 URL 配独立 key，互不干扰。
//
// 完整场景路径：
//   1. sproxy.yaml 配置多 key → syncConfigTargetsToDatabase → DB
//   2. SyncLLMTargets → loadAllTargets → balancer（UUID 为 ID）
//   3. 请求到来 → pickLLMTarget（balancer 选 UUID）→ llmTargetInfoForID(UUID)
//   4. Director → firstInfo.APIKey 注入 Authorization 头
//
// 核心 Bug（SyncLLMTargets 后路由失效）：
//   sp.targets 是启动时从 cfg.LLM.Targets 构建的只读列表，LLMTarget.ID 为空（yaml 不含 UUID）。
//   SyncLLMTargets 更新 balancer（ID=UUID），但不更新 sp.targets。
//   llmTargetInfoForID(UUID) 遍历 sp.targets，每个 t.ID="" 故回退到 t.URL 匹配，永远找不到 UUID。
//   结果：返回 &lb.LLMTargetInfo{URL: UUID}，APIKey 为空，所有请求发出空 Bearer token。
//
// 附属 Bug（Name 碰撞）：
//   resolveAPIKeyID 生成 Name = "Auto-{provider}-{suffix}"，suffix 取 obfuscated key 末 8 位。
//   两个不同 key 的 obfuscated 末 8 位相同时，第二次 db.Create 会因 Name uniqueIndex 报错。

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// 辅助：模拟 LLM 后端，记录收到的 Authorization header
// ---------------------------------------------------------------------------

type captureServer struct {
	mu    sync.Mutex
	auths []string // 按请求顺序记录 Authorization header
	srv   *httptest.Server
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.auths = append(cs.auths, r.Header.Get("Authorization"))
		cs.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *captureServer) lastAuth() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.auths) == 0 {
		return ""
	}
	return cs.auths[len(cs.auths)-1]
}

// doRequest 向 sp 发送一个带 JWT 的 chat 请求，返回 HTTP 状态码
func doRequest(t *testing.T, sp *SProxy, jwtMgr *auth.Manager, userID, username, groupID string) int {
	t.Helper()
	claims := auth.JWTClaims{UserID: userID, Username: username, GroupID: groupID}
	token, err := jwtMgr.Sign(claims, time.Hour)
	require.NoError(t, err)

	body := `{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)
	io.ReadAll(rr.Body) //nolint:errcheck
	return rr.Code
}

// ---------------------------------------------------------------------------
// CORE BUG: SyncLLMTargets 后 llmTargetInfoForID 无法还原 APIKey
// ---------------------------------------------------------------------------

// TestIssue2_CoreBug_SyncThenRequest 验证 Issue #2 核心 bug：
// config → DB sync → SyncLLMTargets 后发出的请求，APIKey 应正确注入而非为空。
//
// Bug 路径：
//   sp.targets[i].ID == "" → llmTargetInfoForID(uuid) 找不到 → APIKey=""
//
// 修复后：SyncLLMTargets 应同时更新 sp.targets（或维护 targetByID 索引）。
func TestIssue2_CoreBug_SyncThenRequest(t *testing.T) {
	const wantKey = "sk-ant-key-for-bailian"

	cs := newCaptureServer(t)

	logger := zap.NewNop()
	jwtMgr, err := auth.NewManager(logger, "secret")
	require.NoError(t, err)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	// 用 cfg.LLM.Targets 构建 SProxy（模拟真实启动路径）
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      cs.srv.URL,
					APIKey:   wantKey,
					Provider: "anthropic",
					Name:     "Bailian",
					Weight:   1,
				},
			},
		},
	}

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: cs.srv.URL, APIKey: wantKey, Provider: "anthropic"},
	})
	require.NoError(t, err)
	sp.SetConfigAndDB(cfg, gormDB)

	// 配置 balancer（模拟 main.go 启动路径）
	bal := lb.NewWeightedRandom([]lb.Target{})
	hc := lb.NewHealthChecker(bal, logger)
	sp.SetLLMHealthChecker(bal, hc)

	// Step 1: config sync → DB
	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	// Step 2: SyncLLMTargets（从 DB 加载，UUID 进入 balancer）
	sp.SyncLLMTargets()

	// Step 3: 创建用户并发请求
	userRepo := db.NewUserRepo(gormDB, logger)
	require.NoError(t, userRepo.Create(&db.User{
		ID: "usr-001", Username: "alice", PasswordHash: "x", IsActive: true,
	}))

	code := doRequest(t, sp, jwtMgr, "usr-001", "alice", "")
	assert.Equal(t, http.StatusOK, code)

	// 关键断言：后端收到的 Authorization 应是正确的 key，不应为空
	gotAuth := cs.lastAuth()
	assert.Equal(t, "Bearer "+wantKey, gotAuth,
		"核心 Bug: SyncLLMTargets 后 llmTargetInfoForID 应能还原 APIKey，不应为空")
}

// TestIssue2_TwoServices_SameProvider_CorrectKeyRouting 模拟 Issue #2 最典型场景：
// 同一 provider（openai）配置两个不同 URL（百炼、火山），各自有不同的 key。
// 验证请求被路由到正确 URL 时，使用该 URL 对应的 key（而非被覆盖或丢失）。
func TestIssue2_TwoServices_SameProvider_CorrectKeyRouting(t *testing.T) {
	const (
		keyBailian = "sk-bailian-key-abc123"
		keyHuoshan = "sk-huoshan-key-xyz789"
	)

	// 两个独立的后端服务
	csBailian := newCaptureServer(t)
	csHuoshan := newCaptureServer(t)

	logger := zap.NewNop()
	jwtMgr, err := auth.NewManager(logger, "secret")
	require.NoError(t, err)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      csBailian.srv.URL,
					APIKey:   keyBailian,
					Provider: "openai",
					Name:     "Alibaba Bailian",
					Weight:   1,
				},
				{
					URL:      csHuoshan.srv.URL,
					APIKey:   keyHuoshan,
					Provider: "openai",
					Name:     "Volcano Huoshan",
					Weight:   1,
				},
			},
		},
	}

	// 初始启动：两个 target 都在 sp.targets 里（有 key）
	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: csBailian.srv.URL, APIKey: keyBailian, Provider: "openai"},
		{URL: csHuoshan.srv.URL, APIKey: keyHuoshan, Provider: "openai"},
	})
	require.NoError(t, err)
	sp.SetConfigAndDB(cfg, gormDB)

	bal := lb.NewWeightedRandom([]lb.Target{})
	hc := lb.NewHealthChecker(bal, logger)
	sp.SetLLMHealthChecker(bal, hc)

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))
	sp.SyncLLMTargets()

	// 验证 DB 中有 2 个 api_keys（不应被 provider 覆盖）
	var keyCount int64
	gormDB.Model(&db.APIKey{}).Count(&keyCount)
	assert.Equal(t, int64(2), keyCount,
		"Issue #2 核心：两个不同 URL 的 key 应各自独立存储，不应因 provider 相同而合并")

	// 验证 balancer 中有 2 个 target
	assert.Len(t, sp.llmBalancer.Targets(), 2,
		"两个不同 URL 的 target 应各自进入 balancer")

	// 发送多次请求，统计两个后端各自收到的 Authorization
	userRepo := db.NewUserRepo(gormDB, logger)
	require.NoError(t, userRepo.Create(&db.User{
		ID: "usr-multi", Username: "multi", PasswordHash: "x", IsActive: true,
	}))

	bailianHit, huoshanHit := 0, 0
	for i := 0; i < 50; i++ {
		doRequest(t, sp, jwtMgr, "usr-multi", "multi", "")
		// 检查哪个后端被打到
		csBailian.mu.Lock()
		bailianCount := len(csBailian.auths)
		csBailian.mu.Unlock()
		csHuoshan.mu.Lock()
		huoshanCount := len(csHuoshan.auths)
		csHuoshan.mu.Unlock()
		bailianHit = bailianCount
		huoshanHit = huoshanCount
	}

	// 两个后端都应被路由到
	assert.Greater(t, bailianHit, 0, "百炼后端应被路由到")
	assert.Greater(t, huoshanHit, 0, "火山后端应被路由到")

	// 验证每个后端收到的是自己的 key
	csBailian.mu.Lock()
	for _, a := range csBailian.auths {
		assert.Equal(t, "Bearer "+keyBailian, a,
			"百炼后端收到的 key 应是 keyBailian，不应是 keyHuoshan 或为空")
	}
	csBailian.mu.Unlock()

	csHuoshan.mu.Lock()
	for _, a := range csHuoshan.auths {
		assert.Equal(t, "Bearer "+keyHuoshan, a,
			"火山后端收到的 key 应是 keyHuoshan，不应是 keyBailian 或为空")
	}
	csHuoshan.mu.Unlock()
}

// ---------------------------------------------------------------------------
// sp.targets 更新：SyncLLMTargets 必须同步更新 sp.targets
// ---------------------------------------------------------------------------

// TestIssue2_SpTargets_UpdatedAfterSync 验证：
// SyncLLMTargets 后，sp.targets 应包含所有活跃 target，且 ID 字段为 DB UUID。
// 这保证 llmTargetInfoForID 能按 UUID 找到 APIKey。
func TestIssue2_SpTargets_UpdatedAfterSync(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: "https://bailian.aliyun.com", APIKey: "sk-bailian", Provider: "openai", Weight: 1},
				{URL: "https://ark.volces.com", APIKey: "sk-huoshan", Provider: "openai", Weight: 1},
			},
		},
	}

	bal := lb.NewWeightedRandom([]lb.Target{})
	hc := lb.NewHealthChecker(bal, logger)
	sp := &SProxy{
		cfg:         cfg,
		db:          gormDB,
		logger:      logger,
		llmBalancer: bal,
		llmHC:       hc,
		// targets 初始为空（模拟未从 yaml 初始化的情况）
		targets: []LLMTarget{},
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))
	sp.SyncLLMTargets()

	// 验证 sp.targets 已被更新
	require.Len(t, sp.targets, 2,
		"SyncLLMTargets 后 sp.targets 应包含 2 个 target（不应为空）")

	// 验证每个 target 的 ID 是 DB UUID（不是 URL，也不是空）
	for _, tgt := range sp.targets {
		assert.NotEmpty(t, tgt.ID, "sp.targets 中的 target 应有 DB UUID")
		assert.NotEqual(t, tgt.URL, tgt.ID, "ID 应为 UUID，不应等于 URL")
		assert.NotEmpty(t, tgt.APIKey, "sp.targets 中的 target 应有 APIKey")
	}

	// 验证 llmTargetInfoForID 能按 UUID 找到对应 target 的 APIKey
	for _, bal_tgt := range bal.Targets() {
		info := sp.llmTargetInfoForID(bal_tgt.ID)
		assert.NotEmpty(t, info.APIKey,
			"llmTargetInfoForID(%s) 应能找到 APIKey，不应返回空", bal_tgt.ID)
		assert.Equal(t, bal_tgt.Addr, info.URL,
			"llmTargetInfoForID 返回的 URL 应与 balancer 中的 Addr 一致")
	}
}

// TestIssue2_SpTargets_LLMTargetInfoConsistency 验证 llmTargetInfoForID 与 balancer 的一致性：
// balancer 中每个 target 的 ID，都能在 sp.targets 中找到对应的 APIKey。
func TestIssue2_SpTargets_LLMTargetInfoConsistency(t *testing.T) {
	const (
		urlA   = "https://api-a.example.com"
		urlB   = "https://api-b.example.com"
		keyA   = "sk-key-for-service-a"
		keyB   = "sk-key-for-service-b"
	)

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: urlA, APIKey: keyA, Provider: "openai", Weight: 1},
				{URL: urlB, APIKey: keyB, Provider: "openai", Weight: 1},
			},
		},
	}

	bal := lb.NewWeightedRandom([]lb.Target{})
	sp := &SProxy{
		cfg:         cfg,
		db:          gormDB,
		logger:      logger,
		llmBalancer: bal,
		llmHC:       lb.NewHealthChecker(bal, logger),
		targets:     []LLMTarget{},
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))
	sp.SyncLLMTargets()

	// 建立 URL → 期望 key 的映射
	expectedKeys := map[string]string{
		urlA: keyA,
		urlB: keyB,
	}

	// 验证每个 balancer target 都能查到正确的 key
	balTargets := bal.Targets()
	require.Len(t, balTargets, 2)

	for _, bt := range balTargets {
		info := sp.llmTargetInfoForID(bt.ID)
		wantKey, ok := expectedKeys[bt.Addr]
		require.True(t, ok, "未知的 Addr: %s", bt.Addr)
		assert.Equal(t, wantKey, info.APIKey,
			"URL=%s 的 target，llmTargetInfoForID 应返回正确的 key", bt.Addr)
	}
}

// ---------------------------------------------------------------------------
// Name 碰撞 Bug：resolveAPIKeyID 生成的 Name 可能与现有记录冲突
// ---------------------------------------------------------------------------

// TestIssue2_AutoName_Collision_SameProvider_SimilarKeys 验证 Name 碰撞 Bug：
// 两个不同 API key，但 obfuscated 后的末 8 位相同（或手工构造等价场景），
// 第二次 Create 会因 Name uniqueIndex 失败。
//
// obfuscateKey 逻辑：保留最后一个 "-" 前的前缀，对后缀执行 swapFirstLast。
// 如果两个 key 有相同前缀和相同长度后缀，且末 8 位相同，则 Name 会碰撞。
func TestIssue2_AutoName_Collision_SameProvider_SimilarKeys(t *testing.T) {
	// 构造两个 key：前缀相同，后缀不同但 obfuscated 末 8 位相同
	// obfuscateKey("sk-ant-ABCDEFGH") = "sk-ant-HBCDEFGA" (swap A↔H)
	// obfuscateKey("sk-ant-XBCDEFGX") = "sk-ant-XBCDEFGX" (swap X↔X = no change if X==X)
	// 更直接：末 8 位相同的情况
	// key1: "sk-openai-12345678"  obfuscated末8: "87654321" -> 取末8 = "87654321"
	// key2: "sk-openai-87654321"  obfuscated末8: "12345678" -> 末8 = "12345678"
	// 不容易直接碰，但可以用具体值验证 Name 生成逻辑
	//
	// 实际上最可能的碰撞：两个长度完全相同且只有特定位不同的 key
	// 为测试目的，我们直接测试 Name 生成是否唯一化（用 URL 区分）

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	sp := &SProxy{db: gormDB, logger: logger}

	// 这两个 key 的 obfuscated 形式的末 8 位完全相同（构造碰撞场景）
	// obfuscateKey 对于后缀 "XXXXXXXX"（8个X）: swapFirstLast("XXXXXXXX") = "XXXXXXXX"（不变）
	// 所以 "sk-openai-XXXXXXXX" 和 "sk-other-XXXXXXXX" 的 suffix 都是 "XXXXXXXX"
	// 但 provider 不同，Name = "Auto-openai-XXXXXXXX" vs "Auto-other-XXXXXXXX" 不碰撞
	// 要碰撞需要同 provider 且 suffix 相同

	// 构造：同 provider，suffix 相同（8位全相同字符）
	key1 := "sk-openai-XXXXXXXX" // obfuscated = "sk-openai-XXXXXXXX"（X swap X）, suffix = "XXXXXXXX"
	key2 := "sk-openai-YXXXXXXY" // obfuscated = "sk-openai-YXXXXXXY" -> swapFirstLast("YXXXXXXY") = "YXXXXXXY"
	// 末8: "YXXXXXXY" != "XXXXXXXX" → 不碰撞
	// 真正碰撞：key1="sk-p-ABCDEFGH" key2="sk-p-HBCDEFGA"
	// obfuscate("sk-p-ABCDEFGH") = "sk-p-" + swapFirstLast("ABCDEFGH") = "sk-p-HBCDEFGA"
	// obfuscate("sk-p-HBCDEFGA") = "sk-p-" + swapFirstLast("HBCDEFGA") = "sk-p-ABCDEFGH"
	// 两者的 obfuscated 末8: "HBCDEFGA" vs "ABCDEFGH" → 不同 → 不碰撞
	// 碰撞条件：obfuscate(k1)末8 == obfuscate(k2)末8
	// 即 swapFirstLast(suffix1) 末8 == swapFirstLast(suffix2) 末8
	// 最简单：suffix1 = suffix2 且长度≥8（同 key 被写两次，但 (provider,encrypted_value) 会复用）
	// 真正的碰撞场景：两个不同的 key，obfuscated 结果末8相同
	// obfuscate("sk-p-12345678") → "sk-p-" + swapFirstLast("12345678") = "sk-p-82345671"
	// obfuscate("sk-p-82345671") → "sk-p-" + swapFirstLast("82345671") = "sk-p-12345678"
	// 末8: "82345671" vs "12345678" → 不同
	// 结论：对于长度>8的后缀，swapFirstLast 只影响首尾，末8仅在原key末8完全相同时才碰撞
	// 即：key1和key2的原文末8相同，且前缀相同 → obfuscated末8也相同 → Name碰撞
	key1 = "sk-openai-prefix-ABCXXXXX"
	key2 = "sk-openai-suffix-ABCXXXXX" // 末8相同，但前缀不同，Name 仍碰撞（suffix取末8）

	// 验证 obfuscated 末8相同（确认测试数据正确）
	obf1 := obfuscateKey(key1)
	obf2 := obfuscateKey(key2)
	suffix1 := obf1
	if len(suffix1) > 8 {
		suffix1 = suffix1[len(suffix1)-8:]
	}
	suffix2 := obf2
	if len(suffix2) > 8 {
		suffix2 = suffix2[len(suffix2)-8:]
	}
	t.Logf("key1 obfuscated suffix8: %q", suffix1)
	t.Logf("key2 obfuscated suffix8: %q", suffix2)

	if suffix1 != suffix2 {
		t.Skipf("测试数据未构造出碰撞场景，跳过（suffix1=%q != suffix2=%q）", suffix1, suffix2)
	}

	// 第一个 key 写入成功
	id1, err := sp.resolveAPIKeyID(key1, "openai", "https://url-1.example.com")
	require.NoError(t, err, "第一个 key 应写入成功")
	require.NotNil(t, id1)

	// 第二个 key 写入应失败（Name 碰撞）或修复后成功（Name 含 URL 区分）
	id2, err := sp.resolveAPIKeyID(key2, "openai", "https://url-2.example.com")
	if err != nil {
		t.Logf("Name 碰撞 Bug 确认：第二个 key 因 Name=%q 冲突写入失败: %v",
			fmt.Sprintf("Auto-openai-%s", suffix2), err)
		t.Fail() // 修复后此处不应报错
	} else {
		require.NotNil(t, id2)
		assert.NotEqual(t, *id1, *id2, "两个不同的 key 应有不同的 APIKey ID")
		t.Log("Name 碰撞 Bug 已修复：两个 key 均成功写入")
	}
}

// TestIssue2_AutoName_Uniqueness_Always 验证 Name 碰撞修复：
// 即使两个不同 key 的 obfuscated 末8相同，Name 也应保证唯一性（如包含 UUID）。
func TestIssue2_AutoName_Uniqueness_Always(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	sp := &SProxy{db: gormDB, logger: logger}

	// 写入 20 个不同的 key（相同 provider），验证都能成功写入（Name 不碰撞）
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("sk-openai-key-%04d-suffix", i)
		id, err := sp.resolveAPIKeyID(key, "openai", fmt.Sprintf("https://url-%d.example.com", i))
		assert.NoError(t, err, "第 %d 个 key 写入不应失败（Name 碰撞）", i)
		assert.NotNil(t, id)
	}

	// 验证 20 个 key 都成功写入
	var count int64
	gormDB.Model(&db.APIKey{}).Count(&count)
	assert.Equal(t, int64(20), count, "20 个不同的 key 应全部写入 DB")
}

// ---------------------------------------------------------------------------
// 同 URL 多 Key 场景（Issue #2 扩展：多账号同一服务商）
// ---------------------------------------------------------------------------

// TestIssue2_SameURL_MultipleKeys_E2E 验证同一 URL 配置两个 key（多账号同一服务商）的完整流程：
// 1. config sync：同 URL 两个 key → DB 中有 2 个 llm_targets
// 2. SyncLLMTargets：balancer 中有 2 个 UUID 不同的 target
// 3. 请求：两个 target 都能被路由，各自的 key 正确注入
func TestIssue2_SameURL_MultipleKeys_E2E(t *testing.T) {
	const (
		keyAccountA = "sk-ant-account-a-key"
		keyAccountB = "sk-ant-account-b-key"
	)

	cs := newCaptureServer(t) // 两个 target 指向同一个后端（测试用）

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: cs.srv.URL, APIKey: keyAccountA, Provider: "anthropic", Name: "Account A", Weight: 1},
				{URL: cs.srv.URL, APIKey: keyAccountB, Provider: "anthropic", Name: "Account B", Weight: 1},
			},
		},
	}

	bal := lb.NewWeightedRandom([]lb.Target{})
	sp := &SProxy{
		cfg:         cfg,
		db:          gormDB,
		logger:      logger,
		llmBalancer: bal,
		llmHC:       lb.NewHealthChecker(bal, logger),
		targets:     []LLMTarget{},
		transport:   http.DefaultTransport,
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	// 验证 DB 中有 2 个独立 target（同 URL 不同 key）
	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2, "同 URL 两个 key 应创建两个独立的 DB target")
	if len(targets) == 2 {
		require.NotNil(t, targets[0].APIKeyID)
		require.NotNil(t, targets[1].APIKeyID)
		assert.NotEqual(t, *targets[0].APIKeyID, *targets[1].APIKeyID,
			"两个 target 的 APIKeyID 应不同")
	}

	sp.SyncLLMTargets()

	// 验证 balancer 中有 2 个 target
	assert.Len(t, bal.Targets(), 2, "balancer 中应有 2 个 target")

	// 验证 sp.targets 被更新，每个 target 有正确的 APIKey
	assert.Len(t, sp.targets, 2, "sp.targets 应有 2 个 target")
	foundKeyA, foundKeyB := false, false
	for _, tgt := range sp.targets {
		if tgt.APIKey == keyAccountA {
			foundKeyA = true
		}
		if tgt.APIKey == keyAccountB {
			foundKeyB = true
		}
	}
	assert.True(t, foundKeyA, "sp.targets 中应包含 Account A 的 key")
	assert.True(t, foundKeyB, "sp.targets 中应包含 Account B 的 key")

	// 发送多次请求，两个 key 都应被使用（至少各出现一次）
	jwtMgr, err := auth.NewManager(logger, "secret")
	require.NoError(t, err)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	sp.jwtMgr = jwtMgr
	sp.writer = writer

	userRepo := db.NewUserRepo(gormDB, logger)
	require.NoError(t, userRepo.Create(&db.User{
		ID: "usr-e2e", Username: "e2e", PasswordHash: "x", IsActive: true,
	}))

	usedKeys := map[string]int{}
	for i := 0; i < 100; i++ {
		doRequest(t, sp, jwtMgr, "usr-e2e", "e2e", "")
	}
	cs.mu.Lock()
	for _, a := range cs.auths {
		usedKeys[a]++
	}
	cs.mu.Unlock()

	t.Logf("key 使用分布: %v", usedKeys)
	assert.Greater(t, usedKeys["Bearer "+keyAccountA], 0,
		"Account A 的 key 应被使用")
	assert.Greater(t, usedKeys["Bearer "+keyAccountB], 0,
		"Account B 的 key 应被使用")
}

// ---------------------------------------------------------------------------
// 热更新场景：先启动（空 balancer），再 SyncLLMTargets，再请求
// ---------------------------------------------------------------------------

// TestIssue2_HotReload_AddTargetAfterStart 验证热更新场景：
// 启动后通过 SyncLLMTargets 新增 target，立即对新请求生效。
// 这是用户在运行时通过 WebUI 添加新服务商的典型场景。
func TestIssue2_HotReload_AddTargetAfterStart(t *testing.T) {
	cs := newCaptureServer(t)
	const wantKey = "sk-hotreload-key-001"

	logger := zap.NewNop()
	jwtMgr, err := auth.NewManager(logger, "secret")
	require.NoError(t, err)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	// 启动时配置一个 placeholder target（防止 NewSProxy 报"no target"）
	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: cs.srv.URL, APIKey: wantKey, Provider: "anthropic"},
	})
	require.NoError(t, err)

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: cs.srv.URL, APIKey: wantKey, Provider: "anthropic", Weight: 1},
			},
		},
	}
	sp.SetConfigAndDB(cfg, gormDB)

	bal := lb.NewWeightedRandom([]lb.Target{})
	hc := lb.NewHealthChecker(bal, logger)
	sp.SetLLMHealthChecker(bal, hc)

	// Step 1: 首次 sync + SyncLLMTargets
	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))
	sp.SyncLLMTargets()

	// Step 2: 请求应使用正确的 key
	userRepo := db.NewUserRepo(gormDB, logger)
	require.NoError(t, userRepo.Create(&db.User{
		ID: "usr-hot", Username: "hotuser", PasswordHash: "x", IsActive: true,
	}))

	code := doRequest(t, sp, jwtMgr, "usr-hot", "hotuser", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Bearer "+wantKey, cs.lastAuth(),
		"热更新后首次请求应使用正确的 key")
}

// ---------------------------------------------------------------------------
// 覆盖率补充：resolveAPIKeyID 的幂等性和各种边界
// ---------------------------------------------------------------------------

// TestIssue2_ResolveAPIKeyID_Idempotent 验证相同 key 多次调用 resolveAPIKeyID 不创建重复记录
func TestIssue2_ResolveAPIKeyID_Idempotent(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	sp := &SProxy{db: gormDB, logger: logger}

	const key = "sk-openai-test-key-idempotent"
	const provider = "openai"
	const url = "https://api.openai.com/v1"

	id1, err := sp.resolveAPIKeyID(key, provider, url)
	require.NoError(t, err)
	require.NotNil(t, id1)

	id2, err := sp.resolveAPIKeyID(key, provider, url)
	require.NoError(t, err)
	require.NotNil(t, id2)

	assert.Equal(t, *id1, *id2, "相同 key 多次 resolveAPIKeyID 应返回同一个 ID")

	var count int64
	gormDB.Model(&db.APIKey{}).Count(&count)
	assert.Equal(t, int64(1), count, "相同 key 应只创建一条 api_keys 记录")
}

// TestIssue2_ResolveAPIKeyID_DifferentURL_SameKey_Reuses 验证：
// 同一 key 用于不同 URL（同 provider），应复用同一个 APIKey 记录
func TestIssue2_ResolveAPIKeyID_DifferentURL_SameKey_Reuses(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	sp := &SProxy{db: gormDB, logger: logger}

	const key = "sk-shared-key"
	id1, _ := sp.resolveAPIKeyID(key, "anthropic", "https://url1.com")
	id2, _ := sp.resolveAPIKeyID(key, "anthropic", "https://url2.com")

	require.NotNil(t, id1)
	require.NotNil(t, id2)
	assert.Equal(t, *id1, *id2, "同一 key 值在不同 URL 下应复用同一条 APIKey 记录")

	var count int64
	gormDB.Model(&db.APIKey{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

// TestIssue2_ResolveAPIKeyID_DifferentProvider_SameKey_Independent 验证：
// 同一 key 值但不同 provider，应创建独立的 APIKey 记录
func TestIssue2_ResolveAPIKeyID_DifferentProvider_SameKey_Independent(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))
	sp := &SProxy{db: gormDB, logger: logger}

	const key = "sk-same-value"
	id1, err1 := sp.resolveAPIKeyID(key, "anthropic", "https://url.com")
	id2, err2 := sp.resolveAPIKeyID(key, "openai", "https://url.com")

	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NotNil(t, id1)
	require.NotNil(t, id2)
	assert.NotEqual(t, *id1, *id2, "不同 provider 的相同 key 值应创建独立记录")

	var count int64
	gormDB.Model(&db.APIKey{}).Count(&count)
	assert.Equal(t, int64(2), count)
}

// TestIssue2_FullConfigSync_3Providers 验证 Issue #2 最初报告的场景：
// 3 个不同服务商（openai兼容）配置各自独立的 key，互不干扰
func TestIssue2_FullConfigSync_3Providers(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIKey: "sk-bailian-xxx", Provider: "openai", Name: "阿里百炼", Weight: 1},
				{URL: "https://ark.volces.com/api/v3", APIKey: "sk-huoshan-yyy", Provider: "openai", Name: "火山引擎", Weight: 1},
				{URL: "https://api.anthropic.com", APIKey: "sk-ant-zzz", Provider: "anthropic", Name: "Anthropic", Weight: 1},
			},
		},
	}

	sp := &SProxy{cfg: cfg, db: gormDB, logger: logger}
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// 执行同步
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	// 验证：3 个 target，3 个独立 APIKey
	allTargets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, allTargets, 3, "3 个不同 URL 的 target 应各自独立存储")

	var keyCount int64
	gormDB.Model(&db.APIKey{}).Count(&keyCount)
	assert.Equal(t, int64(3), keyCount,
		"Issue #2：3 个不同服务商的 key 应各自独立，不因 provider 相同而合并")

	// 验证每个 target 的 APIKeyID 都不相同
	keyIDs := map[string]bool{}
	for _, tgt := range allTargets {
		require.NotNil(t, tgt.APIKeyID)
		keyIDs[*tgt.APIKeyID] = true
	}
	assert.Len(t, keyIDs, 3, "3 个 target 的 APIKeyID 应各不相同")

	// 幂等性验证
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))
	allTargets2, _ := repo.ListAll()
	assert.Len(t, allTargets2, 3, "幂等同步后 target 数量不变")
	var keyCount2 int64
	gormDB.Model(&db.APIKey{}).Count(&keyCount2)
	assert.Equal(t, int64(3), keyCount2, "幂等同步后 APIKey 数量不变")
}

// TestIssue2_SyncCleanup_RemoveOneKey 验证：配置中删除一个 key 后，sync 精确删除该条 target
func TestIssue2_SyncCleanup_RemoveOneKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	// 初始：3 个 target
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{URL: "https://url-a.com", APIKey: "sk-a", Provider: "openai", Weight: 1},
				{URL: "https://url-b.com", APIKey: "sk-b", Provider: "openai", Weight: 1},
				{URL: "https://url-c.com", APIKey: "sk-c", Provider: "openai", Weight: 1},
			},
		},
	}
	sp := &SProxy{cfg: cfg, db: gormDB, logger: logger}
	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	targets, _ := repo.ListAll()
	require.Len(t, targets, 3)

	// 删除 url-b，模拟用户从 yaml 中删除一个服务商
	cfg.LLM.Targets = []config.LLMTarget{
		{URL: "https://url-a.com", APIKey: "sk-a", Provider: "openai", Weight: 1},
		{URL: "https://url-c.com", APIKey: "sk-c", Provider: "openai", Weight: 1},
	}
	sp.cfg = cfg
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	targets, _ = repo.ListAll()
	assert.Len(t, targets, 2, "删除一个 target 后，DB 中应只剩 2 个")

	// 验证剩下的是 url-a 和 url-c
	urls := map[string]bool{}
	for _, tgt := range targets {
		urls[tgt.URL] = true
	}
	assert.True(t, urls["https://url-a.com"])
	assert.True(t, urls["https://url-c.com"])
	assert.False(t, urls["https://url-b.com"], "url-b 应被精确删除")
}
