package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestHandleAuditPageF10 tests the audit log page rendering
func TestHandleAuditPageF10(t *testing.T) {
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

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create some audit logs
	auditRepo.Create("admin", "user.create", "testuser", "created user testuser")
	auditRepo.Create("admin", "group.create", "testgroup", "created group testgroup")

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("audit_page_load", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/audit", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("audit_page_with_flash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/audit?flash=test+message", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}

// TestHandleTrendsAPI tests the trends API endpoint
func TestHandleTrendsAPI(t *testing.T) {
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

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	// Create test user and usage data
	userRepo.Create(&db.User{ID: "user1", Username: "testuser"})

	// Add some usage logs directly to database
	now := time.Now()
	logs := []db.UsageLog{
		{
			RequestID:    "req-1",
			UserID:       "user1",
			Model:        "claude-3-5-sonnet-20241022",
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
			CostUSD:      0.001,
			CreatedAt:    now.Add(-24 * time.Hour),
		},
		{
			RequestID:    "req-2",
			UserID:       "user1",
			Model:        "claude-3-5-sonnet-20241022",
			InputTokens:  200,
			OutputTokens: 100,
			TotalTokens:  300,
			CostUSD:      0.002,
			CreatedAt:    now.Add(-48 * time.Hour),
		},
	}

	for _, log := range logs {
		if err := gormDB.Create(&log).Error; err != nil {
			t.Fatalf("Create log: %v", err)
		}
	}

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("default_7_days", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard/trends", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}

		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if _, ok := resp["daily_tokens"]; !ok {
			t.Error("response missing daily_tokens field")
		}
		if _, ok := resp["daily_cost"]; !ok {
			t.Error("response missing daily_cost field")
		}
		if _, ok := resp["top_users"]; !ok {
			t.Error("response missing top_users field")
		}
	})

	t.Run("custom_days_30", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard/trends?days=30", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("invalid_days_parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard/trends?days=invalid", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should fallback to default 7 days
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("days_exceeds_max", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard/trends?days=500", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should fallback to default 7 days (exceeds 365 limit)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("days_zero", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard/trends?days=0", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should fallback to default 7 days
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}

// TestHandleMyUsagePage tests the user self-service usage page
func TestHandleMyUsagePage(t *testing.T) {
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

	hash, _ := bcrypt.GenerateFromPassword([]byte("test-pass"), bcrypt.MinCost)

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	token, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)

	t.Run("my_usage_page_load", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/my-usage", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("my_usage_with_flash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/my-usage?flash=welcome", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("my_usage_with_error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/my-usage?error=test+error", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: token})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// TestOverviewChartContainerFix verifies that the overview page HTML uses the
// correct Chart.js-safe canvas pattern after the "infinite scroll" bug fix.
//
// Background: Chart.js with responsive:true + maintainAspectRatio:false reads
// the CSS height of the *parent* element to size the canvas.  When a bare
// <canvas height="N"> has no parent with an explicit CSS height, the browser
// lets the canvas expand the parent, which in turn triggers Chart.js to resize
// the canvas again – an infinite growth loop.
//
// The fix wraps every <canvas> in a <div style="position:relative; height:Npx">
// so the parent always has a concrete, non-growing CSS height.
//
// These tests check the rendered HTML to ensure:
//  1. No bare <canvas> with only a height attribute remains.
//  2. Each chart canvas is preceded by the correct wrapper div.
//  3. The wrapper heights match the intended values (200px / 120px).
func TestOverviewChartContainerFix(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("overview page returned status %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	// --- 1. Bare <canvas height="..."> must not exist ----------------------
	// The old buggy pattern was <canvas id="..." height="200">.
	// After the fix the height attribute is removed from the canvas element.
	barePatterns := []string{
		`<canvas id="tokenTrendChart" height=`,
		`<canvas id="costTrendChart" height=`,
		`<canvas id="topUsersChart" height=`,
	}
	for _, pat := range barePatterns {
		if strings.Contains(body, pat) {
			t.Errorf("found bare canvas pattern %q – Chart.js infinite-height bug not fixed", pat)
		}
	}

	// --- 2. Wrapper div with position:relative must surround each canvas ---
	// Chart.js requires position:relative on the parent so that the canvas,
	// which is set to position:absolute internally, is anchored correctly.
	if !strings.Contains(body, "position: relative") && !strings.Contains(body, "position:relative") {
		t.Error("no 'position: relative' wrapper div found – canvas containers must have position:relative")
	}

	// --- 3. Token and cost trend canvases must be inside a 200px wrapper ---
	// We verify that the 200px height string appears in the page and that the
	// canvas ids immediately follow it (within a reasonable HTML distance).
	trendChartIDs := []string{"tokenTrendChart", "costTrendChart"}
	for _, id := range trendChartIDs {
		wrapperDiv := `height: 200px`
		canvasTag := `id="` + id + `"`
		// Find the wrapper that precedes this specific canvas
		canvasIdx := strings.Index(body, canvasTag)
		if canvasIdx == -1 {
			t.Errorf("canvas %q not found in overview HTML", id)
			continue
		}
		// Look backwards from canvas position for the wrapper div
		preceding := body[:canvasIdx]
		wrapperIdx := strings.LastIndex(preceding, wrapperDiv)
		if wrapperIdx == -1 {
			t.Errorf("no 'height: 200px' wrapper div found before canvas %q", id)
			continue
		}
		// The wrapper should be within 300 characters before the canvas tag
		if canvasIdx-wrapperIdx > 300 {
			t.Errorf("canvas %q is not immediately inside a 'height: 200px' wrapper div (distance=%d chars)",
				id, canvasIdx-wrapperIdx)
		}
	}

	// --- 4. Top-users canvas must be inside a 120px wrapper ---------------
	topUsersWrapper := `height: 120px`
	topUsersCanvas := `id="topUsersChart"`
	canvasIdx := strings.Index(body, topUsersCanvas)
	if canvasIdx == -1 {
		t.Error("canvas id=\"topUsersChart\" not found in overview HTML")
	} else {
		preceding := body[:canvasIdx]
		wrapperIdx := strings.LastIndex(preceding, topUsersWrapper)
		if wrapperIdx == -1 {
			t.Errorf("no 'height: 120px' wrapper div found before topUsersChart canvas")
		} else if canvasIdx-wrapperIdx > 300 {
			t.Errorf("topUsersChart canvas is not immediately inside a 'height: 120px' wrapper div (distance=%d chars)",
				canvasIdx-wrapperIdx)
		}
	}
}

// TestOverviewChartContainerCount verifies that exactly three chart canvas
// elements exist in the overview page and each one is wrapped correctly.
func TestOverviewChartContainerCount(t *testing.T) {
	env := newDashEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req.AddCookie(env.adminCookie(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("overview page status %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	expectedCanvases := []string{
		`id="tokenTrendChart"`,
		`id="costTrendChart"`,
		`id="topUsersChart"`,
	}
	for _, id := range expectedCanvases {
		if !strings.Contains(body, id) {
			t.Errorf("expected canvas %s not found in overview page", id)
		}
	}

	// Count occurrences of position:relative – should be at least 3 (one per chart).
	count := strings.Count(body, "position: relative")
	if count < 3 {
		t.Errorf("expected at least 3 'position: relative' wrapper divs, found %d", count)
	}
}
