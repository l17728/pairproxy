package db

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestRefreshTokenRepo_CreateAndGet 测试创建和获取刷新令牌
func TestRefreshTokenRepo_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	rt := &RefreshToken{
		JTI:       "test-jti-1",
		UserID:    "user-1",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	if err := repo.Create(rt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 验证 CreatedAt 被设置
	if rt.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}

	// 获取
	found, err := repo.GetByJTI("test-jti-1")
	if err != nil {
		t.Fatalf("GetByJTI: %v", err)
	}
	if found == nil {
		t.Fatal("should find token")
	}
	if found.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", found.UserID)
	}
	if found.Revoked {
		t.Error("Revoked should be false")
	}
}

// TestRefreshTokenRepo_GetByJTI_NotFound 测试获取不存在的令牌
func TestRefreshTokenRepo_GetByJTI_NotFound(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	found, err := repo.GetByJTI("non-existent-jti")
	if err != nil {
		t.Fatalf("GetByJTI should not error: %v", err)
	}
	if found != nil {
		t.Error("should return nil for non-existent token")
	}
}

// TestRefreshTokenRepo_Revoke 测试撤销单个令牌
func TestRefreshTokenRepo_Revoke(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	rt := &RefreshToken{
		JTI:       "test-jti-revoke",
		UserID:    "user-revoke",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	repo.Create(rt)

	// 撤销
	if err := repo.Revoke("test-jti-revoke"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// 验证已撤销
	found, _ := repo.GetByJTI("test-jti-revoke")
	if found == nil || !found.Revoked {
		t.Error("token should be revoked")
	}
}

// TestRefreshTokenRepo_RevokeAllForUser 测试撤销用户所有令牌
func TestRefreshTokenRepo_RevokeAllForUser(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	// 创建多个令牌
	for i := 0; i < 3; i++ {
		repo.Create(&RefreshToken{
			JTI:       "jti-user-all-" + string(rune('0'+i)),
			UserID:    "user-multi-revoke",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	}

	// 为另一个用户创建令牌
	repo.Create(&RefreshToken{
		JTI:       "jti-other-user",
		UserID:    "other-user",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})

	// 撤销 user-multi-revoke 的所有令牌
	if err := repo.RevokeAllForUser("user-multi-revoke"); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}

	// 验证
	for i := 0; i < 3; i++ {
		found, _ := repo.GetByJTI("jti-user-all-" + string(rune('0'+i)))
		if found == nil || !found.Revoked {
			t.Errorf("token %d should be revoked", i)
		}
	}

	// other-user 的令牌不应该被撤销
	other, _ := repo.GetByJTI("jti-other-user")
	if other == nil || other.Revoked {
		t.Error("other-user's token should not be revoked")
	}
}

// TestRefreshTokenRepo_DeleteExpired 测试删除过期令牌
func TestRefreshTokenRepo_DeleteExpired(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	// 创建过期令牌
	repo.Create(&RefreshToken{
		JTI:       "expired-jti",
		UserID:    "user-expired",
		ExpiresAt: time.Now().Add(-time.Hour), // 已过期
	})

	// 创建未过期令牌
	repo.Create(&RefreshToken{
		JTI:       "valid-jti",
		UserID:    "user-valid",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// 删除过期
	count, err := repo.DeleteExpired()
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if count != 1 {
		t.Errorf("deleted count = %d, want 1", count)
	}

	// 验证过期令牌已删除
	found, _ := repo.GetByJTI("expired-jti")
	if found != nil {
		t.Error("expired token should be deleted")
	}

	// 验证有效令牌仍然存在
	valid, _ := repo.GetByJTI("valid-jti")
	if valid == nil {
		t.Error("valid token should still exist")
	}
}

// TestRefreshTokenRepo_CreateWithExistingCreatedAt 测试创建时已有 CreatedAt
func TestRefreshTokenRepo_CreateWithExistingCreatedAt(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewRefreshTokenRepo(db, logger)

	existingTime := time.Now().Add(-time.Hour)
	rt := &RefreshToken{
		JTI:       "test-jti-existing-time",
		UserID:    "user-existing",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		CreatedAt: existingTime,
	}

	if err := repo.Create(rt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 验证 CreatedAt 未被覆盖
	found, _ := repo.GetByJTI("test-jti-existing-time")
	if found == nil {
		t.Fatal("should find token")
	}
	// 时间应该大致相等（允许微小差异）
	if found.CreatedAt.Sub(existingTime) > time.Second {
		t.Errorf("CreatedAt = %v, want %v", found.CreatedAt, existingTime)
	}
}

// TestAuditRepo_CreateAndListExt 测试审计日志创建和列表
func TestAuditRepo_CreateAndListExt(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, db)

	// 创建多条审计记录
	records := []struct {
		operator string
		action   string
		target   string
		detail   string
	}{
		{"admin", "user.create", "alice", `{"group":"engineering"}`},
		{"admin", "group.set_quota", "engineering", `{"daily":10000}`},
		{"admin", "user.disable", "bob", ""},
	}

	for _, r := range records {
		if err := repo.Create(r.operator, r.action, r.target, r.detail); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// 列出所有
	logs, err := repo.ListRecent(100)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("len(logs) = %d, want 3", len(logs))
	}

	// 验证顺序（降序）
	if logs[0].Action != "user.disable" {
		t.Errorf("first log action = %q, want user.disable", logs[0].Action)
	}
}

// TestAuditRepo_ListRecent_Limit 测试列表限制
func TestAuditRepo_ListRecent_Limit(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, db)

	// 创建 5 条记录
	for i := 0; i < 5; i++ {
		repo.Create("admin", "action"+string(rune('0'+i)), "target", "")
	}

	// 限制为 3
	logs, err := repo.ListRecent(3)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("len(logs) = %d, want 3", len(logs))
	}
}

// TestAuditRepo_ListRecent_DefaultLimit 测试默认限制
func TestAuditRepo_ListRecent_DefaultLimit(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, db)

	// 使用 0 或负数作为限制
	logs, err := repo.ListRecent(0)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	// 应该使用默认值 100
	if logs == nil {
		t.Error("logs should not be nil")
	}

	logs2, err := repo.ListRecent(-1)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if logs2 == nil {
		t.Error("logs2 should not be nil")
	}
}

// TestAuditRepo_EmptyList 测试空列表
func TestAuditRepo_EmptyList(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, db)

	logs, err := repo.ListRecent(10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("len(logs) = %d, want 0", len(logs))
	}
}

// TestAuditRepo_Fields 测试审计日志字段
func TestAuditRepo_Fields(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewAuditRepo(logger, db)

	if err := repo.Create("operator1", "action1", "target1", "detail1"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	logs, _ := repo.ListRecent(1)
	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1", len(logs))
	}

	log := logs[0]
	if log.Operator != "operator1" {
		t.Errorf("Operator = %q, want operator1", log.Operator)
	}
	if log.Action != "action1" {
		t.Errorf("Action = %q, want action1", log.Action)
	}
	if log.Target != "target1" {
		t.Errorf("Target = %q, want target1", log.Target)
	}
	if log.Detail != "detail1" {
		t.Errorf("Detail = %q, want detail1", log.Detail)
	}
	if log.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

// TestUserRepo_GetByExternalID 测试按外部 ID 查询用户
func TestUserRepo_GetByExternalID(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	// 创建带外部 ID 的用户
	u := &User{
		Username:     "ldap-user",
		PasswordHash: "",
		AuthProvider: "ldap",
		ExternalID: func(s string) *string { return &s }("cn=zhangsan,ou=users,dc=company,dc=com"),
		IsActive:     true,
	}
	if err := userRepo.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 按外部 ID 查询
	found, err := userRepo.GetByExternalID("ldap", "cn=zhangsan,ou=users,dc=company,dc=com")
	if err != nil {
		t.Fatalf("GetByExternalID: %v", err)
	}
	if found == nil {
		t.Fatal("should find user")
	}
	if found.Username != "ldap-user" {
		t.Errorf("Username = %q, want ldap-user", found.Username)
	}
}

// TestUserRepo_GetByExternalID_NotFound 测试外部 ID 不存在
func TestUserRepo_GetByExternalID_NotFound(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)

	found, err := userRepo.GetByExternalID("ldap", "non-existent")
	if err != nil {
		t.Fatalf("GetByExternalID: %v", err)
	}
	if found != nil {
		t.Error("should return nil for non-existent external ID")
	}
}

// TestUserRepo_SetGroup 测试设置用户分组
func TestUserRepo_SetGroup(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)
	groupRepo := NewGroupRepo(db, logger)

	// 创建分组
	g := &Group{Name: "test-group"}
	groupRepo.Create(g)

	// 创建用户
	u := &User{Username: "group-user", PasswordHash: "hash"}
	userRepo.Create(u)

	// 设置分组
	if err := userRepo.SetGroup(u.ID, &g.ID); err != nil {
		t.Fatalf("SetGroup: %v", err)
	}

	// 验证
	found, _ := userRepo.GetByID(u.ID)
	if found == nil || found.GroupID == nil || *found.GroupID != g.ID {
		t.Error("group should be set")
	}

	// 移除分组
	if err := userRepo.SetGroup(u.ID, nil); err != nil {
		t.Fatalf("SetGroup(nil): %v", err)
	}

	found, _ = userRepo.GetByID(u.ID)
	if found == nil || found.GroupID != nil {
		t.Error("group should be nil after removal")
	}
}

// TestUserRepo_ListByGroup 测试按分组列出用户
func TestUserRepo_ListByGroup(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	userRepo := NewUserRepo(db, logger)
	groupRepo := NewGroupRepo(db, logger)

	// 创建分组
	g1 := &Group{Name: "group1"}
	g2 := &Group{Name: "group2"}
	groupRepo.Create(g1)
	groupRepo.Create(g2)

	// 创建用户
	for i := 0; i < 3; i++ {
		userRepo.Create(&User{
			Username:     "user-g1-" + string(rune('0'+i)),
			PasswordHash: "hash",
			GroupID:      &g1.ID,
		})
	}
	for i := 0; i < 2; i++ {
		userRepo.Create(&User{
			Username:     "user-g2-" + string(rune('0'+i)),
			PasswordHash: "hash",
			GroupID:      &g2.ID,
		})
	}
	// 无分组用户
	userRepo.Create(&User{Username: "user-no-group", PasswordHash: "hash"})

	// 按分组查询
	usersG1, err := userRepo.ListByGroup(g1.ID)
	if err != nil {
		t.Fatalf("ListByGroup: %v", err)
	}
	if len(usersG1) != 3 {
		t.Errorf("len(usersG1) = %d, want 3", len(usersG1))
	}

	usersG2, _ := userRepo.ListByGroup(g2.ID)
	if len(usersG2) != 2 {
		t.Errorf("len(usersG2) = %d, want 2", len(usersG2))
	}

	// 查询所有用户
	allUsers, err := userRepo.ListByGroup("")
	if err != nil {
		t.Fatalf("ListByGroup(''): %v", err)
	}
	if len(allUsers) != 6 {
		t.Errorf("len(allUsers) = %d, want 6", len(allUsers))
	}
}

// TestGroupRepo_Delete 测试删除分组
func TestGroupRepo_Delete(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)
	userRepo := NewUserRepo(db, logger)

	g := &Group{Name: "delete-test-group"}
	groupRepo.Create(g)

	// 创建分组内用户
	userRepo.Create(&User{
		Username:     "user-in-delete-group",
		PasswordHash: "hash",
		GroupID:      &g.ID,
	})

	// 不强制删除应该失败
	err := groupRepo.Delete(g.ID, false)
	if err == nil {
		t.Error("Delete should fail when group has users")
	}

	// 强制删除
	err = groupRepo.Delete(g.ID, true)
	if err != nil {
		t.Fatalf("Delete(force=true): %v", err)
	}

	// 验证分组已删除
	found, _ := groupRepo.GetByID(g.ID)
	if found != nil {
		t.Error("group should be deleted")
	}

	// 验证用户的 group_id 已清空
	u, _ := userRepo.GetByUsername("user-in-delete-group")
	if u == nil || u.GroupID != nil {
		t.Error("user's group_id should be nil after group deletion")
	}
}

// TestGroupRepo_Delete_Empty 测试删除空分组
func TestGroupRepo_Delete_Empty(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	g := &Group{Name: "empty-group"}
	groupRepo.Create(g)

	// 空分组应该可以正常删除
	err := groupRepo.Delete(g.ID, false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	found, _ := groupRepo.GetByID(g.ID)
	if found != nil {
		t.Error("group should be deleted")
	}
}

// TestGroupRepo_GetByName 测试按名称查询分组
func TestGroupRepo_GetByName(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	g := &Group{Name: "unique-group-name"}
	groupRepo.Create(g)

	found, err := groupRepo.GetByName("unique-group-name")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if found == nil {
		t.Fatal("should find group")
	}
	if found.ID != g.ID {
		t.Errorf("ID = %q, want %q", found.ID, g.ID)
	}
}

// TestGroupRepo_GetByName_NotFound 测试名称不存在
func TestGroupRepo_GetByName_NotFound(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	found, err := groupRepo.GetByName("non-existent-group")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if found != nil {
		t.Error("should return nil for non-existent group")
	}
}

// TestGroupRepo_List 测试列出所有分组
func TestGroupRepo_List(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	// 创建多个分组
	for i := 0; i < 3; i++ {
		groupRepo.Create(&Group{Name: "list-group-" + string(rune('0'+i))})
	}

	groups, err := groupRepo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(groups) != 3 {
		t.Errorf("len(groups) = %d, want 3", len(groups))
	}
}

// TestGroupRepo_DuplicateName 测试重复分组名
func TestGroupRepo_DuplicateName(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	groupRepo := NewGroupRepo(db, logger)

	g1 := &Group{Name: "duplicate-name"}
	if err := groupRepo.Create(g1); err != nil {
		t.Fatalf("Create: %v", err)
	}

	g2 := &Group{Name: "duplicate-name"}
	if err := groupRepo.Create(g2); err == nil {
		t.Error("Create with duplicate name should fail")
	}
}

// TestUsageRepo_DeleteBefore 测试删除指定时间之前的日志
func TestUsageRepo_DeleteBefore(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, 100*time.Millisecond)
	ctx, cancel := contextWithCancel()
	writer.Start(ctx)

	now := time.Now()
	// 创建旧记录
	for i := 0; i < 3; i++ {
		writer.Record(UsageRecord{
			RequestID: "old-req-" + string(rune('0'+i)),
			UserID:    "user-old",
			CreatedAt: now.Add(-48 * time.Hour),
		})
	}
	// 创建新记录
	for i := 0; i < 2; i++ {
		writer.Record(UsageRecord{
			RequestID: "new-req-" + string(rune('0'+i)),
			UserID:    "user-new",
			CreatedAt: now,
		})
	}
	stopWriter(cancel, writer)
	time.Sleep(50 * time.Millisecond) // 等待数据库写入完成

	repo := NewUsageRepo(db, logger)

	// 删除 24 小时前的记录
	deleted, err := repo.DeleteBefore(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}

	// 验证新记录仍然存在
	logs, _ := repo.Query(UsageFilter{UserID: "user-new"})
	if len(logs) != 2 {
		t.Errorf("len(logs) = %d, want 2", len(logs))
	}
}

// TestUsageRepo_DailyTokensExt 测试按天聚合 token
func TestUsageRepo_DailyTokensExt(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, 100*time.Millisecond)
	ctx, cancel := contextWithCancel()
	writer.Start(ctx)

	now := time.Now().UTC() // 使用 UTC 时间避免时区问题
	yesterday := now.Add(-24 * time.Hour)

	// 今天的数据
	for i := 0; i < 3; i++ {
		writer.Record(UsageRecord{
			RequestID:    "today-req-" + string(rune('0'+i)),
			UserID:       "user-daily",
			InputTokens:  100,
			OutputTokens: 50,
			CreatedAt:    now,
		})
	}
	// 昨天的数据
	for i := 0; i < 2; i++ {
		writer.Record(UsageRecord{
			RequestID:    "yesterday-req-" + string(rune('0'+i)),
			UserID:       "user-daily",
			InputTokens:  200,
			OutputTokens: 100,
			CreatedAt:    yesterday,
		})
	}
	stopWriter(cancel, writer)
	time.Sleep(50 * time.Millisecond) // 等待数据库写入完成

	repo := NewUsageRepo(db, logger)

	from := yesterday.Truncate(24 * time.Hour)
	to := now

	rows, err := repo.DailyTokens(from, to, "user-daily")
	if err != nil {
		t.Fatalf("DailyTokens: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("len(rows) = %d, want 2", len(rows))
	}

	// 调试：打印所有返回的日期
	expectedDate := now.Format("2006-01-02")
	t.Logf("Expected today's date: %s", expectedDate)
	for i, row := range rows {
		t.Logf("Row %d: date=%s, input=%d, output=%d", i, row.Date, row.InputTokens, row.OutputTokens)
	}

	// 验证今天的总量
	var todayRow *DailyTokenRow
	for i := range rows {
		if rows[i].Date == expectedDate {
			todayRow = &rows[i]
			break
		}
	}
	if todayRow == nil {
		t.Fatalf("should find today's row (expected date: %s)", expectedDate)
	}
	if todayRow.InputTokens != 300 || todayRow.OutputTokens != 150 {
		t.Errorf("today tokens = %d/%d, want 300/150", todayRow.InputTokens, todayRow.OutputTokens)
	}
}

// TestUsageRepo_DailyCostExt 测试按天聚合费用
func TestUsageRepo_DailyCostExt(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, time.Minute)
	writer.SetCostFunc(func(model string, input, output int) float64 {
		return float64(input+output) * 0.001
	})
	ctx, cancel := contextWithCancel()
	writer.Start(ctx)

	now := time.Now()
	writer.Record(UsageRecord{
		RequestID:    "cost-req-1",
		UserID:       "user-cost",
		InputTokens:  1000,
		OutputTokens: 500,
		CreatedAt:    now,
	})
	stopWriter(cancel, writer)

	repo := NewUsageRepo(db, logger)

	rows, err := repo.DailyCost(now.Add(-24*time.Hour), now, "user-cost")
	if err != nil {
		t.Fatalf("DailyCost: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].CostUSD != 1.5 {
		t.Errorf("CostUSD = %f, want 1.5", rows[0].CostUSD)
	}
}

// TestUsageRepo_GlobalSumTokens 测试全局统计
func TestUsageRepo_GlobalSumTokens(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)

	writer := NewUsageWriter(db, logger, 100, 100*time.Millisecond)
	ctx, cancel := contextWithCancel()
	writer.Start(ctx)

	now := time.Now()
	for i := 0; i < 5; i++ {
		writer.Record(UsageRecord{
			RequestID:    "global-req-" + string(rune('0'+i)),
			UserID:       "user-global",
			InputTokens:  100,
			OutputTokens: 50,
			StatusCode:   200,
			CreatedAt:    now,
		})
	}
	// 一个错误请求
	writer.Record(UsageRecord{
		RequestID:    "global-err",
		UserID:       "user-global",
		InputTokens:  0,
		OutputTokens: 0,
		StatusCode:   500,
		CreatedAt:    now,
	})
	stopWriter(cancel, writer)

	repo := NewUsageRepo(db, logger)

	stats, err := repo.GlobalSumTokens(now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("GlobalSumTokens: %v", err)
	}
	if stats.TotalInput != 500 {
		t.Errorf("TotalInput = %d, want 500", stats.TotalInput)
	}
	if stats.TotalOutput != 250 {
		t.Errorf("TotalOutput = %d, want 250", stats.TotalOutput)
	}
	if stats.RequestCount != 6 {
		t.Errorf("RequestCount = %d, want 6", stats.RequestCount)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", stats.ErrorCount)
	}
}

// contextWithCancel 创建可取消的上下文（简化测试）
func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
