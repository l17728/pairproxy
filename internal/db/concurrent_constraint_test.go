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
