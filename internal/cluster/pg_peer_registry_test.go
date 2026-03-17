package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// TestPGPeerRegistry_Heartbeat 验证心跳写入 peers 表，再次调用更新 last_seen
func TestPGPeerRegistry_Heartbeat(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	reg := NewPGPeerRegistry(gormDB, logger, "node-1:9000", 50, 30*time.Second)
	ctx := context.Background()

	// 第一次心跳：插入记录
	require.NoError(t, reg.Heartbeat(ctx))

	var peer db.Peer
	require.NoError(t, gormDB.Where("addr = ?", "node-1:9000").First(&peer).Error)
	assert.Equal(t, "node-1:9000", peer.Addr)
	assert.Equal(t, 50, peer.Weight)
	assert.True(t, peer.IsActive)
	require.NotNil(t, peer.LastSeen)

	firstSeen := *peer.LastSeen

	// 等一小段时间再发心跳
	time.Sleep(5 * time.Millisecond)

	// 第二次心跳：更新 last_seen
	require.NoError(t, reg.Heartbeat(ctx))

	require.NoError(t, gormDB.Where("addr = ?", "node-1:9000").First(&peer).Error)
	require.NotNil(t, peer.LastSeen)
	assert.True(t, peer.LastSeen.After(firstSeen) || peer.LastSeen.Equal(firstSeen))
}

// TestPGPeerRegistry_ListHealthy_FiltersStale 验证 last_seen 过旧的节点不在 ListHealthy 结果中
func TestPGPeerRegistry_ListHealthy_FiltersStale(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx := context.Background()

	// 写入一个"旧"节点（last_seen = 1小时前）
	oldTime := time.Now().Add(-time.Hour)
	stale := db.Peer{
		ID:           "stale-node:9000",
		Addr:         "stale-node:9000",
		Weight:       50,
		IsActive:     true,
		RegisteredAt: oldTime,
		LastSeen:     &oldTime,
	}
	require.NoError(t, gormDB.Create(&stale).Error)

	// 当前节点发心跳
	reg := NewPGPeerRegistry(gormDB, logger, "fresh-node:9000", 50, 30*time.Second)
	require.NoError(t, reg.Heartbeat(ctx))

	peers, err := reg.ListHealthy(ctx)
	require.NoError(t, err)

	// 只应返回 fresh-node（stale-node last_seen 超过 staleTimeout=90s）
	assert.Len(t, peers, 1)
	assert.Equal(t, "fresh-node:9000", peers[0].Addr)
}

// TestPGPeerRegistry_EvictStale 验证驱逐后旧节点 is_active=false
func TestPGPeerRegistry_EvictStale(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx := context.Background()

	// 写入一个旧节点
	oldTime := time.Now().Add(-2 * time.Hour)
	stale := db.Peer{
		ID:           "stale:9000",
		Addr:         "stale:9000",
		Weight:       50,
		IsActive:     true,
		RegisteredAt: oldTime,
		LastSeen:     &oldTime,
	}
	require.NoError(t, gormDB.Create(&stale).Error)

	reg := NewPGPeerRegistry(gormDB, logger, "self:9000", 50, 30*time.Second)
	require.NoError(t, reg.EvictStale(ctx))

	var evicted db.Peer
	require.NoError(t, gormDB.Where("addr = ?", "stale:9000").First(&evicted).Error)
	assert.False(t, evicted.IsActive, "stale node should be marked inactive after eviction")
}

// TestPGPeerRegistry_Unregister 验证关闭时自身 is_active=false
func TestPGPeerRegistry_Unregister(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx := context.Background()
	reg := NewPGPeerRegistry(gormDB, logger, "self:9000", 50, 30*time.Second)

	// 先心跳（确保记录存在）
	require.NoError(t, reg.Heartbeat(ctx))

	// 注销
	require.NoError(t, reg.Unregister(ctx))

	var peer db.Peer
	require.NoError(t, gormDB.Where("addr = ?", "self:9000").First(&peer).Error)
	assert.False(t, peer.IsActive, "peer should be inactive after unregister")
}

// TestPGPeerRegistry_DefaultValues 验证零值参数时使用默认值
func TestPGPeerRegistry_DefaultValues(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)

	// selfWeight=0 和 interval=0 都应使用默认值
	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 0, 0)
	assert.Equal(t, 50, reg.selfWeight, "default selfWeight should be 50")
	assert.Equal(t, 30*time.Second, reg.interval, "default interval should be 30s")
	assert.Equal(t, 90*time.Second, reg.staleTimeout, "staleTimeout should be 3×interval")
}

// TestPGPeerRegistry_Start_AndWait 验证 Start 后台 goroutine 可正常停止
func TestPGPeerRegistry_Start_AndWait(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx, cancel := context.WithCancel(context.Background())

	// 使用极短 interval，确保至少触发一次心跳
	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 50, 50*time.Millisecond)
	reg.Start(ctx)

	// 等待足够时间让 goroutine 跑一次
	time.Sleep(120 * time.Millisecond)

	// 取消 ctx → goroutine 应退出
	cancel()
	done := make(chan struct{})
	go func() {
		reg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// goroutine 正常退出
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() timed out — goroutine did not exit after ctx cancel")
	}

	// 验证心跳确实写入了 DB
	var peer db.Peer
	require.NoError(t, gormDB.Where("addr = ?", "node:9000").First(&peer).Error)
	assert.True(t, peer.IsActive)
}

// TestPGPeerRegistry_Heartbeat_DBError 验证 DB 不可用时 Heartbeat 返回错误
func TestPGPeerRegistry_Heartbeat_DBError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 50, 30*time.Second)

	// 删除 peers 表，强制 DB 错误
	require.NoError(t, gormDB.Migrator().DropTable(&db.Peer{}))

	err = reg.Heartbeat(context.Background())
	assert.Error(t, err, "Heartbeat should return error when peers table is missing")
}

// TestPGPeerRegistry_EvictStale_DBError 验证 DB 不可用时 EvictStale 返回错误
func TestPGPeerRegistry_EvictStale_DBError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 50, 30*time.Second)
	require.NoError(t, gormDB.Migrator().DropTable(&db.Peer{}))

	err = reg.EvictStale(context.Background())
	assert.Error(t, err, "EvictStale should return error when peers table is missing")
}

// TestPGPeerRegistry_ListHealthy_DBError 验证 DB 不可用时 ListHealthy 返回错误
func TestPGPeerRegistry_ListHealthy_DBError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 50, 30*time.Second)
	require.NoError(t, gormDB.Migrator().DropTable(&db.Peer{}))

	peers, err := reg.ListHealthy(context.Background())
	assert.Error(t, err, "ListHealthy should return error when peers table is missing")
	assert.Nil(t, peers)
}

// TestPGPeerRegistry_Unregister_DBError 验证 DB 不可用时 Unregister 返回错误
func TestPGPeerRegistry_Unregister_DBError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	reg := NewPGPeerRegistry(gormDB, logger, "node:9000", 50, 30*time.Second)
	require.NoError(t, gormDB.Migrator().DropTable(&db.Peer{}))

	err = reg.Unregister(context.Background())
	assert.Error(t, err, "Unregister should return error when peers table is missing")
}

// TestPGPeerRegistry_EvictStale_SkipsSelf 验证 EvictStale 不驱逐自身
func TestPGPeerRegistry_EvictStale_SkipsSelf(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx := context.Background()

	reg := NewPGPeerRegistry(gormDB, logger, "self:9000", 50, 30*time.Second)

	// 自身心跳写入正常记录，但手动将 last_seen 设为旧时间（模拟自身 last_seen 过期）
	require.NoError(t, reg.Heartbeat(ctx))
	oldTime := time.Now().Add(-10 * time.Hour)
	require.NoError(t, gormDB.Model(&db.Peer{}).Where("addr = ?", "self:9000").Update("last_seen", oldTime).Error)

	// EvictStale 不应驱逐自身（即使 last_seen 过期）
	require.NoError(t, reg.EvictStale(ctx))

	var self db.Peer
	require.NoError(t, gormDB.Where("addr = ?", "self:9000").First(&self).Error)
	assert.True(t, self.IsActive, "EvictStale should NOT mark self as inactive")
}

// TestPGPeerRegistry_MultiNodeDiscovery 验证两个 PGPeerRegistry 实例互相发现对方
func TestPGPeerRegistry_MultiNodeDiscovery(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gormDB))

	ctx := context.Background()

	// 节点A 和 节点B 使用同一 DB（模拟共享 PG）
	regA := NewPGPeerRegistry(gormDB, logger, "node-a:9000", 50, 30*time.Second)
	regB := NewPGPeerRegistry(gormDB, logger, "node-b:9000", 60, 30*time.Second)

	// 两个节点都发心跳
	require.NoError(t, regA.Heartbeat(ctx))
	require.NoError(t, regB.Heartbeat(ctx))

	// 从 A 的视角查看健康节点
	peersFromA, err := regA.ListHealthy(ctx)
	require.NoError(t, err)
	require.Len(t, peersFromA, 2, "node-a should discover both nodes")

	addrs := make(map[string]bool)
	for _, p := range peersFromA {
		addrs[p.Addr] = true
	}
	assert.True(t, addrs["node-a:9000"], "node-a should see itself")
	assert.True(t, addrs["node-b:9000"], "node-a should discover node-b")

	// B 注销后，A 再次查询应只看到自己
	require.NoError(t, regB.Unregister(ctx))
	peersAfterUnregister, err := regA.ListHealthy(ctx)
	require.NoError(t, err)
	assert.Len(t, peersAfterUnregister, 1, "after node-b unregister, only node-a should be healthy")
	assert.Equal(t, "node-a:9000", peersAfterUnregister[0].Addr)
}
