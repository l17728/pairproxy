package quota

import (
	"sync"
)

// ConcurrentCounter 每用户并发请求计数器（内存，非持久化）。
// 节点重启后计数归零，适合软限制场景（防误用而非严格计费）。
type ConcurrentCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewConcurrentCounter 创建 ConcurrentCounter。
func NewConcurrentCounter() *ConcurrentCounter {
	return &ConcurrentCounter{counts: make(map[string]int)}
}

// TryAcquire 尝试为 userID 获取一个并发请求槽。
// 若当前计数 >= limit 则返回 false（拒绝）；否则计数加一并返回 true。
// limit <= 0 时视为无限制，始终返回 true。
func (c *ConcurrentCounter) TryAcquire(userID string, limit int) bool {
	if limit <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[userID] >= limit {
		return false
	}
	c.counts[userID]++
	return true
}

// Release 释放 userID 的一个并发请求槽（必须与 TryAcquire(true) 配对调用）。
func (c *ConcurrentCounter) Release(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[userID] > 0 {
		c.counts[userID]--
	}
}

// Count 返回 userID 当前持有的并发请求数（供测试/监控使用）。
func (c *ConcurrentCounter) Count(userID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[userID]
}
