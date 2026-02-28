package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/config"
)

// writeCacheFile writes a RoutingTable JSON file into dir so that
// buildInitialTargets can load it via cluster.LoadFromDir.
func writeCacheFile(t *testing.T, dir string, rt *cluster.RoutingTable) {
	t.Helper()
	data, err := json.Marshal(rt)
	if err != nil {
		t.Fatalf("marshal routing table: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routing-cache.json"), data, 0o600); err != nil {
		t.Fatalf("write routing cache: %v", err)
	}
}

// TestBuildInitialTargets_PrimaryOnly verifies that when only primary is set
// the result contains exactly that one target.
func TestBuildInitialTargets_PrimaryOnly(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{Primary: "http://sp-1:9000"}

	targets, err := buildInitialTargets(cfg, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Addr != "http://sp-1:9000" {
		t.Errorf("Addr = %q, want http://sp-1:9000", targets[0].Addr)
	}
	if !targets[0].Healthy {
		t.Error("config-origin target should be Healthy=true")
	}
}

// TestBuildInitialTargets_StaticTargetsOnly verifies that targets list without
// primary works and all entries are present.
func TestBuildInitialTargets_StaticTargetsOnly(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{
		Targets: []string{"http://w1:9000", "http://w2:9000"},
	}

	targets, err := buildInitialTargets(cfg, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %v", len(targets), targets)
	}
	addrs := map[string]bool{targets[0].Addr: true, targets[1].Addr: true}
	if !addrs["http://w1:9000"] || !addrs["http://w2:9000"] {
		t.Errorf("unexpected addrs: %v", addrs)
	}
}

// TestBuildInitialTargets_CacheOnly verifies that when no config targets are
// set the routing cache is the sole source.
func TestBuildInitialTargets_CacheOnly(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dir := t.TempDir()

	rt := &cluster.RoutingTable{
		Version: 7,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-2", Addr: "http://sp-2:9000", Weight: 1, Healthy: true},
			{ID: "sp-3", Addr: "http://sp-3:9000", Weight: 2, Healthy: false},
		},
	}
	writeCacheFile(t, dir, rt)

	cfg := &config.SProxySect{} // no primary, no targets
	targets, err := buildInitialTargets(cfg, dir, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets from cache, got %d", len(targets))
	}
}

// TestBuildInitialTargets_AllThreeSources verifies the merged result when all
// three sources provide distinct entries.
func TestBuildInitialTargets_AllThreeSources(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dir := t.TempDir()

	// Cache adds sp-3 and sp-4 (sp-1 and sp-2 are already in config).
	rt := &cluster.RoutingTable{
		Version: 3,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-1", Addr: "http://sp-1:9000", Weight: 1, Healthy: true},  // dup
			{ID: "sp-3", Addr: "http://sp-3:9000", Weight: 1, Healthy: true},  // new
			{ID: "sp-4", Addr: "http://sp-4:9000", Weight: 1, Healthy: false}, // new, unhealthy
		},
	}
	writeCacheFile(t, dir, rt)

	cfg := &config.SProxySect{
		Primary: "http://sp-1:9000",
		Targets: []string{"http://sp-2:9000"},
	}
	targets, err := buildInitialTargets(cfg, dir, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// sp-1 (config primary) + sp-2 (config target) + sp-3 + sp-4 (from cache)
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets, got %d: %v", len(targets), targets)
	}

	addrs := make(map[string]bool, len(targets))
	for _, tgt := range targets {
		addrs[tgt.Addr] = true
	}
	for _, want := range []string{"http://sp-1:9000", "http://sp-2:9000", "http://sp-3:9000", "http://sp-4:9000"} {
		if !addrs[want] {
			t.Errorf("missing expected addr %q in targets", want)
		}
	}
}

// TestBuildInitialTargets_Deduplication verifies that the same address in both
// primary and targets appears only once.
func TestBuildInitialTargets_Deduplication(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{
		Primary: "http://sp-1:9000",
		Targets: []string{"http://sp-1:9000", "http://sp-1:9000", "http://sp-2:9000"},
	}

	targets, err := buildInitialTargets(cfg, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// sp-1 appears 3× across config; sp-2 once. Result should be 2 unique entries.
	if len(targets) != 2 {
		t.Fatalf("expected 2 deduplicated targets, got %d: %v", len(targets), targets)
	}
}

// TestBuildInitialTargets_CacheDedupWithConfig verifies that cache entries
// whose address is already covered by config are not added again.
func TestBuildInitialTargets_CacheDedupWithConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dir := t.TempDir()

	// Cache contains sp-1 and sp-2, both already in config.
	rt := &cluster.RoutingTable{
		Version: 1,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-1", Addr: "http://sp-1:9000", Weight: 1, Healthy: true},
			{ID: "sp-2", Addr: "http://sp-2:9000", Weight: 1, Healthy: true},
		},
	}
	writeCacheFile(t, dir, rt)

	cfg := &config.SProxySect{
		Primary: "http://sp-1:9000",
		Targets: []string{"http://sp-2:9000"},
	}
	targets, err := buildInitialTargets(cfg, dir, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 (no duplication from cache), got %d", len(targets))
	}
}

// TestBuildInitialTargets_EmptyTargetsSkipped verifies that empty strings in
// cfg.Targets are silently skipped.
func TestBuildInitialTargets_EmptyTargetsSkipped(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{
		Targets: []string{"", "http://sp-1:9000", ""},
	}

	targets, err := buildInitialTargets(cfg, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target (empty strings skipped), got %d", len(targets))
	}
	if targets[0].Addr != "http://sp-1:9000" {
		t.Errorf("Addr = %q, want http://sp-1:9000", targets[0].Addr)
	}
}

// TestBuildInitialTargets_NoSources verifies that an error is returned when
// no targets can be assembled from any source.
func TestBuildInitialTargets_NoSources(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{} // no primary, no targets

	_, err := buildInitialTargets(cfg, t.TempDir(), logger)
	if err == nil {
		t.Fatal("expected error when no sources are configured, got nil")
	}
}

// TestBuildInitialTargets_EmptyCacheDir verifies that passing an empty
// cacheDir disables disk-cache loading (no panic).
func TestBuildInitialTargets_EmptyCacheDir(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := &config.SProxySect{Primary: "http://sp-1:9000"}

	targets, err := buildInitialTargets(cfg, "", logger) // empty cacheDir
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
}

// TestBuildInitialTargets_ConfigTakesPriorityOverCache verifies that a
// config-supplied entry is used even if a cache entry for the same Addr
// has Healthy:false — i.e., config always wins with Healthy:true.
func TestBuildInitialTargets_ConfigTakesPriorityOverCache(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dir := t.TempDir()

	// Cache marks sp-1 as unhealthy.
	rt := &cluster.RoutingTable{
		Version: 5,
		Entries: []cluster.RoutingEntry{
			{ID: "sp-1", Addr: "http://sp-1:9000", Weight: 1, Healthy: false},
		},
	}
	writeCacheFile(t, dir, rt)

	cfg := &config.SProxySect{Primary: "http://sp-1:9000"}
	targets, err := buildInitialTargets(cfg, dir, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	// Config entry should override cache: Healthy must be true.
	if !targets[0].Healthy {
		t.Error("config primary should be Healthy=true regardless of cache state")
	}
}
