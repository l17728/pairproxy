package keygen_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l17728/pairproxy/internal/keygen"
)

// 测试用 HMAC 密钥（至少 32 字节）
var testSecret = []byte("test-secret-key-must-be-at-least-32-bytes-long!!")

// ---- GenerateKey 基本功能 ----

func TestGenerateKey_Format(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, keygen.KeyPrefix), "must start with sk-pp-")
	assert.Equal(t, keygen.KeyTotalLen, len(key), "total length must be 54")

	// 验证 body 只包含 Base62 字符
	body := key[len(keygen.KeyPrefix):]
	for _, c := range body {
		assert.True(t, strings.ContainsRune(keygen.Charset, c),
			"body must be Base62 charset, got %q", c)
	}
}

func TestGenerateKey_Deterministic(t *testing.T) {
	// 相同 username + secret → 相同 key（HMAC 确定性）
	key1, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)
	assert.Equal(t, key1, key2, "same username+secret must produce same key")
}

func TestGenerateKey_DifferentUsernames(t *testing.T) {
	// 不同 username → 不同 key
	key1, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("bob", testSecret)
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "different usernames must produce different keys")
}

func TestGenerateKey_DifferentSecrets(t *testing.T) {
	// 不同 secret → 不同 key
	secret2 := []byte("another-secret-key-must-be-at-least-32-bytes-long!!")
	key1, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("alice", secret2)
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "different secrets must produce different keys")
}

// ---- 碰撞测试（核心：修复旧算法的碰撞问题）----

func TestGenerateKey_NoCollision_SameCharSet(t *testing.T) {
	// 旧算法碰撞场景：alice123 和 321ecila 有相同字符集
	// 新算法必须生成不同 key
	key1, err := keygen.GenerateKey("alice123", testSecret)
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("321ecila", testSecret)
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "users with same char set must NOT collide with HMAC")
}

func TestGenerateKey_NoCollision_1000Users(t *testing.T) {
	// 1000 个不同用户名 → 1000 个唯一 key
	keys := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		username := fmt.Sprintf("user_%04d", i)
		key, err := keygen.GenerateKey(username, testSecret)
		require.NoError(t, err)
		assert.False(t, keys[key], "collision detected for %s", username)
		keys[key] = true
	}
	assert.Equal(t, 1000, len(keys), "all 1000 keys must be unique")
}

func TestGenerateKey_NoCollision_SimilarUsernames(t *testing.T) {
	// 相似用户名不碰撞
	similar := []string{
		"alice", "Alice", "ALICE", "aLiCe",
		"alice1", "alice2", "alice_", "alice.",
		"bob", "bobb", "bo", "b",
	}
	keys := make(map[string]bool, len(similar))
	for _, u := range similar {
		key, err := keygen.GenerateKey(u, testSecret)
		require.NoError(t, err)
		assert.False(t, keys[key], "collision for username %q", u)
		keys[key] = true
	}
}

// ---- 边界条件 ----

func TestGenerateKey_EmptyUsername(t *testing.T) {
	_, err := keygen.GenerateKey("", testSecret)
	assert.Error(t, err, "empty username must return error")
	assert.Contains(t, err.Error(), "username cannot be empty")
}

func TestGenerateKey_ShortSecret(t *testing.T) {
	_, err := keygen.GenerateKey("alice", []byte("short"))
	assert.Error(t, err, "short secret must return error")
	assert.Contains(t, err.Error(), "secret must be at least 32 bytes")
}

func TestGenerateKey_ExactlyMinSecret(t *testing.T) {
	// 恰好 32 字节的 secret 应该成功
	secret32 := []byte("12345678901234567890123456789012")
	assert.Equal(t, 32, len(secret32))
	key, err := keygen.GenerateKey("alice", secret32)
	require.NoError(t, err)
	assert.Equal(t, keygen.KeyTotalLen, len(key))
}

func TestGenerateKey_LongUsername(t *testing.T) {
	// 1000 字符的用户名应该正常工作
	longName := strings.Repeat("a", 1000)
	key, err := keygen.GenerateKey(longName, testSecret)
	require.NoError(t, err)
	assert.Equal(t, keygen.KeyTotalLen, len(key))
}

func TestGenerateKey_UnicodeUsername(t *testing.T) {
	// Unicode 用户名应该正常工作
	key, err := keygen.GenerateKey("用户名テスト", testSecret)
	require.NoError(t, err)
	assert.Equal(t, keygen.KeyTotalLen, len(key))
	assert.True(t, keygen.IsValidFormat(key))
}

func TestGenerateKey_SpecialChars(t *testing.T) {
	// 特殊字符用户名
	specials := []string{
		"user@domain.com",
		"user+tag",
		"user name",
		"user\ttab",
		"user\nnewline",
	}
	for _, u := range specials {
		key, err := keygen.GenerateKey(u, testSecret)
		require.NoError(t, err, "username=%q", u)
		assert.Equal(t, keygen.KeyTotalLen, len(key), "username=%q", u)
		assert.True(t, keygen.IsValidFormat(key), "username=%q", u)
	}
}

func TestGenerateKey_SingleCharUsername(t *testing.T) {
	// 单字符用户名（HMAC 无最小长度要求）
	key, err := keygen.GenerateKey("a", testSecret)
	require.NoError(t, err)
	assert.Equal(t, keygen.KeyTotalLen, len(key))
}
