package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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
