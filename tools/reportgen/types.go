package main

import "time"

// ReportData 是报告的完整数据结构，序列化为 JSON 注入 HTML 模板。
type ReportData struct {
	Title       string `json:"title"`
	PeriodLabel string `json:"period_label"`
	GeneratedAt string `json:"generated_at"`
	PeriodDays  int    `json:"period_days"`
	PrevLabel   string `json:"prev_label,omitempty"`

	KPI         KPIData       `json:"kpi"`
	DailyTrend  []DailyRow    `json:"daily_trend"`
	HeatmapData []HeatmapCell `json:"heatmap_data"`

	TopUsersByToken   []TopUserRow `json:"top_users_by_token"`
	TopUsersByCost    []TopUserRow `json:"top_users_by_cost"`
	TopUsersByRequest []TopUserRow `json:"top_users_by_request"`

	ModelDistribution []ModelRow    `json:"model_distribution"`
	GroupComparison   []GroupRow    `json:"group_comparison"`
	UpstreamStats     []UpstreamRow `json:"upstream_stats"`

	StatusCodeDist []StatusCodeRow  `json:"status_code_dist"`
	SlowRequests   []SlowRequestRow `json:"slow_requests"`
	ErrorRequests  []ErrorRequestRow `json:"error_requests"`

	StreamingRatio StreamingRatioData `json:"streaming_ratio"`
	Engagement     EngagementData     `json:"engagement"`

	UserFreqBuckets []HistogramBucket `json:"user_freq_buckets"`
	IORatioBuckets  []HistogramBucket `json:"io_ratio_buckets"`
	ParetoData      []ParetoRow       `json:"pareto_data"`

	// Phase 2: 延迟分析
	LatencyBoxPlots    []LatencyBoxPlotRow   `json:"latency_box_plots"`
	LatencyPercentiles []LatencyPercentileRow `json:"latency_percentiles"`

	// Phase 2: 成本预测
	DailyLatencyTrend  []DailyLatencyRow `json:"daily_latency_trend"`

	// Phase 3: 留存分析
	RetentionData         []RetentionRow    `json:"retention_data"`
	IOScatterPlot         []IOScatterPoint  `json:"io_scatter_plot"`
	ModelCostBreakdown    []ModelCostRow    `json:"model_cost_breakdown"`

	// Phase 4: 高价值补齐
	EngagementTrend       []EngagementTrendRow `json:"engagement_trend"`
	QuotaUsage            []QuotaUsageRow      `json:"quota_usage"`
	UpstreamLatencyBoxPlot []LatencyBoxPlotRow `json:"upstream_latency_box_plot"`

	// Phase 5: 中等价值补齐
	GroupTokenBoxPlots    []GroupTokenDistribution `json:"group_token_box_plots"`

	// Phase 7: 请求数统计
	UserRequestBoxPlot UserRequestBoxPlotData `json:"user_request_box_plot"`

	// Phase 6: 低频补齐
	ModelRadarData []ModelRadarData `json:"model_radar_data"`
	AdoptionRate   AdoptionRateData `json:"adoption_rate"`

	// Phase 8: 补全缺失特性
	LatencyHistogram      []LatencyHistogramBucket  `json:"latency_histogram"`
	LatencyScatter        []LatencyScatterPoint     `json:"latency_scatter"`
	TokenThroughputHeatmap []TokenThroughputCell    `json:"token_throughput_heatmap"`
	UpstreamShare         []UpstreamShareRow        `json:"upstream_share"`
	UpstreamLatencyTrend  []UpstreamLatencyTrendRow `json:"upstream_latency_trend"`
	CostPerTokenTrend     []CostPerTokenRow         `json:"cost_per_token_trend"`
	IORatioTrend          []IORatioTrendRow         `json:"io_ratio_trend"`
	ModelInputBoxPlots    []TokenBoxPlotRow         `json:"model_input_box_plots"`
	ModelOutputBoxPlots   []TokenBoxPlotRow         `json:"model_output_box_plots"`
	SourceNodeDist        []SourceNodeRow           `json:"source_node_dist"`
	StreamingBoxPlot      []StreamingBoxPlotRow     `json:"streaming_box_plot"`
	ModelDailyTrend       []ModelDailyRow           `json:"model_daily_trend"`

	// Phase 9: 剩余缺口补全
	UserTierDist          []UserTierRow             `json:"user_tier_dist"`
	UserTokenPercentiles  []UserTokenPercentileRow  `json:"user_token_percentiles"`

	Insights []Insight `json:"insights"`
}

// Phase 8: 按模型每日请求数/Token（堆叠面积图）
type ModelDailyRow struct {
	Date   string `json:"date"`
	Model  string `json:"model"`
	Count  int64  `json:"count"`
	Tokens int64  `json:"tokens"`
}

// Phase 9: 用户分层（4 层：超级/活跃/普通/非活跃）
type UserTierRow struct {
	Tier       string  `json:"tier"`        // "超级用户","活跃用户","普通用户","非活跃"
	UserCount  int     `json:"user_count"`
	TokenShare float64 `json:"token_share"` // 该层用户 Token 占总量百分比
	MinReqs    int64   `json:"min_reqs"`
	MaxReqs    int64   `json:"max_reqs"`
}

// Phase 9: 用户 Token 用量百分位（P50/P75/P90/P95/P99）
type UserTokenPercentileRow struct {
	Percentile string `json:"percentile"` // "P50","P75","P90","P95","P99"
	Tokens     int64  `json:"tokens"`
}

type KPIData struct {
	TotalRequests   int64   `json:"total_requests"`
	TotalInput      int64   `json:"total_input"`
	TotalOutput     int64   `json:"total_output"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCost       float64 `json:"total_cost"`
	ActiveUsers     int     `json:"active_users"`
	RegisteredUsers int     `json:"registered_users"`
	ErrorCount      int64   `json:"error_count"`
	ErrorRate       float64 `json:"error_rate"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	P95LatencyMs    int64   `json:"p95_latency_ms"`
	P99LatencyMs    int64   `json:"p99_latency_ms"`
	StreamingPct    float64 `json:"streaming_pct"`
	PeakRPM         int64   `json:"peak_rpm"`

	PrevTotalRequests int64   `json:"prev_total_requests"`
	RequestsChange    float64 `json:"requests_change"`
	TokensChange      float64 `json:"tokens_change"`
	CostChange        float64 `json:"cost_change"`
	UsersChange       float64 `json:"users_change"`
	ErrorRateChange   float64 `json:"error_rate_change"`
	LatencyChange     float64 `json:"latency_change"`
}

type DailyRow struct {
	Date         string  `json:"date"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int64   `json:"requests"`
	ActiveUsers  int     `json:"active_users"`
	Errors       int64   `json:"errors"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

type HeatmapCell struct {
	Hour  int   `json:"hour"`
	Day   int   `json:"day"`
	Value int64 `json:"value"`
}

type TopUserRow struct {
	Username     string  `json:"username"`
	GroupName    string  `json:"group_name,omitempty"`
	Value        float64 `json:"value"`
	Requests     int64   `json:"requests,omitempty"`
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

type ModelRow struct {
	Model        string  `json:"model"`
	Count        int64   `json:"count"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	ErrorRate    float64 `json:"error_rate"`
}

type GroupRow struct {
	GroupID      string  `json:"group_id"`
	GroupName    string  `json:"group_name"`
	Users        int     `json:"users"`
	ActiveUsers  int     `json:"active_users"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int64   `json:"requests"`
}

type UpstreamRow struct {
	URL         string  `json:"url"`
	Requests    int64   `json:"requests"`
	AvgLatency  float64 `json:"avg_latency"`
	ErrorRate   float64 `json:"error_rate"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type StatusCodeRow struct {
	StatusCode int   `json:"status_code"`
	Count      int64 `json:"count"`
}

type SlowRequestRow struct {
	CreatedAt    string `json:"created_at"`
	Username     string `json:"username"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	DurationMs   int64  `json:"duration_ms"`
	StatusCode   int    `json:"status_code"`
	UpstreamURL  string `json:"upstream_url"`
}

type ErrorRequestRow struct {
	CreatedAt   string `json:"created_at"`
	Username    string `json:"username"`
	Model       string `json:"model"`
	StatusCode  int    `json:"status_code"`
	DurationMs  int64  `json:"duration_ms"`
	InputTokens int64  `json:"input_tokens"`
	UpstreamURL string `json:"upstream_url"`
	RequestID   string `json:"request_id"`
}

type StreamingRatioData struct {
	StreamingCount         int64   `json:"streaming_count"`
	NonStreamingCount      int64   `json:"non_streaming_count"`
	StreamingPct           float64 `json:"streaming_pct"`
	StreamingAvgLatency    float64 `json:"streaming_avg_latency"`
	NonStreamingAvgLatency float64 `json:"non_streaming_avg_latency"`
}

type EngagementData struct {
	DAU                int      `json:"dau"`
	WAU                int      `json:"wau"`
	MAU                int      `json:"mau"`
	Stickness          float64  `json:"stickness"`
	AdoptionRate       float64  `json:"adoption_rate"`
	ZeroUseCount       int      `json:"zero_use_count"`
	ZeroUsePct         float64  `json:"zero_use_pct"`
	PowerUsers         []string `json:"power_users"`
	NewUsersThisPeriod int      `json:"new_users_this_period"`
}

type HistogramBucket struct {
	Range string `json:"range"`
	Count int    `json:"count"`
}

type ParetoRow struct {
	Username      string  `json:"username"`
	TotalTokens   int64   `json:"total_tokens"`
	CumulativePct float64 `json:"cumulative_pct"`
}

// Phase 2: 延迟箱线图（按模型分组）
type LatencyBoxPlotRow struct {
	Model   string `json:"model"`
	Min     int64  `json:"min"`
	Q1      int64  `json:"q1"`
	Median  int64  `json:"median"`
	Q3      int64  `json:"q3"`
	Max     int64  `json:"max"`
	IQR     int64  `json:"iqr"`
	Count   int    `json:"count"`
}

// Phase 2: 延迟百分位趋势（按日）
type LatencyPercentileRow struct {
	Date       string `json:"date"`
	P50        int64  `json:"p50"`
	P95        int64  `json:"p95"`
	P99        int64  `json:"p99"`
	Count      int    `json:"count"`
}

// Phase 2: 每日延迟趋势数据
type DailyLatencyRow struct {
	Date         string  `json:"date"`
	AvgLatency   float64 `json:"avg_latency"`
	MaxLatency   int64   `json:"max_latency"`
	CostUSD      float64 `json:"cost_usd"`
}

// Phase 3: 用户留存曲线（同期群分析）
type RetentionRow struct {
	FirstUseDate   string `json:"first_use_date"`
	DaysSinceBirth int    `json:"days_since_birth"`
	ActiveUsers    int    `json:"active_users"`
	RetentionRate  float64 `json:"retention_rate"`
}

// Phase 3: Input vs Output 散点图
type IOScatterPoint struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Phase 3: 按模型的费用分布（而非Token分布）
type ModelCostRow struct {
	Model       string  `json:"model"`
	CostUSD     float64 `json:"cost_usd"`
	CostPercent float64 `json:"cost_percent"`
	Requests    int64   `json:"requests"`
}

// Phase 4: DAU/WAU/MAU 趋势（按日）
type EngagementTrendRow struct {
	Date string `json:"date"`
	DAU  int    `json:"dau"`
	WAU  int    `json:"wau"`
	MAU  int    `json:"mau"`
}

// Phase 4: 用户配额使用（如果有配额数据）
type QuotaUsageRow struct {
	UserID            string  `json:"user_id"`
	Username          string  `json:"username"`
	DailyLimit        int64   `json:"daily_limit"`
	MonthlyLimit      int64   `json:"monthly_limit"`
	DailyUsed         int64   `json:"daily_used"`
	MonthlyUsed       int64   `json:"monthly_used"`
	DailyUsagePercent float64 `json:"daily_usage_percent"`
	MonthlyUsagePercent float64 `json:"monthly_usage_percent"`
}

// Phase 6: Model Radar Chart Data
type ModelRadarData struct {
	Model            string  `json:"model"`
	LatencyScore     float64 `json:"latency_score"`
	CostScore        float64 `json:"cost_score"`
	ThroughputScore  float64 `json:"throughput_score"`
	ReliabilityScore float64 `json:"reliability_score"`
	AdoptionScore    float64 `json:"adoption_score"`
}

// Phase 6: Adoption Rate Data
type AdoptionRateData struct {
	TotalRegistered int     `json:"total_registered"`
	TotalActive     int     `json:"total_active"`
	AdoptionPercent float64 `json:"adoption_percent"`
}

// Phase 7: 用户请求数箱线图统计
type UserRequestBoxPlotData struct {
	Min    int64   `json:"min"`
	Q1     int64   `json:"q1"`
	Median int64   `json:"median"`
	Q3     int64   `json:"q3"`
	Max    int64   `json:"max"`
	IQR    int64   `json:"iqr"`
	Mean   float64 `json:"mean"`
	Count  int     `json:"count"` // number of active users
}

type Insight struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Emoji  string `json:"emoji"`
}

// Phase 8: 延迟直方图（duration_ms 分桶）
type LatencyHistogramBucket struct {
	Range string `json:"range"`
	Count int64  `json:"count"`
}

// Phase 8: 延迟 vs Token 散点（采样）
type LatencyScatterPoint struct {
	TotalTokens int64 `json:"total_tokens"`
	DurationMs  int64 `json:"duration_ms"`
}

// Phase 8: Token 吞吐热力图（hour × dow）
type TokenThroughputCell struct {
	Hour  int   `json:"hour"`
	Day   int   `json:"day"`
	Value int64 `json:"value"`
}

// Phase 8: 上游流量占比饼图
type UpstreamShareRow struct {
	URL      string `json:"url"`
	Requests int64  `json:"requests"`
}

// Phase 8: 按上游逐日延迟趋势（多线）
type UpstreamLatencyTrendRow struct {
	URL        string  `json:"url"`
	Date       string  `json:"date"`
	AvgLatency float64 `json:"avg_latency"`
}

// Phase 8: 每 Token 费用逐日趋势
type CostPerTokenRow struct {
	Date         string  `json:"date"`
	CostPerToken float64 `json:"cost_per_token"`
}

// Phase 8: I/O 比率逐日趋势
type IORatioTrendRow struct {
	Date    string  `json:"date"`
	IORatio float64 `json:"io_ratio"`
}

// Phase 8: 按模型 Input/Output Token 箱线图
type TokenBoxPlotRow struct {
	Model  string `json:"model"`
	Min    int64  `json:"min"`
	Q1     int64  `json:"q1"`
	Median int64  `json:"median"`
	Q3     int64  `json:"q3"`
	Max    int64  `json:"max"`
	Count  int    `json:"count"`
}

// Phase 8: source_node 流量分布
type SourceNodeRow struct {
	SourceNode string `json:"source_node"`
	Requests   int64  `json:"requests"`
}

// Phase 8: 流式 vs 非流式延迟箱线图
type StreamingBoxPlotRow struct {
	Label  string `json:"label"`
	Min    int64  `json:"min"`
	Q1     int64  `json:"q1"`
	Median int64  `json:"median"`
	Q3     int64  `json:"q3"`
	Max    int64  `json:"max"`
	Count  int    `json:"count"`
}

// QueryParams holds query parameters.
type QueryParams struct {
	DBPath string // SQLite file path（Driver=sqlite 时使用）
	DSN    string // PostgreSQL DSN（Driver=postgres 时使用）
	Driver string // "sqlite"（默认）| "postgres"
	From   time.Time
	To     time.Time
}

// PrevPeriod returns the previous period time range.
func (q QueryParams) PrevPeriod() (from, to time.Time) {
	d := q.To.Sub(q.From)
	return q.From.Add(-d), q.From
}
