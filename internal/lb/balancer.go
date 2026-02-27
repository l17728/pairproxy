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
	ID      string // 唯一标识（URL 或自定义 ID）
	Addr    string // 完整地址，如 "http://sp-2:9000"
	Weight  int    // 权重（≥1）
	Healthy bool   // 当前是否健康
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
