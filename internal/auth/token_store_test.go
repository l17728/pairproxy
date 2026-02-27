package auth

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func TestTokenStoreRoundTrip(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	tf := &TokenFile{
		AccessToken:  "at-test-1",
		RefreshToken: "rt-test-1",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		ServerAddr:   "http://sp-1:9000",
	}

	if err := store.Save(dir, tf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.AccessToken != tf.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, tf.AccessToken)
	}
	if loaded.RefreshToken != tf.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, tf.RefreshToken)
	}
	if loaded.ServerAddr != tf.ServerAddr {
		t.Errorf("ServerAddr = %q, want %q", loaded.ServerAddr, tf.ServerAddr)
	}
}

func TestTokenStoreLoadNotExist(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load should not return error when file not found, got: %v", err)
	}
	if loaded != nil {
		t.Error("Load should return nil when file not found")
	}
}

func TestTokenIsValid(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tf := &TokenFile{
		AccessToken: "at-1",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
	}
	if !store.IsValid(tf) {
		t.Error("IsValid should return true for token expiring in 2h")
	}
}

func TestTokenNearExpiry(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	// 20 分钟后过期，< 30min 阈值，应视为无效（需刷新）
	tf := &TokenFile{
		AccessToken: "at-1",
		ExpiresAt:   time.Now().Add(20 * time.Minute),
	}
	if store.IsValid(tf) {
		t.Error("IsValid should return false for token expiring within threshold")
	}
}

func TestTokenNil(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	if store.IsValid(nil) {
		t.Error("IsValid should return false for nil")
	}
}

func TestTokenDelete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	tf := &TokenFile{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour), ServerAddr: "http://sp:9000"}
	_ = store.Save(dir, tf)

	if err := store.Delete(dir); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	loaded, _ := store.Load(dir)
	if loaded != nil {
		t.Error("after Delete, Load should return nil")
	}
}
