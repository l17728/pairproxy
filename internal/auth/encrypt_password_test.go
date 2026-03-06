package auth

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Encrypt 边界情况（补充已有 encrypt_test.go 未覆盖的分支）
// ---------------------------------------------------------------------------

func TestEncrypt_UnicodeAndSpecialChars(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
	}{
		{"emoji", "密码测试 🔐🔑"},
		{"newlines", "line1\nline2\nline3"},
		{"tabs", "col1\tcol2\tcol3"},
		{"null bytes", "before\x00after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := Encrypt(tc.plaintext, "unicode-test-key")
			if err != nil {
				t.Fatalf("Encrypt(%q): %v", tc.name, err)
			}
			got, err := Decrypt(ct, "unicode-test-key")
			if err != nil {
				t.Fatalf("Decrypt(%q): %v", tc.name, err)
			}
			if got != tc.plaintext {
				t.Errorf("round-trip failed: got %q, want %q", got, tc.plaintext)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HashPassword — 额外分支：空密码日志路径
// ---------------------------------------------------------------------------

func TestHashPassword_EmptyPassword_LogsWarn(t *testing.T) {
	logger := testLogger(t)
	// 调用空密码路径（触发 Warn 日志 + 返回 error）
	hash, err := HashPassword(logger, "")
	if err == nil {
		t.Fatal("expected error for empty password")
	}
	if hash != "" {
		t.Error("hash should be empty for empty password")
	}
}

// ---------------------------------------------------------------------------
// VerifyPassword — 损坏 hash 的路径（触发 bcrypt 非正常错误）
// ---------------------------------------------------------------------------

func TestVerifyPassword_CorruptHash_LogsWarn(t *testing.T) {
	logger := testLogger(t)
	// "corrupt" 是非法 bcrypt hash，会触发 unexpected error 路径
	result := VerifyPassword(logger, "corrupt-hash-value", "anypassword")
	if result {
		t.Error("should return false for corrupt hash")
	}
}
