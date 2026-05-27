package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupAPIKeyTest creates an AdminHandler wired with an API key repo and a
// trivial encrypt function (identity for testability).
func setupAPIKeyTest(t *testing.T) (*AdminHandler, *auth.Manager, *http.ServeMux, *db.APIKeyRepo) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "apikey-test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	apiKeyRepo := db.NewAPIKeyRepo(gormDB, logger)

	hash, _ := auth.HashPassword(logger, "adminpass")
	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, hash, time.Hour)

	// Use a pass-through "encrypt" function for test simplicity.
	handler.SetAPIKeyRepo(apiKeyRepo, func(plain string) (string, error) {
		return "enc:" + plain, nil
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return handler, jwtMgr, mux, apiKeyRepo
}

// ---------------------------------------------------------------------------
// TestAdminAPIKeyCRUD — list / create / assign / revoke
// ---------------------------------------------------------------------------

func TestAdminAPIKeyCRUD(t *testing.T) {
	_, jwtMgr, mux, _ := setupAPIKeyTest(t)
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	var createdKeyID string

	t.Run("list api keys empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/api-keys", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var keys []apiKeyResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &keys)
		if keys == nil {
			t.Error("expected non-nil slice")
		}
	})

	t.Run("create api key returns 201 with key details", func(t *testing.T) {
		body, _ := json.Marshal(createAPIKeyRequest{
			Name:     "prod-key",
			Value:    "sk-ant-testkey",
			Provider: "anthropic",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
		}
		var resp apiKeyResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Name != "prod-key" {
			t.Errorf("Name = %q, want prod-key", resp.Name)
		}
		if resp.Provider != "anthropic" {
			t.Errorf("Provider = %q, want anthropic", resp.Provider)
		}
		createdKeyID = resp.ID
	})

	t.Run("list api keys after create", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/api-keys", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		var keys []apiKeyResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &keys)
		if len(keys) == 0 {
			t.Error("expected ≥1 key after create")
		}
	})

	t.Run("revoke api key", func(t *testing.T) {
		if createdKeyID == "" {
			t.Skip("no key ID captured")
		}
		req := httptest.NewRequest(http.MethodDelete,
			"/api/admin/api-keys/"+createdKeyID, nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Errorf("revoke: status = %d, want 204; body: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestAdminAPIKeyCreate_Validation(t *testing.T) {
	_, jwtMgr, mux, _ := setupAPIKeyTest(t)
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	t.Run("missing name returns 400", func(t *testing.T) {
		body, _ := json.Marshal(createAPIKeyRequest{Value: "sk-x"})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("missing value returns 400", func(t *testing.T) {
		body, _ := json.Marshal(createAPIKeyRequest{Name: "key-name"})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewBuffer(body))
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestAdminAPIKey_NotConfigured(t *testing.T) {
	// Create handler WITHOUT SetAPIKeyRepo.
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "s")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })
	hash, _ := auth.HashPassword(logger, "ap")
	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
		db.NewAuditRepo(logger, gormDB),
		hash, time.Hour)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok := adminToken(t, jwtMgr)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when api key repo not configured", rr.Code)
	}
}
