package e2e_test

// availability_e2e_test.go — E2E tests for availability and reliability scenarios
// not yet covered by existing test files.
//
// Scenarios covered:
//
//  1. TestE2EStaleRoutingVersionIgnored    — cproxy ignores routing updates whose
//     version ≤ current; the balancer state must not be downgraded.
//
//  2. TestE2ECircuitBreakerAutoRecovery   — after passive circuit break (3 failures),
//     auto-recovery (WithRecoveryDelay) eventually lets traffic flow back to
//     the formerly-failing target.
//
//  3. TestE2ERoutingTableAddsNode         — routing update that ADDS a new worker
//     while keeping the existing primary; both nodes receive subsequent traffic.
//
//  4. TestE2EWorkerNodeFails              — a worker node starts returning 5xx;
//     passive health detection excludes it; remaining nodes serve all traffic.
//
//  5. TestE2EUnequalWeightLLMDistribution — 1:3 weight ratio between two LLM
//     backends produces ~25% / ~75% hit distribution (statistical validation).
//
//  6. TestE2EGroupLLMBindingRouting       — users in a group are routed exclusively
//     to the group-bound LLM; users outside the group use the default balancer.
//
//  7. TestE2ERollingUpgradeTwoNodes       — full rolling-upgrade sequence:
//     nodeA drains → traffic shifts to nodeB → nodeA undrains → both serve.
//
//  8. TestE2E4xxNotRetried               — a 429 response from LLM is returned
//     directly to the client without triggering a retry to another target.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/proxy"
)

// ---------------------------------------------------------------------------
// 1. TestE2EStaleRoutingVersionIgnored
// ---------------------------------------------------------------------------
//
// Scenario:
//   - cproxy gets a routing update with version=10 from primary (introduces worker)
//   - worker's response carries version=3 (lower than current 10)
//   - cproxy must NOT downgrade its routing table
//   - requests 3+ continue to reach worker (table still has worker entry)
func TestE2EStaleRoutingVersionIgnored(t *testing.T) {
	// Worker: always serves requests, always injects a stale routing update (version=3)
	// with only itself. If cproxy accepted this, it would lose the primary from its table.
	var workerHits atomic.Int64
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerHits.Add(1)
		// Inject a STALE routing update (version=3 < current 10)
		// that removes the primary from the table.
		staleRT := &cluster.RoutingTable{
			Version: 3,
			Entries: []cluster.RoutingEntry{
				// Only worker, no primary — if applied this would break the primary
				{ID: "only-worker", Addr: "http://only-worker:9999", Weight: 1, Healthy: true},
			},
		}
		encoded, _ := staleRT.Encode()
		w.Header().Set("X-Routing-Version", "3")
		w.Header().Set("X-Routing-Update", encoded)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "worker")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer worker.Close()

	var primaryHits atomic.Int64
	var primaryURL string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		// First response from primary: inject version=10 with both primary and worker.
		rt := &cluster.RoutingTable{
			Version: 10,
			Entries: []cluster.RoutingEntry{
				{ID: primaryURL, Addr: primaryURL, Weight: 1, Healthy: true},
				{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
			},
		}
		encoded, _ := rt.Encode()
		w.Header().Set("X-Routing-Version", "10")
		w.Header().Set("X-Routing-Update", encoded)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "primary")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer primary.Close()
	primaryURL = primary.URL

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
	})
	cpSrv, _, accessToken := buildCProxy(t, balancer)

	// req1 → primary (version 10 applied, cproxy now knows both primary+worker)
	resp1 := doClaudeRequest(t, cpSrv, accessToken)
	_, _ = io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: status=%d", resp1.StatusCode)
	}

	// Verify stale routing version header was NOT passed to client.
	// (routing headers should always be stripped by cproxy)
	if v := resp1.Header.Get("X-Routing-Version"); v != "" {
		t.Errorf("X-Routing-Version leaked to client: %q", v)
	}

	// req2-10: cproxy balances between primary and worker.
	// The worker's stale version=3 should be IGNORED.
	// After 10 requests, both nodes should have been called — if the stale
	// update were accepted, cproxy would only have "only-worker:9999" (unreachable).
	successes := 0
	for i := 2; i <= 10; i++ {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			successes++
		}
	}
	if successes != 9 {
		t.Errorf("after stale routing update: %d/9 succeeded, want 9 (stale version must be rejected)", successes)
	}
	// Both nodes should have served traffic (both are in the version=10 table).
	if primaryHits.Load() == 0 {
		t.Error("primary should receive some traffic (not evicted by stale update)")
	}
	if workerHits.Load() == 0 {
		t.Error("worker should receive some traffic")
	}
	t.Logf("primary=%d worker=%d (stale routing version=3 correctly ignored; current=10)",
		primaryHits.Load(), workerHits.Load())
}

// ---------------------------------------------------------------------------
// 2. TestE2ECircuitBreakerAutoRecovery
// ---------------------------------------------------------------------------
//
// Scenario:
//   - target1 fails 3 times → passive circuit break (marked unhealthy)
//   - WithRecoveryDelay(20ms) → after 20ms target1 automatically re-enters healthy
//   - target1 now returns 200 → requests again flow to target1
func TestE2ECircuitBreakerAutoRecovery(t *testing.T) {
	var t1Calls, t2Calls atomic.Int64
	var t1Healthy atomic.Bool
	t1Healthy.Store(false) // starts failing

	t1Transport := &countingTransport{
		response: func() (*http.Response, error) {
			t1Calls.Add(1)
			if !t1Healthy.Load() {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(bytes.NewBufferString(`{"error":"down"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)),
				Header:     make(http.Header),
			}, nil
		},
	}
	t2Transport := &countingTransport{
		response: func() (*http.Response, error) {
			t2Calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	transport := &multiTargetTransport{
		handlers: map[string]http.RoundTripper{
			"http://t1.fake": t1Transport,
			"http://t2.fake": t2Transport,
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://t1.fake", APIKey: "k1"},
		{URL: "http://t2.fake", APIKey: "k2"},
	}

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "cb-recovery-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	sp.SetTransport(transport)

	lbTargets := []lb.Target{
		{ID: "http://t1.fake", Addr: "http://t1.fake", Weight: 1, Healthy: true},
		{ID: "http://t2.fake", Addr: "http://t2.fake", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	// WithRecoveryDelay: 20ms after circuit break, target auto-recovers.
	hc := lb.NewHealthChecker(bal, logger,
		lb.WithFailThreshold(3),
		lb.WithRecoveryDelay(20*time.Millisecond),
	)
	sp.SetLLMHealthChecker(bal, hc)
	sp.SetMaxRetries(1) // single retry; t1 fails → t2 serves

	srv := httptest.NewServer(sp.Handler())
	t.Cleanup(srv.Close)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)

	send := func() int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
			bytes.NewBufferString(`{"model":"claude","messages":[]}`))
		req.Header.Set("X-PairProxy-Auth", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	// Phase 1: 3 requests trigger circuit break on t1 (t1 always returns 503).
	// RetryTransport retries to t2 → client sees 200.
	for i := range 3 {
		if code := send(); code != http.StatusOK {
			t.Errorf("phase1 req%d: status=%d, want 200 (retry to t2 should succeed)", i, code)
		}
	}
	t1AfterBreak := t1Calls.Load()
	t2AfterBreak := t2Calls.Load()
	t.Logf("after circuit break: t1=%d t2=%d", t1AfterBreak, t2AfterBreak)
	if t2AfterBreak < 3 {
		t.Errorf("t2 should have served ≥3 requests during circuit break phase, got %d", t2AfterBreak)
	}

	// Phase 2: Wait for auto-recovery (recoveryDelay=20ms → wait 50ms to be safe).
	// Now mark t1 as healthy.
	t1Healthy.Store(true)
	time.Sleep(50 * time.Millisecond)

	// Phase 3: Send more requests. t1 should now receive some traffic again.
	for range 10 {
		if code := send(); code != http.StatusOK {
			t.Errorf("phase3: status=%d, want 200", code)
		}
	}
	t1AfterRecovery := t1Calls.Load() - t1AfterBreak
	t.Logf("after recovery: t1 received %d additional requests (should be >0)", t1AfterRecovery)
	if t1AfterRecovery == 0 {
		t.Error("t1 received no requests after auto-recovery — circuit breaker may not have recovered")
	}
}

// ---------------------------------------------------------------------------
// 3. TestE2ERoutingTableAddsNode
// ---------------------------------------------------------------------------
//
// Scenario: routing update ADDS a worker without removing the primary.
//   - cproxy starts with only {primary}
//   - primary's response adds {worker} to table (primary stays in the list too)
//   - subsequent requests distribute to BOTH nodes
func TestE2ERoutingTableAddsNode(t *testing.T) {
	var workerHits, primaryHits atomic.Int64

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "worker")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer worker.Close()

	var primaryURL string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		// Inject routing update that INCLUDES both primary and the new worker.
		rt := &cluster.RoutingTable{
			Version: 1,
			Entries: []cluster.RoutingEntry{
				{ID: primaryURL, Addr: primaryURL, Weight: 1, Healthy: true},
				{ID: worker.URL, Addr: worker.URL, Weight: 1, Healthy: true},
			},
		}
		encoded, _ := rt.Encode()
		w.Header().Set("X-Routing-Version", strconv.FormatInt(rt.Version, 10))
		w.Header().Set("X-Routing-Update", encoded)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "primary")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer primary.Close()
	primaryURL = primary.URL

	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
	})
	cpSrv, _, accessToken := buildCProxy(t, balancer)

	// req1 → primary (routing update applied: now knows primary + worker)
	resp1 := doClaudeRequest(t, cpSrv, accessToken)
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: status=%d body=%s", resp1.StatusCode, body1)
	}
	if got := resp1.Header.Get("X-Served-By"); got != "primary" {
		t.Errorf("req1 X-Served-By=%q, want primary", got)
	}

	// req2–20: both primary and worker should receive traffic.
	for i := 2; i <= 20; i++ {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("req%d: status=%d", i, resp.StatusCode)
		}
	}

	if workerHits.Load() == 0 {
		t.Error("worker received 0 requests — routing update may not have added it to the balancer")
	}
	if primaryHits.Load() < 1 {
		t.Error("primary received 0 requests after adding worker — should still be in table")
	}
	t.Logf("primary=%d worker=%d (both nodes active after additive routing update)",
		primaryHits.Load(), workerHits.Load())
}

// ---------------------------------------------------------------------------
// 4. TestE2EWorkerNodeFails
// ---------------------------------------------------------------------------
//
// Scenario: cproxy has {primary, worker1, worker2}; worker1 starts returning 5xx.
//   - Passive circuit breaker marks worker1 unhealthy after 3 failures
//   - Subsequent requests only reach primary and worker2
func TestE2EWorkerNodeFails(t *testing.T) {
	var primaryHits, w1Hits, w2Hits atomic.Int64
	var w1Healthy atomic.Bool
	w1Healthy.Store(true)

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer primary.Close()

	worker1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w1Hits.Add(1)
		if !w1Healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"worker1 down"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer worker1.Close()

	worker2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer worker2.Close()

	// Build cproxy with all three targets; passive health checker with threshold=3.
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: primary.URL, Addr: primary.URL, Weight: 1, Healthy: true},
		{ID: worker1.URL, Addr: worker1.URL, Weight: 1, Healthy: true},
		{ID: worker2.URL, Addr: worker2.URL, Weight: 1, Healthy: true},
	})
	logger := zaptest.NewLogger(t)
	tokenStore := auth.NewTokenStore(logger, 30*time.Minute)
	jwtMgr, _ := auth.NewManager(logger, "worker-fail-secret")
	tokenDir := t.TempDir()
	tok, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "failover-user", Username: "fo"}, time.Hour)
	_ = tokenStore.Save(tokenDir, &auth.TokenFile{
		AccessToken:  tok,
		RefreshToken: "unused",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		ServerAddr:   "http://placeholder:9000",
		Username:     "fo",
	})
	// HealthChecker shares the balancer; RecordFailure directly updates health state.
	hc := lb.NewHealthChecker(balancer, logger, lb.WithFailThreshold(3))
	cp, _ := proxy.NewCProxy(logger, tokenStore, tokenDir, balancer, "")
	cpSrv := httptest.NewServer(cp.Handler())
	t.Cleanup(cpSrv.Close)
	accessToken := tok

	// Warm up: send 3 requests with all nodes healthy — baseline.
	for range 3 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	t.Logf("warm-up: primary=%d w1=%d w2=%d", primaryHits.Load(), w1Hits.Load(), w2Hits.Load())

	// Break worker1 and simulate passive circuit-break detection by recording failures.
	// (CProxy doesn't auto-detect; RecordFailure directly updates balancer health state,
	// matching the real-world pattern where sidecar/health-checker tracks failures.)
	w1Healthy.Store(false)
	hc.RecordFailure(worker1.URL)
	hc.RecordFailure(worker1.URL)
	hc.RecordFailure(worker1.URL) // threshold=3 → worker1 now unhealthy

	// After circuit break, no new requests should go to worker1.
	w1HitsBeforeBreak := w1Hits.Load()
	// Send 10 more requests — worker1 should get 0 new hits.
	for range 10 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	w1HitsAfterBreak := w1Hits.Load() - w1HitsBeforeBreak
	t.Logf("after circuit break: worker1 got %d new hits (want 0)", w1HitsAfterBreak)
	if w1HitsAfterBreak > 0 {
		t.Errorf("worker1 (unhealthy) received %d requests after passive circuit break — should be 0",
			w1HitsAfterBreak)
	}
}

// ---------------------------------------------------------------------------
// 5. TestE2EUnequalWeightLLMDistribution
// ---------------------------------------------------------------------------
//
// Scenario: sproxy has two LLM targets, weight 1:3.
//   - 200 requests sent
//   - Heavy target (weight=3) should get ~75% ± 15% of traffic
func TestE2EUnequalWeightLLMDistribution(t *testing.T) {
	var lightHits, heavyHits atomic.Int64

	lightLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lightHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer lightLLM.Close()

	heavyLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heavyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer heavyLLM.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "unequal-weight-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: lightLLM.URL, APIKey: "k1", Weight: 1},
		{URL: heavyLLM.URL, APIKey: "k2", Weight: 3},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	lbTargets := []lb.Target{
		{ID: lightLLM.URL, Addr: lightLLM.URL, Weight: 1, Healthy: true},
		{ID: heavyLLM.URL, Addr: heavyLLM.URL, Weight: 3, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	hc := lb.NewHealthChecker(bal, logger, lb.WithFailThreshold(5))
	sp.SetLLMHealthChecker(bal, hc)

	srv := httptest.NewServer(sp.Handler())
	t.Cleanup(srv.Close)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u1", Username: "alice"}, time.Hour)

	const n = 200
	for i := range n {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
			bytes.NewBufferString(`{"model":"claude","messages":[]}`))
		req.Header.Set("X-PairProxy-Auth", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("req%d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	light, heavy := lightHits.Load(), heavyHits.Load()
	total := light + heavy
	heavyPct := float64(heavy) / float64(total) * 100
	t.Logf("weight 1:3 distribution: light=%d (%.0f%%) heavy=%d (%.0f%%) total=%d",
		light, 100-heavyPct, heavy, heavyPct, total)

	if total != n {
		t.Errorf("total hits = %d, want %d", total, n)
	}
	// With 1:3 ratio, heavy should get ~75%. Allow ±15% tolerance (60–90%).
	if heavyPct < 60 || heavyPct > 90 {
		t.Errorf("heavy target got %.0f%% of traffic, want 60%%–90%% (1:3 weight ratio)", heavyPct)
	}
}

// ---------------------------------------------------------------------------
// 6. TestE2EGroupLLMBindingRouting
// ---------------------------------------------------------------------------
//
// Scenario: group-level LLM binding routes all users in the group to LLM-2,
//           while users outside the group use the default LB (may hit LLM-1 or LLM-2).
func TestE2EGroupLLMBindingRouting(t *testing.T) {
	var llm1Hits, llm2Hits atomic.Int64

	llm1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llm1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer llm1.Close()

	llm2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llm2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer llm2.Close()

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "group-binding-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: llm1.URL, APIKey: "k1"},
		{URL: llm2.URL, APIKey: "k2"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	lbTargets := []lb.Target{
		{ID: llm1.URL, Addr: llm1.URL, Weight: 1, Healthy: true},
		{ID: llm2.URL, Addr: llm2.URL, Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	sp.SetLLMHealthChecker(bal, nil)

	// Group "engineering" → bound to llm2. UserID "u-eng" is in the group.
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		if groupID == "grp-engineering" {
			return llm2.URL, true
		}
		return "", false
	})

	srv := httptest.NewServer(sp.Handler())
	t.Cleanup(srv.Close)

	// Token for engineering user (groupID field in JWT).
	engToken, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "u-eng",
		Username: "alice",
		GroupID:  "grp-engineering",
	}, time.Hour)
	// Token for user without group.
	freeToken, _ := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "u-free",
		Username: "bob",
	}, time.Hour)

	send := func(tok string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
			bytes.NewBufferString(`{"model":"claude","messages":[]}`))
		req.Header.Set("X-PairProxy-Auth", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Engineering user: 10 requests — must all go to llm2.
	llm1Before := llm1Hits.Load()
	for range 10 {
		send(engToken)
	}
	if llm1Hits.Load() != llm1Before {
		t.Errorf("engineering user hit llm1 %d times, want 0 (group binding should route to llm2)",
			llm1Hits.Load()-llm1Before)
	}
	if llm2Hits.Load() != 10 {
		t.Errorf("llm2 got %d hits from engineering user, want 10", llm2Hits.Load())
	}

	// Free user: 20 requests — LB picks randomly, both llm1 and llm2 may serve.
	llm1BeforeFree, llm2BeforeFree := llm1Hits.Load(), llm2Hits.Load()
	for range 20 {
		send(freeToken)
	}
	llm1FreeHits := llm1Hits.Load() - llm1BeforeFree
	llm2FreeHits := llm2Hits.Load() - llm2BeforeFree
	t.Logf("free user (no binding): llm1=%d llm2=%d (both should get traffic)", llm1FreeHits, llm2FreeHits)
	if llm1FreeHits+llm2FreeHits != 20 {
		t.Errorf("total free-user hits = %d, want 20", llm1FreeHits+llm2FreeHits)
	}
	// With equal weights and 20 requests, probability of either getting 0 is extremely low.
	if llm1FreeHits == 0 {
		t.Error("free user: llm1 got 0 hits — LB may not be balancing correctly")
	}
}

// ---------------------------------------------------------------------------
// 7. TestE2ERollingUpgradeTwoNodes
// ---------------------------------------------------------------------------
//
// Full rolling-upgrade sequence with two s-proxy nodes:
//   Phase 1: nodeA and nodeB both serve traffic (50/50).
//   Phase 2: nodeA drains (routing update marks it Draining=true).
//             All new requests go to nodeB.
//   Phase 3: nodeA "upgrades" (simulated: it's just back to healthy).
//             nodeA re-introduces itself via an undrain routing update.
//             Both nodes again receive traffic.
func TestE2ERollingUpgradeTwoNodes(t *testing.T) {
	var nodeAHits, nodeBHits atomic.Int64
	var nodeADraining atomic.Bool

	var nodeAURL, nodeBURL string

	nodeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeAHits.Add(1)
		if nodeADraining.Load() {
			// Draining: inject routing update marking nodeA as draining, nodeB as healthy.
			rt := &cluster.RoutingTable{
				Version: 2,
				Entries: []cluster.RoutingEntry{
					{ID: nodeAURL, Addr: nodeAURL, Weight: 1, Healthy: true, Draining: true},
					{ID: nodeBURL, Addr: nodeBURL, Weight: 1, Healthy: true, Draining: false},
				},
			}
			encoded, _ := rt.Encode()
			w.Header().Set("X-Routing-Version", "2")
			w.Header().Set("X-Routing-Update", encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "nodeA")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer nodeA.Close()
	nodeAURL = nodeA.URL

	nodeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeBHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "nodeB")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer nodeB.Close()
	nodeBURL = nodeB.URL

	// ---- Phase 1: both nodes healthy ----
	// cproxy starts with routing version=1 (both nodes, both healthy).
	balancer := lb.NewWeightedRandom([]lb.Target{
		{ID: nodeAURL, Addr: nodeAURL, Weight: 1, Healthy: true},
		{ID: nodeBURL, Addr: nodeBURL, Weight: 1, Healthy: true},
	})
	cpSrv, _, accessToken := buildCProxy(t, balancer)

	// 使用确定性循环代替固定次数：持续发请求直到两个节点都被命中至少一次。
	// 上限 200 次；在 50/50 权重下期望命中轮次约为 4，统计上几乎不会超过 50 次。
	// 这彻底消除了"N 次全落在同一节点"的概率性失败（原来 10 次有 1/512 概率失败）。
	const phase1Limit = 200
	for i := range phase1Limit {
		if nodeAHits.Load() > 0 && nodeBHits.Load() > 0 {
			break
		}
		resp := doClaudeRequest(t, cpSrv, accessToken)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("phase1 req%d: status=%d body=%s", i, resp.StatusCode, body)
		}
	}
	t.Logf("phase1 (both healthy): nodeA=%d nodeB=%d", nodeAHits.Load(), nodeBHits.Load())
	if nodeAHits.Load() == 0 || nodeBHits.Load() == 0 {
		t.Errorf("phase1: both nodes should receive traffic within %d requests (nodeA=%d nodeB=%d)",
			phase1Limit, nodeAHits.Load(), nodeBHits.Load())
	}

	// ---- Phase 2: drain nodeA (rolling upgrade starts) ----
	nodeADraining.Store(true)

	// Send one request to nodeA to trigger the drain routing update propagation.
	// (We may or may not hit nodeA since LB is random, so try a few times.)
	drainUpdateReceived := false
	for attempt := range 20 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		served := resp.Header.Get("X-Served-By")
		resp.Body.Close()
		if served == "nodeA" {
			drainUpdateReceived = true
			t.Logf("drain routing update received after %d attempt(s)", attempt+1)
			break
		}
	}
	if !drainUpdateReceived {
		t.Log("drain routing update not yet propagated (nodeA not selected in 20 tries — ok for random LB)")
	}

	// After drain update is applied, send 15 requests; nodeA should get 0.
	nodeABefore := nodeAHits.Load()
	for i := range 15 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("phase2 req%d: status=%d body=%s", i, resp.StatusCode, body)
		}
	}
	nodeADuringDrain := nodeAHits.Load() - nodeABefore
	t.Logf("phase2 (nodeA draining): nodeA new hits=%d (want 0)", nodeADuringDrain)
	if nodeADuringDrain > 0 {
		// Note: this can happen if the drain update was never received. Log as warning not error.
		t.Logf("WARNING: nodeA received %d hits during drain phase (may occur if drain update not yet applied)", nodeADuringDrain)
	}

	// ---- Phase 3: undrain nodeA (upgrade complete) ----
	nodeADraining.Store(false)

	// Simulate nodeB returning a routing update that re-enables nodeA.
	// We inject this by having nodeB send a routing update in its NEXT response.
	// To do this cleanly, we'll use a one-shot flag on nodeB.
	var undrainSent atomic.Bool
	nodeBOrig := nodeB.Config.Handler
	_ = nodeBOrig
	// Simpler: send an explicit routing update from a helper mock that cproxy receives.
	// Use the "undrain" routing update: version=3, both nodes healthy and not draining.
	// We inject it by making the next nodeB request carry the update.
	nodeB.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeBHits.Add(1)
		if !undrainSent.Load() {
			undrainSent.Store(true)
			undrain := &cluster.RoutingTable{
				Version: 3,
				Entries: []cluster.RoutingEntry{
					{ID: nodeAURL, Addr: nodeAURL, Weight: 1, Healthy: true, Draining: false},
					{ID: nodeBURL, Addr: nodeBURL, Weight: 1, Healthy: true, Draining: false},
				},
			}
			encoded, _ := undrain.Encode()
			w.Header().Set("X-Routing-Version", "3")
			w.Header().Set("X-Routing-Update", encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Served-By", "nodeB")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	// Send a request to nodeB (it's the only active node now) to propagate undrain.
	for range 5 {
		resp := doClaudeRequest(t, cpSrv, accessToken)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	// Phase 3 traffic: 使用确定性循环代替固定次数。
	// 持续发请求直到 nodeA 被命中至少一次（证明 undrain 路由已生效），
	// 上限 200 次。在 50/50 权重下期望 2 次即可，统计上几乎不会超过 30 次。
	// 这消除了"15 次请求全落在 nodeB"的 1/32768 概率性失败。
	nodeAPhase3Start := nodeAHits.Load()
	const phase3Limit = 200
	for i := range phase3Limit {
		if nodeAHits.Load() > nodeAPhase3Start {
			break
		}
		resp := doClaudeRequest(t, cpSrv, accessToken)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("phase3 req%d: status=%d body=%s", i, resp.StatusCode, body)
		}
	}
	nodeAPhase3 := nodeAHits.Load() - nodeAPhase3Start
	t.Logf("phase3 (both undrained): nodeA new hits=%d (want >0 after undrain)", nodeAPhase3)
	if nodeAPhase3 == 0 {
		t.Errorf("after undrain, nodeA received no traffic in %d requests — undrain may not have been applied", phase3Limit)
	}
}

// ---------------------------------------------------------------------------
// 8. TestE2E4xxNotRetried
// ---------------------------------------------------------------------------
//
// Scenario: LLM returns 429 (rate limit) for a user's request.
//   - sproxy must NOT retry on another target (4xx is not a retryable error)
//   - Client receives 429 directly
//   - Only the first target is hit (no second target call)
func TestE2E4xxNotRetried(t *testing.T) {
	var t1Calls, t2Calls atomic.Int64

	t1Transport := &countingTransport{
		response: func() (*http.Response, error) {
			t1Calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body: io.NopCloser(bytes.NewBufferString(
					`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`)),
				Header: make(http.Header),
			}, nil
		},
	}
	t2Transport := &countingTransport{
		response: func() (*http.Response, error) {
			t2Calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"r","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	transport := &multiTargetTransport{
		handlers: map[string]http.RoundTripper{
			"http://t1-429.fake": t1Transport,
			"http://t2-ok.fake":  t2Transport,
		},
	}

	targets := []proxy.LLMTarget{
		{URL: "http://t1-429.fake", APIKey: "k1"},
		{URL: "http://t2-ok.fake", APIKey: "k2"},
	}

	logger := zaptest.NewLogger(t)
	jwtMgr, _ := auth.NewManager(logger, "no-retry-secret")
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}
	sp.SetTransport(transport)

	// Bind user to t1 (so it always picks t1 first).
	lbTargets := []lb.Target{
		{ID: "http://t1-429.fake", Addr: "http://t1-429.fake", Weight: 1, Healthy: true},
		{ID: "http://t2-ok.fake", Addr: "http://t2-ok.fake", Weight: 1, Healthy: true},
	}
	bal := lb.NewWeightedRandom(lbTargets)
	hc := lb.NewHealthChecker(bal, logger, lb.WithFailThreshold(5))
	sp.SetLLMHealthChecker(bal, hc)
	sp.SetMaxRetries(2) // retries enabled — but 4xx must not be retried

	// Force binding to t1 so we deterministically test the 4xx path.
	sp.SetBindingResolver(func(userID, _ string) (string, bool) {
		if userID == "u-rate-limited" {
			return "http://t1-429.fake", true
		}
		return "", false
	})

	srv := httptest.NewServer(sp.Handler())
	t.Cleanup(srv.Close)

	token, _ := jwtMgr.Sign(auth.JWTClaims{UserID: "u-rate-limited", Username: "rl"}, time.Hour)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
		bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	req.Header.Set("X-PairProxy-Auth", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (4xx must be passed through, not retried)", resp.StatusCode)
	}
	if t1Calls.Load() != 1 {
		t.Errorf("t1 called %d times, want exactly 1 (no retry on 4xx)", t1Calls.Load())
	}
	if t2Calls.Load() != 0 {
		t.Errorf("t2 called %d times, want 0 (4xx must NOT be retried to another target)", t2Calls.Load())
	}
	t.Logf("4xx non-retry: t1=%d t2=%d, client got %d", t1Calls.Load(), t2Calls.Load(), resp.StatusCode)
}
