package lb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

// TestHealthChecker_Anthropic_Auth 验证 Anthropic 请求携带 x-api-key 和 anthropic-version 头
func TestHealthChecker_Anthropic_Auth(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Mock server 验证 Anthropic 认证头
	var mu sync.Mutex
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	// 创建 balancer 和 health checker
	target := Target{ID: "anthropic-api", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger,
		WithTimeout(1*time.Second),
		WithCredentials(map[string]TargetCredential{
			"anthropic-api": {
				APIKey:   "sk-ant-test-key-12345",
				Provider: "anthropic",
			},
		}),
	)

	// 执行健康检查
	hc.CheckTarget("anthropic-api")
	hc.Wait()
	server.Close() // 确保 handler goroutine 已完成写入

	mu.Lock()
	hdrs := capturedHeaders
	mu.Unlock()

	// 验证请求头
	assert.Equal(t, "sk-ant-test-key-12345", hdrs.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", hdrs.Get("anthropic-version"))
	// 确保没有使用 Bearer 认证
	assert.NotContains(t, hdrs.Get("Authorization"), "Bearer")
}

// TestHealthChecker_OpenAI_Auth 验证 OpenAI/OpenAI-compatible 请求携带 Authorization: Bearer 头
func TestHealthChecker_OpenAI_Auth(t *testing.T) {
	logger := zaptest.NewLogger(t)

	var mu sync.Mutex
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	target := Target{ID: "openai-api", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger,
		WithTimeout(1*time.Second),
		WithCredentials(map[string]TargetCredential{
			"openai-api": {
				APIKey:   "sk-proj-test-key-67890",
				Provider: "openai",
			},
		}),
	)

	hc.CheckTarget("openai-api")
	hc.Wait()
	server.Close()

	mu.Lock()
	hdrs := capturedHeaders
	mu.Unlock()

	// 验证 Bearer 认证头
	assert.Equal(t, "Bearer sk-proj-test-key-67890", hdrs.Get("Authorization"))
	// 确保没有使用 Anthropic 的 x-api-key
	assert.Empty(t, hdrs.Get("x-api-key"))
	assert.Empty(t, hdrs.Get("anthropic-version"))
}

// TestHealthChecker_DashScope_Auth 验证阿里百炼 DashScope 使用标准 Bearer 认证
func TestHealthChecker_DashScope_Auth(t *testing.T) {
	logger := zaptest.NewLogger(t)

	var mu sync.Mutex
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	target := Target{ID: "dashscope-api", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger,
		WithTimeout(1*time.Second),
		WithCredentials(map[string]TargetCredential{
			"dashscope-api": {
				APIKey:   "sk-dashscope-test",
				Provider: "openai", // DashScope 兼容 OpenAI
			},
		}),
	)

	hc.CheckTarget("dashscope-api")
	hc.Wait()
	server.Close()

	mu.Lock()
	hdrs := capturedHeaders
	mu.Unlock()

	// 验证 Bearer 认证
	assert.Equal(t, "Bearer sk-dashscope-test", hdrs.Get("Authorization"))
}

// TestHealthChecker_NoKey_NoAuthHeader 无 APIKey 时不注入任何认证头（本地 vLLM/sglang 行为不变）
func TestHealthChecker_NoKey_NoAuthHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)

	var mu sync.Mutex
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	// 创建 target，不配置任何凭证
	target := Target{ID: "local-vllm", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger, WithTimeout(1*time.Second))

	hc.CheckTarget("local-vllm")
	hc.Wait()
	server.Close()

	mu.Lock()
	hdrs := capturedHeaders
	mu.Unlock()

	// 验证没有注入任何认证头
	assert.Empty(t, hdrs.Get("Authorization"))
	assert.Empty(t, hdrs.Get("x-api-key"))
	assert.Empty(t, hdrs.Get("anthropic-version"))
}

// TestHealthChecker_UpdateCredentials_Runtime 运行时热更新 credentials 后，下次检查使用新 key
func TestHealthChecker_UpdateCredentials_Runtime(t *testing.T) {
	logger := zaptest.NewLogger(t)

	var mu sync.Mutex
	callCount := 0
	var lastHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		lastHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	target := Target{ID: "test-api", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger,
		WithTimeout(1*time.Second),
		WithCredentials(map[string]TargetCredential{
			"test-api": {
				APIKey:   "old-key",
				Provider: "openai",
			},
		}),
	)

	// 第一次检查：使用旧 key
	// 缓存冷启动：discover（1 次）+ 发现后验证（1 次）= 2 次请求
	hc.CheckTarget("test-api")
	hc.Wait()

	mu.Lock()
	firstCount := callCount
	firstHeaders := lastHeaders
	mu.Unlock()

	assert.Equal(t, "Bearer old-key", firstHeaders.Get("Authorization"))
	assert.Equal(t, 2, firstCount)

	// 运行时更新凭证
	hc.UpdateCredentials(map[string]TargetCredential{
		"test-api": {
			APIKey:   "new-key",
			Provider: "openai",
		},
	})

	// 第二次检查：凭证变更后缓存失效，再次 discover + 验证 = 2 次请求
	hc.CheckTarget("test-api")
	hc.Wait()

	mu.Lock()
	secondCount := callCount
	secondHeaders := lastHeaders
	mu.Unlock()

	assert.Equal(t, "Bearer new-key", secondHeaders.Get("Authorization"))
	assert.Equal(t, 4, secondCount)
}

// TestHealthChecker_401_StillTriggersFailure 确认即使有 key，401 也正常触发 recordFailure
func TestHealthChecker_401_StillTriggersFailure(t *testing.T) {
	logger := zaptest.NewLogger(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 key 已注入，但服务返回 401
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized) // 401
	}))
	defer server.Close()

	target := Target{ID: "test-api", Addr: server.URL, Weight: 1, Healthy: true}
	bal := NewWeightedRandom([]Target{target})
	hc := NewHealthChecker(bal, logger,
		WithTimeout(1*time.Second),
		WithFailThreshold(1), // 单次失败即标记不健康
		WithCredentials(map[string]TargetCredential{
			"test-api": {
				APIKey:   "some-key",
				Provider: "openai",
			},
		}),
	)

	hc.CheckTarget("test-api")
	hc.Wait()

	// 确认节点被标记为不健康（401 仍然是失败）
	targets := bal.Targets()
	found := false
	for _, tgt := range targets {
		if tgt.ID == "test-api" {
			assert.False(t, tgt.Healthy, "401 should still mark node as unhealthy")
			found = true
			break
		}
	}
	assert.True(t, found, "test-api target should exist in balancer")
}

// TestHealthChecker_MixedAuthProviders 验证不同 provider 的目标在同一检查器中正确注入认证头。
func TestHealthChecker_MixedAuthProviders(t *testing.T) {
	balance := NewWeightedRandom([]Target{
		{ID: "anthropic-api", Addr: "http://127.0.0.1:0", Healthy: true},
		{ID: "openai-api", Addr: "http://127.0.0.1:0", Healthy: true},
	})

	hc := NewHealthChecker(balance, zaptest.NewLogger(t))
	hc.UpdateCredentials(map[string]TargetCredential{
		"anthropic-api": {APIKey: "sk-ant-123", Provider: "anthropic"},
		"openai-api":    {APIKey: "sk-openai-456", Provider: "openai"},
	})

	// 为 Anthropic 创建请求并注入认证
	req1, _ := http.NewRequest("GET", "http://127.0.0.1:8001/health", nil)
	hc.injectAuth(req1, "anthropic-api")

	// 为 OpenAI 创建请求并注入认证
	req2, _ := http.NewRequest("GET", "http://127.0.0.1:8002/health", nil)
	hc.injectAuth(req2, "openai-api")

	// 验证 Anthropic 使用 x-api-key + version
	assert.Equal(t, "sk-ant-123", req1.Header.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", req1.Header.Get("anthropic-version"))
	assert.Empty(t, req1.Header.Get("Authorization"), "Anthropic should not use Bearer")

	// 验证 OpenAI 使用 Bearer
	assert.Equal(t, "Bearer sk-openai-456", req2.Header.Get("Authorization"))
	assert.Empty(t, req2.Header.Get("x-api-key"), "OpenAI should not use x-api-key")
}

// TestHealthChecker_Ark_Auth 验证 Ark（火山引擎）使用 Bearer token。
func TestHealthChecker_Ark_Auth(t *testing.T) {
	balance := NewWeightedRandom([]Target{{ID: "ark-api", Addr: "http://127.0.0.1:0", Healthy: true}})

	hc := NewHealthChecker(balance, zaptest.NewLogger(t))
	hc.UpdateCredentials(map[string]TargetCredential{
		"ark-api": {APIKey: "sk-ark-789", Provider: "ark"},
	})

	req, _ := http.NewRequest("GET", "http://127.0.0.1:8003/health", nil)
	hc.injectAuth(req, "ark-api")

	// Ark 是 OpenAI-compatible，使用 Bearer token
	assert.Equal(t, "Bearer sk-ark-789", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-api-key"))
	assert.Empty(t, req.Header.Get("anthropic-version"))
}

// TestHealthChecker_InvalidProvider_FallsToDefault 验证未知 provider 时使用默认的 Bearer 方案。
func TestHealthChecker_InvalidProvider_FallsToDefault(t *testing.T) {
	balance := NewWeightedRandom([]Target{{ID: "unknown-api", Addr: "http://127.0.0.1:0", Healthy: true}})

	hc := NewHealthChecker(balance, zaptest.NewLogger(t))
	hc.UpdateCredentials(map[string]TargetCredential{
		"unknown-api": {APIKey: "sk-unknown-999", Provider: "unknown_provider"},
	})

	req, _ := http.NewRequest("GET", "http://127.0.0.1:8004/health", nil)
	hc.injectAuth(req, "unknown-api")

	// 未知 provider 应该走 default case，使用 Bearer
	assert.Equal(t, "Bearer sk-unknown-999", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-api-key"))
}

// TestHealthChecker_ConcurrentAuthInjection 验证并发环境下认证注入和凭证更新的安全性。
func TestHealthChecker_ConcurrentAuthInjection(t *testing.T) {
	balance := NewWeightedRandom([]Target{
		{ID: "target1", Addr: "http://127.0.0.1:0", Healthy: true},
		{ID: "target2", Addr: "http://127.0.0.1:0", Healthy: true},
		{ID: "target3", Addr: "http://127.0.0.1:0", Healthy: true},
	})

	hc := NewHealthChecker(balance, zaptest.NewLogger(t))

	// 并发注入认证，同时更新凭证
	done := make(chan bool, 10)

	// 3 个 goroutine 不断注入认证
	for i := 0; i < 3; i++ {
		go func(idx int) {
			defer func() { done <- true }()
			targetID := fmt.Sprintf("target%d", idx+1)
			for j := 0; j < 100; j++ {
				req, _ := http.NewRequest("GET", "http://127.0.0.1:8000/health", nil)
				hc.injectAuth(req, targetID)
				// 不应该 panic 或产生竞争
			}
		}(i)
	}

	// 1 个 goroutine 不断更新凭证
	go func() {
		defer func() { done <- true }()
		for i := 0; i < 50; i++ {
			hc.UpdateCredentials(map[string]TargetCredential{
				"target1": {APIKey: "key1", Provider: "anthropic"},
				"target2": {APIKey: "key2", Provider: "openai"},
				"target3": {APIKey: "key3", Provider: "ark"},
			})
		}
	}()

	// 等待所有 goroutine 完成
	for i := 0; i < 4; i++ {
		<-done
	}

	// 验证最终状态 - target1 有凭证，应该注入认证
	req, _ := http.NewRequest("GET", "http://127.0.0.1:8000/health", nil)
	hc.injectAuth(req, "target1")
	// target1 最后被设置为 anthropic provider，key 是 "key1"
	assert.Equal(t, "key1", req.Header.Get("x-api-key"), "target1 应为 Anthropic 认证")
}


// TestHealthChecker_CredentialExistsButEmptyAPIKey 验证 credential 存在但 APIKey 为空字符串时，
// 不注入任何认证头（覆盖 injectAuth 中 cred.APIKey == "" 分支）。
func TestHealthChecker_CredentialExistsButEmptyAPIKey(t *testing.T) {
	balance := NewWeightedRandom([]Target{{ID: "target-empty", Addr: "http://127.0.0.1:0", Healthy: true}})

	hc := NewHealthChecker(balance, zaptest.NewLogger(t))
	// credential 存在，provider 非空，但 APIKey 为空字符串
	hc.UpdateCredentials(map[string]TargetCredential{
		"target-empty": {APIKey: "", Provider: "openai"},
	})

	req, _ := http.NewRequest("GET", "http://127.0.0.1:0/health", nil)
	hc.injectAuth(req, "target-empty")

	// APIKey 为空，不应注入任何认证头
	assert.Empty(t, req.Header.Get("Authorization"), "empty APIKey should not inject Authorization header")
	assert.Empty(t, req.Header.Get("x-api-key"), "empty APIKey should not inject x-api-key header")
}
