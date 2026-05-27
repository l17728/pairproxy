package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func setupAPIKeyTest(t *testing.T) (*APIKeyRepo, func()) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	sqlDB, _ := gormDB.DB()
	return NewAPIKeyRepo(gormDB, logger), func() { sqlDB.Close() }
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_CreateAndFind — 创建并按名称查询
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_CreateAndFind(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("prod-key", "enc-value-xyz", "anthropic", "obfuscated")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if key.ID == "" {
		t.Error("ID should be set")
	}
	if !key.IsActive {
		t.Error("new key should be active")
	}

	found, err := repo.GetByName("prod-key")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if found == nil {
		t.Fatal("expected key, got nil")
	}
	if found.EncryptedValue != "enc-value-xyz" {
		t.Errorf("EncryptedValue = %q, want 'enc-value-xyz'", found.EncryptedValue)
	}
	if found.Provider != "anthropic" {
		t.Errorf("Provider = %q, want 'anthropic'", found.Provider)
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_DefaultProvider — provider 默认值为 anthropic
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_DefaultProvider(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("default-prov", "enc-val", "", "obfuscated")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if key.Provider != "anthropic" {
		t.Errorf("default provider = %q, want 'anthropic'", key.Provider)
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_List — 列出所有 key
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_List(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	for i, name := range []string{"k1", "k2", "k3"} {
		_, err := repo.Create(name, "enc-"+name, "", "obfuscated")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	keys, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("len(keys) = %d, want 3", len(keys))
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_Revoke — 吊销后 is_active 变为 false
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_Revoke(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("to-revoke", "enc-val", "anthropic", "obfuscated")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Revoke(key.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	found, err := repo.GetByID(key.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found == nil {
		t.Fatal("expected key record to still exist after revoke")
	}
	if found.IsActive {
		t.Error("revoked key should have IsActive=false")
	}
}

// TestAPIKeyRepo_FindByProviderAndValue_Found 测试查到唯一结果的情况
func TestAPIKeyRepo_FindByProviderAndValue_Found(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("test-key", "encrypted-value-abc", "anthropic", "obfuscated")
	require.NoError(t, err)

	found, err := repo.FindByProviderAndValue("anthropic", "encrypted-value-abc")
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, key.ID, found.ID)
}

// TestAPIKeyRepo_FindByProviderAndValue_NotFound 测试未找到的情况
func TestAPIKeyRepo_FindByProviderAndValue_NotFound(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	found, err := repo.FindByProviderAndValue("anthropic", "nonexistent-encrypted-value")
	require.NoError(t, err)
	require.Nil(t, found)
}

// TestAPIKeyRepo_FindByProviderAndValue_DifferentProvider 测试 provider 不同时不匹配
func TestAPIKeyRepo_FindByProviderAndValue_DifferentProvider(t *testing.T) {
	repo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	_, err := repo.Create("test-key-openai", "same-encrypted-value", "openai", "obfuscated")
	require.NoError(t, err)

	// 用 anthropic 查相同值，不应找到
	found, err := repo.FindByProviderAndValue("anthropic", "same-encrypted-value")
	require.NoError(t, err)
	require.Nil(t, found, "不同 provider 不应匹配")

	// 用 openai 查，应该找到
	found, err = repo.FindByProviderAndValue("openai", "same-encrypted-value")
	require.NoError(t, err)
	assert.NotNil(t, found, "正确 provider 应该匹配")
}
