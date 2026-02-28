package db

import (
	"testing"

	"go.uber.org/zap/zaptest"
)

func TestAuditRepo_CreateAndList(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, gormDB)

	// Create three audit records with distinct actions.
	actions := []struct{ action, target, detail string }{
		{"user.create", "alice", `{"is_active":true}`},
		{"group.create", "engineering", `{"daily_limit":null}`},
		{"user.set_active", "alice", `{"active":false}`},
	}
	for _, a := range actions {
		if err := repo.Create("admin", a.action, a.target, a.detail); err != nil {
			t.Fatalf("Create(%s): %v", a.action, err)
		}
	}

	// ListRecent should return all three, newest first.
	logs, err := repo.ListRecent(10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("want 3 logs, got %d", len(logs))
	}

	// Newest record first: user.set_active was inserted last.
	if logs[0].Action != "user.set_active" {
		t.Errorf("want newest action user.set_active, got %q", logs[0].Action)
	}
	if logs[2].Action != "user.create" {
		t.Errorf("want oldest action user.create, got %q", logs[2].Action)
	}

	// Verify operator field.
	for _, l := range logs {
		if l.Operator != "admin" {
			t.Errorf("expected operator=admin, got %q", l.Operator)
		}
	}
}

func TestAuditRepo_ListLimit(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, gormDB)

	// Insert 5 records.
	for i := 0; i < 5; i++ {
		if err := repo.Create("admin", "user.create", "u", ""); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Request only 3.
	logs, err := repo.ListRecent(3)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("want 3, got %d", len(logs))
	}
}

func TestAuditRepo_ListDefault(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, gormDB)

	// Limit=0 should fall back to 100.
	logs, err := repo.ListRecent(0)
	if err != nil {
		t.Fatalf("ListRecent(0): %v", err)
	}
	// With an empty table the result should just be an empty slice.
	if logs == nil {
		t.Error("expected non-nil slice")
	}
}

func TestAuditRepo_EmptyDetail(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, gormDB)

	// user.reset_password intentionally stores an empty detail for security.
	if err := repo.Create("admin", "user.reset_password", "bob", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	logs, _ := repo.ListRecent(1)
	if len(logs) != 1 {
		t.Fatalf("want 1 log, got %d", len(logs))
	}
	if logs[0].Detail != "" {
		t.Errorf("expected empty detail, got %q", logs[0].Detail)
	}
}
