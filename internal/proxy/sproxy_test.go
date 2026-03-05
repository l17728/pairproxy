package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// newTestSProxy 创建一个指向 mockLLMURL 的 SProxy（使用内存 DB）。
func newTestSProxy(t *testing.T, mockLLMURL string) (*SProxy, *auth.Manager) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "test-secret-key")
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
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	// 不需要后台 goroutine，测试中不关心写入结果

	target := LLMTarget{URL: mockLLMURL, APIKey: "real-api-key"}
	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{target})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	return sp, jwtMgr
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareValidJWT
// ---------------------------------------------------------------------------

func TestAuthMiddlewareValidJWT(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	var gotClaims *auth.JWTClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(logger, jwtMgr, inner)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("claims should be set in context")
	}
	if gotClaims.UserID != "u1" {
		t.Errorf("UserID = %q, want %q", gotClaims.UserID, "u1")
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareNoHeader → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareNoHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareExpired → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareExpired(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	// 签发 1ms TTL，在测试执行前已过期
	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u2", Username: "bob"}, time.Millisecond)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // 等待过期

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddlewareBlacklisted → 401
// ---------------------------------------------------------------------------

func TestAuthMiddlewareBlacklisted(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u3", Username: "charlie"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Parse to get JTI, then blacklist it
	claims, err := jwtMgr.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	jwtMgr.Blacklist(claims.JTI, claims.ExpiresAt.Time)

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAuthMiddleware_BearerToken — OpenAI 格式客户端认证
// ---------------------------------------------------------------------------

func TestAuthMiddleware_BearerTokenValid(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	var gotClaims *auth.JWTClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(logger, jwtMgr, inner)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u-openai", Username: "openai-user"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("claims should be set in context")
	}
	if gotClaims.UserID != "u-openai" {
		t.Errorf("UserID = %q, want %q", gotClaims.UserID, "u-openai")
	}
}

func TestAuthMiddleware_BearerTokenInvalid(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	handler := AuthMiddleware(logger, jwtMgr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer invalid-jwt-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestAuthMiddleware_BothHeadersPriority(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	var gotClaims *auth.JWTClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(logger, jwtMgr, inner)

	// 创建两个不同的 token
	tokenPairProxy, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-pairproxy", Username: "pairproxy-user"}, time.Hour)
	tokenBearer, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-bearer", Username: "bearer-user"}, time.Hour)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-PairProxy-Auth", tokenPairProxy)
	req.Header.Set("Authorization", "Bearer "+tokenBearer)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("claims should be set in context")
	}
	// 应该使用 X-PairProxy-Auth 的 token（优先级更高）
	if gotClaims.UserID != "u-pairproxy" {
		t.Errorf("UserID = %q, want %q (X-PairProxy-Auth should take priority)", gotClaims.UserID, "u-pairproxy")
	}
}

// ---------------------------------------------------------------------------
// TestHeaderReplacement
// ---------------------------------------------------------------------------

func TestHeaderReplacement(t *testing.T) {
	var capturedAuth string
	var capturedPairProxyAuth string

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPairProxyAuth = r.Header.Get("X-PairProxy-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message"}`)
	}))
	defer mockLLM.Close()

	sp, jwtMgr := newTestSProxy(t, mockLLM.URL)

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body := strings.NewReader(`{"model":"claude-3","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Authorization", "Bearer dummy-key")

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if capturedAuth != "Bearer real-api-key" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer real-api-key")
	}
	if capturedPairProxyAuth != "" {
		t.Errorf("X-PairProxy-Auth should be removed from upstream request, got %q", capturedPairProxyAuth)
	}
}

// ---------------------------------------------------------------------------
// TestRecoveryMiddleware
// ---------------------------------------------------------------------------

func TestRecoveryMiddleware(t *testing.T) {
	logger := zaptest.NewLogger(t)

	handler := RecoveryMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestHealthHandler
// ---------------------------------------------------------------------------

func TestHealthHandler(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockLLM.Close()

	sp, _ := newTestSProxy(t, mockLLM.URL)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	sp.HealthHandler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestSProxyOpenAICompatE2E — OpenAI 格式客户端完整链路测试
// ---------------------------------------------------------------------------

func TestSProxyOpenAICompatE2E(t *testing.T) {
	var capturedBody []byte
	var capturedAuth string

	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedBody, _ = io.ReadAll(r.Body)

		// 模拟 OpenAI streaming 响应（带 usage）
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer mockOpenAI.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, 50*time.Millisecond)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	// 创建测试用户
	userRepo := db.NewUserRepo(gormDB, logger)
	testUser := &db.User{
		ID:           "openai-user-id",
		Username:     "openai-test",
		PasswordHash: "dummy",
		IsActive:     true,
	}
	if err := userRepo.Create(testUser); err != nil {
		t.Fatalf("userRepo.Create: %v", err)
	}

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: mockOpenAI.URL, APIKey: "sk-openai-test", Provider: "openai"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	// 生成 JWT
	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: testUser.ID, Username: testUser.Username}, time.Hour)
	if err != nil {
		t.Fatalf("jwtMgr.Sign: %v", err)
	}

	// 构造 OpenAI 格式请求（流式，无 stream_options — 验证 sproxy 自动注入）
	reqBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(reqBody))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	// 验证响应
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// 验证转发到 OpenAI 的请求包含 stream_options.include_usage: true
	var forwardedReq map[string]interface{}
	if err := json.Unmarshal(capturedBody, &forwardedReq); err != nil {
		t.Fatalf("failed to parse forwarded body: %v (body: %q)", err, capturedBody)
	}

	streamOpts, ok := forwardedReq["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("stream_options not found in forwarded request; forwarded body: %s", capturedBody)
	}

	includeUsage, ok := streamOpts["include_usage"].(bool)
	if !ok || !includeUsage {
		t.Errorf("stream_options.include_usage = %v, want true", includeUsage)
	}

	// 验证 Authorization 头被替换为真实 API Key（客户端 Bearer JWT 不泄漏）
	if capturedAuth != "Bearer sk-openai-test" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer sk-openai-test")
	}

	// 等待 usage 异步写入
	time.Sleep(200 * time.Millisecond)

	// 验证 usage 记录
	usageRepo := db.NewUsageRepo(gormDB, logger)
	records, err := usageRepo.Query(db.UsageFilter{UserID: testUser.ID, Limit: 10})
	if err != nil {
		t.Fatalf("usageRepo.Query: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(records))
	}

	record := records[0]
	if record.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", record.InputTokens)
	}
	if record.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", record.OutputTokens)
	}
	if record.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", record.Model, "gpt-4")
	}
}
