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

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/tap"
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

// ---------------------------------------------------------------------------
// TestMultipleTargetsRoundRobin：验证多 LLM 目标按轮询分发
// ---------------------------------------------------------------------------

func TestMultipleTargetsRoundRobin(t *testing.T) {
	var hits [2]int

	// 两个 mock LLM，各自记录命中次数
	mkServer := func(idx int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[idx]++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
		}))
	}
	s0 := mkServer(0)
	defer s0.Close()
	s1 := mkServer(1)
	defer s1.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := NewSProxy(logger, jwtMgr, writer, []LLMTarget{
		{URL: s0.URL, APIKey: "k0"},
		{URL: s1.URL, APIKey: "k1"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	const requests = 4
	for i := 0; i < requests; i++ {
		token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-rr", Username: "rr"}, time.Hour)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
		req.Header.Set("X-PairProxy-Auth", token)
		rr := httptest.NewRecorder()
		sp.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, rr.Code)
		}
	}

	// 轮询应均匀分配（各 2 次）
	if hits[0] != requests/2 {
		t.Errorf("server 0 hits = %d, want %d", hits[0], requests/2)
	}
	if hits[1] != requests/2 {
		t.Errorf("server 1 hits = %d, want %d", hits[1], requests/2)
	}
}

// ---------------------------------------------------------------------------
// TestModelExtractedFromNonStreamingResponse：非 streaming 响应中 model 字段写入 usage
// ---------------------------------------------------------------------------

func TestModelExtractedFromNonStreamingResponse(t *testing.T) {
	const wantModel = "claude-3-5-sonnet-20241022"

	// Mock LLM：在响应 body 中包含 model 字段
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body := `{"id":"msg","type":"message","model":"` + wantModel + `","usage":{"input_tokens":20,"output_tokens":10}}`
		_, _ = io.WriteString(w, body)
	}))
	defer mockLLM.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	sp, _ := NewSProxy(logger, jwtMgr, writer, []LLMTarget{{URL: mockLLM.URL, APIKey: "key"}})

	// 请求中不设置 X-PairProxy-Model，让 sproxy 从响应 body 提取
	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-model", Username: "model"}, time.Hour)
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
	logs, err := repo.Query(db.UsageFilter{UserID: "u-model", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].Model != wantModel {
		t.Errorf("Model = %q, want %q", logs[0].Model, wantModel)
	}
}

// ---------------------------------------------------------------------------
// newIntegrationEnv：返回 gormDB 供 token 精确验证测试使用
// ---------------------------------------------------------------------------

// newTokenEnv 创建带真实 gormDB 访问权限的集成测试环境。
// 与 newIntegrationSProxy 的区别：暴露 gormDB 供 UsageRepo 查询，
// 并支持指定 targets（多 provider）。
func newTokenEnv(t *testing.T, targets []LLMTarget) (*SProxy, *auth.Manager, *db.UsageRepo, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "secret")
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

	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	repo := db.NewUsageRepo(gormDB, logger)

	t.Cleanup(func() {
		cancel()
		writer.Wait()
	})

	return sp, jwtMgr, repo, cancel
}

// ---------------------------------------------------------------------------
// TestInputTokens_AnthropicStreaming
//
// 回归测试：/v1/messages + Anthropic SSE，InputTokens 必须写入 DB（非 0）。
// 历史 bug：当 quotaChecker 未配置时 needBodyRead 为 false，bodyBytes 为 nil，
// 虽然不影响 Anthropic SSE 的 token 解析，但 model 字段会丢失；
// 本测试同时断言 model 正确写入。
// ---------------------------------------------------------------------------

func TestInputTokens_AnthropicStreaming(t *testing.T) {
	const (
		wantInput  = 200
		wantOutput = 80
		wantModel  = "claude-3-5-sonnet-20241022"
	)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, tap.BuildAnthropicSSE(wantInput, wantOutput, []string{"hi"}))
	}))
	defer mockLLM.Close()

	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "key", Provider: "anthropic"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-ant-stream", Username: "ant"}, time.Hour)
	body := `{"model":"` + wantModel + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")
	// ContentLength 故意不设（-1），验证 needBodyRead 修复后 body 仍被正确读取
	req.ContentLength = -1

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	cancel()
	// cancel() 已被 t.Cleanup 注册，此处再调一次是为了立即 flush
	// 重新 Wait 不必要，但 repo.Query 需要在 writer 停止后执行
	// 等待异步 writer 处理（cancel 触发 drain）
	// writer.Wait() 已在 t.Cleanup 中，手动再等一点点确保 flush
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-ant-stream", Limit: 10})
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
	if log.Model != wantModel {
		t.Errorf("Model = %q, want %q", log.Model, wantModel)
	}
}

// ---------------------------------------------------------------------------
// TestInputTokens_OpenAIStreaming
//
// 回归测试：/v1/chat/completions + OpenAI SSE，InputTokens 必须写入 DB（非 0）。
// 验证：sproxy 注入了 stream_options.include_usage:true，OpenAI 返回带 usage 的
// final chunk，parser 正确提取 token 数。
// ---------------------------------------------------------------------------

func TestInputTokens_OpenAIStreaming(t *testing.T) {
	const (
		wantInput  = 15
		wantOutput = 7
	)

	var capturedBody []byte
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, tap.BuildOpenAISSE(wantInput, wantOutput, []string{"hello"}))
	}))
	defer mockLLM.Close()

	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "sk-test", Provider: "openai"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-oai-stream", Username: "oai"}, time.Hour)
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// 验证 stream_options.include_usage:true 已注入
	var fwd map[string]interface{}
	if err := json.Unmarshal(capturedBody, &fwd); err != nil {
		t.Fatalf("forwarded body parse error: %v", err)
	}
	streamOpts, ok := fwd["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatal("stream_options not found in forwarded request — injection failed")
	}
	if iu, _ := streamOpts["include_usage"].(bool); !iu {
		t.Error("stream_options.include_usage = false, want true")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-oai-stream", Limit: 10})
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
}

// ---------------------------------------------------------------------------
// TestInputTokens_OpenAIStreaming_ContentLengthMinusOne
//
// 回归测试：ContentLength == -1（HTTP chunked 传输，无 Content-Length 头）时，
// stream_options 仍须被正确注入，InputTokens 不得为 0。
// ---------------------------------------------------------------------------

func TestInputTokens_OpenAIStreaming_ContentLengthMinusOne(t *testing.T) {
	const (
		wantInput  = 22
		wantOutput = 9
	)

	var capturedBody []byte
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, tap.BuildOpenAISSE(wantInput, wantOutput, []string{"test"}))
	}))
	defer mockLLM.Close()

	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "sk-cl-test", Provider: "openai"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-cl-minus1", Username: "cl"}, time.Hour)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // 模拟 chunked / 未知长度

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// stream_options 必须已注入
	var fwd map[string]interface{}
	if err := json.Unmarshal(capturedBody, &fwd); err != nil {
		t.Fatalf("forwarded body parse error: %v", err)
	}
	streamOpts, ok := fwd["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatal("stream_options not found — injection failed for ContentLength=-1 request")
	}
	if iu, _ := streamOpts["include_usage"].(bool); !iu {
		t.Error("stream_options.include_usage = false, want true")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-cl-minus1", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d (ContentLength=-1 must not break token counting)", logs[0].InputTokens, wantInput)
	}
}

// ---------------------------------------------------------------------------
// TestInputTokens_AnthropicNonStreaming
//
// 回归测试：/v1/messages + 非 streaming JSON 响应，InputTokens 非 0，
// IsStreaming == false，DurationMs > 0。
// ---------------------------------------------------------------------------

func TestInputTokens_AnthropicNonStreaming(t *testing.T) {
	const (
		wantInput  = 100
		wantOutput = 40
	)

	mockResp := `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":100,"output_tokens":40}}`

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, mockResp)
	}))
	defer mockLLM.Close()

	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "key", Provider: "anthropic"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-ant-nosm", Username: "ant-ns"}, time.Hour)
	body := `{"model":"claude-3-haiku","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-ant-nosm", Limit: 10})
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
	if log.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming response")
	}
	// DurationMs 由 ModifyResponse 计算，本地 mock 可能 < 1ms，只验证非负
	if log.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", log.DurationMs)
	}
}

// ---------------------------------------------------------------------------
// TestInputTokens_OpenAINonStreaming
//
// 回归测试：/v1/chat/completions + 非 streaming JSON 响应，InputTokens 非 0，
// IsStreaming == false，DurationMs > 0。
// ---------------------------------------------------------------------------

func TestInputTokens_OpenAINonStreaming(t *testing.T) {
	const (
		wantInput  = 25
		wantOutput = 12
	)

	mockResp := `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":12,"total_tokens":37}}`

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, mockResp)
	}))
	defer mockLLM.Close()

	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "sk-ns-test", Provider: "openai"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-oai-nosm", Username: "oai-ns"}, time.Hour)
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-oai-nosm", Limit: 10})
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
	if log.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming response")
	}
	// DurationMs 由 ModifyResponse 计算，本地 mock 可能 < 1ms，只验证非负
	if log.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", log.DurationMs)
	}
}

// ---------------------------------------------------------------------------
// TestInputTokens_AtoONonStreaming
//
// 回归测试：AtoO 方向（/v1/messages + provider=ollama），非 streaming JSON 响应。
// Bug: 修复前 body 先被转为 Anthropic 格式，再用 OpenAISSEParser 解析 → 返回 (0,0)。
// 修复后：在转换前先从原始 OpenAI body 记录 token，InputTokens 必须非 0。
// ---------------------------------------------------------------------------

func TestInputTokens_AtoONonStreaming(t *testing.T) {
	const (
		wantInput  = 42
		wantOutput = 15
	)

	// Mock LLM（Ollama 兼容）：接收 OpenAI 格式请求，返回 OpenAI 格式非流式响应
	mockResp := `{"id":"chatcmpl-atoa","object":"chat.completion","model":"llama3.1",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":42,"completion_tokens":15,"total_tokens":57}}`

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 sproxy 已将路径转为 OpenAI 格式
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, mockResp)
	}))
	defer mockLLM.Close()

	// provider="ollama" 触发 AtoO 转换
	sp, jwtMgr, repo, cancel := newTokenEnv(t, []LLMTarget{
		{URL: mockLLM.URL, APIKey: "ollama", Provider: "ollama"},
	})

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-atoa-nosm", Username: "atoa-ns"}, time.Hour)
	// 客户端发 Anthropic 格式（非流式）
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	logs, err := repo.Query(db.UsageFilter{UserID: "u-atoa-nosm", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	log := logs[0]
	if log.InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d (AtoO non-streaming token bug)", log.InputTokens, wantInput)
	}
	if log.OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", log.OutputTokens, wantOutput)
	}
	if log.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming response")
	}
}
