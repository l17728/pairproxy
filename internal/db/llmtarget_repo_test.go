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

	target1 := &LLMTarget{
		ID:  uuid.NewString(),
		URL: "http://test.local:8080",
		Source: "database",
	}
	err = repo.Create(target1)
	require.NoError(t, err)

	// 尝试创建相同 URL
	target2 := &LLMTarget{
		ID:  uuid.NewString(),
		URL: "http://test.local:8080",
		Source: "database",
	}
	err = repo.Create(target2)
	assert.Error(t, err) // 应该失败（唯一性约束）
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
