package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// TokenStore.DefaultTokenDir
// ---------------------------------------------------------------------------

func TestDefaultTokenDir_ContainsPairproxy(t *testing.T) {
	dir := DefaultTokenDir()
	if dir == "" {
		t.Fatal("DefaultTokenDir() should not return empty string")
	}
	if filepath.Base(dir) != "pairproxy" {
		t.Errorf("DefaultTokenDir() last segment should be 'pairproxy', got %q", filepath.Base(dir))
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Save + Load 更多边界情况
// ---------------------------------------------------------------------------

func TestTokenStore_Load_InvalidJSON_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 0)

	// 写入非法 JSON
	path := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(path, []byte("{invalid-json}"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := store.Load(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON token file")
	}
}

func TestTokenStore_Save_CreatesNestedDir(t *testing.T) {
	// 使用尚不存在的嵌套子目录
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "config", "pairproxy")
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 0)

	tf := &TokenFile{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := store.Save(dir, tf); err != nil {
		t.Fatalf("Save to non-existent nested dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, tokenFileName)); err != nil {
		t.Fatalf("token file should exist after Save to nested dir: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TokenStore.IsValid — nil 和空 token 覆盖
// ---------------------------------------------------------------------------

func TestTokenStore_IsValid_Nil_ReturnsFalse(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	if store.IsValid(nil) {
		t.Error("nil TokenFile should be invalid")
	}
}

func TestTokenStore_IsValid_EmptyAccessToken_ReturnsFalse(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tf := &TokenFile{AccessToken: "", ExpiresAt: time.Now().Add(2 * time.Hour)}
	if store.IsValid(tf) {
		t.Error("empty AccessToken should be invalid")
	}
}

func TestTokenStore_NeedsRefresh_NoRefreshToken_ReturnsFalse(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tf := &TokenFile{
		AccessToken:  "tok",
		RefreshToken: "", // 无刷新 token
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}
	if store.NeedsRefresh(tf) {
		t.Error("token without refresh_token should not need refresh")
	}
}

func TestTokenStore_NeedsRefresh_NilReturnsFalse(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	if store.NeedsRefresh(nil) {
		t.Error("nil TokenFile should not need refresh")
	}
}

// ---------------------------------------------------------------------------
// TokenStore 文件权限 (Unix-only)
// ---------------------------------------------------------------------------

func TestTokenStore_Save_FilePermission_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions not applicable on Windows")
	}

	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 0)

	tf := &TokenFile{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save(dir, tf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, tokenFileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permission = %04o, want 0600", perm)
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Delete — 目录不存在时不报错
// ---------------------------------------------------------------------------

func TestTokenStore_Delete_DirectoryNotExist_NoError(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 0)

	// 未保存过 token，删除应无错
	if err := store.Delete(dir); err != nil {
		t.Fatalf("Delete of non-existent token file should not error, got: %v", err)
	}
}
