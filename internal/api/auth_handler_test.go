package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"gorm.io/gorm"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
)

// setupTest 初始化内存 DB + 测试用户，返回 AuthHandler 和 HTTP mux。
func setupTest(t *testing.T) (*AuthHandler, *http.ServeMux, *gorm.DB) {
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

	// 创建测试用户（password: "correct-password"）
	hash, err := auth.HashPassword(logger, "correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := userRepo.Create(&db.User{
		Username:     "testuser",
		PasswordHash: hash,
	}); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	handler := NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, AuthConfig{
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return handler, mux, gormDB
}

func doRequest(t *testing.T, mux http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// TestLoginSuccess
// ---------------------------------------------------------------------------

func TestLoginSuccess(t *testing.T) {
	_, mux, _ := setupTest(t)

	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "testuser",
		Password: "correct-password",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp loginResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("access_token should not be empty")
	}
	if resp.RefreshToken == "" {
		t.Error("refresh_token should not be empty")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", resp.TokenType)
	}
}

// ---------------------------------------------------------------------------
// TestLoginWrongPassword → 401
// ---------------------------------------------------------------------------

func TestLoginWrongPassword(t *testing.T) {
	_, mux, _ := setupTest(t)

	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "testuser",
		Password: "wrong-password",
	})

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestLoginUnknownUser → 401 (不暴露用户是否存在)
// ---------------------------------------------------------------------------

func TestLoginUnknownUser(t *testing.T) {
	_, mux, _ := setupTest(t)

	rr := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "nosuchuser",
		Password: "any-password",
	})

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestRefreshSuccess
// ---------------------------------------------------------------------------

func TestRefreshSuccess(t *testing.T) {
	_, mux, _ := setupTest(t)

	// 先登录，拿到 refresh_token
	loginRR := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "testuser",
		Password: "correct-password",
	})
	var loginResp loginResponse
	_ = json.NewDecoder(loginRR.Body).Decode(&loginResp)

	// 使用 refresh_token 换新 access_token
	rr := doRequest(t, mux, http.MethodPost, "/auth/refresh", refreshRequest{
		RefreshToken: loginResp.RefreshToken,
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp refreshResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.AccessToken == "" {
		t.Error("new access_token should not be empty")
	}
	if resp.AccessToken == loginResp.AccessToken {
		t.Error("new access_token should differ from original")
	}
}

// ---------------------------------------------------------------------------
// TestRefreshRevoked → 401
// ---------------------------------------------------------------------------

func TestRefreshRevoked(t *testing.T) {
	handler, mux, _ := setupTest(t)
	_ = handler // just to check it's initialized

	// 登录
	loginRR := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "testuser",
		Password: "correct-password",
	})
	var loginResp loginResponse
	_ = json.NewDecoder(loginRR.Body).Decode(&loginResp)

	// 登出（撤销 refresh_token）
	doRequest(t, mux, http.MethodPost, "/auth/logout", logoutRequest{
		RefreshToken: loginResp.RefreshToken,
	})

	// 再次用已撤销的 refresh_token 刷新 → 401
	rr := doRequest(t, mux, http.MethodPost, "/auth/refresh", refreshRequest{
		RefreshToken: loginResp.RefreshToken,
	})

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 after revoke", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestLogout — 登出后旧 access_token 返回 401
// ---------------------------------------------------------------------------

func TestLogout(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	userRepo := db.NewUserRepo(gormDB, logger)
	tokenRepo := db.NewRefreshTokenRepo(gormDB, logger)

	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	hash, _ := auth.HashPassword(logger, "pw")
	_ = userRepo.Create(&db.User{Username: "user2", PasswordHash: hash})

	authHandler := NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, DefaultAuthConfig)
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)

	// 登录
	loginRR := doRequest(t, mux, http.MethodPost, "/auth/login", loginRequest{
		Username: "user2", Password: "pw",
	})
	var loginResp loginResponse
	_ = json.NewDecoder(loginRR.Body).Decode(&loginResp)

	// 在登出之前解析 token，获取 JTI（登出后 token 会被加入黑名单，Parse 会失败）
	claims, err := jwtMgr.Parse(loginResp.AccessToken)
	if err != nil {
		t.Fatalf("Parse before logout: %v", err)
	}
	jti := claims.JTI

	// 带 access_token 登出
	req := httptest.NewRequest(http.MethodPost, "/auth/logout",
		bytes.NewBufferString(`{"refresh_token":"`+loginResp.RefreshToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+loginResp.AccessToken)
	logoutRR := httptest.NewRecorder()
	mux.ServeHTTP(logoutRR, req)

	if logoutRR.Code != http.StatusNoContent {
		t.Errorf("logout status = %d, want 204", logoutRR.Code)
	}

	// 验证旧 access_token 已被加入黑名单
	if !jwtMgr.IsBlacklisted(jti) {
		t.Error("access_token JTI should be blacklisted after logout")
	}
}
