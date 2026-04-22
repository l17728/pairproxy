package main

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

func TestLLMTargetUpdate(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewLLMTargetRepo(gormDB, logger)

	t.Run("update database-sourced target", func(t *testing.T) {
		// 创建一个数据库来源的 target
		apiKeyID := "test-key-123"
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        "http://test-update.local:11434",
			APIKeyID:   &apiKeyID,
			Provider:   "ollama",
			Name:       "Test Update",
			Weight:     1,
			Source:     "database",
			IsEditable: true,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		err := repo.Create(target)
		require.NoError(t, err)

		// 更新字段
		target.Provider = "openai"
		target.Name = "Updated Name"
		target.Weight = 5
		target.HealthCheckPath = "/health"
		target.UpdatedAt = time.Now()

		err = repo.Update(target)
		assert.NoError(t, err)

		// 验证更新
		updated, err := repo.GetByID(target.ID)
		require.NoError(t, err)
		assert.Equal(t, "openai", updated.Provider)
		assert.Equal(t, "Updated Name", updated.Name)
		assert.Equal(t, 5, updated.Weight)
		assert.Equal(t, "/health", updated.HealthCheckPath)
	})

	t.Run("can update config-sourced target via CLI", func(t *testing.T) {
		// CLI（admin API）可以修改 config-sourced target；WebUI 才拦截。
		target := &db.LLMTarget{
			ID:        uuid.NewString(),
			URL:       "http://test-config-update.local:11434",
			Provider:  "ollama",
			Name:      "Test Config",
			Weight:    1,
			Source:    "config",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(target)
		require.NoError(t, err)

		// Explicitly set IsEditable to false
		err = gormDB.Model(target).Update("is_editable", false).Error
		require.NoError(t, err)

		// Reload to get updated value
		target, err = repo.GetByID(target.ID)
		require.NoError(t, err)
		require.False(t, target.IsEditable, "target should be non-editable")

		// Repo 层不拦截，应成功
		target.Name = "Updated via CLI"
		err = repo.Update(target)
		assert.NoError(t, err)
	})

	t.Run("update non-existent target", func(t *testing.T) {
		target := &db.LLMTarget{
			ID:         "non-existent-id",
			URL:        "http://non-existent.local",
			Provider:   "ollama",
			IsEditable: true,
		}
		err := repo.Update(target)
		// Update 会执行 Save，对于不存在的记录会创建新记录
		// 所以这里不会报错，但我们可以验证行为
		assert.NoError(t, err)
	})

	t.Run("update with nil api key", func(t *testing.T) {
		// 创建一个没有 API Key 的 target
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        "http://test-nil-key.local:11434",
			APIKeyID:   nil,
			Provider:   "ollama",
			Name:       "Test Nil Key",
			Weight:     1,
			Source:     "database",
			IsEditable: true,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		err := repo.Create(target)
		require.NoError(t, err)

		// 更新其他字段
		target.Weight = 3
		err = repo.Update(target)
		assert.NoError(t, err)

		// 验证更新
		updated, err := repo.GetByID(target.ID)
		require.NoError(t, err)
		assert.Equal(t, 3, updated.Weight)
		assert.Nil(t, updated.APIKeyID)
	})
}

