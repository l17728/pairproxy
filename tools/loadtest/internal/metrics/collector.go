// Package metrics 收集和报告性能指标
package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Collector 指标收集器
type Collector struct {
	mu      sync.RWMutex
	results []*Result
	logger  *zap.Logger

	// 实时统计
	startTime      time.Time
	lastReportTime time.Time
}

// Result 单次请求的指标
type Result struct {
	Timestamp  time.Time     `json:"timestamp"`
	WorkerID   int           `json:"worker_id"`
	Prompt     string        `json:"prompt"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Duration   time.Duration `json:"duration_ms"`
	Success    bool          `json:"success"`
	Error      string        `json:"error,omitempty"`
	OutputSize int           `json:"output_size"`
}

// Report 测试报告
type Report struct {
	StartTime    time.Time     `json:"start_time"`
	EndTime      time.Time     `json:"end_time"`
	Duration     time.Duration `json:"duration"`
	TotalWorkers int           `json:"total_workers"`

	// 请求统计
	TotalRequests int64   `json:"total_requests"`
	SuccessCount  int64   `json:"success_count"`
	FailureCount  int64   `json:"failure_count"`
	SuccessRate   float64 `json:"success_rate"`

	// 延迟统计（毫秒）
	LatencyStats LatencyStats `json:"latency_stats"`

	// 吞吐量
	ThroughputRPS float64 `json:"throughput_rps"`
	ThroughputBPS float64 `json:"throughput_bytes_per_sec"`

	// 错误分类
	ErrorBreakdown map[string]int `json:"error_breakdown"`

	// 时间序列数据（用于图表）
	TimeSeries []TimePoint `json:"time_series"`
}

// LatencyStats 延迟统计
type LatencyStats struct {
	Min  float64 `json:"min_ms"`
	Max  float64 `json:"max_ms"`
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P90  float64 `json:"p90_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
}

// TimePoint 时间点数据
type TimePoint struct {
	Timestamp     time.Time `json:"timestamp"`
	ActiveWorkers int       `json:"active_workers"`
	RPS           float64   `json:"rps"`
	SuccessRate   float64   `json:"success_rate"`
	AvgLatency    float64   `json:"avg_latency_ms"`
}

// NewCollector 创建新的收集器
func NewCollector(logger *zap.Logger) *Collector {
	return &Collector{
		results:        make([]*Result, 0),
		logger:         logger,
		startTime:      time.Now(),
		lastReportTime: time.Now(),
	}
}

// Record 记录单次结果
func (c *Collector) Record(r *Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.results = append(c.results, r)
}

// GetSnapshot 获取当前统计快照
func (c *Collector) GetSnapshot(activeWorkers int) *Report {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.generateReport(activeWorkers)
}

// GenerateFinalReport 生成最终报告
func (c *Collector) GenerateFinalReport(totalWorkers int) *Report {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.generateReport(totalWorkers)
}

// generateReport 生成报告（内部方法）
func (c *Collector) generateReport(totalWorkers int) *Report {
	now := time.Now()
	duration := now.Sub(c.startTime)

	report := &Report{
		StartTime:      c.startTime,
		EndTime:        now,
		Duration:       duration,
		TotalWorkers:   totalWorkers,
		ErrorBreakdown: make(map[string]int),
		TimeSeries:     make([]TimePoint, 0),
	}

	if len(c.results) == 0 {
		return report
	}

	// 计算基本统计
	var totalDuration time.Duration
	latencies := make([]float64, 0, len(c.results))

	for _, r := range c.results {
		report.TotalRequests++
		if r.Success {
			report.SuccessCount++
			totalDuration += r.Duration
			latencies = append(latencies, float64(r.Duration.Milliseconds()))
		} else {
			report.FailureCount++
			report.ErrorBreakdown[r.Error]++
		}
	}

	// 成功率
	report.SuccessRate = float64(report.SuccessCount) / float64(report.TotalRequests) * 100

	// 延迟统计
	if len(latencies) > 0 {
		report.LatencyStats = calculateLatencyStats(latencies)
	}

	// 吞吐量
	if duration.Seconds() > 0 {
		report.ThroughputRPS = float64(report.TotalRequests) / duration.Seconds()
	}

	return report
}

// calculateLatencyStats 计算延迟统计
func calculateLatencyStats(latencies []float64) LatencyStats {
	sort.Float64s(latencies)
	n := len(latencies)

	var sum float64
	for _, v := range latencies {
		sum += v
	}

	return LatencyStats{
		Min:  latencies[0],
		Max:  latencies[n-1],
		Mean: sum / float64(n),
		P50:  percentile(latencies, 50),
		P90:  percentile(latencies, 90),
		P95:  percentile(latencies, 95),
		P99:  percentile(latencies, 99),
	}
}

// percentile 计算百分位数
func percentile(sorted []float64, p float64) float64 {
	n := float64(len(sorted))
	index := (p / 100) * (n - 1)

	lower := int(index)
	upper := lower + 1

	if upper >= len(sorted) {
		return sorted[lower]
	}

	weight := index - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

// PrintReport 打印报告到控制台
func (r *Report) PrintReport() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("                    Load Test Report")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\nTest Duration:     %v\n", r.Duration.Round(time.Second))
	fmt.Printf("Total Workers:     %d\n", r.TotalWorkers)

	fmt.Println("\n--- Request Statistics ---")
	fmt.Printf("Total Requests:    %d\n", r.TotalRequests)
	fmt.Printf("Success:           %d (%.2f%%)\n", r.SuccessCount, r.SuccessRate)
	fmt.Printf("Failures:          %d (%.2f%%)\n", r.FailureCount, 100-r.SuccessRate)

	fmt.Println("\n--- Latency Statistics (ms) ---")
	fmt.Printf("Min:               %.2f\n", r.LatencyStats.Min)
	fmt.Printf("Mean:              %.2f\n", r.LatencyStats.Mean)
	fmt.Printf("Max:               %.2f\n", r.LatencyStats.Max)
	fmt.Printf("P50:               %.2f\n", r.LatencyStats.P50)
	fmt.Printf("P90:               %.2f\n", r.LatencyStats.P90)
	fmt.Printf("P95:               %.2f\n", r.LatencyStats.P95)
	fmt.Printf("P99:               %.2f\n", r.LatencyStats.P99)

	fmt.Println("\n--- Throughput ---")
	fmt.Printf("Requests/sec:      %.2f\n", r.ThroughputRPS)
	fmt.Printf("Success/sec:       %.2f\n", r.ThroughputRPS*r.SuccessRate/100)

	if len(r.ErrorBreakdown) > 0 {
		fmt.Println("\n--- Error Breakdown ---")
		for err, count := range r.ErrorBreakdown {
			fmt.Printf("%-30s %d\n", err+":", count)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
}

// SaveToFile 保存报告到 JSON 文件
func (r *Report) SaveToFile(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// Reporter 实时报告器
type Reporter struct {
	collector *Collector
	interval  time.Duration
	logger    *zap.Logger
	stopCh    chan struct{}
}

// NewReporter 创建实时报告器
func NewReporter(collector *Collector, interval time.Duration, logger *zap.Logger) *Reporter {
	return &Reporter{
		collector: collector,
		interval:  interval,
		logger:    logger,
		stopCh:    make(chan struct{}),
	}
}

// Start 启动实时报告
func (r *Reporter) Start(getWorkers func() int) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := r.collector.GetSnapshot(getWorkers())
			r.printRealtimeStats(snapshot)
		case <-r.stopCh:
			return
		}
	}
}

// Stop 停止报告
func (r *Reporter) Stop() {
	close(r.stopCh)
}

// printRealtimeStats 打印实时统计
func (r *Reporter) printRealtimeStats(s *Report) {
	r.logger.Info("Realtime stats",
		zap.Int("workers", s.TotalWorkers),
		zap.Int64("requests", s.TotalRequests),
		zap.Float64("success_rate", s.SuccessRate),
		zap.Float64("rps", s.ThroughputRPS),
		zap.Float64("p50_ms", s.LatencyStats.P50),
		zap.Float64("p95_ms", s.LatencyStats.P95),
		zap.Float64("p99_ms", s.LatencyStats.P99),
	)
}

// Aggregator 多节点结果聚合器
type Aggregator struct {
	reports []*Report
}

// NewAggregator 创建聚合器
func NewAggregator() *Aggregator {
	return &Aggregator{
		reports: make([]*Report, 0),
	}
}

// Add 添加报告
func (a *Aggregator) Add(r *Report) {
	a.reports = append(a.reports, r)
}

// Aggregate 生成聚合报告
func (a *Aggregator) Aggregate() *Report {
	if len(a.reports) == 0 {
		return &Report{}
	}

	aggregated := &Report{
		StartTime:      a.reports[0].StartTime,
		EndTime:        a.reports[0].EndTime,
		ErrorBreakdown: make(map[string]int),
	}

	// 聚合所有报告
	allLatencies := make([]float64, 0)

	for _, r := range a.reports {
		aggregated.TotalWorkers += r.TotalWorkers
		aggregated.TotalRequests += r.TotalRequests
		aggregated.SuccessCount += r.SuccessCount
		aggregated.FailureCount += r.FailureCount

		// 聚合延迟数据
		if r.TotalRequests > 0 {
			allLatencies = append(allLatencies, r.LatencyStats.Mean)
		}

		// 聚合错误
		for err, count := range r.ErrorBreakdown {
			aggregated.ErrorBreakdown[err] += count
		}
	}

	// 计算聚合指标
	if aggregated.TotalRequests > 0 {
		aggregated.SuccessRate = float64(aggregated.SuccessCount) / float64(aggregated.TotalRequests) * 100

		// 延迟取平均
		if len(allLatencies) > 0 {
			var sum float64
			for _, v := range allLatencies {
				sum += v
			}
			aggregated.LatencyStats.Mean = sum / float64(len(allLatencies))
		}
	}

	return aggregated
}

// LoadFromFiles 从多个 JSON 文件加载报告
func LoadFromFiles(paths []string) (*Aggregator, error) {
	agg := NewAggregator()

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read file %s: %w", path, err)
		}

		var report Report
		if err := json.Unmarshal(data, &report); err != nil {
			return nil, fmt.Errorf("parse file %s: %w", path, err)
		}

		agg.Add(&report)
	}

	return agg, nil
}
