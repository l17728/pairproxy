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
		ID:       uuid.NewString(),
		URL:      "http://test.local:8080",
		Provider: "anthropic",
		Name:     "Test Target",
		Weight:   1,
		Source:   "database",
		IsEditable: true,
		IsActive: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
		ID:  uuid.NewString(),
		URL: "http://test.local:8080",
		Provider: "anthropic",
		Source: "database",
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
		ID:  uuid.NewString(),
		URL: "http://test.local:8080",
		Provider: "anthropic",
		Source: "database",
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
		ID:  uuid.NewString(),
		URL: "http://test.local:8080",
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
	keepURLs := []string{"http://config1.local", "http://config2.local"}
	deleted, err := repo.DeleteConfigTargetsNotInList(keepURLs)
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
	deleted, err := repo.DeleteConfigTargetsNotInList([]string{})
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
	deleted, err := repo.DeleteConfigTargetsNotInList([]string{})
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)

	// Verify database target is untouched
	_, err = repo.GetByURL("http://database1.local")
	assert.NoError(t, err)
}
