package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestTokenStore_SaveAndLoad 测试保存和加载 token
func TestTokenStore_SaveAndLoad(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tmpDir := t.TempDir()

	tf := &TokenFile{
		AccessToken:  "access-token-123",
		RefreshToken: "refresh-token-456",
		ExpiresAt:    time.Now().Add(time.Hour),
		ServerAddr:   "http://localhost:9000",
		Username:     "testuser",
	}

	// 保存
	if err := store.Save(tmpDir, tf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 验证文件存在
	path := filepath.Join(tmpDir, tokenFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("token file should exist")
	}

	// 加载
	loaded, err := store.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded token should not be nil")
	}

	// 验证字段
	if loaded.AccessToken != tf.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, tf.AccessToken)
	}
	if loaded.RefreshToken != tf.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, tf.RefreshToken)
	}
	if loaded.ServerAddr != tf.ServerAddr {
		t.Errorf("ServerAddr = %q, want %q", loaded.ServerAddr, tf.ServerAddr)
	}
	if loaded.Username != tf.Username {
		t.Errorf("Username = %q, want %q", loaded.Username, tf.Username)
	}
}

// TestTokenStore_Load_NotFound 测试加载不存在的文件
func TestTokenStore_Load_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tmpDir := t.TempDir()

	loaded, err := store.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load should not error for missing file: %v", err)
	}
	if loaded != nil {
		t.Error("loaded should be nil for missing file")
	}
}

// TestTokenStore_Delete 测试删除 token 文件
func TestTokenStore_Delete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tmpDir := t.TempDir()

	// 先保存
	tf := &TokenFile{
		AccessToken:  "token-to-delete",
		RefreshToken: "refresh-to-delete",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	store.Save(tmpDir, tf)

	// 删除
	if err := store.Delete(tmpDir); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 验证文件已删除
	loaded, _ := store.Load(tmpDir)
	if loaded != nil {
		t.Error("loaded should be nil after delete")
	}
}

// TestTokenStore_Delete_NotExist 测试删除不存在的文件
func TestTokenStore_Delete_NotExist(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tmpDir := t.TempDir()

	// 删除不存在的文件应该不报错
	if err := store.Delete(tmpDir); err != nil {
		t.Fatalf("Delete of non-existent file should not error: %v", err)
	}
}

// TestTokenStore_IsValid 测试 token 有效性检查
func TestTokenStore_IsValid(t *testing.T) {
	logger := zaptest.NewLogger(t)
	threshold := 30 * time.Minute
	store := NewTokenStore(logger, threshold)

	tests := []struct {
		name        string
		tf          *TokenFile
		expectValid bool
	}{
		{
			name:        "nil token",
			tf:          nil,
			expectValid: false,
		},
		{
			name: "empty access token",
			tf: &TokenFile{
				AccessToken: "",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
			expectValid: false,
		},
		{
			name: "valid token (far from expiry)",
			tf: &TokenFile{
				AccessToken: "valid-token",
				ExpiresAt:   time.Now().Add(2 * time.Hour),
			},
			expectValid: true,
		},
		{
			name: "token near threshold",
			tf: &TokenFile{
				AccessToken: "near-threshold",
				ExpiresAt:   time.Now().Add(15 * time.Minute), // 小于 30min 阈值
			},
			expectValid: false,
		},
		{
			name: "expired token",
			tf: &TokenFile{
				AccessToken: "expired-token",
				ExpiresAt:   time.Now().Add(-time.Hour),
			},
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid := store.IsValid(tt.tf)
			if valid != tt.expectValid {
				t.Errorf("IsValid = %v, want %v", valid, tt.expectValid)
			}
		})
	}
}

// TestTokenStore_NeedsRefresh 测试是否需要刷新
func TestTokenStore_NeedsRefresh(t *testing.T) {
	logger := zaptest.NewLogger(t)
	threshold := 30 * time.Minute
	store := NewTokenStore(logger, threshold)

	tests := []struct {
		name        string
		tf          *TokenFile
		expectNeeds bool
	}{
		{
			name:        "nil token",
			tf:          nil,
			expectNeeds: false,
		},
		{
			name: "empty tokens",
			tf: &TokenFile{
				AccessToken:  "",
				RefreshToken: "",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
			expectNeeds: false,
		},
		{
			name: "no refresh token",
			tf: &TokenFile{
				AccessToken:  "access",
				RefreshToken: "",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
			expectNeeds: false,
		},
		{
			name: "far from expiry",
			tf: &TokenFile{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(2 * time.Hour),
			},
			expectNeeds: false,
		},
		{
			name: "within threshold",
			tf: &TokenFile{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(15 * time.Minute),
			},
			expectNeeds: true,
		},
		{
			name: "already expired",
			tf: &TokenFile{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(-time.Hour),
			},
			expectNeeds: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			needs := store.NeedsRefresh(tt.tf)
			if needs != tt.expectNeeds {
				t.Errorf("NeedsRefresh = %v, want %v", needs, tt.expectNeeds)
			}
		})
	}
}

// TestTokenStore_CustomThreshold 测试自定义刷新阈值
func TestTokenStore_CustomThreshold(t *testing.T) {
	logger := zaptest.NewLogger(t)
	customThreshold := time.Hour // 使用 1 小时阈值
	store := NewTokenStore(logger, customThreshold)

	// 45 分钟后过期的 token，在 30 分钟阈值下是有效的
	// 但在 1 小时阈值下需要刷新
	tf := &TokenFile{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(45 * time.Minute),
	}

	// 在 1 小时阈值下应该无效
	if store.IsValid(tf) {
		t.Error("token should be invalid with 1-hour threshold")
	}

	// 应该需要刷新
	if !store.NeedsRefresh(tf) {
		t.Error("token should need refresh with 1-hour threshold")
	}
}

// TestTokenStore_DefaultThreshold 测试默认刷新阈值
func TestTokenStore_DefaultThreshold(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 0) // 传入 0 应使用默认值

	// 验证默认阈值为 30 分钟
	tf := &TokenFile{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(45 * time.Minute), // 45 分钟后过期
	}

	// 45 分钟 - 30 分钟 = 15 分钟 > 0，应该有效
	if !store.IsValid(tf) {
		t.Error("token should be valid with default 30-min threshold")
	}
}

// TestDefaultTokenDir 测试默认 token 目录
func TestDefaultTokenDir(t *testing.T) {
	dir := DefaultTokenDir()
	if dir == "" {
		t.Error("DefaultTokenDir should not return empty string")
	}
	// 应该包含 "pairproxy"
	if !filepath.IsAbs(dir) {
		t.Errorf("DefaultTokenDir should return absolute path, got %q", dir)
	}
}

// TestTokenStore_SaveCreatesDirectory 测试保存时自动创建目录
func TestTokenStore_SaveCreatesDirectory(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)

	tmpDir := t.TempDir()
	// 创建一个不存在的子目录
	subDir := filepath.Join(tmpDir, "subdir", "nested")

	tf := &TokenFile{
		AccessToken:  "test",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	// 保存应该自动创建目录
	if err := store.Save(subDir, tf); err != nil {
		t.Fatalf("Save should create directory: %v", err)
	}

	// 验证文件已创建
	if _, err := os.Stat(filepath.Join(subDir, tokenFileName)); os.IsNotExist(err) {
		t.Error("token file should be created in nested directory")
	}
}

// TestEncryptDecrypt_RoundTrip 测试加密解密往返
func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	plaintext := "my-secret-api-key-12345"
	key := "encryption-key"

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == "" {
		t.Error("ciphertext should not be empty")
	}
	if ciphertext == plaintext {
		t.Error("ciphertext should not equal plaintext")
	}

	decrypted, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

// TestEncrypt_DifferentOutputsSameInput 测试相同输入产生不同输出（随机 nonce）
func TestEncrypt_DifferentOutputsSameInput(t *testing.T) {
	plaintext := "same-input"
	key := "same-key"

	c1, _ := Encrypt(plaintext, key)
	c2, _ := Encrypt(plaintext, key)

	if c1 == c2 {
		t.Error("two encryptions of same input should differ (random nonce)")
	}

	// 但两者都应该能解密
	d1, _ := Decrypt(c1, key)
	d2, _ := Decrypt(c2, key)
	if d1 != plaintext || d2 != plaintext {
		t.Error("both should decrypt to same plaintext")
	}
}

// TestDecrypt_WrongKey 测试错误密钥解密
func TestDecrypt_WrongKey(t *testing.T) {
	plaintext := "secret-data"
	key1 := "correct-key"
	key2 := "wrong-key"

	ciphertext, _ := Encrypt(plaintext, key1)

	_, err := Decrypt(ciphertext, key2)
	if err == nil {
		t.Error("Decrypt with wrong key should fail")
	}
}

// TestDecrypt_InvalidBase64 测试无效 base64 输入
func TestDecrypt_InvalidBase64(t *testing.T) {
	_, err := Decrypt("not-valid-base64!!!", "key")
	if err == nil {
		t.Error("Decrypt with invalid base64 should fail")
	}
}

// TestDecrypt_TooShort 测试太短的密文
func TestDecrypt_TooShort(t *testing.T) {
	// base64 编码的太短数据
	shortCiphertext := "YWJj" // "abc" 的 base64

	_, err := Decrypt(shortCiphertext, "key")
	if err == nil {
		t.Error("Decrypt with too short ciphertext should fail")
	}
}

// TestEncrypt_EmptyPlaintextExt 测试加密空字符串
func TestEncrypt_EmptyPlaintextExt(t *testing.T) {
	ciphertext, err := Encrypt("", "key")
	if err != nil {
		t.Fatalf("Encrypt empty string should succeed: %v", err)
	}

	decrypted, err := Decrypt(ciphertext, "key")
	if err != nil {
		t.Fatalf("Decrypt should succeed: %v", err)
	}
	if decrypted != "" {
		t.Errorf("decrypted = %q, want empty string", decrypted)
	}
}

// TestEncrypt_DifferentKeyLengths 测试不同长度的密钥
func TestEncrypt_DifferentKeyLengths(t *testing.T) {
	keys := []string{
		"a",
		"short",
		"exactly-32-bytes-key-here-12345",
		"this-is-a-very-long-key-that-is-more-than-32-bytes",
	}

	plaintext := "test-data"

	for _, key := range keys {
		t.Run("key_len_"+string(rune(len(key))), func(t *testing.T) {
			ciphertext, err := Encrypt(plaintext, key)
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}

			decrypted, err := Decrypt(ciphertext, key)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}
			if decrypted != plaintext {
				t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
			}
		})
	}
}

// TestDeriveKey_Consistency 测试密钥派生一致性
func TestDeriveKey_Consistency(t *testing.T) {
	key := "test-key"

	k1 := deriveKey(key)
	k2 := deriveKey(key)

	// 相同输入应产生相同输出
	if string(k1) != string(k2) {
		t.Error("deriveKey should be deterministic")
	}

	// 输出应为 32 字节
	if len(k1) != 32 {
		t.Errorf("derived key length = %d, want 32", len(k1))
	}
}

// TestEncrypt_SpecialCharacters 测试特殊字符
func TestEncrypt_SpecialCharacters(t *testing.T) {
	testCases := []string{
		"hello\nworld",
		"tab\there",
		"unicode: 你好世界",
		"emoji: 🔐",
		"json: {\"key\": \"value\"}",
	}

	key := "test-key"

	for _, tc := range testCases {
		t.Run("special_chars", func(t *testing.T) {
			ciphertext, err := Encrypt(tc, key)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}

			decrypted, err := Decrypt(ciphertext, key)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}

			if decrypted != tc {
				t.Errorf("decrypted = %q, want %q", decrypted, tc)
			}
		})
	}
}
