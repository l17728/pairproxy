package cluster

import (
	"strconv"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/lb"
)

// Manager 维护当前路由表，并提供给 s-proxy 注入响应头。
// 同时接受外部更新（peer 注册/注销时调用）以触发版本递增。
type Manager struct {
	logger   *zap.Logger
	balancer lb.Balancer
	cacheDir string // 路由表持久化目录

	mu      sync.RWMutex
	current RoutingTable

	version atomic.Int64 // 单调递增版本号
}

// NewManager 创建 ClusterManager。
// initialTargets: 初始目标列表（通常来自配置文件）。
// cacheDir: 路由表缓存目录（空串表示不持久化）。
func NewManager(
	logger *zap.Logger,
	balancer lb.Balancer,
	initialTargets []lb.Target,
	cacheDir string,
) *Manager {
	m := &Manager{
		logger:   logger.Named("cluster_manager"),
		balancer: balancer,
		cacheDir: cacheDir,
	}

	// 尝试从缓存加载版本号（重启后继续递增）
	if cacheDir != "" {
		if cached, err := LoadFromDir(cacheDir); err == nil && cached != nil {
			m.version.Store(cached.Version)
		}
	}

	m.applyTargets(initialTargets)
	return m
}

// CurrentTable 返回当前路由表快照。
func (m *Manager) CurrentTable() RoutingTable {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.current
	cp.Entries = make([]RoutingEntry, len(m.current.Entries))
	copy(cp.Entries, m.current.Entries)
	return cp
}

// UpdateTargets 原子更新目标列表，版本号递增，并持久化。
func (m *Manager) UpdateTargets(targets []lb.Target) {
	m.balancer.UpdateTargets(targets)
	m.applyTargets(targets)
}

// MarkHealthy 将节点标记为健康并更新路由表版本。
func (m *Manager) MarkHealthy(id string) {
	m.balancer.MarkHealthy(id)
	m.rebuildFromBalancer()
}

// MarkUnhealthy 将节点标记为不健康并更新路由表版本。
func (m *Manager) MarkUnhealthy(id string) {
	m.balancer.MarkUnhealthy(id)
	m.rebuildFromBalancer()
}

// applyTargets 将 lb.Target 列表同步到 RoutingTable，版本递增。
func (m *Manager) applyTargets(targets []lb.Target) {
	entries := make([]RoutingEntry, len(targets))
	for i, t := range targets {
		entries[i] = RoutingEntry{
			ID:      t.ID,
			Addr:    t.Addr,
			Weight:  t.Weight,
			Healthy: t.Healthy,
		}
	}

	ver := m.version.Add(1)
	m.mu.Lock()
	m.current = RoutingTable{Version: ver, Entries: entries}
	m.mu.Unlock()

	m.persist()
}

// rebuildFromBalancer 从 Balancer 当前状态重建路由表（健康状态变更时调用）。
func (m *Manager) rebuildFromBalancer() {
	targets := m.balancer.Targets()
	entries := make([]RoutingEntry, len(targets))
	for i, t := range targets {
		entries[i] = RoutingEntry{
			ID:      t.ID,
			Addr:    t.Addr,
			Weight:  t.Weight,
			Healthy: t.Healthy,
		}
	}

	ver := m.version.Add(1)
	m.mu.Lock()
	m.current = RoutingTable{Version: ver, Entries: entries}
	m.mu.Unlock()

	m.persist()
}

// persist 将当前路由表异步写入磁盘（忽略错误，仅日志记录）。
func (m *Manager) persist() {
	if m.cacheDir == "" {
		return
	}
	rt := m.CurrentTable()
	go func() {
		if err := rt.SaveToDir(m.cacheDir); err != nil {
			m.logger.Warn("failed to persist routing table", zap.Error(err))
		}
	}()
}

// InjectResponseHeaders 将路由表版本注入响应头。
// 若 clientVersion < 当前版本，同时注入完整路由表（X-Routing-Update）。
func (m *Manager) InjectResponseHeaders(headers interface {
	Set(key, value string)
}, clientVersion int64) {
	rt := m.CurrentTable()

	// 总是注入当前版本号
	headers.Set("X-Routing-Version", strconv.FormatInt(rt.Version, 10))

	if clientVersion < rt.Version {
		encoded, err := rt.Encode()
		if err != nil {
			m.logger.Warn("failed to encode routing table for header", zap.Error(err))
			return
		}
		headers.Set("X-Routing-Update", encoded)
	}
}
