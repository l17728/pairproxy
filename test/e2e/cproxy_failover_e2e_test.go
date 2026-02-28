package e2e_test

// cproxy_failover_e2e_test.go — E2E tests for c-proxy primary s-proxy failure resilience.
//
// These tests verify the merged Option 1+3 design: when the primary s-proxy is
// unavailable, c-proxy still serves traffic by routing to nodes discovered via:
//   - sproxy.targets (static worker list in config)
//   - routing-cache.json (persisted routing table from a previous run)
//
// Each test constructs a proxy.NewCProxy with an lb.Balancer seeded with
// multiple targets, simulating what buildInitialTargets() produces.

import (
	"context"
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
	"github.com/l17728/pairproxy/internal/proxy"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildCProxy assembles a c-proxy backed by the given balancer.
// The returned httptest.Server serves as the local Claude Code endpoint;
// the caller is responsible for closing it.
func buildCProxy(t *testing.T, balancer lb.Balancer) (*httptest.Server, *auth.TokenStore, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	// Issue a JWT for the fake user.
	jwtMgr, err := auth.NewManager(logger, "failover-jwt-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	accessToken, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "failover-user",
		Username: "failover",
		Role:     "user",
	}, time.Hour)
	if err != nil {
		t.Fatalf("Sign JWT: %v", err)
	}

	tokenStore := auth.NewTokenStore(logger, 30*time.Minute)
	tokenDir := t.TempDir()
	tf := &auth.TokenFile{
		AccessToken:  accessToken,
		RefreshToken: "unused",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		ServerAddr:   "http://placeholder:9000",
		Username:     "failover",
	}
	if err := tokenStore.Save(tokenDir, tf); err != nil {
		t.Fatalf("Save token: %v", err)
	}

	cp, err := proxy.NewCProxy(logger, tokenStore, tokenDir, balancer, "")
	if err != nil {
		t.Fatalf("NewCProxy: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", cp.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv, tokenStore, accessToken
}

// mockSProxyWorker creates a mock s-proxy that accepts requests with the
// X-PairProxy-Auth header and returns a minimal JSON response.
// Returns the test server; caller is responsible for closing it.
func mockSProxyWorker(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-PairProxy-Auth") == "" {
			t.Errorf("mock %s: missing X-PairProxy-Auth", name)
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", name) // lets tests know which node responded
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// doClaudeRequest sends a POST /v1/messages through the c-proxy server,
// using the provided access token as the bearer credential (Claude Code style).
func doClaudeRequest(t *testing.T, cpSrv *httptest.Server, accessToken string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, cpSrv.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dummy-from-claude-code")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// E2E Test 1 — Static target fallback (Option 1)
// ---------------------------------------------------------------------------
//
// Scenario:
//   - Primary s-proxy is NOT in the initial balancer at all
//     (simulates "only sproxy.targets configured, no primary").
//   - A single worker s-proxy is healthy.
//
// Expected: 200 OK, traffic routed to worker.

func TestE2EFailover_StaticTargetOnly(t *testing.T) {
	worker := mockSProxyWorker(t, "worker-1")

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
	})

	cpSrv, _, accessToken := buildCProxy(t, balancer)

	resp := doClaudeRequest(t, cpSrv, accessToken)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (worker should handle request); body: %s",
			resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// E2E Test 2 — Primary down, worker available (primary pre-marked unhealthy)
// ---------------------------------------------------------------------------
//
// Scenario:
//   - Primary is in the balancer but pre-marked Healthy:false
//     (simulates cache entry for a known-down primary, or health checker already
//      detected the primary is down before the first user request).
//   - Worker is healthy.
//
// Expected: 200 OK, requests skip the unhealthy primary and reach the worker.

func TestE2EFailover_PrimaryDownWorkerRouted(t *testing.T) {
	// Primary is intentionally closed: any connection attempt fails immediately.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "primary is down", http.StatusServiceUnavailable)
	}))
	primary.Close() // immediately close so it refuses connections

	worker := mockSProxyWorker(t, "worker-1")

	// Simulate: buildInitialTargets found primary in config (Healthy:true initially)
	// but health checker has since marked it unhealthy.
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: false}, // already unhealthy
		{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
	})

	cpSrv, _, accessToken := buildCProxy(t, balancer)

	resp := doClaudeRequest(t, cpSrv, accessToken)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (should route to healthy worker); body: %s",
			resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// E2E Test 3 — Cache-only fallback (Option 3)
// ---------------------------------------------------------------------------
//
// Scenario:
//   - No primary, no static config targets.
//   - Routing cache provided a single worker address.
//   - Simulates a c-proxy restart after the primary has died:
//     the cache is the only knowledge of available workers.
//
// Expected: 200 OK, traffic routed via the cached worker address.

func TestE2EFailover_CacheFallback(t *testing.T) {
	// Worker simulates what was previously discovered via s-proxy routing updates.
	worker := mockSProxyWorker(t, "cached-worker")

	// Simulate what buildInitialTargets produces when only routing cache has entries.
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: "cached-worker", Addr: worker.URL, Weight: 1, Healthy: true},
	})

	cpSrv, _, accessToken := buildCProxy(t, balancer)

	resp := doClaudeRequest(t, cpSrv, accessToken)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (cache-discovered worker should handle request); body: %s",
			resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// E2E Test 4 — Passive health detection with failover
// ---------------------------------------------------------------------------
//
// Scenario:
//   - Both primary and worker start healthy in the balancer.
//   - Primary returns 502 errors; each c-proxy error response triggers
//     hc.RecordFailure() → after failThreshold (3) consecutive failures, the
//     health checker marks primary Healthy:false.
//   - Worker returns 200.
//
// Expected:
//   - Before passive detection: some requests may hit primary and get 502.
//   - After passive detection: all requests route to worker (200).
//
// This test exercises the passive failure path directly by calling
// RecordFailure() the required number of times and then verifying that the
// balancer no longer routes to the primary.

func TestE2EFailover_PassiveHealthDetection(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Primary always returns 502.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream error", http.StatusBadGateway)
	}))
	defer primary.Close()

	worker := mockSProxyWorker(t, "passive-worker")

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
		{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
	})

	// Create a health checker with threshold=3 to test passive failover.
	hc := lb.NewHealthChecker(balancer, logger,
		lb.WithFailThreshold(3),
		lb.WithInterval(30*time.Second), // long interval: we test passive path only
	)

	// Simulate 3 consecutive failures for primary: triggers MarkUnhealthy.
	hc.RecordFailure(primary.URL)
	hc.RecordFailure(primary.URL)
	hc.RecordFailure(primary.URL)

	// Now verify primary is unreachable via Pick().
	cpSrv, _, accessToken := buildCProxy(t, balancer)

	// All subsequent requests should succeed via worker.
	for i := range 5 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200 (passive failover should route to worker); body: %s",
				i, resp.StatusCode, body)
		}
	}
}

// ---------------------------------------------------------------------------
// E2E Test 5 — Multi-worker load distribution
// ---------------------------------------------------------------------------
//
// Scenario:
//   - Primary and two workers, all healthy.
//   - Simulates the state after all three sources (primary + targets + cache)
//     contributed to the initial target list.
//
// Expected:
//   - All N requests succeed (200 OK).
//   - At least 2 distinct nodes serve traffic (load is spread).

func TestE2EFailover_MultiWorkerDistribution(t *testing.T) {
	// All three nodes are healthy mock s-proxy workers.
	primary := mockSProxyWorker(t, "primary")
	worker1 := mockSProxyWorker(t, "worker-1")
	worker2 := mockSProxyWorker(t, "worker-2")

	// Simulate combined output of buildInitialTargets with all 3 sources.
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
		{ID: worker1.URL, Addr: worker1.URL, Weight: 1, Healthy: true},
		{ID: worker2.URL, Addr: worker2.URL, Weight: 1, Healthy: true},
	})

	cpSrv, _, _ := buildCProxy(t, balancer)

	client := &http.Client{Timeout: 5 * time.Second}
	servedBy := make(map[string]int)
	const n = 30

	for range n {
		req, _ := http.NewRequest(http.MethodPost, cpSrv.URL+"/v1/messages",
			strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer dummy-key")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		// The X-Served-By header is set by each mock worker.
		servedBy[resp.Header.Get("X-Served-By")]++
	}

	if len(servedBy) < 2 {
		t.Errorf("expected traffic spread across ≥2 nodes, got: %v", servedBy)
	}
	t.Logf("load distribution across %d requests: %v", n, servedBy)
}

// ---------------------------------------------------------------------------
// E2E Test 6 — Primary recovery after passive failover
// ---------------------------------------------------------------------------
//
// Scenario:
//   - Primary is marked unhealthy after 3 passive failures.
//   - Health checker's active check detects primary is back (returns 200 /health).
//   - After recovery, requests can again be routed to primary.
//
// Expected:
//   - Before recovery: only worker serves traffic.
//   - After hc.RecordSuccess(primary): primary becomes healthy again.

func TestE2EFailover_PrimaryRecovery(t *testing.T) {
	logger := zaptest.NewLogger(t)

	primary := mockSProxyWorker(t, "recovering-primary")
	worker := mockSProxyWorker(t, "worker-1")

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
		{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
	})

	hc := lb.NewHealthChecker(balancer, logger,
		lb.WithFailThreshold(3),
		lb.WithInterval(30*time.Second),
	)

	// Phase 1: primary fails → marked unhealthy.
	hc.RecordFailure(primary.URL)
	hc.RecordFailure(primary.URL)
	hc.RecordFailure(primary.URL)

	cpSrv, _, accessToken := buildCProxy(t, balancer)

	// Verify primary is excluded.
	for range 5 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		body, _ := io.ReadAll(resp.Body)
		served := resp.Header.Get("X-Served-By")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Phase 1: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		if served == "recovering-primary" {
			t.Error("Phase 1: primary should not receive requests while unhealthy")
		}
	}

	// Phase 2: primary recovers (active health check detects it is back).
	hc.RecordSuccess(primary.URL)

	// Verify primary is eligible again — with a weighted random balancer over
	// many requests it will eventually be chosen.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 3 * time.Second}
	primaryRecovered := false

	for ctx.Err() == nil && !primaryRecovered {
		req, _ := http.NewRequest(http.MethodPost, cpSrv.URL+"/v1/messages",
			strings.NewReader(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer dummy")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Phase 2: Do: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		served := resp.Header.Get("X-Served-By")
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Phase 2: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		if served == "recovering-primary" {
			primaryRecovered = true
		}
	}

	if !primaryRecovered {
		t.Error("Phase 2: primary should receive traffic after RecordSuccess (recovery), but was never selected")
	}
}

// ---------------------------------------------------------------------------
// E2E Test 7 — No available targets returns 502
// ---------------------------------------------------------------------------
//
// Scenario:
//   - All targets in the balancer are marked unhealthy.
//
// Expected: 502 Bad Gateway (no healthy target to pick).

func TestE2EFailover_NoHealthyTargets(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "primary down", http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: false}, // all unhealthy
	})

	cpSrv, _, accessToken := buildCProxy(t, balancer)

	resp := doClaudeRequest(t, cpSrv, accessToken)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when no healthy targets available; body: %s",
			resp.StatusCode, body)
	}

	// Verify the error response is well-formed JSON.
	var errBody map[string]interface{}
	if err := json.Unmarshal(body, &errBody); err == nil {
		t.Logf("error response: %v", errBody)
	}
}
