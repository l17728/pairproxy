package db

import (
	"testing"

	"go.uber.org/zap/zaptest"
)

func setupSemanticRouteRepo(t *testing.T) *SemanticRouteRepo {
	t.Helper()
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return NewSemanticRouteRepo(gormDB, logger)
}

func TestSemanticRouteRepo_CreateAndGet(t *testing.T) {
	repo := setupSemanticRouteRepo(t)

	route, err := repo.Create("code-gen", "Generate code from natural language", []string{"http://llm-a:8080", "http://llm-b:8080"}, 10)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if route.ID == "" {
		t.Error("expected non-empty ID")
	}
	if route.Name != "code-gen" {
		t.Errorf("Name = %q, want %q", route.Name, "code-gen")
	}
	if route.Priority != 10 {
		t.Errorf("Priority = %d, want 10", route.Priority)
	}
	if !route.IsActive {
		t.Error("expected IsActive=true")
	}
	if route.Source != "database" {
		t.Errorf("Source = %q, want %q", route.Source, "database")
	}

	// GetByID
	got, err := repo.GetByID(route.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "code-gen" {
		t.Errorf("GetByID Name = %q, want %q", got.Name, "code-gen")
	}

	// GetByName
	got2, err := repo.GetByName("code-gen")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got2.ID != route.ID {
		t.Errorf("GetByName ID = %q, want %q", got2.ID, route.ID)
	}
}

func TestSemanticRouteRepo_GetByID_NotFound(t *testing.T) {
	repo := setupSemanticRouteRepo(t)
	_, err := repo.GetByID("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}

func TestSemanticRouteRepo_ListAll(t *testing.T) {
	repo := setupSemanticRouteRepo(t)

	// Create two routes with different priorities
	_, _ = repo.Create("low-prio", "low priority route", []string{"http://a:8080"}, 1)
	_, _ = repo.Create("high-prio", "high priority route", []string{"http://b:8080"}, 100)

	routes, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("ListAll count = %d, want 2", len(routes))
	}
	// Should be ordered by priority DESC
	if routes[0].Name != "high-prio" {
		t.Errorf("first route = %q, want %q (highest priority)", routes[0].Name, "high-prio")
	}
	if routes[1].Name != "low-prio" {
		t.Errorf("second route = %q, want %q", routes[1].Name, "low-prio")
	}
}

func TestSemanticRouteRepo_Update(t *testing.T) {
	repo := setupSemanticRouteRepo(t)
	route, _ := repo.Create("updatable", "original desc", []string{"http://a:8080"}, 5)

	err := repo.Update(route.ID, map[string]interface{}{
		"description": "updated desc",
		"priority":    99,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := repo.GetByID(route.ID)
	if got.Description != "updated desc" {
		t.Errorf("Description = %q, want %q", got.Description, "updated desc")
	}
	if got.Priority != 99 {
		t.Errorf("Priority = %d, want 99", got.Priority)
	}
}

func TestSemanticRouteRepo_Delete(t *testing.T) {
	repo := setupSemanticRouteRepo(t)
	route, _ := repo.Create("deletable", "will be deleted", []string{"http://a:8080"}, 1)

	err := repo.Delete(route.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.GetByID(route.ID)
	if err == nil {
		t.Error("expected error after Delete, got nil")
	}
}

func TestSemanticRouteRepo_SetActive(t *testing.T) {
	repo := setupSemanticRouteRepo(t)
	route, _ := repo.Create("toggleable", "toggle test", []string{"http://a:8080"}, 1)

	// Disable
	if err := repo.SetActive(route.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	got, _ := repo.GetByID(route.ID)
	if got.IsActive {
		t.Error("expected IsActive=false after disable")
	}

	// Re-enable
	if err := repo.SetActive(route.ID, true); err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	got, _ = repo.GetByID(route.ID)
	if !got.IsActive {
		t.Error("expected IsActive=true after enable")
	}
}

func TestSemanticRoute_DecodeTargetURLs(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		sr := &SemanticRoute{TargetURLsJSON: `["http://a:8080","http://b:8080"]`}
		urls, err := sr.DecodeTargetURLs()
		if err != nil {
			t.Fatalf("DecodeTargetURLs: %v", err)
		}
		if len(urls) != 2 {
			t.Fatalf("len = %d, want 2", len(urls))
		}
		if urls[0] != "http://a:8080" {
			t.Errorf("urls[0] = %q", urls[0])
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		sr := &SemanticRoute{TargetURLsJSON: `not-json`}
		_, err := sr.DecodeTargetURLs()
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("empty array", func(t *testing.T) {
		sr := &SemanticRoute{TargetURLsJSON: `[]`}
		urls, err := sr.DecodeTargetURLs()
		if err != nil {
			t.Fatalf("DecodeTargetURLs: %v", err)
		}
		if len(urls) != 0 {
			t.Errorf("len = %d, want 0", len(urls))
		}
	})

	t.Run("TargetURLs backward compat", func(t *testing.T) {
		sr := &SemanticRoute{TargetURLsJSON: `["http://x:8080"]`}
		urls := sr.TargetURLs()
		if len(urls) != 1 || urls[0] != "http://x:8080" {
			t.Errorf("TargetURLs() = %v", urls)
		}
	})
}
