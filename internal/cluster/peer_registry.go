package cluster

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/lb"
)

const (
	defaultHeartbeatTTL = 90 * time.Second // peer 超时阈值：2 个心跳周期
)

// PeerInfo 记录一个已注册 sp-2 节点的信息。
type PeerInfo struct {
	ID          string    `json:"id"`
	Addr        string    `json:"addr"`    // HTTP 地址，如 "http://sp-2:9000"
	Weight      int       `json:"weight"`  // 负载权重（≥1）
	LastSeen    time.Time `json:"last_seen"`
	IsHealthy   bool      `json:"is_healthy"`
	SourceNode  string    `json:"source_node"` // peer 自报的节点标识
}

// PeerRegistry 管理已注册的 sp-2 节点。
// sp-1 持有此 Registry；sp-2 通过 POST /api/internal/register 心跳注册。
type PeerRegistry struct {
	logger  *zap.Logger
	manager *Manager // 同步到负载均衡器
	ttl     time.Duration

	mu    sync.RWMutex
	peers map[string]*PeerInfo // key: peer.ID
}

// NewPeerRegistry 创建 PeerRegistry。
func NewPeerRegistry(logger *zap.Logger, manager *Manager) *PeerRegistry {
	return &PeerRegistry{
		logger:  logger.Named("peer_registry"),
		manager: manager,
		ttl:     defaultHeartbeatTTL,
		peers:   make(map[string]*PeerInfo),
	}
}

// Register 注册或更新一个 peer（心跳调用）。
func (pr *PeerRegistry) Register(id, addr, sourceNode string, weight int) {
	if weight <= 0 {
		weight = 1
	}

	pr.mu.Lock()
	existing, exists := pr.peers[id]
	if exists {
		existing.LastSeen = time.Now()
		existing.Addr = addr
		existing.Weight = weight
		existing.SourceNode = sourceNode
		existing.IsHealthy = true
	} else {
		pr.peers[id] = &PeerInfo{
			ID:         id,
			Addr:       addr,
			Weight:     weight,
			LastSeen:   time.Now(),
			IsHealthy:  true,
			SourceNode: sourceNode,
		}
		pr.logger.Info("peer registered",
			zap.String("id", id),
			zap.String("addr", addr),
			zap.Int("weight", weight),
		)
	}
	pr.mu.Unlock()

	pr.syncToManager()
}

// Deregister 主动注销一个 peer（优雅下线时调用）。
func (pr *PeerRegistry) Deregister(id string) {
	pr.mu.Lock()
	delete(pr.peers, id)
	pr.mu.Unlock()

	pr.logger.Info("peer deregistered", zap.String("id", id))
	pr.syncToManager()
}

// Peers 返回所有 peer 的快照（含不健康的）。
func (pr *PeerRegistry) Peers() []PeerInfo {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	result := make([]PeerInfo, 0, len(pr.peers))
	for _, p := range pr.peers {
		result = append(result, *p)
	}
	return result
}

// EvictStale 将超过 TTL 未心跳的 peer 标记为不健康，并从路由表中移除。
// 通常由后台 goroutine 定期调用。
func (pr *PeerRegistry) EvictStale() {
	now := time.Now()
	var evicted []string

	pr.mu.Lock()
	for id, p := range pr.peers {
		if now.Sub(p.LastSeen) > pr.ttl {
			p.IsHealthy = false
			evicted = append(evicted, id)
			delete(pr.peers, id)
		}
	}
	pr.mu.Unlock()

	for _, id := range evicted {
		pr.logger.Warn("peer evicted (heartbeat timeout)",
			zap.String("id", id),
			zap.Duration("ttl", pr.ttl),
		)
	}

	if len(evicted) > 0 {
		pr.syncToManager()
	}
}

// syncToManager 将当前 peer 列表同步到 ClusterManager（触发路由表版本递增）。
func (pr *PeerRegistry) syncToManager() {
	if pr.manager == nil {
		return
	}

	pr.mu.RLock()
	targets := make([]lb.Target, 0, len(pr.peers))
	for _, p := range pr.peers {
		targets = append(targets, lb.Target{
			ID:      p.ID,
			Addr:    p.Addr,
			Weight:  p.Weight,
			Healthy: p.IsHealthy,
		})
	}
	pr.mu.RUnlock()

	pr.manager.UpdateTargets(targets)
}
