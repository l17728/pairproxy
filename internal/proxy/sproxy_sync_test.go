package proxy

import (
	"testing"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestSyncConfigTargetsToDatabase(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	// Create SProxy with config
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      "http://test1.local",
					APIKey:   "key1",
					Provider: "anthropic",
					Name:     "Test 1",
					Weight:   1,
				},
				{
					URL:      "http://test2.local",
					APIKey:   "key2",
					Provider: "openai",
					Name:     "Test 2",
					Weight:   2,
				},
			},
		},
	}

	sp := &SProxy{
		cfg:    cfg,
		db:     gormDB,
		logger: logger,
	}

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Sync
	err = sp.syncConfigTargetsToDatabase(repo)
	require.NoError(t, err)

	// Verify targets were synced
	targets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, targets, 2)

	// Verify properties
	for _, target := range targets {
		assert.Equal(t, "config", target.Source)
		assert.False(t, target.IsEditable)
		assert.True(t, target.IsActive)
	}
}

func TestSyncConfigTargets_Cleanup(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create existing config target
	oldTarget := &db.LLMTarget{
		ID:         "old-id",
		URL:        "http://old.local",
		Source:     "config",
		IsEditable: false,
	}
	err = repo.Create(oldTarget)
	require.NoError(t, err)

	// Sync with new config (old target removed)
	cfg := &config.SProxyFullConfig{
		LLM: config.LLMConfig{
			Targets: []config.LLMTarget{
				{
					URL:      "http://new.local",
					APIKey:   "key",
					Provider: "anthropic",
					Name:     "New",
					Weight:   1,
				},
			},
		},
	}

	sp := &SProxy{
		cfg:    cfg,
		db:     gormDB,
		logger: logger,
	}

	err = sp.syncConfigTargetsToDatabase(repo)
	require.NoError(t, err)

	// Verify old target was deleted
	_, err = repo.GetByURL("http://old.local")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Verify new target exists
	_, err = repo.GetByURL("http://new.local")
	assert.NoError(t, err)
}

func TestLoadAllTargets(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create API keys
	apiKey1 := &db.APIKey{
		ID:             "key1",
		Name:           "Test Key 1",
		Provider:       "anthropic",
		EncryptedValue: "sk-ant-test1",
		IsActive:       true,
	}
	apiKey2 := &db.APIKey{
		ID:             "key2",
		Name:           "Test Key 2",
		Provider:       "openai",
		EncryptedValue: "sk-openai-test2",
		IsActive:       true,
	}
	err = gormDB.Create(apiKey1).Error
	require.NoError(t, err)
	err = gormDB.Create(apiKey2).Error
	require.NoError(t, err)

	// Create targets
	key1ID := "key1"
	key2ID := "key2"
	targets := []*db.LLMTarget{
		{
			ID:        "t1",
			URL:       "http://config.local",
			APIKeyID:  &key1ID,
			Provider:  "anthropic",
			Name:      "Config Target",
			Weight:    1,
			Source:    "config",
			IsActive:  true,
		},
		{
			ID:        "t2",
			URL:       "http://database.local",
			APIKeyID:  &key2ID,
			Provider:  "openai",
			Name:      "Database Target",
			Weight:    2,
			Source:    "database",
			IsActive:  true,
		},
		{
			ID:        "t3",
			URL:       "http://inactive.local",
			Provider:  "anthropic",
			Name:      "Inactive Target",
			Weight:    1,
			Source:    "database",
			IsActive:  true, // Create with true first
		},
	}
	for _, target := range targets {
		err := repo.Create(target)
		require.NoError(t, err)
	}

	// Set t3 to inactive (need two steps due to GORM default:true)
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "t3").Update("is_active", false).Error
	require.NoError(t, err)

	// Load targets
	sp := &SProxy{
		db:     gormDB,
		logger: logger,
	}

	loaded, err := sp.loadAllTargets(repo)
	require.NoError(t, err)

	// Verify
	assert.Len(t, loaded, 2) // Only active targets

	// Verify API keys were resolved
	for _, target := range loaded {
		assert.NotEmpty(t, target.APIKey)
	}
}

func TestLoadAllTargets_SkipInactive(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Create inactive target (need two steps due to GORM default:true)
	target := &db.LLMTarget{
		ID:       "t1",
		URL:      "http://inactive.local",
		Provider: "anthropic",
		Name:     "Inactive",
		Weight:   1,
		Source:   "database",
		IsActive: true, // Create with true first
	}
	err = repo.Create(target)
	require.NoError(t, err)

	// Then set to false
	err = gormDB.Model(&db.LLMTarget{}).Where("id = ?", "t1").Update("is_active", false).Error
	require.NoError(t, err)

	// Load targets
	sp := &SProxy{
		db:     gormDB,
		logger: logger,
	}

	loaded, err := sp.loadAllTargets(repo)
	require.NoError(t, err)

	// Should be empty
	assert.Len(t, loaded, 0)
}
