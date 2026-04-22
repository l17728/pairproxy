// probe_real_test.go — 针对用户提供的真实 LLM API 端点进行智能探活测试。
//
// 运行方式（需要网络）：
//
//	go test ./internal/lb/ -run TestProbe_RealEndpoints -v -timeout 120s
//
// 正常 CI 中该测试会被跳过（需要设置 RUN_REAL_PROBE_TESTS=1）。
package lb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// realEndpoint 描述一个真实的 LLM API 端点。
type realEndpoint struct {
	name     string
	addr     string // 不含路径的 base URL（与 config.LLMTarget.URL 对应）
	provider string
	// expectUnreachable: 若为 true 则期望服务不可达（用于测试失败场景）
	expectUnreachable bool
	// expectFoundPath: 期望发现的探活路径（空=任意路径均可）
	expectFoundPath string
}

// 用户提供的真实端点列表（来自 Issue #4 讨论）
var realEndpoints = []realEndpoint{
	// ── 自建代理（有 /health）──────────────────────────────────────────────────
	{
		name:            "starfire proxy",
		addr:            "https://starfire.fox.edu.pl",
		provider:        "openai",
		expectFoundPath: "/health",
	},
	{
		name:     "anthropic.edu.pl proxy",
		addr:     "https://anthropic.edu.pl",
		provider: "anthropic",
		// Anthropic 专属策略优先：/v1/models (anthropic) 会先被发现（401=服务在线），不限定路径
	},

	// ── 华为云 MaaS（/health=404，/v1/models=400 无 auth）────────────────────
	{
		name:     "huawei cloud MaaS (openai compat)",
		addr:     "https://api.modelarts-maas.com/openai/v1",
		provider: "openai",
		// 400 (缺认证) 应被识别为"服务在线"，探活成功
	},

	// ── 小米（/health=404，/v1/models=401）───────────────────────────────────
	{
		name:     "xiaomi LLM gateway",
		addr:     "https://token-plan-cn.xiaomimimo.com/v1",
		provider: "openai",
	},

	// ── 阿里百炼 OpenAI 兼容（/v1/models=404，只有 chat completions）─────────
	{
		name:     "dashscope openai-compat",
		addr:     "https://coding.dashscope.aliyuncs.com/v1",
		provider: "openai",
		// /v1/models 是 404，/v1/chat/completions POST 返回 401 → 服务在线
	},

	// ── 阿里百炼 Anthropic 兼容──────────────────────────────────────────────
	{
		name:     "dashscope anthropic-compat",
		addr:     "https://coding.dashscope.aliyuncs.com/apps/anthropic",
		provider: "anthropic",
		// /v1/messages POST 返回 401 → 服务在线
	},

	// ── 火山 ark Anthropic 兼容（/health=401 需认证，/v1/models 也有效）────────────
	{
		name:     "volcengine ark (anthropic-compat)",
		addr:     "https://ark.cn-beijing.volces.com/api/coding",
		provider: "anthropic",
		// anthropic 专属策略优先：/v1/models (anthropic) 或 /health 均可，不限定路径
	},

	// ── 火山 ark OpenAI 兼容───────────────────────────────────────────────────
	{
		name:     "volcengine ark (openai-compat)",
		addr:     "https://ark.cn-beijing.volces.com/api/coding/v3",
		provider: "openai",
	},

	// ── 腾讯 lkeap OpenAI 兼容───────────────────────────────────────────────
	{
		name:     "tencent lkeap (openai-compat)",
		addr:     "https://api.lkeap.cloud.tencent.com/coding/v3",
		provider: "openai",
	},

	// ── 腾讯 lkeap Anthropic 兼容──────────────────────────────────────────────
	{
		name:     "tencent lkeap (anthropic-compat)",
		addr:     "https://api.lkeap.cloud.tencent.com/coding/anthropic",
		provider: "anthropic",
	},
}

func TestProbe_RealEndpoints(t *testing.T) {
	if os.Getenv("RUN_REAL_PROBE_TESTS") == "" {
		t.Skip("skipping real endpoint tests; set RUN_REAL_PROBE_TESTS=1 to run")
	}

	logger := zaptest.NewLogger(t, zaptest.Level(zap.DebugLevel))
	prober := NewProber(8*time.Second, logger)

	for _, ep := range realEndpoints {
		ep := ep
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			t.Logf("testing: addr=%s provider=%s", ep.addr, ep.provider)

			found, unreachable := prober.Discover(ctx, ep.addr, ep.name, ep.provider, nil)

			if ep.expectUnreachable {
				if !unreachable {
					t.Errorf("expected unreachable, but got found=%v unreachable=%v", found, unreachable)
				}
				return
			}

			if unreachable {
				t.Errorf("got unreachable=true but expected service to be online: addr=%s", ep.addr)
				return
			}

			if found == nil {
				// 无法找到探活路径，说明服务在线但没有已知的无副作用探活接口
				// 这不是错误，只是说明需要被动熔断
				t.Logf("WARN: no suitable probe method found (service may be online, will use passive circuit-breaking)")
				return
			}

			t.Logf("OK: discovered probe method=%s path=%s", found.Name, found.Path)

			if ep.expectFoundPath != "" && found.Path != ep.expectFoundPath {
				t.Errorf("expected path %q, got %q (method=%s)", ep.expectFoundPath, found.Path, found.Name)
			}
		})
	}
}

// TestProbe_Discover_ContinuesPastEndpointTimeout 验证 Discover 在单个端点超时后继续尝试后续策略。
// 旧版本：任意 err!=nil,status==0 都触发 return nil, true（认为不可达），跳过剩余策略。
// 修复后：HTTP 客户端超时（单端点）与连接拒绝区分，超时仅跳过该方法，继续尝试下一个。
func TestProbe_Discover_ContinuesPastEndpointTimeout(t *testing.T) {
	// 服务器：/health 超时（无响应）, /models 返回 200
	// 使用极短的 HTTP client 超时（50ms）确保 /health 快速超时
	slowPath := make(chan struct{}) // /health 永远阻塞，直到测试结束
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// 阻塞直到测试结束（模拟超时）
			<-slowPath
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(slowPath) // 解除阻塞使服务器可以干净关闭
		srv.Close()
	}()

	// 使用 50ms 超时——/health 会超时，/models 会成功
	logger := zaptest.NewLogger(t, zaptest.Level(zap.DebugLevel))
	prober := NewProber(50*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	found, unreachable := prober.Discover(ctx, srv.URL, "t-timeout", "", nil)

	if unreachable {
		t.Error("service should NOT be unreachable — /models responds 200; only /health times out")
	}
	if found == nil {
		t.Fatal("expected Discover to find /models after /health timeout, got nil")
	}
	if found.Path != "/models" {
		t.Errorf("expected found path /models, got %q", found.Path)
	}
}

// TestProbe_Discover_AllMethodsTimeout_NotUnreachable 验证所有端点均超时时 Discover 返回 unreachable=false。
// 旧版本：any err != nil, status==0 → isEndpointTimeout → continue，循环结束后 gotHTTPResponse=false →
//   return nil, true（错误地标记服务不可达）。
// 修复后：循环结束后 !gotHTTPResponse 时保守返回 nil, false（不可达留给连接拒绝路径处理）。
func TestProbe_Discover_AllMethodsTimeout_NotUnreachable(t *testing.T) {
	// 服务器：所有路径都永远阻塞（模拟响应非常慢的服务）
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	// 极短超时（50ms）确保每个端点都超时
	logger := zaptest.NewLogger(t)
	prober := NewProber(50*time.Millisecond, logger)

	// discovery 预算足够让所有方法都尝试（不触发 ctx 超时）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	found, unreachable := prober.Discover(ctx, srv.URL, "t-all-timeout", "", nil)

	// 所有方法超时 → 不应标记为 unreachable（服务可能只是慢）
	if unreachable {
		t.Error("all-methods-timeout should return unreachable=false (service may be slow, not down)")
	}
	if found != nil {
		t.Errorf("expected found=nil when all methods timeout, got %v", found)
	}
}

// TestProbe_Discover_ConnectionRefusedIsUnreachable 验证连接拒绝立即返回 unreachable=true。
// （与上面的超时测试形成对比：超时→继续，连接拒绝→停止）
func TestProbe_Discover_ConnectionRefusedIsUnreachable(t *testing.T) {
	// 使用一个不监听的端口（直接绑定然后关闭，确保连接被拒绝）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // 立即关闭：后续连接会被拒绝

	logger := zaptest.NewLogger(t)
	prober := NewProber(200*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, unreachable := prober.Discover(ctx, addr, "t-refused", "", nil)
	if !unreachable {
		t.Error("connection refused should mark target as unreachable")
	}
}

// TestProbe_SmartHealthChecker_Integration 用 httptest 服务器验证智能探活完整流程：
// HealthChecker 自动发现并缓存探活策略，后续 checkAll 复用缓存。
func TestProbe_SmartHealthChecker_Integration(t *testing.T) {
	// 场景 1：服务只有 /models（模拟 OpenAI/腾讯/小米）
	t.Run("discovers /models when /health is 404", func(t *testing.T) {
		srv := newSelectiveServer(map[string]int{
			"/health":  404,
			"/models":  200,
		})
		defer srv.close()

		bal := NewWeightedRandom([]Target{{ID: "t1", Addr: srv.url, Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t))
		// 不设置 healthPaths → 触发智能探活

		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		// 探活缓存应命中 /models
		entry := hc.probeCache.get("t1")
		if entry == nil {
			t.Fatal("expected probe cache entry after discovery")
		}
		if entry.method == nil {
			t.Fatal("expected valid method in cache entry")
		}
		if entry.method.Path != "/models" {
			t.Errorf("expected cached path /models, got %s", entry.method.Path)
		}
		// target 应标记为健康
		targets := bal.Targets()
		if !targets[0].Healthy {
			t.Error("expected target to be marked healthy after discovery")
		}
	})

	// 场景 2：服务只有 /health（模拟 vLLM/sglang/自建代理）
	t.Run("uses /health when available", func(t *testing.T) {
		srv := newSelectiveServer(map[string]int{
			"/health": 200,
		})
		defer srv.close()

		bal := NewWeightedRandom([]Target{{ID: "t2", Addr: srv.url, Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t))

		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		entry := hc.probeCache.get("t2")
		if entry == nil || entry.method.Path != "/health" {
			t.Errorf("expected /health in cache, got %v", entry)
		}
	})

	// 场景 3：/health=401（需认证），/models 也返回 401，均视为服务在线
	t.Run("401 on /health treated as online (auth required)", func(t *testing.T) {
		srv := newSelectiveServer(map[string]int{
			"/health":  401,
			"/models":  401,
		})
		defer srv.close()

		bal := NewWeightedRandom([]Target{{ID: "t3", Addr: srv.url, Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t))

		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		// /health 返回 401 → OKStatuses 包含 401 → 探活成功
		entry := hc.probeCache.get("t3")
		if entry == nil {
			t.Fatal("expected probe cache entry")
		}
		if entry.method == nil {
			t.Error("expected valid method in cache entry (401 = service online)")
		}
	})

	// 场景 4：用户显式配置 health_check_path，走确定路径，不触发自动探测
	t.Run("explicit health_check_path bypasses smart probe", func(t *testing.T) {
		srv := newSelectiveServer(map[string]int{
			"/custom-health": 200,
			"/health":        404,
		})
		defer srv.close()

		bal := NewWeightedRandom([]Target{{ID: "t4", Addr: srv.url, Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t),
			WithHealthPaths(map[string]string{"t4": "/custom-health"}),
		)

		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		// 走显式路径，不写 probeCache
		entry := hc.probeCache.get("t4")
		if entry != nil {
			t.Error("explicit path should NOT write probeCache")
		}
		targets := bal.Targets()
		if !targets[0].Healthy {
			t.Error("expected healthy after explicit path check")
		}
	})

	// 场景 5：服务完全不可达（连接拒绝），不缓存 unreachable，每次心跳重试
	// 设计原则：不缓存 unreachable，避免 2h TTL 内服务恢复后仍无法被探活（死锁）
	t.Run("connection refused marks failure without caching unreachable", func(t *testing.T) {
		// 使用一个不存在的地址
		bal := NewWeightedRandom([]Target{{ID: "t5", Addr: "http://127.0.0.1:19999", Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t),
			WithTimeout(500*time.Millisecond),
			WithFailThreshold(1), // 单次失败即标记不健康
		)

		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		// unreachable 不再缓存，避免 2h 盲区
		entry := hc.probeCache.get("t5")
		if entry != nil {
			t.Error("unreachable targets should not be cached (to allow recovery detection on next heartbeat)")
		}

		// 但节点应被标记为不健康（通过 recordFailure）
		targets := bal.Targets()
		for _, tgt := range targets {
			if tgt.ID == "t5" {
				if tgt.Healthy {
					t.Error("unreachable target should be marked unhealthy after failure threshold")
				}
				break
			}
		}
	})

	// 场景 6：探活缓存 TTL 过期后重新发现
	t.Run("cache invalidated after TTL, rediscovery triggered", func(t *testing.T) {
		srv := newSelectiveServer(map[string]int{"/models": 200})
		defer srv.close()

		bal := NewWeightedRandom([]Target{{ID: "t6", Addr: srv.url, Weight: 1, Healthy: true}})
		hc := NewHealthChecker(bal, zaptest.NewLogger(t))
		// 注入一个极短 TTL 的 probeCache
		hc.probeCache = NewProbeCache(10 * time.Millisecond)

		// 第一次 checkAll → discover 并缓存
		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.checkAll()
		}()
		hc.wg.Wait()

		if hc.probeCache.get("t6") == nil {
			t.Fatal("expected cache hit after first discovery")
		}

		// 等待 TTL 过期
		time.Sleep(20 * time.Millisecond)

		// 缓存应失效
		if hc.probeCache.get("t6") != nil {
			t.Error("expected cache miss after TTL expiry")
		}
	})

		// Scenario 7: cache hit + connection error -> clear cache -> rediscover next heartbeat
		// Covers: checkOneSmart cache-hit path + definitivelyUnhealthy() branch
		t.Run("cache hit + connection error clears cache for rediscovery", func(t *testing.T) {
			srv := newSelectiveServer(map[string]int{"/models": 200})

			bal := NewWeightedRandom([]Target{{ID: "t7", Addr: srv.url, Weight: 1, Healthy: true}})
			hc := NewHealthChecker(bal, zaptest.NewLogger(t),
				WithTimeout(300*time.Millisecond),
			)

			// First checkAll: discover and cache /models
			hc.wg.Add(1)
			go func() {
				defer hc.wg.Done()
				hc.checkAll()
			}()
			hc.wg.Wait()

			if hc.probeCache.get("t7") == nil {
				t.Fatal("expected cache entry after first discovery")
			}

			// Simulate service down: close the httptest server
			srv.close()

			// Second checkAll: cache hit, but CheckWithMethod returns connection error
			// -> definitivelyUnhealthy() = true -> clear cache + recordFailure
			hc.wg.Add(1)
			go func() {
				defer hc.wg.Done()
				hc.checkAll()
			}()
			hc.wg.Wait()

			// Cache should be cleared (next heartbeat will rediscover)
			if hc.probeCache.get("t7") != nil {
				t.Error("expected cache to be cleared after connection error")
			}
		})

		// Scenario 8: service online but all probe paths return 5xx
		// -> cache nil method entry to prevent probe storm (5 HTTP reqs every 30s)
		// Covers: Discover() returns found=nil, unreachable=false path
		t.Run("no matching probe path: retries discover every heartbeat for recovery", func(t *testing.T) {
			// All builtin probe paths return 500 (service online but no path matches)
			srv := newCountingServer(map[string]int{
				"/health":              500,
				"/models":              500,
				"/v1/chat/completions": 500,
				"/v1/messages":         500,
			})
			defer srv.close()

			bal := NewWeightedRandom([]Target{{ID: "t8", Addr: srv.url, Weight: 1, Healthy: true}})
			hc := NewHealthChecker(bal, zaptest.NewLogger(t))

			// First checkAll: Discover tries all strategies, all fail -> found=nil, no caching
			hc.wg.Add(1)
			go func() {
				defer hc.wg.Done()
				hc.checkAll()
			}()
			hc.wg.Wait()

			// No nil-method cache entry — we stopped caching to allow recovery
			entry := hc.probeCache.get("t8")
			if entry != nil {
				t.Errorf("no-match result should NOT be cached (to allow recovery), but found entry method=%v",
					entry.method)
			}

			// Second checkAll: no cache -> retry Discover again (sends HTTP requests)
			requestsBefore := srv.count()
			hc.wg.Add(1)
			go func() {
				defer hc.wg.Done()
				hc.checkAll()
			}()
			hc.wg.Wait()

			if srv.count() == requestsBefore {
				t.Error("second checkAll should retry Discover (send HTTP requests) when no cache entry, but sent 0")
			}
		})
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助：可配置响应码的 httptest 服务器
// ─────────────────────────────────────────────────────────────────────────────

type selectiveServer struct {
	srv      *httptest.Server
	url      string
	pathCode map[string]int
}

func newSelectiveServer(pathCode map[string]int) *selectiveServer {
	s := &selectiveServer{pathCode: pathCode}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, ok := pathCode[r.URL.Path]
		if !ok {
			code = 404
		}
		w.WriteHeader(code)
	}))
	s.url = s.srv.URL
	return s
}

func (s *selectiveServer) close() { s.srv.Close() }
// countingServer is like selectiveServer but tracks request count for probe storm detection.
type countingServer struct {
	*selectiveServer
	mu  sync.Mutex
	reqs int
}

func newCountingServer(pathCode map[string]int) *countingServer {
	cs := &countingServer{}
	cs.selectiveServer = &selectiveServer{pathCode: pathCode}
	cs.selectiveServer.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.reqs++
		cs.mu.Unlock()
		code, ok := pathCode[r.URL.Path]
		if !ok {
			code = 404
		}
		w.WriteHeader(code)
	}))
	cs.selectiveServer.url = cs.selectiveServer.srv.URL
	return cs
}

func (cs *countingServer) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.reqs
}

