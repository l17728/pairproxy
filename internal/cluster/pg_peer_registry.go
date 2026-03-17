package cluster

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/l17728/pairproxy/internal/db"
)

// PGPeerRegistry 通过 peers 表在共享数据库（PostgreSQL）中实现分布式节点发现。
// 每个 peer 节点在启动时 UPSERT 自己，定期更新 last_seen，
// 任意节点均可驱逐超时节点（stale eviction）。
type PGPeerRegistry struct {
	db           *gorm.DB
	logger       *zap.Logger
	selfAddr     string
	selfWeight   int
	interval     time.Duration
	staleTimeout time.Duration // 默认 3 × interval
	wg           sync.WaitGroup
}

// NewPGPeerRegistry 创建 PGPeerRegistry。
// selfAddr 为本节点对外地址（唯一标识），selfWeight 为负载权重。
// interval 为心跳间隔；staleTimeout 自动设为 3×interval。
func NewPGPeerRegistry(gormDB *gorm.DB, logger *zap.Logger, selfAddr string, selfWeight int, interval time.Duration) *PGPeerRegistry {
	if selfWeight <= 0 {
		selfWeight = 50
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &PGPeerRegistry{
		db:           gormDB,
		logger:       logger.Named("pg_peer_registry"),
		selfAddr:     selfAddr,
		selfWeight:   selfWeight,
		interval:     interval,
		staleTimeout: 3 * interval,
	}
}

// Heartbeat UPSERT 自身到 peers 表，更新 last_seen。
// 首次调用时插入记录，后续调用仅更新 weight/is_active/last_seen。
func (r *PGPeerRegistry) Heartbeat(ctx context.Context) error {
	now := time.Now()
	peer := db.Peer{
		ID:           r.selfAddr,
		Addr:         r.selfAddr,
		Weight:       r.selfWeight,
		IsActive:     true,
		RegisteredAt: now,
		LastSeen:     &now,
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "addr"}},
			DoUpdates: clause.AssignmentColumns([]string{"weight", "is_active", "last_seen"}),
		}).
		Create(&peer)
	if result.Error != nil {
		r.logger.Warn("peer heartbeat failed",
			zap.String("self_addr", r.selfAddr),
			zap.Error(result.Error),
		)
		return result.Error
	}
	r.logger.Debug("peer heartbeat sent",
		zap.String("self_addr", r.selfAddr),
		zap.Time("last_seen", now),
	)
	return nil
}

// EvictStale 将 last_seen < now-staleTimeout 的节点标记为 is_active=false。
// 自身节点不参与驱逐（避免与 Heartbeat 并发竞争）。
func (r *PGPeerRegistry) EvictStale(ctx context.Context) error {
	cutoff := time.Now().Add(-r.staleTimeout)
	result := r.db.WithContext(ctx).
		Model(&db.Peer{}).
		Where("last_seen < ? AND addr != ?", cutoff, r.selfAddr).
		Update("is_active", false)
	if result.Error != nil {
		r.logger.Warn("peer stale eviction failed", zap.Error(result.Error))
		return result.Error
	}
	if result.RowsAffected > 0 {
		r.logger.Info("peer stale eviction complete",
			zap.Int64("evicted", result.RowsAffected),
			zap.Duration("stale_timeout", r.staleTimeout),
		)
	}
	return nil
}

// ListHealthy 返回 is_active=true 且 last_seen > now-staleTimeout 的节点列表。
func (r *PGPeerRegistry) ListHealthy(ctx context.Context) ([]db.Peer, error) {
	cutoff := time.Now().Add(-r.staleTimeout)
	var peers []db.Peer
	result := r.db.WithContext(ctx).
		Where("is_active = ? AND last_seen > ?", true, cutoff).
		Find(&peers)
	if result.Error != nil {
		r.logger.Warn("list healthy peers failed", zap.Error(result.Error))
		return nil, result.Error
	}
	r.logger.Debug("listed healthy peers",
		zap.Int("count", len(peers)),
	)
	return peers, nil
}

// Unregister 在关闭时将自身标记为 is_active=false。
func (r *PGPeerRegistry) Unregister(ctx context.Context) error {
	result := r.db.WithContext(ctx).
		Model(&db.Peer{}).
		Where("addr = ?", r.selfAddr).
		Update("is_active", false)
	if result.Error != nil {
		r.logger.Warn("peer unregister failed",
			zap.String("self_addr", r.selfAddr),
			zap.Error(result.Error),
		)
		return result.Error
	}
	r.logger.Info("peer unregistered (graceful shutdown)",
		zap.String("self_addr", r.selfAddr),
	)
	return nil
}

// Start 启动后台 goroutine：定期 Heartbeat + EvictStale。
func (r *PGPeerRegistry) Start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.logger.Debug("pg peer registry stopped", zap.String("self_addr", r.selfAddr))
				return
			case <-ticker.C:
				if err := r.Heartbeat(ctx); err != nil {
					r.logger.Warn("periodic heartbeat failed", zap.Error(err))
				}
				if err := r.EvictStale(ctx); err != nil {
					r.logger.Warn("periodic stale eviction failed", zap.Error(err))
				}
			}
		}
	}()
}

// Wait 等待后台 goroutine 退出。
func (r *PGPeerRegistry) Wait() {
	r.wg.Wait()
}
