package db

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func setupAPIKeyTest(t *testing.T) (*APIKeyRepo, *UserRepo, *GroupRepo, func()) {
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
	return NewAPIKeyRepo(gormDB, logger),
		NewUserRepo(gormDB, logger),
		NewGroupRepo(gormDB, logger),
		func() { sqlDB.Close() }
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_CreateAndFind — 创建并按名称查询
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_CreateAndFind(t *testing.T) {
	repo, _, _, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("prod-key", "enc-value-xyz", "anthropic")
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
	repo, _, _, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("default-prov", "enc-val", "")
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
	repo, _, _, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	for i, name := range []string{"k1", "k2", "k3"} {
		_, err := repo.Create(name, "enc-"+name, "")
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
// TestAPIKeyRepo_Revoke — 吊销后 FindForUser 返回 nil
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_Revoke(t *testing.T) {
	repo, userRepo, groupRepo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	key, err := repo.Create("to-revoke", "enc-val", "anthropic")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	grp := &Group{ID: "grp-rev", Name: "rev-group"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := grp.ID
	user := &User{ID: "usr-rev", Username: "revoker", PasswordHash: "x", GroupID: &gid}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 分配
	uid := user.ID
	if err := repo.Assign(key.ID, &uid, nil); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// 吊销
	if err := repo.Revoke(key.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// 吊销后 FindForUser 应返回 nil
	found, err := repo.FindForUser(user.ID, grp.ID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if found != nil {
		t.Error("revoked key should not be returned by FindForUser")
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_UserAssignment — 用户级分配优先
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_UserAssignment(t *testing.T) {
	repo, userRepo, groupRepo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	grp := &Group{ID: "grp-ua", Name: "ua-group"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := grp.ID
	user := &User{ID: "usr-ua", Username: "ua-user", PasswordHash: "x", GroupID: &gid}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	userKey, _ := repo.Create("user-key", "enc-user", "anthropic")
	groupKey, _ := repo.Create("group-key", "enc-group", "anthropic")

	uid := user.ID
	// 用户级分配
	if err := repo.Assign(userKey.ID, &uid, nil); err != nil {
		t.Fatalf("Assign user: %v", err)
	}
	// 分组级分配
	if err := repo.Assign(groupKey.ID, nil, &gid); err != nil {
		t.Fatalf("Assign group: %v", err)
	}

	found, err := repo.FindForUser(user.ID, grp.ID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if found == nil {
		t.Fatal("expected a key, got nil")
	}
	if found.Name != "user-key" {
		t.Errorf("expected user-level key, got %q", found.Name)
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_GroupFallback — 无用户级分配时回退到分组级
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_GroupFallback(t *testing.T) {
	repo, userRepo, groupRepo, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	grp := &Group{ID: "grp-fb", Name: "fb-group"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := grp.ID
	user := &User{ID: "usr-fb", Username: "fb-user", PasswordHash: "x", GroupID: &gid, CreatedAt: time.Now()}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	groupKey, _ := repo.Create("fallback-key", "enc-fallback", "openai")

	// 仅分组级分配（无用户级）
	if err := repo.Assign(groupKey.ID, nil, &gid); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	found, err := repo.FindForUser(user.ID, grp.ID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if found == nil {
		t.Fatal("expected fallback group key, got nil")
	}
	if found.Name != "fallback-key" {
		t.Errorf("expected 'fallback-key', got %q", found.Name)
	}
}

// ---------------------------------------------------------------------------
// TestAPIKeyRepo_FindForUser_NoAssignment — 无分配时返回 nil
// ---------------------------------------------------------------------------

func TestAPIKeyRepo_FindForUser_NoAssignment(t *testing.T) {
	repo, _, _, cleanup := setupAPIKeyTest(t)
	defer cleanup()

	found, err := repo.FindForUser("nonexistent-user", "nonexistent-group")
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil for unassigned user, got %v", found)
	}
}
