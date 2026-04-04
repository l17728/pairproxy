// Package lb 提供负载均衡器接口和实现。
package lb

import (
	"errors"
	"sync"
)

// ErrNoHealthyTarget 当所有目标都不可用时返回。
var ErrNoHealthyTarget = errors.New("no healthy target available")

// Target 表示一个上游节点。
type Target struct {
	ID              string   // 唯一标识（URL 或自定义 ID）
	Addr            string   // 完整地址，如 "http://sp-2:9000"
	Weight          int      // 权重（≥1）
	Healthy         bool     // 当前是否健康
	Draining        bool     // 是否处于排水模式（不接受新流量）
	SupportedModels []string // 支持的模型名列表（含通配符）；空 = 不限制
	AutoModel       string   // auto 模式使用的模型名；空 = 降级或透传
}

// Balancer 负载均衡器接口。
type Balancer interface {
	// Pick 选取一个健康节点，无健康节点时返回 ErrNoHealthyTarget。
	Pick() (*Target, error)

	// UpdateTargets 原子替换目标列表（用于路由表更新）。
	UpdateTargets(targets []Target)

	// MarkHealthy 将指定 ID 的节点标记为健康。
	MarkHealthy(id string)

	// MarkUnhealthy 将指定 ID 的节点标记为不健康。
	MarkUnhealthy(id string)

	// Targets 返回当前目标列表的快照（只读）。
	Targets() []Target

	// SetDraining 设置节点的排水状态。
	// draining=true 时，节点不接受新流量（Pick 会跳过该节点）。
	SetDraining(id string, draining bool)

	// IsDraining 检查节点是否处于排水模式。
	IsDraining(id string) bool
}

// ---------------------------------------------------------------------------
// targetList：带读写锁的目标列表，Balancer 实现共用。
// ---------------------------------------------------------------------------

type targetList struct {
	mu      sync.RWMutex
	targets []Target
}

func (tl *targetList) snapshot() []Target {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	cp := make([]Target, len(tl.targets))
	copy(cp, tl.targets)
	return cp
}

func (tl *targetList) update(targets []Target) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.targets = make([]Target, len(targets))
	copy(tl.targets, targets)
}

func (tl *targetList) setHealthy(id string, healthy bool) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	for i := range tl.targets {
		if tl.targets[i].ID == id {
			tl.targets[i].Healthy = healthy
			return
		}
	}
}

func (tl *targetList) setDraining(id string, draining bool) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	for i := range tl.targets {
		if tl.targets[i].ID == id {
			tl.targets[i].Draining = draining
			return
		}
	}
}

func (tl *targetList) isDraining(id string) bool {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	for _, t := range tl.targets {
		if t.ID == id {
			return t.Draining
		}
	}
	return false
}
