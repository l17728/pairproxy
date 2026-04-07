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

// TestGroupTargetSetIntegration_CompleteFlow 测试完整的 Group Target Set 流程
func TestGroupTargetSetIntegration_CompleteFlow(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	alertRepo := db.NewTargetAlertRepo(testDB, zap.NewNop())

	// 创建集成层
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

	// 创建 target set
	set := &db.GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "test-set",
		Strategy:  "weighted_random",
		IsDefault: true,
	}
	require.NoError(t, repo.Create(set))

	// 添加 targets
	for i := 0; i < 3; i++ {
		addTestMember(t, testDB, repo, set.ID, "https://api"+string(rune('1'+i))+".example.com", 1, true, "healthy")
	}

	// 测试选择 target
	selected, hasMore, err := integration.SelectTarget(context.Background(), "", []string{})
	require.NoError(t, err)
	assert.NotNil(t, selected)
	assert.True(t, hasMore)

	// 测试记录错误
	integration.RecordError(selected.URL, 503, nil, []string{})

	// 等待事件处理
	time.Sleep(100 * time.Millisecond)

	// 验证活跃告警
	alerts := integration.GetActiveAlerts()
	assert.Greater(t, len(alerts), 0)

	// 测试记录成功
	integration.RecordSuccess(selected.URL)

	// GetHealthStatus 由 HealthMonitor 管理，不会因 RecordError 自动追踪
	// 只验证 GetAllHealthStatus 返回非 nil
	assert.NotNil(t, integration.GetAllHealthStatus())
}

// TestGroupTargetSetIntegration_FailoverFlow 测试故障转移流程
func TestGroupTargetSetIntegration_FailoverFlow(t *testing.T) {
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
		ID:        uuid.New().String(),
		Name:      "test-set",
		Strategy:  "weighted_random",
		IsDefault: true,
	}
	require.NoError(t, repo.Create(set))

	// 添加两个 targets
	url1 := "https://api1.example.com"
	url2 := "https://api2.example.com"

	for _, url := range []string{url1, url2} {
		addTestMember(t, testDB, repo, set.ID, url, 1, true, "healthy")
	}

	// 第一次选择
	selected1, _, err := integration.SelectTarget(context.Background(), "", []string{})
	require.NoError(t, err)
	assert.NotNil(t, selected1)

	// 第二次选择，排除第一个（故障转移）
	selected2, _, err := integration.SelectTarget(context.Background(), "", []string{selected1.URL})
	require.NoError(t, err)
	assert.NotNil(t, selected2)
	assert.NotEqual(t, selected1.URL, selected2.URL)
}

// TestGroupTargetSetIntegration_AlertSubscription 测试告警订阅
func TestGroupTargetSetIntegration_AlertSubscription(t *testing.T) {
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

	// 记录错误（应该推送事件）
	integration.RecordError("https://api.example.com", 503, nil, []string{})

	// 等待事件
	select {
	case event := <-alertCh:
		assert.NotNil(t, event)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for alert event")
	}
}
