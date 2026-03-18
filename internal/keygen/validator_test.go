package keygen_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l17728/pairproxy/internal/keygen"
)

// ---- IsValidFormat ----

func TestIsValidFormat_Valid(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
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

// ---- ValidateAndGetUser (HMAC) ----

func TestValidateAndGetUser_CorrectKey(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice", u.Username)
	assert.Equal(t, "u1", u.ID)
}

func TestValidateAndGetUser_WrongKey(t *testing.T) {
	// bob 的 key 不应匹配 alice
	key, err := keygen.GenerateKey("bob", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
	assert.NoError(t, err)
	assert.Nil(t, u, "bob's key should not match alice")
}

func TestValidateAndGetUser_WrongSecret(t *testing.T) {
	// 用 secret1 生成的 key，用 secret2 验证应失败
	secret2 := []byte("different-secret-key-must-be-at-least-32-bytes!!")
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users, secret2)
	assert.NoError(t, err)
	assert.Nil(t, u, "key generated with different secret must not match")
}

func TestValidateAndGetUser_InvalidFormat(t *testing.T) {
	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser("invalid-key", users, testSecret)
	assert.NoError(t, err)
	assert.Nil(t, u, "invalid format key should fast-reject")
}

func TestValidateAndGetUser_InactiveUser(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: false},
	}
	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
	require.NoError(t, err)
	assert.Nil(t, u, "inactive user must not be returned")
}

func TestValidateAndGetUser_MultipleUsers_FindsCorrect(t *testing.T) {
	key, err := keygen.GenerateKey("charlie", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true},
		{ID: "u2", Username: "bob", IsActive: true},
		{ID: "u3", Username: "charlie", IsActive: true},
		{ID: "u4", Username: "dave", IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
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
			ID:       fmt.Sprintf("u%d", i),
			Username: fmt.Sprintf("user_%03d", i),
			IsActive: true,
		}
	}

	// 验证第 50 个用户的 key
	key, err := keygen.GenerateKey("user_050", testSecret)
	require.NoError(t, err)

	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "user_050", u.Username)
}

func TestValidateAndGetUser_NoCollision_SameCharSet(t *testing.T) {
	// 旧算法碰撞场景：alice123 和 321ecila 有相同字符集
	// HMAC 算法下两者 key 不同，不会碰撞
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice123", IsActive: true},
		{ID: "u2", Username: "321ecila", IsActive: true},
	}

	key1, err := keygen.GenerateKey("alice123", testSecret)
	require.NoError(t, err)
	key2, err := keygen.GenerateKey("321ecila", testSecret)
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "HMAC keys must differ for different usernames")

	// key1 只匹配 alice123
	u, err := keygen.ValidateAndGetUser(key1, users, testSecret)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice123", u.Username)

	// key2 只匹配 321ecila
	u, err = keygen.ValidateAndGetUser(key2, users, testSecret)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "321ecila", u.Username)
}

func TestValidateAndGetUser_EmptyUserList(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	u, err := keygen.ValidateAndGetUser(key, nil, testSecret)
	assert.NoError(t, err)
	assert.Nil(t, u)

	u, err = keygen.ValidateAndGetUser(key, []keygen.UserEntry{}, testSecret)
	assert.NoError(t, err)
	assert.Nil(t, u)
}

func TestValidateAndGetUser_GroupIDPreserved(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	gid := "group1"
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true, GroupID: &gid},
	}
	u, err := keygen.ValidateAndGetUser(key, users, testSecret)
	require.NoError(t, err)
	require.NotNil(t, u)
	require.NotNil(t, u.GroupID)
	assert.Equal(t, "group1", *u.GroupID)
}

func TestValidateAndGetUser_Deterministic(t *testing.T) {
	// 多次验证同一 key 应返回相同结果
	key, err := keygen.GenerateKey("alice", testSecret)
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	for i := 0; i < 10; i++ {
		u, err := keygen.ValidateAndGetUser(key, users, testSecret)
		require.NoError(t, err)
		require.NotNil(t, u, "iteration %d", i)
		assert.Equal(t, "alice", u.Username, "iteration %d", i)
	}
}
