package db_test

import (
	"testing"

	"github.com/l17728/pairproxy/internal/db"
	"go.uber.org/zap/zaptest"
)

// TestUser_Composite_NULLHandling tests the (username, auth_provider) composite constraint
// with NULL values (only ExternalID can be NULL, since auth_provider is always set).
// NULL values are distinct in UNIQUE constraints (NULL != NULL), so:
// - (username="alice", auth_provider="local", external_id=NULL) ✓ allowed
// - (username="alice", auth_provider="ldap", external_id="uuid-123") ✓ allowed (different provider)
func TestUser_Composite_NULLHandling(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)

	// Create local "alice" (external_id = NULL)
	user1 := &db.User{
		ID:           "local-alice",
		Username:     "alice",
		PasswordHash: "hashed1",
		AuthProvider: "local",
		ExternalID:   nil, // NULL
		IsActive:     true,
	}
	if err := userRepo.Create(user1); err != nil {
		t.Fatalf("create local alice: %v", err)
	}

	// Create LDAP "alice" with external_id (different provider, so composite constraint allows)
	extID := "ldap-alice-123"
	user2 := &db.User{
		ID:           "ldap-alice",
		Username:     "alice",
		PasswordHash: "", // LDAP typically has empty password
		AuthProvider: "ldap",
		ExternalID:   &extID,
		IsActive:     true,
	}
	if err := userRepo.Create(user2); err != nil {
		t.Fatalf("create ldap alice: %v", err)
	}

	// Verify both users exist
	users := []*db.User{user1, user2}
	for i, expected := range users {
		found, err := userRepo.GetByID(expected.ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", expected.ID, err)
		}
		if found == nil {
			t.Fatalf("user %d not found", i+1)
		}
		if found.AuthProvider != expected.AuthProvider {
			t.Errorf("user %d: auth_provider mismatch", i+1)
		}
	}

	// Try to create duplicate LDAP "alice" with same external_id — should fail
	// (composite constraint on auth_provider + external_id)
	user3 := &db.User{
		ID:           "ldap-alice-2",
		Username:     "alice",
		PasswordHash: "",
		AuthProvider: "ldap",
		ExternalID:   &extID, // Same external_id as user2
		IsActive:     true,
	}
	err = userRepo.Create(user3)
	if err == nil {
		t.Error("expected error creating duplicate (auth_provider, external_id), but succeeded")
	}

	// Try to create another local "alice" — should fail (same username + auth_provider)
	user4 := &db.User{
		ID:           "local-alice-2",
		Username:     "alice",
		PasswordHash: "hashed2",
		AuthProvider: "local",
		ExternalID:   nil,
		IsActive:     true,
	}
	err = userRepo.Create(user4)
	if err == nil {
		t.Error("expected error creating duplicate (username, auth_provider), but succeeded")
	}
}

// TestLLMTarget_Composite_NULLHandling tests the URL-unique constraint on llm_targets.
// Since v3.1.0 the URL column has a single-column UNIQUE index; the old composite
// (url, api_key_id) constraint no longer exists. Any second target at the same URL
// must be rejected regardless of the APIKeyID value.
func TestLLMTarget_Composite_NULLHandling(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)

	const targetURL = "https://llm.example.com"
	const targetURL2 = "https://llm2.example.com"

	// Create target with APIKeyID = NULL — succeeds.
	target1 := &db.LLMTarget{
		ID:       "target-1",
		URL:      targetURL,
		Provider: "anthropic",
		APIKeyID: nil,
	}
	if err := llmTargetRepo.Create(target1); err != nil {
		t.Fatalf("create target1: %v", err)
	}

	// Second target at the same URL with NULL APIKeyID — must fail (URL unique).
	target2 := &db.LLMTarget{
		ID:       "target-2",
		URL:      targetURL,
		Provider: "openai",
		APIKeyID: nil,
	}
	if err := llmTargetRepo.Create(target2); err == nil {
		t.Error("expected error creating second target at same URL with nil APIKeyID, but succeeded")
	}

	// Second target at the same URL with a non-NULL APIKeyID — must also fail (URL unique).
	keyID := "key-1"
	target3 := &db.LLMTarget{
		ID:       "target-3",
		URL:      targetURL,
		Provider: "anthropic",
		APIKeyID: &keyID,
	}
	if err := llmTargetRepo.Create(target3); err == nil {
		t.Error("expected error creating second target at same URL with non-nil APIKeyID, but succeeded")
	}

	// A target at a different URL is allowed even with the same APIKeyID.
	target4 := &db.LLMTarget{
		ID:       "target-4",
		URL:      targetURL2,
		Provider: "openai",
		APIKeyID: &keyID,
	}
	if err := llmTargetRepo.Create(target4); err != nil {
		t.Fatalf("create target4 (different URL): %v", err)
	}
}

// TestLLMBinding_GroupBinding_1N_NULLHandling tests that group bindings use 1:N semantics:
// - AddGroupBinding is idempotent for the same (group_id, target_id) pair
// - Adding a second target with the same provider is allowed (1:N)
// - Adding a target with a different provider is rejected (provider consistency constraint)
// - Set() with groupID=non-nil and userID=nil is rejected (must use AddGroupBinding for groups)
func TestLLMBinding_GroupBinding_1N_NULLHandling(t *testing.T) {
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

	// Create two anthropic targets and one openai target
	target1 := &db.LLMTarget{ID: "target-1", URL: "https://llm1.example.com", Provider: "anthropic"}
	target2 := &db.LLMTarget{ID: "target-2", URL: "https://llm2.example.com", Provider: "anthropic"}
	targetOAI := &db.LLMTarget{ID: "target-oai", URL: "https://oai.example.com", Provider: "openai"}
	for _, tgt := range []*db.LLMTarget{target1, target2, targetOAI} {
		if err := llmTargetRepo.Create(tgt); err != nil {
			t.Fatalf("create target %s: %v", tgt.ID, err)
		}
	}

	groupID := "group-1"

	// Add target1 to group
	if err := llmBindingRepo.AddGroupBinding(target1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding target1: %v", err)
	}

	// Adding target1 again is idempotent (no error, still 1 binding)
	if err := llmBindingRepo.AddGroupBinding(target1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding target1 (idempotent): %v", err)
	}
	ids, err := llmBindingRepo.FindAllForGroup(groupID)
	if err != nil {
		t.Fatalf("FindAllForGroup: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 binding after idempotent add, got %d", len(ids))
	}

	// Adding target2 (same provider) is allowed → 2 bindings
	if err := llmBindingRepo.AddGroupBinding(target2.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding target2 (same provider): %v", err)
	}
	ids, err = llmBindingRepo.FindAllForGroup(groupID)
	if err != nil {
		t.Fatalf("FindAllForGroup after target2: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 bindings after same-provider add, got %d", len(ids))
	}

	// Adding openai target to group with existing anthropic → provider conflict
	err = llmBindingRepo.AddGroupBinding(targetOAI.ID, groupID)
	if err == nil {
		t.Error("expected provider conflict error when mixing providers, got nil")
	}

	// Set() with userID=nil must be rejected (use AddGroupBinding for groups)
	err = llmBindingRepo.Set(target1.ID, nil, &groupID)
	if err == nil {
		t.Error("expected error from Set() with userID=nil, got nil")
	}
}

