package auth

import (
	"strings"
	"testing"
)

// TestEncrypt_DecryptRoundtrip 验证加密后能正确解密。
func TestEncrypt_DecryptRoundtrip(t *testing.T) {
	key := "my-super-secret-key-for-testing"
	plaintext := "sk-ant-api03-secret-key-value"

	encrypted, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == plaintext {
		t.Error("encrypted text should differ from plaintext")
	}

	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

// TestEncrypt_NonDeterministic 验证每次加密结果不同（随机 nonce）。
func TestEncrypt_NonDeterministic(t *testing.T) {
	key := "test-key"
	plaintext := "same-value"

	enc1, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if enc1 == enc2 {
		t.Error("two encryptions of the same plaintext should differ (random nonce)")
	}
}

// TestEncrypt_WrongKey 验证用错误密钥解密会失败。
func TestEncrypt_WrongKey(t *testing.T) {
	plaintext := "sensitive-api-key"
	encrypted, err := Encrypt(plaintext, "correct-key")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = Decrypt(encrypted, "wrong-key")
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

// TestEncrypt_TamperDetection 验证篡改密文后解密失败。
func TestEncrypt_TamperDetection(t *testing.T) {
	key := "tamper-test-key"
	encrypted, err := Encrypt("value", key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// 修改 base64 字符串的最后一个字符（改变 ciphertext/tag）
	tampered := encrypted[:len(encrypted)-1] + "X"
	if tampered == encrypted {
		tampered = encrypted[:len(encrypted)-1] + "Y"
	}

	_, err = Decrypt(tampered, key)
	if err == nil {
		t.Error("expected error when decrypting tampered ciphertext")
	}
}

// TestEncrypt_EmptyPlaintext 验证空字符串可以加解密。
func TestEncrypt_EmptyPlaintext(t *testing.T) {
	key := "empty-test"
	enc, err := Encrypt("", key)
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	dec, err := Decrypt(enc, key)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if dec != "" {
		t.Errorf("expected empty string, got %q", dec)
	}
}

// TestEncrypt_LongKey 验证任意长度密钥均可（通过 SHA-256 规范化）。
func TestEncrypt_LongKey(t *testing.T) {
	key := strings.Repeat("long-key-", 100)
	plaintext := "test-value"
	enc, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	dec, err := Decrypt(enc, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if dec != plaintext {
		t.Errorf("got %q, want %q", dec, plaintext)
	}
}
