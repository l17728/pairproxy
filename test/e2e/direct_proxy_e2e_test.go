package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/keygen"
	"github.com/l17728/pairproxy/internal/proxy"
)

// 直连模式 e2e 测试：API Key 由用户自己的 PasswordHash 派生，无共享密钥。

// setupDirectProxyTest 创建一个完整的直连测试环境。
func setupDirectProxyTest(t *testing.T) (spURL string, aliceKey string, cleanup func()) {
	t.Helper()

	logger := zaptest.NewLogger(t)
	ctx, cancel := context.WithCancel(context.Background())

	// 1. 内存数据库
	gdb, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gdb))
	writer := db.NewUsageWriter(gdb, logger, 200, 30*time.Second)
	writer.Start(ctx)
	userRepo := db.NewUserRepo(gdb, logger)

	// 2. 创建测试用户 alice
	hash, hashErr := auth.HashPassword(logger, "pass123")
	require.NoError(t, hashErr)
	alice := &db.User{Username: "alice", PasswordHash: hash, IsActive: true}
	require.NoError(t, userRepo.Create(alice))

	// 3. 模拟 LLM 上游
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/messages"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":          "msg_test",
				"type":        "message",
				"role":        "assistant",
				"content":     []map[string]string{{"type": "text", "text": "hello from mock"}},
				"model":       "claude-3-5-sonnet-20241022",
				"stop_reason": "end_turn",
				"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
			})
		case strings.HasSuffix(path, "/chat/completions"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     "chatcmpl_test",
				"object": "chat.completion",
				"choices": []map[string]interface{}{{
					"index":         0,
					"message":       map[string]string{"role": "assistant", "content": "hello from mock"},
					"finish_reason": "stop",
				}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
		default:
			w.WriteHeader(404)
		}
	}))

	// 4. 构建 sproxy
	jwtMgr, jwtErr := auth.NewManager(logger, "e2e-direct-secret")
	require.NoError(t, jwtErr)
	sp, spErr := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockLLM.URL, APIKey: "fake-llm-key", Provider: "anthropic"},
	})
	require.NoError(t, spErr)
	sp.SetDB(gdb)

	// 5. 构建 API Key 缓存和直连 handler
	apiKeyCache, cacheErr := keygen.NewKeyCache(100, 0) // TTL=0 永不过期
	require.NoError(t, cacheErr)
	dbLister := proxy.NewDBUserLister(userRepo)
	directH := proxy.NewDirectProxyHandler(logger, sp, dbLister, apiKeyCache, nil, nil)

	// 6. 构建测试 HTTP 服务
	mux := http.NewServeMux()
	mux.Handle("/anthropic/", directH.HandlerAnthropic())
	openAIH := directH.HandlerOpenAI()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-pp-") {
			openAIH.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(401)
	})

	spServer := httptest.NewServer(mux)

	// 7. 生成 alice 的 API Key（由 PasswordHash 派生）
	aliceKey, keyErr := keygen.GenerateKey("alice", []byte(hash))
	require.NoError(t, keyErr)

	return spServer.URL, aliceKey, func() {
		spServer.Close()
		mockLLM.Close()
		cancel()
		writer.Wait()
	}
}

func TestDirectProxy_Anthropic_Messages(t *testing.T) {
	spURL, aliceKey, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 100,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, _ := http.NewRequest("POST", spURL+"/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", aliceKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	respBody, _ := io.ReadAll(resp.Body)
	var respJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &respJSON))
	assert.Equal(t, "message", respJSON["type"])
}

func TestDirectProxy_OpenAI_ChatCompletions(t *testing.T) {
	spURL, aliceKey, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, _ := http.NewRequest("POST", spURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+aliceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	respBody, _ := io.ReadAll(resp.Body)
	var respJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &respJSON))
	assert.Equal(t, "chat.completion", respJSON["object"])
}

func TestDirectProxy_InvalidKey(t *testing.T) {
	spURL, _, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{"model": "claude-3-5-sonnet", "max_tokens": 10,
		"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req, _ := http.NewRequest("POST", spURL+"/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-not-a-pairproxy-key")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

func TestDirectProxy_MissingKey(t *testing.T) {
	spURL, _, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{"model": "claude-3-5-sonnet", "max_tokens": 10,
		"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req, _ := http.NewRequest("POST", spURL+"/anthropic/v1/messages", bytes.NewReader(body))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

func TestDirectProxy_CacheHit(t *testing.T) {
	spURL, aliceKey, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	send := func() int {
		body, _ := json.Marshal(map[string]interface{}{"model": "claude-3-5-sonnet", "max_tokens": 10,
			"messages": []map[string]string{{"role": "user", "content": "hi"}}})
		req, _ := http.NewRequest("POST", spURL+"/anthropic/v1/messages", bytes.NewReader(body))
		req.Header.Set("x-api-key", aliceKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, 200, send(), "first request (cache miss) should succeed")
	assert.Equal(t, 200, send(), "second request (cache hit) should succeed")
}

func TestDirectProxy_AnthropicPathRewrite(t *testing.T) {
	logger := zap.NewNop()

	var receivedPath string
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_1", "type": "message", "role": "assistant",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"model":       "claude-test", "stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer mockLLM.Close()

	ctx, cancel := context.WithCancel(context.Background())
	gdb, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gdb)
	writer := db.NewUsageWriter(gdb, logger, 200, 30*time.Second)
	writer.Start(ctx)
	userRepo := db.NewUserRepo(gdb, logger)
	hash, _ := auth.HashPassword(logger, "p")
	u := &db.User{Username: "dave", PasswordHash: hash, IsActive: true}
	_ = userRepo.Create(u)
	jwtMgr, _ := auth.NewManager(logger, "test-secret")
	sp, _ := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockLLM.URL, APIKey: "k", Provider: "anthropic"},
	})
	sp.SetDB(gdb)
	defer func() { cancel(); writer.Wait() }()

	cache, _ := keygen.NewKeyCache(10, 0)
	dh := proxy.NewDirectProxyHandler(logger, sp, proxy.NewDBUserLister(userRepo), cache, nil, nil)
	mux := http.NewServeMux()
	mux.Handle("/anthropic/", dh.HandlerAnthropic())
	server := httptest.NewServer(mux)
	defer server.Close()

	// dave 的 key 由自己的 PasswordHash 派生
	daveKey, _ := keygen.GenerateKey("dave", []byte(u.PasswordHash))
	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-test", "max_tokens": 10,
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	})
	req, _ := http.NewRequest("POST", server.URL+"/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", daveKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, "/v1/messages", receivedPath,
		"upstream must receive /v1/messages, not /anthropic/v1/messages")
}

func TestDirectProxy_V1_HybridRoute_DirectKey(t *testing.T) {
	spURL, aliceKey, cleanup := setupDirectProxyTest(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"model": "gpt-4", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, _ := http.NewRequest("POST", spURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+aliceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode, "sk-pp- key on /v1/ should route to direct handler")
}
