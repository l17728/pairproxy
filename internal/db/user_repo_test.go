package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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
