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

	target1 := &LLMTarget{
		ID:     uuid.NewString(),
		URL:    "http://test.local:8080",
		Source: "database",
	}
	err = repo.Create(target1)
	require.NoError(t, err)

	// 同 URL → 应失败（URL 唯一约束）
	target2 := &LLMTarget{
		ID:     uuid.NewString(),
		URL:    "http://test.local:8080",
		Source: "database",
	}
	err = repo.Create(target2)
	assert.Error(t, err) // 应该失败（URL 唯一性约束）
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

	// Repo层不做 IsEditable 限制（WebUI 层负责拦截）；CLI 可以强制修改 config-sourced target。
	target.Name = "Updated via CLI"
	err = repo.Update(target)
	assert.NoError(t, err)
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

	// Repo层不做 IsEditable 限制（WebUI 层负责拦截）；CLI 可以强制删除 config-sourced target。
	err = repo.Delete(target.ID)
	assert.NoError(t, err)
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

// TestURLUniqueness_BlocksDuplicateURL2 验证 URL 全局唯一约束（in llmtarget_repo_test.go）。
func TestURLUniqueness_BlocksDuplicateURL2(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const testURL = "https://api.anthropic.com/v2"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: testURL, Source: "database", IsEditable: true}))

	// 相同 URL → 应失败（URL 全局唯一约束）
	err = repo.Create(&LLMTarget{ID: uuid.NewString(), URL: testURL, Source: "database", IsEditable: true})
	assert.Error(t, err, "同 URL 第二次创建应因唯一约束失败")
}

// TestDeleteConfigTargetsNotInList_URLInKeepList_BothPreserved 验证：
// URL 在保留列表中 → 不删除。
func TestDeleteConfigTargetsNotInList_URLInKeepList_BothPreserved(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const urlA = "https://api.anthropic.com"
	const urlB = "https://openai.example.com"
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: urlA, Source: "config"}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: urlB, Source: "config"}))

	// 保留列表包含两个 URL → 都不删
	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{
		{URL: urlA},
		{URL: urlB},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, deleted, "两个 URL 都在保留列表中，不应删除任何一条")

	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2, "两条记录应都保留")
}

// TestGetByID_MultiTarget 验证 GetByID 在多目标场景下仍能精确定位。
func TestGetByID_MultiTarget(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	idA, idB := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.Create(&LLMTarget{ID: idA, URL: "https://api.anthropic.com", Source: "database", IsEditable: true}))
	require.NoError(t, repo.Create(&LLMTarget{ID: idB, URL: "https://openai.example.com", Source: "database", IsEditable: true}))

	gotA, err := repo.GetByID(idA)
	require.NoError(t, err)
	assert.Equal(t, idA, gotA.ID)

	gotB, err := repo.GetByID(idB)
	require.NoError(t, err)
	assert.Equal(t, idB, gotB.ID)
}

// TestUpsert_SameURL_Updates 验证 Upsert 幂等：同 URL 再次 Upsert → 更新，不新增。
func TestUpsert_SameURL_Updates(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.anthropic.com"
	t1 := &LLMTarget{URL: url, Source: "config", Weight: 1}
	require.NoError(t, repo.Upsert(t1))
	originalID := t1.ID

	// 再次 Upsert 相同 URL，仅改 Weight → 应更新，不新增
	t2 := &LLMTarget{URL: url, Source: "config", Weight: 5}
	require.NoError(t, repo.Upsert(t2))

	targets, err := repo.ListAll()
	require.NoError(t, err)
	require.Len(t, targets, 1, "同 URL Upsert 应更新，不新增")
	assert.Equal(t, originalID, targets[0].ID, "ID 应保持不变（保留原有记录）")
	assert.Equal(t, 5, targets[0].Weight, "Weight 应被更新")
}

// TestGetByURLAndAPIKeyID_Deprecated 验证 GetByURLAndAPIKeyID 已弃用但仍能查询。
func TestGetByURLAndAPIKeyID_Deprecated(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.anthropic.com"
	target := &LLMTarget{ID: uuid.NewString(), URL: url, Source: "database", IsEditable: true}
	require.NoError(t, repo.Create(target))

	// GetByURLAndAPIKeyID 忽略 apiKeyID 参数，直接按 URL 查找
	keyA := "key-a"
	got, err := repo.GetByURLAndAPIKeyID(url, &keyA)
	require.NoError(t, err)
	assert.Equal(t, target.ID, got.ID)

	// 查不到不存在的 URL
	_, err = repo.GetByURLAndAPIKeyID("https://nonexistent.com", nil)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestDeleteConfigTargetsNotInList_EmptyKeepList_DeletesAll 验证：
// 空的保留列表 → 删除所有 config 来源的 target。
func TestDeleteConfigTargetsNotInList_EmptyKeepList_DeletesAll(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: "https://api.anthropic.com", Source: "config"}))
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: "https://openai.example.com", Source: "config"}))
	// 一条 database 来源（不应被删）
	require.NoError(t, repo.Create(&LLMTarget{ID: uuid.NewString(), URL: "https://other.com", Source: "database", IsEditable: true}))

	deleted, err := repo.DeleteConfigTargetsNotInList([]ConfigTargetKey{})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted, "空保留列表应删除全部 config 来源的记录（两条）")

	remaining, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "只有 database 来源的 target 应保留")
	assert.Equal(t, "database", remaining[0].Source)
}

// TestLLMTargetRepo_ListByURL_SingleResult 验证 ListByURL 在 URL 唯一时返回单条记录
func TestLLMTargetRepo_ListByURL_SingleResult(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMTargetRepo(gormDB, logger)

	const url = "https://api.anthropic.com"
	target := &LLMTarget{
		ID:         uuid.NewString(),
		URL:        url,
		Provider:   "anthropic",
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}
	require.NoError(t, repo.Create(target))

	// ListByURL 应该返回 1 条记录（URL 全局唯一）
	results, err := repo.ListByURL(url)
	require.NoError(t, err)
	assert.Len(t, results, 1, "ListByURL 应返回 1 条记录（URL 现为全局唯一）")
	assert.Equal(t, target.ID, results[0].ID)
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
