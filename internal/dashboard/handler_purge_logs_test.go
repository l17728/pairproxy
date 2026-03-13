package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
)

// purgeLogsEnv 测试环境
type purgeLogsEnv struct {
	mux       *http.ServeMux
	token     string
	gormDB    *gorm.DB
	usageRepo *db.UsageRepo
	auditRepo *db.AuditRepo
}

func newPurgeLogsEnv(t *testing.T) *purgeLogsEnv {
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

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID: "__admin__", Username: "admin", Role: "admin",
	}, time.Hour)

	return &purgeLogsEnv{
		mux: mux, token: token,
		gormDB: gormDB, usageRepo: usageRepo, auditRepo: auditRepo,
	}
}

// postPurge 发送 POST /dashboard/logs/purge-all，携带 confirm 字段
func (e *purgeLogsEnv) postPurge(t *testing.T, confirm string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("confirm", confirm)
	req := httptest.NewRequest(http.MethodPost, "/dashboard/logs/purge-all", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.token})
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

// insertLog 直接向 DB 写入一条使用日志
func (e *purgeLogsEnv) insertLog(t *testing.T, requestID string) {
	t.Helper()
	log := db.UsageLog{
		RequestID:    requestID,
		UserID:       "user-test",
		Model:        "claude-3-5-sonnet",
		InputTokens:  100,
		OutputTokens: 50,
		TotalTokens:  150,
		StatusCode:   200,
		CreatedAt:    time.Now().Add(-time.Minute),
	}
	if err := e.gormDB.Create(&log).Error; err != nil {
		t.Fatalf("insertLog %q: %v", requestID, err)
	}
}

// ---------------------------------------------------------------------------
// 功能测试
// ---------------------------------------------------------------------------

// TestLogsPurgeAll_OK 输入正确的 OK，日志被全部清空
func TestLogsPurgeAll_OK(t *testing.T) {
	e := newPurgeLogsEnv(t)
	e.insertLog(t, "req-001")
	e.insertLog(t, "req-002")

	rr := e.postPurge(t, "OK")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/logs") {
		t.Errorf("expected redirect to /dashboard/logs, got %q", loc)
	}
	if strings.Contains(loc, "error=") {
		t.Errorf("unexpected error in redirect: %q", loc)
	}
	if !strings.Contains(loc, "flash=") {
		t.Errorf("expected flash message in redirect, got %q", loc)
	}
}

// TestLogsPurgeAll_LeavesNoLogs 清空后 DB 中应无日志记录
func TestLogsPurgeAll_LeavesNoLogs(t *testing.T) {
	e := newPurgeLogsEnv(t)
	for _, id := range []string{"req-a", "req-b", "req-c"} {
		e.insertLog(t, id)
	}

	e.postPurge(t, "OK")

	logs, err := e.usageRepo.Query(db.UsageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("query after purge: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 logs after purge, got %d", len(logs))
	}
}

// TestLogsPurgeAll_WrongConfirm 输入错误确认字符串，返回错误重定向，日志不被删除
func TestLogsPurgeAll_WrongConfirm(t *testing.T) {
	e := newPurgeLogsEnv(t)
	e.insertLog(t, "req-keep-001")

	rr := e.postPurge(t, "yes")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "error=") {
		t.Error("expected error redirect for wrong confirm")
	}
	logs, _ := e.usageRepo.Query(db.UsageFilter{Limit: 10})
	if len(logs) == 0 {
		t.Error("logs should not have been deleted with wrong confirm")
	}
}

// TestLogsPurgeAll_EmptyConfirm 空确认字符串，应返回错误重定向
func TestLogsPurgeAll_EmptyConfirm(t *testing.T) {
	e := newPurgeLogsEnv(t)
	e.insertLog(t, "req-keep-002")

	rr := e.postPurge(t, "")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "error=") {
		t.Error("expected error redirect for empty confirm")
	}
	logs, _ := e.usageRepo.Query(db.UsageFilter{Limit: 10})
	if len(logs) == 0 {
		t.Error("logs should not have been deleted with empty confirm")
	}
}

// TestLogsPurgeAll_CaseSensitive "OK" 区分大小写，其他变体应失败
func TestLogsPurgeAll_CaseSensitive(t *testing.T) {
	e := newPurgeLogsEnv(t)

	for _, bad := range []string{"ok", "Ok", "oK", "OK.", " OK", "OK "} {
		t.Run(bad, func(t *testing.T) {
			rr := e.postPurge(t, bad)
			loc := rr.Header().Get("Location")
			if !strings.Contains(loc, "error=") {
				t.Errorf("confirm=%q should fail, got redirect %q", bad, loc)
			}
		})
	}
}

// TestLogsPurgeAll_RequiresSession 未携带 session cookie 应被重定向到登录页
func TestLogsPurgeAll_RequiresSession(t *testing.T) {
	e := newPurgeLogsEnv(t)

	form := url.Values{}
	form.Set("confirm", "OK")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/logs/purge-all", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 不设置 cookie

	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther && rr.Code != http.StatusFound {
		t.Fatalf("expected redirect for unauthenticated request, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "login") {
		t.Errorf("expected redirect to login, got %q", rr.Header().Get("Location"))
	}
}

// TestLogsPurgeAll_EmptyDB 空数据库清空操作应正常成功（不报错）
func TestLogsPurgeAll_EmptyDB(t *testing.T) {
	e := newPurgeLogsEnv(t)

	rr := e.postPurge(t, "OK")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rr.Code)
	}
	if strings.Contains(rr.Header().Get("Location"), "error=") {
		t.Errorf("purge on empty DB should not error, got %q", rr.Header().Get("Location"))
	}
}

// ---------------------------------------------------------------------------
// 页面渲染测试（模板）
// ---------------------------------------------------------------------------

func getLogsPage(t *testing.T, e *purgeLogsEnv) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/logs", nil)
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: e.token})
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/logs returned %d", rr.Code)
	}
	return rr.Body.String()
}

// TestLogsPageHasPurgeButton 日志页应渲染"清空日志"按钮
func TestLogsPageHasPurgeButton(t *testing.T) {
	e := newPurgeLogsEnv(t)
	body := getLogsPage(t, e)

	if !strings.Contains(body, "清空日志") {
		t.Error("logs page should contain '清空日志' button")
	}
	if !strings.Contains(body, "openPurgeModal") {
		t.Error("logs page should call openPurgeModal() on click")
	}
}

// TestLogsPageHasPurgeModal 日志页应包含确认模态框
func TestLogsPageHasPurgeModal(t *testing.T) {
	e := newPurgeLogsEnv(t)
	body := getLogsPage(t, e)

	if !strings.Contains(body, `action="/dashboard/logs/purge-all"`) {
		t.Error("purge form should POST to /dashboard/logs/purge-all")
	}
	if !strings.Contains(body, `name="confirm"`) {
		t.Error("purge form should have confirm input field")
	}
	if !strings.Contains(body, "purgeModal") {
		t.Error("page should contain purgeModal element")
	}
}

// TestLogsPagePurgeModalInstructions 模态框应包含操作说明及 "OK" 提示
func TestLogsPagePurgeModalInstructions(t *testing.T) {
	e := newPurgeLogsEnv(t)
	body := getLogsPage(t, e)

	if !strings.Contains(body, "永久删除") {
		t.Error("modal should warn about permanent deletion")
	}
	if !strings.Contains(body, "OK") {
		t.Error("modal should show OK as required input")
	}
}
