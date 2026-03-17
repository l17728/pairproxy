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

	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

const testClusterSecret = "test-shared-secret-for-cluster"

// setupClusterHandler 创建用于测试的 ClusterHandler（使用 testClusterSecret 鉴权）。
// 所有测试必须在请求中携带正确 Bearer token，模拟生产环境 fail-closed 行为。
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
	handler := NewClusterHandler(logger, registry, writer, testClusterSecret)

	t.Cleanup(func() {
		cancel()
		writer.Wait()
	})

	return handler, registry, writer, repo, cancel
}

// authReq 构造带正确 Bearer 认证头的请求，供测试使用
func authReq(method, target string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("Authorization", "Bearer "+testClusterSecret)
	return r
}

// ---------------------------------------------------------------------------
// TestClusterAuth — P0-4 fail-closed 认证测试
// ---------------------------------------------------------------------------

// TestClusterAuth_EmptySecret 验证 shared_secret 为空时所有请求被拒绝（fail-closed）。
func TestClusterAuth_EmptySecret(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := cluster.NewManager(logger, balancer, nil, "")
	registry := cluster.NewPeerRegistry(logger, mgr)

	ctx, cancel := context.WithCancel(context.Background())
	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	// 注意 defer 顺序（LIFO）：先 Wait 后 cancel，确保 cancel 先执行使 goroutine 退出。
	defer writer.Wait()
	defer cancel()

	// 空密钥：任何请求都应被拒绝
	handler := NewClusterHandler(logger, registry, writer, "")

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-2", Addr: "http://sp-2:9000"})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// 即使带了 Bearer ""，也应拒绝
	req.Header.Set("Authorization", "Bearer ")

	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("empty shared_secret: status = %d, want 401 (fail-closed)", rr.Code)
	}
}

// TestClusterAuth_WrongSecret 验证错误密钥被拒绝（401）。
func TestClusterAuth_WrongSecret(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-2", Addr: "http://sp-2:9000"})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-secret")

	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: status = %d, want 401", rr.Code)
	}
}

// TestClusterAuth_MissingHeader 验证缺少 Authorization 头时返回 401。
func TestClusterAuth_MissingHeader(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-2", Addr: "http://sp-2:9000"})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// 故意不设置 Authorization 头

	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing auth header: status = %d, want 401", rr.Code)
	}
}

// TestClusterAuth_CorrectSecret 验证正确密钥允许通过（200）。
func TestClusterAuth_CorrectSecret(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-auth-ok", Addr: "http://sp-auth-ok:9000"})
	req := authReq(http.MethodPost, "/api/internal/register", body)

	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("correct secret: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestClusterAuth_AllEndpointsEnforced 验证三个端点都执行鉴权。
func TestClusterAuth_AllEndpointsEnforced(t *testing.T) {
	handler, registry, _, _, _ := setupClusterHandler(t)
	registry.Register("sp-x", "http://sp-x:9000", "sp-x", 1)

	endpoints := []struct {
		method  string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodPost, "/api/internal/register", handler.handleRegister},
		{http.MethodPost, "/api/internal/usage", handler.handleUsageReport},
		{http.MethodGet, "/cluster/routing", handler.handleGetRouting},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			var body []byte
			if ep.method == http.MethodPost {
				body, _ = json.Marshal(map[string]string{})
			}
			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(ep.method, ep.path, bytes.NewReader(body))
			} else {
				req = httptest.NewRequest(ep.method, ep.path, nil)
			}
			// 不带 Authorization
			rr := httptest.NewRecorder()
			ep.handler(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without auth: status = %d, want 401", ep.method, ep.path, rr.Code)
			}
		})
	}
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
	req := authReq(http.MethodPost, "/api/internal/register", body)
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
	req := authReq(http.MethodPost, "/api/internal/register", body)
	rr := httptest.NewRecorder()

	handler.handleRegister(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRegisterEndpointAuthRequired(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	body, _ := json.Marshal(cluster.RegisterPayload{ID: "sp-2", Addr: "http://sp-2:9000"})

	// 不带认证头
	req := httptest.NewRequest(http.MethodPost, "/api/internal/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.handleRegister(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}

	// 带正确密钥
	req2 := authReq(http.MethodPost, "/api/internal/register", body)
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
	req := authReq(http.MethodPost, "/api/internal/usage", body)
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

	req := authReq(http.MethodGet, "/cluster/routing", nil)
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

// ---------------------------------------------------------------------------
// TestConfigSnapshot_Endpoint — GET /api/internal/config-snapshot
// ---------------------------------------------------------------------------

// TestConfigSnapshot_NotAvailableWithoutRepos 验证未设置 repos 时返回 503。
func TestConfigSnapshot_NotAvailableWithoutRepos(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := authReq(http.MethodGet, "/api/internal/config-snapshot", nil)
	rr := httptest.NewRecorder()
	handler.handleConfigSnapshot(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

// TestConfigSnapshot_ReturnsData 验证设置 repos 后端点返回正确的 JSON 快照。
func TestConfigSnapshot_ReturnsData(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	if err := userRepo.Create(&db.User{ID: "u1", Username: "alice", PasswordHash: "h1", IsActive: true}); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	handler.SetConfigRepos(userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	req := authReq(http.MethodGet, "/api/internal/config-snapshot", nil)
	rr := httptest.NewRecorder()
	handler.handleConfigSnapshot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var snap cluster.ConfigSnapshot
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}

	if len(snap.Users) != 1 || snap.Users[0].Username != "alice" {
		t.Errorf("expected 1 user 'alice', got %v", snap.Users)
	}
	if snap.Version.IsZero() {
		t.Error("snapshot Version should not be zero")
	}
}

// TestConfigSnapshot_RequiresAuth 验证无鉴权时返回 401。
func TestConfigSnapshot_RequiresAuth(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/config-snapshot", nil)
	rr := httptest.NewRecorder()
	handler.handleConfigSnapshot(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// TestConfigSnapshot_MethodNotAllowed 验证非 GET 请求返回 405。
func TestConfigSnapshot_MethodNotAllowed(t *testing.T) {
	handler, _, _, _, _ := setupClusterHandler(t)

	req := authReq(http.MethodPost, "/api/internal/config-snapshot", []byte(`{}`))
	rr := httptest.NewRecorder()
	handler.handleConfigSnapshot(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}
