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
		TargetID: "test-target-uuid",
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
	assert.Equal(t, member.TargetID, members[0].TargetID)
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
		TargetID: "test-target-uuid",
		Weight:    1,
		IsActive:  true,
	}
	require.NoError(t, repo.AddMember(set.ID, member))

	// 删除 member
	err := repo.RemoveMember(set.ID, member.TargetID)
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
		TargetID: "test-target-uuid",
		Weight:    1,
		Priority:  0,
		IsActive:  true,
	}
	require.NoError(t, repo.AddMember(set.ID, member))

	// 更新权重
	err := repo.UpdateMember(set.ID, member.TargetID, 3, 2)
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
		TargetURL:    "test-target-uuid",
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
		TargetURL:    "test-target-uuid",
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
		TargetURL:    "test-target-uuid",
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
		TargetURL:    "test-target-uuid",
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
		TargetURL:    "test-target-uuid",
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
		ID: uuid.New().String(), TargetID: "test-target-1",
		Weight: 2, IsActive: true, HealthStatus: "healthy",
	}))
	// 添加不健康 target
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetID: "test-target-2",
		Weight: 1, IsActive: true, HealthStatus: "unhealthy",
	}))
	// 添加非活跃 target
	require.NoError(t, repo.AddMember(set.ID, &GroupTargetSetMember{
		ID: uuid.New().String(), TargetID: "test-target-3",
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
		ID: uuid.New().String(), TargetID: "test-target-uuid",
		Weight: 1, IsActive: true,
	}))

	// 标记为不健康（使用 targetID，不是 URL）
	require.NoError(t, repo.UpdateTargetHealth("test-target-uuid", false))

	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Equal(t, "unhealthy", members[0].HealthStatus)

	// 标记为健康
	require.NoError(t, repo.UpdateTargetHealth("test-target-uuid", true))
	members, err = repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Equal(t, "healthy", members[0].HealthStatus)
}

// TestGroupTargetSetRepo_GetByName_AmbiguousWithDifferentGroups 测试 GetByName 的歧义问题
// Name 与 GroupID 组成复合唯一约束，GetByName 只按 Name 查询会导致多个结果
func TestGroupTargetSetRepo_GetByName_AmbiguousWithDifferentGroups(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	// 创建两个不同 GroupID 但相同 Name 的 target set
	groupID1 := uuid.New().String()
	groupID2 := uuid.New().String()

	set1 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "shared-name",
		GroupID:  &groupID1,
		Strategy: "weighted_random",
	}
	set2 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "shared-name",
		GroupID:  &groupID2,
		Strategy: "weighted_random",
	}

	require.NoError(t, repo.Create(set1))
	require.NoError(t, repo.Create(set2))

	// GetByName 应该返回多个结果，但当前实现只返回第一个
	// 这表现为与 User.ListByUsername() 相同的歧义问题
	retrieved, err := repo.GetByName("shared-name")
	require.NoError(t, err)

	// 当前实现返回第一个结果，应该改为检测歧义或改用 GetByGroupIDAndName
	assert.NotNil(t, retrieved)
	// 实际上，这暴露了设计缺陷：GetByName 不应该在存在复合约束时使用
}

// TestGroupTargetSetRepo_GetByGroupID_AmbiguousMultipleSets 测试 GetByGroupID 的歧义问题
// 一个 GroupID 可能关联多个 target set（Name 不同），GetByGroupID 只返回 LIMIT 1
func TestGroupTargetSetRepo_GetByGroupID_AmbiguousMultipleSets(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	groupID := uuid.New().String()

	// 创建两个不同 Name 但相同 GroupID 的 target set
	set1 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "set-1",
		GroupID:  &groupID,
		Strategy: "weighted_random",
	}
	set2 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "set-2",
		GroupID:  &groupID,
		Strategy: "weighted_random",
	}

	require.NoError(t, repo.Create(set1))
	require.NoError(t, repo.Create(set2))

	// GetByGroupID 只返回第一个，隐藏了实际有多个的事实
	retrieved, err := repo.GetByGroupID(groupID)
	require.NoError(t, err)
	assert.NotNil(t, retrieved)

	// 应该使用列表方法或明确哪个 Name 的 set
}

// TestGroupTargetSetRepo_ListByGroupID 测试返回一个 group 的所有 target set
// 这是问题 #28/#35 的修复：确保一对多关系正确处理
func TestGroupTargetSetRepo_ListByGroupID_MultipleResults(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	groupID := uuid.New().String()

	// 创建三个不同 Name 但相同 GroupID 的 target set
	set1 := &GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "set-1",
		GroupID:   &groupID,
		Strategy:  "weighted_random",
		IsDefault: true,
	}
	set2 := &GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "set-2",
		GroupID:   &groupID,
		Strategy:  "round_robin",
		IsDefault: false,
	}
	set3 := &GroupTargetSet{
		ID:        uuid.New().String(),
		Name:      "set-3",
		GroupID:   &groupID,
		Strategy:  "priority",
		IsDefault: false,
	}

	require.NoError(t, repo.Create(set1))
	require.NoError(t, repo.Create(set2))
	require.NoError(t, repo.Create(set3))

	// ListByGroupID 应该返回所有 3 个 set
	sets, err := repo.ListByGroupID(groupID)
	require.NoError(t, err)
	assert.Len(t, sets, 3)

	// 验证全部存在
	ids := make(map[string]bool)
	for _, s := range sets {
		ids[s.ID] = true
	}
	assert.True(t, ids[set1.ID])
	assert.True(t, ids[set2.ID])
	assert.True(t, ids[set3.ID])

	// 验证策略正确
	defaultSet := &GroupTargetSet{}
	for i := range sets {
		if sets[i].IsDefault {
			defaultSet = &sets[i]
			break
		}
	}
	assert.True(t, defaultSet.IsDefault)
	assert.Equal(t, "set-1", defaultSet.Name)
}

// TestGroupTargetSetRepo_ListByGroupID_EmptyResult 测试无 set 的 group
func TestGroupTargetSetRepo_ListByGroupID_EmptyResult(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	groupID := uuid.New().String()

	sets, err := repo.ListByGroupID(groupID)
	require.NoError(t, err)
	assert.Len(t, sets, 0)
}

// TestGroupTargetSetRepo_GetByGroupIDAndName_UniqueComposite 测试复合约束正确查询
// GetByGroupIDAndName 正确利用复合唯一约束，应该返回唯一结果
func TestGroupTargetSetRepo_GetByGroupIDAndName_UniqueComposite(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	groupID := uuid.New().String()

	// 创建两个不同 Name 但相同 GroupID 的 target set
	set1 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "set-1",
		GroupID:  &groupID,
		Strategy: "weighted_random",
	}
	set2 := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "set-2",
		GroupID:  &groupID,
		Strategy: "weighted_random",
	}

	require.NoError(t, repo.Create(set1))
	require.NoError(t, repo.Create(set2))

	// GetByGroupIDAndName 应该唯一地返回 set1
	retrieved, err := repo.GetByGroupIDAndName(&groupID, "set-1")
	require.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, set1.ID, retrieved.ID)

	// 验证可以唯一地检索 set2
	retrieved2, err := repo.GetByGroupIDAndName(&groupID, "set-2")
	require.NoError(t, err)
	assert.NotNil(t, retrieved2)
	assert.Equal(t, set2.ID, retrieved2.ID)
}

// TestGroupTargetSetRepo_AddMember_DuplicateRejected 测试 AddMember 的复合唯一约束强制
// (target_set_id, target_id) 组合必须唯一
func TestGroupTargetSetRepo_AddMember_DuplicateRejected(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	targetID := "test-target-uuid"
	member1 := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetID:  targetID,
		Weight:    1,
		IsActive:  true,
	}

	// 第一次添加应成功
	err := repo.AddMember(set.ID, member1)
	require.NoError(t, err)

	// 第二次添加相同 (set_id, target_id) 应失败
	member2 := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetID:  targetID,
		Weight:    2,
		IsActive:  true,
	}
	err = repo.AddMember(set.ID, member2)
	assert.Error(t, err, "AddMember 应拒绝重复的 (target_set_id, target_id) 组合")
}

// TestGroupTargetSetRepo_AddMember_TransactionalCheck 测试 AddMember 的事务性检查
// 确保检查和插入在同一事务中执行，避免 TOCTOU 问题
func TestGroupTargetSetRepo_AddMember_TransactionalCheck(t *testing.T) {
	testDB := setupTestDB(t)
	repo := NewGroupTargetSetRepo(testDB, zap.NewNop())

	set := &GroupTargetSet{
		ID:       uuid.New().String(),
		Name:     "test-set",
		Strategy: "weighted_random",
	}
	require.NoError(t, repo.Create(set))

	targetID := "test-target-uuid"
	member := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetID:  targetID,
		Weight:    1,
		IsActive:  true,
	}

	// 第一次添加成功
	err := repo.AddMember(set.ID, member)
	require.NoError(t, err)

	// 验证成功添加
	members, err := repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)

	// 尝试再次添加相同成员（应在事务中被检查）
	member2 := &GroupTargetSetMember{
		ID:        uuid.New().String(),
		TargetID:  targetID,
		Weight:    2,
		IsActive:  true,
	}
	err = repo.AddMember(set.ID, member2)
	require.Error(t, err, "应拒绝添加")

	// 验证仍然只有一条记录（没有部分成功）
	members, err = repo.ListMembers(set.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1, "事务应确保要么全部成功，要么全部失败")
}
