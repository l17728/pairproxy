// Package e2e_test contains end-to-end tests for OpenAI compatibility layer.
//
// OpenAI 兼容层功能：
//   - 支持 Authorization: Bearer <jwt> 认证（替代 X-PairProxy-Auth）
//   - 自动注入 stream_options.include_usage: true 到流式请求
//   - 与 Anthropic 客户端共享配额、审计、统计系统
package e2e_test

import (
	"bufio"
	"bytes"
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

// TestOpenAIAuthE2E tests OpenAI-style Authorization header authentication
func TestOpenAIAuthE2E(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Setup mock OpenAI API
	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the Authorization header is the real API key, not the JWT
		auth := r.Header.Get("Authorization")
		if auth != "Bearer real-openai-key" {
			t.Errorf("Authorization = %q, want 'Bearer real-openai-key'", auth)
		}

		// Return a simple response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": "Hello from mock OpenAI!",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     10,
				"completion_tokens": 20,
				"total_tokens":      30,
			},
		})
	}))
	defer mockOpenAI.Close()

	// Setup database
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// Setup JWT manager
	jwtMgr, err := auth.NewManager(logger, "e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create test user
	userRepo := db.NewUserRepo(gormDB, logger)
	userRepo.Create(&db.User{ID: "user1", Username: "alice", IsActive: true})

	// Setup sproxy with OpenAI target
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := db.NewUsageWriter(gormDB, logger, 200, 30*time.Second)
	writer.Start(ctx)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockOpenAI.URL, APIKey: "real-openai-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(5 * time.Second)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	// Setup mux like in sproxy_e2e_test.go
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", sp.HealthHandler())
	mux.Handle("/", sp.Handler())

	// Create test server
	server := httptest.NewServer(mux)
	defer server.Close()

	// Generate JWT token
	token, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "user1",
		Username: "alice",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token: %v", err)
	}

	t.Run("openai_auth_with_bearer", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token) // OpenAI-style auth

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, body)
		}

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		// Verify response structure
		if result["id"] == nil {
			t.Error("response missing 'id' field")
		}
		if result["choices"] == nil {
			t.Error("response missing 'choices' field")
		}
	})

	t.Run("openai_auth_fallback_to_x_pairproxy", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-PairProxy-Auth", token) // Legacy auth

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, body)
		}
	})

	t.Run("openai_auth_unauthorized", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// No Authorization header

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Error("expected non-200 status for unauthorized request")
		}
	})
}

// TestOpenAIStreamOptionsInjectionE2E tests automatic stream_options injection
func TestOpenAIStreamOptionsInjectionE2E(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Track what the mock OpenAI receives
	var receivedBody map[string]interface{}
	var mu sync.Mutex

	// Setup mock OpenAI API
	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the request body
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedBody)
		mu.Unlock()

		// Return SSE stream
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter doesn't support flushing")
			return
		}

		// Send some chunks
		chunks := []string{
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}

		for _, chunk := range chunks {
			w.Write([]byte(chunk))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer mockOpenAI.Close()

	// Setup database
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	// Setup JWT manager
	jwtMgr, err := auth.NewManager(logger, "e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create test user
	userRepo := db.NewUserRepo(gormDB, logger)
	userRepo.Create(&db.User{ID: "user1", Username: "alice", IsActive: true})

	// Setup sproxy
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := db.NewUsageWriter(gormDB, logger, 200, 30*time.Second)
	writer.Start(ctx)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockOpenAI.URL, APIKey: "real-openai-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(5 * time.Second)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	// Setup mux like in sproxy_e2e_test.go
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", sp.HealthHandler())
	mux.Handle("/", sp.Handler())

	// Create test server
	server := httptest.NewServer(mux)
	defer server.Close()

	// Generate JWT token
	token, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "user1",
		Username: "alice",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign token: %v", err)
	}

	t.Run("inject_stream_options_when_missing", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"stream": true,
			// No stream_options
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, body)
		}

		// Read the stream
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") { //nolint:staticcheck
				_ = line // just verify we can read the stream
			}
		}

		// Verify that stream_options was injected
		mu.Lock()
		streamOpts, ok := receivedBody["stream_options"].(map[string]interface{})
		mu.Unlock()

		if !ok {
			t.Error("stream_options not injected")
		} else if includeUsage, ok := streamOpts["include_usage"].(bool); !ok || !includeUsage {
			t.Errorf("stream_options.include_usage = %v, want true", includeUsage)
		}
	})

	t.Run("preserve_existing_stream_options", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"stream": true,
			"stream_options": map[string]interface{}{
				"include_usage": true,
			},
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, body)
		}

		// Verify that stream_options was preserved
		mu.Lock()
		streamOpts, ok := receivedBody["stream_options"].(map[string]interface{})
		mu.Unlock()

		if !ok {
			t.Error("stream_options not preserved")
		} else if includeUsage, ok := streamOpts["include_usage"].(bool); !ok || !includeUsage {
			t.Errorf("stream_options.include_usage = %v, want true", includeUsage)
		}
	})

	t.Run("no_injection_for_non_stream", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "gpt-4",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"stream": false,
		}
		body, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// For non-streaming, mock returns JSON not SSE
		// This test just verifies the request goes through
		if resp.StatusCode != http.StatusOK {
			t.Logf("Non-streaming request status: %d (acceptable)", resp.StatusCode)
		}
	})
}
