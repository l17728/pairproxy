// Package e2e 包含 sproxy 的端到端测试。
//
// 与单元测试/集成测试的区别：
//   - 使用真实的 TCP 服务器（httptest.NewServer），而非 httptest.NewRecorder
//   - 使用真实的 http.Client，经过完整的 HTTP 传输层（headers、body、连接管理）
//   - 每个测试都构建完整的 sproxy 处理链（Auth → Quota → ReverseProxy → DB write）
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/tap"
)

// ---------------------------------------------------------------------------
// 测试环境构建
// ---------------------------------------------------------------------------

// e2eEnv 封装完整的 sproxy 测试环境。
type e2eEnv struct {
	Server    *httptest.Server // 真实 TCP 服务器
	Client    *http.Client     // HTTP 客户端
	JWTMgr   *auth.Manager
	gormDB    *gorm.DB
	Writer    *db.UsageWriter
	UsageRepo *db.UsageRepo
	cancel    context.CancelFunc
	t         *testing.T
}

// setupE2E 创建带完整配额检查的 sproxy 测试环境。
func setupE2E(t *testing.T, mockLLMURL string) *e2eEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "e2e-secret-key")
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
	writer := db.NewUsageWriter(gormDB, logger, 200, 30*time.Second)
	writer.Start(ctx)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockLLMURL, APIKey: "e2e-real-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(5 * time.Second)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	// 与 cmd/sproxy/main.go 一致：/health 走独立处理器，其余走 sp.Handler()（含 Auth 中间件）
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", sp.HealthHandler())
	mux.Handle("/", sp.Handler())

	srv := httptest.NewServer(mux)

	env := &e2eEnv{
		Server:    srv,
		Client:    &http.Client{Timeout: 10 * time.Second},
		JWTMgr:   jwtMgr,
		gormDB:    gormDB,
		Writer:    writer,
		UsageRepo: usageRepo,
		cancel:    cancel,
		t:         t,
	}

	t.Cleanup(func() {
		srv.Close()
		cancel()
		writer.Wait()
	})

	return env
}

// createUser 在数据库中创建用户（可选分组），返回已签发的 JWT。
// dailyLimit 或 rpm 为 nil 时表示不限制。
func (e *e2eEnv) createUser(userID, groupID string, dailyLimit *int64, rpm *int) string {
	e.t.Helper()
	logger := zaptest.NewLogger(e.t)
	userRepo := db.NewUserRepo(e.gormDB, logger)
	groupRepo := db.NewGroupRepo(e.gormDB, logger)

	if groupID != "" {
		grp := &db.Group{
			ID:                groupID,
			Name:              groupID,
			DailyTokenLimit:   dailyLimit,
			RequestsPerMinute: rpm,
		}
		_ = groupRepo.Create(grp) // 忽略重复创建错误

		gid := groupID
		user := &db.User{
			ID: userID, Username: userID, PasswordHash: "x",
			GroupID: &gid, IsActive: true,
		}
		if err := userRepo.Create(user); err != nil {
			e.t.Fatalf("Create user %s: %v", userID, err)
		}
	} else {
		user := &db.User{ID: userID, Username: userID, PasswordHash: "x", IsActive: true}
		if err := userRepo.Create(user); err != nil {
			e.t.Fatalf("Create user %s: %v", userID, err)
		}
	}

	token, err := e.JWTMgr.Sign(auth.JWTClaims{UserID: userID, Username: userID}, time.Hour)
	if err != nil {
		e.t.Fatalf("Sign JWT for %s: %v", userID, err)
	}
	return token
}

// seedTokens 直接向数据库插入预置用量记录（绕过 UsageWriter channel，立即可查）。
func (e *e2eEnv) seedTokens(userID string, inputTokens int) {
	e.t.Helper()
	log := db.UsageLog{
		RequestID:   fmt.Sprintf("seed-%s-%d", userID, time.Now().UnixNano()),
		UserID:      userID,
		InputTokens: inputTokens,
		TotalTokens: inputTokens,
		StatusCode:  200,
		CreatedAt:   time.Now().UTC(),
	}
	if err := e.gormDB.Create(&log).Error; err != nil {
		e.t.Fatalf("seed tokens for %s: %v", userID, err)
	}
}

// doRequest 向 sproxy 发送 POST /v1/messages，返回 *http.Response（调用方负责关闭 body）。
func (e *e2eEnv) doRequest(token, body string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.Server.URL+"/v1/messages",
		strings.NewReader(body))
	if err != nil {
		e.t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.Client.Do(req)
	if err != nil {
		e.t.Fatalf("Do request: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// E2E Test 1：多租户配额隔离
// ---------------------------------------------------------------------------
//
// 场景：
//   - 用户 A（组 a，限额 500 token）已使用 600 token（超限）
//   - 用户 B（组 b，限额 500 token）无使用记录
//
// 期望：
//   - A → 429（quota_exceeded），响应头含 X-RateLimit-Reset
//   - B → 200，A 的超限不影响 B

func TestE2EMultiTenantQuotaIsolation(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			`{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)

	daily := int64(500)
	tokenA := env.createUser("quota-user-a", "quota-group-a", &daily, nil)
	tokenB := env.createUser("quota-user-b", "quota-group-b", &daily, nil)

	// 预置 A 已超限（600 > 500）
	env.seedTokens("quota-user-a", 600)

	body := `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`

	// A 的请求 → 应被拒绝
	respA := env.doRequest(tokenA, body)
	defer respA.Body.Close()

	if respA.StatusCode != http.StatusTooManyRequests {
		t.Errorf("user A: status = %d, want 429 (quota exceeded)", respA.StatusCode)
	}
	var errBody map[string]interface{}
	_ = json.NewDecoder(respA.Body).Decode(&errBody)
	if errType, _ := errBody["error"].(string); errType != "quota_exceeded" {
		t.Errorf("user A: error = %q, want quota_exceeded; body: %v", errType, errBody)
	}
	if respA.Header.Get("X-RateLimit-Reset") == "" {
		t.Error("user A: X-RateLimit-Reset header should be present on 429")
	}

	// B 的请求 → 应正常通过
	respB := env.doRequest(tokenB, body)
	defer respB.Body.Close()

	if respB.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(respB.Body)
		t.Errorf("user B: status = %d, want 200 (should not be affected by A's quota); body: %s",
			respB.StatusCode, bodyBytes)
	}
}

// ---------------------------------------------------------------------------
// E2E Test 2：Streaming 客户端中断优雅处理
// ---------------------------------------------------------------------------
//
// 场景：
//   - Mock LLM 发送 message_start 后暂停（等待客户端）
//   - 客户端读到第一个 SSE 事件后关闭连接（模拟 Ctrl+C）
//
// 期望：
//   - sproxy 不 panic，不死锁
//   - 中断后 sproxy 仍能响应健康检查（200）

func TestE2EStreamingAbortGraceful(t *testing.T) {
	clientAborted := make(chan struct{})

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement Flusher")
			return
		}

		// 发送第一个 SSE 事件，让客户端有东西可读
		_, _ = fmt.Fprintf(w,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":0}}}\n\n")
		flusher.Flush()

		// 等待客户端断开（或 context 取消）
		select {
		case <-clientAborted:
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}

		// 尝试继续写（客户端已断开，写入静默失败）
		_, _ = fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)
	token := env.createUser("abort-user", "", nil, nil)

	req, _ := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/messages",
		strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := env.Client.Do(req)
	if err != nil {
		t.Fatalf("Do streaming request: %v", err)
	}

	// 读取第一行 SSE 事件，然后中止
	scanner := bufio.NewScanner(resp.Body)
	gotFirstEvent := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "message_start") {
			gotFirstEvent = true
			break
		}
	}
	if !gotFirstEvent {
		t.Log("warning: did not receive message_start before abort (may be too fast)")
	}

	// 关闭连接（模拟客户端中止）
	resp.Body.Close()
	close(clientAborted)

	// 给服务端处理断开事件留一点时间
	time.Sleep(200 * time.Millisecond)

	// 关键验证：服务端仍然存活
	healthResp, err := env.Client.Get(env.Server.URL + "/health")
	if err != nil {
		t.Fatalf("health check after abort failed (server may have panicked): %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Errorf("health check: status = %d, want 200 (server alive after client abort)", healthResp.StatusCode)
	}

	// 进一步验证：再发一个正常请求，确保服务端能正常处理
	token2 := env.createUser("abort-user2", "", nil, nil)
	resp2 := env.doRequest(token2, `{}`)
	defer resp2.Body.Close()
	// 不关心状态码（LLM 可能返回502因为流已关闭），关键是服务端有响应
	if resp2.StatusCode == 0 {
		t.Error("sproxy failed to respond after client abort (possible deadlock)")
	}
	t.Logf("post-abort request status = %d (server is responsive)", resp2.StatusCode)
}

// ---------------------------------------------------------------------------
// E2E Test 3：每用户 RPM 独立限速
// ---------------------------------------------------------------------------
//
// 场景：
//   - 用户 C（rpm=2）连续发 3 个请求
//   - 用户 D（rpm=2）同时发 1 个请求
//
// 期望：
//   - C 第 1、2 个请求 → 200；第 3 个 → 429（rate_limit）
//   - D 的请求 → 200（C 的限速不影响 D）

func TestE2EConcurrentRPMIsolation(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			`{"id":"msg","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)

	rpm := 2
	tokenC := env.createUser("rpm-user-c", "rpm-group-c", nil, &rpm)
	tokenD := env.createUser("rpm-user-d", "rpm-group-d", nil, &rpm)

	body := `{"model":"claude-3","messages":[{"role":"user","content":"ping"}]}`

	// C 串行发 3 个请求（保证第 3 个确实超出 rpm=2 的限额）
	cStatus := make([]int, 3)
	for i := range cStatus {
		resp := env.doRequest(tokenC, body)
		cStatus[i] = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// D 并发发 1 个请求
	var (
		dStatus int
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := env.doRequest(tokenD, body)
		dStatus = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	wg.Wait()

	// C 的前 2 个请求应通过
	if cStatus[0] != http.StatusOK {
		t.Errorf("user C req 1: status = %d, want 200", cStatus[0])
	}
	if cStatus[1] != http.StatusOK {
		t.Errorf("user C req 2: status = %d, want 200", cStatus[1])
	}
	// C 第 3 个请求应被限速
	if cStatus[2] != http.StatusTooManyRequests {
		t.Errorf("user C req 3: status = %d, want 429 (RPM exceeded)", cStatus[2])
	}

	// D 不受 C 影响
	if dStatus != http.StatusOK {
		t.Errorf("user D: status = %d, want 200 (must not be affected by C's RPM)", dStatus)
	}
}

// ---------------------------------------------------------------------------
// E2E Test 4：Streaming Token 端到端写入验证
// ---------------------------------------------------------------------------
//
// 场景：通过真实 HTTP 客户端发送 streaming 请求，Mock LLM 返回完整 SSE 序列。
//
// 期望：
//   - 响应实时到达客户端（逐行读取 SSE 成功）
//   - 认证头在 LLM 侧已被正确替换（X-PairProxy-Auth 消失，Authorization 注入真实 key）
//   - usage_logs 中写入正确的 input/output token

func TestE2EStreamingTokenEndToEnd(t *testing.T) {
	const (
		wantInput  = 250
		wantOutput = 80
	)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer e2e-real-key" {
			t.Errorf("LLM received Authorization = %q, want 'Bearer e2e-real-key'", got)
		}
		if r.Header.Get("X-PairProxy-Auth") != "" {
			t.Error("X-PairProxy-Auth should be stripped before forwarding to LLM")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sse := tap.BuildAnthropicSSE(wantInput, wantOutput, []string{"Hello", " world"})
		_, _ = io.WriteString(w, sse)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)
	token := env.createUser("stream-token-user", "", nil, nil)

	req, _ := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := env.Client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// 逐行读取 SSE，收集关键事件
	var gotStart, gotStop bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "message_start") {
			gotStart = true
		}
		if strings.Contains(line, "message_stop") {
			gotStop = true
			break
		}
	}
	if !gotStart {
		t.Error("SSE stream: missing message_start event")
	}
	if !gotStop {
		t.Error("SSE stream: missing message_stop event")
	}

	// 等待异步 token 写入完成
	env.cancel()
	env.Writer.Wait()

	logs, err := env.UsageRepo.Query(db.UsageFilter{UserID: "stream-token-user", Limit: 10})
	if err != nil {
		t.Fatalf("Query usage logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", logs[0].InputTokens, wantInput)
	}
	if logs[0].OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", logs[0].OutputTokens, wantOutput)
	}
	if !logs[0].IsStreaming {
		t.Error("IsStreaming should be true for SSE request")
	}
}
