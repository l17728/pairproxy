package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestLLMTargetRepo_Create(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	target := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://test.local:8080",
		Provider:   "anthropic",
		Name:       "Test Target",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err = repo.Create(target)
	assert.NoError(t, err)

	// 验证创建成功
	found, err := repo.GetByURL(target.URL)
	require.NoError(t, err)
	assert.Equal(t, target.URL, found.URL)
	assert.Equal(t, target.Provider, found.Provider)
}

func TestLLMTargetRepo_Create_DuplicateURL(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// 先创建一个 API key
	keyID := "test-key-1"
	require.NoError(t, gormDB.Create(&APIKey{
		ID: keyID, Name: "test-key", EncryptedValue: "sk-test", Provider: "anthropic",
	}).Error)

	target1 := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		APIKeyID: &keyID,
		Source:   "database",
	}
	err = repo.Create(target1)
	require.NoError(t, err)

	// 尝试创建相同 URL + 相同 APIKeyID → 应失败（复合唯一约束）
	target2 := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		APIKeyID: &keyID,
		Source:   "database",
	}
	err = repo.Create(target2)
	assert.Error(t, err) // 应该失败（唯一性约束）

	// 相同 URL + 不同 APIKeyID → 应成功（允许多 key 共用同一 URL）
	keyID2 := "test-key-2"
	require.NoError(t, gormDB.Create(&APIKey{
		ID: keyID2, Name: "test-key-2", EncryptedValue: "sk-test-2", Provider: "anthropic",
	}).Error)
	target3 := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		APIKeyID: &keyID2,
		Source:   "database",
	}
	err = repo.Create(target3)
	assert.NoError(t, err) // 允许不同 API key 共用同一 URL
}

func TestLLMTargetRepo_GetByURL(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	target := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		Provider: "anthropic",
		Source:   "database",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// 查询
	found, err := repo.GetByURL(target.URL)
	require.NoError(t, err)
	assert.Equal(t, target.ID, found.ID)
	assert.Equal(t, target.URL, found.URL)
}

func TestLLMTargetRepo_GetByURL_NotFound(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	_, err = repo.GetByURL("http://nonexistent.local")
	assert.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestLLMTargetRepo_GetByID(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	target := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		Provider: "anthropic",
		Source:   "database",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// 查询
	found, err := repo.GetByID(target.ID)
	require.NoError(t, err)
	assert.Equal(t, target.ID, found.ID)
	assert.Equal(t, target.URL, found.URL)
}

func TestLLMTargetRepo_GetByID_NotFound(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	_, err = repo.GetByID("nonexistent-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestLLMTargetRepo_ListAll(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// 创建多个 targets
	targets := []*LLMTarget{
		{ID: uuid.NewString(), URL: "http://test1.local", Source: "config"},
		{ID: uuid.NewString(), URL: "http://test2.local", Source: "database"},
		{ID: uuid.NewString(), URL: "http://test3.local", Source: "database", IsActive: false},
	}
	for _, target := range targets {
		err := repo.Create(target)
		require.NoError(t, err)
	}

	// 查询所有
	all, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestLLMTargetRepo_URLExists(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	target := &LLMTarget{
		ID:     uuid.NewString(),
		URL:    "http://test.local:8080",
		Source: "database",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// 检查存在
	exists, err := repo.URLExists(target.URL)
	require.NoError(t, err)
	assert.True(t, exists)

	// 检查不存在
	exists, err = repo.URLExists("http://nonexistent.local")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestLLMTargetRepo_Update(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create editable target
	target := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://test.local:8080",
		Provider:   "anthropic",
		Name:       "Original Name",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Update
	target.Name = "Updated Name"
	target.Weight = 5
	err = repo.Update(target)
	assert.NoError(t, err)

	// Verify
	found, err := repo.GetByURL(target.URL)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", found.Name)
	assert.Equal(t, 5, found.Weight)
}

func TestLLMTargetRepo_Update_NotEditable(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create non-editable target (config-sourced)
	// Note: GORM ignores false for bool with default:true, so we need two steps
	target := &LLMTarget{
		ID:     uuid.NewString(),
		URL:    "http://test.local:8080",
		Source: "config",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Explicitly set IsEditable to false
	err = gormDB.Model(target).Update("is_editable", false).Error
	require.NoError(t, err)

	// Reload to get updated value
	target, err = repo.GetByID(target.ID)
	require.NoError(t, err)
	require.False(t, target.IsEditable, "target should be non-editable")

	// Try to update
	target.Name = "Should Fail"
	err = repo.Update(target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not editable")
}

func TestLLMTargetRepo_Delete(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create editable target
	target := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://test.local:8080",
		Source:     "database",
		IsEditable: true,
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Delete
	err = repo.Delete(target.ID)
	assert.NoError(t, err)

	// Verify deleted
	_, err = repo.GetByID(target.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestLLMTargetRepo_Delete_NotEditable(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create non-editable target
	// Note: GORM ignores false for bool with default:true, so we need two steps
	target := &LLMTarget{
		ID:     uuid.NewString(),
		URL:    "http://test.local:8080",
		Source: "config",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Explicitly set IsEditable to false
	err = gormDB.Model(target).Update("is_editable", false).Error
	require.NoError(t, err)

	// Try to delete
	err = repo.Delete(target.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not editable")
}

func TestLLMTargetRepo_Upsert_Insert(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Upsert new target
	target := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		Provider: "anthropic",
		Name:     "Test",
		Source:   "config",
	}
	err = repo.Upsert(target)
	assert.NoError(t, err)

	// Verify inserted
	found, err := repo.GetByURL(target.URL)
	require.NoError(t, err)
	assert.Equal(t, target.Name, found.Name)
}

func TestLLMTargetRepo_Upsert_Update(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create existing target
	target := &LLMTarget{
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		Provider: "anthropic",
		Name:     "Original",
		Source:   "config",
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Upsert with same URL but different data
	target.Name = "Updated"
	target.Weight = 10
	err = repo.Upsert(target)
	assert.NoError(t, err)

	// Verify updated
	found, err := repo.GetByURL(target.URL)
	require.NoError(t, err)
	assert.Equal(t, "Updated", found.Name)
	assert.Equal(t, 10, found.Weight)
}

func TestLLMTargetRepo_DeleteConfigTargetsNotInList(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create multiple targets
	targets := []*LLMTarget{
		{ID: uuid.NewString(), URL: "http://config1.local", Source: "config", IsEditable: false},
		{ID: uuid.NewString(), URL: "http://config2.local", Source: "config", IsEditable: false},
		{ID: uuid.NewString(), URL: "http://config3.local", Source: "config", IsEditable: false},
		{ID: uuid.NewString(), URL: "http://database1.local", Source: "database", IsEditable: true},
	}
	for _, target := range targets {
		err := repo.Create(target)
		require.NoError(t, err)
	}

	// Keep only config1 and config2, delete config3
	keepKeys := []ConfigTargetKey{
		{URL: "http://config1.local"},
		{URL: "http://config2.local"},
	}
	deleted, err := repo.DeleteConfigTargetsNotInList(keepKeys)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted) // Only config3 should be deleted

	// Verify config3 is deleted
	_, err = repo.GetByURL("http://config3.local")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Verify config1 and config2 still exist
	_, err = repo.GetByURL("http://config1.local")
	assert.NoError(t, err)
	_, err = repo.GetByURL("http://config2.local")
	assert.NoError(t, err)

	// Verify database-sourced target is untouched
	_, err = repo.GetByURL("http://database1.local")
	assert.NoError(t, err)
}

func TestLLMTargetRepo_DeleteConfigTargetsNotInList_EmptyList(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create targets
	targets := []*LLMTarget{
		{ID: uuid.NewString(), URL: "http://config1.local", Source: "config", IsEditable: false},
		{ID: uuid.NewString(), URL: "http://config2.local", Source: "config", IsEditable: false},
		{ID: uuid.NewString(), URL: "http://database1.local", Source: "database", IsEditable: true},
	}
	for _, target := range targets {
		err := repo.Create(target)
		require.NoError(t, err)
	}

	// Empty list - should delete all config-sourced targets
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted) // Both config targets deleted

	// Verify config targets are deleted
	_, err = repo.GetByURL("http://config1.local")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = repo.GetByURL("http://config2.local")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Verify database-sourced target is untouched
	_, err = repo.GetByURL("http://database1.local")
	assert.NoError(t, err)
}

func TestLLMTargetRepo_DeleteConfigTargetsNotInList_NoConfigTargets(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// Create only database-sourced targets
	target := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        "http://database1.local",
		Source:     "database",
		IsEditable: true,
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Should delete nothing
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{})
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)

	// Verify database target is untouched
	_, err = repo.GetByURL("http://database1.local")
	assert.NoError(t, err)
}

// ============================================================
// 同 URL 多 Key 场景：举一反三测试
// 揭示哪些操作在多 Key 时行为明确、哪些存在歧义
// ============================================================

// TestGetByURLAndAPIKeyID_Disambiguates 验证 GetByURLAndAPIKeyID 能精确区分同 URL 不同 Key。
func TestGetByURLAndAPIKeyID_Disambiguates(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	tA := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "database", IsEditable: true}
	tB := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "database", IsEditable: true}
	require.NoError(t, repo.Create(tA))
	require.NoError(t, repo.Create(tB))

	// GetByURLAndAPIKeyID 能精确定位
	got, err := repo.GetByURLAndAPIKeyID(url, &keyA)
	require.NoError(t, err)
	assert.Equal(t, tA.ID, got.ID, "应返回 keyA 对应的 target")

	got, err = repo.GetByURLAndAPIKeyID(url, &keyB)
	require.NoError(t, err)
	assert.Equal(t, tB.ID, got.ID, "应返回 keyB 对应的 target")

	// GetByURL 只返回其中一条（歧义）—— 此测试记录该行为
	gotAmbiguous, err := repo.GetByURL(url)
	require.NoError(t, err)
	assert.Contains(t, []string{tA.ID, tB.ID}, gotAmbiguous.ID,
		"GetByURL 在多 Key 时返回其中一条（歧义行为，调用方不应依赖具体是哪条）")
}

// TestURLExists_MultiKey_ReturnsTrue 验证 URLExists 在同 URL 多 Key 时返回 true。
// 用于判断 Seed() 的行为：只要该 URL 有任何 target 存在，就跳过（不管 Key 是否不同）。
func TestURLExists_MultiKey_ReturnsTrue(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "database", IsEditable: true}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "database", IsEditable: true}))

	exists, err := repo.URLExists(url)
	require.NoError(t, err)
	assert.True(t, exists, "URLExists 应返回 true（URL 存在，不论有几条）")
}

// TestSeed_MultiKey_AllowsDifferentAPIKeys 验证 Seed 行为：
// 同 URL 不同 APIKey 的组合允许各自独立 Seed（ComboExists 检查复合键）。
func TestSeed_MultiKey_AllowsDifferentAPIKeys(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	// 先 Seed 一条（keyA）
	err = repo.Seed(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "database"})
	require.NoError(t, err)

	// 再 Seed 同 URL 但不同 Key（keyB）→ (url, keyB) 不存在，应创建新记录
	err = repo.Seed(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "database"})
	require.NoError(t, err, "Seed 对新的 (url, apiKeyID) 组合应成功创建")

	// 验证：DB 中有 2 条记录（不同 APIKey 各一条）
	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2,
		"同 URL 不同 APIKey 应各自独立存在（ComboExists 按复合键检查）")

	// 再次 Seed 已有的 (url, keyA) → 应跳过
	err = repo.Seed(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "database"})
	require.NoError(t, err)
	targets2, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets2, 2, "重复 Seed 相同 (url, apiKeyID) 应被跳过")
}

// TestDeleteConfigTargetsNotInList_MultiKey_Precision 验证：
// 同 URL 两个 Key，删除列表中不包含其中一个 (url, key) → 精确删除那一条，不误删另一条。
func TestDeleteConfigTargetsNotInList_MultiKey_Precision(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	tA := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "config", IsEditable: false}
	tB := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "config", IsEditable: false}
	require.NoError(t, repo.Create(tA))
	require.NoError(t, repo.Create(tB))

	// 只保留 keyA → 应精确删除 keyB 那条，不误删 keyA
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{
		{URL: url, APIKeyID: &keyA},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "应精确删除 1 条（keyB）")

	// 验证 keyA 还在
	got, err := repo.GetByURLAndAPIKeyID(url, &keyA)
	require.NoError(t, err)
	assert.Equal(t, tA.ID, got.ID, "keyA 对应的 target 应保留")

	// 验证 keyB 已删除
	_, err = repo.GetByURLAndAPIKeyID(url, &keyB)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "keyB 对应的 target 应已删除")
}

// TestDeleteConfigTargetsNotInList_OldURLOnlyBehavior_WouldBreakMultiKey 文档化测试：
// 记录若使用旧的"仅按 URL 匹配"删除逻辑时，同 URL 多 Key 会被全部删除（破坏性）。
// 此测试验证新实现的正确性，并作为回归防护。
func TestDeleteConfigTargetsNotInList_SameURL_BothPreserved(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "config"}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "config"}))

	// 保留列表包含两个 (url, key) 对 → 都不删
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{
		{URL: url, APIKeyID: &keyA},
		{URL: url, APIKeyID: &keyB},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, deleted, "两个 (url,key) 都在保留列表中，不应删除任何一条")

	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2, "两条记录应都保留")
}

// TestGetByID_StillWorks_MultiKey 验证 GetByID 在多 Key 场景下仍能精确定位。
func TestGetByID_StillWorks_MultiKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	idA, idB := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.Create(&LLMTarget{ID: idA, URL: url, APIKeyID: &keyA, Source: "database", IsEditable: true}))
	require.NoError(t, repo.Create(&LLMTarget{ID: idB, URL: url, APIKeyID: &keyB, Source: "database", IsEditable: true}))

	gotA, err := repo.GetByID(idA)
	require.NoError(t, err)
	assert.Equal(t, idA, gotA.ID)
	assert.Equal(t, keyA, *gotA.APIKeyID)

	gotB, err := repo.GetByID(idB)
	require.NoError(t, err)
	assert.Equal(t, idB, gotB.ID)
	assert.Equal(t, keyB, *gotB.APIKeyID)
}

// TestListAll_MultiKey_ReturnsAll 验证 ListAll 在多 Key 场景下返回所有记录。
func TestListAll_MultiKey_ReturnsAll(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "database", IsEditable: true, IsActive: true}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "database", IsEditable: true, IsActive: true}))

	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2, "ListAll 应返回全部 target，包括同 URL 不同 Key 的")

	urls := make(map[string]int)
	for _, tgt := range targets {
		urls[tgt.URL]++
	}
	assert.Equal(t, 2, urls[url], "两条记录都应有相同的 URL")
}

// TestUpsert_SameURLSameKey_Updates 验证 Upsert 幂等：同 (url, key) 再次 Upsert → 更新，不新增。
func TestUpsert_SameURLSameKey_Updates(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA := "key-a"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})

	const url = "https://api.anthropic.com"
	t1 := &LLMTarget{URL: url, APIKeyID: &keyA, Source: "config", Weight: 1}
	require.NoError(t, repo.Upsert(t1))
	originalID := t1.ID

	// 再次 Upsert 相同 (url, key)，仅改 Weight → 应更新，不新增
	t2 := &LLMTarget{URL: url, APIKeyID: &keyA, Source: "config", Weight: 5}
	require.NoError(t, repo.Upsert(t2))

	targets, err := repo.ListAll()
	require.NoError(t, err)
	require.Len(t, targets, 1, "同 (url,key) Upsert 应更新，不新增")
	assert.Equal(t, originalID, targets[0].ID, "ID 应保持不变（保留原有记录）")
	assert.Equal(t, 5, targets[0].Weight, "Weight 应被更新")
}

// TestUpsert_SameURLDifferentKey_Creates 验证 Upsert 在不同 Key 时创建新记录（与 TestUpsert_SameURLSameKey_Updates 对比）。
func TestUpsert_SameURLDifferentKey_Creates(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	require.NoError(t, repo.Upsert(&LLMTarget{URL: url, APIKeyID: &keyA, Source: "config", Weight: 2}))
	require.NoError(t, repo.Upsert(&LLMTarget{URL: url, APIKeyID: &keyB, Source: "config", Weight: 1}))

	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2, "不同 Key 的 Upsert 应创建两条独立记录")
}

// TestDeleteConfigTargetsNotInList_KeyNilVsNonNil 验证：
// api_key_id IS NULL 和 api_key_id = 'xxx' 被视为不同的 (url, key) 对，互不影响。
func TestDeleteConfigTargetsNotInList_KeyNilVsNonNil(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA := "key-a"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})

	const url = "https://api.anthropic.com"
	// 一条 key=nil，一条 key=keyA
	tNil := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: nil, Source: "config"}
	tA := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "config"}
	require.NoError(t, repo.Create(tNil))
	require.NoError(t, repo.Create(tA))

	// 只保留 keyA 对，删除 nil-key 那条
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{
		{URL: url, APIKeyID: &keyA},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "应精确删除 nil-key 那条")

	_, err = repo.GetByURLAndAPIKeyID(url, &keyA)
	assert.NoError(t, err, "keyA 对应记录应保留")

	_, err = repo.GetByURLAndAPIKeyID(url, nil)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "nil-key 对应记录应删除")
}

// TestGetByURLAndAPIKeyID_NilKey 验证 nil api_key_id 场景的查询。
func TestGetByURLAndAPIKeyID_NilKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.anthropic.com"
	tNil := &LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: nil, Source: "database", IsEditable: true}
	require.NoError(t, repo.Create(tNil))

	got, err := repo.GetByURLAndAPIKeyID(url, nil)
	require.NoError(t, err)
	assert.Equal(t, tNil.ID, got.ID)
	assert.Nil(t, got.APIKeyID)

	// 查不到不存在的
	keyA := "key-a"
	_, err = repo.GetByURLAndAPIKeyID(url, &keyA)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestGetByURLAndAPIKeyID_NotFound 验证查询不存在记录时返回 ErrRecordNotFound。
func TestGetByURLAndAPIKeyID_NotFound(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA := "key-a"
	_, err = repo.GetByURLAndAPIKeyID("https://nonexistent.com", &keyA)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestUpsert_Idempotent_NoAPIKey 验证 Upsert 在 api_key_id 为 nil 时的幂等行为。
func TestUpsert_Idempotent_NoAPIKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.anthropic.com"
	t1 := &LLMTarget{URL: url, APIKeyID: nil, Source: "config", Weight: 1}
	require.NoError(t, repo.Upsert(t1))
	id1 := t1.ID

	// 再次 Upsert 相同 (url, nil-key) → 更新
	t2 := &LLMTarget{URL: url, APIKeyID: nil, Source: "config", Weight: 3}
	require.NoError(t, repo.Upsert(t2))

	targets, err := repo.ListAll()
	require.NoError(t, err)
	require.Len(t, targets, 1, "nil-key 的幂等 Upsert 不应新增记录")
	assert.Equal(t, id1, targets[0].ID)
	assert.Equal(t, 3, targets[0].Weight, "Weight 应被更新")
}

// TestDeleteConfigTargetsNotInList_EmptyKeepList_DeletesAll 验证：
// 空的保留列表 → 删除所有 config 来源的 target（包括同 URL 多 Key 的情况）。
func TestDeleteConfigTargetsNotInList_EmptyKeepList_DeletesAll(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	keyA, keyB := "key-a", "key-b"
	gormDB.Create(&APIKey{ID: keyA, Name: "A", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "B", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyA, Source: "config"}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: url, APIKeyID: &keyB, Source: "config"}))
	// 一条 database 来源（不应被删）
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: "https://other.com", APIKeyID: nil, Source: "database", IsEditable: true}))

	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted, "空保留列表应删除全部 config 来源的记录（两条）")

	remaining, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "只有 database 来源的 target 应保留")
	assert.Equal(t, "database", remaining[0].Source)
}

// TestLLMTargetRepo_ListByURL_MultipleKeys 验证 ListByURL 能返回同 URL 不同 Key 的所有记录
func TestLLMTargetRepo_ListByURL_MultipleKeys(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	// 创建两个 API Key
	keyA := uuid.NewString()
	keyB := uuid.NewString()
	gormDB.Create(&APIKey{ID: keyA, Name: "keyA", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})
	gormDB.Create(&APIKey{ID: keyB, Name: "keyB", Provider: "anthropic", EncryptedValue: "sk-B", IsActive: true})

	const url = "https://api.anthropic.com"

	// 创建两个同 URL 但不同 APIKeyID 的 target
	target1 := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        url,
		APIKeyID:   &keyA,
		Provider:   "anthropic",
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}
	target2 := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        url,
		APIKeyID:   &keyB,
		Provider:   "anthropic",
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}

	require.NoError(t, repo.Create(target1))
	require.NoError(t, repo.Create(target2))

	// ListByURL 应该返回两条记录
	results, err := repo.ListByURL(url)
	require.NoError(t, err)
	assert.Len(t, results, 2, "ListByURL 应返回所有同 URL 的记录")

	// 验证返回的是我们创建的两条记录
	ids := make(map[string]bool)
	for _, r := range results {
		ids[r.ID] = true
	}
	assert.True(t, ids[target1.ID], "应包含 target1")
	assert.True(t, ids[target2.ID], "应包含 target2")
}

// TestLLMTargetRepo_ListByURL_WithNilKey 验证 ListByURL 正确处理 nil api_key_id 的情况
func TestLLMTargetRepo_ListByURL_WithNilKey(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.example.com"

	// 创建同 URL 的两条记录：一条 nil key，一条有 key
	keyA := uuid.NewString()
	gormDB.Create(&APIKey{ID: keyA, Name: "keyA", Provider: "anthropic", EncryptedValue: "sk-A", IsActive: true})

	targetNil := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        url,
		APIKeyID:   nil,
		Provider:   "anthropic",
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}
	targetWithKey := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        url,
		APIKeyID:   &keyA,
		Provider:   "anthropic",
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}

	require.NoError(t, repo.Create(targetNil))
	require.NoError(t, repo.Create(targetWithKey))

	// ListByURL 应返回两条
	results, err := repo.ListByURL(url)
	require.NoError(t, err)
	assert.Len(t, results, 2, "ListByURL 应返回所有同 URL 的记录，包括 nil key")

	// 使用 GetByURLAndAPIKeyID 精确查询 nil key
	found, err := repo.GetByURLAndAPIKeyID(url, nil)
	require.NoError(t, err)
	assert.Equal(t, targetNil.ID, found.ID)
}

// TestLLMTargetRepo_ListByURL_Empty 验证 ListByURL 在没有匹配记录时返回空列表
func TestLLMTargetRepo_ListByURL_Empty(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	// 查询不存在的 URL
	results, err := repo.ListByURL("https://nonexistent.com")
	require.NoError(t, err)
	assert.Len(t, results, 0, "不存在的 URL 应返回空列表")
}

// --- Seed 方法测试（F1: Config-as-Seed）---

func TestLLMTargetRepo_Seed_InsertNew(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	target := &LLMTarget{
		ID:        uuid.NewString(),
		URL:       "https://api.anthropic.com",
		Provider:  "anthropic",
		Name:      "Anthropic Main",
		Weight:    1,
		Source:    "config",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = repo.Seed(target)
	require.NoError(t, err)

	// Verify: target was inserted
	found, err := repo.GetByURL("https://api.anthropic.com")
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com", found.URL)
	assert.Equal(t, "anthropic", found.Provider)
	assert.Equal(t, "config", found.Source)
	// Seed preserves the caller's IsEditable value; config-sourced targets pass IsEditable=false
	// so they cannot be edited/deleted via WebUI.
	assert.False(t, found.IsEditable, "Seed should preserve IsEditable=false for config-sourced targets")
}

func TestLLMTargetRepo_Seed_SkipExisting(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// First: create a target with Weight=2 (simulating WebUI modification)
	existing := &LLMTarget{
		ID:        uuid.NewString(),
		URL:       "https://api.anthropic.com",
		Provider:  "anthropic",
		Weight:    2,
		Source:    "config",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(existing))

	// Second: Seed with same URL but Weight=1 (config file value)
	seedTarget := &LLMTarget{
		ID:        uuid.NewString(),
		URL:       "https://api.anthropic.com",
		Provider:  "anthropic",
		Weight:    1,
		Source:    "config",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = repo.Seed(seedTarget)
	require.NoError(t, err)

	// Verify: Weight is still 2 (Seed skipped, preserved original)
	found, err := repo.GetByURL("https://api.anthropic.com")
	require.NoError(t, err)
	assert.Equal(t, 2, found.Weight, "Seed should skip existing target, preserving WebUI modifications")
}

func TestLLMTargetRepo_Seed_SkipExistingWithWebUIModifications(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMTargetRepo(gormDB, logger)

	// First: create a target with SupportedModelsJSON and Weight=1 (WebUI modified values)
	existing := &LLMTarget{
		ID:                  uuid.NewString(),
		URL:                 "https://api.anthropic.com",
		Provider:            "anthropic",
		Weight:              1,
		SupportedModelsJSON: `["gpt-4o"]`,
		Source:              "config",
		IsActive:            true,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	require.NoError(t, repo.Create(existing))

	// Second: Seed with same URL but different values (config file defaults)
	seedTarget := &LLMTarget{
		ID:                  uuid.NewString(),
		URL:                 "https://api.anthropic.com",
		Provider:            "anthropic",
		Weight:              5,
		SupportedModelsJSON: `[]`,
		Source:              "config",
		IsActive:            true,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	err = repo.Seed(seedTarget)
	require.NoError(t, err)

	// Verify: WebUI modifications preserved
	found, err := repo.GetByURL("https://api.anthropic.com")
	require.NoError(t, err)
	assert.Equal(t, 1, found.Weight, "Weight should be preserved from WebUI modification")
	assert.Equal(t, `["gpt-4o"]`, found.SupportedModelsJSON, "SupportedModelsJSON should be preserved from WebUI modification")
}
