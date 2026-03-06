package metrics

import (
	"sync"
	"testing"
)

// TestLatencyHistogram_New tests creating a new histogram
func TestLatencyHistogram_New_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()
	if h == nil {
		t.Fatal("expected histogram, got nil")
	}
	if len(h.buckets) != len(LatencyBucketBounds)+1 {
		t.Errorf("expected %d buckets, got %d", len(LatencyBucketBounds)+1, len(h.buckets))
	}
}

// TestLatencyHistogram_Observe tests observing latency values
func TestLatencyHistogram_Observe_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	testCases := []struct {
		latency   int64
		bucketIdx int
	}{
		{50, 0},    // 0-100ms bucket
		{100, 0},   // 0-100ms bucket (boundary)
		{200, 1},   // 100-500ms bucket
		{500, 1},   // 100-500ms bucket (boundary)
		{750, 2},   // 500-1000ms bucket
		{1000, 2},  // 500-1000ms bucket (boundary)
		{2500, 3},  // 1000-5000ms bucket
		{5000, 3},  // 1000-5000ms bucket (boundary)
		{15000, 4}, // 5000-30000ms bucket
		{30000, 4}, // 5000-30000ms bucket (boundary)
		{60000, 5}, // 30000+ms bucket (overflow)
	}

	for _, tc := range testCases {
		h.Observe(tc.latency)
	}

	snap := h.Snapshot()
	if snap.Count != int64(len(testCases)) {
		t.Errorf("expected count %d, got %d", len(testCases), snap.Count)
	}

	// Verify sum
	var expectedSum int64 = 50 + 100 + 200 + 500 + 750 + 1000 + 2500 + 5000 + 15000 + 30000 + 60000
	if snap.Sum != expectedSum {
		t.Errorf("expected sum %d, got %d", expectedSum, snap.Sum)
	}
}

// TestLatencyHistogram_Snapshot tests snapshot functionality
func TestLatencyHistogram_Snapshot_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	h.Observe(200)
	h.Observe(300)

	snap := h.Snapshot()

	if snap.Count != 3 {
		t.Errorf("expected count 3, got %d", snap.Count)
	}

	if snap.AvgMs <= 0 {
		t.Error("expected positive average")
	}

	if len(snap.Buckets) != len(h.buckets) {
		t.Errorf("expected %d buckets in snapshot, got %d", len(h.buckets), len(snap.Buckets))
	}
}

// TestLatencyHistogram_Reset tests reset functionality
func TestLatencyHistogram_Reset_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	h.Observe(200)

	h.Reset()

	snap := h.Snapshot()
	if snap.Count != 0 {
		t.Errorf("expected count 0 after reset, got %d", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("expected sum 0 after reset, got %d", snap.Sum)
	}
	for i, b := range snap.Buckets {
		if b != 0 {
			t.Errorf("expected bucket %d to be 0 after reset, got %d", i, b)
		}
	}
}

// TestLatencyHistogram_Concurrent tests concurrent access
func TestLatencyHistogram_Concurrent_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Observe(int64(j * 10))
			}
		}()
	}

	wg.Wait()

	snap := h.Snapshot()
	if snap.Count != 10000 {
		t.Errorf("expected count 10000, got %d", snap.Count)
	}
}

// TestLatencyHistogram_EmptySnapshot tests snapshot on empty histogram
func TestLatencyHistogram_EmptySnapshot_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	snap := h.Snapshot()

	if snap.Count != 0 {
		t.Errorf("expected count 0, got %d", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("expected sum 0, got %d", snap.Sum)
	}
	if snap.AvgMs != 0 {
		t.Errorf("expected avg 0, got %f", snap.AvgMs)
	}
}

// TestLatencyTracker_New tests creating a new latency tracker
func TestLatencyTracker_New_by_kimi2_5(t *testing.T) {
	tracker := NewLatencyTracker()
	if tracker == nil {
		t.Fatal("expected tracker, got nil")
	}
	if tracker.proxyLatency == nil {
		t.Error("expected proxyLatency histogram to be set")
	}
	if tracker.llmLatency == nil {
		t.Error("expected llmLatency histogram to be set")
	}
}

// TestLatencyTracker_ObserveProxyLatency tests observing proxy latency
func TestLatencyTracker_ObserveProxyLatency_by_kimi2_5(t *testing.T) {
	tracker := NewLatencyTracker()

	tracker.ObserveProxyLatency(100)
	tracker.ObserveProxyLatency(200)

	snap := tracker.ProxyLatency().Snapshot()
	if snap.Count != 2 {
		t.Errorf("expected count 2, got %d", snap.Count)
	}
}

// TestLatencyTracker_ObserveLLMLatency tests observing LLM latency
func TestLatencyTracker_ObserveLLMLatency_by_kimi2_5(t *testing.T) {
	tracker := NewLatencyTracker()

	tracker.ObserveLLMLatency(500)
	tracker.ObserveLLMLatency(1000)

	snap := tracker.LLMLatency().Snapshot()
	if snap.Count != 2 {
		t.Errorf("expected count 2, got %d", snap.Count)
	}
}

// TestLatencyTracker_Isolation tests proxy and LLM latencies are isolated
func TestLatencyTracker_Isolation_by_kimi2_5(t *testing.T) {
	tracker := NewLatencyTracker()

	tracker.ObserveProxyLatency(100)
	tracker.ObserveLLMLatency(500)

	proxySnap := tracker.ProxyLatency().Snapshot()
	llmSnap := tracker.LLMLatency().Snapshot()

	if proxySnap.Count != 1 {
		t.Errorf("expected proxy count 1, got %d", proxySnap.Count)
	}
	if llmSnap.Count != 1 {
		t.Errorf("expected LLM count 1, got %d", llmSnap.Count)
	}
	if proxySnap.Sum != 100 {
		t.Errorf("expected proxy sum 100, got %d", proxySnap.Sum)
	}
	if llmSnap.Sum != 500 {
		t.Errorf("expected LLM sum 500, got %d", llmSnap.Sum)
	}
}

// TestGlobalLatencyTracker tests global latency tracker functions
func TestGlobalLatencyTracker_by_kimi2_5(t *testing.T) {
	// Initially should be nil
	if GetGlobalLatencyTracker() != nil {
		t.Error("expected nil global tracker initially")
	}

	// Set a tracker
	tracker := NewLatencyTracker()
	SetGlobalLatencyTracker(tracker)

	// Retrieve should return the same tracker
	retrieved := GetGlobalLatencyTracker()
	if retrieved == nil {
		t.Fatal("expected non-nil global tracker after setting")
	}

	// Observe through global tracker
	retrieved.ObserveProxyLatency(100)
	snap := retrieved.ProxyLatency().Snapshot()
	if snap.Count != 1 {
		t.Errorf("expected count 1, got %d", snap.Count)
	}

	// Reset to nil for other tests
	SetGlobalLatencyTracker(nil)
}

// TestLatencyHistogram_SnapshotIsolation tests that snapshot is independent
func TestLatencyHistogram_SnapshotIsolation_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(100)
	snap1 := h.Snapshot()

	h.Observe(200)
	snap2 := h.Snapshot()

	// snap1 should be unchanged
	if snap1.Count != 1 {
		t.Errorf("expected snap1 count 1, got %d", snap1.Count)
	}
	if snap1.Sum != 100 {
		t.Errorf("expected snap1 sum 100, got %d", snap1.Sum)
	}

	// snap2 should have both observations
	if snap2.Count != 2 {
		t.Errorf("expected snap2 count 2, got %d", snap2.Count)
	}
	if snap2.Sum != 300 {
		t.Errorf("expected snap2 sum 300, got %d", snap2.Sum)
	}
}

// TestLatencyBucketBounds tests the bucket bounds are defined correctly
func TestLatencyBucketBounds_by_kimi2_5(t *testing.T) {
	expected := []int64{100, 500, 1000, 5000, 30000}
	if len(LatencyBucketBounds) != len(expected) {
		t.Fatalf("expected %d bucket bounds, got %d", len(expected), len(LatencyBucketBounds))
	}
	for i, bound := range LatencyBucketBounds {
		if bound != expected[i] {
			t.Errorf("expected bound %d at index %d, got %d", expected[i], i, bound)
		}
	}
}

// TestLatencyHistogram_MultipleResets tests multiple resets
func TestLatencyHistogram_MultipleResets_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	for i := 0; i < 3; i++ {
		h.Observe(100)
		snap := h.Snapshot()
		if snap.Count != 1 {
			t.Errorf("iteration %d: expected count 1, got %d", i, snap.Count)
		}
		h.Reset()
	}
}

// TestLatencyHistogram_ZeroLatency tests observing zero latency
func TestLatencyHistogram_ZeroLatency_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	h.Observe(0)

	snap := h.Snapshot()
	if snap.Count != 1 {
		t.Errorf("expected count 1, got %d", snap.Count)
	}
	if snap.Sum != 0 {
		t.Errorf("expected sum 0, got %d", snap.Sum)
	}
	// Should be in first bucket
	if snap.Buckets[0] != 1 {
		t.Errorf("expected first bucket to have 1, got %d", snap.Buckets[0])
	}
}

// TestLatencyHistogram_NegativeLatency tests observing negative latency (edge case)
func TestLatencyHistogram_NegativeLatency_by_kimi2_5(t *testing.T) {
	h := NewLatencyHistogram()

	// Negative latencies should still work, going into first bucket
	h.Observe(-100)

	snap := h.Snapshot()
	if snap.Count != 1 {
		t.Errorf("expected count 1, got %d", snap.Count)
	}
	if snap.Sum != -100 {
		t.Errorf("expected sum -100, got %d", snap.Sum)
	}
}
