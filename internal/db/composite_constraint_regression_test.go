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

// TestComboExists_SameURL_DifferentKey 验证 ComboExists 正确区分同 URL 不同 APIKey
func TestComboExists_SameURL_DifferentKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://api.openai.com/v1"
	key1 := "key-aaaa"
	key2 := "key-bbbb"

	// 创建 (url, key1) target
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &key1, Provider: "openai"}))

	// (url, key1) 应该存在
	exists, err := repo.ComboExists(url, &key1)
	require.NoError(t, err)
	assert.True(t, exists, "(url, key1) should exist")

	// (url, key2) 不应该存在
	exists, err = repo.ComboExists(url, &key2)
	require.NoError(t, err)
	assert.False(t, exists, "(url, key2) should NOT exist")

	// (url, nil) 不应该存在（无 key）
	exists, err = repo.ComboExists(url, nil)
	require.NoError(t, err)
	assert.False(t, exists, "(url, nil) should NOT exist")
}

// TestComboExists_NilAPIKey 验证 nil apiKeyID 的 IS NULL 查询
func TestComboExists_NilAPIKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://selfhost.example.com"

	// 创建无 APIKey 的 target
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: nil, Provider: "ollama"}))

	// (url, nil) 应该存在
	exists, err := repo.ComboExists(url, nil)
	require.NoError(t, err)
	assert.True(t, exists, "(url, nil) should exist")

	// (url, someKey) 不应该存在
	key := "some-key"
	exists, err = repo.ComboExists(url, &key)
	require.NoError(t, err)
	assert.False(t, exists, "(url, non-nil key) should NOT exist when only nil-key target exists")
}

// TestComboExists_SameURLSameKey_BlocksDuplicate 验证重复复合键被阻止
func TestComboExists_SameURLSameKey_BlocksDuplicate(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	url := "https://api.anthropic.com/v1"
	key := "sk-ant-key123"

	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &key, Provider: "anthropic"}))

	// 尝试再次创建相同复合键 → 数据库约束拒绝
	err = repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &key, Provider: "anthropic"})
	assert.Error(t, err, "duplicate (url, api_key_id) should fail at DB level")
}

// TestGroupTargetSetMember_DuplicatePrevented 验证 (target_set_id, target_id) 复合约束
func TestGroupTargetSetMember_DuplicatePrevented(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewGroupTargetSetRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	setID := uuid.NewString()
	require.NoError(t, gormDB.Create(&GroupTargetSet{ID: setID, Name: "test-set", GroupID: nil}).Error)

	targetID := uuid.NewString()
	require.NoError(t, targetRepo.Create(&LLMTarget{ID: targetID, URL: "http://test.local", Provider: "anthropic"}))

	// 第一次添加 → 成功
	member1 := &GroupTargetSetMember{ID: uuid.NewString(), TargetSetID: setID, TargetID: targetID, Weight: 1}
	require.NoError(t, repo.AddMember(setID, member1))

	// 第二次添加相同 (set, target) → 应该失败
	member2 := &GroupTargetSetMember{ID: uuid.NewString(), TargetSetID: setID, TargetID: targetID, Weight: 2}
	err = repo.AddMember(setID, member2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target already in set")
}

// TestGroupTargetSetMember_DifferentSets_Allowed 同一 target 在不同 set 中可以存在
func TestGroupTargetSetMember_DifferentSets_Allowed(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewGroupTargetSetRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	setID1 := uuid.NewString()
	setID2 := uuid.NewString()
	require.NoError(t, gormDB.Create(&GroupTargetSet{ID: setID1, Name: "set-a", GroupID: nil}).Error)
	require.NoError(t, gormDB.Create(&GroupTargetSet{ID: setID2, Name: "set-b", GroupID: nil}).Error)

	targetID := uuid.NewString()
	require.NoError(t, targetRepo.Create(&LLMTarget{ID: targetID, URL: "http://shared.local", Provider: "anthropic"}))

	// 同一 target 可以在不同 set 中出现
	m1 := &GroupTargetSetMember{ID: uuid.NewString(), TargetSetID: setID1, TargetID: targetID, Weight: 1}
	m2 := &GroupTargetSetMember{ID: uuid.NewString(), TargetSetID: setID2, TargetID: targetID, Weight: 1}
	require.NoError(t, repo.AddMember(setID1, m1))
	require.NoError(t, repo.AddMember(setID2, m2))
}

// TestGroupTargetSet_GroupIDNameComposite 验证 (group_id, name) 复合约束
func TestGroupTargetSet_GroupIDNameComposite(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	groupID := "group-1"

	// 同组内相同名称 → 失败
	err = gormDB.Create(&GroupTargetSet{ID: uuid.NewString(), GroupID: &groupID, Name: "my-set"}).Error
	require.NoError(t, err)
	err = gormDB.Create(&GroupTargetSet{ID: uuid.NewString(), GroupID: &groupID, Name: "my-set"}).Error
	assert.Error(t, err, "duplicate (group_id, name) must fail")

	// 不同组相同名称 → 允许
	groupID2 := "group-2"
	err = gormDB.Create(&GroupTargetSet{ID: uuid.NewString(), GroupID: &groupID2, Name: "my-set"}).Error
	assert.NoError(t, err, "same name in different group must be allowed")
}

// TestGroupTargetSet_GetByGroupIDAndName_NullGroup 验证 NULL group_id 的 IS NULL 查询
func TestGroupTargetSet_GetByGroupIDAndName_NullGroup(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewGroupTargetSetRepo(gormDB, logger)

	// 创建全局（group_id=NULL）的 set
	require.NoError(t, gormDB.Create(&GroupTargetSet{ID: uuid.NewString(), GroupID: nil, Name: "global-set"}).Error)

	// 通过 GetByGroupIDAndName(nil, name) 查找
	found, err := repo.GetByGroupIDAndName(nil, "global-set")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Nil(t, found.GroupID)
	assert.Equal(t, "global-set", found.Name)

	// 通过非 nil group ID 查找 → nil（不匹配全局 set）
	gid := "some-group"
	notFound, err := repo.GetByGroupIDAndName(&gid, "global-set")
	require.NoError(t, err)
	assert.Nil(t, notFound)
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
