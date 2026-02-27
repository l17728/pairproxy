// Package quota 实现用户/分组配额检查与缓存。
package quota

import (
	"sync"
	"time"
)

// usageEntry 单个用户的缓存用量条目。
type usageEntry struct {
	dailyUsed   int64     // 今日已用 token（input+output）
	monthlyUsed int64     // 本月已用 token
	expiresAt   time.Time // 过期时间
}

// QuotaCache 缓存每个用户的近期用量，避免每次请求都查询 DB。
// 使用 sync.Map 保证并发安全；TTL 到期后下次访问时惰性刷新。
type QuotaCache struct {
	m   sync.Map
	ttl time.Duration
}

// NewQuotaCache 创建 QuotaCache。ttl 建议 60s。
func NewQuotaCache(ttl time.Duration) *QuotaCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &QuotaCache{ttl: ttl}
}

// get 取缓存条目，若不存在或已过期则返回 nil。
func (c *QuotaCache) get(userID string) *usageEntry {
	v, ok := c.m.Load(userID)
	if !ok {
		return nil
	}
	e := v.(*usageEntry)
	if time.Now().After(e.expiresAt) {
		c.m.Delete(userID)
		return nil
	}
	return e
}

// set 写入缓存条目（覆盖已有值）。
func (c *QuotaCache) set(userID string, daily, monthly int64) {
	c.m.Store(userID, &usageEntry{
		dailyUsed:   daily,
		monthlyUsed: monthly,
		expiresAt:   time.Now().Add(c.ttl),
	})
}

// invalidate 主动驱逐指定用户的缓存条目（用于测试或强制刷新）。
func (c *QuotaCache) invalidate(userID string) {
	c.m.Delete(userID)
}
