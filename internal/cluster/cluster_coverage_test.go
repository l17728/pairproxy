package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

// ---------------------------------------------------------------------------
// ConfigSyncer.LastSyncAt (0% → covered)
// ---------------------------------------------------------------------------

func TestConfigSyncer_LastSyncAt_InitiallyZero(t *testing.T) {
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

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  "http://127.0.0.1:19999", // unreachable
		SharedSecret: "secret",
		Interval:     time.Second,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	// Before any sync, LastSyncAt should be 0
	if syncer.LastSyncAt() != 0 {
		t.Errorf("LastSyncAt() before any sync = %d, want 0", syncer.LastSyncAt())
	}
}

func TestConfigSyncer_LastSyncAt_UpdatedAfterSuccessfulSync(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	snap := ConfigSnapshot{
		Version:     time.Now(),
		Users:       []db.User{},
		Groups:      []db.Group{},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	beforeSync := time.Now().Unix()

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "secret",
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	lastSync := syncer.LastSyncAt()
	if lastSync == 0 {
		t.Error("LastSyncAt() should be non-zero after successful sync")
	}
	if lastSync < beforeSync {
		t.Errorf("LastSyncAt() = %d, expected >= %d", lastSync, beforeSync)
	}
}

// ---------------------------------------------------------------------------
// ConfigSyncer — default interval (0 uses defaultSyncInterval)
// ---------------------------------------------------------------------------

func TestConfigSyncer_DefaultInterval(t *testing.T) {
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

	// Interval = 0 should use defaultSyncInterval (30s)
	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  "http://127.0.0.1:19999",
		SharedSecret: "secret",
		Interval:     0, // trigger default
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	if syncer.interval != defaultSyncInterval {
		t.Errorf("interval = %v, want %v", syncer.interval, defaultSyncInterval)
	}
}

// ---------------------------------------------------------------------------
// Manager.NewManager — with cache dir (persist branch)
// ---------------------------------------------------------------------------

func TestManager_NewManager_WithCacheDir_LoadsVersion(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)

	// Create first manager to write cache
	targets := []lb.Target{
		{ID: "n1", Addr: "http://n1:9000", Weight: 1, Healthy: true},
	}
	b1 := lb.NewWeightedRandom(targets)
	mgr1 := NewManager(logger, b1, targets, dir)
	v1 := mgr1.CurrentTable().Version

	// Allow async persist to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err := LoadFromDir(dir)
		if err == nil && loaded != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Create second manager with the same cache dir — should restore version
	b2 := lb.NewWeightedRandom(targets)
	mgr2 := NewManager(logger, b2, targets, dir)
	v2 := mgr2.CurrentTable().Version

	// v2 should be > v1 (loaded v1 from cache, then incremented once for applyTargets)
	if v2 <= v1 {
		t.Errorf("second manager version %d should be > first manager version %d (loaded from cache)", v2, v1)
	}
}

// ---------------------------------------------------------------------------
// Manager.NewManager — without cache dir (no-cache branch)
// ---------------------------------------------------------------------------

func TestManager_NewManager_NoCacheDir(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []lb.Target{
		{ID: "x", Addr: "http://x:9000", Weight: 1, Healthy: true},
	}
	b := lb.NewWeightedRandom(targets)
	mgr := NewManager(logger, b, targets, "") // empty cacheDir

	rt := mgr.CurrentTable()
	if rt.Version < 1 {
		t.Errorf("version = %d, want >= 1", rt.Version)
	}
	if len(rt.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(rt.Entries))
	}
}

// ---------------------------------------------------------------------------
// Manager.InjectResponseHeaders — client version equal (no update)
// ---------------------------------------------------------------------------

func TestManager_InjectResponseHeaders_ClientUpToDate(t *testing.T) {
	mgr, _ := makeManager(t, []lb.Target{
		{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true},
	})

	rt := mgr.CurrentTable()
	h := http.Header{}
	mgr.InjectResponseHeaders(h, rt.Version) // equal to current

	if h.Get("X-Routing-Version") == "" {
		t.Error("X-Routing-Version must always be set")
	}
	if h.Get("X-Routing-Update") != "" {
		t.Error("X-Routing-Update must NOT be set when client is up-to-date")
	}
}

// ---------------------------------------------------------------------------
// Manager.InjectResponseHeaders — client version ahead (should not happen, but safe)
// ---------------------------------------------------------------------------

func TestManager_InjectResponseHeaders_ClientAheadOfServer(t *testing.T) {
	mgr, _ := makeManager(t, []lb.Target{
		{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true},
	})

	rt := mgr.CurrentTable()
	h := http.Header{}
	// Client claims a version HIGHER than server — no update should be sent
	mgr.InjectResponseHeaders(h, rt.Version+100)

	if h.Get("X-Routing-Version") == "" {
		t.Error("X-Routing-Version must always be set")
	}
	if h.Get("X-Routing-Update") != "" {
		t.Error("X-Routing-Update must NOT be set when client version >= server version")
	}
}

// ---------------------------------------------------------------------------
// Manager.persist — cacheDir empty skips write (branch covered implicitly)
// ---------------------------------------------------------------------------

func TestManager_Persist_NoCacheDir_DoesNotPanic(t *testing.T) {
	logger := zaptest.NewLogger(t)
	b := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, b, nil, "")

	// persist() with empty cacheDir should be a no-op, no panic
	mgr.persist()
}

// ---------------------------------------------------------------------------
// Manager.persist — async write to invalid dir logs warning (not panic)
// ---------------------------------------------------------------------------

func TestManager_Persist_InvalidCacheDir_DoesNotPanic(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []lb.Target{
		{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true},
	}
	b := lb.NewWeightedRandom(targets)
	// Use a path that doesn't exist to trigger a write error
	_ = NewManager(logger, b, targets, "/nonexistent/path/that/does/not/exist")

	// The persist goroutine will fail but should not panic. Give it time to run.
	time.Sleep(50 * time.Millisecond)
	// If we reach here without panic, the test passes.
}

// ---------------------------------------------------------------------------
// ConfigSyncer.pull — shared secret header is sent
// ---------------------------------------------------------------------------

func TestConfigSyncer_Pull_SendsAuthHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	const secret = "my-shared-secret"
	receivedAuth := ""

	snap := ConfigSnapshot{
		Version:     time.Now(),
		Users:       []db.User{},
		Groups:      []db.Group{},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: secret,
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	expected := "Bearer " + secret
	if receivedAuth != expected {
		t.Errorf("Authorization header = %q, want %q", receivedAuth, expected)
	}
}

// ---------------------------------------------------------------------------
// ConfigSyncer.pull — no shared secret omits Authorization header
// ---------------------------------------------------------------------------

func TestConfigSyncer_Pull_NoSharedSecret_NoAuthHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	receivedAuth := "not-empty"

	snap := ConfigSnapshot{
		Version:     time.Now(),
		Users:       []db.User{},
		Groups:      []db.Group{},
		LLMTargets:  []*db.LLMTarget{},
		LLMBindings: []db.LLMBinding{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer srv.Close()

	userRepo := db.NewUserRepo(gormDB, logger)
	groupRepo := db.NewGroupRepo(gormDB, logger)
	llmTargetRepo := db.NewLLMTargetRepo(gormDB, logger)
	llmBindingRepo := db.NewLLMBindingRepo(gormDB, logger)

	syncer := NewConfigSyncer(logger, ConfigSyncerConfig{
		PrimaryAddr:  srv.URL,
		SharedSecret: "", // no secret
		Interval:     50 * time.Millisecond,
	}, gormDB, userRepo, groupRepo, llmTargetRepo, llmBindingRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	syncer.Start(ctx)
	syncer.Wait()

	if receivedAuth != "" {
		t.Errorf("Authorization header should be empty when no shared secret, got %q", receivedAuth)
	}
}
