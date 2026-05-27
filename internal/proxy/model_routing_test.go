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

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// setupModelRoutingTest 创建带模型感知路由配置的 SProxy 测试实例。
// 每个 testCase 的 targets 直接传入 lb.Target 列表（含 SupportedModels 和 AutoModel）。
func setupModelRoutingTest(t *testing.T, lbTargets []lb.Target, proxyTargets []LLMTarget) (*SProxy, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)

	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)

	if len(proxyTargets) == 0 {
		proxyTargets = make([]LLMTarget, len(lbTargets))
		for i, lt := range lbTargets {
			proxyTargets[i] = LLMTarget{URL: lt.Addr, Weight: lt.Weight}
		}
	}

	sp, err := NewSProxy(logger, jwtMgr, writer, proxyTargets)
	require.NoError(t, err)

	balancer := lb.NewWeightedRandom(lbTargets)
	sp.llmBalancer = balancer

	return sp, func() {
		cancel()
		writer.Wait()
	}
}

// --- T4: 模型过滤路由集成测试 ---

func TestModelRouting_ExactMatch_RoutesToCorrectTarget(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "https://api.openai.com", Addr: "https://api.openai.com", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o"}},
	}, nil)
	defer cleanup()

	// Request claude-sonnet-4 → should route to anthropic target
	info, err := sp.weightedPickExcluding("/v1/messages", "claude-sonnet-4", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com", info.URL)
}

func TestModelRouting_NoMatch_FailOpen(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "https://api.openai.com", Addr: "https://api.openai.com", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o"}},
	}, nil)
	defer cleanup()

	// Request llama3 → no target supports it, fail-open returns first available
	info, err := sp.weightedPickExcluding("/v1/messages", "llama3", nil)
	require.NoError(t, err)
	assert.NotNil(t, info)
	// Fail-open: returns some target (doesn't matter which)
}

func TestModelRouting_UnconfiguredTarget_NoFilter(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "http://localhost:11434", Addr: "http://localhost:11434", Weight: 1, Healthy: true, SupportedModels: nil},
	}, nil)
	defer cleanup()

	// Request gpt-4o → anthropic filtered out, ollama (unconfigured) passes
	info, err := sp.weightedPickExcluding("/v1/messages", "gpt-4o", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:11434", info.URL)
}

func TestModelRouting_AutoMode_SkipsFilter(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "https://api.openai.com", Addr: "https://api.openai.com", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o"}},
	}, nil)
	defer cleanup()

	// Request "auto" → should skip model filtering, all targets participate
	info, err := sp.weightedPickExcluding("/v1/messages", "auto", nil)
	require.NoError(t, err)
	assert.NotNil(t, info)
}

func TestModelRouting_EmptyModel_NoFilter(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "https://api.openai.com", Addr: "https://api.openai.com", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o"}},
	}, nil)
	defer cleanup()

	// Empty model → no filtering
	info, err := sp.weightedPickExcluding("/v1/messages", "", nil)
	require.NoError(t, err)
	assert.NotNil(t, info)
}

func TestModelRouting_AllTargetsUnconfigured_NoFilter(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://t1.example.com", Addr: "https://t1.example.com", Weight: 1, Healthy: true, SupportedModels: nil},
		{ID: "https://t2.example.com", Addr: "https://t2.example.com", Weight: 1, Healthy: true, SupportedModels: []string{}},
	}, nil)
	defer cleanup()

	// All targets unconfigured → any model passes
	info, err := sp.weightedPickExcluding("/v1/messages", "claude-sonnet-4", nil)
	require.NoError(t, err)
	assert.NotNil(t, info)
}

// --- T5: Auto 模式集成测试 ---

func TestAutoMode_AutoModelUsed(t *testing.T) {
	// Mock backend that captures the request body
	var receivedBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		// Return a minimal Anthropic response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-20250514","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer backend.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)

	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	defer func() { cancel(); writer.Wait() }()

	targetURL := backend.URL
	proxyTargets := []LLMTarget{
		{URL: targetURL, APIKey: "test-key", Provider: "anthropic", Weight: 1},
	}
	sp, err := NewSProxy(logger, jwtMgr, writer, proxyTargets)
	require.NoError(t, err)

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: targetURL, Addr: targetURL, Weight: 1, Healthy: true, AutoModel: "claude-sonnet-4-20250514"},
	})
	sp.llmBalancer = balancer

	// Create a valid JWT for the request
	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "user1", Username: "user1", GroupID: "group1"}, time.Hour)
	require.NoError(t, err)

	// Send request with model="auto"
	reqBody := `{"model":"auto","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	rec := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify the model was rewritten from "auto" to "claude-sonnet-4-20250514"
	require.NotEmpty(t, receivedBody)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(receivedBody), &parsed))
	assert.Equal(t, "claude-sonnet-4-20250514", parsed["model"])
}

func TestAutoMode_FallbackToFirstSupportedModel(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "http://t1", Addr: "http://t1", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o", "gpt-4o-mini"}, AutoModel: ""},
	}, []LLMTarget{{URL: "http://t1", Weight: 1}})
	defer cleanup()

	// AutoModel empty → should fallback to SupportedModels[0]
	got := sp.autoModelFromURL("http://t1")
	assert.Equal(t, "gpt-4o", got)
}

func TestAutoMode_FallbackToPassthrough(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "http://t1", Addr: "http://t1", Weight: 1, Healthy: true, SupportedModels: nil, AutoModel: ""},
	}, []LLMTarget{{URL: "http://t1", Weight: 1}})
	defer cleanup()

	// Both empty → should return "" (passthrough)
	got := sp.autoModelFromURL("http://t1")
	assert.Equal(t, "", got)
}

func TestAutoMode_NonAutoModel_NotRewritten(t *testing.T) {
	// rewriteModelInBody should not change model if it's not "auto"
	body := `{"model":"gpt-4o","messages":[]}`
	result := rewriteModelInBody([]byte(body), "auto", "claude-sonnet-4")
	assert.Equal(t, body, string(result))
}

// --- T6: 重试路径一致性测试 ---

func TestRetry_ModelFilteringConsistency(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "https://api.anthropic.com", Addr: "https://api.anthropic.com", Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: "https://api.openai.com", Addr: "https://api.openai.com", Weight: 1, Healthy: true, SupportedModels: []string{"gpt-4o"}},
	}, nil)
	defer cleanup()

	// Build retry transport with requestedModel="claude-sonnet-4"
	rt := sp.buildRetryTransport("user1", "group1", "/v1/messages", "claude-sonnet-4")
	assert.NotNil(t, rt)

	// Verify: the transport is a RetryTransport (not the base transport)
	_, ok := rt.(*lb.RetryTransport)
	assert.True(t, ok, "buildRetryTransport with balancer should return RetryTransport")
}

func TestRetry_AutoModeConsistency(t *testing.T) {
	sp, cleanup := setupModelRoutingTest(t, []lb.Target{
		{ID: "http://t1", Addr: "http://t1", Weight: 1, Healthy: true, AutoModel: "claude-sonnet-4"},
	}, []LLMTarget{{URL: "http://t1", Weight: 1}})
	defer cleanup()

	// Build retry transport with requestedModel="auto"
	rt := sp.buildRetryTransport("user1", "group1", "/v1/messages", "auto")
	assert.NotNil(t, rt)

	_, ok := rt.(*lb.RetryTransport)
	assert.True(t, ok)
}

// --- T8: E2E 模型路由测试 ---

func TestE2E_ModelAwareRouting_FullFlow(t *testing.T) {
	// Two mock backends
	anthropicReceived := false
	openaiReceived := false

	anthropicBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicReceived = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer anthropicBackend.Close()

	openaiBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiReceived = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer openaiBackend.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)

	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	defer func() { cancel(); writer.Wait() }()

	proxyTargets := []LLMTarget{
		{URL: anthropicBackend.URL, APIKey: "anthropic-key", Provider: "anthropic", Weight: 1},
		{URL: openaiBackend.URL, APIKey: "openai-key", Provider: "openai", Weight: 1},
	}
	sp, err := NewSProxy(logger, jwtMgr, writer, proxyTargets)
	require.NoError(t, err)

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: anthropicBackend.URL, Addr: anthropicBackend.URL, Weight: 1, Healthy: true, SupportedModels: []string{"claude-*"}},
		{ID: openaiBackend.URL, Addr: openaiBackend.URL, Weight: 1, Healthy: true, SupportedModels: []string{"gpt-*"}},
	})
	sp.llmBalancer = balancer

	// Issue JWT
	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "user1", Username: "user1", GroupID: "group1"}, time.Hour)
	require.NoError(t, err)

	// Send request for claude-sonnet-4 → should only hit Anthropic
	reqBody := `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	rec := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, anthropicReceived, "Anthropic backend should have received the request")
	assert.False(t, openaiReceived, "OpenAI backend should NOT have received the request")
}

func TestE2E_AutoMode_FullFlow(t *testing.T) {
	var receivedModel string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]interface{}
		if json.Unmarshal(body, &parsed) == nil {
			if m, ok := parsed["model"].(string); ok {
				receivedModel = m
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-20250514","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer backend.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret")
	require.NoError(t, err)

	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Second)
	writer.Start(ctx)
	defer func() { cancel(); writer.Wait() }()

	proxyTargets := []LLMTarget{
		{URL: backend.URL, APIKey: "test-key", Provider: "anthropic", Weight: 1},
	}
	sp, err := NewSProxy(logger, jwtMgr, writer, proxyTargets)
	require.NoError(t, err)

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: backend.URL, Addr: backend.URL, Weight: 1, Healthy: true, AutoModel: "claude-sonnet-4-20250514"},
	})
	sp.llmBalancer = balancer

	token, err := jwtMgr.Sign(auth.JWTClaims{UserID: "user1", Username: "user1", GroupID: "group1"}, time.Hour)
	require.NoError(t, err)

	reqBody := `{"model":"auto","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	rec := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "claude-sonnet-4-20250514", receivedModel, "auto should be rewritten to actual model")
}

func TestE2E_SeedThenWebUIUpdate_PreservesChanges(t *testing.T) {
	logger := zaptest.NewLogger(t)

	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	repo := db.NewLLMTargetRepo(gormDB, logger)

	// Step 1: Seed a target
	target := &db.LLMTarget{
		ID:                  "seed-target-1",
		URL:                 "https://api.anthropic.com",
		Provider:            "anthropic",
		Weight:              1,
		SupportedModelsJSON: `[]`,
		Source:              "config",
		IsActive:            true,
	}
	require.NoError(t, repo.Seed(target))

	// Step 2: Simulate WebUI update — change supported_models
	// config-sourced targets have IsEditable=false; explicitly unlock via direct DB update
	// to simulate an admin making the target editable before WebUI modification.
	existing, err := repo.GetByURL("https://api.anthropic.com")
	require.NoError(t, err)
	require.NoError(t, gormDB.Model(existing).Update("is_editable", true).Error)
	existing.IsEditable = true
	existing.SupportedModelsJSON = `["gpt-*"]`
	require.NoError(t, repo.Update(existing))

	// Step 3: Seed again (config file hasn't changed)
	seedAgain := &db.LLMTarget{
		ID:                  "seed-target-1-v2",
		URL:                 "https://api.anthropic.com",
		Provider:            "anthropic",
		Weight:              1,
		SupportedModelsJSON: `[]`,
		Source:              "config",
		IsActive:            true,
	}
	require.NoError(t, repo.Seed(seedAgain))

	// Verify: WebUI modification preserved
	final, err := repo.GetByURL("https://api.anthropic.com")
	require.NoError(t, err)
	assert.Equal(t, `["gpt-*"]`, final.SupportedModelsJSON, "WebUI modification should be preserved after re-seed")
}
