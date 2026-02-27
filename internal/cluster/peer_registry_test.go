package cluster

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/lb"
)

func TestPeerRegisterAndList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, balancer, nil, "")
	registry := NewPeerRegistry(logger, mgr)

	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)
	registry.Register("sp-3", "http://sp-3:9000", "sp-3", 2)

	peers := registry.Peers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	peerIDs := map[string]bool{}
	for _, p := range peers {
		peerIDs[p.ID] = true
		if !p.IsHealthy {
			t.Errorf("peer %s should be healthy after registration", p.ID)
		}
	}
	if !peerIDs["sp-2"] || !peerIDs["sp-3"] {
		t.Errorf("expected sp-2 and sp-3 in peers, got %v", peerIDs)
	}
}

func TestPeerDeregister(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, balancer, nil, "")
	registry := NewPeerRegistry(logger, mgr)

	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)
	registry.Register("sp-3", "http://sp-3:9000", "sp-3", 1)
	registry.Deregister("sp-2")

	peers := registry.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after deregister, got %d", len(peers))
	}
	if peers[0].ID != "sp-3" {
		t.Errorf("expected sp-3 after deregister, got %s", peers[0].ID)
	}
}

func TestPeerEvictStale(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, balancer, nil, "")
	registry := NewPeerRegistry(logger, mgr)
	registry.ttl = 50 * time.Millisecond // 很短的 TTL

	registry.Register("old", "http://old:9000", "old", 1)

	// 等待超时
	time.Sleep(100 * time.Millisecond)

	registry.EvictStale()

	peers := registry.Peers()
	if len(peers) != 0 {
		t.Errorf("expected 0 peers after eviction, got %d", len(peers))
	}
}

func TestPeerHeartbeatUpdatesLastSeen(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, balancer, nil, "")
	registry := NewPeerRegistry(logger, mgr)
	registry.ttl = 50 * time.Millisecond

	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)
	time.Sleep(30 * time.Millisecond)

	// 再次 Register = 心跳，更新 LastSeen
	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)
	time.Sleep(30 * time.Millisecond)

	// TTL=50ms，但总时间=60ms，最后心跳是30ms前，未超时
	registry.EvictStale()

	peers := registry.Peers()
	if len(peers) != 1 {
		t.Errorf("peer should still be alive after heartbeat, got %d peers", len(peers))
	}
}

func TestPeerSyncToBalancer(t *testing.T) {
	logger := zaptest.NewLogger(t)
	balancer := lb.NewWeightedRandom(nil)
	mgr := NewManager(logger, balancer, nil, "")
	registry := NewPeerRegistry(logger, mgr)

	registry.Register("sp-2", "http://sp-2:9000", "sp-2", 1)
	registry.Register("sp-3", "http://sp-3:9000", "sp-3", 1)

	// Balancer 应该有 2 个目标
	targets := balancer.Targets()
	if len(targets) != 2 {
		t.Fatalf("balancer should have 2 targets, got %d", len(targets))
	}

	registry.Deregister("sp-3")
	targets = balancer.Targets()
	if len(targets) != 1 {
		t.Fatalf("balancer should have 1 target after deregister, got %d", len(targets))
	}
	if targets[0].ID != "sp-2" {
		t.Errorf("remaining target should be sp-2, got %s", targets[0].ID)
	}
}
