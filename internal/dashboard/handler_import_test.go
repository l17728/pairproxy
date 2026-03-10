package dashboard_test

import (
	"bytes"
	"mime/multipart"
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

// importEnv holds all repos so tests can verify DB state after import.
type importEnv struct {
	mux            *http.ServeMux
	jwtMgr         *auth.Manager
	userRepo       *db.UserRepo
	groupRepo      *db.GroupRepo
	llmBindingRepo *db.LLMBindingRepo
}

// newImportEnv creates a dashboard handler wired to an in-memory SQLite DB
// with all dependencies needed for import (including llmBindingRepo).
func newImportEnv(t *testing.T) *importEnv {
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

	jwtMgr, err := auth.NewManager(logger, "import-test-jwt-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(testAdminPass), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	h.SetLLMDeps(llmBindingRepo, func() []proxy.LLMTargetStatus { return nil })

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	return &importEnv{
		mux:            mux,
		jwtMgr:         jwtMgr,
		userRepo:       userRepo,
		groupRepo:      groupRepo,
		llmBindingRepo: llmBindingRepo,
	}
}

// adminCookieForImport creates a valid admin session cookie for the importEnv.
func (e *importEnv) adminCookieForImport(t *testing.T) *http.Cookie {
	t.Helper()
	token, err := e.jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}
	return &http.Cookie{Name: api.AdminCookieName, Value: token}
}

// postImportForm sends a URL-encoded POST to /dashboard/import.
func postImportForm(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, content, dryRun string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("content", content)
	if dryRun != "" {
		form.Set("dry_run", dryRun)
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/import", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

const importTestContent = `[engineering]
alice Password123
bob   Password456
`

// ---------------------------------------------------------------------------
// GET /dashboard/import
// ---------------------------------------------------------------------------

// TestHandleImportPage_GetRendersForm verifies that GET /dashboard/import with
// a valid admin cookie returns 200 HTML containing the import form marker.
func TestHandleImportPage_GetRendersForm(t *testing.T) {
	env := newImportEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/import", nil)
	req.AddCookie(env.adminCookieForImport(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "上传或粘贴导入内容") {
		t.Error("body should contain import form marker '上传或粘贴导入内容'")
	}
}

// TestHandleImportPage_RequiresAuth verifies that GET /dashboard/import without
// a session cookie redirects to login.
func TestHandleImportPage_RequiresAuth(t *testing.T) {
	env := newImportEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/import", nil)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}

// ---------------------------------------------------------------------------
// POST /dashboard/import — form submissions
// ---------------------------------------------------------------------------

// TestHandleImportSubmit_EmptyContent verifies that POSTing empty content
// returns 200 with the error message "请上传文件或粘贴导入内容".
func TestHandleImportSubmit_EmptyContent(t *testing.T) {
	env := newImportEnv(t)
	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), "", "")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "请上传文件或粘贴导入内容") {
		t.Error("body should contain error '请上传文件或粘贴导入内容'")
	}
}

// TestHandleImportSubmit_ParseError verifies that content which fails to parse
// returns 200 HTML containing "解析失败".
func TestHandleImportSubmit_ParseError(t *testing.T) {
	env := newImportEnv(t)
	// A line with only one field (no password) triggers a parse error.
	badContent := "invalid\nline\nno_password"
	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), badContent, "")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "解析失败") {
		t.Errorf("body should contain '解析失败'; got: %s", rr.Body.String()[:min(200, rr.Body.Len())])
	}
}

// TestHandleImportSubmit_DryRun verifies that a dry-run POST returns HTML
// containing "预览结果" and does NOT persist users to the DB.
func TestHandleImportSubmit_DryRun(t *testing.T) {
	env := newImportEnv(t)
	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), importTestContent, "on")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "预览结果") {
		t.Error("body should contain '预览结果' for dry-run")
	}

	// Users must NOT be created in DB.
	alice, err := env.userRepo.GetByUsername("alice")
	if err != nil {
		t.Fatalf("GetByUsername('alice'): %v", err)
	}
	if alice != nil {
		t.Error("dry-run must not create users in DB; alice was created")
	}
}

// TestHandleImportSubmit_ActualImport verifies that an actual import (dry_run
// off) creates users alice and bob in the DB and returns HTML with "导入完成".
func TestHandleImportSubmit_ActualImport(t *testing.T) {
	env := newImportEnv(t)
	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), importTestContent, "")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "导入完成") {
		t.Error("body should contain '导入完成'")
	}

	for _, name := range []string{"alice", "bob"} {
		u, err := env.userRepo.GetByUsername(name)
		if err != nil {
			t.Fatalf("GetByUsername(%q): %v", name, err)
		}
		if u == nil {
			t.Errorf("user %q should exist in DB after actual import", name)
		}
	}
}

// TestHandleImportSubmit_SkipExisting verifies that importing content whose
// group and a user already exist reports the skip counts in the HTML.
func TestHandleImportSubmit_SkipExisting(t *testing.T) {
	env := newImportEnv(t)

	// Pre-create group "engineering" and user "alice".
	if err := env.groupRepo.Create(&db.Group{Name: "engineering"}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	if err := env.userRepo.Create(&db.User{
		Username:     "alice",
		PasswordHash: string(hash),
		IsActive:     true,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), importTestContent, "")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "个分组已存在，跳过") {
		t.Error("body should contain '个分组已存在，跳过'")
	}
	if !strings.Contains(body, "个用户已存在，跳过") {
		t.Error("body should contain '个用户已存在，跳过'")
	}
}

// TestHandleImportSubmit_LLMBinding verifies that importing content with LLM
// targets results in "导入完成" and that the LLM 绑定 stat card shows > 0.
func TestHandleImportSubmit_LLMBinding(t *testing.T) {
	env := newImportEnv(t)

	content := "[eng llm=https://api.anthropic.com]\nalice Pass123 llm=https://api.openai.com\n"
	rr := postImportForm(t, env.mux, env.adminCookieForImport(t), content, "")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "导入完成") {
		t.Error("body should contain '导入完成'")
	}
	// The LLM binding stat card should show a non-zero number.
	if !strings.Contains(body, "LLM 绑定") {
		t.Error("body should contain 'LLM 绑定' stat card")
	}
}

// TestHandleImportSubmit_FileUpload verifies that POSTing a multipart form
// with a "file" field results in "导入完成".
func TestHandleImportSubmit_FileUpload(t *testing.T) {
	env := newImportEnv(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "import.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte("[eng]\nalice Pass123\n")); err != nil {
		t.Fatalf("write file field: %v", err)
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/dashboard/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(env.adminCookieForImport(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "导入完成") {
		t.Error("body should contain '导入完成' after file upload import")
	}
}

// TestHandleImportSubmit_RequiresAuth verifies that POST /dashboard/import
// without a session cookie redirects to login.
func TestHandleImportSubmit_RequiresAuth(t *testing.T) {
	env := newImportEnv(t)
	rr := postImportForm(t, env.mux, nil, importTestContent, "")

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}

// min returns the smaller of a and b (helper for error messages).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
