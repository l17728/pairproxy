package proxy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/db"
)

// setupTestDB 创建测试数据库
func setupTestDB(t *testing.T) *gorm.DB {
	testDB, err := db.Open(zap.NewNop(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(zap.NewNop(), testDB))
	return testDB
}

// TestGroupTargetSelector_SelectTarget_WeightedRandom 测试加权随机选择
func TestGroupTargetSelector_SelectTarget_WeightedRandom(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	selector := NewGroupTargetSelector(repo, zap.NewNop())

	// 创建 target set（设置 IsDefault=true 以便按空 groupID 查询）
	set := &db.GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "test-set",
		Strategy:  "weighted_random",
		IsDefault: true,
	}
	require.NoError(t, repo.Create(set))

	// 添加 targets
	targetConfigs := []struct {
		url    string
		weight int
	}{
		{"https://api1.example.com", 2},
		{"https://api2.example.com", 1},
	}

	for _, tc := range targetConfigs {
		member := &db.GroupTargetSetMember{
			ID:           uuid.New().String(),
			TargetURL:    tc.url,
			Weight:       tc.weight,
			IsActive:     true,
			HealthStatus: "healthy",
		}
		require.NoError(t, repo.AddMember(set.ID, member))
	}

	// 执行选择
	selected, hasMore, err := selector.SelectTarget(context.Background(), "", []string{})
	require.NoError(t, err)
	assert.NotNil(t, selected)
	assert.True(t, hasMore) // 应该还有其他可用 targets
}

// TestGroupTargetSelector_SelectTarget_NoHealthyTargets 测试没有健康的 targets
func TestGroupTargetSelector_SelectTarget_NoHealthyTargets(t *testing.T) {
	testDB := setupTestDB(t)
	repo := db.NewGroupTargetSetRepo(testDB, zap.NewNop())
	selector := NewGroupTargetSelector(repo, zap.NewNop())

	// 创建 target set
	set := &db.GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 添加不健康的 target
	member := &db.GroupTargetSetMember{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		Weight:       1,
		IsActive:     true,
		HealthStatus: "unhealthy",
	}
	require.NoError(t, repo.AddMember(set.ID, member))

	// 执行选择（应该失败）
	selected, _, err := selector.SelectTarget(context.Background(), "", []string{})
	assert.Error(t, err)
	assert.Nil(t, selected)
}

// TestWeightedRandomStrategy_Select 测试加权随机策略
func TestWeightedRandomStrategy_Select(t *testing.T) {
	strategy := NewWeightedRandomStrategy()

	targets := []db.TargetWithWeight{
		{URL: "https://api1.example.com", Weight: 2, Healthy: true},
		{URL: "https://api2.example.com", Weight: 1, Healthy: true},
	}

	// 执行多次选择，验证分布
	counts := make(map[string]int)
	for i := 0; i < 300; i++ {
		selected := strategy.Select(targets, make(map[string]bool))
		if selected != nil {
			counts[selected.URL]++
		}
	}

	// 验证 api1 被选中的次数大约是 api2 的两倍
	assert.Greater(t, counts["https://api1.example.com"], counts["https://api2.example.com"])
}

// TestRoundRobinStrategy_Select 测试轮询策略
func TestRoundRobinStrategy_Select(t *testing.T) {
	strategy := NewRoundRobinStrategy()

	targets := []db.TargetWithWeight{
		{URL: "https://api1.example.com", Weight: 1, Healthy: true},
		{URL: "https://api2.example.com", Weight: 1, Healthy: true},
	}

	// 执行轮询选择
	selected1 := strategy.Select(targets, make(map[string]bool))
	selected2 := strategy.Select(targets, make(map[string]bool))
	selected3 := strategy.Select(targets, make(map[string]bool))

	assert.NotNil(t, selected1)
	assert.NotNil(t, selected2)
	assert.NotNil(t, selected3)

	// 验证轮询顺序
	assert.NotEqual(t, selected1.URL, selected2.URL)
	assert.Equal(t, selected1.URL, selected3.URL)
}

// TestPriorityStrategy_Select 测试优先级策略
func TestPriorityStrategy_Select(t *testing.T) {
	strategy := NewPriorityStrategy()

	targets := []db.TargetWithWeight{
		{URL: "https://api1.example.com", Priority: 2, Healthy: true},
		{URL: "https://api2.example.com", Priority: 1, Healthy: true},
		{URL: "https://api3.example.com", Priority: 3, Healthy: true},
	}

	// 执行优先级选择
	selected := strategy.Select(targets, make(map[string]bool))
	assert.NotNil(t, selected)
	assert.Equal(t, "https://api2.example.com", selected.URL) // priority=1 最高
}
