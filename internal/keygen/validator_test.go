package keygen_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l17728/pairproxy/internal/keygen"
)

// testPasswordHash 模拟用户的 PasswordHash（>= 32 字节）。
// 在 per-user-password 设计下，ValidateAndGetUser 用每个用户自己的 PasswordHash 作 HMAC 密钥。
const testPasswordHash = "test-password-hash-must-be-at-least-32-bytes!!"

// ---- IsValidFormat ----

func TestIsValidFormat_Valid(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)
	assert.True(t, keygen.IsValidFormat(key))
}

func TestIsValidFormat_WrongPrefix(t *testing.T) {
	assert.False(t, keygen.IsValidFormat("sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
}

func TestIsValidFormat_TooShort(t *testing.T) {
	assert.False(t, keygen.IsValidFormat("sk-pp-short"))
}

func TestIsValidFormat_TooLong(t *testing.T) {
	assert.False(t, keygen.IsValidFormat("sk-pp-"+strings.Repeat("a", 49))) // 6+49=55 chars
}

func TestIsValidFormat_InvalidChars(t *testing.T) {
	assert.False(t, keygen.IsValidFormat("sk-pp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-aa"))
}

func TestIsValidFormat_EmptyString(t *testing.T) {
	assert.False(t, keygen.IsValidFormat(""))
}

func TestIsValidFormat_PrefixOnly(t *testing.T) {
	assert.False(t, keygen.IsValidFormat("sk-pp-"))
}

// ---- ValidateAndGetUser ----

func TestValidateAndGetUser_CorrectKey(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice", u.Username)
	assert.Equal(t, "u1", u.ID)
}

func TestValidateAndGetUser_WrongKey(t *testing.T) {
	// bob 的 key 不应匹配 alice
	key, err := keygen.GenerateKey("bob", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users)
	assert.NoError(t, err)
	assert.Nil(t, u, "bob's key should not match alice")
}

func TestValidateAndGetUser_WrongPassword(t *testing.T) {
	// 用 hash1 派生的 key，针对持有 hash2 的用户验证应失败（模拟密码已被改变）
	ph2 := "different-password-hash-must-be-at-least-32-bytes!!"
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash)) // 旧密码派生
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", PasswordHash: ph2, IsActive: true}} // 新密码
	u, err := keygen.ValidateAndGetUser(key, users)
	assert.NoError(t, err)
	assert.Nil(t, u, "key from old password must not match user with new password hash")
}

func TestValidateAndGetUser_InvalidFormat(t *testing.T) {
	users := []keygen.UserEntry{{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true}}
	u, err := keygen.ValidateAndGetUser("invalid-key", users)
	assert.NoError(t, err)
	assert.Nil(t, u, "invalid format key should fast-reject")
}

func TestValidateAndGetUser_InactiveUser(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: false},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	assert.Nil(t, u, "inactive user must not be returned")
}

func TestValidateAndGetUser_EmptyPasswordHash_Skipped(t *testing.T) {
	// LDAP 用户 PasswordHash 为空，应被跳过，无法持有 sk-pp- Key
	key, err := keygen.GenerateKey("ldap_user", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "ldap_user", PasswordHash: "", IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	assert.Nil(t, u, "user with empty PasswordHash (LDAP) must not match any key")
}

func TestValidateAndGetUser_MultipleUsers_FindsCorrect(t *testing.T) {
	key, err := keygen.GenerateKey("charlie", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true},
		{ID: "u2", Username: "bob", PasswordHash: testPasswordHash, IsActive: true},
		{ID: "u3", Username: "charlie", PasswordHash: testPasswordHash, IsActive: true},
		{ID: "u4", Username: "dave", PasswordHash: testPasswordHash, IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "charlie", u.Username)
	assert.Equal(t, "u3", u.ID)
}

func TestValidateAndGetUser_100Users(t *testing.T) {
	// 在 100 个用户中找到正确的用户
	users := make([]keygen.UserEntry, 100)
	for i := 0; i < 100; i++ {
		users[i] = keygen.UserEntry{
			ID:           fmt.Sprintf("u%d", i),
			Username:     fmt.Sprintf("user_%03d", i),
			PasswordHash: testPasswordHash,
			IsActive:     true,
		}
	}

	// 验证第 50 个用户的 key
	key, err := keygen.GenerateKey("user_050", []byte(testPasswordHash))
	require.NoError(t, err)

	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "user_050", u.Username)
}

func TestValidateAndGetUser_NoCollision_SameCharSet(t *testing.T) {
	// 旧算法碰撞场景：alice123 和 321ecila 有相同字符集
	// HMAC 算法下两者 key 不同，不会碰撞
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice123", PasswordHash: testPasswordHash, IsActive: true},
		{ID: "u2", Username: "321ecila", PasswordHash: testPasswordHash, IsActive: true},
	}

	key1, err := keygen.GenerateKey("alice123", []byte(testPasswordHash))
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("321ecila", []byte(testPasswordHash))
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "HMAC keys must differ for different usernames")

	// key1 只匹配 alice123
	u, err := keygen.ValidateAndGetUser(key1, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice123", u.Username)

	// key2 只匹配 321ecila
	u, err = keygen.ValidateAndGetUser(key2, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "321ecila", u.Username)
}

func TestValidateAndGetUser_EmptyUserList(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)

	u, err := keygen.ValidateAndGetUser(key, nil)
	assert.NoError(t, err)
	assert.Nil(t, u)

	u, err = keygen.ValidateAndGetUser(key, []keygen.UserEntry{})
	assert.NoError(t, err)
	assert.Nil(t, u)
}

func TestValidateAndGetUser_GroupIDPreserved(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)

	gid := "group1"
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true, GroupID: &gid},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	require.NotNil(t, u.GroupID)
	assert.Equal(t, "group1", *u.GroupID)
}

func TestValidateAndGetUser_Deterministic(t *testing.T) {
	// 多次验证同一 key 应返回相同结果
	key, err := keygen.GenerateKey("alice", []byte(testPasswordHash))
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true}}
	for i := 0; i < 10; i++ {
		u, err := keygen.ValidateAndGetUser(key, users)
		require.NoError(t, err)
		require.NotNil(t, u, "iteration %d", i)
		assert.Equal(t, "alice", u.Username, "iteration %d", i)
	}
}

func TestValidateAndGetUser_PerUserKey_DifferentPasswords(t *testing.T) {
	// 每个用户有独立的 PasswordHash，对应独立的 API Key
	ph_alice := "alice-password-hash-must-be-at-least-32-bytes!!!"
	ph_bob := "bob-password-hash-must-be-at-least-32-bytes!!!!"

	aliceKey, err := keygen.GenerateKey("alice", []byte(ph_alice))
	require.NoError(t, err)
	bobKey, err := keygen.GenerateKey("bob", []byte(ph_bob))
	require.NoError(t, err)

	assert.NotEqual(t, aliceKey, bobKey, "different users with different passwords must have different keys")

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: ph_alice, IsActive: true},
		{ID: "u2", Username: "bob", PasswordHash: ph_bob, IsActive: true},
	}

	// alice 的 key 只匹配 alice
	u, err := keygen.ValidateAndGetUser(aliceKey, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice", u.Username)

	// bob 的 key 只匹配 bob
	u, err = keygen.ValidateAndGetUser(bobKey, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "bob", u.Username)
}

// ---- ValidateWithLegacySecret ----

const legacySecret = "legacy-keygen-secret-must-be-at-least-32-bytes!!"

// TestValidateWithLegacySecret_Match 验证：旧版共享 secret 派生的 Key 可以通过兜底校验。
func TestValidateWithLegacySecret_Match(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(legacySecret))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true},
	}

	// per-user 校验：key 是旧 secret 派生的，PasswordHash 不同，应不匹配
	u, valErr := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, valErr)
	assert.Nil(t, u, "per-user 校验不应匹配旧版 key")

	// legacy 兜底：匹配
	matched := keygen.ValidateWithLegacySecret(key, users, []byte(legacySecret))
	require.NotNil(t, matched)
	assert.Equal(t, "alice", matched.Username)
}

// TestValidateWithLegacySecret_WrongSecret 验证：不同的 secret 不匹配。
func TestValidateWithLegacySecret_WrongSecret(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(legacySecret))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true},
	}

	wrongSecret := "wrong-secret-but-still-at-least-32-bytes-long!!!"
	matched := keygen.ValidateWithLegacySecret(key, users, []byte(wrongSecret))
	assert.Nil(t, matched)
}

// TestValidateWithLegacySecret_ShortSecret 验证：secret < 32 字节时直接返回 nil。
func TestValidateWithLegacySecret_ShortSecret(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(legacySecret))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true},
	}
	matched := keygen.ValidateWithLegacySecret(key, users, []byte("short"))
	assert.Nil(t, matched)
}

// TestValidateWithLegacySecret_InactiveUser 验证：非活跃用户被跳过。
func TestValidateWithLegacySecret_InactiveUser(t *testing.T) {
	key, err := keygen.GenerateKey("alice", []byte(legacySecret))
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: false},
	}
	matched := keygen.ValidateWithLegacySecret(key, users, []byte(legacySecret))
	assert.Nil(t, matched)
}
