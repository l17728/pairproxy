package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
	"github.com/pairproxy/pairproxy/internal/quota"
	"github.com/pairproxy/pairproxy/internal/tap"
)

// newSProxyWithQuota 创建带配额检查的 SProxy（用于集成测试）。
func newSProxyWithQuota(
	t *testing.T,
	mockLLMURL string,
	userID, groupID string,
	dailyLimit *int64,
	existingTokens int,
) (*SProxy, *auth.Manager) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	// 创建分组（带限额）
	groupRepo := db.NewGroupRepo(gormDB, logger)
	grp := &db.Group{ID: groupID, Name: groupID, DailyTokenLimit: dailyLimit}
	if err := groupRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}

	// 创建用户
	userRepo := db.NewUserRepo(gormDB, logger)
	gid := groupID
	user := &db.User{ID: userID, Username: userID, PasswordHash: "x", GroupID: &gid, IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// 预置已有用量
	if existingTokens > 0 {
		writer.Record(db.UsageRecord{
			RequestID:    "pre-existing",
			UserID:       userID,
			InputTokens:  existingTokens,
			OutputTokens: 0,
			StatusCode:   200,
			CreatedAt:    time.Now(),
		})
		cancel()
		writer.Wait()

		// 重建 writer（已 cancel，需新 ctx）
		ctx2, cancel2 := context.WithCancel(context.Background())
		writer = db.NewUsageWriter(gormDB, logger, 100, time.Minute)
		writer.Start(ctx2)
		t.Cleanup(func() { cancel2(); writer.Wait() })
	} else {
		t.Cleanup(func() { cancel(); writer.Wait() })
	}

	// 创建 SProxy
	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: mockLLMURL, APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	// 设置配额检查器
	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(time.Minute)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	return sp, jwtMgr
}

// ---------------------------------------------------------------------------
// TestSProxyQuotaNotExceeded — 未超限时请求正常转发
// ---------------------------------------------------------------------------

func TestSProxyQuotaNotExceeded(t *testing.T) {
	daily := int64(10000)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse := tap.BuildAnthropicSSE(100, 50, []string{"hello"})
		_, _ = io.WriteString(w, sse)
	}))
	defer mockLLM.Close()

	sp, jwtMgr := newSProxyWithQuota(t, mockLLM.URL, "u-ok", "g-ok", &daily, 500)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-ok", Username: "u-ok"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-3","messages":[],"stream":true}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestSProxyQuotaExceeded — 超限时返回 429
// ---------------------------------------------------------------------------

func TestSProxyQuotaExceeded(t *testing.T) {
	daily := int64(100) // 100 token 上限

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("LLM should not be reached when quota exceeded")
		w.WriteHeader(http.StatusOK)
	}))
	defer mockLLM.Close()

	// 预置 200 tokens（超出 100 限额）
	sp, jwtMgr := newSProxyWithQuota(t, mockLLM.URL, "u-over", "g-over", &daily, 200)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-over", Username: "u-over"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("X-RateLimit-Limit should be set on 429 response")
	}
	if rr.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset should be set on 429 response")
	}
}

// ---------------------------------------------------------------------------
// TestSProxyQuotaNoGroup — 无分组用户不受配额限制
// ---------------------------------------------------------------------------

func TestSProxyQuotaNoGroup(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	// 用户无分组
	userRepo := db.NewUserRepo(gormDB, logger)
	_ = userRepo.Create(&db.User{ID: "u-free", Username: "u-free", PasswordHash: "x", IsActive: true})

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{{URL: mockLLM.URL, APIKey: "key"}})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	usageRepo := db.NewUsageRepo(gormDB, logger)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quota.NewQuotaCache(time.Minute))
	sp.SetQuotaChecker(checker)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-free", Username: "u-free"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (no group = no quota limit)", rr.Code)
	}
}
