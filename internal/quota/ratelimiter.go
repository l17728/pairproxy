package quota

import (
	"sync"
	"time"
)

// RateLimiter 基于滑动窗口的每分钟请求限速器。
// 线程安全，使用内存存储（进程重启后计数归零）。
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time // userID → 窗口内的请求时间戳
	window  time.Duration          // 滑动窗口长度（固定 1 分钟）
}

// NewRateLimiter 创建 RateLimiter
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		windows: make(map[string][]time.Time),
		window:  time.Minute,
	}
}

// Allow 检查 userID 是否可发起新请求（窗口内不超过 limit 次）。
// 若允许，记录本次请求并返回 (true, 当前计数)。
// 若超限，不记录并返回 (false, 当前计数)。
func (r *RateLimiter) Allow(userID string, limit int) (allowed bool, current int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// 过滤窗口外的旧时间戳（原地操作复用底层数组）
	times := r.windows[userID]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	current = len(valid)
	if current >= limit {
		r.windows[userID] = valid
		return false, current
	}

	r.windows[userID] = append(valid, now)
	return true, current + 1
}

// ResetAt 返回窗口内最早一条请求过期的时间（即最早可重试的时刻）。
// 若用户无历史记录，返回 now。
func (r *RateLimiter) ResetAt(userID string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()

	times := r.windows[userID]
	if len(times) == 0 {
		return time.Now()
	}
	// 最早的时间戳 + 1 分钟 = 窗口内最老记录何时过期
	return times[0].Add(r.window)
}

// Purge 清理长时间无请求的用户条目（减少内存占用）。
// 建议每隔几分钟调用一次。
func (r *RateLimiter) Purge() {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.window)
	for uid, times := range r.windows {
		var valid []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(r.windows, uid)
		} else {
			r.windows[uid] = valid
		}
	}
}
