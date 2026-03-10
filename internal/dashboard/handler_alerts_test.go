package dashboard_test

import (
	"encoding/json"
	"fmt"
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
	"github.com/l17728/pairproxy/internal/eventlog"
)

// alertsEnv holds the mux, a way to get an admin cookie, and an optional
// event log that can be injected into the handler.
type alertsEnv struct {
	mux    *http.ServeMux
	jwtMgr *auth.Manager
}

// newAlertsEnv creates a dashboard handler with a fresh in-memory SQLite DB.
// If evtLog is non-nil it is set on the handler via SetEventLog.
func newAlertsEnv(t *testing.T, evtLog *eventlog.Log) *alertsEnv {
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

	jwtMgr, err := auth.NewManager(logger, "alerts-test-jwt-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(testAdminPass), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)
	if evtLog != nil {
		h.SetEventLog(evtLog)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	return &alertsEnv{mux: mux, jwtMgr: jwtMgr}
}

// adminCookieForAlerts creates a valid admin session cookie for the alertsEnv.
func (e *alertsEnv) adminCookieForAlerts(t *testing.T) *http.Cookie {
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

// eventsAPIResponse mirrors the JSON structure returned by handleEventsAPI.
type eventsAPIResponse struct {
	Events []eventlog.Event `json:"events"`
	Total  int              `json:"total"`
}

// getEvents sends an authenticated GET to /api/dashboard/events with optional
// query string additions and unmarshals the JSON response.
func getEvents(t *testing.T, env *alertsEnv, query string) eventsAPIResponse {
	t.Helper()
	path := "/api/dashboard/events"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(env.adminCookieForAlerts(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", path, rr.Code)
	}
	var resp eventsAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal events response: %v", err)
	}
	return resp
}

// makeEvents appends n events to the log: alternating warn/error.
// Returns a slice of the times used so callers can build "since" params.
func appendTestEvents(log *eventlog.Log, n int, baseTime time.Time) {
	for i := 0; i < n; i++ {
		lvl := eventlog.LevelWarn
		if i%2 == 0 {
			lvl = eventlog.LevelError
		}
		log.Append(eventlog.Event{
			Time:    baseTime.Add(time.Duration(i) * time.Millisecond),
			Level:   lvl,
			Message: fmt.Sprintf("test event %d", i),
		})
	}
}

// ---------------------------------------------------------------------------
// GET /dashboard/alerts
// ---------------------------------------------------------------------------

// TestHandleAlertsPage_GetRendersHTML verifies that the alerts page returns
// 200 HTML containing the heading "系统告警与错误".
func TestHandleAlertsPage_GetRendersHTML(t *testing.T) {
	env := newAlertsEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/alerts", nil)
	req.AddCookie(env.adminCookieForAlerts(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "系统告警与错误") {
		t.Error("body should contain '系统告警与错误'")
	}
}

// TestHandleAlertsPage_RequiresAuth verifies that GET /dashboard/alerts
// without a session cookie redirects to login.
func TestHandleAlertsPage_RequiresAuth(t *testing.T) {
	env := newAlertsEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/alerts", nil)
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
// GET /api/dashboard/events
// ---------------------------------------------------------------------------

// TestHandleEventsAPI_NilEventLog verifies that when no event log is configured
// the endpoint returns 200 JSON {"events":[],"total":0}.
func TestHandleEventsAPI_NilEventLog(t *testing.T) {
	env := newAlertsEnv(t, nil) // no event log
	resp := getEvents(t, env, "")
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0 (nil event log)", resp.Total)
	}
	if len(resp.Events) != 0 {
		t.Errorf("events len = %d, want 0 (nil event log)", len(resp.Events))
	}
}

// TestHandleEventsAPI_AllEvents appends 3 events (1 warn, 2 error) and verifies
// the endpoint returns all 3.
func TestHandleEventsAPI_AllEvents(t *testing.T) {
	log := eventlog.New(500)
	now := time.Now()
	log.Append(eventlog.Event{Time: now, Level: eventlog.LevelWarn, Message: "warn1"})
	log.Append(eventlog.Event{Time: now.Add(time.Millisecond), Level: eventlog.LevelError, Message: "error1"})
	log.Append(eventlog.Event{Time: now.Add(2 * time.Millisecond), Level: eventlog.LevelError, Message: "error2"})

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "")

	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
	if len(resp.Events) != 3 {
		t.Errorf("events len = %d, want 3", len(resp.Events))
	}
}

// TestHandleEventsAPI_LevelFilterError verifies that ?level=error returns only
// error-level events (total=2 from the 3-event set).
func TestHandleEventsAPI_LevelFilterError(t *testing.T) {
	log := eventlog.New(500)
	now := time.Now()
	log.Append(eventlog.Event{Time: now, Level: eventlog.LevelWarn, Message: "warn1"})
	log.Append(eventlog.Event{Time: now.Add(time.Millisecond), Level: eventlog.LevelError, Message: "error1"})
	log.Append(eventlog.Event{Time: now.Add(2 * time.Millisecond), Level: eventlog.LevelError, Message: "error2"})

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "level=error")

	if resp.Total != 2 {
		t.Errorf("total = %d, want 2 for level=error", resp.Total)
	}
	for _, e := range resp.Events {
		if e.Level != eventlog.LevelError {
			t.Errorf("event level = %q, want %q", e.Level, eventlog.LevelError)
		}
	}
}

// TestHandleEventsAPI_LevelFilterWarn verifies that ?level=warn returns only
// warn-level events (total=1 from the 3-event set).
func TestHandleEventsAPI_LevelFilterWarn(t *testing.T) {
	log := eventlog.New(500)
	now := time.Now()
	log.Append(eventlog.Event{Time: now, Level: eventlog.LevelWarn, Message: "warn1"})
	log.Append(eventlog.Event{Time: now.Add(time.Millisecond), Level: eventlog.LevelError, Message: "error1"})
	log.Append(eventlog.Event{Time: now.Add(2 * time.Millisecond), Level: eventlog.LevelError, Message: "error2"})

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "level=warn")

	if resp.Total != 1 {
		t.Errorf("total = %d, want 1 for level=warn", resp.Total)
	}
	if len(resp.Events) != 1 || resp.Events[0].Level != eventlog.LevelWarn {
		t.Errorf("expected 1 warn event, got %+v", resp.Events)
	}
}

// TestHandleEventsAPI_LevelFilterAll verifies that ?level=all returns all 3 events.
func TestHandleEventsAPI_LevelFilterAll(t *testing.T) {
	log := eventlog.New(500)
	now := time.Now()
	log.Append(eventlog.Event{Time: now, Level: eventlog.LevelWarn, Message: "warn1"})
	log.Append(eventlog.Event{Time: now.Add(time.Millisecond), Level: eventlog.LevelError, Message: "error1"})
	log.Append(eventlog.Event{Time: now.Add(2 * time.Millisecond), Level: eventlog.LevelError, Message: "error2"})

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "level=all")

	if resp.Total != 3 {
		t.Errorf("total = %d, want 3 for level=all", resp.Total)
	}
}

// TestHandleEventsAPI_LimitParameter verifies that ?limit=5 returns at most 5
// events when 20 are present.
func TestHandleEventsAPI_LimitParameter(t *testing.T) {
	log := eventlog.New(500)
	appendTestEvents(log, 20, time.Now())

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "limit=5")

	if resp.Total != 5 {
		t.Errorf("total = %d, want 5", resp.Total)
	}
	if len(resp.Events) != 5 {
		t.Errorf("events len = %d, want 5", len(resp.Events))
	}
}

// TestHandleEventsAPI_LimitCapped verifies that ?limit=9999 is capped at 500;
// we append 600 events so the ring holds 500 and the response holds at most 500.
func TestHandleEventsAPI_LimitCapped(t *testing.T) {
	log := eventlog.New(600)
	appendTestEvents(log, 600, time.Now())

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "limit=9999")

	if resp.Total > 500 {
		t.Errorf("total = %d, want ≤500 (cap)", resp.Total)
	}
}

// TestHandleEventsAPI_SinceFilter appends events at t-2s, t-1s, t and verifies
// that ?since=<t-1s-10ms> returns the 2 newest events.
func TestHandleEventsAPI_SinceFilter(t *testing.T) {
	log := eventlog.New(500)
	// Use UTC to avoid timezone offset with '+' sign in RFC3339 formatting,
	// which would require URL-encoding when passed as a query parameter.
	base := time.Now().UTC().Truncate(time.Second)

	t0 := base.Add(-2 * time.Second)
	t1 := base.Add(-1 * time.Second)
	t2 := base

	log.Append(eventlog.Event{Time: t0, Level: eventlog.LevelWarn, Message: "oldest"})
	log.Append(eventlog.Event{Time: t1, Level: eventlog.LevelError, Message: "middle"})
	log.Append(eventlog.Event{Time: t2, Level: eventlog.LevelError, Message: "newest"})

	env := newAlertsEnv(t, log)

	// since is just before t1, so t1 and t2 should be returned.
	sinceTime := t1.Add(-10 * time.Millisecond)
	// URL-encode the since value to handle '+' in timezone offsets safely.
	query := url.Values{"since": {sinceTime.Format(time.RFC3339Nano)}}
	resp := getEvents(t, env, query.Encode())

	if resp.Total != 2 {
		t.Errorf("total = %d, want 2 (events after since)", resp.Total)
	}
}

// TestHandleEventsAPI_InvalidSince verifies that ?since=not-a-date does not
// return a 4xx; it falls back to returning all events.
func TestHandleEventsAPI_InvalidSince(t *testing.T) {
	log := eventlog.New(500)
	now := time.Now()
	log.Append(eventlog.Event{Time: now, Level: eventlog.LevelWarn, Message: "w1"})
	log.Append(eventlog.Event{Time: now.Add(time.Millisecond), Level: eventlog.LevelError, Message: "e1"})

	env := newAlertsEnv(t, log)

	// Use direct HTTP to verify no 4xx.
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/events?since=not-a-date", nil)
	req.AddCookie(env.adminCookieForAlerts(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for invalid since param", rr.Code)
	}
	var resp eventsAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should fall back to returning all events (both).
	if resp.Total < 2 {
		t.Errorf("total = %d, want ≥2 (fallback to all events on invalid since)", resp.Total)
	}
}

// TestHandleEventsAPI_ResponseOrderingNewestFirst appends events with increasing
// timestamps and verifies the first element in the response is the newest.
func TestHandleEventsAPI_ResponseOrderingNewestFirst(t *testing.T) {
	log := eventlog.New(500)
	base := time.Now()
	log.Append(eventlog.Event{Time: base, Level: eventlog.LevelWarn, Message: "first"})
	log.Append(eventlog.Event{Time: base.Add(time.Second), Level: eventlog.LevelError, Message: "second"})
	log.Append(eventlog.Event{Time: base.Add(2 * time.Second), Level: eventlog.LevelError, Message: "third"})

	env := newAlertsEnv(t, log)
	resp := getEvents(t, env, "")

	if len(resp.Events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(resp.Events))
	}
	if !resp.Events[0].Time.After(resp.Events[1].Time) {
		t.Errorf("response[0].Time (%v) should be after response[1].Time (%v) (newest first)",
			resp.Events[0].Time, resp.Events[1].Time)
	}
}

// TestHandleEventsAPI_ContentTypeJSON verifies that the response Content-Type
// header contains "application/json".
func TestHandleEventsAPI_ContentTypeJSON(t *testing.T) {
	env := newAlertsEnv(t, eventlog.New(500))
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/events", nil)
	req.AddCookie(env.adminCookieForAlerts(t))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestHandleEventsAPI_RequiresAuth verifies that GET /api/dashboard/events
// without a session cookie redirects to login.
func TestHandleEventsAPI_RequiresAuth(t *testing.T) {
	env := newAlertsEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/events", nil)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (no auth)", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard/login" {
		t.Errorf("Location = %q, want /dashboard/login", loc)
	}
}
