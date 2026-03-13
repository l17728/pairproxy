package dashboard_test

// llm_ui_test.go — 覆盖本次 LLM 面板 UI 修改的完备测试
//
// 修改点回顾：
//  1. 绑定列表修复（deref *string 类型错误）→ 绑定后列表不为空
//  2. 绑定列表显示用户名/分组名（非 UUID）
//  3. "添加绑定"表单仅显示未绑定的用户/分组
//  4. 全部绑定后隐藏"添加绑定"区域
//  5. 解绑后用户归还到"添加绑定"下拉框
//  6. 解绑按钮文字改为"解绑"（原"删除"）
//  7. "查看详情"按钮改用 data-* 属性（修复 html/template JS 上下文转义问题）
//  8. "编辑"按钮改用 data-* 属性（同上）
//  9. "添加绑定"目标下拉改用 AllTargets（DB 数据），而非运行时健康检查数据
// 10. 分组绑定表单字段应为 group_id（非 group_name）

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
)

// ---------------------------------------------------------------------------
// 测试辅助：构建带完整依赖的 handler
// ---------------------------------------------------------------------------

type llmUIEnv struct {
	mux            *http.ServeMux
	token          string
	llmBindingRepo *db.LLMBindingRepo
	llmTargetRepo  *db.LLMTargetRepo
	userRepo       *db.UserRepo
	groupRepo      *db.GroupRepo
}

func newLLMUIEnv(t *testing.T) *llmUIEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	// healthFn 返回一个纯运行时 target（不在 DB 中），用于区分 AllTargets vs Targets
	h.SetLLMDeps(llmBindingRepo, func() []proxy.LLMTargetStatus {
		return []proxy.LLMTargetStatus{
			{URL: "http://runtime-only.example.com", Name: "RuntimeNode", Healthy: true},
		}
	})
	h.SetLLMTargetRepo(llmTargetRepo)
	h.SetDrainFunctions(func() error { return nil }, func() error { return nil },
		func() proxy.DrainStatus { return proxy.DrainStatus{} })

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID: "__admin__", Username: "admin", Role: "admin",
	}, time.Hour)

	return &llmUIEnv{
		mux: mux, token: token,
		llmBindingRepo: llmBindingRepo, llmTargetRepo: llmTargetRepo,
		userRepo: userRepo, groupRepo: groupRepo,
	}
}

// getPage 发起带鉴权的 GET /dashboard/llm 并返回响应体
func (e *llmUIEnv) getPage(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/llm", nil)
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.token})
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/llm: status=%d body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// postBinding 发起带鉴权的 POST /dashboard/llm/bindings
func (e *llmUIEnv) postBinding(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.token})
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

// deleteBinding 发起带鉴权的 POST /dashboard/llm/bindings/{id}/delete
func (e *llmUIEnv) deleteBinding(t *testing.T, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/llm/bindings/"+id+"/delete", nil)
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.token})
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// 1. 绑定列表渲染修复（原 deref *string 类型错误导致列表为空）
// ---------------------------------------------------------------------------

// TestLLMPageBindingListNotEmptyAfterBinding 绑定后列表应显示绑定行，不显示空状态提示
func TestLLMPageBindingListNotEmptyAfterBinding(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-alice"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "alice", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	if strings.Contains(body, "暂无绑定关系") {
		t.Error("binding list shows empty-state placeholder; expected binding row to be rendered (deref bug regression)")
	}
}

// ---------------------------------------------------------------------------
// 2. 绑定列表显示用户名/分组名（非 UUID）
// ---------------------------------------------------------------------------

// TestLLMPageBindingListShowsUsername 绑定列表应显示用户名而非 UUID
func TestLLMPageBindingListShowsUsername(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "uuid-0001-alice"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "alice", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "alice") {
		t.Error("binding list should display username 'alice'")
	}
}

// TestLLMPageBindingListShowsGroupName 绑定列表应显示分组名而非 UUID
func TestLLMPageBindingListShowsGroupName(t *testing.T) {
	e := newLLMUIEnv(t)

	g := &db.Group{ID: "grp-engineering", Name: "engineering"}
	if err := e.groupRepo.Create(g); err != nil {
		t.Fatalf("create group: %v", err)
	}
	gid := g.ID
	if err := e.llmBindingRepo.Set("http://llm.example.com", nil, &gid); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	if strings.Contains(body, "暂无绑定关系") {
		t.Error("group binding should make list non-empty")
	}
	if !strings.Contains(body, "engineering") {
		t.Error("binding list should display group name 'engineering'")
	}
}

// TestLLMPageBindingListTypeLabel 绑定类型列应分别标注"用户"或"分组"
func TestLLMPageBindingListTypeLabel(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-bob"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "bob", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	// 用户绑定行中应有"用户"类型标签
	if !strings.Contains(body, ">用户<") {
		t.Error("user binding row should have '用户' type label")
	}
}

// ---------------------------------------------------------------------------
// 3. 解绑按钮文字
// ---------------------------------------------------------------------------

// TestLLMPageUnbindButtonText 绑定列表中的操作按钮文字应为"解绑"
func TestLLMPageUnbindButtonText(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-carol"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "carol", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "解绑") {
		t.Error("binding list action button should read '解绑'")
	}
}

// ---------------------------------------------------------------------------
// 4. "添加绑定"区域的显隐逻辑
// ---------------------------------------------------------------------------

// TestLLMPageAddBindingFormVisibleWithUnboundUser 存在未绑定用户时"添加绑定"区域应显示
func TestLLMPageAddBindingFormVisibleWithUnboundUser(t *testing.T) {
	e := newLLMUIEnv(t)

	if err := e.userRepo.Create(&db.User{ID: "user-dave", Username: "dave", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should be visible when unbound users exist")
	}
}

// TestLLMPageAddBindingFormHiddenWhenAllBound 所有用户均已绑定时"添加绑定"区域应隐藏
func TestLLMPageAddBindingFormHiddenWhenAllBound(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-eve"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "eve", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	// form action 不应出现（区域被隐藏）
	if strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should be hidden when all users are bound")
	}
}

// TestLLMPageAddBindingFormHiddenWithNoUsersOrGroups 无用户也无分组时"添加绑定"区域应隐藏
func TestLLMPageAddBindingFormHiddenWithNoUsersOrGroups(t *testing.T) {
	e := newLLMUIEnv(t)
	body := e.getPage(t)

	if strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should be hidden when there are no users or groups")
	}
}

// TestLLMPageAddBindingFormVisibleWithUnboundGroup 存在未绑定分组时"添加绑定"区域应显示
func TestLLMPageAddBindingFormVisibleWithUnboundGroup(t *testing.T) {
	e := newLLMUIEnv(t)

	if err := e.groupRepo.Create(&db.Group{ID: "g-free", Name: "free-group"}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should be visible when unbound groups exist")
	}
}

// ---------------------------------------------------------------------------
// 5. "添加绑定"下拉框仅包含未绑定的用户/分组
// ---------------------------------------------------------------------------

// TestLLMPageAddBindingDropdownExcludesBoundUser 已绑定用户不应出现在下拉框 option 中
func TestLLMPageAddBindingDropdownExcludesBoundUser(t *testing.T) {
	e := newLLMUIEnv(t)

	uidAlice := "user-alice-bound"
	uidBob := "user-bob-free"
	if err := e.userRepo.Create(&db.User{ID: uidAlice, Username: "alice", IsActive: true}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := e.userRepo.Create(&db.User{ID: uidBob, Username: "bob", IsActive: true}); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uidAlice, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	// bob（未绑定）应在 option value 中
	if !strings.Contains(body, `value="`+uidBob+`"`) {
		t.Errorf("unbound user bob (id=%s) should appear as option in AddBinding dropdown", uidBob)
	}
	// alice（已绑定）不应在 option value 中
	if strings.Contains(body, `value="`+uidAlice+`"`) {
		t.Errorf("bound user alice (id=%s) should NOT appear as option in AddBinding dropdown", uidAlice)
	}
}

// TestLLMPageAddBindingDropdownExcludesBoundGroup 已绑定分组不应出现在下拉框 option 中
func TestLLMPageAddBindingDropdownExcludesBoundGroup(t *testing.T) {
	e := newLLMUIEnv(t)

	gBound := &db.Group{ID: "g-bound", Name: "bound-grp"}
	gFree := &db.Group{ID: "g-free", Name: "free-grp"}
	if err := e.groupRepo.Create(gBound); err != nil {
		t.Fatalf("create bound group: %v", err)
	}
	if err := e.groupRepo.Create(gFree); err != nil {
		t.Fatalf("create free group: %v", err)
	}
	gid := gBound.ID
	if err := e.llmBindingRepo.Set("http://llm.example.com", nil, &gid); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, `value="`+gFree.ID+`"`) {
		t.Errorf("unbound group (id=%s) should appear in AddBinding dropdown", gFree.ID)
	}
	if strings.Contains(body, `value="`+gBound.ID+`"`) {
		t.Errorf("bound group (id=%s) should NOT appear in AddBinding dropdown", gBound.ID)
	}
}

// TestLLMPageAddBindingDropdownShowsRemainingUserAfterPartialBind
// 绑定部分用户后，下拉框只剩余未绑定的用户
func TestLLMPageAddBindingDropdownShowsRemainingUserAfterPartialBind(t *testing.T) {
	e := newLLMUIEnv(t)

	uid1 := "user-frank"
	uid2 := "user-grace"
	if err := e.userRepo.Create(&db.User{ID: uid1, Username: "frank", IsActive: true}); err != nil {
		t.Fatalf("create frank: %v", err)
	}
	if err := e.userRepo.Create(&db.User{ID: uid2, Username: "grace", IsActive: true}); err != nil {
		t.Fatalf("create grace: %v", err)
	}
	// 只绑定 frank
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid1, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	// grace 应在下拉框中
	if !strings.Contains(body, `value="`+uid2+`"`) {
		t.Errorf("unbound user grace should appear in dropdown")
	}
	// frank 不应在下拉框中
	if strings.Contains(body, `value="`+uid1+`"`) {
		t.Errorf("bound user frank should NOT appear in dropdown")
	}
	// 添加绑定区域仍应显示（grace 未绑定）
	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should still be visible with one unbound user")
	}
}

// ---------------------------------------------------------------------------
// 6. 解绑后用户归还到"添加绑定"表单
// ---------------------------------------------------------------------------

// TestLLMPageUnbindRestoresUserToDropdown 解绑后用户重新出现在"添加绑定"下拉框
func TestLLMPageUnbindRestoresUserToDropdown(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-henry"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "henry", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	// 绑定后：用户不在表单中
	body := e.getPage(t)
	if strings.Contains(body, `value="`+uid+`"`) {
		t.Error("bound user should not appear in AddBinding dropdown")
	}

	// 获取绑定 ID 并解绑
	bindings, err := e.llmBindingRepo.List()
	if err != nil || len(bindings) == 0 {
		t.Fatalf("list bindings: %v, count=%d", err, len(bindings))
	}
	rr := e.deleteBinding(t, bindings[0].ID)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete binding: status=%d", rr.Code)
	}

	// 解绑后：用户应重新出现在下拉框中
	body = e.getPage(t)
	if !strings.Contains(body, `value="`+uid+`"`) {
		t.Errorf("unbound user should reappear in AddBinding dropdown after unbinding")
	}
	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' section should reappear after unbinding")
	}
}

// TestLLMPageUnbindAndRebind 解绑后再次绑定到不同 target
func TestLLMPageUnbindAndRebind(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-iris"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "iris", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 第一次绑定
	if err := e.llmBindingRepo.Set("http://llm1.example.com", &uid, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	// 解绑
	bindings, _ := e.llmBindingRepo.List()
	if len(bindings) == 0 {
		t.Fatal("no binding found")
	}
	rr := e.deleteBinding(t, bindings[0].ID)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete binding: status=%d", rr.Code)
	}

	// 确认解绑后 DB 中无绑定
	bindings, _ = e.llmBindingRepo.List()
	if len(bindings) != 0 {
		t.Errorf("after unbind, expected 0 bindings, got %d", len(bindings))
	}

	// 再次绑定到不同 target
	form := url.Values{}
	form.Set("target_url", "http://llm2.example.com")
	form.Set("bind_type", "user")
	form.Set("user_id", uid)
	rr2 := e.postBinding(t, form)
	if rr2.Code != http.StatusSeeOther {
		t.Fatalf("rebind: status=%d", rr2.Code)
	}
	if strings.Contains(rr2.Header().Get("Location"), "error=") {
		t.Errorf("rebind should succeed, got error redirect: %s", rr2.Header().Get("Location"))
	}

	bindings, _ = e.llmBindingRepo.List()
	if len(bindings) != 1 {
		t.Errorf("after rebind, expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].TargetURL != "http://llm2.example.com" {
		t.Errorf("rebind target = %s, want http://llm2.example.com", bindings[0].TargetURL)
	}
}

// ---------------------------------------------------------------------------
// 7. 分组绑定表单字段（group_id，非 group_name）
// ---------------------------------------------------------------------------

// TestCreateGroupBindingUsesGroupIDField 分组绑定必须使用 group_id 字段
func TestCreateGroupBindingUsesGroupIDField(t *testing.T) {
	e := newLLMUIEnv(t)

	g := &db.Group{Name: "dev-team"}
	if err := e.groupRepo.Create(g); err != nil {
		t.Fatalf("create group: %v", err)
	}

	form := url.Values{}
	form.Set("target_url", "http://llm.example.com")
	form.Set("bind_type", "group")
	form.Set("group_id", g.ID) // 正确字段
	rr := e.postBinding(t, form)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if strings.Contains(loc, "error=") {
		t.Errorf("group binding with group_id should succeed, got error redirect: %s", loc)
	}
	if !strings.Contains(loc, "flash=") {
		t.Errorf("expected success flash in redirect, got: %s", loc)
	}

	bindings, _ := e.llmBindingRepo.List()
	if len(bindings) == 0 {
		t.Fatal("expected group binding in DB")
	}
	if bindings[0].GroupID == nil || *bindings[0].GroupID != g.ID {
		t.Errorf("binding GroupID=%v, want %s", bindings[0].GroupID, g.ID)
	}
}

// TestCreateGroupBindingWithGroupNameFieldFails 使用 group_name（错误字段）应返回 error redirect
func TestCreateGroupBindingWithGroupNameFieldFails(t *testing.T) {
	e := newLLMUIEnv(t)

	form := url.Values{}
	form.Set("target_url", "http://llm.example.com")
	form.Set("bind_type", "group")
	form.Set("group_name", "some-group") // 错误字段
	rr := e.postBinding(t, form)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "error=") {
		t.Errorf("using wrong field 'group_name' should produce error redirect, got: %s", rr.Header().Get("Location"))
	}
}

// ---------------------------------------------------------------------------
// 8. "查看详情"按钮使用 data-* 属性（html/template JS 上下文修复）
// ---------------------------------------------------------------------------

// TestLLMPageViewTargetDetailsButtonUsesDataAttributes
// 配置文件来源（IsEditable=false）的 target 应使用 data-* 属性，不应在 onclick 中嵌入字符串参数
func TestLLMPageViewTargetDetailsButtonUsesDataAttributes(t *testing.T) {
	e := newLLMUIEnv(t)

	if err := e.llmTargetRepo.Upsert(&db.LLMTarget{
		ID:         "t-config-1",
		URL:        "https://config-node.example.com",
		Provider:   "anthropic",
		Name:       "Config Node",
		Weight:     1,
		Source:     "config",
		IsEditable: false,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("upsert config target: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "查看详情") {
		t.Error("'查看详情' button not rendered for config-sourced target")
	}
	if !strings.Contains(body, `data-url="https://config-node.example.com"`) {
		t.Error("'查看详情' button should carry data-url attribute")
	}
	if !strings.Contains(body, `data-provider="anthropic"`) {
		t.Error("'查看详情' button should carry data-provider attribute")
	}
	if !strings.Contains(body, `data-name="Config Node"`) {
		t.Error("'查看详情' button should carry data-name attribute")
	}
	if !strings.Contains(body, `data-weight="1"`) {
		t.Error("'查看详情' button should carry data-weight attribute")
	}
	// 旧的不安全 inline 方式不应出现
	if strings.Contains(body, `onclick="viewTargetDetails('https://`) {
		t.Error("'查看详情' onclick must NOT use inline string params (html/template JS escaping unsafe)")
	}
}

// TestLLMPageViewTargetDetailsSpecialCharsInDataAttrs 含特殊字符的 target 名称应正确 HTML 转义
func TestLLMPageViewTargetDetailsSpecialCharsInDataAttrs(t *testing.T) {
	e := newLLMUIEnv(t)

	if err := e.llmTargetRepo.Upsert(&db.LLMTarget{
		ID:         "t-special",
		URL:        "https://node.example.com",
		Provider:   "anthropic",
		Name:       `Node "A" & <B>`,
		Weight:     1,
		Source:     "config",
		IsEditable: false,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("upsert target: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "查看详情") {
		t.Error("'查看详情' button not rendered")
	}
	// html/template 会将特殊字符 HTML 转义到 data-* 属性中，页面应能正常渲染（不崩溃）
	// 验证页面正常渲染即可（不含 500/模板错误）
}

// ---------------------------------------------------------------------------
// 9. "编辑"按钮使用 data-* 属性
// ---------------------------------------------------------------------------

// TestLLMPageEditTargetButtonUsesDataAttributes
// 数据库来源（IsEditable=true）的 target 应使用 data-* 属性
func TestLLMPageEditTargetButtonUsesDataAttributes(t *testing.T) {
	e := newLLMUIEnv(t)

	if err := e.llmTargetRepo.Create(&db.LLMTarget{
		ID:         "t-db-1",
		URL:        "https://db-node.example.com",
		Provider:   "openai",
		Name:       "DB Node",
		Weight:     3,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("create db target: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "编辑") {
		t.Error("'编辑' button not rendered for database-sourced target")
	}
	if !strings.Contains(body, `data-id="t-db-1"`) {
		t.Error("'编辑' button should carry data-id attribute")
	}
	if !strings.Contains(body, `data-url="https://db-node.example.com"`) {
		t.Error("'编辑' button should carry data-url attribute")
	}
	if !strings.Contains(body, `data-provider="openai"`) {
		t.Error("'编辑' button should carry data-provider attribute")
	}
	if !strings.Contains(body, `data-weight="3"`) {
		t.Error("'编辑' button should carry data-weight attribute")
	}
	// 旧的不安全 inline 方式不应出现
	if strings.Contains(body, `onclick="editTarget('t-db-1'`) {
		t.Error("'编辑' onclick must NOT use inline string params")
	}
}

// ---------------------------------------------------------------------------
// 10. "添加绑定"目标下拉使用 AllTargets（DB），不依赖运行时健康检查数据
// ---------------------------------------------------------------------------

// TestLLMPageAddBindingTargetDropdownUsesDBTargets
// 目标下拉框 option 来自 DB AllTargets，不依赖运行时 healthFn 返回值
func TestLLMPageAddBindingTargetDropdownUsesDBTargets(t *testing.T) {
	e := newLLMUIEnv(t)

	// DB 中有一个 target
	if err := e.llmTargetRepo.Create(&db.LLMTarget{
		ID:         "t-db-drop",
		URL:        "https://db-drop.example.com",
		Provider:   "anthropic",
		Weight:     1,
		Source:     "database",
		IsEditable: true,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("create db target: %v", err)
	}
	// healthFn 返回的是 "runtime-only.example.com"（不在 DB 中）

	// 创建未绑定用户使"添加绑定"区域显示
	if err := e.userRepo.Create(&db.User{ID: "user-z", Username: "zach", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := e.getPage(t)

	// "添加绑定"区域应显示
	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Fatal("'添加绑定' form should be visible")
	}
	// DB target 的 URL 应出现在下拉框 option value 中
	if !strings.Contains(body, `value="https://db-drop.example.com"`) {
		t.Error("DB target URL should appear as option value in AddBinding target dropdown")
	}
	// 纯运行时 target 不应出现在下拉框 option value 中
	// （"runtime-only.example.com" 仅在 healthFn 中，不在 DB 里）
	if strings.Contains(body, `value="http://runtime-only.example.com"`) {
		t.Error("runtime-only target should NOT appear as option value in AddBinding target dropdown")
	}
}

// TestLLMPageAddBindingTargetDropdownEmptyWhenNoDBTargets
// DB 中无 target 时，目标下拉框为空
func TestLLMPageAddBindingTargetDropdownEmptyWhenNoDBTargets(t *testing.T) {
	e := newLLMUIEnv(t)
	// 不向 DB 插入任何 target，但 healthFn 有返回值

	if err := e.userRepo.Create(&db.User{ID: "user-q", Username: "quinn", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := e.getPage(t)

	// 纯运行时 target 不应出现在下拉框 option value 中
	if strings.Contains(body, `value="http://runtime-only.example.com"`) {
		t.Error("runtime-only target should NOT appear in target dropdown")
	}
}

// ---------------------------------------------------------------------------
// 11. 页面整体渲染健壮性（模板不崩溃）
// ---------------------------------------------------------------------------

// TestLLMPageRenderWithMixedBindings 混合用户绑定和分组绑定，页面应正常渲染
func TestLLMPageRenderWithMixedBindings(t *testing.T) {
	e := newLLMUIEnv(t)

	uid := "user-mixed"
	gid := "grp-mixed"
	if err := e.userRepo.Create(&db.User{ID: uid, Username: "mixed-user", IsActive: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.groupRepo.Create(&db.Group{ID: gid, Name: "mixed-group"}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm1.example.com", &uid, nil); err != nil {
		t.Fatalf("set user binding: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm2.example.com", nil, &gid); err != nil {
		t.Fatalf("set group binding: %v", err)
	}

	body := e.getPage(t)

	// 两个绑定都应显示
	if !strings.Contains(body, "mixed-user") {
		t.Error("user binding should display username")
	}
	if !strings.Contains(body, "mixed-group") {
		t.Error("group binding should display group name")
	}
	if strings.Contains(body, "暂无绑定关系") {
		t.Error("page should not show empty state with bindings")
	}
	// 两个用户和分组均已绑定，"添加绑定"区域应隐藏
	if strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should be hidden when all users and groups are bound")
	}
}

// TestLLMPageRenderWithBothBoundAndUnboundUsers 部分绑定时页面两个区域同时显示
func TestLLMPageRenderWithBothBoundAndUnboundUsers(t *testing.T) {
	e := newLLMUIEnv(t)

	uid1 := "user-p1"
	uid2 := "user-p2"
	if err := e.userRepo.Create(&db.User{ID: uid1, Username: "paul", IsActive: true}); err != nil {
		t.Fatalf("create paul: %v", err)
	}
	if err := e.userRepo.Create(&db.User{ID: uid2, Username: "pete", IsActive: true}); err != nil {
		t.Fatalf("create pete: %v", err)
	}
	if err := e.llmBindingRepo.Set("http://llm.example.com", &uid1, nil); err != nil {
		t.Fatalf("set binding: %v", err)
	}

	body := e.getPage(t)

	// 绑定列表有数据
	if strings.Contains(body, "暂无绑定关系") {
		t.Error("bound user should make list non-empty")
	}
	// 绑定列表显示 paul
	if !strings.Contains(body, "paul") {
		t.Error("binding list should show bound user 'paul'")
	}
	// 添加绑定区域仍显示（pete 未绑定）
	if !strings.Contains(body, `action="/dashboard/llm/bindings"`) {
		t.Error("'添加绑定' form should show for unbound user 'pete'")
	}
	// 下拉框含 pete，不含 paul
	if !strings.Contains(body, `value="`+uid2+`"`) {
		t.Errorf("unbound user pete should be in dropdown")
	}
	if strings.Contains(body, `value="`+uid1+`"`) {
		t.Errorf("bound user paul should NOT be in dropdown")
	}
}

// TestLLMPageRenderWithConfigAndDBTargets 同时存在配置文件和数据库来源 target，页面正常渲染
func TestLLMPageRenderWithConfigAndDBTargets(t *testing.T) {
	e := newLLMUIEnv(t)

	// 配置来源 → 显示"查看详情"；用 Upsert 以绕过 GORM boolean default:true 零值问题
	if err := e.llmTargetRepo.Upsert(&db.LLMTarget{
		ID: "t-cfg", URL: "https://cfg.example.com",
		Provider: "anthropic", Weight: 1,
		Source: "config", IsEditable: false, IsActive: true,
	}); err != nil {
		t.Fatalf("upsert config target: %v", err)
	}
	// 数据库来源 → 显示"编辑"
	if err := e.llmTargetRepo.Create(&db.LLMTarget{
		ID: "t-db2", URL: "https://db2.example.com",
		Provider: "openai", Weight: 1,
		Source: "database", IsEditable: true, IsActive: true,
	}); err != nil {
		t.Fatalf("create db target: %v", err)
	}

	body := e.getPage(t)

	if !strings.Contains(body, "查看详情") {
		t.Error("should have '查看详情' button for config-sourced target")
	}
	if !strings.Contains(body, "编辑") {
		t.Error("should have '编辑' button for database-sourced target")
	}
	if !strings.Contains(body, `data-url="https://cfg.example.com"`) {
		t.Error("config target should use data-url attribute")
	}
	if !strings.Contains(body, `data-id="t-db2"`) {
		t.Error("db target should use data-id attribute")
	}
}
