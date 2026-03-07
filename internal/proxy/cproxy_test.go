package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/lb"
)

// newTestCProxy 创建一个指向 mockSProxyURL 的 CProxy。
// 它使用一个临时目录存储 token 文件。
func newTestCProxy(t *testing.T, mockSProxyURL string, tf *auth.TokenFile) (*CProxy, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	store := auth.NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	if tf != nil {
		if err := store.Save(dir, tf); err != nil {
			t.Fatalf("Save token: %v", err)
		}
	}

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: mockSProxyURL, Addr: mockSProxyURL, Weight: 1, Healthy: true},
	})

	cp, err := NewCProxy(logger, store, dir, balancer, "")
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}
	return cp, dir
}

// validToken 创建一个 24h 有效的 TokenFile。
func validToken() *auth.TokenFile {
	return &auth.TokenFile{
		AccessToken:  "jwt-access-token",
		RefreshToken: "jwt-refresh-token",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		ServerAddr:   "http://sproxy:9000",
	}
}

// ---------------------------------------------------------------------------
// TestCProxyInjectsJWT
// ---------------------------------------------------------------------------

func TestCProxyInjectsJWT(t *testing.T) {
	var capturedAuth string
	var capturedPairProxyAuth string

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPairProxyAuth = r.Header.Get("X-PairProxy-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message"}`)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy-api-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if capturedAuth != "" {
		t.Errorf("Authorization header should be removed, got %q", capturedAuth)
	}
	if capturedPairProxyAuth != "jwt-access-token" {
		t.Errorf("X-PairProxy-Auth = %q, want %q", capturedPairProxyAuth, "jwt-access-token")
	}
}

// ---------------------------------------------------------------------------
// TestCProxyNoToken → 401 with hint message
// ---------------------------------------------------------------------------

func TestCProxyNoToken(t *testing.T) {
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("s-proxy should not be reached")
	}))
	defer mockSProxy.Close()

	// nil token = no token file
	cp, _ := newTestCProxy(t, mockSProxy.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "not_authenticated" {
		t.Errorf("error code = %q, want not_authenticated", resp.Error)
	}
	if !strings.Contains(resp.Message, "cproxy login") {
		t.Errorf("message should hint at 'cproxy login', got: %q", resp.Message)
	}
}

// ---------------------------------------------------------------------------
// TestCProxyPreservesSSE — streaming response passes through intact
// ---------------------------------------------------------------------------

func TestCProxyPreservesSSE(t *testing.T) {
	sseData := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, sseData)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "message_start") {
		t.Errorf("SSE body should contain 'message_start', got: %q", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Errorf("SSE body should contain 'message_stop', got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// TestCProxyLoadBalancing — 请求在多个 target 之间分发
// ---------------------------------------------------------------------------

func TestCProxyLoadBalancing(t *testing.T) {
	var target1Count, target2Count int

	mock1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Count++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer mock1.Close()

	mock2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target2Count++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer mock2.Close()

	logger := zaptest.NewLogger(t)
	store := auth.NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()
	_ = store.Save(dir, validToken())

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: mock1.URL, Addr: mock1.URL, Weight: 1, Healthy: true},
		{ID: mock2.URL, Addr: mock2.URL, Weight: 1, Healthy: true},
	})
	cp, err := NewCProxy(logger, store, dir, balancer, "")
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}

	// 发送 100 次请求，两个 target 都应被使用
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer dummy")
		cp.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	if target1Count == 0 {
		t.Error("target1 should have received some requests")
	}
	if target2Count == 0 {
		t.Error("target2 should have received some requests")
	}
	t.Logf("distribution: target1=%d target2=%d", target1Count, target2Count)
}

// ---------------------------------------------------------------------------
// TestCProxyAutoRefresh — P2-4: auto-refresh when token is expired
// ---------------------------------------------------------------------------

func TestCProxyAutoRefresh(t *testing.T) {
	const newAccessToken = "new-access-token-after-refresh"
	const newRefreshJTI = "new-refresh-jti"

	var capturedRefreshBody map[string]string
	var capturedPairProxyAuth string

	// Mock s-proxy that handles both /auth/refresh and normal requests.
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/auth/refresh" {
			_ = json.NewDecoder(r.Body).Decode(&capturedRefreshBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  newAccessToken,
				"refresh_token": newRefreshJTI,
				"expires_in":    86400,
			})
			return
		}
		capturedPairProxyAuth = r.Header.Get("X-PairProxy-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message"}`)
	}))
	defer mockSProxy.Close()

	// Expired token (5 minutes in the past) — NeedsRefresh() = true, IsValid() = false.
	expiredTF := &auth.TokenFile{
		AccessToken:  "old-expired-token",
		RefreshToken: "old-refresh-jti",
		ExpiresAt:    time.Now().Add(-5 * time.Minute),
		ServerAddr:   mockSProxy.URL,
		Username:     "alice",
	}

	cp, _ := newTestCProxy(t, mockSProxy.URL, expiredTF)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (refresh should succeed); body: %s", rr.Code, rr.Body.String())
	}

	// Verify refresh was called with the old refresh JTI.
	if capturedRefreshBody["refresh_token"] != "old-refresh-jti" {
		t.Errorf("refresh request body refresh_token = %q, want old-refresh-jti", capturedRefreshBody["refresh_token"])
	}

	// Verify the proxy forwarded with the NEW access token.
	if capturedPairProxyAuth != newAccessToken {
		t.Errorf("X-PairProxy-Auth after refresh = %q, want %q", capturedPairProxyAuth, newAccessToken)
	}
}

func TestCProxyAutoRefreshFailure(t *testing.T) {
	// Mock s-proxy that always fails the refresh endpoint.
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/refresh" {
			http.Error(w, "invalid refresh token", http.StatusUnauthorized)
			return
		}
		t.Error("proxy endpoint should not be reached after failed refresh")
	}))
	defer mockSProxy.Close()

	expiredTF := &auth.TokenFile{
		AccessToken:  "old-expired-token",
		RefreshToken: "bad-refresh-jti",
		ExpiresAt:    time.Now().Add(-5 * time.Minute),
		ServerAddr:   mockSProxy.URL,
		Username:     "bob",
	}

	cp, _ := newTestCProxy(t, mockSProxy.URL, expiredTF)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 after failed refresh", rr.Code)
	}
	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "not_authenticated" {
		t.Errorf("error = %q, want not_authenticated", resp.Error)
	}
	if !strings.Contains(resp.Message, "cproxy login") {
		t.Errorf("message should mention 'cproxy login', got: %q", resp.Message)
	}
}

func TestCProxyAutoRefreshTimeout(t *testing.T) {
	// done signals the mock handler to exit so mockSProxy.Close() can finish.
	done := make(chan struct{})

	// Mock s-proxy that hangs on /auth/refresh until done is closed.
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/refresh" {
			select {
			case <-done:
			case <-time.After(30 * time.Second):
			}
			return
		}
		t.Error("proxy endpoint should not be reached after refresh timeout")
	}))

	expiredTF := &auth.TokenFile{
		AccessToken:  "old-expired-token",
		RefreshToken: "jti-for-slow-server",
		ExpiresAt:    time.Now().Add(-5 * time.Minute),
		ServerAddr:   mockSProxy.URL,
		Username:     "carol",
	}

	cp, _ := newTestCProxy(t, mockSProxy.URL, expiredTF)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()

	start := time.Now()
	cp.Handler().ServeHTTP(rr, req)
	elapsed := time.Since(start)

	// Unblock the mock handler goroutine so Close() can complete.
	close(done)
	mockSProxy.Close()

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 after refresh timeout", rr.Code)
	}
	// Should have timed out within ~5s (allow some margin for test env variance).
	if elapsed > 10*time.Second {
		t.Errorf("refresh took %v; expected timeout around 5s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TestCProxyRoutingTableUpdate — 从响应头读取路由更新并应用
// ---------------------------------------------------------------------------

func TestCProxyRoutingTableUpdate(t *testing.T) {
	import_ := `{"version":5,"entries":[{"id":"sp-new","addr":"http://new:9000","weight":2,"healthy":true}]}`
	import_encoded := import_

	// 先把路由表编码成 Base64
	import_table := `eyJ2ZXJzaW9uIjo1LCJlbnRyaWVzIjpbeyJpZCI6InNwLW5ldyIsImFkZHIiOiJodHRwOi8vbmV3OjkwMDAiLCJ3ZWlnaHQiOjIsImhlYWx0aHkiOnRydWV9XX0=`
	_ = import_
	_ = import_encoded

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 注入路由更新头
		w.Header().Set("X-Routing-Version", "5")
		w.Header().Set("X-Routing-Update", import_table)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message"}`)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	// 发送请求，触发路由表更新
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	// 验证本地路由版本已更新
	if cp.routingVersion.Load() != 5 {
		t.Errorf("routingVersion = %d, want 5", cp.routingVersion.Load())
	}
}

// ---------------------------------------------------------------------------
// TestNewCProxy_CacheDir — NewCProxy cacheDir 路径（37.5% → 提升）
// ---------------------------------------------------------------------------

// TestNewCProxy_WithCacheDir_NoCache 验证 cacheDir != "" 但缓存不存在时，不影响创建。
func TestNewCProxy_WithCacheDir_NoCache(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := auth.NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir() // 空目录，无缓存文件
	cacheDir := t.TempDir()

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "sp1", Addr: "http://sp1:9000", Weight: 1, Healthy: true},
	})

	cp, err := NewCProxy(logger, store, dir, balancer, cacheDir)
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}
	if cp == nil {
		t.Fatal("NewCProxy returned nil")
	}
	// 无缓存，routingVersion 应从 0 开始
	if cp.routingVersion.Load() != 0 {
		t.Errorf("routingVersion = %d, want 0 (no cache)", cp.routingVersion.Load())
	}
}

// TestNewCProxy_WithCacheDir_CacheExists 验证 cacheDir != "" 且缓存存在时，恢复路由表。
func TestNewCProxy_WithCacheDir_CacheExists(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := auth.NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()
	cacheDir := t.TempDir()

	// 先创建一个缓存路由表
	rt := &cluster.RoutingTable{
		Version: 42,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-cached", Addr: "http://cached:9000", Weight: 1, Healthy: true},
		},
	}
	if err := rt.SaveToDir(cacheDir); err != nil {
		t.Fatalf("SaveToDir: %v", err)
	}

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "sp1", Addr: "http://sp1:9000", Weight: 1, Healthy: true},
	})

	cp, err := NewCProxy(logger, store, dir, balancer, cacheDir)
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}
	if cp == nil {
		t.Fatal("NewCProxy returned nil")
	}

	// 从缓存恢复，routingVersion 应为 42
	if cp.routingVersion.Load() != 42 {
		t.Errorf("routingVersion = %d, want 42 (from cache)", cp.routingVersion.Load())
	}

	// Balancer 应被更新为缓存中的条目
	targets := balancer.Targets()
	found := false
	for _, t2 := range targets {
		if t2.ID == "sp-cached" {
			found = true
			break
		}
	}
	if !found {
		t.Error("balancer should have been updated with cached routing entries")
	}
}

// ---------------------------------------------------------------------------
// TestBalancer — Balancer() getter (0% coverage)
// ---------------------------------------------------------------------------

func TestCProxy_Balancer_ReturnsNonNil(t *testing.T) {
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	b := cp.Balancer()
	if b == nil {
		t.Error("Balancer() should return non-nil balancer")
	}
}

// ---------------------------------------------------------------------------
// TestApplyRoutingTable — ApplyRoutingTable() (0% coverage)
// ---------------------------------------------------------------------------

func TestCProxy_ApplyRoutingTable_UpdatesTargets(t *testing.T) {
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	newRT := &cluster.RoutingTable{
		Version: 10,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-new1", Addr: "http://new1:9000", Weight: 1, Healthy: true},
			{ID: "sp-new2", Addr: "http://new2:9000", Weight: 2, Healthy: true},
		},
	}

	cp.ApplyRoutingTable(newRT)

	// routingVersion 应更新为 10
	if cp.routingVersion.Load() != 10 {
		t.Errorf("routingVersion = %d, want 10 after ApplyRoutingTable", cp.routingVersion.Load())
	}

	// Balancer 应有 2 个新目标
	targets := cp.Balancer().Targets()
	if len(targets) != 2 {
		t.Fatalf("balancer targets = %d, want 2", len(targets))
	}
	ids := map[string]bool{}
	for _, t2 := range targets {
		ids[t2.ID] = true
	}
	if !ids["sp-new1"] || !ids["sp-new2"] {
		t.Errorf("balancer should have sp-new1 and sp-new2, got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// TestProcessRoutingHeaders — processRoutingHeaders 缺失分支 (68.2% → 提升)
// ---------------------------------------------------------------------------

// makeMinimalResponse 创建用于测试的最小 http.Response。
func makeMinimalResponse(headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// TestProcessRoutingHeaders_EmptyVersion 验证 X-Routing-Version 为空时直接 return。
func TestProcessRoutingHeaders_EmptyVersion(t *testing.T) {
	cp, _ := newTestCProxy(t, "http://dummy", validToken())
	before := cp.routingVersion.Load()

	resp := makeMinimalResponse(map[string]string{}) //nolint:bodyclose // no body in response
	cp.processRoutingHeaders(resp, "req-test")

	if cp.routingVersion.Load() != before {
		t.Errorf("routingVersion should not change when X-Routing-Version is empty")
	}
}

// TestProcessRoutingHeaders_InvalidVersion 验证 X-Routing-Version 为非数字时直接 return。
func TestProcessRoutingHeaders_InvalidVersion(t *testing.T) {
	cp, _ := newTestCProxy(t, "http://dummy", validToken())
	before := cp.routingVersion.Load()

	resp := makeMinimalResponse(map[string]string{ //nolint:bodyclose
		"X-Routing-Version": "not-a-number",
	})
	cp.processRoutingHeaders(resp, "req-test")

	if cp.routingVersion.Load() != before {
		t.Errorf("routingVersion should not change with invalid version header")
	}
}

// TestProcessRoutingHeaders_ServerVersionLELocalVersion 验证 server version <= local version 时跳过。
func TestProcessRoutingHeaders_ServerVersionLELocalVersion(t *testing.T) {
	cp, _ := newTestCProxy(t, "http://dummy", validToken())
	// 先设置本地版本为 10
	cp.routingVersion.Store(10)

	// server version = 10 <= local version 10，应跳过
	resp := makeMinimalResponse(map[string]string{ //nolint:bodyclose
		"X-Routing-Version": "10",
		"X-Routing-Update":  "some-data",
	})
	cp.processRoutingHeaders(resp, "req-test")

	if cp.routingVersion.Load() != 10 {
		t.Errorf("routingVersion should remain 10 when server version <= local version")
	}
}

// TestProcessRoutingHeaders_ServerVersionGreaterButNoUpdate 验证 server version > local 但 X-Routing-Update 为空时只更新版本号。
func TestProcessRoutingHeaders_ServerVersionGreaterButNoUpdate(t *testing.T) {
	cp, _ := newTestCProxy(t, "http://dummy", validToken())
	cp.routingVersion.Store(5)

	// server version 15 > local 5，但无 X-Routing-Update
	resp := makeMinimalResponse(map[string]string{ //nolint:bodyclose
		"X-Routing-Version": "15",
		// 无 X-Routing-Update
	})
	cp.processRoutingHeaders(resp, "req-test")

	if cp.routingVersion.Load() != 15 {
		t.Errorf("routingVersion = %d, want 15 (only version updated, no full table)", cp.routingVersion.Load())
	}
}

// TestProcessRoutingHeaders_InvalidRoutingUpdate 验证 X-Routing-Update 内容无效时不更新。
func TestProcessRoutingHeaders_InvalidRoutingUpdate(t *testing.T) {
	cp, _ := newTestCProxy(t, "http://dummy", validToken())
	cp.routingVersion.Store(5)

	// server version 20 > local 5，但 X-Routing-Update 解码失败
	resp := makeMinimalResponse(map[string]string{ //nolint:bodyclose
		"X-Routing-Version": "20",
		"X-Routing-Update":  "!!!invalid-base64!!!",
	})
	cp.processRoutingHeaders(resp, "req-test")

	// 版本号不应更新（解码失败直接 return）
	if cp.routingVersion.Load() != 5 {
		t.Errorf("routingVersion = %d, want 5 (decode failure should not update version)", cp.routingVersion.Load())
	}
}

// ---------------------------------------------------------------------------
// Model header injection tests  (FR-1 + FR-2)
// ---------------------------------------------------------------------------

// TestExtractModelFromBody 验证从不同格式的 JSON body 中提取 model 字段。
func TestExtractModelFromBody(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected string
	}{
		{
			name:     "Anthropic format",
			body:     []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[],"max_tokens":1024}`),
			expected: "claude-3-5-sonnet-20241022",
		},
		{
			name:     "OpenAI format",
			body:     []byte(`{"model":"gpt-4","messages":[]}`),
			expected: "gpt-4",
		},
		{
			name:     "Ollama format",
			body:     []byte(`{"model":"llama2","prompt":"hello"}`),
			expected: "llama2",
		},
		{
			name:     "missing model field",
			body:     []byte(`{"messages":[]}`),
			expected: "",
		},
		{
			name:     "invalid JSON",
			body:     []byte(`not-json`),
			expected: "",
		},
		{
			name:     "empty body",
			body:     []byte(``),
			expected: "",
		},
		{
			name:     "nil body",
			body:     nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractModelFromBody(tt.body)
			if got != tt.expected {
				t.Errorf("extractModelFromBody() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestCProxy_InjectsModelHeader 验证 cproxy 将请求体中的 model 字段注入为
// X-PairProxy-Model 头发送给 s-proxy。
func TestCProxy_InjectsModelHeader(t *testing.T) {
	var capturedModel string

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedModel = r.Header.Get("X-PairProxy-Model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message"}`)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-3-opus-20240229","messages":[],"max_tokens":256}`))
	req.Header.Set("Authorization", "Bearer dummy-api-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if capturedModel != "claude-3-opus-20240229" {
		t.Errorf("X-PairProxy-Model = %q, want %q", capturedModel, "claude-3-opus-20240229")
	}
}

// TestCProxy_NoModelHeader_WhenMissing 验证 body 中缺少 model 字段时，
// cproxy 不注入 X-PairProxy-Model 头（保留 sproxy fallback 机制）。
func TestCProxy_NoModelHeader_WhenMissing(t *testing.T) {
	var capturedModel string

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedModel = r.Header.Get("X-PairProxy-Model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_2","type":"message"}`)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	// body 中不含 model 字段
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"messages":[],"max_tokens":256}`))
	req.Header.Set("Authorization", "Bearer dummy-api-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if capturedModel != "" {
		t.Errorf("X-PairProxy-Model = %q, want empty (no model in body)", capturedModel)
	}
}

// TestCProxy_ModelHeader_InvalidJSON 验证 body 为非法 JSON 时，
// cproxy 不注入 X-PairProxy-Model 头且请求正常转发（不阻断）。
func TestCProxy_ModelHeader_InvalidJSON(t *testing.T) {
	var capturedModel string
	requestReceived := false

	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		capturedModel = r.Header.Get("X-PairProxy-Model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_3","type":"message"}`)
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`not-valid-json`))
	req.Header.Set("Authorization", "Bearer dummy-api-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	if !requestReceived {
		t.Fatal("mock s-proxy should have received the request even with invalid JSON body")
	}
	if capturedModel != "" {
		t.Errorf("X-PairProxy-Model = %q, want empty for invalid JSON", capturedModel)
	}
}
