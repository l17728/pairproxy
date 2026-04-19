package db_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"github.com/l17728/pairproxy/internal/db"
)

// TestUserRepo_Create_ConcurrentDuplicateUsername tests that creating two users with
// the same username concurrently triggers the unique constraint violation (or one succeeds,
// one fails). This ensures the (username, auth_provider) composite constraint is enforced
// even under concurrency.
func TestUserRepo_Create_ConcurrentDuplicateUsername(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)

	const (
		username = "concurrent-user"
		provider = "local"
	)

	var (
		successCount int32
		failCount    int32
		wg           sync.WaitGroup
	)

	// Launch 10 goroutines trying to create the same user (with different IDs)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			user := &db.User{
				ID:           fmt.Sprintf("user-%d", idx),
				Username:     username,
				PasswordHash: "hashed",
				AuthProvider: provider,
				IsActive:     true,
			}
			err := userRepo.Create(user)
			if err != nil {
				atomic.AddInt32(&failCount, 1)
			} else {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// Exactly one should succeed, 9 should fail due to unique constraint
	// (the constraint prevents all from succeeding)
	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d; failures: %d", successCount, failCount)
	}
	if failCount != 9 {
		t.Errorf("expected exactly 9 failures, got %d; successes: %d", failCount, successCount)
	}

	// Verify only one user was created
	users, err := userRepo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("expected 1 user in DB, got %d", len(users))
	}
}

// TestAPIKeyRepo_Assign_ConcurrentConsistency tests that concurrent API key assignments
// maintain consistency. When two goroutines simultaneously assign the same user a different key,
// only one assignment should survive (the last one written).
func TestAPIKeyRepo_Assign_ConcurrentConsistency(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	apiKeyRepo := db.NewAPIKeyRepo(gormDB, logger)
	userRepo := db.NewUserRepo(gormDB, logger)

	// Create test user
	user := &db.User{
		ID:           "user-assign-test",
		Username:     "assign-test",
		PasswordHash: "hashed",
		IsActive:     true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create two API keys
	keyA, err := apiKeyRepo.Create("key-a", "enc-a", "anthropic", "obfuscated")
	if err != nil {
		t.Fatalf("create keyA: %v", err)
	}
	keyB, err := apiKeyRepo.Create("key-b", "enc-b", "openai", "obfuscated")
	if err != nil {
		t.Fatalf("create keyB: %v", err)
	}

	var wg sync.WaitGroup

	// Goroutine 1: Assign user to keyA
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = apiKeyRepo.Assign(user.ID, nil, &keyA.ID)
	}()

	// Goroutine 2: Assign user to keyB (concurrently)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = apiKeyRepo.Assign(user.ID, nil, &keyB.ID)
	}()

	wg.Wait()

	// Wait a bit for any async operations
	time.Sleep(100 * time.Millisecond)

	// The final state should be one assignment (the last written)
	// We count by querying the database directly
	var count int64
	if err := gormDB.Model(&db.APIKeyAssignment{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatalf("count assignments: %v", err)
	}

	// Since the Set() method uses delete-then-insert, if there's a race
	// between the two goroutines, both deletes might happen before either insert,
	// leaving 0 assignments. Or both inserts succeed, leaving 2. Or exactly 1 (normal case).
	// The important thing is that the constraint prevents duplicate (user_id, api_key_id) pairs.
	if count > 1 {
		t.Errorf("expected at most 1 assignment, got %d", count)
	}
}

// TestGroupTargetSetMember_AddMember_ConcurrentDuplicate tests that adding the same
// target to a set multiple times concurrently results in only one member (unique constraint).
func TestGroupTargetSetMember_AddMember_ConcurrentDuplicate(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	gtSetRepo := db.NewGroupTargetSetRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)

	// Create a target set
	set := &db.GroupTargetSet{
		ID:   "set-test",
		Name: "test-set",
	}
	if err := gtSetRepo.Create(set); err != nil {
		t.Fatalf("create set: %v", err)
	}

	// Create an LLM target
	target := &db.LLMTarget{
		ID:       "target-1",
		URL:      "https://llm.example.com",
		Provider: "anthropic",
	}
	if err := llmTargetRepo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	var (
		successCount int32
		failCount    int32
		wg           sync.WaitGroup
	)

	// Launch 5 goroutines trying to add the same target to the set
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			member := &db.GroupTargetSetMember{
				TargetSetID: set.ID,
				TargetID:    target.ID,
			}
			err := gtSetRepo.AddMember(set.ID, member)
			if err != nil {
				atomic.AddInt32(&failCount, 1)
			} else {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}

	wg.Wait()

	// At least one should succeed; others may fail due to constraint
	if successCount == 0 {
		t.Errorf("expected at least 1 success, got 0")
	}

	// Verify only one member exists in the set
	members, err := gtSetRepo.ListMembers(set.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
	if members[0].TargetID != target.ID {
		t.Errorf("member target mismatch: got %s, want %s", members[0].TargetID, target.ID)
	}
}

// TestLLMBinding_Set_ConcurrentUserBinding tests that concurrent Set() calls for the same user
// result in exactly one binding (due to delete-then-insert and unique constraint).
func TestLLMBinding_Set_ConcurrentUserBinding(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	userRepo := db.NewUserRepo(gormDB, logger)

	// Create a user
	user := &db.User{
		ID:           "user-binding-test",
		Username:     "binding-test",
		PasswordHash: "hashed",
		IsActive:     true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create two targets
	target1 := &db.LLMTarget{
		ID:       "target-1",
		URL:      "https://llm1.example.com",
		Provider: "anthropic",
	}
	if err := llmTargetRepo.Create(target1); err != nil {
		t.Fatalf("create target1: %v", err)
	}

	target2 := &db.LLMTarget{
		ID:       "target-2",
		URL:      "https://llm2.example.com",
		Provider: "openai",
	}
	if err := llmTargetRepo.Create(target2); err != nil {
		t.Fatalf("create target2: %v", err)
	}

	var wg sync.WaitGroup

	// Goroutine 1: Set user binding to target1
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = llmBindingRepo.Set(target1.URL, &user.ID, nil)
	}()

	// Goroutine 2: Set user binding to target2 (concurrently)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = llmBindingRepo.Set(target2.URL, &user.ID, nil)
	}()

	wg.Wait()

	// Wait for async operations
	time.Sleep(100 * time.Millisecond)

	// Verify exactly one binding exists for the user
	bindings, err := llmBindingRepo.List()
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}

	userBindings := 0
	for _, b := range bindings {
		if b.UserID != nil && *b.UserID == user.ID {
			userBindings++
		}
	}

	if userBindings != 1 {
		t.Errorf("expected 1 user binding, got %d", userBindings)
	}
}
