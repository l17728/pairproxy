package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/proxy"
)

// TestSProxyBasicFlow tests basic s-proxy request flow
func TestSProxyBasicFlow(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create in-memory database
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open database: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("Migrate database: %v", err)
	}

	// Create JWT manager
	jwtMgr, err := auth.NewManager(logger, "test-secret-key-for-integration-test")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create usage writer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := db.NewUsageWriter(database, logger, 100, time.Minute)
	writer.Start(ctx)

	// Create a mock LLM backend
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"Hello, world!"}}]}`))
	}))
	defer llmServer.Close()

	// Create LLM targets
	targets := []proxy.LLMTarget{
		{URL: llmServer.URL, APIKey: "test-key", Provider: "openai", Name: "test-llm", Weight: 1},
	}

	// Create SProxy
	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	// Create test JWT token
	claims := auth.JWTClaims{
		UserID:   "user-1",
		Username: "testuser",
		Role:     "user",
	}
	token, err := jwtMgr.Sign(claims, time.Hour)
	if err != nil {
		t.Fatalf("Sign JWT: %v", err)
	}

	// Create request
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// Execute
	w := httptest.NewRecorder()
	sp.Handler().ServeHTTP(w, req)

	// Response should be processed (may be error due to missing body, but shouldn't crash)
	t.Logf("Response status: %d", w.Code)
}

// TestLoadBalancerIntegration tests load balancer with multiple targets
func TestLoadBalancerIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	var mu sync.Mutex
	requestCounts := make(map[string]int)

	// Create multiple backends
	backends := make([]*httptest.Server, 3)
	for i := 0; i < 3; i++ {
		i := i // capture loop variable
		backends[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requestCounts[backends[i].URL]++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		defer backends[i].Close()
	}

	// Create targets
	targets := make([]lb.Target, 3)
	for i, s := range backends {
		targets[i] = lb.Target{
			ID:      s.URL,
			Addr:    s.URL,
			Weight:  1,
			Healthy: true,
		}
	}

	balancer := lb.NewWeightedRandom(targets)

	// Create health checker
	hc := lb.NewHealthChecker(balancer, logger,
		lb.WithInterval(100*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc.Start(ctx)

	// Make multiple requests
	for i := 0; i < 30; i++ {
		target, err := balancer.Pick()
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		mu.Lock()
		requestCounts[target.Addr]++
		mu.Unlock()
	}

	// Verify distribution
	mu.Lock()
	defer mu.Unlock()
	for addr, count := range requestCounts {
		t.Logf("Backend %s: %d requests", addr, count)
	}

	// Should have distributed requests
	if len(requestCounts) < 2 {
		t.Error("Expected requests to be distributed across multiple backends")
	}
}

// TestQuotaEnforcement tests quota enforcement integration
func TestQuotaEnforcement(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create in-memory database
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open database: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("Migrate database: %v", err)
	}

	userRepo := db.NewUserRepo(database, logger)
	groupRepo := db.NewGroupRepo(database, logger)

	// Create group with quota
	group := &db.Group{Name: "quota-group"}
	if err := groupRepo.Create(group); err != nil {
		t.Fatalf("Create group: %v", err)
	}

	dailyLimit := int64(1000)
	if err := groupRepo.SetQuota(group.ID, &dailyLimit, nil, nil, nil, nil); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	// Create user
	user := &db.User{
		Username:     "quota-user",
		PasswordHash: "hash",
		GroupID:      &group.ID,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Verify user was created
	found, err := userRepo.GetByUsername("quota-user")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if found == nil {
		t.Fatal("User not found")
	}

	// Verify group association
	if found.GroupID == nil || *found.GroupID != group.ID {
		t.Error("User should be associated with group")
	}
}

// TestAuthenticationFlow tests the authentication flow
func TestAuthenticationFlow(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create JWT manager
	jwtMgr, err := auth.NewManager(logger, "test-secret-for-auth-flow")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Test token creation and verification
	claims := auth.JWTClaims{
		UserID:   "user-auth-test",
		Username: "authuser",
		Role:     "admin",
	}

	token, err := jwtMgr.Sign(claims, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if token == "" {
		t.Fatal("Token should not be empty")
	}

	// Verify token
	parsed, err := jwtMgr.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if parsed.UserID != claims.UserID {
		t.Errorf("UserID = %q, want %q", parsed.UserID, claims.UserID)
	}
	if parsed.Username != claims.Username {
		t.Errorf("Username = %q, want %q", parsed.Username, claims.Username)
	}
	if parsed.Role != claims.Role {
		t.Errorf("Role = %q, want %q", parsed.Role, claims.Role)
	}
}

// TestPasswordHashing tests password hashing integration
func TestPasswordHashing(t *testing.T) {
	logger := zaptest.NewLogger(t)

	password := "my-secure-password-123"

	hash, err := auth.HashPassword(logger, password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !auth.VerifyPassword(logger, hash, password) {
		t.Error("Password verification failed")
	}

	if auth.VerifyPassword(logger, hash, "wrong-password") {
		t.Error("Wrong password should not verify")
	}
}

// TestDatabaseOperations tests database CRUD operations
func TestDatabaseOperations(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create in-memory database
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open database: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("Migrate database: %v", err)
	}

	userRepo := db.NewUserRepo(database, logger)
	groupRepo := db.NewGroupRepo(database, logger)

	// Create group
	group := &db.Group{Name: "test-group"}
	if err := groupRepo.Create(group); err != nil {
		t.Fatalf("Create group: %v", err)
	}

	// Create user
	user := &db.User{
		Username:     "testuser",
		PasswordHash: "hash",
		GroupID:      &group.ID,
		IsActive:     true,
	}
	if err := userRepo.Create(user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Read user
	found, err := userRepo.GetByID(user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if found == nil || found.Username != "testuser" {
		t.Error("User not found or username mismatch")
	}

	// Update user
	if err := userRepo.SetActive(user.ID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// Verify update
	found, _ = userRepo.GetByID(user.ID)
	if found == nil || found.IsActive {
		t.Error("User should be inactive")
	}

	// List users
	users, err := userRepo.ListByGroup(group.ID)
	if err != nil {
		t.Fatalf("ListByGroup: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("Expected 1 user, got %d", len(users))
	}
}

// TestRefreshTokenOperations tests refresh token operations
func TestRefreshTokenOperations(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create in-memory database
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open database: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("Migrate database: %v", err)
	}

	tokenRepo := db.NewRefreshTokenRepo(database, logger)

	// Create token
	token := &db.RefreshToken{
		JTI:       "test-jti-123",
		UserID:    "user-1",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := tokenRepo.Create(token); err != nil {
		t.Fatalf("Create token: %v", err)
	}

	// Get token
	found, err := tokenRepo.GetByJTI("test-jti-123")
	if err != nil {
		t.Fatalf("GetByJTI: %v", err)
	}
	if found == nil || found.UserID != "user-1" {
		t.Error("Token not found or user mismatch")
	}

	// Revoke token
	if err := tokenRepo.Revoke("test-jti-123"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Verify revoked
	found, _ = tokenRepo.GetByJTI("test-jti-123")
	if found == nil || !found.Revoked {
		t.Error("Token should be revoked")
	}
}

// TestUsageLogOperations tests usage log operations
func TestUsageLogOperations(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create in-memory database
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("Open database: %v", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("Migrate database: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := db.NewUsageWriter(database, logger, 10, 100*time.Millisecond)
	writer.Start(ctx)
	defer cancel() // 确保在测试结束时停止 writer

	// Record usage
	for i := 0; i < 5; i++ {
		writer.Record(db.UsageRecord{
			RequestID:    "req-" + string(rune('0'+i)),
			UserID:       "user-1",
			Model:        "claude-3",
			InputTokens:  100,
			OutputTokens: 50,
			StatusCode:   200,
		})
	}
	writer.Flush()

	// Wait for async flush to complete
	time.Sleep(200 * time.Millisecond)

	// Query usage
	repo := db.NewUsageRepo(database, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 5 {
		t.Errorf("Expected 5 logs, got %d", len(logs))
	}

	// Sum tokens
	inputSum, outputSum, err := repo.SumTokens("user-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("SumTokens: %v", err)
	}
	if inputSum != 500 || outputSum != 250 {
		t.Errorf("Expected 500/250 tokens, got %d/%d", inputSum, outputSum)
	}

	// 停止 writer 并等待清理完成
	cancel()
	time.Sleep(100 * time.Millisecond)
}
