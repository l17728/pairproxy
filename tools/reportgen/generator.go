package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// GenerateReport orchestrates the full report generation pipeline.
func GenerateReport(params QueryParams, templatePath, outputPath string) error {
	q, err := NewQuerier(params.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer q.Close()

	var data ReportData
	data.Title = "PairProxy 分析报告"
	data.PeriodLabel = formatPeriodLabel(params.From, params.To)
	data.GeneratedAt = time.Now().Format("2006-01-02 15:04:05")
	data.PeriodDays = int(params.To.Sub(params.From).Hours() / 24)
	if data.PeriodDays < 1 {
		data.PeriodDays = 1
	}
	data.PrevLabel = formatPrevLabel(params.From, params.To)

	// Run all queries (best-effort: don't abort on individual failures)
	data.KPI, _ = q.QueryKPI(params.From, params.To)
	data.DailyTrend, _ = q.QueryDailyTrend(params.From, params.To)
	data.HeatmapData, _ = q.QueryHeatmap(params.From, params.To)
	data.TopUsersByToken, _ = q.QueryTopUsers(params.From, params.To, "tokens", 10)
	data.TopUsersByCost, _ = q.QueryTopUsers(params.From, params.To, "cost", 10)
	data.ModelDistribution, _ = q.QueryModelDistribution(params.From, params.To)
	data.GroupComparison, _ = q.QueryGroupComparison(params.From, params.To)
	data.UpstreamStats, _ = q.QueryUpstreamStats(params.From, params.To)
	data.StatusCodeDist, _ = q.QueryStatusCodeDist(params.From, params.To)
	data.SlowRequests, _ = q.QuerySlowRequests(params.From, params.To, 10)
	data.StreamingRatio, _ = q.QueryStreamingRatio(params.From, params.To)

	registeredUsers := q.CountRegisteredUsers()
	data.Engagement, _ = q.QueryEngagement(params.From, params.To, registeredUsers)

	data.UserFreqBuckets, _ = q.QueryUserFreqBuckets(params.From, params.To)
	data.IORatioBuckets, _ = q.QueryIORatioBuckets(params.From, params.To)
	data.ParetoData, _ = q.QueryParetoData(params.From, params.To)

	// Phase 2: Latency analysis
	data.LatencyBoxPlots, _ = q.QueryLatencyBoxPlotByModel(params.From, params.To)
	data.LatencyPercentiles, _ = q.QueryLatencyPercentileTrend(params.From, params.To)
	data.DailyLatencyTrend, _ = q.QueryDailyLatencyTrend(params.From, params.To)

	// Phase 3: Advanced analysis
	data.RetentionData, _ = q.QueryRetentionData(params.From, params.To)
	data.IOScatterPlot, _ = q.QueryIOScatterPlot(params.From, params.To, 1000)
	data.ModelCostBreakdown, _ = q.QueryModelCostBreakdown(params.From, params.To)

	// Phase 4: High-value supplements
	data.EngagementTrend, _ = q.QueryEngagementTrend(params.From, params.To)
	data.QuotaUsage, _ = q.QueryQuotaUsage(params.From, params.To)
	data.UpstreamLatencyBoxPlot, _ = q.QueryLatencyBoxPlotByUpstream(params.From, params.To)

	// Phase 5: Medium-value supplements
	data.GroupTokenBoxPlots, _ = q.QueryGroupTokenDistribution(params.From, params.To)

	// Phase 6: Low-frequency enhancements
	data.ModelRadarData, _ = q.QueryModelRadarData(params.From, params.To)
	data.AdoptionRate.TotalRegistered = q.CountRegisteredUsers()
	activeUsers, _ := q.QueryActiveUsersInPeriod(params.From, params.To)
	data.AdoptionRate.TotalActive = activeUsers
	if data.AdoptionRate.TotalRegistered > 0 {
		data.AdoptionRate.AdoptionPercent = float64(activeUsers) / float64(data.AdoptionRate.TotalRegistered) * 100
	}

	// Generate insights
	data.Insights = GenerateInsights(&data)

	// Read template
	tmplBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read template: %w", err)
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

func formatPeriodLabel(from, to time.Time) string {
	return fmt.Sprintf("%s 至 %s",
		from.Format("2006-01-02"),
		to.Add(-time.Second).Format("2006-01-02"))
}

func formatPrevLabel(from, to time.Time) string {
	pf, pt := QueryParams{From: from, To: to}.PrevPeriod()
	return formatPeriodLabel(pf, pt)
}
