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

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
)

const testAdminPass = "dashboard-test-pass-123"

// dashEnv holds shared test state for dashboard handler tests.
type dashEnv struct {
	mux    *http.ServeMux
	jwtMgr *auth.Manager
}

// newDashEnv creates a dashboard handler wired to an in-memory SQLite DB.
// The admin password is set to testAdminPass (hashed at MinCost for speed).
func newDashEnv(t *testing.T) *dashEnv {
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

	jwtMgr, err := auth.NewManager(logger, "dashboard-test-jwt-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Use bcrypt.MinCost (cost=4) for fast test execution.
	hash, err := bcrypt.GenerateFromPassword([]byte(testAdminPass), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, string(hash), time.Hour)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	return &dashEnv{mux: mux, jwtMgr: jwtMgr}
}

// adminCookie creates a valid admin session cookie for use in authenticated requests.
func (e *dashEnv) adminCookie(t *testing.T) *http.Cookie {
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

// ---------------------------------------------------------------------------
// Login / Logout
// ---------------------------------------------------------------------------

// TestDashboardLoginPage verifies that the login page renders (200 + HTML).
func TestDashboardLoginPage(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// TestDashboardLoginSuccess verifies that the correct password causes a redirect
// to /dashboard/overview and that the admin session cookie is set.
func TestDashboardLoginSuccess(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("password", testAdminPass)
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/overview" {
		t.Errorf("Location = %q, want /dashboard/overview", loc)
	}

	var cookieFound bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == api.AdminCookieName && c.Value != "" {
			cookieFound = true
		}
	}
	if !cookieFound {
		t.Error("admin session cookie should be set after successful login")
	}
}

// TestDashboardLoginFailure verifies that an incorrect password re-renders the login
// page (200) and does not set a session cookie.
func TestDashboardLoginFailure(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("password", "wrong-password")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render login page)", rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == api.AdminCookieName {
			t.Error("session cookie must not be set on failed login")
		}
	}
}

// TestDashboardLoginEmptyPassword verifies that submitting an empty password is rejected.
func TestDashboardLoginEmptyPassword(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("password", "")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render login page on empty password)", rr.Code)
	}
}

// TestDashboardLogout verifies that the logout handler clears the cookie
// and redirects to /dashboard/login.
func TestDashboardLogout(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/logout", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
	// The cookie should be deleted (MaxAge = -1).
	for _, c := range rr.Result().Cookies() {
		if c.Name == api.AdminCookieName && c.MaxAge > 0 {
			t.Error("cookie MaxAge must be ≤0 (deletion) after logout")
		}
	}
}

// ---------------------------------------------------------------------------
// requireSession middleware
// ---------------------------------------------------------------------------

// TestDashboardRequireSession_NoCookie verifies that a protected route
// redirects to /dashboard/login when no cookie is present.
func TestDashboardRequireSession_NoCookie(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}

// TestDashboardRequireSession_BadToken verifies that a malformed JWT
// causes a redirect to the login page.
func TestDashboardRequireSession_BadToken(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: "not-a-valid-jwt"})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 for invalid token", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}

// TestDashboardRequireSession_NonAdminRole verifies that a valid JWT with
// role="user" is rejected (redirects to login).
func TestDashboardRequireSession_NonAdminRole(t *testing.T) {
	env := newDashEnv(t)

	token, err := env.jwtMgr.Sign(auth.JWTClaims{
		UserID:   "u1",
		Username: "regularuser",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 for non-admin role", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}

// ---------------------------------------------------------------------------
// Authenticated pages
// ---------------------------------------------------------------------------

// TestDashboardOverview verifies that the overview page renders (200 + HTML)
// for an authenticated admin session with an empty database.
func TestDashboardOverview(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// TestDashboardUsersPage verifies that the users management page renders.
func TestDashboardUsersPage(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/users", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// TestDashboardGroupsPage verifies that the groups management page renders.
func TestDashboardGroupsPage(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/groups", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// TestDashboardLogsPage verifies that the logs page renders.
func TestDashboardLogsPage(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/logs", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// User management form actions
// ---------------------------------------------------------------------------

// TestDashboardCreateUser verifies that POSTing a valid user form redirects
// back to /dashboard/users with a flash message.
func TestDashboardCreateUser(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("username", "newuser")
	form.Set("password", "newpass123")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "/dashboard/users") {
		t.Errorf("Location = %q, should contain /dashboard/users", loc)
	}
}

// TestDashboardCreateUser_EmptyFields verifies that missing username/password
// causes a redirect back with an error query parameter.
func TestDashboardCreateUser_EmptyFields(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("username", "")
	form.Set("password", "")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Errorf("Location = %q, want error query param on empty fields", loc)
	}
}

// ---------------------------------------------------------------------------
// Group management form actions
// ---------------------------------------------------------------------------

// TestDashboardCreateGroup verifies that POSTing a valid group form redirects
// back to /dashboard/groups.
func TestDashboardCreateGroup(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("name", "test-group")
	form.Set("daily_limit", "10000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/groups", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "/dashboard/groups") {
		t.Errorf("Location = %q, should contain /dashboard/groups", loc)
	}
}

// TestDashboardCreateGroup_EmptyName verifies that an empty group name
// causes a redirect with an error query parameter.
func TestDashboardCreateGroup_EmptyName(t *testing.T) {
	env := newDashEnv(t)

	form := url.Values{}
	form.Set("name", "")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/groups", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Errorf("Location = %q, want error query param for empty group name", loc)
	}
}
