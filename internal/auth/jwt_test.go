package auth

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func testLogger(t *testing.T) *zap.Logger {
	return zaptest.NewLogger(t)
}

func TestJWTSignAndParse(t *testing.T) {
	logger := testLogger(t)
	m, err := NewManager(logger, "test-secret-key")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	claims := JWTClaims{
		UserID:   "user-123",
		Username: "alice",
		GroupID:  "group-1",
		Role:     "user",
	}

	token, err := m.Sign(claims, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Fatal("Sign returned empty token")
	}

	parsed, err := m.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.UserID != claims.UserID {
		t.Errorf("UserID = %q, want %q", parsed.UserID, claims.UserID)
	}
	if parsed.Username != claims.Username {
		t.Errorf("Username = %q, want %q", parsed.Username, claims.Username)
	}
	if parsed.JTI == "" {
		t.Error("JTI should not be empty")
	}
}

func TestJWTUniqueJTI(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "secret")

	claims := JWTClaims{UserID: "u1"}
	t1, _ := m.Sign(claims, time.Hour)
	t2, _ := m.Sign(claims, time.Hour)

	p1, _ := m.Parse(t1)
	p2, _ := m.Parse(t2)

	if p1.JTI == p2.JTI {
		t.Error("two Sign calls produced the same JTI")
	}
}

func TestJWTExpired(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "secret")

	claims := JWTClaims{UserID: "u1"}
	token, err := m.Sign(claims, time.Millisecond)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	_, err = m.Parse(token)
	if err == nil {
		t.Fatal("Parse should return error for expired token")
	}
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("error should wrap ErrTokenExpired, got: %v", err)
	}
}

func TestJWTInvalidSignature(t *testing.T) {
	logger := testLogger(t)
	mA, _ := NewManager(logger, "secret-A")
	mB, _ := NewManager(logger, "secret-B")

	token, _ := mA.Sign(JWTClaims{UserID: "u1"}, time.Hour)

	_, err := mB.Parse(token)
	if err == nil {
		t.Fatal("Parse should fail with wrong secret")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("error should wrap ErrInvalidToken, got: %v", err)
	}
}

func TestJWTBlacklist(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "secret")

	claims := JWTClaims{UserID: "u1"}
	token, _ := m.Sign(claims, time.Hour)

	parsed, _ := m.Parse(token)
	jti := parsed.JTI

	// 加入黑名单
	m.Blacklist(jti, time.Now().Add(time.Hour))

	_, err := m.Parse(token)
	if err == nil {
		t.Fatal("Parse should fail for blacklisted token")
	}
	if !errors.Is(err, ErrTokenRevoked) {
		t.Errorf("error should wrap ErrTokenRevoked, got: %v", err)
	}
}

func TestBlacklistAutoCleanup(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	bl.Add("jti-1", time.Now().Add(50*time.Millisecond))
	if !bl.IsBlocked("jti-1") {
		t.Fatal("jti-1 should be blocked before expiry")
	}

	time.Sleep(100 * time.Millisecond)

	// IsBlocked 通过懒删除处理过期
	if bl.IsBlocked("jti-1") {
		t.Error("jti-1 should NOT be blocked after expiry")
	}
}

func TestNewManagerEmptySecret(t *testing.T) {
	logger := testLogger(t)
	_, err := NewManager(logger, "")
	if err == nil {
		t.Error("NewManager should fail with empty secret")
	}
}
