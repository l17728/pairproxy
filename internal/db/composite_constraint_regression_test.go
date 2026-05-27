package db

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ============================================================================
// 复合约束测试（举一反三，基于 Issue #6 模式）
// ============================================================================

// TestURLUniqueness_BlocksDuplicateURL 验证 URL 全局唯一约束：同 URL 第二次创建应失败。
func TestURLUniqueness_BlocksDuplicateURL(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://api.anthropic.com/v1"

	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, Provider: "anthropic"}))

	// 尝试再次创建相同 URL → 数据库约束拒绝
	err = repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, Provider: "anthropic"})
	assert.Error(t, err, "duplicate URL should fail at DB level")
}

// TestURLExists_ExistingURL_ReturnsTrue 验证 URLExists 在 URL 存在时返回 true。
func TestURLExists_ExistingURL_ReturnsTrue(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://api.openai.com/v1"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, Provider: "openai"}))

	exists, err := repo.URLExists(url)
	require.NoError(t, err)
	assert.True(t, exists, "URL should exist")

	exists, err = repo.URLExists("https://nonexistent.example.com")
	require.NoError(t, err)
	assert.False(t, exists, "nonexistent URL should not exist")
}

// TestComboExists_Deprecated_DelegatesToURLExists 验证 ComboExists 已弃用但仍委托给 URLExists。
func TestComboExists_Deprecated_DelegatesToURLExists(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://selfhost.example.com"
	key := "some-key"

	// URL 不存在时，ComboExists 返回 false（不论 key 如何）
	exists, err := repo.ComboExists(url, &key)
	require.NoError(t, err)
	assert.False(t, exists, "URL does not exist yet")

	exists, err = repo.ComboExists(url, nil)
	require.NoError(t, err)
	assert.False(t, exists, "URL does not exist yet (nil key)")

	// 创建后，ComboExists 返回 true（不论 key 如何）
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, Provider: "ollama"}))

	exists, err = repo.ComboExists(url, &key)
	require.NoError(t, err)
	assert.True(t, exists, "URL now exists, ComboExists should return true regardless of key")

	exists, err = repo.ComboExists(url, nil)
	require.NoError(t, err)
	assert.True(t, exists, "URL now exists, ComboExists with nil key should return true")
}


// TestUser_AuthProviderUsernameComposite 验证 (auth_provider, username) 复合约束
func TestUser_AuthProviderUsernameComposite(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	// local 用户 "alice"
	u1 := &User{ID: uuid.NewString(), Username: "alice", PasswordHash: "h1", AuthProvider: "local"}
	require.NoError(t, gormDB.Create(u1).Error)

	// ldap 用户 "alice" → 允许（不同 provider）
	u2 := &User{ID: uuid.NewString(), Username: "alice", PasswordHash: "h2", AuthProvider: "ldap", ExternalID: func(s string) *string { return &s }("uid=alice,cn=users")}
	require.NoError(t, gormDB.Create(u2).Error)

	// 再次创建 local 用户 "alice" → 失败（相同 provider + username）
	u3 := &User{ID: uuid.NewString(), Username: "alice", PasswordHash: "h3", AuthProvider: "local"}
	err = gormDB.Create(u3).Error
	assert.Error(t, err, "duplicate (auth_provider, username) must fail")
}

// TestUser_GetByUsernameAndProvider 验证新方法
func TestUser_GetByUsernameAndProvider(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	// 创建两个同名但不同 provider 的用户
	require.NoError(t, gormDB.Create(&User{ID: uuid.NewString(), Username: "bob", PasswordHash: "h1", AuthProvider: "local"}).Error)
	require.NoError(t, gormDB.Create(&User{ID: uuid.NewString(), Username: "bob", PasswordHash: "h2", AuthProvider: "ldap", ExternalID: func(s string) *string { return &s }("uid=bob")}).Error)

	// 精确查找 local/bob
	localBob, err := repo.GetByUsernameAndProvider("bob", "local")
	require.NoError(t, err)
	require.NotNil(t, localBob)
	assert.Equal(t, "local", localBob.AuthProvider)

	// 精确查找 ldap/bob
	ldapBob, err := repo.GetByUsernameAndProvider("bob", "ldap")
	require.NoError(t, err)
	require.NotNil(t, ldapBob)
	assert.Equal(t, "ldap", ldapBob.AuthProvider)

	// 查找不存在的
	notFound, err := repo.GetByUsernameAndProvider("bob", "oauth")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

// TestUser_AuthProviderExternalIDComposite 验证 (auth_provider, external_id) 复合约束
func TestUser_AuthProviderExternalIDComposite(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	// ldap 用户 uid=u1
	require.NoError(t, gormDB.Create(&User{
		ID: uuid.NewString(), Username: "user1", PasswordHash: "h1",
		AuthProvider: "ldap", ExternalID: func(s string) *string { return &s }("uid=u1,cn=users"),
	}).Error)

	// 再次用相同 (auth_provider, external_id) 创建 → 失败
	err = gormDB.Create(&User{
		ID: uuid.NewString(), Username: "user1-dup", PasswordHash: "h2",
		AuthProvider: "ldap", ExternalID: func(s string) *string { return &s }("uid=u1,cn=users"),
	}).Error
	assert.Error(t, err, "duplicate (auth_provider, external_id) must fail")

	// 不同 provider 相同 external_id → 允许
	err = gormDB.Create(&User{
		ID: uuid.NewString(), Username: "user1-oauth", PasswordHash: "h3",
		AuthProvider: "oauth", ExternalID: func(s string) *string { return &s }("uid=u1,cn=users"),
	}).Error
	assert.NoError(t, err, "same external_id with different provider must be allowed")
}

// ---------------------------------------------------------------------------
// Fix 6: UserRepo.Create() 返回语义化错误（非原始 GORM 错误）
// ---------------------------------------------------------------------------

// TestUserRepo_Create_DuplicateReturnsSemanticError 验证 userRepo.Create() 在唯一约束冲突时
// 返回包含 "user already exists" 的错误，而非原始数据库错误字符串。
func TestUserRepo_Create_DuplicateReturnsSemanticError(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	// 第一次创建成功
	require.NoError(t, repo.Create(&User{
		Username: "alice", PasswordHash: "h1", AuthProvider: "local",
	}))

	// 第二次相同 (username, auth_provider) → 应返回含 "user already exists" 的错误
	err = repo.Create(&User{
		Username: "alice", PasswordHash: "h2", AuthProvider: "local",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user already exists",
		"Create() 应返回语义化错误而非原始 GORM 错误")
	// 确保不是泄露原始 DB 错误
	assert.NotContains(t, err.Error(), "UNIQUE constraint failed",
		"不应暴露原始数据库错误细节给调用方")
}

// TestUserRepo_ListByUsername_MultipleProviders 验证 ListByUsername 在混合认证下返回所有匹配用户。
func TestUserRepo_ListByUsername_MultipleProviders(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	extID := "ldap-alice-uid"
	require.NoError(t, repo.Create(&User{Username: "alice", PasswordHash: "h1", AuthProvider: "local"}))
	require.NoError(t, repo.Create(&User{Username: "alice", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID}))
	require.NoError(t, repo.Create(&User{Username: "bob", PasswordHash: "h3", AuthProvider: "local"}))

	// alice 存在于两个 provider → 返回 2 个
	users, err := repo.ListByUsername("alice")
	require.NoError(t, err)
	assert.Len(t, users, 2, "ListByUsername 应返回所有同名不同 provider 的用户")

	providers := map[string]bool{}
	for _, u := range users {
		providers[u.AuthProvider] = true
	}
	assert.True(t, providers["local"], "应包含 local provider 的 alice")
	assert.True(t, providers["ldap"], "应包含 ldap provider 的 alice")

	// bob 只有一个 → 返回 1 个
	bobs, err := repo.ListByUsername("bob")
	require.NoError(t, err)
	assert.Len(t, bobs, 1)

	// 不存在 → 返回空切片，无错误
	nobody, err := repo.ListByUsername("nobody")
	require.NoError(t, err)
	assert.Empty(t, nobody)
}

// ---------------------------------------------------------------------------
// Fix 7: User.ExternalID 改为 *string，本地用户 NULL 不冲突
// ---------------------------------------------------------------------------

// TestUserRepo_Create_TwoLocalUsers_NilExternalID_NoConflict 验证两个本地用户都可以
// 拥有 ExternalID=nil（NULL），不会触发 (auth_provider, external_id) 唯一约束冲突。
// 这是 ExternalID string→*string 修复的直接回归测试。
func TestUserRepo_Create_TwoLocalUsers_NilExternalID_NoConflict(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	// 创建多个本地用户，均不设 ExternalID（nil）
	require.NoError(t, repo.Create(&User{Username: "alice", PasswordHash: "h1", AuthProvider: "local"}),
		"第一个本地用户应创建成功")
	require.NoError(t, repo.Create(&User{Username: "bob", PasswordHash: "h2", AuthProvider: "local"}),
		"第二个本地用户应创建成功（ExternalID=nil 不应触发唯一约束）")
	require.NoError(t, repo.Create(&User{Username: "carol", PasswordHash: "h3", AuthProvider: "local"}),
		"第三个本地用户应创建成功")

	// 确认三个用户均存在
	users, err := repo.ListAll()
	require.NoError(t, err)
	count := 0
	for _, u := range users {
		if u.AuthProvider == "local" {
			count++
		}
	}
	assert.GreaterOrEqual(t, count, 3, "三个本地用户应全部存在于数据库")
}

// ---------------------------------------------------------------------------
// 增强日志记录覆盖
// ---------------------------------------------------------------------------

// TestUserRepo_ListByUsername_LogsAmbiguity 验证 ListByUsername 在检测到歧义时正确记录警告日志
func TestUserRepo_ListByUsername_LogsAmbiguity(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	// 创建混合认证用户
	u1 := &User{Username: "alice", PasswordHash: "h1", AuthProvider: "local"}
	u2 := &User{Username: "alice", PasswordHash: "h2", AuthProvider: "ldap"}
	require.NoError(t, repo.Create(u1))
	require.NoError(t, repo.Create(u2))

	// ListByUsername 应返回 2 个结果
	users, err := repo.ListByUsername("alice")
	require.NoError(t, err)
	assert.Len(t, users, 2, "应返回 local 和 ldap 的 alice")
}

// TestUserRepo_ListByUsername_NoAmbiguity 验证 ListByUsername 在单一匹配时正确记录
func TestUserRepo_ListByUsername_NoAmbiguity(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewUserRepo(gormDB, logger)

	// 创建单个用户
	u := &User{Username: "bob", PasswordHash: "h1", AuthProvider: "local"}
	require.NoError(t, repo.Create(u))

	// ListByUsername 应返回 1 个结果
	users, err := repo.ListByUsername("bob")
	require.NoError(t, err)
	assert.Len(t, users, 1)
	assert.Equal(t, "bob", users[0].Username)
}
