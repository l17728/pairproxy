package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// TestAdminQuotaStatus — GET /api/admin/quota/status?user=<username>
// ---------------------------------------------------------------------------

func TestAdminQuotaStatus(t *testing.T) {
	_, jwtMgr, mux, userRepo, groupRepo := setupAdminTestWithTokenRepo(t)
	tok := adminToken(t, jwtMgr)
	authHdr := "Bearer " + tok

	// Create group with daily limit.
	daily := int64(10000)
	grp := &db.Group{Name: "limited"}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	if err := groupRepo.SetQuota(grp.ID, &daily, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	user := &db.User{ID: "u-qs", Username: "quota-user", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	gid := grp.ID
	_ = userRepo.SetGroup(user.ID, &gid)

	t.Run("quota status for existing user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=quota-user", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var resp quotaStatusResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.UserID != user.ID {
			t.Errorf("UserID = %q, want %q", resp.UserID, user.ID)
		}
		if resp.Username != "quota-user" {
			t.Errorf("Username = %q, want quota-user", resp.Username)
		}
		if resp.DailyLimit == nil || *resp.DailyLimit != daily {
			t.Errorf("DailyLimit = %v, want %d", resp.DailyLimit, daily)
		}
		// No usage yet → "ok"
		if resp.DailyStatus != "ok" {
			t.Errorf("DailyStatus = %q, want ok", resp.DailyStatus)
		}
	})

	t.Run("quota status for unknown user returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=nobody", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("quota status without user param returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("quota status for user without group", func(t *testing.T) {
		noGroup := &db.User{ID: "u-qs2", Username: "no-group-user", PasswordHash: "x", IsActive: true}
		if err := userRepo.Create(noGroup); err != nil {
			t.Fatalf("Create user: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=no-group-user", nil)
		req.Header.Set("Authorization", authHdr)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var resp quotaStatusResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		if resp.DailyLimit != nil {
			t.Error("DailyLimit should be nil for user without group")
		}
	})
}

// TestAdminQuotaStatus_AmbiguousUsername_Returns409 verifies that when two users
// share the same username (different auth providers), the quota-status endpoint
// returns HTTP 409 Conflict with "username_ambiguous" error code.
// This is the regression test for Fix 8 (ListByUsername ambiguity detection).
func TestAdminQuotaStatus_AmbiguousUsername_Returns409(t *testing.T) {
	_, jwtMgr, mux, userRepo, _ := setupAdminTestWithTokenRepo(t)
	tok := adminToken(t, jwtMgr)

	extID := "ldap-alice"
	// Create local "alice"
	if err := userRepo.Create(&db.User{
		Username: "alice", PasswordHash: "h1", AuthProvider: "local", IsActive: true,
	}); err != nil {
		t.Fatalf("create local alice: %v", err)
	}
	// Create LDAP "alice"
	if err := userRepo.Create(&db.User{
		Username: "alice", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID, IsActive: true,
	}); err != nil {
		t.Fatalf("create ldap alice: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/quota/status?user=alice", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("ambiguous username: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if code, _ := body["error"].(string); code != "username_ambiguous" {
		t.Errorf("error code = %q, want username_ambiguous", code)
	}
}
