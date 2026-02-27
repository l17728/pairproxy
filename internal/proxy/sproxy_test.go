package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
)

// newTestSProxy 创建一个指向 mockLLMURL 的 SProxy（使用内存 DB）。
func newTestSProxy(t *testing.T, mockLLMURL string) (*SProxy, *auth.Manager) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "test-secret-key")
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
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	// 不需要后台 goroutine，测试中不关心写入结果

	target := LLMTarget{URL: mockLLMURL, APIKey: "real-api-key"}
	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{target})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	return sp, jwtMgr
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareValidJWT
// ---------------------------------------------------------------------------

func TestAuthMiddlewareValidJWT(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	var gotClaims *auth.JWTClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(logger, jwtMgr, inner)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("claims should be set in context")
	}
	if gotClaims.UserID != "u1" {
		t.Errorf("UserID = %q, want %q", gotClaims.UserID, "u1")
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareNoHeader → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareNoHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareExpired → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareExpired(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	// 签发 1ms TTL，在测试执行前已过期
	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u2", Username: "bob"}, time.Millisecond)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // 等待过期

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareBlacklisted → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareBlacklisted(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u3", Username: "charlie"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Parse to get JTI, then blacklist it
	claims, err := jwtMgr.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	jwtMgr.Blacklist(claims.JTI, claims.ExpiresAt.Time)

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestHeaderReplacement
// ---------------------------------------------------------------------------

func TestHeaderReplacement(t *testing.T) {
	var capturedAuth string
	var capturedPairProxyAuth string

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPairProxyAuth = r.Header.Get("X-PairProxy-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message"}`)
	}))
	defer mockLLM.Close()

	sp, jwtMgr := newTestSProxy(t, mockLLM.URL)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body := strings.NewReader(`{"model":"claude-3","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Authorization", "Bearer dummy-key")

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if capturedAuth != "Bearer real-api-key" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer real-api-key")
	}
	if capturedPairProxyAuth != "" {
		t.Errorf("X-PairProxy-Auth should be removed from upstream request, got %q", capturedPairProxyAuth)
	}
}

// ---------------------------------------------------------------------------
// TestRecoveryMiddleware
// ---------------------------------------------------------------------------

func TestRecoveryMiddleware(t *testing.T) {
	logger := zaptest.NewLogger(t)

	handler := RecoveryMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestHealthHandler
// ---------------------------------------------------------------------------

func TestHealthHandler(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockLLM.Close()

	sp, _ := newTestSProxy(t, mockLLM.URL)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	sp.HealthHandler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}
