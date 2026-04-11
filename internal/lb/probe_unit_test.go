package lb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ─── okForDiscovery 表驱动测试 ────────────────────────────────────────────────

func TestProbeResult_OkForDiscovery(t *testing.T) {
	withAuth := map[int]bool{
		http.StatusOK:           true,
		http.StatusUnauthorized: true,
		http.StatusForbidden:    true,
	}

	cases := []struct {
		name   string
		result probeResult
		want   bool
	}{
		{
			name:   "401 in OKStatuses → discovery ok",
			result: probeResult{method: &ProbeMethod{OKStatuses: withAuth}, status: 401},
			want:   true,
		},
		{
			name:   "403 in OKStatuses → discovery ok",
			result: probeResult{method: &ProbeMethod{OKStatuses: withAuth}, status: 403},
			want:   true,
		},
		{
			name:   "200 in OKStatuses → discovery ok",
			result: probeResult{method: &ProbeMethod{OKStatuses: withAuth}, status: 200},
			want:   true,
		},
		{
			name:   "404 not in OKStatuses → discovery fail",
			result: probeResult{method: &ProbeMethod{OKStatuses: withAuth}, status: 404},
			want:   false,
		},
		{
			name:   "connection error (err != nil, status=0) → discovery fail",
			result: probeResult{method: &ProbeMethod{OKStatuses: withAuth}, err: errors.New("refused"), status: 0},
			want:   false,
		},
		{
			name:   "nil OKStatuses, 200 → ok",
			result: probeResult{method: &ProbeMethod{}, status: 200},
			want:   true,
		},
		{
			name:   "nil OKStatuses, 401 → not ok (only 200 accepted)",
			result: probeResult{method: &ProbeMethod{}, status: 401},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.result.okForDiscovery()
			if got != tc.want {
				t.Errorf("okForDiscovery() = %v, want %v (status=%d)", got, tc.want, tc.result.status)
			}
		})
	}
}

// TestProbeResult_DiscoveryVsHeartbeat_401Semantics 验证发现阶段与心跳阶段对 401 的核心语义差异。
// 发现阶段：401 = "端点存在，有认证机制" → okForDiscovery() = true
// 心跳阶段（有凭证）：401 = "API Key 无效" → okWithAuth(true) = false
// 心跳阶段（无凭证）：401 = "服务在线，需要认证" → okWithAuth(false) = true
func TestProbeResult_DiscoveryVsHeartbeat_401Semantics(t *testing.T) {
	okStatuses := map[int]bool{
		http.StatusOK:           true,
		http.StatusUnauthorized: true,
	}
	r := probeResult{
		method: &ProbeMethod{OKStatuses: okStatuses},
		status: http.StatusUnauthorized,
	}

	if !r.okForDiscovery() {
		t.Error("okForDiscovery() should return true for 401 (endpoint exists)")
	}
	if r.okWithAuth(true) {
		t.Error("okWithAuth(hasCredential=true) should return false for 401 (key invalid)")
	}
	if !r.okWithAuth(false) {
		t.Error("okWithAuth(hasCredential=false) should return true for 401 (service online, no auth configured)")
	}
}

// ─── selectMethods 表驱动测试 ─────────────────────────────────────────────────

func TestSelectMethods(t *testing.T) {
	cases := []struct {
		provider         string
		wantFirstHint    string // 期望第一个策略的 ProviderHint
		wantGenericCount int    // 期望通用策略数量（ProviderHint == ""）
		wantSpecific     bool   // 是否期望存在 provider 专属策略
	}{
		{
			provider:         "anthropic",
			wantFirstHint:    "anthropic", // anthropic 专属策略排在前面
			wantGenericCount: 3,            // GET /health, GET /v1/models, POST /v1/chat/completions
			wantSpecific:     true,
		},
		{
			provider:         "ANTHROPIC", // 大小写不敏感
			wantFirstHint:    "anthropic",
			wantGenericCount: 3,
			wantSpecific:     true,
		},
		{
			provider:         "openai",
			wantFirstHint:    "", // 无 openai 专属策略，第一个是通用策略
			wantGenericCount: 3,
			wantSpecific:     false,
		},
		{
			provider:         "",
			wantFirstHint:    "", // 空 provider，只有通用策略
			wantGenericCount: 3,
			wantSpecific:     false,
		},
		{
			provider:         "unknown_vendor",
			wantFirstHint:    "", // 无专属策略，第一个是通用策略
			wantGenericCount: 3,
			wantSpecific:     false,
		},
	}

	for _, tc := range cases {
		t.Run("provider="+tc.provider, func(t *testing.T) {
			methods := selectMethods(tc.provider)
			if len(methods) == 0 {
				t.Fatal("selectMethods returned empty list")
			}
			if methods[0].ProviderHint != tc.wantFirstHint {
				t.Errorf("first method ProviderHint = %q, want %q", methods[0].ProviderHint, tc.wantFirstHint)
			}

			genericCount := 0
			hasSpecific := false
			for _, m := range methods {
				if m.ProviderHint == "" {
					genericCount++
				} else {
					hasSpecific = true
				}
			}
			if genericCount != tc.wantGenericCount {
				t.Errorf("generic method count = %d, want %d", genericCount, tc.wantGenericCount)
			}
			if hasSpecific != tc.wantSpecific {
				t.Errorf("hasSpecific = %v, want %v", hasSpecific, tc.wantSpecific)
			}
		})
	}
}

// TestSelectMethods_AnthropicFirst 验证 anthropic provider 专属策略排在通用策略前面。
func TestSelectMethods_AnthropicFirst(t *testing.T) {
	methods := selectMethods("anthropic")

	// 所有专属策略必须排在通用策略之前
	seenGeneric := false
	for _, m := range methods {
		if m.ProviderHint == "" {
			seenGeneric = true
		} else if seenGeneric {
			t.Errorf("provider-specific method %q appears after a generic method — wrong ordering", m.Name)
		}
	}
}

// ─── buildProbeURL 边界测试 ───────────────────────────────────────────────────

func TestBuildProbeURL(t *testing.T) {
	cases := []struct {
		addr, path, want string
	}{
		// 基本拼接
		{"http://host", "/health", "http://host/health"},
		{"https://api.openai.com", "/v1/models", "https://api.openai.com/v1/models"},

		// 末尾斜线处理
		{"http://host/", "/health", "http://host/health"},
		{"http://host//", "/health", "http://host/health"},

		// path 无前导斜线：自动补充
		{"http://host", "health", "http://host/health"},

		// addr 含路径前缀，path 与之重叠 → 去重
		{"https://host/openai/v1", "/v1/models", "https://host/openai/v1/models"},
		{"https://api.modelarts-maas.com/openai/v1", "/v1/models", "https://api.modelarts-maas.com/openai/v1/models"},
		{"https://api.example.com/v1", "/v1/models", "https://api.example.com/v1/models"},
		{"https://host/v1", "/v1/models", "https://host/v1/models"},

		// addr 含路径前缀，path 与之不重叠 → 直接拼接
		{"https://host/openai/v1", "/health", "https://host/openai/v1/health"},
		{"https://host/api/v2", "/v1/models", "https://host/api/v2/v1/models"},

		// 完整路径重叠（极端情况）
		{"https://host/v1/models", "/v1/models", "https://host/v1/models"},

		// 无 scheme（本地路径）
		{"", "/health", "/health"},
	}

	for _, tc := range cases {
		got := buildProbeURL(tc.addr, tc.path)
		if got != tc.want {
			t.Errorf("buildProbeURL(%q, %q)\n  got  %q\n  want %q", tc.addr, tc.path, got, tc.want)
		}
	}
}

// ─── injectCredential 独立测试 ────────────────────────────────────────────────

func TestInjectCredential(t *testing.T) {
	makeReq := func() *http.Request {
		req, _ := http.NewRequest("GET", "http://x/health", nil)
		return req
	}

	t.Run("nil cred → no headers injected", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, nil)
		if req.Header.Get("Authorization") != "" || req.Header.Get("x-api-key") != "" {
			t.Error("nil cred should not inject any headers")
		}
	})

	t.Run("empty APIKey → no headers injected", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "", Provider: "openai"})
		if req.Header.Get("Authorization") != "" {
			t.Error("empty APIKey should not inject Authorization header")
		}
	})

	t.Run("anthropic provider → x-api-key + anthropic-version", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-ant-123", Provider: "anthropic"})
		if req.Header.Get("x-api-key") != "sk-ant-123" {
			t.Errorf("x-api-key = %q, want %q", req.Header.Get("x-api-key"), "sk-ant-123")
		}
		if req.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", req.Header.Get("anthropic-version"), "2023-06-01")
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("anthropic should not set Authorization header")
		}
	})

	t.Run("ANTHROPIC (uppercase) provider → x-api-key", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-ant-abc", Provider: "ANTHROPIC"})
		if req.Header.Get("x-api-key") != "sk-ant-abc" {
			t.Errorf("ANTHROPIC provider should still use x-api-key, got %q", req.Header.Get("x-api-key"))
		}
	})

	t.Run("openai provider → Authorization: Bearer", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-proj-456", Provider: "openai"})
		if req.Header.Get("Authorization") != "Bearer sk-proj-456" {
			t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), "Bearer sk-proj-456")
		}
		if req.Header.Get("x-api-key") != "" {
			t.Error("openai should not set x-api-key header")
		}
	})

	t.Run("empty provider → default Bearer", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-789", Provider: ""})
		if req.Header.Get("Authorization") != "Bearer sk-789" {
			t.Errorf("empty provider should use Bearer, got %q", req.Header.Get("Authorization"))
		}
	})

	t.Run("key with leading/trailing spaces → trimmed before inject", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "  sk-trimme  ", Provider: "openai"})
		if req.Header.Get("Authorization") != "Bearer sk-trimme" {
			t.Errorf("key should be trimmed, got %q", req.Header.Get("Authorization"))
		}
	})

	t.Run("key with newline → skipped (header injection prevention)", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-bad\nkey", Provider: "openai"})
		// Should not inject — newline in header value is dangerous
		if req.Header.Get("Authorization") != "" {
			t.Errorf("key with newline should be rejected, got %q", req.Header.Get("Authorization"))
		}
	})

	t.Run("key with carriage return → skipped", func(t *testing.T) {
		req := makeReq()
		injectCredential(req, &TargetCredential{APIKey: "sk-bad\rkey", Provider: "openai"})
		if req.Header.Get("Authorization") != "" {
			t.Errorf("key with carriage return should be rejected, got %q", req.Header.Get("Authorization"))
		}
	})
}

// ─── ProbeCache 并发竞态测试 ──────────────────────────────────────────────────
// 此测试在 go test -race 下运行以检测竞态

func TestProbeCache_ConcurrentAccess(t *testing.T) {
	cache := NewProbeCache(10) // 极短 TTL（纳秒级），强制触发 TTL 过期路径
	method := &ProbeMethod{Name: "test", Path: "/health", HTTPMethod: "GET"}

	const goroutines = 20
	done := make(chan struct{}, goroutines*3)

	// writers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				targetID := "target-" + string(rune('0'+id%5))
				cache.set(targetID, method)
			}
		}(i)
	}
	// readers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				targetID := "target-" + string(rune('0'+id%5))
				_ = cache.get(targetID)
			}
		}(i)
	}
	// invalidators
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				targetID := "target-" + string(rune('0'+id%5))
				cache.invalidate(targetID)
			}
		}(i)
	}

	for i := 0; i < goroutines*3; i++ {
		<-done
	}
	// No panic + race detector silent = pass
}

// ─── injectCredential: whitespace-only APIKey ─────────────────────────────────

// TestInjectCredential_WhitespaceKey 确认全空白 APIKey 不注入任何认证头（Issue 4）。
// 旧版本：cred.APIKey == "" 检查在 TrimSpace 之前，"   " 通过检查后注入 "Bearer "（空 token）。
// 修复后：先 TrimSpace，再检查 key == ""，全空白 key 被拒绝。
func TestInjectCredential_WhitespaceKey(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/health", nil)

	injectCredential(req, &TargetCredential{APIKey: "   ", Provider: "openai"})

	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Errorf("whitespace-only APIKey should not inject Authorization, got %q", auth)
	}
	if xkey := req.Header.Get("x-api-key"); xkey != "" {
		t.Errorf("whitespace-only APIKey should not inject x-api-key, got %q", xkey)
	}
}

func TestInjectCredential_TabWhitespaceKey(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/health", nil)

	injectCredential(req, &TargetCredential{APIKey: "\t  \t", Provider: "anthropic"})

	if auth := req.Header.Get("x-api-key"); auth != "" {
		t.Errorf("tab-whitespace APIKey should not inject x-api-key, got %q", auth)
	}
}

// ─── buildProbeURL: query parameter handling ──────────────────────────────────

// TestBuildProbeURL_QueryParams 确认 addr 中含查询参数时不污染结果 URL（Issue 2）。
// 旧版本：将含查询参数的 addr 当成普通字符串做路径段分割，产生 "?key=abc/v1/models" 格式。
// 修复后：使用 url.Parse 提取 addr 的 path 部分，查询参数不出现在结果中。
func TestBuildProbeURL_QueryParams(t *testing.T) {
	cases := []struct {
		addr, path, want string
	}{
		{
			// 查询参数不出现在结果中，路径重叠正常检测
			addr: "http://host/v1?key=abc",
			path: "/v1/models",
			want: "http://host/v1/models",
		},
		{
			// 无重叠时也应正常拼接，查询参数被剥离
			addr: "http://host?key=abc",
			path: "/v1/models",
			want: "http://host/v1/models",
		},
		{
			// 查询参数 + 路径前缀重叠
			addr: "https://api.example.com/openai/v1?api-key=xyz",
			path: "/v1/models",
			want: "https://api.example.com/openai/v1/models",
		},
		{
			// 带 fragment 的地址（# 后的内容也应被剥离）
			addr: "http://host/v1#fragment",
			path: "/v1/models",
			want: "http://host/v1/models",
		},
	}

	for _, tc := range cases {
		got := buildProbeURL(tc.addr, tc.path)
		if got != tc.want {
			t.Errorf("buildProbeURL(%q, %q)\n  got  %q\n  want %q", tc.addr, tc.path, got, tc.want)
		}
	}
}

// ─── WithTimeout: prober client timeout consistency ────────────────────────────

// TestWithTimeout_UpdatesProberClient 确认 WithTimeout 同时更新 hc.prober 的 HTTP 客户端（Issue 10）。
// 旧版本：只更新 hc.client，hc.prober 保留初始 5s 超时，智能探活始终用默认超时。
// 修复后：WithTimeout 同时调用 NewProber(d, logger)，prober.client.Timeout == d。
func TestWithTimeout_UpdatesProberClient(t *testing.T) {
	balancer := NewWeightedRandom([]Target{})
	logger := zap.NewNop()

	const customTimeout = 15 * time.Second
	hc := NewHealthChecker(balancer, logger, WithTimeout(customTimeout))

	if hc.timeout != customTimeout {
		t.Errorf("hc.timeout = %v, want %v", hc.timeout, customTimeout)
	}
	if hc.client.Timeout != customTimeout {
		t.Errorf("hc.client.Timeout = %v, want %v", hc.client.Timeout, customTimeout)
	}
	if hc.prober.client.Timeout != customTimeout {
		t.Errorf("hc.prober.client.Timeout = %v, want %v (prober not updated)", hc.prober.client.Timeout, customTimeout)
	}
}

// TestWithTimeout_Default 确认默认超时下 prober 和 client 保持一致。
func TestWithTimeout_Default(t *testing.T) {
	balancer := NewWeightedRandom([]Target{})
	logger := zap.NewNop()

	hc := NewHealthChecker(balancer, logger)

	if hc.client.Timeout != hc.prober.client.Timeout {
		t.Errorf("default: hc.client.Timeout=%v vs hc.prober.client.Timeout=%v — mismatch",
			hc.client.Timeout, hc.prober.client.Timeout)
	}
}

// ─── isEndpointTimeout ────────────────────────────────────────────────────────

// TestIsEndpointTimeout 验证 isEndpointTimeout 能区分超时错误和连接拒绝。
func TestIsEndpointTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"context.Canceled", context.Canceled, true},
		{
			"url.Error wrapping DeadlineExceeded",
			&url.Error{Op: "Get", URL: "http://x", Err: context.DeadlineExceeded},
			true,
		},
		{
			"url.Error wrapping non-timeout (connection refused)",
			&url.Error{Op: "Get", URL: "http://x", Err: errors.New("connection refused")},
			false,
		},
		{"plain error", errors.New("some error"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isEndpointTimeout(tc.err)
			if got != tc.want {
				t.Errorf("isEndpointTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ─── UpdateHealthPaths probeCache invalidation ────────────────────────────────

// TestUpdateHealthPaths_InvalidatesCache 验证失去显式路径的 target 的探活缓存被清除。
func TestUpdateHealthPaths_InvalidatesCache(t *testing.T) {
	balancer := NewWeightedRandom([]Target{})
	hc := NewHealthChecker(balancer, zap.NewNop(),
		WithHealthPaths(map[string]string{"a": "/custom-a", "b": "/custom-b"}),
	)

	method := &ProbeMethod{Name: "test", Path: "/v1/models", HTTPMethod: "GET"}

	// 预置探活缓存（模拟 target b 曾走过智能探活）
	hc.probeCache.set("b", method)
	hc.probeCache.set("c", method) // target c 也有缓存

	// 更新：保留 a 的显式路径，删除 b；c 从未在 healthPaths 里，不受影响
	hc.UpdateHealthPaths(map[string]string{"a": "/custom-a"})

	// b 失去显式路径 → 缓存应被清除
	if hc.probeCache.get("b") != nil {
		t.Error("probe cache for target 'b' should be invalidated after losing explicit path")
	}
	// c 不在旧 healthPaths 中 → 缓存不受影响
	if hc.probeCache.get("c") == nil {
		t.Error("probe cache for target 'c' should NOT be invalidated (it was not in old healthPaths)")
	}
	// a 保留显式路径 → 缓存不受影响（这里 a 没有缓存，验证无 panic）
	if hc.probeCache.get("a") != nil {
		t.Error("probe cache for target 'a' should be nil (it had no cache entry)")
	}
}

// TestUpdateHealthPaths_NoInvalidationForNewTargets 验证新增目标不触发错误清除。
func TestUpdateHealthPaths_NoInvalidationForNewTargets(t *testing.T) {
	balancer := NewWeightedRandom([]Target{})
	hc := NewHealthChecker(balancer, zap.NewNop())

	method := &ProbeMethod{Name: "test", Path: "/health", HTTPMethod: "GET"}
	hc.probeCache.set("existing", method)

	// 增加新目标 "new" — 不应影响 "existing" 的缓存
	hc.UpdateHealthPaths(map[string]string{"new": "/custom"})

	if hc.probeCache.get("existing") == nil {
		t.Error("probe cache for 'existing' target should not be invalidated when adding new paths")
	}
}

// ─── Discover: outer context expiry semantics ─────────────────────────────────

// TestDiscover_CtxExpiry_NotUnreachable 验证整体探活 ctx 超时时返回 nil, false（预算耗尽）而非 nil, true（不可达）。
// 旧版本：isEndpointTimeout 对 context.Canceled 返回 true，ctx 超时后所有剩余方法循环 continue，
// 最终 gotHTTPResponse=false → return nil, true（错误地标记服务不可达）。
// 修复后：循环顶部检查 ctx.Err()，预算耗尽时 break 并返回 nil, false（保守：不标记不可达）。
func TestDiscover_CtxExpiry_NotUnreachable(t *testing.T) {
	logger := zap.NewNop()

	// 使用已超时的 ctx（所有 probe() 调用立即因 ctx 失败）
	ctx, cancel := context.WithTimeout(context.Background(), 1)
	cancel() // 立即取消

	prober := NewProber(5*time.Second, logger)

	// 任意合法地址（连接不会建立因为 ctx 已超时）
	found, unreachable := prober.Discover(ctx, "http://127.0.0.1:1", "t-budget", "", nil)

	// 预算耗尽 != 不可达：应返回 nil, false
	if unreachable {
		t.Error("budget-exhausted Discover should return unreachable=false (cannot conclude service is down)")
	}
	if found != nil {
		t.Errorf("expected found=nil for budget-exhausted Discover, got %v", found)
	}
}

// TestIsEndpointTimeout_DoesNotMatchCanceledForOuterCtx 文档化：
// isEndpointTimeout 对 context.Canceled 返回 true（作为单端点超时处理）；
// 外层 ctx 超时由 Discover() 循环顶部的 ctx.Err() 检查捕获，而非 isEndpointTimeout。
// 这两层机制相互配合：单端点超时→continue，整体预算耗尽→break。
func TestIsEndpointTimeout_ContextCanceled(t *testing.T) {
	// context.Canceled 被视为端点超时（HTTP 客户端可能用 Canceled 包装超时）
	if !isEndpointTimeout(context.Canceled) {
		t.Error("isEndpointTimeout(context.Canceled) should return true")
	}
	// 整体预算耗尽由 Discover() 循环中的 ctx.Err() != nil 检查处理，不依赖 isEndpointTimeout
}
