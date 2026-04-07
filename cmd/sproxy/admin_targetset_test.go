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

func TestTargetSetList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewGroupTargetSetRepo(gormDB, logger)

	t.Run("list empty target sets", func(t *testing.T) {
		sets, err := repo.ListAll()
		assert.NoError(t, err)
		assert.Equal(t, 0, len(sets))
	})

	t.Run("list multiple target sets", func(t *testing.T) {
		// Create default target set
		set1 := &db.GroupTargetSet{
			ID:        "default-set",
			Name:      "Default Set",
			GroupID:   nil,
			Strategy:  "weighted_random",
			IsDefault: true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(set1)
		require.NoError(t, err)

		// Create group-bound target set
		groupID := "group-123"
		set2 := &db.GroupTargetSet{
			ID:        "prod-set",
			Name:      "Production Set",
			GroupID:   &groupID,
			Strategy:  "round_robin",
			IsDefault: false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err = repo.Create(set2)
		require.NoError(t, err)

		sets, err := repo.ListAll()
		assert.NoError(t, err)
		assert.Equal(t, 2, len(sets))
	})
}

func TestTargetSetCreate(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewGroupTargetSetRepo(gormDB, logger)

	t.Run("create default target set", func(t *testing.T) {
		set := &db.GroupTargetSet{
			ID:          "default-set",
			Name:        "Default Set",
			GroupID:     nil,
			Strategy:    "weighted_random",
			RetryPolicy: "try_next",
			IsDefault:   true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		err := repo.Create(set)
		assert.NoError(t, err)

		retrieved, err := repo.GetByName("Default Set")
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "default-set", retrieved.ID)
		assert.Nil(t, retrieved.GroupID)
		assert.True(t, retrieved.IsDefault)
	})

	t.Run("create group-bound target set", func(t *testing.T) {
		groupID := "group-456"
		set := &db.GroupTargetSet{
			ID:          "prod-set",
			Name:        "Production Set",
			GroupID:     &groupID,
			Strategy:    "round_robin",
			RetryPolicy: "fail_fast",
			IsDefault:   false,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		err := repo.Create(set)
		assert.NoError(t, err)

		retrieved, err := repo.GetByName("Production Set")
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "prod-set", retrieved.ID)
		assert.NotNil(t, retrieved.GroupID)
		assert.Equal(t, groupID, *retrieved.GroupID)
		assert.False(t, retrieved.IsDefault)
	})

	t.Run("create with empty ID fails", func(t *testing.T) {
		set := &db.GroupTargetSet{
			ID:        "",
			Name:      "Invalid Set",
			Strategy:  "weighted_random",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(set)
		assert.Error(t, err)
	})

	t.Run("create with empty name fails", func(t *testing.T) {
		set := &db.GroupTargetSet{
			ID:        "invalid-set",
			Name:      "",
			Strategy:  "weighted_random",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(set)
		assert.Error(t, err)
	})
}

func TestTargetSetDelete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewGroupTargetSetRepo(gormDB, logger)

	t.Run("delete existing target set", func(t *testing.T) {
		set := &db.GroupTargetSet{
			ID:        "delete-test",
			Name:      "Delete Test",
			Strategy:  "weighted_random",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(set)
		require.NoError(t, err)

		err = repo.Delete(set.ID)
		assert.NoError(t, err)

		// Verify deletion - GetByName returns nil when not found
		retrieved, err := repo.GetByName("Delete Test")
		assert.NoError(t, err)
		assert.Nil(t, retrieved)
	})

	t.Run("delete non-existent target set", func(t *testing.T) {
		// GORM Delete doesn't error when no rows match, it just returns success
		err := repo.Delete("non-existent-id")
		assert.NoError(t, err)
	})
}

func TestTargetSetMembers(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewGroupTargetSetRepo(gormDB, logger)

	// Create target set
	set := &db.GroupTargetSet{
		ID:        "member-test",
		Name:      "Member Test",
		Strategy:  "weighted_random",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = repo.Create(set)
	require.NoError(t, err)

	t.Run("add member to target set", func(t *testing.T) {
		member := &db.GroupTargetSetMember{
			ID:           uuid.NewString(),
			TargetSetID:  set.ID,
			TargetURL:    "http://llm1.local:8080",
			Weight:       10,
			Priority:     0,
			IsActive:     true,
			HealthStatus: "unknown",
			CreatedAt:    time.Now(),
		}
		err := repo.AddMember(set.ID, member)
		assert.NoError(t, err)

		members, err := repo.ListMembers(set.ID)
		assert.NoError(t, err)
		assert.Equal(t, 1, len(members))
		assert.Equal(t, "http://llm1.local:8080", members[0].TargetURL)
		assert.Equal(t, 10, members[0].Weight)
	})

	t.Run("add multiple members", func(t *testing.T) {
		member2 := &db.GroupTargetSetMember{
			ID:           uuid.NewString(),
			TargetSetID:  set.ID,
			TargetURL:    "http://llm2.local:8080",
			Weight:       5,
			Priority:     1,
			IsActive:     true,
			HealthStatus: "unknown",
			CreatedAt:    time.Now(),
		}
		err := repo.AddMember(set.ID, member2)
		assert.NoError(t, err)

		members, err := repo.ListMembers(set.ID)
		assert.NoError(t, err)
		assert.Equal(t, 2, len(members))
	})

	t.Run("update member weight", func(t *testing.T) {
		err := repo.UpdateMember(set.ID, "http://llm1.local:8080", 20, 0)
		assert.NoError(t, err)

		members, err := repo.ListMembers(set.ID)
		assert.NoError(t, err)
		for _, m := range members {
			if m.TargetURL == "http://llm1.local:8080" {
				assert.Equal(t, 20, m.Weight)
			}
		}
	})

	t.Run("remove member from target set", func(t *testing.T) {
		err := repo.RemoveMember(set.ID, "http://llm1.local:8080")
		assert.NoError(t, err)

		members, err := repo.ListMembers(set.ID)
		assert.NoError(t, err)
		assert.Equal(t, 1, len(members))
		assert.Equal(t, "http://llm2.local:8080", members[0].TargetURL)
	})

	t.Run("remove non-existent member", func(t *testing.T) {
		err := repo.RemoveMember(set.ID, "http://non-existent.local:8080")
		// RemoveMember doesn't error on non-existent URL, it just doesn't delete anything
		assert.NoError(t, err)
	})
}

func TestTargetSetGetDefault(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	defer closeGormDB(logger, gormDB)

	err = db.Migrate(logger, gormDB)
	require.NoError(t, err)

	repo := db.NewGroupTargetSetRepo(gormDB, logger)

	t.Run("get default when none exists", func(t *testing.T) {
		set, err := repo.GetDefault()
		assert.NoError(t, err)
		assert.Nil(t, set)
	})

	t.Run("get default target set", func(t *testing.T) {
		set := &db.GroupTargetSet{
			ID:        "default-set",
			Name:      "Default Set",
			GroupID:   nil,
			Strategy:  "weighted_random",
			IsDefault: true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Create(set)
		require.NoError(t, err)

		retrieved, err := repo.GetDefault()
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "default-set", retrieved.ID)
		assert.True(t, retrieved.IsDefault)
	})
}
