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

func TestAdminHandler_GetActiveUsers(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, "", time.Hour)

	// 创建测试用户
	user1 := &db.User{Username: "user1", PasswordHash: "hash1", IsActive: true}
	user2 := &db.User{Username: "user2", PasswordHash: "hash2", IsActive: true}
	require.NoError(t, userRepo.Create(user1))
	require.NoError(t, userRepo.Create(user2))

	// 创建用量记录
	now := time.Now()
	require.NoError(t, gormDB.Create(&db.UsageLog{
		RequestID:    "req1",
		UserID:       user1.ID,
		InputTokens:  100,
		OutputTokens: 50,
		CreatedAt:    now.Add(-1 * time.Hour),
	}).Error)
	require.NoError(t, gormDB.Create(&db.UsageLog{
		RequestID:    "req2",
		UserID:       user2.ID,
		InputTokens:  200,
		OutputTokens: 100,
		CreatedAt:    now.Add(-2 * time.Hour),
	}).Error)

	// 生成管理员 token
	adminToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	require.NoError(t, err)

	// 测试：获取活跃用户列表
	req := httptest.NewRequest("GET", "/api/admin/active-users?days=30", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()

	handler.handleGetActiveUsers(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp []activeUserResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Len(t, resp, 2)

	usernames := []string{resp[0].Username, resp[1].Username}
	assert.Contains(t, usernames, "user1")
	assert.Contains(t, usernames, "user2")
}

func TestAdminHandler_GetActiveUsers_CustomDays(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	handler := NewAdminHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, "", time.Hour)

	// 创建用户和旧的用量记录
	user := &db.User{Username: "olduser", PasswordHash: "hash", IsActive: true}
	require.NoError(t, userRepo.Create(user))

	now := time.Now()
	require.NoError(t, gormDB.Create(&db.UsageLog{
		RequestID:    "req_old",
		UserID:       user.ID,
		InputTokens:  100,
		OutputTokens: 50,
		CreatedAt:    now.AddDate(0, 0, -40), // 40 天前
	}).Error)

	adminToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	require.NoError(t, err)

	// 测试：查询最近 30 天（应该为空）
	req := httptest.NewRequest("GET", "/api/admin/active-users?days=30", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	handler.handleGetActiveUsers(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp []activeUserResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Empty(t, resp)

	// 测试：查询最近 60 天（应该有 1 个用户）
	req = httptest.NewRequest("GET", "/api/admin/active-users?days=60", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w = httptest.NewRecorder()
	handler.handleGetActiveUsers(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	json.NewDecoder(w.Body).Decode(&resp)
	assert.Len(t, resp, 1)
	assert.Equal(t, "olduser", resp[0].Username)
}
