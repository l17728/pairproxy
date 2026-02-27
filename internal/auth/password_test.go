package auth

import (
	"testing"

	"go.uber.org/zap/zaptest"
)

func TestPasswordRoundTrip(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, err := HashPassword(logger, "MySecretPass123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("hash should not be empty")
	}
	if !VerifyPassword(logger, hash, "MySecretPass123") {
		t.Error("VerifyPassword should return true for correct password")
	}
}

func TestPasswordWrongInput(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, _ := HashPassword(logger, "correct")
	if VerifyPassword(logger, hash, "wrong") {
		t.Error("VerifyPassword should return false for wrong password")
	}
}

func TestPasswordEmpty(t *testing.T) {
	logger := zaptest.NewLogger(t)
	_, err := HashPassword(logger, "")
	if err == nil {
		t.Error("HashPassword should fail for empty password")
	}
}

func TestPasswordHashUnique(t *testing.T) {
	logger := zaptest.NewLogger(t)
	h1, _ := HashPassword(logger, "same-password")
	h2, _ := HashPassword(logger, "same-password")
	if h1 == h2 {
		t.Error("two hashes of the same password should differ (bcrypt uses random salt)")
	}
}
