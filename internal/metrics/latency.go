package metrics

import (
	"sync"
	"sync/atomic"
)

// LatencyHistogram 在内存中维护请求延迟的直方图统计。
// 用于 Prometheus /metrics 端点暴露延迟分布（P50/P90/P99）。
type LatencyHistogram struct {
	mu sync.RWMutex

	// Buckets: 0-100ms, 100-500ms, 500ms-1s, 1s-5s, 5s-30s, 30s+
	buckets []int64

	// 统计值
	count int64
	sum   int64 // milliseconds
}

// LatencyBucketBounds 定义延迟桶边界（毫秒）
var LatencyBucketBounds = []int64{100, 500, 1000, 5000, 30000}

// NewLatencyHistogram 创建 LatencyHistogram
func NewLatencyHistogram() *LatencyHistogram {
	return &LatencyHistogram{
		buckets: make([]int64, len(LatencyBucketBounds)+1), // +1 for "overflow" bucket
	}
}

// Observe 记录一次延迟观察值（毫秒）
func (h *LatencyHistogram) Observe(latencyMs int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.count++
	h.sum += latencyMs

	// 找到对应的桶
	bucket := 0
	for i, bound := range LatencyBucketBounds {
		if latencyMs <= bound {
			bucket = i
			break
		}
		bucket = i + 1
	}
	h.buckets[bucket]++
}

// Snapshot 返回当前直方图的快照
type LatencySnapshot struct {
	Count   int64   // 总请求数
	Sum     int64   // 总延迟（毫秒）
	AvgMs   float64 // 平均延迟
	Buckets []int64 // 各桶计数
}

// Snapshot 获取当前快照
func (h *LatencyHistogram) Snapshot() LatencySnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	buckets := make([]int64, len(h.buckets))
	copy(buckets, h.buckets)

	var avg float64
	if h.count > 0 {
		avg = float64(h.sum) / float64(h.count)
	}

	return LatencySnapshot{
		Count:   h.count,
		Sum:     h.sum,
		AvgMs:   avg,
		Buckets: buckets,
	}
}

// Reset 重置统计（用于测试）
func (h *LatencyHistogram) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count = 0
	h.sum = 0
	for i := range h.buckets {
		h.buckets[i] = 0
	}
}

// LatencyTracker 全局延迟追踪器，供 sproxy 记录延迟
type LatencyTracker struct {
	proxyLatency *LatencyHistogram // 代理请求延迟
	llmLatency   *LatencyHistogram // LLM 上游延迟
}

// NewLatencyTracker 创建延迟追踪器
func NewLatencyTracker() *LatencyTracker {
	return &LatencyTracker{
		proxyLatency: NewLatencyHistogram(),
		llmLatency:   NewLatencyHistogram(),
	}
}

// ObserveProxyLatency 记录代理请求延迟（毫秒）
func (t *LatencyTracker) ObserveProxyLatency(latencyMs int64) {
	t.proxyLatency.Observe(latencyMs)
}

// ObserveLLMLatency 记录 LLM 上游延迟（毫秒）
func (t *LatencyTracker) ObserveLLMLatency(latencyMs int64) {
	t.llmLatency.Observe(latencyMs)
}

// ProxyLatency 返回代理延迟直方图
func (t *LatencyTracker) ProxyLatency() *LatencyHistogram {
	return t.proxyLatency
}

// LLMLatency 返回 LLM 延迟直方图
func (t *LatencyTracker) LLMLatency() *LatencyHistogram {
	return t.llmLatency
}

// GlobalLatencyTracker 全局实例（使用 atomic.Value 支持动态设置）
var globalLatencyTracker atomic.Value

// SetGlobalLatencyTracker 设置全局延迟追踪器
func SetGlobalLatencyTracker(t *LatencyTracker) {
	globalLatencyTracker.Store(t)
}

// GetGlobalLatencyTracker 获取全局延迟追踪器
func GetGlobalLatencyTracker() *LatencyTracker {
	if v := globalLatencyTracker.Load(); v != nil {
		return v.(*LatencyTracker)
	}
	return nil
}