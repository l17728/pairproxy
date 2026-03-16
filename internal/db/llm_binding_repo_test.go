package db

import (
	"testing"

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
	if err := repo.Set("https://api.anthropic.com", &userID, nil); err != nil {
		t.Fatalf("Set: %v", err)
	}

	url, found, err := repo.FindForUser(userID, "")
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding to be found")
	}
	if url != "https://api.anthropic.com" {
		t.Errorf("url = %q, want %q", url, "https://api.anthropic.com")
	}
}

func TestLLMBindingRepo_SetGroup(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	groupID := "group-1"
	if err := repo.Set("https://api.openai.com", nil, &groupID); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// user ID 不匹配 → 走分组绑定
	url, found, err := repo.FindForUser("", groupID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected group binding to be found")
	}
	if url != "https://api.openai.com" {
		t.Errorf("url = %q, want %q", url, "https://api.openai.com")
	}
}

func TestLLMBindingRepo_UserPriorityOverGroup(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userID := "user-2"
	groupID := "group-2"

	// 分组绑定 A
	if err := repo.Set("https://api.openai.com", nil, &groupID); err != nil {
		t.Fatalf("Set group: %v", err)
	}
	// 用户绑定 B（应优先）
	if err := repo.Set("https://api.anthropic.com", &userID, nil); err != nil {
		t.Fatalf("Set user: %v", err)
	}

	url, found, err := repo.FindForUser(userID, groupID)
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding found")
	}
	if url != "https://api.anthropic.com" {
		t.Errorf("expected user-level binding (anthropic), got %q", url)
	}
}

func TestLLMBindingRepo_SetReplace(t *testing.T) {
	db := openTestDB(t)
	logger := zaptest.NewLogger(t)
	repo := NewLLMBindingRepo(db, logger)

	userID := "user-3"
	if err := repo.Set("https://api.anthropic.com", &userID, nil); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := repo.Set("https://api.openai.com", &userID, nil); err != nil {
		t.Fatalf("Set second: %v", err)
	}

	url, found, err := repo.FindForUser(userID, "")
	if err != nil {
		t.Fatalf("FindForUser: %v", err)
	}
	if !found {
		t.Fatal("expected binding found after replace")
	}
	if url != "https://api.openai.com" {
		t.Errorf("expected openai after replace, got %q", url)
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
	if err := repo.Set("https://api.anthropic.com", &userID, nil); err != nil {
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

	targets := []string{"https://api.anthropic.com", "https://api.openai.com"}
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
	targets := []string{"https://a.com", "https://b.com", "https://c.com"}

	if err := repo.EvenDistribute(userIDs, targets); err != nil {
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
		counts[b.TargetURL]++
	}
	for _, tgt := range targets {
		if counts[tgt] != 2 {
			t.Errorf("target %q: expected 2 users, got %d", tgt, counts[tgt])
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

	targets := []string{"https://a.com", "https://b.com"}

	// u1 已有绑定（模拟直连用户手动设置）
	u1 := "u1"
	if err := repo.Set("https://fixed.com", &u1, nil); err != nil {
		t.Fatalf("Set u1: %v", err)
	}

	// u2, u3 无绑定，应被 distribute 分配
	userIDs := []string{"u1", "u2", "u3"}
	if err := repo.EvenDistribute(userIDs, targets); err != nil {
		t.Fatalf("EvenDistribute: %v", err)
	}

	// u1 的绑定必须保持不变
	url, found, err := repo.FindForUser("u1", "")
	if err != nil {
		t.Fatalf("FindForUser u1: %v", err)
	}
	if !found {
		t.Fatal("u1 binding should still exist after distribute")
	}
	if url != "https://fixed.com" {
		t.Errorf("u1 binding = %q, want https://fixed.com (distribute must not overwrite existing bindings)", url)
	}

	// u2, u3 应被分配到 targets 中
	for _, uid := range []string{"u2", "u3"} {
		url, found, err := repo.FindForUser(uid, "")
		if err != nil {
			t.Fatalf("FindForUser %s: %v", uid, err)
		}
		if !found {
			t.Errorf("%s should have been assigned a binding by distribute", uid)
			continue
		}
		if url != "https://a.com" && url != "https://b.com" {
			t.Errorf("%s binding = %q, want one of the distribute targets", uid, url)
		}
	}
}
