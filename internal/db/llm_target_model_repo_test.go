package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
)

func openTestDBForModels(t *testing.T) *gorm.DB {
	t.Helper()
	logger := zap.NewNop()
	database, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	err = db.Migrate(logger, database)
	require.NoError(t, err)
	return database
}

func TestLLMTargetModelRepo_UpsertFromConfig(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	models := []config.ModelEntry{
		{ID: "claude-opus-4-5", Default: true, Aliases: []string{"claude-opus"}},
		{ID: "claude-sonnet-4-5", UpstreamName: "claude-sonnet-4-5"},
	}

	err := repo.UpsertFromConfig("https://api.anthropic.com", models)
	require.NoError(t, err)

	all, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// 幂等：重复同步不报错
	err = repo.UpsertFromConfig("https://api.anthropic.com", models)
	require.NoError(t, err)

	all2, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, all2, 2, "幂等同步后数量不变")
}

func TestLLMTargetModelRepo_FindTargetForModel_ExactMatch(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "claude-opus-4-5", Default: true},
		{ID: "claude-sonnet-4-5"},
	})
	require.NoError(t, err)

	targetURL, upstream, found, err := repo.FindTargetForModel("claude-opus-4-5")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "https://api.anthropic.com", targetURL)
	assert.Equal(t, "claude-opus-4-5", upstream)
}

func TestLLMTargetModelRepo_FindTargetForModel_AliasMatch(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "claude-opus-4-5", Aliases: []string{"claude-opus", "opus"}},
	})
	require.NoError(t, err)

	// 精确模型名
	targetURL, upstream, found, err := repo.FindTargetForModel("claude-opus")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "https://api.anthropic.com", targetURL)
	// upstream 应为主 model ID（UpstreamName 为空时返回 ModelID）
	assert.Equal(t, "claude-opus-4-5", upstream)

	// 另一个别名
	_, _, found2, err2 := repo.FindTargetForModel("opus")
	require.NoError(t, err2)
	assert.True(t, found2)
}

func TestLLMTargetModelRepo_FindTargetForModel_NotFound(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	_, _, found, err := repo.FindTargetForModel("nonexistent-model")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLLMTargetModelRepo_FindTargetForModel_UpstreamNameOverride(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	// Ollama: claude-haiku → llama3.2:1b
	err := repo.UpsertFromConfig("http://localhost:11434", []config.ModelEntry{
		{ID: "claude-haiku-4-5", UpstreamName: "llama3.2:1b"},
	})
	require.NoError(t, err)

	targetURL, upstream, found, err := repo.FindTargetForModel("claude-haiku-4-5")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "http://localhost:11434", targetURL)
	assert.Equal(t, "llama3.2:1b", upstream)
}

func TestLLMTargetModelRepo_GetDefaultModelForTarget(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "claude-opus-4-5", Default: true},
		{ID: "claude-sonnet-4-5"},
	})
	require.NoError(t, err)

	modelID, upstream, found, err := repo.GetDefaultModelForTarget("https://api.anthropic.com")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "claude-opus-4-5", modelID)
	assert.Equal(t, "claude-opus-4-5", upstream)
}

func TestLLMTargetModelRepo_GetDefaultModelForTarget_NoDefault(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "claude-sonnet-4-5"}, // no default
	})
	require.NoError(t, err)

	_, _, found, err := repo.GetDefaultModelForTarget("https://api.anthropic.com")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLLMTargetModelRepo_Create_And_Delete(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	entry, err := repo.Create("http://localhost:11434", "llama3.2", "llama3.2", []string{"llama"}, true)
	require.NoError(t, err)
	assert.Equal(t, "llama3.2", entry.ModelID)
	assert.Equal(t, "database", entry.Source)

	// 验证可以查找
	_, _, found, err := repo.FindTargetForModel("llama3.2")
	require.NoError(t, err)
	assert.True(t, found)

	// 通过别名查找
	_, _, foundAlias, err := repo.FindTargetForModel("llama")
	require.NoError(t, err)
	assert.True(t, foundAlias)

	// 删除
	err = repo.Delete("http://localhost:11434", "llama3.2")
	require.NoError(t, err)

	// 删除后找不到
	_, _, found2, err2 := repo.FindTargetForModel("llama3.2")
	require.NoError(t, err2)
	assert.False(t, found2)
}

func TestLLMTargetModelRepo_ListByTarget(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "model-a", Default: true},
		{ID: "model-b"},
	})
	require.NoError(t, err)

	err = repo.UpsertFromConfig("http://localhost:11434", []config.ModelEntry{
		{ID: "llama3.2"},
	})
	require.NoError(t, err)

	// ListByTarget 只返回该 target 的条目
	byAnthropic, err := repo.ListByTarget("https://api.anthropic.com")
	require.NoError(t, err)
	assert.Len(t, byAnthropic, 2)

	byOllama, err := repo.ListByTarget("http://localhost:11434")
	require.NoError(t, err)
	assert.Len(t, byOllama, 1)
	assert.Equal(t, "llama3.2", byOllama[0].ModelID)
}

func TestLLMTargetModel_Aliases(t *testing.T) {
	gdb := openTestDBForModels(t)
	repo := db.NewLLMTargetModelRepo(gdb, zap.NewNop())

	err := repo.UpsertFromConfig("https://api.anthropic.com", []config.ModelEntry{
		{ID: "claude-opus-4-5", Aliases: []string{"claude-opus", "opus-4"}},
	})
	require.NoError(t, err)

	all, err := repo.ListAll()
	require.NoError(t, err)
	require.Len(t, all, 1)

	aliases := all[0].Aliases()
	assert.ElementsMatch(t, []string{"claude-opus", "opus-4"}, aliases)
}

func TestLLMTargetModel_ResolvedUpstreamName(t *testing.T) {
	m := &db.LLMTargetModel{
		ModelID:      "model-a",
		UpstreamName: "",
	}
	assert.Equal(t, "model-a", m.ResolvedUpstreamName())

	m.UpstreamName = "model-a-actual"
	assert.Equal(t, "model-a-actual", m.ResolvedUpstreamName())
}
