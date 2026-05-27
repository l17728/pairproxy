package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// LLMBindingRepo 测试
// ---------------------------------------------------------------------------

func TestLLMBindingRepo_SetUser(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userID := "user-1"
	targetID := "target-uuid-anthropic"
	if err := repo.Set(targetID, &userID, nil); err != nil {
		t.Fatalf("Set: %v", err)
	}

	gotTargetID, found, err := repo.FindForUser(userID, "")
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding to be found")
	}
	if gotTargetID != targetID {
		t.Errorf("targetID = %q, want %q", gotTargetID, targetID)
	}
}

func TestLLMBindingRepo_SetGroup(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	// 分组绑定现在通过 AddGroupBinding（需要真实 target）
	tgt := &LLMTarget{ID: "target-uuid-openai", URL: "https://openai.example.com", Provider: "openai"}
	if err := targetRepo.Create(tgt); err != nil {
		t.Fatalf("create target: %v", err)
	}

	groupID := "group-1"
	if err := repo.AddGroupBinding(tgt.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding: %v", err)
	}

	// user ID 不匹配 → 走分组绑定
	gotTargetID, found, err := repo.FindForUser("", groupID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected group binding to be found")
	}
	if gotTargetID != tgt.ID {
		t.Errorf("targetID = %q, want %q", gotTargetID, tgt.ID)
	}
}

func TestLLMBindingRepo_UserPriorityOverGroup(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	userID := "user-2"
	groupID := "group-2"

	// 分组绑定需要真实 target（AddGroupBinding 会查 DB）
	tgtGroup := &LLMTarget{ID: "target-uuid-openai-g", URL: "https://openai-g.example.com", Provider: "openai"}
	if err := targetRepo.Create(tgtGroup); err != nil {
		t.Fatalf("create group target: %v", err)
	}

	// 分组绑定 A
	if err := repo.AddGroupBinding(tgtGroup.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding: %v", err)
	}
	// 用户绑定 B（应优先）—— 用户级绑定 targetID 不需要存在于 llm_targets
	targetAnthropic := "target-uuid-anthropic-u"
	if err := repo.Set(targetAnthropic, &userID, nil); err != nil {
		t.Fatalf("Set user: %v", err)
	}

	gotTargetID, found, err := repo.FindForUser(userID, groupID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding found")
	}
	if gotTargetID != targetAnthropic {
		t.Errorf("expected user-level binding, got %q", gotTargetID)
	}
}

func TestLLMBindingRepo_SetReplace(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userID := "user-3"
	targetA := "target-uuid-a"
	targetB := "target-uuid-b"
	if err := repo.Set(targetA, &userID, nil); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := repo.Set(targetB, &userID, nil); err != nil {
		t.Fatalf("Set second: %v", err)
	}

	gotTargetID, found, err := repo.FindForUser(userID, "")
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding found after replace")
	}
	if gotTargetID != targetB {
		t.Errorf("expected targetB after replace, got %q", gotTargetID)
	}

	// 只应有一条绑定
	bindings, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bindings) != 1 {
		t.Errorf("expected 1 binding after replace, got %d", len(bindings))
	}
}

func TestLLMBindingRepo_Delete(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userID := "user-4"
	if err := repo.Set("target-uuid-del", &userID, nil); err != nil {
		t.Fatalf("Set: %v", err)
	}

	bindings, _ := repo.List()
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}

	if err := repo.Delete(bindings[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found, err := repo.FindForUser(userID, "")
	if err != nil {
		t.Fatalf("FindForUser after delete: %v", err)
	}
	if found {
		t.Error("expected no binding after delete")
	}
}

func TestLLMBindingRepo_List(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	targets := []string{"target-uuid-list-1", "target-uuid-list-2"}
	for i, tgt := range targets {
		uid := "user-list-" + itoa(i)
		if err := repo.Set(tgt, &uid, nil); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}

	bindings, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bindings) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(bindings))
	}
}

func TestLLMBindingRepo_EvenDistribute_RoundRobin(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userIDs := []string{"u1", "u2", "u3", "u4", "u5", "u6"}
	// 使用 UUID 风格的 targetID（与新的 LLMBinding.TargetID 字段对应）
	targetIDs := []string{"tid-a", "tid-b", "tid-c"}

	if err := repo.EvenDistribute(userIDs, targetIDs); err != nil {
		t.Fatalf("EvenDistribute: %v", err)
	}

	// 验证每个 target 各有 2 个用户
	bindings, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bindings) != 6 {
		t.Fatalf("expected 6 bindings, got %d", len(bindings))
	}

	counts := map[string]int{}
	for _, b := range bindings {
		counts[b.TargetID]++ // 按 TargetID（UUID）统计
	}
	for _, tid := range targetIDs {
		if counts[tid] != 2 {
			t.Errorf("target %q: expected 2 users, got %d", tid, counts[tid])
		}
	}
}

func TestLLMBindingRepo_EvenDistribute_EmptyTargets(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	err := repo.EvenDistribute([]string{"u1"}, []string{})
	if err == nil {
		t.Error("expected error for empty targetURLs")
	}
}

// ---------------------------------------------------------------------------
// TestLLMBindingRepo_EvenDistribute_SkipsExistingBindings
//
// 回归测试：distribute 不应覆盖已有用户级绑定（直连用户场景）。
// 修复前：先清空所有用户绑定再重新分配，直连用户绑定被破坏。
// 修复后：跳过已有绑定的用户，只分配无绑定的用户。
// ---------------------------------------------------------------------------

func TestLLMBindingRepo_EvenDistribute_SkipsExistingBindings(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	targetIDs := []string{"tid-skip-a", "tid-skip-b"}
	fixedTargetID := "tid-fixed"

	// u1 已有绑定（模拟直连用户手动设置）
	u1 := "u1"
	if err := repo.Set(fixedTargetID, &u1, nil); err != nil {
		t.Fatalf("Set u1: %v", err)
	}

	// u2, u3 无绑定，应被 distribute 分配
	userIDs := []string{"u1", "u2", "u3"}
	if err := repo.EvenDistribute(userIDs, targetIDs); err != nil {
		t.Fatalf("EvenDistribute: %v", err)
	}

	// u1 的绑定必须保持不变
	gotTargetID, found, err := repo.FindForUser("u1", "")
	if err != nil {
		t.Fatalf("FindForUser u1: %v", err)
	}
	if !found {
		t.Fatal("u1 binding should still exist after distribute")
	}
	if gotTargetID != fixedTargetID {
		t.Errorf("u1 binding = %q, want %q (distribute must not overwrite existing bindings)", gotTargetID, fixedTargetID)
	}

	// u2, u3 应被分配到 targets 中
	for _, uid := range []string{"u2", "u3"} {
		gotID, found, err := repo.FindForUser(uid, "")
		if err != nil {
			t.Fatalf("FindForUser %s: %v", uid, err)
		}
		if !found {
			t.Errorf("%s should have been assigned a binding by distribute", uid)
			continue
		}
		if gotID != "tid-skip-a" && gotID != "tid-skip-b" {
			t.Errorf("%s binding = %q, want one of the distribute targets", uid, gotID)
		}
	}
}

// TestLLMBindingRepo_SetReplace_UserMultipleBindings_DeletesOld 验证 Set 正确替换用户的旧绑定
// 这测试 (user_id, target_id) 复合唯一约束的使用
func TestLLMBindingRepo_SetReplace_UserMultipleBindings_DeletesOld(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMBindingRepo(gormDB, logger)

	userID := "user-123"
	oldTargetID := "target-old"
	newTargetID := "target-new"

	// 第一次 Set：user-123 → target-old
	err = repo.Set(oldTargetID, &userID, nil)
	require.NoError(t, err)

	// 验证初始绑定
	found, foundUser, err := repo.FindForUser(userID, "")
	require.NoError(t, err)
	assert.True(t, foundUser)
	assert.Equal(t, oldTargetID, found)

	// 第二次 Set：user-123 → target-new（应替换）
	err = repo.Set(newTargetID, &userID, nil)
	require.NoError(t, err)

	// 验证绑定已更新
	found, foundUser, err = repo.FindForUser(userID, "")
	require.NoError(t, err)
	assert.True(t, foundUser)
	assert.Equal(t, newTargetID, found, "Set 应替换用户的旧绑定")

	// 验证旧绑定已删除
	bindings, err := repo.List()
	require.NoError(t, err)
	userBindings := 0
	for _, b := range bindings {
		if b.UserID != nil && *b.UserID == userID {
			userBindings++
		}
	}
	assert.Equal(t, 1, userBindings, "用户应只有一条绑定记录（旧的已删除）")
}

// TestLLMBindingRepo_GroupBinding_AccumulatesNotReplaces 验证分组绑定的 1:N 语义：
// AddGroupBinding 追加（不替换），分组可持有多条绑定。
// 若要"替换"，需先 RemoveGroupBinding 删除旧条目。
func TestLLMBindingRepo_GroupBinding_AccumulatesNotReplaces(t *testing.T) {
	logger := zap.NewNop()
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	groupID := "group-456"
	tgt1 := &LLMTarget{ID: "target-old", URL: "https://old.example.com", Provider: "anthropic"}
	tgt2 := &LLMTarget{ID: "target-new", URL: "https://new.example.com", Provider: "anthropic"}
	require.NoError(t, targetRepo.Create(tgt1))
	require.NoError(t, targetRepo.Create(tgt2))

	// 添加第一条绑定
	err = repo.AddGroupBinding(tgt1.ID, groupID)
	require.NoError(t, err)

	// 验证第一条绑定
	ids, err := repo.FindAllForGroup(groupID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(ids))
	assert.Equal(t, tgt1.ID, ids[0])

	// 添加第二条同 provider 绑定（追加，不替换）
	err = repo.AddGroupBinding(tgt2.ID, groupID)
	require.NoError(t, err)

	// 现在分组应有两条绑定
	ids, err = repo.FindAllForGroup(groupID)
	require.NoError(t, err)
	assert.Equal(t, 2, len(ids), "分组绑定应是 1:N（追加），不是替换")

	// 模拟"替换"：先删除旧的，再验证只剩新的
	bindings, err := repo.List()
	require.NoError(t, err)
	for _, b := range bindings {
		if b.GroupID != nil && *b.GroupID == groupID && b.TargetID == tgt1.ID {
			require.NoError(t, repo.RemoveGroupBinding(b.ID))
			break
		}
	}

	ids, err = repo.FindAllForGroup(groupID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(ids))
	assert.Equal(t, tgt2.ID, ids[0], "删除旧绑定后分组只剩新绑定")
}

// TestLLMBindingRepo_FindForUser_DefensiveCheck 测试防御性检查（问题 #31/#36 修复）
// 验证当 Set() 的 delete-then-insert 保证生效时，FindForUser() 返回唯一结果
func TestLLMBindingRepo_FindForUser_SetGuaranteesUnique(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMBindingRepo(gormDB, logger)

	const (
		userID  = "user-test-1"
		target1 = "https://llm1.example.com"
		target2 = "https://llm2.example.com"
	)

	uid := userID

	// 第一次 Set
	require.NoError(t, repo.Set(target1, &uid, nil))

	// FindForUser 应该找到 target1
	tid, found, err := repo.FindForUser(userID, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, target1, tid)

	// 再次 Set（替换）
	require.NoError(t, repo.Set(target2, &uid, nil))

	// FindForUser 应该找到 target2（旧绑定已被删除）
	tid, found, err = repo.FindForUser(userID, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, target2, tid)

	// 验证数据库中只有一条绑定
	bindings, err := repo.List()
	require.NoError(t, err)
	userBindings := 0
	for _, b := range bindings {
		if b.UserID != nil && *b.UserID == userID {
			userBindings++
		}
	}
	assert.Equal(t, 1, userBindings, "Set() 应通过 delete-then-insert 保证最多一条用户绑定")
}

// ---------------------------------------------------------------------------
// AddGroupBinding / RemoveGroupBinding / FindAllForGroup 测试（1:N 语义）
// ---------------------------------------------------------------------------

// setupGroupBindingTargets 在 DB 中创建两个 anthropic target 和一个 openai target。
func setupGroupBindingTargets(t *testing.T, targetRepo *LLMTargetRepo) (anthro1, anthro2, oaiTarget *LLMTarget) {
	t.Helper()
	anthro1 = &LLMTarget{ID: "tgt-anthro-1", URL: "https://anthro1.example.com", Provider: "anthropic"}
	anthro2 = &LLMTarget{ID: "tgt-anthro-2", URL: "https://anthro2.example.com", Provider: "anthropic"}
	oaiTarget = &LLMTarget{ID: "tgt-oai-1", URL: "https://oai1.example.com", Provider: "openai"}
	for _, tgt := range []*LLMTarget{anthro1, anthro2, oaiTarget} {
		if err := targetRepo.Create(tgt); err != nil {
			t.Fatalf("create target %q: %v", tgt.ID, err)
		}
	}
	return
}

func TestLLMBindingRepo_AddGroupBinding_SameProvider(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	anthro1, anthro2, _ := setupGroupBindingTargets(t, targetRepo)
	const groupID = "grp-same-provider"

	// 添加第一个绑定
	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro1: %v", err)
	}
	// 添加第二个同 provider 的绑定
	if err := repo.AddGroupBinding(anthro2.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro2: %v", err)
	}

	// 分组应有两条绑定
	ids, err := repo.FindAllForGroup(groupID)
	if err != nil {
		t.Fatalf("FindAllForGroup: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 group bindings, got %d", len(ids))
	}
}

func TestLLMBindingRepo_AddGroupBinding_CrossProvider_Rejected(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	anthro1, _, oaiTarget := setupGroupBindingTargets(t, targetRepo)
	const groupID = "grp-cross-provider"

	// 添加 anthropic 绑定
	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro1: %v", err)
	}
	// 添加 openai 绑定到同一分组 → 应返回 provider conflict 错误
	err := repo.AddGroupBinding(oaiTarget.ID, groupID)
	if err == nil {
		t.Error("expected provider conflict error, but got nil")
	} else if !strings.Contains(err.Error(), "provider conflict") {
		t.Errorf("expected 'provider conflict' in error, got: %v", err)
	}
}

func TestLLMBindingRepo_AddGroupBinding_Idempotent(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	anthro1, _, _ := setupGroupBindingTargets(t, targetRepo)
	const groupID = "grp-idempotent"

	// 添加同一绑定两次
	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("first AddGroupBinding: %v", err)
	}
	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("second AddGroupBinding (should be idempotent): %v", err)
	}

	// 仍然只有一条绑定
	ids, err := repo.FindAllForGroup(groupID)
	if err != nil {
		t.Fatalf("FindAllForGroup: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 binding after idempotent add, got %d", len(ids))
	}
}

func TestLLMBindingRepo_RemoveGroupBinding(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	anthro1, anthro2, _ := setupGroupBindingTargets(t, targetRepo)
	const groupID = "grp-remove"

	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro1: %v", err)
	}
	if err := repo.AddGroupBinding(anthro2.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro2: %v", err)
	}

	// 找到第一条绑定的 ID 并删除
	bindings, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var bindingID string
	for _, b := range bindings {
		if b.GroupID != nil && *b.GroupID == groupID && b.TargetID == anthro1.ID {
			bindingID = b.ID
			break
		}
	}
	if bindingID == "" {
		t.Fatal("could not find anthro1 binding to remove")
	}

	if err := repo.RemoveGroupBinding(bindingID); err != nil {
		t.Fatalf("RemoveGroupBinding: %v", err)
	}

	// 分组现在应只有一条绑定（anthro2）
	ids, err := repo.FindAllForGroup(groupID)
	if err != nil {
		t.Fatalf("FindAllForGroup after remove: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 binding after remove, got %d", len(ids))
	}
	if ids[0] != anthro2.ID {
		t.Errorf("remaining binding = %q, want %q", ids[0], anthro2.ID)
	}
}

func TestLLMBindingRepo_FindAllForGroup_Empty(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)

	ids, err := repo.FindAllForGroup("nonexistent-group")
	if err != nil {
		t.Fatalf("FindAllForGroup: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 bindings for unknown group, got %d", len(ids))
	}
}

func TestLLMBindingRepo_AddGroupBinding_TargetNotFound(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)

	err := repo.AddGroupBinding("nonexistent-target-id", "some-group")
	if err == nil {
		t.Error("expected error for nonexistent target, got nil")
	}
}

// TestLLMBindingRepo_FindForUser_GroupMultiBinding_FallsBack 验证分组有多绑定时
// FindForUser 仍返回第一条（兼容旧调用方），不报错
func TestLLMBindingRepo_FindForUser_GroupMultiBinding_FallsBack(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	anthro1, anthro2, _ := setupGroupBindingTargets(t, targetRepo)
	const groupID = "grp-fallback"

	if err := repo.AddGroupBinding(anthro1.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro1: %v", err)
	}
	if err := repo.AddGroupBinding(anthro2.ID, groupID); err != nil {
		t.Fatalf("AddGroupBinding anthro2: %v", err)
	}

	// FindForUser（无用户级绑定）→ 返回分组的第一条绑定（按 created_at ASC）
	tid, found, err := repo.FindForUser("", groupID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding to be found for group")
	}
	if tid != anthro1.ID {
		t.Errorf("expected anthro1 (first binding), got %q", tid)
	}
}

// TestLLMBindingRepo_SetGroup_Now_RejectsGroupBinding 验证 Set() 对分组绑定返回错误
// （分组绑定必须通过 AddGroupBinding，保证 1:N 语义）
func TestLLMBindingRepo_SetGroup_NowRejectsGroupOnlyCall(t *testing.T) {
	gormDB := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(gormDB, logger)

	groupID := "grp-set-reject"
	err := repo.Set("some-target", nil, &groupID)
	if err == nil {
		t.Error("expected error when calling Set() with userID=nil (group-only), got nil")
	}
}

// TestLLMBindingRepo_FindForUser_UserPriorityConfirmed 测试用户级绑定优先于分组级
// 验证同时有用户和分组绑定时，用户级优先
func TestLLMBindingRepo_FindForUser_UserPriorityConfirmed(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, Migrate(logger, gormDB))

	repo := NewLLMBindingRepo(gormDB, logger)
	targetRepo := NewLLMTargetRepo(gormDB, logger)

	const (
		userID  = "user-priority-test"
		groupID = "group-priority-test"
		userTarget  = "https://user-llm.example.com"
		groupTarget = "https://group-llm.example.com"
	)

	uid := userID
	gid := groupID

	// 分组绑定需要真实 target
	tgtGroup := &LLMTarget{ID: "prio-group-tgt", URL: groupTarget, Provider: "openai"}
	require.NoError(t, targetRepo.Create(tgtGroup))

	// 设置用户级绑定（1:1，无需真实 target）
	require.NoError(t, repo.Set(userTarget, &uid, nil))
	// 设置分组级绑定（1:N，通过 AddGroupBinding）
	require.NoError(t, repo.AddGroupBinding(tgtGroup.ID, gid))

	// FindForUser 时用户级绑定应优先
	tid, found, err := repo.FindForUser(userID, groupID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, userTarget, tid, "用户级绑定应优先于分组级绑定")

	// 删除用户级绑定后，分组级绑定生效
	bindings, _ := repo.List()
	for _, b := range bindings {
		if b.UserID != nil && *b.UserID == userID {
			require.NoError(t, repo.Delete(b.ID))
			break
		}
	}

	tid, found, err = repo.FindForUser(userID, gid)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, tgtGroup.ID, tid, "用户级绑定删除后，分组级绑定应生效")
}
