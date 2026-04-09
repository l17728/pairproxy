package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// GenerateReport orchestrates the full report generation pipeline.
// All query failures are non-fatal: missing data produces empty sections.
// LLM failure degrades gracefully to rule-based insights only.
func GenerateReport(params QueryParams, templatePath, outputPath string) error {
	q, err := NewQuerier(params.Driver, params.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer q.Close()

	fmt.Fprintf(os.Stderr, "📊 开始生成报告...\n")
	fmt.Fprintf(os.Stderr, "   数据库驱动: %s\n", params.Driver)
	fmt.Fprintf(os.Stderr, "   查询时间范围: %s 至 %s\n", params.From.Format("2006-01-02 15:04:05"), params.To.Format("2006-01-02 15:04:05"))

	var data ReportData
	data.Title = "PairProxy 分析报告"
	data.PeriodLabel = formatPeriodLabel(params.From, params.To)
	data.GeneratedAt = time.Now().Format("2006-01-02 15:04:05")
	data.PeriodDays = int(params.To.Sub(params.From).Hours() / 24)
	if data.PeriodDays < 1 {
		data.PeriodDays = 1
	}
	data.PrevLabel = formatPrevLabel(params.From, params.To)

	// Run all queries (best-effort: individual failures yield nil/zero, not abort)
	data.KPI, _ = q.QueryKPI(params.From, params.To)
	if data.KPI.TotalRequests > 0 {
		fmt.Fprintf(os.Stderr, "✅ KPI 查询成功: 总请求 %d, 总 Token %d, 总费用 $%.2f\n",
			data.KPI.TotalRequests, data.KPI.TotalTokens, data.KPI.TotalCost)
	} else {
		fmt.Fprintf(os.Stderr, "⚠️  KPI 查询结果为空: 请检查时间范围和数据库连接\n")
	}
	data.DailyTrend, _ = q.QueryDailyTrend(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   每日趋势: %d 天\n", len(data.DailyTrend))
	data.HeatmapData, _ = q.QueryHeatmap(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   热力图数据点: %d\n", len(data.HeatmapData))
	data.TopUsersByToken, _ = q.QueryTopUsers(params.From, params.To, "tokens", 10)
	data.TopUsersByCost, _ = q.QueryTopUsers(params.From, params.To, "cost", 10)
	data.TopUsersByRequest, _ = q.QueryTopUsers(params.From, params.To, "requests", 10)
	fmt.Fprintf(os.Stderr, "   TOP用户: Token=%d Cost=%d Req=%d\n",
		len(data.TopUsersByToken), len(data.TopUsersByCost), len(data.TopUsersByRequest))
	data.ModelDistribution, _ = q.QueryModelDistribution(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   模型分布: %d 个模型\n", len(data.ModelDistribution))
	data.GroupComparison, _ = q.QueryGroupComparison(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   分组对比: %d 个分组\n", len(data.GroupComparison))
	data.UpstreamStats, _ = q.QueryUpstreamStats(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   上游状态: %d 个上游\n", len(data.UpstreamStats))
	data.StatusCodeDist, _ = q.QueryStatusCodeDist(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   状态码分布: %d 种\n", len(data.StatusCodeDist))
	data.SlowRequests, _ = q.QuerySlowRequests(params.From, params.To, 10)
	fmt.Fprintf(os.Stderr, "   慢请求: %d 条\n", len(data.SlowRequests))
	data.ErrorRequests, _ = q.QueryErrorRequests(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   错误请求: %d 条\n", len(data.ErrorRequests))
	data.StreamingRatio, _ = q.QueryStreamingRatio(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   流式/非流式: %d/%d\n", data.StreamingRatio.StreamingCount, data.StreamingRatio.NonStreamingCount)

	registeredUsers := q.CountRegisteredUsers()
	data.Engagement, _ = q.QueryEngagement(params.From, params.To, registeredUsers)
	fmt.Fprintf(os.Stderr, "   参与度: DAU=%d WAU=%d MAU=%d\n",
		data.Engagement.DAU, data.Engagement.WAU, data.Engagement.MAU)

	data.UserFreqBuckets, _ = q.QueryUserFreqBuckets(params.From, params.To)
	data.IORatioBuckets, _ = q.QueryIORatioBuckets(params.From, params.To)
	data.ParetoData, _ = q.QueryParetoData(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   频次桶=%d I/O桶=%d 帕累托=%d\n",
		len(data.UserFreqBuckets), len(data.IORatioBuckets), len(data.ParetoData))

	// Phase 2: Latency analysis
	data.LatencyBoxPlots, _ = q.QueryLatencyBoxPlotByModel(params.From, params.To)
	data.LatencyPercentiles, _ = q.QueryLatencyPercentileTrend(params.From, params.To)
	data.DailyLatencyTrend, _ = q.QueryDailyLatencyTrend(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   延迟箱线=%d 百分位趋势=%d 每日延迟=%d\n",
		len(data.LatencyBoxPlots), len(data.LatencyPercentiles), len(data.DailyLatencyTrend))

	// Phase 3: Advanced analysis
	data.RetentionData, _ = q.QueryRetentionData(params.From, params.To)
	data.IOScatterPlot, _ = q.QueryIOScatterPlot(params.From, params.To, 1000)
	data.ModelCostBreakdown, _ = q.QueryModelCostBreakdown(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   留存=%d IO散点=%d 模型费用=%d\n",
		len(data.RetentionData), len(data.IOScatterPlot), len(data.ModelCostBreakdown))

	// Phase 4: High-value supplements
	data.EngagementTrend, _ = q.QueryEngagementTrend(params.From, params.To)
	data.QuotaUsage, _ = q.QueryQuotaUsage(params.From, params.To)
	data.UpstreamLatencyBoxPlot, _ = q.QueryLatencyBoxPlotByUpstream(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   参与趋势=%d 配额=%d 上游延迟箱=%d\n",
		len(data.EngagementTrend), len(data.QuotaUsage), len(data.UpstreamLatencyBoxPlot))

	// Phase 5: Medium-value supplements
	data.GroupTokenBoxPlots, _ = q.QueryGroupTokenDistribution(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   分组Token箱=%d\n", len(data.GroupTokenBoxPlots))

	// Phase 6: Low-frequency enhancements
	data.ModelRadarData, _ = q.QueryModelRadarData(params.From, params.To)
	data.AdoptionRate.TotalRegistered = registeredUsers
	activeUsers, _ := q.QueryActiveUsersInPeriod(params.From, params.To)
	data.AdoptionRate.TotalActive = activeUsers
	if data.AdoptionRate.TotalRegistered > 0 {
		data.AdoptionRate.AdoptionPercent = float64(activeUsers) / float64(data.AdoptionRate.TotalRegistered) * 100
	}
	fmt.Fprintf(os.Stderr, "   雷达=%d 采纳率=%.1f%%(活跃%d/注册%d)\n",
		len(data.ModelRadarData), data.AdoptionRate.AdoptionPercent,
		data.AdoptionRate.TotalActive, data.AdoptionRate.TotalRegistered)

	// Phase 7: Request-count analytics
	data.UserRequestBoxPlot, _ = q.QueryUserRequestBoxPlot(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   用户请求箱线: count=%d min=%d median=%d max=%d\n",
		data.UserRequestBoxPlot.Count, data.UserRequestBoxPlot.Min,
		data.UserRequestBoxPlot.Median, data.UserRequestBoxPlot.Max)

	// Phase 8: Missing/partial features
	data.LatencyHistogram, _ = q.QueryLatencyHistogram(params.From, params.To)
	data.LatencyScatter, _ = q.QueryLatencyScatter(params.From, params.To, 1000)
	data.TokenThroughputHeatmap, _ = q.QueryTokenThroughputHeatmap(params.From, params.To)
	data.UpstreamShare, _ = q.QueryUpstreamShare(params.From, params.To)
	data.UpstreamLatencyTrend, _ = q.QueryUpstreamLatencyTrend(params.From, params.To)
	data.CostPerTokenTrend, _ = q.QueryCostPerTokenTrend(params.From, params.To)
	data.IORatioTrend, _ = q.QueryIORatioTrend(params.From, params.To)
	data.ModelInputBoxPlots, _ = q.QueryModelTokenBoxPlots(params.From, params.To, "input_tokens")
	data.ModelOutputBoxPlots, _ = q.QueryModelTokenBoxPlots(params.From, params.To, "output_tokens")
	data.SourceNodeDist, _ = q.QuerySourceNodeDist(params.From, params.To)
	data.StreamingBoxPlot, _ = q.QueryStreamingBoxPlot(params.From, params.To)
	data.ModelDailyTrend, _ = q.QueryModelDailyTrend(params.From, params.To)
	data.KPI.PeakRPM, _ = q.QueryPeakRPM(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   延迟直方图=%d 延迟散点=%d Token热力=%d 上游占比=%d 上游延迟趋势=%d\n",
		len(data.LatencyHistogram), len(data.LatencyScatter), len(data.TokenThroughputHeatmap),
		len(data.UpstreamShare), len(data.UpstreamLatencyTrend))
	fmt.Fprintf(os.Stderr, "   I/O比率趋势=%d 模型每日趋势=%d 峰值RPM=%d\n",
		len(data.IORatioTrend), len(data.ModelDailyTrend), data.KPI.PeakRPM)

	// Phase 9: remaining gaps
	data.UserTierDist, _ = q.QueryUserTierDist(params.From, params.To)
	data.UserTokenPercentiles, _ = q.QueryUserTokenPercentiles(params.From, params.To)
	fmt.Fprintf(os.Stderr, "   用户分层=%d Token百分位=%d\n",
		len(data.UserTierDist), len(data.UserTokenPercentiles))

	// Warn when no data was found (empty period or new deployment)
	if data.KPI.TotalRequests == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  指定时间段内无请求数据，将生成空报告\n")
		data.Insights = []Insight{{
			Type:   "no_data",
			Title:  "📭 暂无数据",
			Detail: fmt.Sprintf("在 %s 期间未找到任何请求记录。请确认时间范围和数据库路径是否正确。", data.PeriodLabel),
			Emoji:  "📭",
		}}
	} else {
		// Generate rule-based insights (with panic recovery)
		data.Insights = safeGenerateInsights(&data)

		// Generate LLM insights (best-effort, degrades to rule-based on failure)
		if llmInsight := GenerateLLMInsights(&data, params); llmInsight != nil {
			data.Insights = append(data.Insights, *llmInsight)
		}
	}

	// Read template; fall back to minimal HTML on failure
	tmplBytes, err := os.ReadFile(templatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  模板读取失败（%v），使用内置最小模板\n", err)
		tmplBytes = []byte(minimalTemplate)
	}

	// Marshal data to JSON
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	// Inject JSON into template
	jsonStr := string(jsonBytes)
	// Escape </script> to prevent premature script tag closure
	jsonStr = strings.ReplaceAll(jsonStr, "</script>", "<\\/script>")

	html := strings.ReplaceAll(string(tmplBytes), "{{REPORT_DATA}}", jsonStr)

	// Write output
	if err := os.WriteFile(outputPath, []byte(html), 0644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// safeGenerateInsights wraps GenerateInsights with panic recovery so that
// a bug in any insight rule cannot crash the entire report generation.
func safeGenerateInsights(data *ReportData) (insights []Insight) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "⚠️  规则洞察生成异常（已跳过）: %v\n", r)
			insights = nil
		}
	}()
	return GenerateInsights(data)
}

// minimalTemplate is used when the HTML template file cannot be read.
// It renders the report data as formatted JSON for debugging purposes.
const minimalTemplate = `<!DOCTYPE html>
<html lang="zh">
<head><meta charset="UTF-8"><title>PairProxy 报告（降级模式）</title>
<style>body{font-family:monospace;padding:20px;background:#f5f5f5}
pre{background:#fff;padding:20px;border-radius:8px;overflow:auto;font-size:12px}
h1{color:#e74c3c}p{color:#666}</style></head>
<body>
<h1>⚠️ 报告模板加载失败（降级模式）</h1>
<p>HTML 模板文件不可用，以下为原始报告数据（JSON）。请检查 <code>-template</code> 参数路径是否正确。</p>
<pre id="data"></pre>
<script>
const d = {{REPORT_DATA}};
document.getElementById('data').textContent = JSON.stringify(d, null, 2);
</script>
</body></html>`

func formatPeriodLabel(from, to time.Time) string {
	return fmt.Sprintf("%s 至 %s",
		from.Format("2006-01-02"),
		to.Add(-time.Second).Format("2006-01-02"))
}

func formatPrevLabel(from, to time.Time) string {
	pf, pt := QueryParams{From: from, To: to}.PrevPeriod()
	return formatPeriodLabel(pf, pt)
}
