package lb

import (
	"math/rand/v2"
)

// WeightedRandomBalancer 按权重随机选取健康节点。
// 权重为 0 或不健康的节点不参与选取。
type WeightedRandomBalancer struct {
	tl targetList
}

// NewWeightedRandom 创建加权随机负载均衡器。
// targets 中 Weight ≤ 0 的节点会被视为权重 1。
func NewWeightedRandom(targets []Target) *WeightedRandomBalancer {
	b := &WeightedRandomBalancer{}
	b.tl.update(normalizeWeights(targets))
	return b
}

// Pick 按权重随机选取一个健康节点。
func (b *WeightedRandomBalancer) Pick() (*Target, error) {
	b.tl.mu.RLock()
	defer b.tl.mu.RUnlock()

	// 计算健康节点总权重
	total := 0
	for i := range b.tl.targets {
		if b.tl.targets[i].Healthy {
			total += b.tl.targets[i].Weight
		}
	}
	if total == 0 {
		return nil, ErrNoHealthyTarget
	}

	// 加权随机选取
	r := rand.IntN(total)
	for i := range b.tl.targets {
		t := &b.tl.targets[i]
		if !t.Healthy {
			continue
		}
		r -= t.Weight
		if r < 0 {
			// 返回副本，避免调用方持有内部引用
			cp := *t
			return &cp, nil
		}
	}

	// 理论上不应到达（浮点/整数边界保护）
	for i := range b.tl.targets {
		if b.tl.targets[i].Healthy {
			cp := b.tl.targets[i]
			return &cp, nil
		}
	}
	return nil, ErrNoHealthyTarget
}

// UpdateTargets 原子替换目标列表。
func (b *WeightedRandomBalancer) UpdateTargets(targets []Target) {
	b.tl.update(normalizeWeights(targets))
}

// MarkHealthy 将指定 ID 的节点标记为健康。
func (b *WeightedRandomBalancer) MarkHealthy(id string) {
	b.tl.setHealthy(id, true)
}

// MarkUnhealthy 将指定 ID 的节点标记为不健康。
func (b *WeightedRandomBalancer) MarkUnhealthy(id string) {
	b.tl.setHealthy(id, false)
}

// Targets 返回当前目标列表的快照。
func (b *WeightedRandomBalancer) Targets() []Target {
	return b.tl.snapshot()
}

// normalizeWeights 将 Weight ≤ 0 的节点权重修正为 1。
func normalizeWeights(targets []Target) []Target {
	cp := make([]Target, len(targets))
	copy(cp, targets)
	for i := range cp {
		if cp[i].Weight <= 0 {
			cp[i].Weight = 1
		}
	}
	return cp
}
