package dashboard_test

// handler_html_test.go
//
// HTML 结构回归测试：验证 users.html 操作列布局和 layout.html 导航高亮功能。
//
// 背景：
//   - users.html 操作列从 flex-wrap 改为 flex-col，确保每个操作独占一行。
//   - layout.html 新增 data-nav 属性和内联 <script>，实现当前页导航高亮。
//
// 测试策略：使用 strings.Contains / strings.Index 对渲染后的 HTML 做字符串
// 匹配断言，与 handler_f10_test.go 的 TestOverviewChartContainerFix 保持一致。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// usersPageWithUser 创建一个用户（通过 HTTP POST）后，GET /dashboard/users
// 返回包含该用户操作列的完整 HTML body。
// 操作列只有在 .Users 非空时才被 template 渲染，必须先创建用户。
func usersPageWithUser(t *testing.T) string {
	t.Helper()
	env := newDashEnv(t)

	// 创建一个测试用户
	form := url.Values{}
	form.Set("username", "html-test-user")
	form.Set("password", "testpass123")
	postReq := httptest.NewRequest(http.MethodPost, "/dashboard/users", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(env.adminCookie(t))
	postRR := httptest.NewRecorder()
	env.mux.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusFound {
		t.Fatalf("user creation returned %d, want 302", postRR.Code)
	}

	// GET 用户列表页
	getReq := httptest.NewRequest(http.MethodGet, "/dashboard/users", nil)
	getReq.AddCookie(env.adminCookie(t))
	getRR := httptest.NewRecorder()
	env.mux.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("users page returned %d, want 200", getRR.Code)
	}
	return getRR.Body.String()
}

// ---------------------------------------------------------------------------
// users.html — 操作列布局回归测试
// ---------------------------------------------------------------------------

// TestUsersPage_ActionColumnLayout 验证操作列使用竖排布局（flex-col），
// 而不是会导致混排的 flex-wrap。
func TestUsersPage_ActionColumnLayout(t *testing.T) {
	body := usersPageWithUser(t)

	// 必须包含竖排布局
	if !strings.Contains(body, "flex-col") {
		t.Error("action column should use flex-col (vertical layout), flex-col class not found")
	}
}

// TestUsersPage_ActionColumnFlexWrapAbsent 验证操作列容器内没有 flex-wrap，
// flex-wrap 会导致多个操作在同一行混排。
// 注意：顶部"添加用户"表单用了 flex-wrap，属于正常；
// 操作列容器（在 <tbody> 里）不应出现 flex-wrap。
func TestUsersPage_ActionColumnFlexWrapAbsent(t *testing.T) {
	body := usersPageWithUser(t)

	// 在 <tbody> 区域里定位，检查操作列 div 不含 flex-wrap
	tbodyStart := strings.Index(body, `id="userStatsBody"`)
	if tbodyStart == -1 {
		t.Fatal("<tbody id='userStatsBody'> not found in HTML")
	}
	tbodySection := body[tbodyStart:]

	if strings.Contains(tbodySection, "flex-wrap") {
		t.Error("action column inside tbody must NOT use flex-wrap — it causes actions to mix on the same line")
	}
}

// TestUsersPage_ActionColumnWhitespaceNowrap 验证操作列按钮带有 whitespace-nowrap，
// 防止按钮文字在小屏幕上被截断。
func TestUsersPage_ActionColumnWhitespaceNowrap(t *testing.T) {
	body := usersPageWithUser(t)

	if !strings.Contains(body, "whitespace-nowrap") {
		t.Error("action column buttons should have whitespace-nowrap to prevent mid-word wrapping")
	}
}

// TestUsersPage_ActionColumnMinWidth 验证操作列表头有最小宽度样式，
// 防止列被过度压缩。
func TestUsersPage_ActionColumnMinWidth(t *testing.T) {
	body := usersPageWithUser(t)

	if !strings.Contains(body, "min-width") {
		t.Error("action column <th> should have min-width style to prevent column from being crushed")
	}
}

// TestUsersPage_TableMinWMax 验证表格整体设置了 min-w-max，
// 使得宽度不足时优先横向滚动而不是压缩列。
func TestUsersPage_TableMinWMax(t *testing.T) {
	body := usersPageWithUser(t)

	if !strings.Contains(body, "min-w-max") {
		t.Error("users table should have min-w-max class to enable horizontal scrolling instead of column compression")
	}
}

// TestUsersPage_ActionButtonLabels 验证操作列关键按钮文字存在于页面中。
// 特别是 "重置密码"（旧版为 "重置"，截断了语义）。
func TestUsersPage_ActionButtonLabels(t *testing.T) {
	body := usersPageWithUser(t)

	expectedLabels := []string{"重置密码", "改组", "吊销Token"}
	for _, label := range expectedLabels {
		if !strings.Contains(body, label) {
			t.Errorf("action column missing button label %q", label)
		}
	}
}

// TestUsersPage_ActionColumnNoOldResetLabel 验证旧版截断的按钮文字 "重置" 已被替换，
// 操作列里不能有仅含"重置"而非"重置密码"的按钮。
func TestUsersPage_ActionColumnNoOldResetLabel(t *testing.T) {
	body := usersPageWithUser(t)

	// 旧版精确写法：'>重置<' 紧跟关闭标签，用来区分 "重置密码" 里的"重置"前缀
	oldResetButton := ">重置<"
	if strings.Contains(body, oldResetButton) {
		t.Error("found old truncated button label '>重置<' — should be '重置密码' for clarity")
	}
}

// TestUsersPage_TableHeadersWhitespaceNowrap 验证表头单元格有 whitespace-nowrap，
// 防止列标题换行。
func TestUsersPage_TableHeadersWhitespaceNowrap(t *testing.T) {
	// 表头即使无用户数据也会渲染，用不含用户的空页面测试
	env := newDashEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/users", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("users page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	// 找到表头区域（<thead>...）
	theadStart := strings.Index(body, "<thead")
	theadEnd := strings.Index(body, "</thead>")
	if theadStart == -1 || theadEnd == -1 {
		t.Fatal("table <thead> section not found in HTML")
	}
	thead := body[theadStart:theadEnd]

	if !strings.Contains(thead, "whitespace-nowrap") {
		t.Error("table header cells should have whitespace-nowrap to prevent header text from wrapping")
	}
}

// ---------------------------------------------------------------------------
// layout.html — 导航高亮回归测试
// ---------------------------------------------------------------------------

// TestNavLayout_DataNavAttribute 验证每个导航链接都带有 data-nav 属性，
// 供 JS 高亮脚本的 querySelectorAll('[data-nav]') 选择器使用。
func TestNavLayout_DataNavAttribute(t *testing.T) {
	env := newDashEnv(t)

	// 任意已认证页面都使用相同 layout，用 overview 测试
	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("overview page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	if !strings.Contains(body, `data-nav`) {
		t.Error("nav links should have data-nav attribute for JS active-state detection")
	}

	// 验证所有 7 个导航项都带有 data-nav
	count := strings.Count(body, `data-nav`)
	if count < 7 {
		t.Errorf("expected at least 7 data-nav attributes (one per nav link), got %d", count)
	}
}

// TestNavLayout_ActiveStateScript 验证 layout.html 包含导航高亮内联脚本，
// 且脚本读取 window.location.pathname 来决定高亮哪个链接。
func TestNavLayout_ActiveStateScript(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/users", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("users page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	if !strings.Contains(body, "window.location.pathname") {
		t.Error("layout should contain nav active-state script using window.location.pathname")
	}

	// 验证脚本会应用高亮样式
	if !strings.Contains(body, "bg-indigo-800") {
		t.Error("nav active-state script should apply bg-indigo-800 class to active link")
	}
	if !strings.Contains(body, "font-semibold") {
		t.Error("nav active-state script should apply font-semibold class to active link")
	}
}

// TestNavLayout_AllNavLinksPresent 验证导航栏包含全部 7 个页面入口链接。
func TestNavLayout_AllNavLinksPresent(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("overview page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	navLinks := []string{
		`href="/dashboard/overview"`,
		`href="/dashboard/users"`,
		`href="/dashboard/groups"`,
		`href="/dashboard/logs"`,
		`href="/dashboard/audit"`,
		`href="/dashboard/llm"`,
		`href="/dashboard/my-usage"`,
	}
	for _, link := range navLinks {
		if !strings.Contains(body, link) {
			t.Errorf("nav bar missing link %q", link)
		}
	}
}

// TestNavLayout_ScriptPositionAfterNav 验证高亮脚本出现在 </nav> 之后、
// <main> 之前，确保 DOM 已就绪时能正确选中导航链接。
func TestNavLayout_ScriptPositionAfterNav(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/logs", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("logs page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	navCloseIdx := strings.Index(body, "</nav>")
	mainOpenIdx := strings.Index(body, "<main")
	scriptIdx := strings.Index(body, "window.location.pathname")

	if navCloseIdx == -1 {
		t.Fatal("</nav> tag not found in rendered HTML")
	}
	if mainOpenIdx == -1 {
		t.Fatal("<main tag not found in rendered HTML")
	}
	if scriptIdx == -1 {
		t.Fatal("window.location.pathname script not found in rendered HTML")
	}

	// 脚本必须在 </nav> 之后
	if scriptIdx < navCloseIdx {
		t.Error("nav active-state script should appear AFTER </nav>, but found before it")
	}
	// 脚本必须在 <main> 之前
	if scriptIdx > mainOpenIdx {
		t.Error("nav active-state script should appear BEFORE <main>, but found after it")
	}
}

// ---------------------------------------------------------------------------
// my-usage.html — 图表容器高度修复回归测试
// ---------------------------------------------------------------------------

// myUsagePage 返回 /dashboard/my-usage 的完整 HTML body（管理员视角）。
func myUsagePage(t *testing.T) string {
	t.Helper()
	env := newDashEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/my-usage", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("my-usage page status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}

// TestMyUsagePage_ChartCanvasHasWrapperDiv 验证用量历史图表的 <canvas> 被包在
// 带有固定高度的 <div style="position: relative; height: 200px;"> 容器内。
// 背景：Chart.js responsive=true + maintainAspectRatio=false 下，若 <canvas>
// 没有固定高度的父容器，图表会不断读取并撑大父容器，导致无限下推。
func TestMyUsagePage_ChartCanvasHasWrapperDiv(t *testing.T) {
	body := myUsagePage(t)

	if !strings.Contains(body, `position: relative; height: 200px`) {
		t.Error("usageHistoryChart canvas should be wrapped in a div with 'position: relative; height: 200px' to prevent Chart.js infinite growth")
	}
}

// TestMyUsagePage_ChartCanvasNoInlineHeight 验证 <canvas id="usageHistoryChart">
// 元素本身不再有 height 属性（高度应由父容器 div 控制，而非 canvas 元素属性）。
func TestMyUsagePage_ChartCanvasNoInlineHeight(t *testing.T) {
	body := myUsagePage(t)

	// 在 canvas 标签区域检查是否还有 height= 属性
	canvasStart := strings.Index(body, `id="usageHistoryChart"`)
	if canvasStart == -1 {
		t.Fatal(`canvas id="usageHistoryChart" not found in HTML`)
	}
	// 往前找到 <canvas 起始位置
	openTag := body[strings.LastIndex(body[:canvasStart], "<canvas"):canvasStart+30]
	if strings.Contains(openTag, `height=`) {
		t.Error(`<canvas id="usageHistoryChart"> should NOT have a height= attribute — height must be controlled by the parent wrapper div`)
	}
}

// TestMyUsagePage_CanvasInsideWrapperDiv 验证 <canvas> 在 wrapper div 之内：
// wrapper div 的起始位置必须早于 canvas 标签。
func TestMyUsagePage_CanvasInsideWrapperDiv(t *testing.T) {
	body := myUsagePage(t)

	wrapperIdx := strings.Index(body, `position: relative; height: 200px`)
	canvasIdx := strings.Index(body, `id="usageHistoryChart"`)

	if wrapperIdx == -1 {
		t.Fatal("wrapper div with 'position: relative; height: 200px' not found")
	}
	if canvasIdx == -1 {
		t.Fatal(`canvas id="usageHistoryChart" not found`)
	}
	if wrapperIdx > canvasIdx {
		t.Error("wrapper div must appear BEFORE canvas in HTML — canvas must be nested inside the wrapper")
	}
}

// TestMyUsagePage_OverviewChartsAlsoHaveWrapperDiv 验证 overview 页面的图表也使用了
// 相同的 wrapper div 修复（回归保护：防止概览页的修复被误还原）。
func TestMyUsagePage_OverviewChartsAlsoHaveWrapperDiv(t *testing.T) {
	env := newDashEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("overview page status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	count := strings.Count(body, `position: relative; height: 200px`)
	if count < 2 {
		t.Errorf("overview page should have at least 2 chart wrapper divs with 'position: relative; height: 200px', got %d", count)
	}
}

// TestNavLayout_MultiplePages_ConsistentNav 验证多个不同页面都渲染了完整的
// 导航高亮脚本（确保 layout.html 的改动被所有页面继承）。
func TestNavLayout_MultiplePages_ConsistentNav(t *testing.T) {
	env := newDashEnv(t)

	pages := []struct {
		path string
		name string
	}{
		{"/dashboard/overview", "overview"},
		{"/dashboard/users", "users"},
		{"/dashboard/groups", "groups"},
		{"/dashboard/logs", "logs"},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p.path, nil)
			req.AddCookie(env.adminCookie(t))
			rr := httptest.NewRecorder()
			env.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("%s page status = %d, want 200", p.name, rr.Code)
			}
			body := rr.Body.String()

			if !strings.Contains(body, "data-nav") {
				t.Errorf("%s page: nav links missing data-nav attribute", p.name)
			}
			if !strings.Contains(body, "window.location.pathname") {
				t.Errorf("%s page: nav active-state script missing", p.name)
			}
		})
	}
}
