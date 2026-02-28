package e2e_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/quota"
)

// TestE2EFullStack exercises the complete request path:
//
//	Claude Code → cproxy → sproxy → mock LLM
//
// cproxy holds a saved access token issued by sproxy's JWT manager.
// The test verifies:
//  1. cproxy replaces the dummy Authorization with X-PairProxy-Auth
//  2. sproxy validates the JWT, strips X-PairProxy-Auth, and injects the real API key
//  3. The mock LLM receives the request with the correct Authorization header
//  4. The response propagates back to the caller (200 OK)
func TestE2EFullStack(t *testing.T) {
	// Step 1: Mock LLM — verifies header substitution has happened.
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer real-api-key" {
			t.Errorf("LLM got Authorization = %q, want 'Bearer real-api-key'", got)
		}
		if r.Header.Get("X-PairProxy-Auth") != "" {
			t.Error("X-PairProxy-Auth must be stripped before reaching the LLM")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":30,"output_tokens":15}}`)
	}))
	defer mockLLM.Close()

	logger := zaptest.NewLogger(t)

	// Step 2: Build sproxy with real JWT validation and a user in the DB.
	jwtMgr, err := auth.NewManager(logger, "fullstack-jwt-secret")
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
	t.Cleanup(func() { cancel(); writer.Wait() })

	userRepo := db.NewUserRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)

	user := &db.User{ID: "fs-user-1", Username: "fullstack", PasswordHash: "x", IsActive: true}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockLLM.URL, APIKey: "real-api-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	checker := quota.NewChecker(logger, userRepo, usageRepo, quota.NewQuotaCache(time.Minute))
	sp.SetQuotaChecker(checker)

	spMux := http.NewServeMux()
	spMux.Handle("/", sp.Handler())
	spSrv := httptest.NewServer(spMux)
	defer spSrv.Close()

	// Step 3: Issue an access token for the user (as if issued by POST /auth/login).
	accessToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "fs-user-1",
		Username: "fullstack",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign JWT: %v", err)
	}

	// Step 4: Build cproxy with the saved token pointing to sproxy.
	tokenStore := auth.NewTokenStore(logger, 30*time.Minute)
	tokenDir := t.TempDir()
	tf := &auth.TokenFile{
		AccessToken:  accessToken,
		RefreshToken: "unused",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		ServerAddr:   spSrv.URL,
		Username:     "fullstack",
	}
	if err := tokenStore.Save(tokenDir, tf); err != nil {
		t.Fatalf("Save token: %v", err)
	}

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: spSrv.URL, Addr: spSrv.URL, Weight: 1, Healthy: true},
	})
	cp, err := proxy.NewCProxy(logger, tokenStore, tokenDir, balancer, "")
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}

	cpMux := http.NewServeMux()
	cpMux.Handle("/", cp.Handler())
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	// Step 5: Send a request through cproxy, as Claude Code would.
	req, err := http.NewRequest(http.MethodPost, cpSrv.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Claude Code sends a dummy key; cproxy replaces it with X-PairProxy-Auth.
	req.Header.Set("Authorization", "Bearer dummy-from-claude-code")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do request through full stack: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("full-stack status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "message") {
		t.Errorf("response body = %s, want JSON with 'message'", body)
	}
}

// TestE2ENonStreamingFlow verifies that a non-streaming JSON request is proxied
// correctly and token usage is recorded in the database.
func TestE2ENonStreamingFlow(t *testing.T) {
	const (
		wantInput  = 120
		wantOutput = 45
	)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Header substitution must have happened before reaching LLM.
		if got := r.Header.Get("Authorization"); got != "Bearer e2e-real-key" {
			t.Errorf("Authorization = %q, want 'Bearer e2e-real-key'", got)
		}
		if r.Header.Get("X-PairProxy-Auth") != "" {
			t.Error("X-PairProxy-Auth should be stripped before forwarding to LLM")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","model":"claude-3-5-sonnet",`+
			`"usage":{"input_tokens":120,"output_tokens":45}}`)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)
	token := env.createUser("nonstream-e2e-user", "", nil, nil)

	body := `{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}]}`
	respHTTP := env.doRequest(token, body)
	defer respHTTP.Body.Close()

	if respHTTP.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respHTTP.Body)
		t.Fatalf("status = %d, want 200; body: %s", respHTTP.StatusCode, b)
	}
	if ct := respHTTP.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json for non-streaming response", ct)
	}
	_, _ = io.Copy(io.Discard, respHTTP.Body)

	// Wait for async token write to complete.
	env.cancel()
	env.Writer.Wait()

	logs, err := env.UsageRepo.Query(db.UsageFilter{UserID: "nonstream-e2e-user", Limit: 5})
	if err != nil {
		t.Fatalf("Query usage logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", logs[0].InputTokens, wantInput)
	}
	if logs[0].OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", logs[0].OutputTokens, wantOutput)
	}
	if logs[0].IsStreaming {
		t.Error("IsStreaming should be false for a non-streaming request")
	}
	if logs[0].StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", logs[0].StatusCode)
	}
}

// TestE2EAuthInvalid verifies that a request with an invalid JWT is rejected
// with 401 before reaching the LLM.
func TestE2EAuthInvalid(t *testing.T) {
	llmCalled := false
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)

	// Send request with a completely invalid JWT.
	req, err := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/messages",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-PairProxy-Auth", "not-a-jwt")
	req.Header.Set("Content-Type", "application/json")

	resp, err := env.Client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for invalid JWT", resp.StatusCode)
	}
	if llmCalled {
		t.Error("LLM should not be called when authentication fails")
	}
}

// TestE2EHealthCheck verifies the /health endpoint is always accessible
// and returns 200 without authentication.
func TestE2EHealthCheck(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockLLM.Close()

	env := setupE2E(t, mockLLM.URL)

	resp, err := env.Client.Get(env.Server.URL + "/health")
	if err != nil {
		t.Fatalf("health GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("health Content-Type = %q, want application/json", ct)
	}
}
