package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
)

// TestRequestIDFromContext_ByMinMax tests retrieving request ID from context
func TestRequestIDFromContext_ByMinMax(t *testing.T) {
	// Test with empty context
	ctx := context.Background()
	reqID := RequestIDFromContext(ctx)
	if reqID != "" {
		t.Error("expected empty string for context without request ID")
	}

	// Test with request ID in context
	testID := uuid.New().String()
	ctx = context.WithValue(ctx, ctxKeyRequestID, testID)
	reqID = RequestIDFromContext(ctx)
	if reqID != testID {
		t.Errorf("expected %s, got %s", testID, reqID)
	}
}

// TestClaimsFromContext_ByMinMax tests retrieving JWT claims from context
func TestClaimsFromContext_ByMinMax(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create JWT manager and token
	mgr, err := auth.NewManager(logger, "test-secret-key-for-testing")
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	claims := auth.JWTClaims{
		UserID:   "user123",
		Username: "testuser",
		Role:     "user",
	}
	token, err := mgr.Sign(claims, time.Hour)
	if err != nil {
		t.Fatalf("failed to sign JWT: %v", err)
	}

	parsedClaims, err := mgr.Parse(token)
	if err != nil {
		t.Fatalf("failed to parse JWT: %v", err)
	}

	// Test with claims in context
	ctx := context.WithValue(context.Background(), ctxKeyClaims, parsedClaims)
	retrieved := ClaimsFromContext(ctx)
	if retrieved == nil {
		t.Fatal("expected claims, got nil")
	}
	if retrieved.UserID != "user123" {
		t.Errorf("expected user123, got %s", retrieved.UserID)
	}

	// Test with empty context
	emptyCtx := context.Background()
	retrieved = ClaimsFromContext(emptyCtx)
	if retrieved != nil {
		t.Error("expected nil for context without claims")
	}
}

// TestRequestIDMiddleware_ByMinMax tests the request ID middleware
func TestRequestIDMiddleware_ByMinMax(t *testing.T) {
	logger := zaptest.NewLogger(t)

	handler := RequestIDMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := RequestIDFromContext(r.Context())
		if reqID == "" {
			t.Error("expected request ID in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Test without X-Request-ID header - should generate new UUID
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Check that X-Request-ID header is set
	respReqID := w.Header().Get("X-Request-ID")
	if respReqID == "" {
		t.Error("expected X-Request-ID header in response")
	}

	// Test with X-Request-ID header - should use provided value
	customID := "custom-request-id-123"
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", customID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	respReqID = w.Header().Get("X-Request-ID")
	if respReqID != customID {
		t.Errorf("expected %s, got %s", customID, respReqID)
	}
}

// TestAuthMiddleware_ByMinMax tests authentication middleware
func TestAuthMiddleware_ByMinMax(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr, err := auth.NewManager(logger, "test-secret-key-for-testing")
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Test missing auth header
	handler := AuthMiddleware(logger, mgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// Test with valid JWT via X-PairProxy-Auth header
	claims := auth.JWTClaims{UserID: "user123", Username: "testuser", Role: "user"}
	token, _ := mgr.Sign(claims, time.Hour)
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Test with valid JWT via Authorization Bearer header
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Test priority - X-PairProxy-Auth should take precedence
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (X-PairProxy-Auth should take precedence), got %d", w.Code)
	}

	// Test with invalid token
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-PairProxy-Auth", "invalid-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", w.Code)
	}
}

// TestRecoveryMiddleware_ByMinMax tests the panic recovery middleware
func TestRecoveryMiddleware_ByMinMax(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Test normal handler - no panic
	handler := RecoveryMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Test handler that panics
	handler = RecoveryMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))

	req = httptest.NewRequest("GET", "/test", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 after panic, got %d", w.Code)
	}
}

// TestWriteJSONError_ByMinMax tests the error response writer
func TestWriteJSONError_ByMinMax(t *testing.T) {
	testCases := []struct {
		status int
		code   string
		msg    string
	}{
		{400, "bad_request", "invalid input"},
		{401, "unauthorized", "authentication required"},
		{403, "forbidden", "access denied"},
		{404, "not_found", "resource not found"},
		{500, "internal_error", "server error"},
		{429, "rate_limit", "too many requests"},
	}

	for _, tc := range testCases {
		w := httptest.NewRecorder()
		writeJSONError(w, tc.status, tc.code, tc.msg)

		if w.Code != tc.status {
			t.Errorf("expected status %d, got %d", tc.status, w.Code)
		}

		contentType := w.Header().Get("Content-Type")
		if contentType != "application/json" {
			t.Errorf("expected application/json, got %s", contentType)
		}
	}
}
