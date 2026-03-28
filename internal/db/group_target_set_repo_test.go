package db

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// setupTestDB 创建测试数据库
func setupTestDB(t *testing.T) *gorm.DB {
	testDB, err := Open(zap.NewNop(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(zap.NewNop(), testDB))
	return testDB
}

// TestGroupTargetSetRepo_Create 测试创建 target set
func TestGroupTargetSetRepo_Create(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}

	err := repo.Create(set)
	require.NoError(t, err)

	// 验证创建成功
	retrieved, err := repo.GetByID(set.ID)
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, set.Name, retrieved.Name)
}

// TestGroupTargetSetRepo_GetByName 测试按名称获取
func TestGroupTargetSetRepo_GetByName(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "unique-name",
		Strategy: "weighted_random",
	}

	err := repo.Create(set)
	require.NoError(t, err)

	// 按名称获取
	retrieved, err := repo.GetByName("unique-name")
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, set.ID, retrieved.ID)
}

// TestGroupTargetSetRepo_AddMember 测试添加 member
func TestGroupTargetSetRepo_AddMember(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	member := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetURL: "https://api.example.com",
		Weight:    2,
		Priority:  1,
		IsActive:  true,
	}

	err := repo.AddMember(set.ID, member)
	require.NoError(t, err)

	// 验证添加成功
	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)
	assert.Equal(t, member.TargetURL, members[0].TargetURL)
}

// TestGroupTargetSetRepo_RemoveMember 测试删除 member
func TestGroupTargetSetRepo_RemoveMember(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	member := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetURL: "https://api.example.com",
		Weight:    1,
		IsActive:  true,
	}
	require.NoError(t, repo.AddMember(set.ID, member))

	// 删除 member
	err := repo.RemoveMember(set.ID, member.TargetURL)
	require.NoError(t, err)

	// 验证删除成功
	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Len(t, members, 0)
}

// TestGroupTargetSetRepo_UpdateMember 测试更新 member
func TestGroupTargetSetRepo_UpdateMember(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	member := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetURL: "https://api.example.com",
		Weight:    1,
		Priority:  0,
		IsActive:  true,
	}
	require.NoError(t, repo.AddMember(set.ID, member))

	// 更新权重
	err := repo.UpdateMember(set.ID, member.TargetURL, 3, 2)
	require.NoError(t, err)

	// 验证更新成功
	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)
	assert.Equal(t, 3, members[0].Weight)
	assert.Equal(t, 2, members[0].Priority)
}

// TestTargetAlertRepo_Create 测试创建告警
func TestTargetAlertRepo_Create(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewTargetAlertRepo(testDB, zap.NewNop())

	alert := &TargetAlert{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		AlertType:    "error",
		Severity:     "error",
		ErrorMessage: "connection timeout",
	}

	err := repo.Create(alert)
	require.NoError(t, err)

	// 验证创建成功
	retrieved, err := repo.GetByID(alert.ID)
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, alert.TargetURL, retrieved.TargetURL)
}

// TestTargetAlertRepo_ListActive 测试列出活跃告警
func TestTargetAlertRepo_ListActive(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewTargetAlertRepo(testDB, zap.NewNop())

	// 创建活跃告警
	alert := &TargetAlert{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		AlertType:    "error",
		Severity:     "error",
		ErrorMessage: "connection timeout",
	}
	require.NoError(t, repo.Create(alert))

	// 列出活跃告警
	filters := AlertFilters{
		Limit: 50,
	}
	alerts, err := repo.ListActive(filters)
	require.NoError(t, err)
	assert.Len(t, alerts, 1)
	assert.Equal(t, alert.ID, alerts[0].ID)
}

// TestTargetAlertRepo_Resolve 测试解决告警
func TestTargetAlertRepo_Resolve(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewTargetAlertRepo(testDB, zap.NewNop())

	alert := &TargetAlert{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		AlertType:    "error",
		Severity:     "error",
		ErrorMessage: "connection timeout",
	}
	require.NoError(t, repo.Create(alert))

	// 解决告警
	err := repo.Resolve(alert.ID)
	require.NoError(t, err)

	// 验证告警已解决
	resolved, err := repo.GetByID(alert.ID)
	require.NoError(t, err)
	assert.NotNil(t, resolved.ResolvedAt)
}

// TestTargetAlertRepo_GetOrCreateAlert 测试获取或创建告警（去重）
func TestTargetAlertRepo_GetOrCreateAlert(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewTargetAlertRepo(testDB, zap.NewNop())

	alert1 := &TargetAlert{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		AlertType:    "error",
		Severity:     "error",
		ErrorMessage: "connection timeout",
		AlertKey:     "api.example.com:connection_timeout",
	}

	// 第一次创建
	result1, err := repo.GetOrCreateAlert(alert1)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.OccurrenceCount)

	// 第二次创建相同的告警（应该更新计数）
	alert2 := &TargetAlert{
		ID:           uuid.New().String(),
		TargetURL:    "https://api.example.com",
		AlertType:    "error",
		Severity:     "error",
		ErrorMessage: "connection timeout",
		AlertKey:     "api.example.com:connection_timeout",
	}

	result2, err := repo.GetOrCreateAlert(alert2)
	require.NoError(t, err)
	assert.Equal(t, result1.ID, result2.ID) // 应该是同一条告警
	assert.Equal(t, 2, result2.OccurrenceCount)
}

// TestGroupTargetSetRepo_GetByGroupID 测试按 GroupID 获取
func TestGroupTargetSetRepo_GetByGroupID(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	groupID := uuid.New().String()
	set := &GroupTargetSet{
		ID:      uuid.New().String(),
		Name:    "group-set",
		GroupID: &groupID,
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 按 GroupID 获取
	retrieved, err := repo.GetByGroupID(groupID)
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, set.ID, retrieved.ID)

	// 不存在的 GroupID
	notFound, err := repo.GetByGroupID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

// TestGroupTargetSetRepo_GetDefault 测试获取默认 target set
func TestGroupTargetSetRepo_GetDefault(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	// 无默认时返回 nil
	notFound, err := repo.GetDefault()
	require.NoError(t, err)
	assert.Nil(t, notFound)

	// 创建默认 target set
	set := &GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "default-set",
		IsDefault: true,
		Strategy:  "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 获取默认
	retrieved, err := repo.GetDefault()
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, set.ID, retrieved.ID)
	assert.True(t, retrieved.IsDefault)
}

// TestGroupTargetSetRepo_Update 测试更新 target set
func TestGroupTargetSetRepo_Update(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "original-name",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 更新名称
	set.Name = "updated-name"
	require.NoError(t, repo.Update(set))

	// 验证更新
	retrieved, err := repo.GetByID(set.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated-name", retrieved.Name)

	// 空 ID 应该报错
	err = repo.Update(&GroupTargetSet{})
	assert.Error(t, err)
}

// TestGroupTargetSetRepo_Delete 测试删除 target set
func TestGroupTargetSetRepo_Delete(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "to-delete",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 删除
	require.NoError(t, repo.Delete(set.ID))

	// 验证删除
	retrieved, err := repo.GetByID(set.ID)
	require.NoError(t, err)
	assert.Nil(t, retrieved)
}

// TestGroupTargetSetRepo_ListAll 测试列出所有
func TestGroupTargetSetRepo_ListAll(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	// 空列表
	sets, err := repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, sets, 0)

	// 创建两个
	for i := 0; i < 2; i++ {
		s := &GroupTargetSet{
			ID:       uuid.New().String(),
			Name:     "set-" + uuid.New().String(),
			Strategy: "weighted_random",
		}
		require.NoError(t, repo.Create(s))
	}

	sets, err = repo.ListAll()
	require.NoError(t, err)
	assert.Len(t, sets, 2)
}

// TestGroupTargetSetRepo_GetAvailableTargetsForGroup 测试获取可用 targets
func TestGroupTargetSetRepo_GetAvailableTargetsForGroup(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	// 创建默认 target set
	set := &GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "default",
		IsDefault: true,
		Strategy:  "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	// 添加健康 target
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetURL: "https://api1.example.com",
		Weight: 2, IsActive: true, HealthStatus: "healthy",
	}))
	// 添加不健康 target
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetURL: "https://api2.example.com",
		Weight: 1, IsActive: true, HealthStatus: "unhealthy",
	}))
	// 添加非活跃 target
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetURL: "https://api3.example.com",
		Weight: 1, IsActive: false, HealthStatus: "healthy",
	}))

	// 按空 groupID 获取（默认组）
	targets, err := repo.GetAvailableTargetsForGroup("")
	require.NoError(t, err)
	assert.Len(t, targets, 2) // api1(healthy) + api2(unhealthy), api3 因 IsActive=false 被过滤

	// 验证健康状态
	healthyCount := 0
	for _, t2 := range targets {
		if t2.Healthy {
			healthyCount++
		}
	}
	assert.Equal(t, 1, healthyCount)
}

// TestGroupTargetSetRepo_UpdateTargetHealth 测试更新健康状态
func TestGroupTargetSetRepo_UpdateTargetHealth(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID: uuid.New().String(), Name: "test", Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetURL: "https://api.example.com",
		Weight: 1, IsActive: true,
	}))

	// 标记为不健康
	require.NoError(t, repo.UpdateTargetHealth("https://api.example.com", false))

	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Equal(t, "unhealthy", members[0].HealthStatus)

	// 标记为健康
	require.NoError(t, repo.UpdateTargetHealth("https://api.example.com", true))
	members, err = repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Equal(t, "healthy", members[0].HealthStatus)
}
