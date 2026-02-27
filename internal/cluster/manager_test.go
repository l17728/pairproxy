package cluster

import (
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/lb"
)

func makeManager(t *testing.T, targets []lb.Target) (*Manager, *lb.WeightedRandomBalancer) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(targets)
	mgr := NewManager(logger, balancer, targets, "")
	return mgr, balancer
}

func TestManagerCurrentTable(t *testing.T) {
	targets := []lb.Target{
		{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true},
		{ID: "b", Addr: "http://b:9000", Weight: 2, Healthy: false},
	}
	mgr, _ := makeManager(t, targets)

	rt := mgr.CurrentTable()
	if rt.Version < 1 {
		t.Errorf("Version = %d, want ≥1", rt.Version)
	}
	if len(rt.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(rt.Entries))
	}
	if rt.Entries[0].ID != "a" || rt.Entries[1].ID != "b" {
		t.Errorf("unexpected entries: %+v", rt.Entries)
	}
}

func TestManagerVersionIncrements(t *testing.T) {
	targets := []lb.Target{{ID: "x", Addr: "http://x:9000", Weight: 1, Healthy: true}}
	mgr, _ := makeManager(t, targets)

	v1 := mgr.CurrentTable().Version

	mgr.MarkUnhealthy("x")
	v2 := mgr.CurrentTable().Version

	mgr.MarkHealthy("x")
	v3 := mgr.CurrentTable().Version

	if v2 <= v1 {
		t.Errorf("version should increase: v1=%d v2=%d", v1, v2)
	}
	if v3 <= v2 {
		t.Errorf("version should increase: v2=%d v3=%d", v2, v3)
	}
}

func TestManagerInjectHeaders_AlwaysSendsVersion(t *testing.T) {
	targets := []lb.Target{{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true}}
	mgr, _ := makeManager(t, targets)

	h := http.Header{}
	mgr.InjectResponseHeaders(h, 0) // client version 0 < server version

	if h.Get("X-Routing-Version") == "" {
		t.Error("X-Routing-Version should always be set")
	}
	if h.Get("X-Routing-Update") == "" {
		t.Error("X-Routing-Update should be set when client version < server version")
	}
}

func TestManagerInjectHeaders_NoUpdateWhenCurrent(t *testing.T) {
	targets := []lb.Target{{ID: "a", Addr: "http://a:9000", Weight: 1, Healthy: true}}
	mgr, _ := makeManager(t, targets)

	rt := mgr.CurrentTable()
	h := http.Header{}
	mgr.InjectResponseHeaders(h, rt.Version) // client already up-to-date

	if h.Get("X-Routing-Version") == "" {
		t.Error("X-Routing-Version should always be set")
	}
	if h.Get("X-Routing-Update") != "" {
		t.Error("X-Routing-Update should NOT be set when client is up-to-date")
	}
}

func TestManagerUpdateTargets(t *testing.T) {
	targets := []lb.Target{{ID: "old", Addr: "http://old:9000", Weight: 1, Healthy: true}}
	mgr, balancer := makeManager(t, targets)

	v1 := mgr.CurrentTable().Version

	newTargets := []lb.Target{
		{ID: "new1", Addr: "http://new1:9000", Weight: 1, Healthy: true},
		{ID: "new2", Addr: "http://new2:9000", Weight: 1, Healthy: true},
	}
	mgr.UpdateTargets(newTargets)

	rt := mgr.CurrentTable()
	if rt.Version <= v1 {
		t.Error("version should increase after UpdateTargets")
	}
	if len(rt.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(rt.Entries))
	}

	// Balancer should have new targets
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		got, err := balancer.Pick()
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seen[got.ID] = true
	}
	if seen["old"] {
		t.Error("old target should not be in balancer after UpdateTargets")
	}
}

func TestManagerPersistAndLoad(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)

	targets := []lb.Target{
		{ID: "p1", Addr: "http://p1:9000", Weight: 1, Healthy: true},
	}
	mgr := NewManager(logger, balancer, targets, dir)
	rt := mgr.CurrentTable()

	// Wait for async persist goroutine to write file
	var loaded *RoutingTable
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		loaded, err = LoadFromDir(dir)
		if err == nil && loaded != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if loaded == nil {
		t.Fatal("routing table not persisted to cache dir within 2s")
	}
	if loaded.Version != rt.Version {
		t.Errorf("cached version = %d, want %d", loaded.Version, rt.Version)
	}
}
