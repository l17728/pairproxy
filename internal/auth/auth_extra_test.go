package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// DefaultTokenDir — 覆盖分支
// ---------------------------------------------------------------------------

func TestDefaultTokenDir_NonEmpty(t *testing.T) {
	dir := DefaultTokenDir()
	if dir == "" {
		t.Fatal("DefaultTokenDir() should not return empty string")
	}
	if filepath.Base(dir) != "pairproxy" {
		t.Errorf("DefaultTokenDir() last segment = %q, want 'pairproxy'", filepath.Base(dir))
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Save — 写入无效路径触发错误日志（通过文件路径冲突模拟）
// ---------------------------------------------------------------------------

func TestTokenStore_Save_InvalidDir_FileConflict(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ts := &TokenStore{logger: logger}

	dir := t.TempDir()

	// 在预期是目录的路径上创建一个文件（导致 MkdirAll 失败）
	conflict := filepath.Join(dir, "conflict")
	if err := os.WriteFile(conflict, []byte("file"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// conflict/subdir 无法被创建（因为 conflict 是文件）
	tf := &TokenFile{
		ServerAddr:  "https://sproxy.test",
		AccessToken: "jwt-token",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	err := ts.Save(filepath.Join(conflict, "subdir"), tf)
	if err == nil {
		t.Error("Save to invalid path should return error")
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Load — JSON 解析失败
// ---------------------------------------------------------------------------

func TestTokenStore_Load_InvalidJSON(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ts := &TokenStore{logger: logger}

	dir := t.TempDir()
	// 写入无效 JSON
	path := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(path, []byte("not-json{"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tf, err := ts.Load(dir)
	if err == nil {
		t.Error("Load with invalid JSON should return error")
	}
	if tf != nil {
		t.Error("Load with invalid JSON should return nil TokenFile")
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Delete — 覆盖文件存在时成功删除
// ---------------------------------------------------------------------------

func TestTokenStore_Delete_FileExists(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ts := &TokenStore{logger: logger}

	dir := t.TempDir()
	// 创建 token 文件
	path := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(path, []byte(`{"server_addr":"test"}`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ts.Delete(dir); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 验证文件已删除
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("token file should be deleted")
	}
}

// ---------------------------------------------------------------------------
// Encrypt — 覆盖空密钥（空字符串作为 key）
// ---------------------------------------------------------------------------

func TestEncrypt_EmptyKey(t *testing.T) {
	enc, err := Encrypt("hello world", "")
	if err != nil {
		t.Fatalf("Encrypt with empty key: %v", err)
	}
	// 用同样的空 key 解密
	plain, err := Decrypt(enc, "")
	if err != nil {
		t.Fatalf("Decrypt with empty key: %v", err)
	}
	if plain != "hello world" {
		t.Errorf("Decrypt = %q, want 'hello world'", plain)
	}
}

// ---------------------------------------------------------------------------
// HashPassword — 覆盖 empty password 日志路径
// ---------------------------------------------------------------------------

func TestHashPassword_NonEmpty(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, err := HashPassword(logger, "my-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Error("HashPassword should return non-empty hash")
	}
}

// ---------------------------------------------------------------------------
// Manager.Sign — 覆盖错误 secret（空字符串）
// ---------------------------------------------------------------------------

func TestManager_Sign_ShortExpiry(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr, err := NewManager(logger, "test-sign-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// 1 纳秒过期
	_, err = mgr.Sign(JWTClaims{UserID: "u1", Username: "user1", Role: "user"}, time.Nanosecond)
	if err != nil {
		t.Fatalf("Sign with short expiry: %v", err)
	}
}
