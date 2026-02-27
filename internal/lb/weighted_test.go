package lb

import (
	"testing"
)

func TestWeightedPickSingleHealthy(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("got ID=%q, want 'a'", got.ID)
	}
}

func TestAllUnhealthy(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: false},
		{ID: "b", Addr: "http://b", Weight: 2, Healthy: false},
	})
	_, err := b.Pick()
	if err != ErrNoHealthyTarget {
		t.Errorf("err = %v, want ErrNoHealthyTarget", err)
	}
}

func TestSkipUnhealthy(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: false},
		{ID: "b", Addr: "http://b", Weight: 1, Healthy: true},
	})
	for i := 0; i < 20; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if got.ID != "b" {
			t.Errorf("iteration %d: got %q, want 'b'", i, got.ID)
		}
	}
}

func TestWeightedDistribution(t *testing.T) {
	// 权重 1:3，采样 4000 次，预期 b 出现约 75%
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
		{ID: "b", Addr: "http://b", Weight: 3, Healthy: true},
	})

	counts := map[string]int{}
	const N = 4000
	for i := 0; i < N; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[got.ID]++
	}

	ratioB := float64(counts["b"]) / float64(N)
	// 期望 75%，允许 ±10%（即 65%~85%）
	if ratioB < 0.65 || ratioB > 0.85 {
		t.Errorf("b pick ratio = %.2f, want ~0.75 (±0.10); counts=%v", ratioB, counts)
	}
}

func TestMarkHealthyUnhealthy(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
		{ID: "b", Addr: "http://b", Weight: 1, Healthy: true},
	})

	// 标记 a 为不健康
	b.MarkUnhealthy("a")
	for i := 0; i < 10; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick after MarkUnhealthy: %v", err)
		}
		if got.ID != "b" {
			t.Errorf("got %q, want 'b' after marking 'a' unhealthy", got.ID)
		}
	}

	// 恢复 a 为健康
	b.MarkHealthy("a")
	sawA := false
	for i := 0; i < 50; i++ {
		got, _ := b.Pick()
		if got.ID == "a" {
			sawA = true
			break
		}
	}
	if !sawA {
		t.Error("expected 'a' to be picked after MarkHealthy, but never selected in 50 tries")
	}
}

func TestUpdateTargets(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 1, Healthy: true},
	})

	// 替换目标列表
	b.UpdateTargets([]Target{
		{ID: "x", Addr: "http://x", Weight: 1, Healthy: true},
		{ID: "y", Addr: "http://y", Weight: 1, Healthy: true},
	})

	// 旧节点 a 不应出现
	ids := map[string]bool{}
	for i := 0; i < 40; i++ {
		got, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		ids[got.ID] = true
	}
	if ids["a"] {
		t.Error("old target 'a' should not appear after UpdateTargets")
	}
	if !ids["x"] && !ids["y"] {
		t.Error("neither 'x' nor 'y' was selected after UpdateTargets")
	}
}

func TestNormalizeWeights(t *testing.T) {
	b := NewWeightedRandom([]Target{
		{ID: "a", Addr: "http://a", Weight: 0, Healthy: true},  // 应被修正为 1
		{ID: "b", Addr: "http://b", Weight: -5, Healthy: true}, // 应被修正为 1
	})
	// 不应 panic，且能正常 Pick
	got, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick with zero/negative weight: %v", err)
	}
	if got == nil {
		t.Error("expected non-nil target")
	}
}

func TestTargetsSnapshot(t *testing.T) {
	original := []Target{
		{ID: "a", Addr: "http://a", Weight: 2, Healthy: true},
	}
	b := NewWeightedRandom(original)

	snap := b.Targets()
	if len(snap) != 1 || snap[0].ID != "a" {
		t.Errorf("Targets() = %v, want [{a ...}]", snap)
	}

	// 修改快照不应影响内部状态
	snap[0].Healthy = false
	got, err := b.Pick()
	if err != nil {
		t.Errorf("Pick should still succeed after modifying snapshot: %v", err)
	}
	_ = got
}
