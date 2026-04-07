package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// mockProvider — 测试用认证提供者 mock
// ---------------------------------------------------------------------------

type mockProvider struct {
	user *auth.ProviderUser
	err  error
}

func (m *mockProvider) Authenticate(_ context.Context, _, _ string) (*auth.ProviderUser, error) {
	return m.user, m.err
}

// ---------------------------------------------------------------------------
// setupProviderTest — 创建带 mock provider 的 AuthHandler
// ---------------------------------------------------------------------------

func setupProviderTest(t *testing.T, p auth.Provider) (*AuthHandler, *http.ServeMux, *db.UserRepo) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	tokenRepo := db.NewRefreshTokenRepo(gormDB, logger)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	handler := NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, AuthConfig{
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	})
	handler.SetProvider(p)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return handler, mux, userRepo
}

// ---------------------------------------------------------------------------
// TestJITProvisioning — 首次 LDAP 登录自动创建用户
// ---------------------------------------------------------------------------

func TestJITProvisioning(t *testing.T) {
	provider := &mockProvider{
		user: &auth.ProviderUser{
			ExternalID: "ldap-uid-alice",
			Username:     "alice",
			Email:        "alice@example.com",
			AuthProvider: "ldap",
		},
	}
	_, mux, userRepo := setupProviderTest(t, provider)

	// 首次登录
	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "alice",
		Password: "any-password", // provider mock 不验证
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// 验证用户已在 DB 中创建
	user, err := userRepo.GetByExternalID("ldap", "ldap-uid-alice")
	if err != nil {
		t.Fatalf("GetByExternalID: %v", err)
	}
	if user == nil {
		t.Fatal("expected JIT-created user, got nil")
	}
	if user.Username != "alice" {
		t.Errorf("Username = %q, want alice", user.Username)
	}
	if user.AuthProvider != "ldap" {
		t.Errorf("AuthProvider = %q, want ldap", user.AuthProvider)
	}
	if *user.ExternalID != "ldap-uid-alice" {
		t.Errorf("ExternalID = %q, want ldap-uid-alice", *user.ExternalID)
	}
	if !user.IsActive {
		t.Error("JIT-created user should be active")
	}
}

// ---------------------------------------------------------------------------
// TestJITProvisioning_Idempotent — 二次登录不重复创建用户
// ---------------------------------------------------------------------------

func TestJITProvisioning_Idempotent(t *testing.T) {
	provider := &mockProvider{
		user: &auth.ProviderUser{
			ExternalID: "ldap-uid-bob",
			Username:     "bob",
			AuthProvider: "ldap",
		},
	}
	_, mux, userRepo := setupProviderTest(t, provider)

	// 第一次登录
	rr1 := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "bob",
		Password: "pass",
	})
	if rr1.Code != http.StatusOK {
		t.Fatalf("first login status = %d; body: %s", rr1.Code, rr1.Body.String())
	}

	// 第二次登录
	rr2 := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "bob",
		Password: "pass",
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("second login status = %d; body: %s", rr2.Code, rr2.Body.String())
	}

	// 验证 DB 中只有一个 bob 用户
	users, err := userRepo.ListByGroup("")
	if err != nil {
		t.Fatalf("ListByGroup: %v", err)
	}
	count := 0
	for _, u := range users {
		if u.ExternalID != nil && *u.ExternalID == "ldap-uid-bob" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 user with external_id=ldap-uid-bob, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TestJITProvisioning_AuthFailed — Provider 认证失败返回 401
// ---------------------------------------------------------------------------

func TestJITProvisioning_AuthFailed(t *testing.T) {
	provider := &mockProvider{
		err: context.Canceled, // 任意 error
	}
	_, mux, _ := setupProviderTest(t, provider)

	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "eve",
		Password: "wrong",
	})

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestJITProvisioning_DisabledUser — 已被禁用的 JIT 用户返回 403
// ---------------------------------------------------------------------------

func TestJITProvisioning_DisabledUser(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	userRepo := db.NewUserRepo(gormDB, logger)
	tokenRepo := db.NewRefreshTokenRepo(gormDB, logger)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	// 预先创建被禁用的用户（先创建再禁用，因为 GORM bool default:true 会忽略零值）
	disabledUser := &db.User{
		Username:     "disabled-ldap",
		PasswordHash: "",
		AuthProvider: "ldap",
		ExternalID: func(s string) *string { return &s }("ldap-uid-disabled"),
	}
	if err := userRepo.Create(disabledUser); err != nil {
		t.Fatalf("Create disabled user: %v", err)
	}
	if err := userRepo.SetActive(disabledUser.ID, false); err != nil {
		t.Fatalf("SetActive false: %v", err)
	}

	provider := &mockProvider{
		user: &auth.ProviderUser{
			ExternalID: "ldap-uid-disabled",
			Username:     "disabled-ldap",
			AuthProvider: "ldap",
		},
	}

	handler := NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, AuthConfig{
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	})
	handler.SetProvider(provider)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "disabled-ldap",
		Password: "pass",
	})

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for disabled user", rr.Code)
	}
}
