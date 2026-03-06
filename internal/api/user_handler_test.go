package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// injectClaims 将 JWTClaims 注入 context（测试用）
func injectClaims(r *http.Request, claims *auth.JWTClaims) *http.Request {
	ctx := context.WithValue(r.Context(), userCtxKeyClaims, claims)
	return r.WithContext(ctx)
}

func TestUserHandler_QuotaStatus(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	// 创建测试用户
	user := &db.User{ID: "u1", Username: "alice", PasswordHash: "dummy", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 插入今日用量
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	log := db.UsageLog{
		RequestID:    "r1",
		UserID:       user.ID,
		InputTokens:  1000,
		OutputTokens: 500,
		CreatedAt:    todayStart.Add(time.Hour),
	}
	if err := gormDB.Create(&log).Error; err != nil {
		t.Fatalf("Create log: %v", err)
	}

	handler := NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	claims := &auth.JWTClaims{UserID: user.ID, Username: user.Username}
	req := injectClaims(httptest.NewRequest(http.MethodGet, "/api/user/quota-status", nil), claims)

	rr := httptest.NewRecorder()
	handler.handleQuotaStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp userQuotaResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if resp.DailyUsed != 1500 {
		t.Errorf("DailyUsed = %d, want 1500", resp.DailyUsed)
	}
	if resp.DailyLimit != 0 {
		t.Errorf("DailyLimit = %d, want 0 (no group)", resp.DailyLimit)
	}
}

func TestUserHandler_QuotaStatus_WithGroup(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	// 创建分组（含配额）
	dailyLim := int64(50000)
	monthlyLim := int64(1000000)
	rpmLim := 10
	group := &db.Group{ID: "g1", Name: "standard", DailyTokenLimit: &dailyLim, MonthlyTokenLimit: &monthlyLim, RequestsPerMinute: &rpmLim}
	if err := gormDB.Create(group).Error; err != nil {
		t.Fatalf("Create group: %v", err)
	}

	// 创建用户（属于分组）
	groupID := "g1"
	user := &db.User{ID: "u2", Username: "bob", PasswordHash: "dummy", IsActive: true, GroupID: &groupID}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	handler := NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	claims := &auth.JWTClaims{UserID: user.ID, Username: user.Username}
	req := injectClaims(httptest.NewRequest(http.MethodGet, "/api/user/quota-status", nil), claims)

	rr := httptest.NewRecorder()
	handler.handleQuotaStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp userQuotaResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if resp.DailyLimit != 50000 {
		t.Errorf("DailyLimit = %d, want 50000", resp.DailyLimit)
	}
	if resp.MonthlyLimit != 1000000 {
		t.Errorf("MonthlyLimit = %d, want 1000000", resp.MonthlyLimit)
	}
	if resp.RPMLimit != 10 {
		t.Errorf("RPMLimit = %d, want 10", resp.RPMLimit)
	}
}

func TestUserHandler_UsageHistory(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	user := &db.User{ID: "u1", Username: "alice", PasswordHash: "dummy", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 插入 3 天的用量（不同用户，验证隔离）
	now := time.Now()
	for i := 0; i < 3; i++ {
		day := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)
		// u1 的记录
		log1 := db.UsageLog{
			RequestID:    "r-u1-" + string(rune('a'+i)),
			UserID:       "u1",
			InputTokens:  (i + 1) * 100,
			OutputTokens: (i + 1) * 50,
			CreatedAt:    day.Add(time.Hour),
		}
		log2 := db.UsageLog{
			RequestID:   "r-u2-" + string(rune('a'+i)),
			UserID:      "u2",
			InputTokens: 9999,
			CreatedAt:   day.Add(2 * time.Hour),
		}
		for _, l := range []db.UsageLog{log1, log2} {
			if err := gormDB.Create(&l).Error; err != nil {
				t.Fatalf("Create log: %v", err)
			}
		}
	}

	handler := NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)

	claims := &auth.JWTClaims{UserID: "u1", Username: "alice"}
	req := injectClaims(
		httptest.NewRequest(http.MethodGet, "/api/user/usage-history?days=7", nil),
		claims,
	)

	rr := httptest.NewRecorder()
	handler.handleUsageHistory(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp usageHistoryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(resp.History) != 3 {
		t.Errorf("expected 3 days for u1, got %d", len(resp.History))
	}

	// 验证数据隔离：u2 的数据不应出现（最大 InputTokens 应该是 300）
	for _, row := range resp.History {
		if row.InputTokens >= 9999 {
			t.Errorf("u2 data leaked into u1 history: InputTokens = %d", row.InputTokens)
		}
	}
}

func TestUserHandler_Unauthorized(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, _ := db.Open(logger, ":memory:")
	db.Migrate(logger, gormDB)

	handler := NewUserHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
	)

	// 请求没有注入 claims（模拟未经认证）
	req := httptest.NewRequest(http.MethodGet, "/api/user/quota-status", nil)
	rr := httptest.NewRecorder()
	handler.handleQuotaStatus(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestUserHandler_RequireUser_InvalidToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, _ := db.Open(logger, ":memory:")
	db.Migrate(logger, gormDB)

	handler := NewUserHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		db.NewUsageRepo(gormDB, logger),
	)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 无效 token
	req := httptest.NewRequest(http.MethodGet, "/api/user/quota-status", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}
