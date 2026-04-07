package dashboard_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
)

// TestHandleLLMPage tests the LLM management page rendering
func TestHandleLLMPage(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create test user and group
	userRepo.Create(&db.User{ID: "user1", Username: "testuser"})
	groupRepo.Create(&db.Group{Name: "testgroup"})

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	// Set LLM dependencies
	h.SetLLMDeps(llmBindingRepo, func() []proxy.LLMTargetStatus {
		return []proxy.LLMTargetStatus{
			{URL: "http://llm1.example.com", Healthy: true},
			{URL: "http://llm2.example.com", Healthy: false},
		}
	})

	// Set drain functions
	h.SetDrainFunctions(
		func() error { return nil },
		func() error { return nil },
		func() proxy.DrainStatus {
			return proxy.DrainStatus{Draining: false}
		},
	)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create admin cookie
	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("basic_page_load", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/llm", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("with_flash_message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/llm?flash=test+message", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("with_error_message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/llm?error=test+error", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}

// TestHandleLLMCreateBinding tests creating LLM bindings
func TestHandleLLMCreateBinding(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create test user and group
	userRepo.Create(&db.User{ID: "user1", Username: "testuser"})
	groupRepo.Create(&db.Group{Name: "testgroup"})

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMDeps(llmBindingRepo, nil)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("create_user_binding", func(t *testing.T) {
		form := url.Values{}
		form.Set("target_url", "http://llm.example.com")
		form.Set("bind_type", "user")
		form.Set("user_id", "user1")

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "/dashboard/llm") {
			t.Errorf("redirect location = %q, want /dashboard/llm", loc)
		}
	})

	t.Run("create_group_binding", func(t *testing.T) {
		form := url.Values{}
		form.Set("target_url", "http://llm2.example.com")
		form.Set("bind_type", "group")
		form.Set("group_name", "testgroup")

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}
	})

	t.Run("missing_target_url", func(t *testing.T) {
		form := url.Values{}
		form.Set("bind_type", "user")
		form.Set("user_id", "user1")

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})

	t.Run("invalid_bind_type", func(t *testing.T) {
		form := url.Values{}
		form.Set("target_url", "http://llm.example.com")
		form.Set("bind_type", "invalid")

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})
}

// TestHandleLLMDeleteBinding tests deleting LLM bindings
func TestHandleLLMDeleteBinding(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create test binding
	userRepo.Create(&db.User{ID: "user1", Username: "testuser"})
	userID := "user1"
	llmBindingRepo.Set("http://llm.example.com", &userID, nil)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMDeps(llmBindingRepo, nil)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("delete_existing_binding", func(t *testing.T) {
		// Get the binding ID
		bindings, _ := llmBindingRepo.List()
		if len(bindings) == 0 {
			t.Fatal("no bindings found")
		}
		bindingID := bindings[0].ID

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings/"+bindingID+"/delete", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}
	})

	t.Run("delete_nonexistent_binding", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings/nonexistent-id/delete", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}
	})
}

// TestHandleDrainEnter tests entering drain mode
func TestHandleDrainEnter(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("drain_not_configured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/enter", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})

	t.Run("drain_success", func(t *testing.T) {
		drainCalled := false
		h.SetDrainFunctions(
			func() error {
				drainCalled = true
				return nil
			},
			nil,
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/enter", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		if !drainCalled {
			t.Error("drain function was not called")
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "flash=") {
			t.Errorf("expected flash message in redirect, got %q", loc)
		}
	})

	t.Run("drain_error", func(t *testing.T) {
		h.SetDrainFunctions(
			func() error {
				return errors.New("drain failed")
			},
			nil,
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/enter", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})
}

// TestHandleDrainExit tests exiting drain mode
func TestHandleDrainExit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("undrain_not_configured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/exit", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})

	t.Run("undrain_success", func(t *testing.T) {
		undrainCalled := false
		h.SetDrainFunctions(
			nil,
			func() error {
				undrainCalled = true
				return nil
			},
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/exit", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		if !undrainCalled {
			t.Error("undrain function was not called")
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "flash=") {
			t.Errorf("expected flash message in redirect, got %q", loc)
		}
	})

	t.Run("undrain_error", func(t *testing.T) {
		h.SetDrainFunctions(
			nil,
			func() error {
				return errors.New("undrain failed")
			},
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/drain/exit", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})
}

// TestHandleLLMDistribute tests the LLM distribution handler
func TestHandleLLMDistribute(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create test users
	userRepo.Create(&db.User{ID: "user1", Username: "user1", IsActive: true})
	userRepo.Create(&db.User{ID: "user2", Username: "user2", IsActive: true})
	userRepo.Create(&db.User{ID: "user3", Username: "user3", IsActive: false})

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	// Set LLM dependencies
	h.SetLLMDeps(llmBindingRepo, func() []proxy.LLMTargetStatus {
		return []proxy.LLMTargetStatus{
			{URL: "http://llm1.example.com", Healthy: true},
			{URL: "http://llm2.example.com", Healthy: true},
		}
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("distribute_success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/distribute", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusFound && rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 302 or 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "flash=") {
			t.Errorf("expected flash message in redirect, got %q", loc)
		}
	})

	t.Run("no_llm_targets", func(t *testing.T) {
		h2 := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
		h2.SetLLMDeps(llmBindingRepo, func() []proxy.LLMTargetStatus {
			return []proxy.LLMTargetStatus{}
		})

		mux2 := http.NewServeMux()
		h2.RegisterRoutes(mux2)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/distribute", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux2.ServeHTTP(rr, req)

		if rr.Code != http.StatusFound && rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 302 or 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})

	t.Run("llm_binding_not_configured", func(t *testing.T) {
		h3 := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

		mux3 := http.NewServeMux()
		h3.RegisterRoutes(mux3)

		req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/distribute", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux3.ServeHTTP(rr, req)

		if rr.Code != http.StatusFound && rr.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 302 or 303", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("expected error in redirect, got %q", loc)
		}
	})
}

// ---------------------------------------------------------------------------
// Fix 3: handleLLMCreateTarget and handleLLMUpdateTarget use ComboExists
// ---------------------------------------------------------------------------

// TestHandleLLMCreateTarget_SameURLDifferentKey_Succeeds verifies that when an LLM
// target exists for (url, keyA), creating another target at (url, keyB) succeeds.
// This is the regression test for Fix 3 (ComboExists vs URLExists).
func TestHandleLLMCreateTarget_SameURLDifferentKey_Succeeds(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gdb, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gdb); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gdb, logger)
	groupRepo := db.NewGroupRepo(gdb, logger)
	usageRepo := db.NewUsageRepo(gdb, logger)
	auditRepo := db.NewAuditRepo(logger, gdb)
	llmTargetRepo := db.NewLLMTargetRepo(gdb, logger)
	apiKeyRepo := db.NewAPIKeyRepo(gdb, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMTargetRepo(llmTargetRepo)
	h.SetLLMDeps(db.NewLLMBindingRepo(gdb, logger), func() []proxy.LLMTargetStatus { return nil })
	h.SetDrainFunctions(func() error { return nil }, func() error { return nil }, func() proxy.DrainStatus { return proxy.DrainStatus{} })

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tok, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "__admin__", Username: "admin", Role: "admin"}, time.Hour)
	cookie := &http.Cookie{Name: api.AdminCookieName, Value: tok}

	// Create two API keys
	keyA, err := apiKeyRepo.Create("keyA", "enc-A", "anthropic")
	if err != nil {
		t.Fatalf("create keyA: %v", err)
	}
	keyB, err := apiKeyRepo.Create("keyB", "enc-B", "openai")
	if err != nil {
		t.Fatalf("create keyB: %v", err)
	}

	const targetURL = "https://api.llm.example.com"

	// Create first target: (url, keyA) — should succeed
	form1 := url.Values{
		"url":        {targetURL},
		"provider":   {"anthropic"},
		"api_key_id": {keyA.ID},
	}
	req1 := httptest.NewRequest(http.MethodPost, "/dashboard/llm/targets", strings.NewReader(form1.Encode()))
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req1.AddCookie(cookie)
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if loc := rr1.Header().Get("Location"); strings.Contains(loc, "error=") {
		t.Fatalf("first target (url, keyA) creation failed: redirect to %q", loc)
	}

	// Create second target: (url, keyB) — should succeed (different API key)
	form2 := url.Values{
		"url":        {targetURL},
		"provider":   {"openai"},
		"api_key_id": {keyB.ID},
	}
	req2 := httptest.NewRequest(http.MethodPost, "/dashboard/llm/targets", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	loc2 := rr2.Header().Get("Location")
	if strings.Contains(loc2, "error=") {
		t.Errorf("second target (url, keyB) should succeed with ComboExists; redirect to %q — "+
			"old URLExists would have wrongly blocked this", loc2)
	}

	// Verify two targets exist
	targets, err := llmTargetRepo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("expected 2 targets in DB, got %d", len(targets))
	}
}

// TestHandleLLMCreateTarget_SameURLSameKey_IsRejected verifies that creating
// a duplicate (url, apiKeyID) combo is rejected.
func TestHandleLLMCreateTarget_SameURLSameKey_IsRejected(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gdb, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gdb); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gdb, logger)
	groupRepo := db.NewGroupRepo(gdb, logger)
	usageRepo := db.NewUsageRepo(gdb, logger)
	auditRepo := db.NewAuditRepo(logger, gdb)
	llmTargetRepo := db.NewLLMTargetRepo(gdb, logger)
	apiKeyRepo := db.NewAPIKeyRepo(gdb, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMTargetRepo(llmTargetRepo)
	h.SetLLMDeps(db.NewLLMBindingRepo(gdb, logger), func() []proxy.LLMTargetStatus { return nil })
	h.SetDrainFunctions(func() error { return nil }, func() error { return nil }, func() proxy.DrainStatus { return proxy.DrainStatus{} })

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tok, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "__admin__", Username: "admin", Role: "admin"}, time.Hour)
	cookie := &http.Cookie{Name: api.AdminCookieName, Value: tok}

	keyA, err := apiKeyRepo.Create("keyA", "enc-A", "anthropic")
	if err != nil {
		t.Fatalf("create keyA: %v", err)
	}

	const targetURL = "https://api.llm.example.com"

	// Create first target: (url, keyA)
	form := url.Values{"url": {targetURL}, "provider": {"anthropic"}, "api_key_id": {keyA.ID}}
	req1 := httptest.NewRequest(http.MethodPost, "/dashboard/llm/targets", strings.NewReader(form.Encode()))
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req1.AddCookie(cookie)
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)

	// Create duplicate (url, keyA) — should redirect with error
	req2 := httptest.NewRequest(http.MethodPost, "/dashboard/llm/targets", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	loc2 := rr2.Header().Get("Location")
	if !strings.Contains(loc2, "error=") {
		t.Errorf("duplicate (url, keyA) should be rejected; redirect location: %q", loc2)
	}
}

// TestHandleLLMUpdateTarget_ChangeAPIKeyID_CheckCombo verifies that when updating an LLM
// target's API key (but keeping URL the same), the ComboExists check is performed.
// This is the regression test for Fix #23: previously only URL changes triggered the check,
// allowing APIKeyID changes to bypass the uniqueness constraint.
func TestHandleLLMUpdateTarget_ChangeAPIKeyID_CheckCombo(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gdb, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gdb); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gdb, logger)
	groupRepo := db.NewGroupRepo(gdb, logger)
	usageRepo := db.NewUsageRepo(gdb, logger)
	auditRepo := db.NewAuditRepo(logger, gdb)
	llmTargetRepo := db.NewLLMTargetRepo(gdb, logger)
	apiKeyRepo := db.NewAPIKeyRepo(gdb, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMTargetRepo(llmTargetRepo)
	h.SetLLMDeps(db.NewLLMBindingRepo(gdb, logger), func() []proxy.LLMTargetStatus { return nil })
	h.SetDrainFunctions(func() error { return nil }, func() error { return nil }, func() proxy.DrainStatus { return proxy.DrainStatus{} })

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tok, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "__admin__", Username: "admin", Role: "admin"}, time.Hour)
	cookie := &http.Cookie{Name: api.AdminCookieName, Value: tok}

	// Create two API keys
	keyA, err := apiKeyRepo.Create("keyA", "enc-A", "anthropic")
	if err != nil {
		t.Fatalf("create keyA: %v", err)
	}
	keyB, err := apiKeyRepo.Create("keyB", "enc-B", "openai")
	if err != nil {
		t.Fatalf("create keyB: %v", err)
	}

	const targetURL = "https://api.llm.example.com"

	// Create target1: (url, keyA)
	target1 := &db.LLMTarget{
		ID:       uuid.New().String(),
		URL:      targetURL,
		Provider: "anthropic",
		APIKeyID: &keyA.ID,
	}
	if err := llmTargetRepo.Create(target1); err != nil {
		t.Fatalf("create target1: %v", err)
	}

	// Create target2: (url, keyB)
	target2 := &db.LLMTarget{
		ID:       uuid.New().String(),
		URL:      targetURL,
		Provider: "openai",
		APIKeyID: &keyB.ID,
	}
	if err := llmTargetRepo.Create(target2); err != nil {
		t.Fatalf("create target2: %v", err)
	}

	// Now try to update target2's APIKeyID from keyB to keyA
	// This should be rejected because (url, keyA) already exists (target1)
	updateForm := url.Values{
		"url":        {targetURL},
		"provider":   {"openai"},
		"api_key_id": {keyA.ID}, // Changing from keyB to keyA — should conflict
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/targets/"+target2.ID+"/update", strings.NewReader(updateForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("update to change APIKeyID from keyB to keyA should be rejected (combo conflict); "+
			"redirect to %q (expected error in redirect)", loc)
	}

	// Verify target2 still has keyB
	updated, err := llmTargetRepo.GetByID(target2.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.APIKeyID == nil || *updated.APIKeyID != keyB.ID {
		t.Errorf("target2 APIKeyID should remain %q, got %v", keyB.ID, updated.APIKeyID)
	}
}
