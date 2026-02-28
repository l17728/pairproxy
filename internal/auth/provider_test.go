package auth

import (
	"context"
	"testing"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// TestLocalProvider_Authenticate — 本地认证提供者测试
// ---------------------------------------------------------------------------

func TestLocalProvider_Authenticate_Success(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, _ := HashPassword(logger, "correct-pass")

	p := NewLocalProvider(logger, func(username string) (id, h string, found bool, err error) {
		if username == "alice" {
			return "user-id-alice", hash, true, nil
		}
		return "", "", false, nil
	})

	pu, err := p.Authenticate(context.Background(), "alice", "correct-pass")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if pu == nil {
		t.Fatal("expected ProviderUser, got nil")
	}
	if pu.ExternalID != "user-id-alice" {
		t.Errorf("ExternalID = %q, want %q", pu.ExternalID, "user-id-alice")
	}
	if pu.Username != "alice" {
		t.Errorf("Username = %q, want alice", pu.Username)
	}
	if pu.AuthProvider != "local" {
		t.Errorf("AuthProvider = %q, want local", pu.AuthProvider)
	}
}

func TestLocalProvider_Authenticate_WrongPassword(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, _ := HashPassword(logger, "correct-pass")

	p := NewLocalProvider(logger, func(username string) (id, h string, found bool, err error) {
		return "uid", hash, true, nil
	})

	_, err := p.Authenticate(context.Background(), "alice", "wrong-pass")
	if err == nil {
		t.Error("expected error for wrong password, got nil")
	}
}

func TestLocalProvider_Authenticate_UserNotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)

	p := NewLocalProvider(logger, func(username string) (id, h string, found bool, err error) {
		return "", "", false, nil
	})

	_, err := p.Authenticate(context.Background(), "nobody", "pass")
	if err == nil {
		t.Error("expected error for unknown user, got nil")
	}
}
