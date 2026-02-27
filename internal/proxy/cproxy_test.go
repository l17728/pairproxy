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

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/lb"
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
