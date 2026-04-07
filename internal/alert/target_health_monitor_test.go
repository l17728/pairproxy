package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// TestTargetHealthMonitor_StartStop 测试启动和停止
func TestTargetHealthMonitor_StartStop(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())
	alertManager := NewTargetAlertManager(alertRepo, TargetAlertConfig{Enabled: false}, zap.NewNop())
	alertManager.Start(context.Background())
	defer alertManager.Stop()

	monitor := NewTargetHealthMonitor(repo, alertManager, HealthCheckConfig{
		Interval:         100 * time.Millisecond,
		Timeout:          1 * time.Second,
		FailureThreshold: 2,
		SuccessThreshold: 1,
	}, zap.NewNop())

	monitor.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	monitor.Stop() // 确保 Stop 不会阻塞
}

// TestTargetHealthMonitor_GetStatus_NotFound 测试获取不存在的 target 状态
func TestTargetHealthMonitor_GetStatus_NotFound(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())
	alertManager := NewTargetAlertManager(alertRepo, TargetAlertConfig{Enabled: false}, zap.NewNop())
	alertManager.Start(context.Background())
	defer alertManager.Stop()

	monitor := NewTargetHealthMonitor(repo, alertManager, HealthCheckConfig{}, zap.NewNop())

	// 未追踪的 target 返回 nil
	status := monitor.GetStatus("https://unknown.example.com")
	assert.Nil(t, status)

	// GetAllStatus 返回空 map
	allStatus := monitor.GetAllStatus()
	assert.NotNil(t, allStatus)
	assert.Len(t, allStatus, 0)
}

// TestTargetHealthMonitor_HealthyTarget 测试健康 target 的检查
func TestTargetHealthMonitor_HealthyTarget(t *testing.T) {
	// 启动一个返回 200 的测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	llmTargetRepo := db.NewLLMTargetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	// 创建 LLMTarget 记录（TargetID → URL 映射）
	targetID := uuid.New().String()
	require.NoError(t, llmTargetRepo.Create(&db.LLMTarget{
		ID: targetID, URL: server.URL, Provider: "anthropic", Source: "database",
	}))

	// 创建 target set 并添加 member（使用 TargetID）
	set := &db.GroupTargetSet{
		ID: uuid.New().String(), Name: "test", Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))
	require.NoError(t, repo.AddMember(set.ID, &db.GroupTargetSetMember{
		ID: uuid.New().String(), TargetID: targetID,
		Weight: 1, IsActive: true,
	}))

	alertManager := NewTargetAlertManager(alertRepo, TargetAlertConfig{Enabled: false}, zap.NewNop())
	alertManager.Start(context.Background())
	defer alertManager.Stop()

	monitor := NewTargetHealthMonitor(repo, alertManager, HealthCheckConfig{
		Interval:         500 * time.Millisecond,
		Timeout:          2 * time.Second,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Path:             "/",
	}, zap.NewNop(), WithLLMTargetRepo(llmTargetRepo))

	monitor.Start(context.Background())
	time.Sleep(200 * time.Millisecond) // 等待首次检查完成
	monitor.Stop()

	// 验证 target 已被追踪
	status := monitor.GetStatus(server.URL)
	require.NotNil(t, status)
	assert.Equal(t, server.URL, status.URL)
	assert.True(t, status.Healthy)
}

// TestTargetHealthMonitor_UnhealthyTarget 测试不健康 target 的检查
func TestTargetHealthMonitor_UnhealthyTarget(t *testing.T) {
	// 启动一个返回 500 的测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	llmTargetRepo := db.NewLLMTargetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	targetID := uuid.New().String()
	require.NoError(t, llmTargetRepo.Create(&db.LLMTarget{
		ID: targetID, URL: server.URL, Provider: "anthropic", Source: "database",
	}))

	set := &db.GroupTargetSet{
		ID: uuid.New().String(), Name: "test", Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))
	require.NoError(t, repo.AddMember(set.ID, &db.GroupTargetSetMember{
		ID: uuid.New().String(), TargetID: targetID,
		Weight: 1, IsActive: true,
	}))

	alertManager := NewTargetAlertManager(alertRepo, TargetAlertConfig{Enabled: false}, zap.NewNop())
	alertManager.Start(context.Background())
	defer alertManager.Stop()

	monitor := NewTargetHealthMonitor(repo, alertManager, HealthCheckConfig{
		Interval:         500 * time.Millisecond,
		Timeout:          2 * time.Second,
		FailureThreshold: 1, // 1次失败即标记不健康
		SuccessThreshold: 1,
		Path:             "/",
	}, zap.NewNop(), WithLLMTargetRepo(llmTargetRepo))

	monitor.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	monitor.Stop()

	status := monitor.GetStatus(server.URL)
	require.NotNil(t, status)
	assert.False(t, status.Healthy)
	assert.Greater(t, status.ConsecutiveFailures, 0)
}

// TestTargetHealthMonitor_GetAllStatus 测试获取所有状态
func TestTargetHealthMonitor_GetAllStatus(t *testing.T) {
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer servers[i].Close()
	}

	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	llmTargetRepo := db.NewLLMTargetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	set := &db.GroupTargetSet{
		ID: uuid.New().String(), Name: "test", Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))
	for _, s := range servers {
		targetID := uuid.New().String()
		require.NoError(t, llmTargetRepo.Create(&db.LLMTarget{
			ID: targetID, URL: s.URL, Provider: "anthropic", Source: "database",
		}))
		require.NoError(t, repo.AddMember(set.ID, &db.GroupTargetSetMember{
			ID: uuid.New().String(), TargetID: targetID,
			Weight: 1, IsActive: true,
		}))
	}

	alertManager := NewTargetAlertManager(alertRepo, TargetAlertConfig{Enabled: false}, zap.NewNop())
	alertManager.Start(context.Background())
	defer alertManager.Stop()

	monitor := NewTargetHealthMonitor(repo, alertManager, HealthCheckConfig{
		Interval: 500 * time.Millisecond,
		Timeout:  2 * time.Second,
		Path:     "/",
	}, zap.NewNop(), WithLLMTargetRepo(llmTargetRepo))

	monitor.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	monitor.Stop()

	allStatus := monitor.GetAllStatus()
	assert.Len(t, allStatus, 3)
	for _, s := range servers {
		status, ok := allStatus[s.URL]
		assert.True(t, ok)
		assert.True(t, status.Healthy)
	}
}
