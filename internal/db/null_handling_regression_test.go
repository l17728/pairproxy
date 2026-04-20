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

// TestLLMBinding_Composite_NULLHandling tests the (user_id, target_id) and (group_id, target_id)
// composite constraints with NULL values.
// Key insight: UNIQUE(user_id, target_id) with NULLs means:
// - Multiple rows with user_id=NULL are all distinct (allowed)
// - But application should never have multiple NULL user_ids (semantically wrong)
func TestLLMBinding_Composite_NULLHandling(t *testing.T) {
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

	// Set binding for group "group-1" to target1
	groupID := "group-1"
	if err := llmBindingRepo.Set(target1.URL, nil, &groupID); err != nil {
		t.Fatalf("set group binding to target1: %v", err)
	}

	// Try to set binding for same group to target1 again (should replace, not duplicate)
	// This tests that Set() (delete-then-insert) maintains uniqueness
	if err := llmBindingRepo.Set(target1.URL, nil, &groupID); err != nil {
		t.Fatalf("set same group binding again: %v", err)
	}

	// Set binding for same group to target2 (different target)
	if err := llmBindingRepo.Set(target2.URL, nil, &groupID); err != nil {
		t.Fatalf("set group binding to target2: %v", err)
	}

	// Verify only one binding exists for the group (the last Set() call overwrites)
	bindings, err := llmBindingRepo.List()
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}

	groupBindings := 0
	for _, b := range bindings {
		if b.GroupID != nil && *b.GroupID == groupID {
			groupBindings++
		}
	}

	if groupBindings != 1 {
		t.Errorf("expected 1 binding for group after Set() calls, got %d", groupBindings)
	}
}

// TestGroupTargetSetMember_Composite_NULLHandling tests the (target_set_id, target_id)
// composite constraint. Both fields are NOT NULL, so NULL handling is not applicable.
// This test documents that the constraint is strict (no NULL values).
func TestGroupTargetSetMember_Composite_NULLHandling(t *testing.T) {
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
		ID:   "set-1",
		Name: "test-set",
	}
	if err := gtSetRepo.Create(set); err != nil {
		t.Fatalf("create set: %v", err)
	}

	// Create a target
	target := &db.LLMTarget{
		ID:       "target-1",
		URL:      "https://llm.example.com",
		Provider: "anthropic",
	}
	if err := llmTargetRepo.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	// Add member
	member := &db.GroupTargetSetMember{
		TargetSetID: set.ID,
		TargetID:    target.ID,
	}
	if err := gtSetRepo.AddMember(set.ID, member); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Both target_set_id and target_id are NOT NULL and unique-constrained,
	// so no special NULL handling — composite constraint is strict.
	// Verify the member exists
	members, err := gtSetRepo.ListMembers(set.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
}
