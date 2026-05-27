package proxy

// sproxy_keypool_test.go — 号池功能 Bug 回归测试
//
// 覆盖范围:
//   BUG-1: 启动路径用 URL 做 ID，同 URL 多 key 时 credentials 被覆盖
//   BUG-4: obfuscateKey 路径与 auth.Encrypt 路径存储格式不一致

import (
	"fmt"
	"testing"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// BUG-1: 启动路径 ID 一致性 —— 同 URL 多 key
// ---------------------------------------------------------------------------

// TestStartupPath_MultipleTargets_CredentialsIndependent 验证：
// 当两个 DB target 指向不同 URL 但使用不同 API key 时，
// loadAllTargets 应为每个 target 分别解析 APIKey，两个 key 独立存在不被覆盖。
// (URL 现为全局唯一，每个 target 有独立 URL)
func TestStartupPath_MultipleTargets_CredentialsIndependent(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	const targetURL1 = "https://api.openai.com/v1"
	const targetURL2 = "https://api.openai.com/v2"

	keyID1 := "key-uuid-aaa"
	keyID2 := "key-uuid-bbb"
	require.NoError(t, gormDB.Create(&db.APIKey{
		ID: keyID1, Name: "key-pool-1",
		EncryptedValue: obfuscateKey("sk-pool-key-one"),
		Provider: "openai", IsActive: true,
	}).Error)
	require.NoError(t, gormDB.Create(&db.APIKey{
		ID: keyID2, Name: "key-pool-2",
		EncryptedValue: obfuscateKey("sk-pool-key-two"),
		Provider: "openai", IsActive: true,
	}).Error)

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "target-uuid-111", URL: targetURL1,
		APIKeyID: &keyID1, Provider: "openai",
		Name: "pool-1", Weight: 1, Source: "database", IsActive: true,
	}))
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "target-uuid-222", URL: targetURL2,
		APIKeyID: &keyID2, Provider: "openai",
		Name: "pool-2", Weight: 1, Source: "database", IsActive: true,
	}))

	sp := &SProxy{db: gormDB, logger: logger}

	targets, err := sp.loadAllTargets(repo)
	require.NoError(t, err)
	require.Len(t, targets, 2, "应加载 2 个 target")

	// 两个 target 的 APIKey 应不同
	apiKeys := map[string]bool{}
	for _, tgt := range targets {
		assert.NotEmpty(t, tgt.APIKey, "每个 target 都应有 APIKey")
		assert.NotEmpty(t, tgt.ID, "每个 target 都应有 UUID（不应为空）")
		apiKeys[tgt.APIKey] = true
	}
	assert.Len(t, apiKeys, 2, "两个 target 的 APIKey 应不同")

	// 验证 UUID 与 URL 不同
	for _, tgt := range targets {
		assert.NotEqual(t, tgt.ID, tgt.URL,
			"target ID 应为 UUID，不应等于 URL")
	}
}

// TestStartupPath_MultipleTargets_BalancerCanPickBoth 验证：
// balancer 能轮换选择两个不同 URL 的 target。
// (URL 现为全局唯一，每个 target 有独立 URL)
func TestStartupPath_MultipleTargets_BalancerCanPickBoth(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	const targetURL1 = "https://api.openai.com/v1"
	const targetURL2 = "https://api.openai.com/v2"

	keyID1, keyID2 := "key-aaa", "key-bbb"
	for _, kid := range []string{keyID1, keyID2} {
		require.NoError(t, gormDB.Create(&db.APIKey{
			ID: kid, Name: kid,
			EncryptedValue: obfuscateKey("sk-" + kid),
			Provider: "openai", IsActive: true,
		}).Error)
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "t1", URL: targetURL1, APIKeyID: &keyID1,
		Provider: "openai", Weight: 1, Source: "database", IsActive: true,
	}))
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "t2", URL: targetURL2, APIKeyID: &keyID2,
		Provider: "openai", Weight: 1, Source: "database", IsActive: true,
	}))

	sp := &SProxy{db: gormDB, logger: logger,
		llmBalancer: lb.NewWeightedRandom([]lb.Target{}),
		llmHC:       lb.NewHealthChecker(lb.NewWeightedRandom([]lb.Target{}), logger),
	}
	sp.SyncLLMTargets()

	// 多次 Pick，两个 target 都应该能被选到
	pickedIDs := map[string]bool{}
	for i := 0; i < 100; i++ {
		tgt, err := sp.llmBalancer.Pick()
		if err == nil {
			pickedIDs[tgt.ID] = true
		}
	}
	assert.True(t, pickedIDs["t1"], "target t1 应能被 Pick 到")
	assert.True(t, pickedIDs["t2"], "target t2 应能被 Pick 到")
}

// ---------------------------------------------------------------------------
// BUG-4: obfuscateKey 路径 vs auth.Encrypt 路径不一致
// ---------------------------------------------------------------------------

// TestObfuscateKey_IsSymmetric 验证 obfuscateKey 的对称性（基础正确性）。
func TestObfuscateKey_IsSymmetric(t *testing.T) {
	cases := []string{
		"sk-ant-api01-very-long-key-here",
		"a",
		"ab",
		"abc",
		"sk-openai-key",
	}
	for _, key := range cases {
		result := obfuscateKey(obfuscateKey(key))
		assert.Equal(t, key, result, "obfuscateKey 应为对称操作: obfuscateKey(obfuscateKey(x)) == x")
	}
}

// TestConfigTarget_APIKeyRoundtrip_ObfuscateConsistent 验证 config target 的 key 存取一致性:
// config sync 时用 obfuscateKey 写入，loadAllTargets 时用 obfuscateKey 读出，
// 两次 obfuscate 是对称操作，能还原原始 key。
func TestConfigTarget_APIKeyRoundtrip_ObfuscateConsistent(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	const originalKey = "sk-ant-config-key-original"
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      "https://api.anthropic.com",
					APIKey:   originalKey,
					Provider: "anthropic",
					Name:     "test-target",
					Weight:   1,
				},
			},
		},
	}

	sp := &SProxy{cfg: cfg, db: gormDB, logger: logger}
	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Config sync 写入（用 obfuscateKey 存储）
	require.NoError(t, sp.syncConfigTargetsToDatabase(repo))

	// loadAllTargets 读取（用 obfuscateKey 还原）
	targets, err := sp.loadAllTargets(repo)
	require.NoError(t, err)
	require.Len(t, targets, 1)

	// 还原后的 key 应等于原始 key
	assert.Equal(t, originalKey, targets[0].APIKey,
		"config target 的 APIKey 经 obfuscateKey 存取后应能正确还原")
}

// TestAdminAPIKey_AES_vs_ConfigKey_Obfuscate_Incompatibility 验证 BUG-4:
// Admin API 创建的 key 使用 auth.Encrypt（AES-256-GCM）存储，
// 但 resolveAPIKey（sproxy.go:486）用 obfuscateKey 读取，会得到乱码。
// 此测试验证两种加密格式确实不兼容（当前状态，证明 bug 存在）。
func TestAdminAPIKey_AES_vs_ConfigKey_Obfuscate_Incompatibility(t *testing.T) {
	const kek = "test-key-encryption-key-32chars!"
	const originalKey = "sk-ant-real-api-key-here"

	// Admin API 路径：用 AES 加密后存入 DB
	aesEncrypted, err := auth.Encrypt(originalKey, kek)
	require.NoError(t, err)

	// config sync 路径：用 obfuscate 加密后存入 DB
	obfuscated := obfuscateKey(originalKey)

	// resolveAPIKey 的读取逻辑：统一用 obfuscateKey 解读
	// 对 obfuscated 值：obfuscateKey(obfuscated) == originalKey（正确）
	fromObfuscate := obfuscateKey(obfuscated)
	assert.Equal(t, originalKey, fromObfuscate,
		"config sync 路径：obfuscateKey 读取 obfuscated 值能还原")

	// 对 aesEncrypted 值：obfuscateKey(aesEncrypted) != originalKey（BUG-4）
	fromAESWithObfuscate := obfuscateKey(aesEncrypted)
	assert.NotEqual(t, originalKey, fromAESWithObfuscate,
		"BUG-4 验证：Admin API 创建的 AES 密文，用 obfuscateKey 读取得到乱码，不等于原始 key")

	// 正确读取 AES 加密 key 的方式是 auth.Decrypt
	fromAESDecrypt, err := auth.Decrypt(aesEncrypted, kek)
	require.NoError(t, err)
	assert.Equal(t, originalKey, fromAESDecrypt,
		"Admin API key 应通过 auth.Decrypt 正确还原")
}

// TestLoadAllTargets_AdminAPIKey_ReturnsCorrectAPIKey 验证 BUG-4 修复后:
// 通过 Admin API（auth.Encrypt）存入的 key，loadAllTargets 应能正确还原。
// 注意：此测试在修复前会失败（返回乱码 key）。
func TestLoadAllTargets_AdminAPIKey_ReturnsCorrectAPIKey(t *testing.T) {
	const kek = "test-kek-32chars-padding-here!!!"
	const originalKey = "sk-ant-admin-api-key"

	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	// 模拟 Admin API 创建 key：用 auth.Encrypt 加密后写入 DB
	encrypted, err := auth.Encrypt(originalKey, kek)
	require.NoError(t, err)

	keyID := "admin-key-001"
	require.NoError(t, gormDB.Create(&db.APIKey{
		ID:             keyID,
		Name:           "admin-created-key",
		EncryptedValue: encrypted, // AES 加密
		KeyScheme:      "aes",
		Provider:       "anthropic",
		IsActive:       true,
	}).Error)

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID:       "target-admin-001",
		URL:      "https://api.anthropic.com",
		APIKeyID: &keyID,
		Provider: "anthropic",
		Name:     "admin-target",
		Weight:   1,
		Source:   "database",
		IsActive: true,
	}))

	sp := &SProxy{db: gormDB, logger: logger}
	// BUG-4 修复：注入 keyDecryptFn，使 resolveAPIKey 能正确处理 AES 加密的 key
	sp.SetKeyDecryptFn(func(encrypted string) (string, error) {
		return auth.Decrypt(encrypted, kek)
	})

	targets, err := sp.loadAllTargets(repo)
	require.NoError(t, err)
	require.Len(t, targets, 1)

	// 修复后：targets[0].APIKey == originalKey
	assert.Equal(t, originalKey, targets[0].APIKey,
		"BUG-4: Admin API 创建的 key（AES加密）在 loadAllTargets 中应能正确还原（需要 auth.Decrypt）")
}

// ---------------------------------------------------------------------------
// BUG-1 补充: SyncLLMTargets 路径（已修复的参照）
// ---------------------------------------------------------------------------

// TestSyncLLMTargets_SameURL_TwoKeys_UseUUID 是 BUG-1 的 SyncLLMTargets 层验证:
// SyncLLMTargets 已经使用 UUID 做 targetID（而非 URL），同 URL 多 key 可正常工作。
// 这个测试验证"正确路径"，对比启动路径的 BUG-1。
// TestSyncLLMTargets_TwoTargets_UseUUID 验证 SyncLLMTargets 使用 UUID 而非 URL 作为 balancer ID。
// (URL 现为全局唯一，每个 target 有独立 URL)
func TestSyncLLMTargets_TwoTargets_UseUUID(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	const targetURL1 = "https://api.volcengine.com/v1"
	const targetURL2 = "https://api.volcengine.com/v2"

	keyID1, keyID2 := "vol-key-1", "vol-key-2"
	for i, kid := range []string{keyID1, keyID2} {
		require.NoError(t, gormDB.Create(&db.APIKey{
			ID: kid, Name: fmt.Sprintf("vol-key-%d", i+1),
			EncryptedValue: obfuscateKey(fmt.Sprintf("sk-vol-%d", i+1)),
			Provider: "openai", IsActive: true,
		}).Error)
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "vol-t1", URL: targetURL1, APIKeyID: &keyID1,
		Provider: "openai", Weight: 1, Source: "database", IsActive: true,
	}))
	require.NoError(t, repo.Create(&db.LLMTarget{
		ID: "vol-t2", URL: targetURL2, APIKeyID: &keyID2,
		Provider: "openai", Weight: 1, Source: "database", IsActive: true,
	}))

	bal := lb.NewWeightedRandom([]lb.Target{})
	hc := lb.NewHealthChecker(bal, logger)
	sp := &SProxy{db: gormDB, logger: logger, llmBalancer: bal, llmHC: hc}

	sp.SyncLLMTargets()

	targets := sp.llmBalancer.Targets()
	assert.Len(t, targets, 2, "SyncLLMTargets 应创建两个独立 balancer 条目")
	if len(targets) == 2 {
		assert.NotEqual(t, targets[0].ID, targets[1].ID)
		assert.NotEqual(t, targets[0].ID, targetURL1, "balancer ID 应为 UUID，不应是 URL")
	}
}
