package keygen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l17728/pairproxy/internal/keygen"
)

// ---- IsValidFormat ----

func TestIsValidFormat_Valid(t *testing.T) {
	key, err := keygen.GenerateKey("alice")
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

// ---- ValidateAndGetUser ----

func TestValidateAndGetUser_Match(t *testing.T) {
	key, err := keygen.GenerateKey("alice")
	require.NoError(t, err)

	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice", u.Username)
}

func TestValidateAndGetUser_NoMatch(t *testing.T) {
	// 格式不合法的 key → 直接返回 (nil, nil)
	users := []keygen.UserEntry{{ID: "u1", Username: "alice", IsActive: true}}
	u, err := keygen.ValidateAndGetUser("invalid-key", users)
	assert.NoError(t, err)
	assert.Nil(t, u, "invalid format key should not match any user")
}

func TestValidateAndGetUser_InactiveSkipped(t *testing.T) {
	key, err := keygen.GenerateKey("alice")
	require.NoError(t, err)
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: false},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	assert.Nil(t, u, "inactive user must not be returned")
}

func TestValidateAndGetUser_LongestMatchWins(t *testing.T) {
	key, err := keygen.GenerateKey("alice")
	require.NoError(t, err)
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true},
		{ID: "u2", Username: "ali", IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "alice", u.Username, "longest match must win")
}

func TestValidateAndGetUser_RepeatedChars(t *testing.T) {
	key, err := keygen.GenerateKey("aaab")
	require.NoError(t, err)
	users := []keygen.UserEntry{
		{ID: "u1", Username: "aaab", IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, "aaab", u.Username)
}

func TestValidateAndGetUser_Collision(t *testing.T) {
	// "abcd" 和 "dcba" 的字母数字字符集完全相同（各 1 个 a/b/c/d）
	// 任何包含这 4 个字符的 key 都会同时匹配这两个用户名 → collision
	// 构造一个明确包含 a, b, c, d 的 key
	key := "sk-pp-abcdabcdabcdabcdabcdabcdabcdabcdabcdabcdabcdabcd" // 6+48=54 chars, body all lowercase abcd×12
	users := []keygen.UserEntry{
		{ID: "u1", Username: "abcd", IsActive: true},
		{ID: "u2", Username: "dcba", IsActive: true},
	}
	u, err := keygen.ValidateAndGetUser(key, users)
	assert.Error(t, err, "same fingerprint length should return collision error")
	assert.Contains(t, err.Error(), "collision")
	assert.Nil(t, u)
}

// ---- ValidateUsername ----

func TestValidateUsername_Valid(t *testing.T) {
	assert.NoError(t, keygen.ValidateUsername("alice"))
	assert.NoError(t, keygen.ValidateUsername("user123"))
	assert.NoError(t, keygen.ValidateUsername("ab12"))
}

func TestValidateUsername_TooShort(t *testing.T) {
	assert.Error(t, keygen.ValidateUsername("ab"))
	assert.Error(t, keygen.ValidateUsername("abc"))
}

func TestValidateUsername_TooFewUniqueChars(t *testing.T) {
	assert.Error(t, keygen.ValidateUsername("aaaa"))
	assert.Error(t, keygen.ValidateUsername("1111"))
	assert.Error(t, keygen.ValidateUsername("----"))
}

func TestValidateUsername_Valid_TwoUniqueChars(t *testing.T) {
	assert.NoError(t, keygen.ValidateUsername("aabb"))
}

// ---- ContainsAllCharsWithCount ----

func TestContainsAllCharsWithCount(t *testing.T) {
	cases := []struct {
		body   string
		chars  []byte
		expect bool
	}{
		{"alicexyz", []byte("alice"), true},
		{"abcd", []byte("alice"), false},
		{"aaabcd", []byte("aaab"), true},
		{"aabcd", []byte("aaab"), false},
	}
	for _, tc := range cases {
		result := keygen.ContainsAllCharsWithCount(tc.body, tc.chars)
		assert.Equal(t, tc.expect, result, "body=%q chars=%q", tc.body, tc.chars)
	}
}
