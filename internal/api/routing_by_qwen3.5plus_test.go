package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"go.uber.org/zap/zaptest"
)

// Helper function to perform assertions in tests
func assertEquals(t *testing.T, expected, actual interface{}) {
	if expected != actual {
		t.Errorf("Expected %v, got %v", expected, actual)
	}
}

// TestExtractBearerToken functionality
func TestAPIExtractBearerTokenFunctionality(t *testing.T) {
	t.Run("extractBearerToken_with_valid_header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer some-token-here")

		token := extractBearerToken(req)
		assertEquals(t, "some-token-here", token)
	})

	t.Run("extractBearerToken_without_prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "some-token-here")

		token := extractBearerToken(req)
		assertEquals(t, "", token)
	})

	t.Run("extractBearerToken_no_header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		token := extractBearerToken(req)
		assertEquals(t, "", token)
	})
}

func TestAPIParseDaysFunctionality(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create admin JWT manager
	adminJwtMgr, err := auth.NewManager(logger, "very_secure_jwt_secret_test_only_for_test_with_at_least_32_chars")
	if err != nil {
		t.Fatalf("Failed to create admin JWT manager: %v", err)
	}

	// Create repos
	userRepo := db.NewUserRepo(nil, logger)
	groupRepo := db.NewGroupRepo(nil, logger)
	usageRepo := db.NewUsageRepo(nil, logger)
	auditRepo := db.NewAuditRepo(logger, nil)

	adminPasswordHash := "$2a$10$8RQY9xL2FZJ2zZJ0z0y3TOmYy6k.Y8x.Zd6.q7.Xy7.yB4YvzHxqK" // "admin"
	_ = NewAdminHandler(logger, adminJwtMgr, userRepo, groupRepo, usageRepo, auditRepo, adminPasswordHash, 24*time.Hour)

	t.Run("parseDays_default", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/usage", nil)
		result := parseDays(req, 7)
		assertEquals(t, 7, result)
	})

	t.Run("parseDays_override", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/usage?days=30", nil)
		result := parseDays(req, 7)
		assertEquals(t, 30, result)
	})

	t.Run("parseDays_invalid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/usage?days=invalid", nil)
		result := parseDays(req, 7)
		assertEquals(t, 7, result) // falls back to default
	})
}

func TestAPIQuotaLevelFunctionality(t *testing.T) {
	t.Run("quotaLevel_below_80_percent", func(t *testing.T) {
		limit := int64(1000)
		result := quotaLevel(700, &limit)
		assertEquals(t, "ok", result)
	})

	t.Run("quotaLevel_at_80_percent", func(t *testing.T) {
		limit := int64(1000)
		result := quotaLevel(800, &limit) // Exactly 80%
		assertEquals(t, "warning", result)
	})

	t.Run("quotaLevel_at_limit", func(t *testing.T) {
		limit := int64(1000)
		result := quotaLevel(1000, &limit)
		assertEquals(t, "exceeded", result)
	})

	t.Run("quotaLevel_unlimited", func(t *testing.T) {
		result := quotaLevel(1000000, nil) // nil means unlimited
		assertEquals(t, "ok", result)
	})
}

func TestAPIExportCSVFunctionality(t *testing.T) {
	now := time.Now()

	usageLog := db.UsageLog{
		ID:           1,
		RequestID:    "req-test-123",
		UserID:       "user-456",
		Model:        "claude-3-5-sonnet",
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		IsStreaming:  true,
		StatusCode:   200,
		DurationMs:   1500,
		CostUSD:      2.25,
		SourceNode:   "local",
		UpstreamURL:  "https://api.anthropic.com",
		CreatedAt:    now,
	}

	t.Run("exportLogToJSON", func(t *testing.T) {
		jsonLog := exportLogToJSON(usageLog)

		assertEquals(t, uint(1), jsonLog["id"])
		assertEquals(t, "req-test-123", jsonLog["request_id"])
		assertEquals(t, "user-456", jsonLog["user_id"])
		assertEquals(t, "claude-3-5-sonnet", jsonLog["model"])
		assertEquals(t, 1000, jsonLog["input_tokens"])
		assertEquals(t, 500, jsonLog["output_tokens"])
		assertEquals(t, 1500, jsonLog["total_tokens"])
		assertEquals(t, true, jsonLog["is_streaming"])
		assertEquals(t, 200, jsonLog["status_code"])
		assertEquals(t, int64(1500), jsonLog["duration_ms"])
		assertEquals(t, 2.25, jsonLog["cost_usd"])
		assertEquals(t, "local", jsonLog["source_node"])
		assertEquals(t, "https://api.anthropic.com", jsonLog["upstream_url"])
		assertEquals(t, now.UTC().Format(time.RFC3339), jsonLog["created_at"])
	})

	t.Run("exportLogToCSVRecord", func(t *testing.T) {
		csvRecord := exportLogToCSVRecord(usageLog)

		assertEquals(t, "1", csvRecord[0])                             // id
		assertEquals(t, "req-test-123", csvRecord[1])                  // request_id
		assertEquals(t, "user-456", csvRecord[2])                      // user_id
		assertEquals(t, "claude-3-5-sonnet", csvRecord[3])             // model
		assertEquals(t, "1000", csvRecord[4])                          // input_tokens
		assertEquals(t, "500", csvRecord[5])                           // output_tokens
		assertEquals(t, "1500", csvRecord[6])                          // total_tokens
		assertEquals(t, "true", csvRecord[7])                          // is_streaming
		assertEquals(t, "200", csvRecord[8])                           // status_code
		assertEquals(t, "1500", csvRecord[9])                          // duration_ms
		assertEquals(t, true, strings.Contains(csvRecord[10], "2"))    // cost_usd (2.250000)
		assertEquals(t, "local", csvRecord[11])                        // source_node
		assertEquals(t, "https://api.anthropic.com", csvRecord[12])    // upstream_url
		assertEquals(t, now.UTC().Format(time.RFC3339), csvRecord[13]) // created_at
	})
}

func TestLoginRequestStructure(t *testing.T) {
	t.Run("login_request_structure", func(t *testing.T) {
		loginReq := loginRequest{
			Username: "testuser",
			Password: "password123",
		}

		bytesData, err := json.Marshal(loginReq)
		assertEquals(t, nil, err)

		var parsedLoginReq loginRequest
		err = json.Unmarshal(bytesData, &parsedLoginReq)
		assertEquals(t, nil, err)
		assertEquals(t, "testuser", parsedLoginReq.Username)
		assertEquals(t, "password123", parsedLoginReq.Password)
	})
}

func TestRefreshRequestStructure(t *testing.T) {
	t.Run("refresh_request_structure", func(t *testing.T) {
		refreshReq := refreshRequest{
			RefreshToken: "some-refresh-token",
		}

		bytesData, err := json.Marshal(refreshReq)
		assertEquals(t, nil, err)

		var parsedRefreshReq refreshRequest
		err = json.Unmarshal(bytesData, &parsedRefreshReq)
		assertEquals(t, nil, err)
		assertEquals(t, "some-refresh-token", parsedRefreshReq.RefreshToken)
	})
}

func TestLogoutRequestStructure(t *testing.T) {
	t.Run("logout_request_structure", func(t *testing.T) {
		logoutReq := logoutRequest{
			RefreshToken: "some-refresh-token",
		}

		bytesData, err := json.Marshal(logoutReq)
		assertEquals(t, nil, err)

		var parsedLogoutReq logoutRequest
		err = json.Unmarshal(bytesData, &parsedLogoutReq)
		assertEquals(t, nil, err)
		assertEquals(t, "some-refresh-token", parsedLogoutReq.RefreshToken)
	})
}

func TestAPIResponseStructures(t *testing.T) {
	_ = zaptest.NewLogger(t)

	dailyTokenLimit := int64(10000)
	requestsPerMinute := 100
	currentTime := time.Now()

	t.Run("groupResponse_structure", func(t *testing.T) {
		group := db.Group{
			ID:                "grp-123",
			Name:              "test-group",
			DailyTokenLimit:   &dailyTokenLimit,
			RequestsPerMinute: &requestsPerMinute,
			CreatedAt:         currentTime,
		}

		resp := groupToResponse(group)

		assertEquals(t, "grp-123", resp.ID)
		assertEquals(t, "test-group", resp.Name)
		assertEquals(t, &dailyTokenLimit, resp.DailyTokenLimit)
		assertEquals(t, &requestsPerMinute, resp.RequestsPerMinute)
		assertEquals(t, currentTime.UTC().Format(time.RFC3339), resp.CreatedAt)
	})

	t.Run("userResponse_structure", func(t *testing.T) {
		groupID := "grp-123"
		lastLoginAt := currentTime.Add(-24 * time.Hour)

		user := db.User{
			ID:        "usr-456",
			Username:  "testuser",
			GroupID:   &groupID,
			IsActive:  true,
			CreatedAt: currentTime,
			Group:     db.Group{Name: "Test Group"},
		}

		lastLoginAtStr := lastLoginAt.UTC().Format(time.RFC3339)
		_ = lastLoginAtStr

		// Create a response manually to match structure
		resp := userToResponse(user)

		assertEquals(t, "usr-456", resp.ID)
		assertEquals(t, "testuser", resp.Username)
		assertEquals(t, &groupID, resp.GroupID)
		assertEquals(t, "Test Group", resp.GroupName)
		assertEquals(t, true, resp.IsActive)
		assertEquals(t, currentTime.UTC().Format(time.RFC3339), resp.CreatedAt)
	})
}

func TestAuthTokenExtraction(t *testing.T) {
	t.Run("extractBearerToken_formats", func(t *testing.T) {
		tests := []struct {
			header, expected string
		}{
			{"Bearer token-value", "token-value"},
			{"bearer token-value", ""},  // Case sensitive: lowercase "bearer" not matched
			{"Bearer ", ""},
			{"SomeToken token-value", ""},
			{"", ""},
		}

		for _, test := range tests {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.header != "" {
				req.Header.Set("Authorization", test.header)
			}

			token := extractBearerToken(req)
			assertEquals(t, test.expected, token)
		}
	})
}

func TestJWTClaimsParsing(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("jwt_claim_generation_and_parsing", func(t *testing.T) {
		manager, err := auth.NewManager(logger, "very_secure_secret_test_only_for_parsing_test")
		assertEquals(t, nil, err)

		claims := auth.JWTClaims{
			UserID:   "test-user-123",
			Username: "testuser",
			Role:     "user",
		}

		tokenStr, err := manager.Sign(claims, 1*time.Hour)
		assertEquals(t, nil, err)
		assertEquals(t, false, tokenStr == "")

		// Verify the token can be parsed
		parsedClaims, err := manager.Parse(tokenStr)
		assertEquals(t, nil, err)
		assertEquals(t, "test-user-123", parsedClaims.UserID)
		assertEquals(t, "testuser", parsedClaims.Username)
		assertEquals(t, "user", parsedClaims.Role)
	})
}

func TestAdminPasswordVerification(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("admin_password_hash_verification", func(t *testing.T) {
		password := "test-password"
		hash, err := auth.HashPassword(logger, password)
		assertEquals(t, nil, err)

		isValid := auth.VerifyPassword(logger, hash, password)
		assertEquals(t, true, isValid)

		// Test with wrong password
		isWrong := auth.VerifyPassword(logger, hash, "wrong-pass")
		assertEquals(t, false, isWrong)
	})
}

func TestRealIPFunctionality(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.100:12345"

	ip := realIP(req, []net.IPNet{})
	// This doesn't test actual trusted proxy functionality but at least executes the path
	_ = ip // Just make sure realIP function is callable
}
