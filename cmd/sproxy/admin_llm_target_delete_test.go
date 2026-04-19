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

func TestLLMTargetDelete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewLLMTargetRepo(gormDB, logger)

	t.Run("delete database-sourced target", func(t *testing.T) {
		// 创建一个数据库来源的 target
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        "http://test-delete.local:11434",
			Provider:   "ollama",
			Name:       "Test Delete",
			Weight:     1,
			Source:     "database",
			IsEditable: true,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		err := repo.Create(target)
		require.NoError(t, err)

		// 删除
		err = repo.Delete(target.ID)
		assert.NoError(t, err)

		// 验证已删除
		_, err = repo.GetByID(target.ID)
		assert.Error(t, err)
	})

	t.Run("can delete config-sourced target via CLI", func(t *testing.T) {
		// CLI（admin API）可以删除 config-sourced target；WebUI 才拦截。
		target := &db.LLMTarget{
			ID:         uuid.NewString(),
			URL:        "http://test-config.local:11434",
			Provider:   "ollama",
			Name:       "Test Config",
			Weight:     1,
			Source:     "config",
			IsEditable: false,
			IsActive:   true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		// 使用 Upsert 来正确处理 IsEditable=false
		err := repo.Upsert(target)
		require.NoError(t, err)

		// Repo 层不拦截，应成功
		err = repo.Delete(target.ID)
		assert.NoError(t, err)
	})

	t.Run("delete non-existent target", func(t *testing.T) {
		err := repo.Delete("non-existent-id")
		assert.Error(t, err)
	})
}
