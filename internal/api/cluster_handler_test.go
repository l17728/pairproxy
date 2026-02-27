package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/cluster"
	"github.com/pairproxy/pairproxy/internal/db"
	"github.com/pairproxy/pairproxy/internal/lb"
)

// setupClusterHandler 创建用于测试的 ClusterHandler（无鉴权）。
func setupClusterHandler(t *testing.T) (*ClusterHandler, *cluster.PeerRegistry, *db.UsageWriter, *db.UsageRepo, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	balancer := lb.NewWeightedRandom(nil)
	mgr := cluster.NewManager(logger, balancer, nil, "")
	registry := cluster.NewPeerRegistry(logger, mgr)

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	repo := db.NewUsageRepo(gormDB, logger)
	handler := NewClusterHandler(logger, registry, writer, "") // 空密钥=不鉴权

	t.Cleanup(func() {
		cancel()
		writer.Wait()
	})

	return handler, registry, writer, repo, cancel
}

// ---------------------------------------------------------------------------
// TestRegisterEndpoint
// ---------------------------------------------------------------------------

func TestRegisterEndpoint(t *testing.T) {
	handler, registry, _, _, _ := setupClusterHandler(t)

	payload := cluster.RegisterPayload{
		ID:         "sp-2",
		Addr:       "http://sp-2:9000",
		SourceNode: "sp-2",
		Weight:     1,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.handleRegister(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	peers := registry.Peers()
	if len(peers) != 1 || peers[0].ID != "sp-2" {
		t.Errorf("expected sp-2 in registry, got %+v", peers)
	}
}

func TestRegisterEndpointMissingFields(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	body, _ := json.Marshal(map[string]string{"id": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.handleRegister(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRegisterEndpointAuthRequired(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := cluster.NewManager(logger, balancer, nil, "")
	registry := cluster.NewPeerRegistry(logger, mgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	handler := NewClusterHandler(logger, registry, writer, "my-secret")

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-2", Addr: "http://sp-2:9000"})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	// 不设置 Authorization
	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}

	// 带正确密钥
	req2 := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req2.Header.Set("Authorization", "Bearer my-secret")
	rr2 := httptest.NewRecorder()
	handler.handleRegister(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("status with correct auth = %d, want 200", rr2.Code)
	}
}

// ---------------------------------------------------------------------------
// TestUsageReportEndpoint
// ---------------------------------------------------------------------------

func TestUsageReportEndpoint(t *testing.T) {
	handler, _, writer, repo, cancel := setupClusterHandler(t)

	records := []db.UsageRecord{
		{
			RequestID:    "req-from-sp2",
			UserID:       "user-sp2",
			Model:        "claude-3",
			InputTokens:  100,
			OutputTokens: 50,
			IsStreaming:  false,
			StatusCode:   200,
			SourceNode:   "sp-2",
		},
	}
	payload := cluster.UsageReportPayload{
		SourceNode: "sp-2",
		Records:    records,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/usage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.handleUsageReport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// 等待异步写入
	cancel()
	writer.Wait()

	logs, err := repo.Query(db.UsageFilter{UserID: "user-sp2", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].InputTokens != 100 || logs[0].OutputTokens != 50 {
		t.Errorf("tokens = %d/%d, want 100/50", logs[0].InputTokens, logs[0].OutputTokens)
	}
	if logs[0].SourceNode != "sp-2" {
		t.Errorf("SourceNode = %q, want 'sp-2'", logs[0].SourceNode)
	}
}

// ---------------------------------------------------------------------------
// TestGetRoutingEndpoint
// ---------------------------------------------------------------------------

func TestGetRoutingEndpoint(t *testing.T) {
	handler, registry, _, _, _ := setupClusterHandler(t)

	// 注册一个 peer
	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)

	req := httptest.NewRequest(http.MethodGet, "/cluster/routing", nil)
	rr := httptest.NewRecorder()

	handler.handleGetRouting(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	peers, ok := result["peers"]
	if !ok {
		t.Error("response should have 'peers' field")
	}
	peersSlice, ok := peers.([]interface{})
	if !ok || len(peersSlice) != 1 {
		t.Errorf("expected 1 peer, got %v", peers)
	}
}
