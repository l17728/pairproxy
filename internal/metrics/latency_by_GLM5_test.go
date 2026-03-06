package metrics

import (
	"testing"
)

// TestLatencyHistogram_Basic 测试基本的延迟直方图功能
func TestLatencyHistogram_Basic(t *testing.T) {
	h := NewLatencyHistogram()

	// 初始状态
	snap := h.Snapshot()
	if snap.Count != 0 {
		t.Errorf("initial count = %d, want 0", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("initial sum = %d, want 0", snap.Sum)
	}
	if snap.AvgMs != 0 {
		t.Errorf("initial avg = %f, want 0", snap.AvgMs)
	}
}

// TestLatencyHistogram_Observe 测试观察延迟值
func TestLatencyHistogram_Observe(t *testing.T) {
	h := NewLatencyHistogram()

	// 记录一些延迟值
	h.Observe(50)    // 0-100ms bucket
	h.Observe(200)   // 100-500ms bucket
	h.Observe(750)   // 500ms-1s bucket
	h.Observe(2000)  // 1s-5s bucket
	h.Observe(10000) // 5s-30s bucket
	h.Observe(60000) // 30s+ bucket

	snap := h.Snapshot()
	if snap.Count != 6 {
		t.Errorf("count = %d, want 6", snap.Count)
	}
	if snap.Sum != 50+200+750+2000+10000+60000 {
		t.Errorf("sum = %d, want %d", snap.Sum, 50+200+750+2000+10000+60000)
	}

	// 验证平均值
	expectedAvg := float64(50+200+750+2000+10000+60000) / 6
	if snap.AvgMs != expectedAvg {
		t.Errorf("avg = %f, want %f", snap.AvgMs, expectedAvg)
	}

	// 验证各桶计数
	if len(snap.Buckets) != 6 {
		t.Errorf("expected 6 buckets, got %d", len(snap.Buckets))
	}
	// 每个桶应该有 1 个值
	for i, count := range snap.Buckets {
		if count != 1 {
			t.Errorf("bucket[%d] = %d, want 1", i, count)
		}
	}
}

// TestLatencyHistogram_BucketBoundaries 测试桶边界
func TestLatencyHistogram_BucketBoundaries(t *testing.T) {
	h := NewLatencyHistogram()

	// 测试边界值
	testCases := []struct {
		latency     int64
		bucketIndex int
		description string
	}{
		{0, 0, "0ms should go to bucket 0"},
		{100, 0, "100ms (boundary) should go to bucket 0"},
		{101, 1, "101ms should go to bucket 1"},
		{500, 1, "500ms (boundary) should go to bucket 1"},
		{501, 2, "501ms should go to bucket 2"},
		{1000, 2, "1000ms (boundary) should go to bucket 2"},
		{1001, 3, "1001ms should go to bucket 3"},
		{5000, 3, "5000ms (boundary) should go to bucket 3"},
		{5001, 4, "5001ms should go to bucket 4"},
		{30000, 4, "30000ms (boundary) should go to bucket 4"},
		{30001, 5, "30001ms should go to bucket 5 (overflow)"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			h.Reset()
			h.Observe(tc.latency)
			snap := h.Snapshot()
			for i, count := range snap.Buckets {
				if i == tc.bucketIndex && count != 1 {
					t.Errorf("bucket[%d] = %d, want 1", i, count)
				} else if i != tc.bucketIndex && count != 0 {
					t.Errorf("bucket[%d] = %d, want 0", i, count)
				}
			}
		})
	}
}

// TestLatencyHistogram_Reset 测试重置功能
func TestLatencyHistogram_Reset(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	h.Observe(200)
	h.Observe(300)

	snap := h.Snapshot()
	if snap.Count != 3 {
		t.Errorf("before reset: count = %d, want 3", snap.Count)
	}

	h.Reset()

	snap = h.Snapshot()
	if snap.Count != 0 {
		t.Errorf("after reset: count = %d, want 0", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("after reset: sum = %d, want 0", snap.Sum)
	}
	for i, count := range snap.Buckets {
		if count != 0 {
			t.Errorf("after reset: bucket[%d] = %d, want 0", i, count)
		}
	}
}

// TestLatencyHistogram_Concurrent 测试并发写入
func TestLatencyHistogram_Concurrent(t *testing.T) {
	h := NewLatencyHistogram()

	const goroutines = 10
	const observationsPerGoroutine = 100

	done := make(chan bool)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < observationsPerGoroutine; j++ {
				h.Observe(int64(j))
			}
			done <- true
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	snap := h.Snapshot()
	expectedCount := int64(goroutines * observationsPerGoroutine)
	if snap.Count != expectedCount {
		t.Errorf("count = %d, want %d", snap.Count, expectedCount)
	}
}

// TestLatencyTracker_Basic 测试 LatencyTracker 基本功能
func TestLatencyTracker_Basic(t *testing.T) {
	tracker := NewLatencyTracker()

	// 初始状态
	proxySnap := tracker.ProxyLatency().Snapshot()
	llmSnap := tracker.LLMLatency().Snapshot()

	if proxySnap.Count != 0 || llmSnap.Count != 0 {
		t.Error("initial histograms should be empty")
	}
}

// TestLatencyTracker_ObserveLatencies 测试记录延迟
func TestLatencyTracker_ObserveLatencies(t *testing.T) {
	tracker := NewLatencyTracker()

	tracker.ObserveProxyLatency(100)
	tracker.ObserveProxyLatency(200)
	tracker.ObserveLLMLatency(150)
	tracker.ObserveLLMLatency(250)

	proxySnap := tracker.ProxyLatency().Snapshot()
	llmSnap := tracker.LLMLatency().Snapshot()

	if proxySnap.Count != 2 {
		t.Errorf("proxy count = %d, want 2", proxySnap.Count)
	}
	if proxySnap.Sum != 300 {
		t.Errorf("proxy sum = %d, want 300", proxySnap.Sum)
	}

	if llmSnap.Count != 2 {
		t.Errorf("llm count = %d, want 2", llmSnap.Count)
	}
	if llmSnap.Sum != 400 {
		t.Errorf("llm sum = %d, want 400", llmSnap.Sum)
	}
}

// TestGlobalLatencyTracker 测试全局延迟追踪器
func TestGlobalLatencyTracker(t *testing.T) {
	// 设置全局追踪器
	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)

	// 获取并验证
	retrieved := GetGlobalLatencyTracker()
	if retrieved != tracker {
		t.Error("retrieved tracker should be the same instance")
	}

	// 记录一些值
	retrieved.ObserveProxyLatency(500)
	retrieved.ObserveLLMLatency(600)

	// 通过全局获取器再次获取并验证
	retrieved2 := GetGlobalLatencyTracker()
	proxySnap := retrieved2.ProxyLatency().Snapshot()
	llmSnap := retrieved2.LLMLatency().Snapshot()

	if proxySnap.Count != 1 {
		t.Errorf("proxy count = %d, want 1", proxySnap.Count)
	}
	if llmSnap.Count != 1 {
		t.Errorf("llm count = %d, want 1", llmSnap.Count)
	}

	// 清理
	SetGlobalLatencyTracker(nil)
}

// TestGlobalLatencyTracker_Nil 测试未设置全局追踪器时返回 nil
func TestGlobalLatencyTracker_Nil(t *testing.T) {
	// 先设置为 nil
	SetGlobalLatencyTracker(nil)

	// 应该返回 nil
	retrieved := GetGlobalLatencyTracker()
	if retrieved != nil {
		t.Error("expected nil tracker")
	}
}

// TestLatencyHistogram_AverageCalculation 测试平均值计算
func TestLatencyHistogram_AverageCalculation(t *testing.T) {
	h := NewLatencyHistogram()

	// 记录一系列值
	values := []int64{100, 200, 300, 400, 500}
	var sum int64
	for _, v := range values {
		h.Observe(v)
		sum += v
	}

	snap := h.Snapshot()
	expectedAvg := float64(sum) / float64(len(values))

	if snap.AvgMs != expectedAvg {
		t.Errorf("avg = %f, want %f", snap.AvgMs, expectedAvg)
	}
}

// TestLatencyHistogram_SingleValue 测试单个值的统计
func TestLatencyHistogram_SingleValue(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(1234)

	snap := h.Snapshot()
	if snap.Count != 1 {
		t.Errorf("count = %d, want 1", snap.Count)
	}
	if snap.Sum != 1234 {
		t.Errorf("sum = %d, want 1234", snap.Sum)
	}
	if snap.AvgMs != 1234.0 {
		t.Errorf("avg = %f, want 1234.0", snap.AvgMs)
	}
}

// TestLatencyHistogram_LargeValues 测试大延迟值
func TestLatencyHistogram_LargeValues(t *testing.T) {
	h := NewLatencyHistogram()

	// 测试非常大的延迟值
	h.Observe(300000) // 5 分钟，应该在 overflow bucket
	h.Observe(600000) // 10 分钟

	snap := h.Snapshot()
	if snap.Count != 2 {
		t.Errorf("count = %d, want 2", snap.Count)
	}

	// 最后一个桶（overflow）应该有 2 个值
	if snap.Buckets[len(snap.Buckets)-1] != 2 {
		t.Errorf("overflow bucket should have 2 values, got %d", snap.Buckets[len(snap.Buckets)-1])
	}
}

// TestLatencyBucketBounds_Values 验证桶边界值
func TestLatencyBucketBounds_Values(t *testing.T) {
	expected := []int64{100, 500, 1000, 5000, 30000}

	if len(LatencyBucketBounds) != len(expected) {
		t.Errorf("LatencyBucketBounds length = %d, want %d", len(LatencyBucketBounds), len(expected))
	}

	for i, v := range expected {
		if LatencyBucketBounds[i] != v {
			t.Errorf("LatencyBucketBounds[%d] = %d, want %d", i, LatencyBucketBounds[i], v)
		}
	}
}

// TestLatencyHistogram_ZeroValue 测试零延迟值
func TestLatencyHistogram_ZeroValue(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(0)

	snap := h.Snapshot()
	if snap.Count != 1 {
		t.Errorf("count = %d, want 1", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("sum = %d, want 0", snap.Sum)
	}
	if snap.AvgMs != 0 {
		t.Errorf("avg = %f, want 0", snap.AvgMs)
	}
	// 第一个桶应该有值
	if snap.Buckets[0] != 1 {
		t.Errorf("bucket[0] = %d, want 1", snap.Buckets[0])
	}
}

// TestLatencyHistogram_ManyObservations 测试大量观察值
func TestLatencyHistogram_ManyObservations(t *testing.T) {
	h := NewLatencyHistogram()

	const count = 10000
	for i := int64(0); i < count; i++ {
		h.Observe(i % 30001) // 周期性覆盖所有桶
	}

	snap := h.Snapshot()
	if snap.Count != count {
		t.Errorf("count = %d, want %d", snap.Count, count)
	}

	// 计算桶内总和
	var bucketSum int64
	for _, c := range snap.Buckets {
		bucketSum += c
	}
	if bucketSum != count {
		t.Errorf("sum of buckets = %d, want %d", bucketSum, count)
	}
}

// TestLatencyHistogram_SnapshotImmutable 测试快照是不可变的
func TestLatencyHistogram_SnapshotImmutable(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	snap1 := h.Snapshot()

	// 修改原始直方图
	h.Observe(200)

	// 快照应该保持不变
	if snap1.Count != 1 {
		t.Errorf("snap1 count changed to %d, want 1", snap1.Count)
	}

	// 新快照应该有新值
	snap2 := h.Snapshot()
	if snap2.Count != 2 {
		t.Errorf("snap2 count = %d, want 2", snap2.Count)
	}
}

// TestLatencyHistogram_BucketsCopy 测试快照的桶是副本
func TestLatencyHistogram_BucketsCopy(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	h.Observe(500)

	snap := h.Snapshot()
	originalBuckets := make([]int64, len(snap.Buckets))
	copy(originalBuckets, snap.Buckets)

	// 修改快照的桶不应该影响原始数据
	snap.Buckets[0] = 9999

	newSnap := h.Snapshot()
	if newSnap.Buckets[0] == 9999 {
		t.Error("modifying snapshot buckets affected original histogram")
	}
}

// TestLatencyHistogram_EdgeCases 测试边界情况
func TestLatencyHistogram_EdgeCases(t *testing.T) {
	h := NewLatencyHistogram()

	// 连续相同的值
	for i := 0; i < 10; i++ {
		h.Observe(100)
	}

	snap := h.Snapshot()
	if snap.Count != 10 {
		t.Errorf("count = %d, want 10", snap.Count)
	}
	if snap.Sum != 1000 {
		t.Errorf("sum = %d, want 1000", snap.Sum)
	}
	if snap.AvgMs != 100.0 {
		t.Errorf("avg = %f, want 100.0", snap.AvgMs)
	}
	// 第一个桶应该有 10 个值
	if snap.Buckets[0] != 10 {
		t.Errorf("bucket[0] = %d, want 10", snap.Buckets[0])
	}
}

// TestLatencyHistogram_NegativeValueBehavior 测试负值行为
// 注意：延迟不应该为负，但测试系统对异常输入的处理
func TestLatencyHistogram_NegativeValueBehavior(t *testing.T) {
	h := NewLatencyHistogram()

	// 负值会进入第一个桶（因为小于所有边界）
	h.Observe(-100)

	snap := h.Snapshot()
	if snap.Count != 1 {
		t.Errorf("count = %d, want 1", snap.Count)
	}
	// 负值会在第一个桶
	if snap.Buckets[0] != 1 {
		t.Errorf("bucket[0] = %d, want 1", snap.Buckets[0])
	}
}
