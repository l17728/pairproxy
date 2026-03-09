package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

func TestUserHandler_QuotaStatus_AdminQueryOtherUser(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	handler := NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	// 创建测试用户
	targetUser := &db.User{Username: "targetuser", PasswordHash: "hash", IsActive: true}
	require.NoError(t, userRepo.Create(targetUser))

	// 创建用量记录
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	require.NoError(t, gormDB.Create(&db.UsageLog{
		RequestID:    "req_target",
		UserID:       targetUser.ID,
		InputTokens:  100,
		OutputTokens: 50,
		CreatedAt:    todayStart.Add(1 * time.Hour),
	}).Error)

	// 生成管理员 token
	adminToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	require.NoError(t, err)

	// 测试：管理员查询指定用户的配额状态
	req := httptest.NewRequest("GET", "/api/user/quota-status?username=targetuser", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()

	// 通过中间件调用
	handler.requireUser(http.HandlerFunc(handler.handleQuotaStatus)).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp userQuotaResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, int64(150), resp.DailyUsed, "should show target user's usage")
}

func TestUserHandler_QuotaStatus_RegularUserCannotQueryOthers(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	handler := NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	// 创建两个用户
	user1 := &db.User{Username: "user1", PasswordHash: "hash1", IsActive: true}
	user2 := &db.User{Username: "user2", PasswordHash: "hash2", IsActive: true}
	require.NoError(t, userRepo.Create(user1))
	require.NoError(t, userRepo.Create(user2))

	// 生成普通用户 token
	userToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   user1.ID,
		Username: "user1",
		Role:     "user",
	}, time.Hour)
	require.NoError(t, err)

	// 测试：普通用户尝试查询其他用户（应该被忽略，返回自己的数据）
	req := httptest.NewRequest("GET", "/api/user/quota-status?username=user2", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	w := httptest.NewRecorder()

	// 通过中间件调用
	handler.requireUser(http.HandlerFunc(handler.handleQuotaStatus)).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// 应该返回 user1 自己的数据，而不是 user2 的
}
