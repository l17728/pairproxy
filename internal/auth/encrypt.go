package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Encrypt 使用 AES-256-GCM 加密 plaintext，返回 base64 编码的密文。
// key 可为任意长度字符串，内部通过 SHA-256 规范化为 32 字节。
// 输出格式：base64(nonce[12] || ciphertext || tag[16])
func Encrypt(plaintext string, key string) (string, error) {
	aesKey := deriveKey(key)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal 将 ciphertext+tag 追加到 nonce 后
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 Encrypt 输出的 base64 密文，返回明文。
func Decrypt(ciphertext64 string, key string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	aesKey := deriveKey(key)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]

	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("GCM open (tamper detected?): %w", err)
	}
	return string(plain), nil
}

// deriveKey 将任意长度字符串规范化为 32 字节 AES 密钥（SHA-256）。
func deriveKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}
