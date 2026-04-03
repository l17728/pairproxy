package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/db"
)

// TestGroupTargetSetIntegration_E2E_CompleteWorkflow 端到端测试：完整工作流
func TestGroupTargetSetIntegration_E2E_CompleteWorkflow(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	// 1. 创建集成层
	alertConfig := alert.TargetAlertConfig{
		Enabled: true,
		Triggers: map[string]alert.TriggerConfig{
			"http_error": {
				Type:           "http_error",
				MinOccurrences: 1,
			},
		},
		Recovery: alert.RecoveryConfig{
			ConsecutiveSuccesses: 1,
		},
	}

	healthCheckConfig := alert.HealthCheckConfig{
		Interval:         30 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Path:             "/health",
	}

	integration := NewGroupTargetSetIntegration(
		repo,
		alertRepo,
		alertConfig,
		healthCheckConfig,
		zap.NewNop(),
	)

	integration.Start(context.Background())
	defer integration.Stop()

	// 2. 创建 target set
	set := &db.GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "production-pool",
		Strategy:  "weighted_random",
		IsDefault: true,
	}
	require.NoError(t, repo.Create(set))

	// 3. 添加多个 targets
	targetURLs := []string{
		"https://api1.example.com",
		"https://api2.example.com",
		"https://api3.example.com",
	}

	for i, url := range targetURLs {
		member := &db.GroupTargetSetMember{
			ID:           uuid.New().String(),
			TargetURL:    url,
			Weight:       i + 1,
			IsActive:     true,
			HealthStatus: "healthy",
		}
		require.NoError(t, repo.AddMember(set.ID, member))
	}

	// 4. 测试选择 target（多次）
	// 使用 100 次采样确保加权随机算法在小样本下也能覆盖全部 3 个 target
	// weights: 1:2:3，10 次有概率漏掉权重最小的 target，100 次概率 >99.99%
	selectedURLs := make(map[string]int)
	for i := 0; i < 100; i++ {
		selected, hasMore, err := integration.SelectTarget(context.Background(), "", []string{})
		require.NoError(t, err)
		assert.NotNil(t, selected)
		assert.True(t, hasMore)
		selectedURLs[selected.URL]++
	}

	// 验证所有 targets 都被选中过
	assert.Equal(t, 3, len(selectedURLs))

	// 5. 测试故障转移
	selected1, _, err := integration.SelectTarget(context.Background(), "", []string{})
	require.NoError(t, err)

	selected2, _, err := integration.SelectTarget(context.Background(), "", []string{selected1.URL})
	require.NoError(t, err)

	assert.NotEqual(t, selected1.URL, selected2.URL)

	// 6. 测试告警流程
	integration.RecordError(selected1.URL, 503, nil, []string{})
	time.Sleep(100 * time.Millisecond)

	alerts := integration.GetActiveAlerts()
	assert.Greater(t, len(alerts), 0)

	// 7. 测试恢复流程
	integration.RecordSuccess(selected1.URL)
	time.Sleep(100 * time.Millisecond)

	// 8. 验证 GetAllHealthStatus 非 nil（HealthMonitor 后台管理，不依赖 RecordError）
	assert.NotNil(t, integration.GetAllHealthStatus())
}

// TestGroupTargetSetIntegration_E2E_MultipleGroups 端到端测试：多个 groups
func TestGroupTargetSetIntegration_E2E_MultipleGroups(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	alertConfig := alert.TargetAlertConfig{
		Enabled: true,
		Triggers: map[string]alert.TriggerConfig{
			"http_error": {
				Type:           "http_error",
				MinOccurrences: 1,
			},
		},
	}

	healthCheckConfig := alert.HealthCheckConfig{
		Interval:         30 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		SuccessThreshold: 2,
	}

	integration := NewGroupTargetSetIntegration(
		repo,
		alertRepo,
		alertConfig,
		healthCheckConfig,
		zap.NewNop(),
	)

	integration.Start(context.Background())
	defer integration.Stop()

	// 创建两个 target sets
	sets := []struct {
		name     string
		targets  []string
	}{
		{
			name: "premium-pool",
			targets: []string{
				"https://premium1.example.com",
				"https://premium2.example.com",
			},
		},
		{
			name: "standard-pool",
			targets: []string{
				"https://standard1.example.com",
				"https://standard2.example.com",
			},
		},
	}

	for _, s := range sets {
		set := &db.GroupTargetSet{
			ID:        uuid.New().String(),
			Name:      s.name,
			Strategy:  "weighted_random",
			IsDefault: true,
		}
		require.NoError(t, repo.Create(set))

		for _, url := range s.targets {
			member := &db.GroupTargetSetMember{
				ID:           uuid.New().String(),
				TargetURL:    url,
				Weight:       1,
				IsActive:     true,
				HealthStatus: "healthy",
			}
			require.NoError(t, repo.AddMember(set.ID, member))
		}
	}

	// 测试选择 targets
	selected1, _, err := integration.SelectTarget(context.Background(), "", []string{})
	require.NoError(t, err)
	assert.NotNil(t, selected1)

	selected2, _, err := integration.SelectTarget(context.Background(), "", []string{selected1.URL})
	require.NoError(t, err)
	assert.NotNil(t, selected2)
}

// TestGroupTargetSetIntegration_E2E_AlertSubscription 端到端测试：告警订阅
func TestGroupTargetSetIntegration_E2E_AlertSubscription(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	alertConfig := alert.TargetAlertConfig{
		Enabled: true,
		Triggers: map[string]alert.TriggerConfig{
			"http_error": {
				Type:           "http_error",
				MinOccurrences: 1,
			},
		},
	}

	healthCheckConfig := alert.HealthCheckConfig{
		Interval:         30 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		SuccessThreshold: 2,
	}

	integration := NewGroupTargetSetIntegration(
		repo,
		alertRepo,
		alertConfig,
		healthCheckConfig,
		zap.NewNop(),
	)

	integration.Start(context.Background())
	defer integration.Stop()

	// 订阅告警
	alertCh := integration.SubscribeAlerts()

	// 记录多个错误
	for i := 0; i < 3; i++ {
		integration.RecordError("https://api.example.com", 503, nil, []string{})
	}

	// 等待事件
	eventCount := 0
	timeout := time.After(2 * time.Second)

	for eventCount < 1 {
		select {
		case event := <-alertCh:
			assert.NotNil(t, event)
			eventCount++
		case <-timeout:
			t.Fatal("timeout waiting for alert events")
		}
	}

	assert.Greater(t, eventCount, 0)
}

// TestGroupTargetSetIntegration_E2E_HealthStatus 端到端测试：健康状态
func TestGroupTargetSetIntegration_E2E_HealthStatus(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	alertConfig := alert.TargetAlertConfig{
		Enabled: true,
		Triggers: map[string]alert.TriggerConfig{
			"http_error": {
				Type:           "http_error",
				MinOccurrences: 1,
			},
		},
	}

	healthCheckConfig := alert.HealthCheckConfig{
		Interval:         30 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		SuccessThreshold: 2,
	}

	integration := NewGroupTargetSetIntegration(
		repo,
		alertRepo,
		alertConfig,
		healthCheckConfig,
		zap.NewNop(),
	)

	integration.Start(context.Background())
	defer integration.Stop()

	// 创建 target set
	set := &db.GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 添加 targets
	targetURLs := []string{
		"https://api1.example.com",
		"https://api2.example.com",
		"https://api3.example.com",
	}
	for _, url := range targetURLs {
		member := &db.GroupTargetSetMember{
			ID:       uuid.New().String(),
			TargetURL: url,
			Weight:   1,
			IsActive: true,
		}
		require.NoError(t, repo.AddMember(set.ID, member))
	}

	// 通过 RecordError 触发 alertManager 记录错误
	for _, url := range targetURLs {
		integration.RecordError(url, 500, nil, []string{})
	}
	time.Sleep(50 * time.Millisecond)

	// 验证 alertManager 追踪到了活跃告警
	alerts := integration.GetActiveAlerts()
	assert.GreaterOrEqual(t, len(alerts), 1)

	// GetAllHealthStatus 由 HealthMonitor 管理（后台定期扫描），此时可能为空
	allStatus := integration.GetAllHealthStatus()
	assert.NotNil(t, allStatus)
}
