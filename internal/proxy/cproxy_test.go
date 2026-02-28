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
