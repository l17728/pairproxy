package e2e_test

// quota_enforcement_e2e_test.go — E2E 测试：请求大小限制 & 并发请求限制
//
// 覆盖两个配额检查场景（之前无 E2E 覆盖）：
//
//  1. TestE2ERequestSizeQuotaEnforcement
//     用户所在分组设置了 max_tokens_per_request = 500；
//     请求体携带 max_tokens=2000 → 应被 sproxy 拒绝，返回 429。
//
//  2. TestE2EConcurrentRequestsQuotaEnforcement
//     用户所在分组设置了 concurrent_requests = 1；
//     同时发出 2 个请求 → 第二个请求应被拒绝（429 concurrent）；
//     第一个请求完成后，第三个请求应正常通过（200）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/quota"
)

// ---------------------------------------------------------------------------
// 辅助：构建带完整配额检查的轻量 sproxy 测试环境
// ---------------------------------------------------------------------------

// setupQuotaEnfE2E 创建带完整配额检查的 sproxy 测试环境（mock LLM 可从外部注入）。
func setupQuotaEnfE2E(t *testing.T, mockLLMURL string) (*httptest.Server, *auth.Manager, *db.UserRepo, *db.GroupRepo, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "quota-enf-e2e-secret")
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
		{URL: mockLLMURL, APIKey: "quota-enf-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(5 * time.Second)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", sp.HealthHandler())
	mux.Handle("/", sp.Handler())

	srv := httptest.NewServer(mux)

	grpRepo := db.NewGroupRepo(gormDB, logger)

	t.Cleanup(func() {
		srv.Close()
		cancel()
		writer.Wait()
	})

	return srv, jwtMgr, userRepo, grpRepo, cancel
}

// signToken 为 userID 签发一小时有效期的 JWT。
func signToken(t *testing.T, mgr *auth.Manager, userID string) string {
	t.Helper()
	tok, err := mgr.Sign(auth.JWTClaims{UserID: userID, Username: userID}, time.Hour)
	if err != nil {
		t.Fatalf("Sign JWT for %s: %v", userID, err)
	}
	return tok
}

// ---------------------------------------------------------------------------
// 1. TestE2ERequestSizeQuotaEnforcement
// ---------------------------------------------------------------------------
//
// 场景：
//   - 用户属于 group "size-limited"，max_tokens_per_request = 500
//   - 请求体携带 "max_tokens": 2000（超限）
//
// 期望：
//   - sproxy 在代理转发前拦截，返回 429；kind = "request_size"
//   - Mock LLM 不应收到该请求（其 handler 中会 t.Error 以检测意外调用）
func TestE2ERequestSizeQuotaEnforcement(t *testing.T) {
	llmHit := false
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmHit = true
		t.Error("LLM should NOT be called when request_size quota is exceeded")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	srv, jwtMgr, userRepo, grpRepo, _ := setupQuotaEnfE2E(t, mockLLM.URL)

	// 创建带 max_tokens_per_request 限制的分组
	maxTokens := int64(500)
	grp := &db.Group{
		ID:                  "group-size-limited",
		Name:                "size-limited",
		MaxTokensPerRequest: &maxTokens,
	}
	if err := grpRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}

	gid := "group-size-limited"
	user := &db.User{
		ID: "user-size-limited", Username: "size-limited-user",
		PasswordHash: "x", GroupID: &gid, IsActive: true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	token := signToken(t, jwtMgr, "user-size-limited")

	// 请求体携带 max_tokens=2000 > 500
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}],"max_tokens":2000}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 429 (request_size exceeded); body: %s", resp.StatusCode, bodyBytes)
		return
	}

	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}

	if kind, _ := errBody["kind"].(string); kind != "request_size" {
		t.Errorf("kind = %q, want %q; full body: %v", kind, "request_size", errBody)
	}
	if current, _ := errBody["current"].(float64); current != 2000 {
		t.Errorf("current = %v, want 2000", current)
	}
	if limit, _ := errBody["limit"].(float64); limit != 500 {
		t.Errorf("limit = %v, want 500", limit)
	}

	if llmHit {
		t.Error("LLM should not have been called")
	}
}

// ---------------------------------------------------------------------------
// 2. TestE2ERequestSizeAllowedWhenWithinLimit
// ---------------------------------------------------------------------------
//
// 场景：
//   - 同一 max_tokens_per_request = 500 的分组用户
//   - 请求体携带 max_tokens=100（不超限）
//
// 期望：
//   - 请求正常转发至 LLM，返回 200
func TestE2ERequestSizeAllowedWhenWithinLimit(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	srv, jwtMgr, userRepo, grpRepo, _ := setupQuotaEnfE2E(t, mockLLM.URL)

	maxTokens := int64(500)
	grp := &db.Group{
		ID:                  "group-size-ok",
		Name:                "size-ok",
		MaxTokensPerRequest: &maxTokens,
	}
	if err := grpRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "group-size-ok"
	user := &db.User{
		ID: "user-size-ok", Username: "size-ok-user",
		PasswordHash: "x", GroupID: &gid, IsActive: true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	token := signToken(t, jwtMgr, "user-size-ok")

	// max_tokens=100 < 500，不超限
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200 (request size within limit); body: %s", resp.StatusCode, bodyBytes)
	}
}

// ---------------------------------------------------------------------------
// 3. TestE2EConcurrentRequestsQuotaEnforcement
// ---------------------------------------------------------------------------
//
// 场景：
//   - 用户属于 group "concurrent-limited"，concurrent_requests = 1
//   - 请求 #1 发出后（Mock LLM 刻意延迟，让 #1 不立即完成）
//   - 请求 #2 立即发出 → 应被拒绝（429，kind="concurrent"）
//   - 请求 #1 完成后，请求 #3 发出 → 应正常通过（200）
func TestE2EConcurrentRequestsQuotaEnforcement(t *testing.T) {
	// 用 channel 控制 Mock LLM 的响应时机
	unblockFirst := make(chan struct{})
	firstReceived := make(chan struct{})

	callCount := 0
	var callMu sync.Mutex

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callMu.Lock()
		callCount++
		n := callCount
		callMu.Unlock()

		if n == 1 {
			// 第一个请求：通知已收到，然后阻塞等待放行信号
			close(firstReceived)
			<-unblockFirst
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	srv, jwtMgr, userRepo, grpRepo, _ := setupQuotaEnfE2E(t, mockLLM.URL)

	conc := 1
	grp := &db.Group{
		ID:                 "group-concurrent-limited",
		Name:               "concurrent-limited",
		ConcurrentRequests: &conc,
	}
	if err := grpRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "group-concurrent-limited"
	user := &db.User{
		ID: "user-concurrent", Username: "concurrent-user",
		PasswordHash: "x", GroupID: &gid, IsActive: true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	token := signToken(t, jwtMgr, "user-concurrent")

	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`

	doReq := func() *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("X-PairProxy-Auth", token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		return resp
	}

	// 请求 #1：在后台发送，Mock LLM 会阻塞它直到我们放行
	resp1Ch := make(chan *http.Response, 1)
	go func() {
		resp1Ch <- doReq() //nolint:bodyclose // body closed by receiver at resp1.Body.Close()
	}()

	// 等待 #1 到达 Mock LLM
	select {
	case <-firstReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first request to reach mock LLM")
	}

	// 请求 #2：此时 #1 正在占用唯一的并发槽 → 应被拒绝
	resp2 := doReq()
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusTooManyRequests {
		bodyBytes, _ := io.ReadAll(resp2.Body)
		t.Errorf("request #2 status = %d, want 429 (concurrent limit); body: %s", resp2.StatusCode, bodyBytes)
	} else {
		var errBody map[string]interface{}
		_ = json.NewDecoder(resp2.Body).Decode(&errBody)
		if kind, _ := errBody["kind"].(string); kind != "concurrent" {
			t.Errorf("request #2: kind = %q, want %q; body: %v", kind, "concurrent", errBody)
		}
	}

	// 放行请求 #1
	close(unblockFirst)
	resp1 := <-resp1Ch
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp1.Body)
		t.Errorf("request #1 status = %d, want 200; body: %s", resp1.StatusCode, bodyBytes)
	}

	// 请求 #3：#1 已完成，并发槽已释放 → 应正常通过
	// 给一小段时间让 sproxy 释放并发计数
	time.Sleep(50 * time.Millisecond)
	resp3 := doReq()
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp3.Body)
		t.Errorf("request #3 status = %d, want 200 (slot released); body: %s", resp3.StatusCode, bodyBytes)
	}
}

// ---------------------------------------------------------------------------
// 4. TestE2ERequestSizeNoMaxTokensField — max_tokens 字段缺失时不触发限制
// ---------------------------------------------------------------------------
//
// 场景：
//   - 用户属于设置了 max_tokens_per_request=500 的分组
//   - 请求体 *不携带* max_tokens 字段
//
// 期望：
//   - sproxy 跳过请求大小检查，请求正常转发（200）
func TestE2ERequestSizeNoMaxTokensField(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer mockLLM.Close()

	srv, jwtMgr, userRepo, grpRepo, _ := setupQuotaEnfE2E(t, mockLLM.URL)

	maxTokens := int64(500)
	grp := &db.Group{
		ID:                  "group-size-no-field",
		Name:                "size-no-field",
		MaxTokensPerRequest: &maxTokens,
	}
	if err := grpRepo.Create(grp); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	gid := "group-size-no-field"
	user := &db.User{
		ID: "user-size-no-field", Username: "size-no-field-user",
		PasswordHash: "x", GroupID: &gid, IsActive: true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	token := signToken(t, jwtMgr, "user-size-no-field")

	// 不带 max_tokens 字段
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200 (no max_tokens field → no size check); body: %s", resp.StatusCode, bodyBytes)
	}
}
