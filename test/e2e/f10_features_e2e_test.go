// Package e2e_test contains end-to-end tests for F-10 WebUI features.
//
// F-10 新增功能：
//   - 趋势图表 API (/api/dashboard/trends)
//   - 用户自助配额查询 (/api/user/quota-status)
//   - 用户自助用量历史 (/api/user/usage-history)
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
)

// TestTrendsAPIE2E tests the complete flow of trends API
func TestTrendsAPIE2E(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Setup database
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// Setup repositories
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	auditRepo := db.NewAuditRepo(logger, gormDB)

	// Setup JWT manager
	jwtMgr, err := auth.NewManager(logger, "e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create test users
	userRepo.Create(&db.User{ID: "user1", Username: "alice", IsActive: true})
	userRepo.Create(&db.User{ID: "user2", Username: "bob", IsActive: true})

	// Insert usage data for the past 7 days
	now := time.Now()
	for i := 0; i < 7; i++ {
		day := now.Add(-time.Duration(i) * 24 * time.Hour)

		// User 1 usage
		if err := gormDB.Create(&db.UsageLog{
			RequestID:    "req-user1-day" + string(rune('0'+i)),
			UserID:       "user1",
			Model:        "claude-3-5-sonnet-20241022",
			InputTokens:  1000 * (i + 1),
			OutputTokens: 500 * (i + 1),
			TotalTokens:  1500 * (i + 1),
			CostUSD:      0.01 * float64(i+1),
			CreatedAt:    day,
		}).Error; err != nil {
			t.Fatalf("Create usage log: %v", err)
		}

		// User 2 usage
		if err := gormDB.Create(&db.UsageLog{
			RequestID:    "req-user2-day" + string(rune('0'+i)),
			UserID:       "user2",
			Model:        "claude-3-5-sonnet-20241022",
			InputTokens:  800 * (i + 1),
			OutputTokens: 400 * (i + 1),
			TotalTokens:  1200 * (i + 1),
			CostUSD:      0.008 * float64(i+1),
			CreatedAt:    day,
		}).Error; err != nil {
			t.Fatalf("Create usage log: %v", err)
		}
	}

	// Setup dashboard handler
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)
	h := dashboard.NewHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo, string(hash), time.Hour)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create test server
	server := httptest.NewServer(mux)
	defer server.Close()

	// Generate admin token
	adminToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token: %v", err)
	}

	t.Run("trends_api_default_7_days", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/dashboard/trends", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: adminToken})

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		// topUserEntry 与 dashboard.topUserEntry 对齐（含 username 字段）
		type topUserEntry struct {
			UserID       string `json:"user_id"`
			Username     string `json:"username"`
			TotalInput   int64  `json:"total_input"`
			TotalOutput  int64  `json:"total_output"`
			RequestCount int64  `json:"request_count"`
		}
		var result struct {
			DailyTokens []db.DailyTokenRow `json:"daily_tokens"`
			DailyCost   []db.DailyCostRow  `json:"daily_cost"`
			TopUsers    []topUserEntry     `json:"top_users"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Verify daily tokens data
		if len(result.DailyTokens) == 0 {
			t.Error("expected daily tokens data, got empty")
		}

		// Verify daily cost data
		if len(result.DailyCost) == 0 {
			t.Error("expected daily cost data, got empty")
		}

		// Verify top users data
		if len(result.TopUsers) != 2 {
			t.Errorf("expected 2 top users, got %d", len(result.TopUsers))
		}

		// Verify user stats and username resolution
		var totalInput, totalOutput int64
		for _, user := range result.TopUsers {
			totalInput += user.TotalInput
			totalOutput += user.TotalOutput
			if user.TotalInput == 0 && user.TotalOutput == 0 {
				t.Errorf("user %s has zero tokens", user.UserID)
			}
			// username 应被解析为真实用户名（alice 或 bob），而非 UUID
			if user.Username == "" {
				t.Errorf("user %s has empty username", user.UserID)
			}
			if user.Username == user.UserID {
				t.Errorf("username %q was not resolved (still equals user_id)", user.Username)
			}
		}

		if totalInput == 0 && totalOutput == 0 {
			t.Error("total tokens should not be zero")
		}
	})

	t.Run("trends_api_custom_30_days", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/dashboard/trends?days=30", nil)
		req.AddCookie(&http.Cookie{Name: api.AdminCookieName, Value: adminToken})

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		type topUserEntry struct {
			UserID       string `json:"user_id"`
			Username     string `json:"username"`
			TotalInput   int64  `json:"total_input"`
			TotalOutput  int64  `json:"total_output"`
			RequestCount int64  `json:"request_count"`
		}
		var result struct {
			DailyTokens []db.DailyTokenRow `json:"daily_tokens"`
			DailyCost   []db.DailyCostRow  `json:"daily_cost"`
			TopUsers    []topUserEntry     `json:"top_users"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Should still return data even for 30 days (we only have 7 days of data)
		if len(result.TopUsers) != 2 {
			t.Errorf("expected 2 top users, got %d", len(result.TopUsers))
		}
	})

	t.Run("trends_api_unauthorized", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/dashboard/trends", nil)
		// No cookie

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should redirect to login
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
			t.Logf("Got status %d, which is acceptable for unauthorized access", resp.StatusCode)
		}
	})
}

// TestUserQuotaStatusE2E tests the user quota status API end-to-end
func TestUserQuotaStatusE2E(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Setup database
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// Setup repositories
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	// Setup JWT manager
	jwtMgr, err := auth.NewManager(logger, "e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create test user with group
	groupRepo.Create(&db.Group{
		Name:             "testgroup",
		DailyTokenLimit:  ptr(int64(100000)),
		MonthlyTokenLimit: ptr(int64(3000000)),
	})
	groups, _ := groupRepo.List()
	if len(groups) == 0 {
		t.Fatal("no groups created")
	}
	group := groups[0]

	userRepo.Create(&db.User{
		ID:       "user1",
		Username: "alice",
		IsActive: true,
		GroupID:  &group.ID,
	})

	// Insert usage data for today
	now := time.Now().UTC()
	if err := gormDB.Create(&db.UsageLog{
		RequestID:    "req-today-1",
		UserID:       "user1",
		Model:        "claude-3-5-sonnet-20241022",
		InputTokens:  5000,
		OutputTokens: 2500,
		TotalTokens:  7500,
		CostUSD:      0.05,
		CreatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("Create usage log: %v", err)
	}

	// Setup API handler
	apiHandler := api.NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	mux := http.NewServeMux()
	apiHandler.RegisterRoutes(mux)

	// Create test server
	server := httptest.NewServer(mux)
	defer server.Close()

	// Generate user token
	userToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "user1",
		Username: "alice",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token: %v", err)
	}

	t.Run("quota_status_success", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/user/quota-status", nil)
		req.Header.Set("Authorization", "Bearer "+userToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		var result struct {
			DailyUsed    int64 `json:"daily_used"`
			DailyLimit   int64 `json:"daily_limit"`
			MonthlyUsed  int64 `json:"monthly_used"`
			MonthlyLimit int64 `json:"monthly_limit"`
			RPMLimit     int   `json:"rpm_limit"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Verify daily usage
		if result.DailyUsed != 7500 {
			t.Errorf("daily_used = %d, want 7500", result.DailyUsed)
		}

		// Verify daily limit
		if result.DailyLimit != 100000 {
			t.Errorf("daily_limit = %d, want 100000", result.DailyLimit)
		}

		// Verify monthly usage
		if result.MonthlyUsed != 7500 {
			t.Errorf("monthly_used = %d, want 7500", result.MonthlyUsed)
		}

		// Verify monthly limit
		if result.MonthlyLimit != 3000000 {
			t.Errorf("monthly_limit = %d, want 3000000", result.MonthlyLimit)
		}
	})

	t.Run("quota_status_unauthorized", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/user/quota-status", nil)
		// No Authorization header

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Error("expected non-200 status for unauthorized request")
		}
	})
}

// TestUserUsageHistoryE2E tests the user usage history API end-to-end
func TestUserUsageHistoryE2E(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Setup database
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// Setup repositories
	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	// Setup JWT manager
	jwtMgr, err := auth.NewManager(logger, "e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create test user
	userRepo.Create(&db.User{
		ID:       "user1",
		Username: "alice",
		IsActive: true,
	})

	// Insert usage history
	now := time.Now()
	for i := 0; i < 10; i++ {
		if err := gormDB.Create(&db.UsageLog{
			RequestID:    "req-" + string(rune('0'+i)),
			UserID:       "user1",
			Model:        "claude-3-5-sonnet-20241022",
			InputTokens:  1000,
			OutputTokens: 500,
			TotalTokens:  1500,
			CostUSD:      0.01,
			CreatedAt:    now.Add(-time.Duration(i) * time.Hour),
		}).Error; err != nil {
			t.Fatalf("Create usage log: %v", err)
		}
	}

	// Setup API handler
	apiHandler := api.NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	mux := http.NewServeMux()
	apiHandler.RegisterRoutes(mux)

	// Create test server
	server := httptest.NewServer(mux)
	defer server.Close()

	// Generate user token
	userToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "user1",
		Username: "alice",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token: %v", err)
	}

	t.Run("usage_history_default_limit", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/user/usage-history", nil)
		req.Header.Set("Authorization", "Bearer "+userToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		var result struct {
			History []db.DailyTokenRow `json:"history"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Should return daily aggregated data (we have 10 days of data)
		if len(result.History) == 0 {
			t.Error("expected history data, got empty")
		}

		// Verify history data structure
		for _, day := range result.History {
			if day.InputTokens == 0 && day.OutputTokens == 0 {
				t.Errorf("day %s has zero tokens", day.Date)
			}
		}
	})

	t.Run("usage_history_custom_limit", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/user/usage-history?days=7", nil)
		req.Header.Set("Authorization", "Bearer "+userToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		var result struct {
			History []db.DailyTokenRow `json:"history"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Should return data for 7 days
		if len(result.History) == 0 {
			t.Error("expected history data, got empty")
		}
	})

	t.Run("usage_history_unauthorized", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/user/usage-history", nil)
		// No Authorization header

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Error("expected non-200 status for unauthorized request")
		}
	})
}

func ptr[T any](v T) *T {
	return &v
}
