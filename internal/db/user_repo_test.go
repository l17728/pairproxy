package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestUserRepo_GetActiveUsers(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	userRepo := NewUserRepo(gormDB, logger)

	// 创建测试用户
	user1 := &User{Username: "active1", PasswordHash: "hash1", IsActive: true}
	user2 := &User{Username: "active2", PasswordHash: "hash2", IsActive: true}
	user3 := &User{Username: "inactive", PasswordHash: "hash3", IsActive: true}
	require.NoError(t, userRepo.Create(user1))
	require.NoError(t, userRepo.Create(user2))
	require.NoError(t, userRepo.Create(user3))

	// 为 user1 和 user2 创建最近的用量记录
	now := time.Now()
	require.NoError(t, gormDB.Create(&UsageLog{
		RequestID:    "req1",
		UserID:       user1.ID,
		InputTokens:  100,
		OutputTokens: 50,
		CreatedAt:    now.Add(-1 * time.Hour),
	}).Error)
	require.NoError(t, gormDB.Create(&UsageLog{
		RequestID:    "req2",
		UserID:       user2.ID,
		InputTokens:  200,
		OutputTokens: 100,
		CreatedAt:    now.Add(-2 * time.Hour),
	}).Error)

	// user3 的用量记录是 40 天前（超出范围）
	require.NoError(t, gormDB.Create(&UsageLog{
		RequestID:    "req3",
		UserID:       user3.ID,
		InputTokens:  50,
		OutputTokens: 25,
		CreatedAt:    now.AddDate(0, 0, -40),
	}).Error)

	// 测试：获取最近 30 天活跃用户
	users, err := userRepo.GetActiveUsers(30)
	require.NoError(t, err)
	assert.Len(t, users, 2, "should return 2 active users")

	// 验证返回的用户
	usernames := make([]string, len(users))
	for i, u := range users {
		usernames[i] = u.Username
	}
	assert.Contains(t, usernames, "active1")
	assert.Contains(t, usernames, "active2")
	assert.NotContains(t, usernames, "inactive")

	// 测试：获取最近 60 天活跃用户（应包含 user3）
	users, err = userRepo.GetActiveUsers(60)
	require.NoError(t, err)
	assert.Len(t, users, 3, "should return 3 active users")
}

func TestUserRepo_GetActiveUsers_Empty(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	userRepo := NewUserRepo(gormDB, logger)

	// 测试：没有用户时返回空列表
	users, err := userRepo.GetActiveUsers(30)
	require.NoError(t, err)
	assert.Empty(t, users)
}

func TestUserRepo_ListActive(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gdb, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gdb))

	repo := NewUserRepo(gdb, logger)

	// 创建两个活跃用户和一个禁用用户
	u1 := &User{Username: "active1", PasswordHash: "h1", IsActive: true}
	u2 := &User{Username: "active2", PasswordHash: "h2", IsActive: true}
	u3 := &User{Username: "disabled3", PasswordHash: "h3"}
	require.NoError(t, repo.Create(u1))
	require.NoError(t, repo.Create(u2))
	require.NoError(t, repo.Create(u3))
	// IsActive 默认 true，手动禁用 u3
	require.NoError(t, repo.SetActive(u3.ID, false))

	users, err := repo.ListActive()
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, u := range users {
		names[u.Username] = true
		assert.True(t, u.IsActive, "ListActive should only return active users")
	}
	assert.True(t, names["active1"])
	assert.True(t, names["active2"])
	assert.False(t, names["disabled3"], "disabled user must not appear")
}
