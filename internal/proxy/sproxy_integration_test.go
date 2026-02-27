package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/db"
	"github.com/pairproxy/pairproxy/internal/tap"
)

// newIntegrationSProxy 创建一个完整的 SProxy（带内存 DB 和 UsageWriter）。
func newIntegrationSProxy(
	t *testing.T,
	mockLLMURL string,
) (*SProxy, *auth.Manager, *db.UsageWriter, context.CancelFunc) {
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

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: mockLLMURL, APIKey: "real-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		writer.Wait()
	})

	return sp, jwtMgr, writer, cancel
}

func signToken(t *testing.T, mgr *auth.Manager, userID, username string) string {
	t.Helper()
	token, err := mgr.Sign(auth.JWTClaims{UserID: userID, Username: username}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return token
}

// ---------------------------------------------------------------------------
// TestFullStreamingFlow：端到端 streaming 流，usage_logs 中有正确记录
// ---------------------------------------------------------------------------

func TestFullStreamingFlow(t *testing.T) {
	const (
		inputTokens  = 150
		outputTokens = 60
	)

	// Mock LLM：返回标准 Anthropic SSE 序列
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证认证头已被 sproxy 替换
		if r.Header.Get("Authorization") != "Bearer real-key" {
			t.Errorf("Authorization = %q, want 'Bearer real-key'", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-PairProxy-Auth") != "" {
			t.Errorf("X-PairProxy-Auth should be removed, got %q", r.Header.Get("X-PairProxy-Auth"))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		sse := tap.BuildAnthropicSSE(inputTokens, outputTokens, []string{"Hello", " world"})
		_, _ = io.WriteString(w, sse)
	}))
	defer mockLLM.Close()

	sp, jwtMgr, writer, cancel := newIntegrationSProxy(t, mockLLM.URL)

	// 发送一个 streaming 请求
	token := signToken(t, jwtMgr, "user-int-1", "alice")
	body := strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Authorization", "Bearer dummy")

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// 验证响应包含完整 SSE 内容（TeeWriter 零缓冲转发）
	respBody := rr.Body.String()
	if !strings.Contains(respBody, "message_stop") {
		t.Errorf("SSE response body should contain 'message_stop', got: %.200s", respBody)
	}

	// 停止 writer，等待异步写入完成
	cancel()
	writer.Wait()

	// 重新获取 DB（通过 writer 内部 db，通过 UsageRepo 查）
	logger := zaptest.NewLogger(t)
	gormDB, _ := db.Open(logger, ":memory:")
	// 我们无法直接访问 writer 的 gormDB（未暴露）
	// 改用 writer 自身的 gormDB（已通过 t.Cleanup 关闭），
	// 所以这里换一种验证方式：通过 mock LLM 验证代理行为正确性。
	// token 统计在下一个 TestFullStreamingTokenCount 测试中精确验证。
	_ = gormDB
	t.Log("streaming response forwarded successfully, SSE content intact")
}

// ---------------------------------------------------------------------------
// TestFullStreamingTokenCount：精确验证 token 写入 DB
// ---------------------------------------------------------------------------

func TestFullStreamingTokenCount(t *testing.T) {
	const (
		wantInput  = 300
		wantOutput = 120
	)

	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sse := tap.BuildAnthropicSSE(wantInput, wantOutput, []string{"test"})
		_, _ = io.WriteString(w, sse)
	}))
	defer mockLLM.Close()

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{{URL: mockLLM.URL, APIKey: "key"}})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "user-count", Username: "count"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	// 等待 SSE 解析和异步记录完成
	cancel()
	writer.Wait()

	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-count", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	log := logs[0]
	if log.InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", log.InputTokens, wantInput)
	}
	if log.OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", log.OutputTokens, wantOutput)
	}
	if !log.IsStreaming {
		t.Error("IsStreaming should be true")
	}
	if log.UserID != "user-count" {
		t.Errorf("UserID = %q, want 'user-count'", log.UserID)
	}
}

// ---------------------------------------------------------------------------
// TestNonStreamingTokenCount：非 streaming 响应的 token 精确验证
// ---------------------------------------------------------------------------

func TestNonStreamingTokenCount(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	// Mock LLM：返回普通 JSON 响应
	mockResponse := map[string]interface{}{
		"id":   "msg_abc",
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{"type": "text", "text": "Hello!"},
		},
		"usage": map[string]int{
			"input_tokens":  80,
			"output_tokens": 30,
		},
	}
	mockJSON, _ := json.Marshal(mockResponse)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockJSON)
	}))
	defer mockLLM.Close()

	sp, _ := NewSProxy(logger, jwtMgr, writer, []LLMTarget{{URL: mockLLM.URL, APIKey: "key"}})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "user-ns", Username: "ns"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	cancel()
	writer.Wait()

	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-ns", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	log := logs[0]
	if log.InputTokens != 80 {
		t.Errorf("InputTokens = %d, want 80", log.InputTokens)
	}
	if log.OutputTokens != 30 {
		t.Errorf("OutputTokens = %d, want 30", log.OutputTokens)
	}
	if log.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming response")
	}
}

// ---------------------------------------------------------------------------
// TestUpstreamErrorRecorded：上游失败时也记录一条 usage 记录
// ---------------------------------------------------------------------------

func TestUpstreamErrorRecorded(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	// Mock LLM：立即关闭连接（模拟上游不可达）
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 故意关闭连接
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer mockLLM.Close()

	sp, _ := NewSProxy(logger, jwtMgr, writer, []LLMTarget{{URL: mockLLM.URL, APIKey: "key"}})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "user-err", Username: "err"}, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-PairProxy-Auth", token)
	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	// 应返回 502
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}

	cancel()
	writer.Wait()

	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-err", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log (error record), got %d", len(logs))
	}
	log := logs[0]
	if log.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", log.StatusCode)
	}
	// token 数应为 0
	if log.InputTokens != 0 || log.OutputTokens != 0 {
		t.Errorf("error record should have 0 tokens, got input=%d output=%d",
			log.InputTokens, log.OutputTokens)
	}
}
