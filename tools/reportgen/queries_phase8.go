package main

import (
	"fmt"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 8: QueryLatencyHistogram — duration_ms bucketed histogram
// ---------------------------------------------------------------------------

func (q *Querier) QueryLatencyHistogram(from, to time.Time) ([]LatencyHistogramBucket, error) {
	rows, err := q.query(`
		SELECT
		  CASE
		    WHEN duration_ms < 500   THEN '0-500ms'
		    WHEN duration_ms < 1000  THEN '500-1000ms'
		    WHEN duration_ms < 2000  THEN '1000-2000ms'
		    WHEN duration_ms < 5000  THEN '2000-5000ms'
		    ELSE '5000+ms'
		  END AS bucket,
		  COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND duration_ms IS NOT NULL
		GROUP BY bucket
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query latency histogram: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var bucket string
		var cnt int64
		if err := rows.Scan(&bucket, &cnt); err != nil {
			continue
		}
		counts[bucket] = cnt
	}

	// Return in fixed display order
	order := []string{"0-500ms", "500-1000ms", "1000-2000ms", "2000-5000ms", "5000+ms"}
	result := make([]LatencyHistogramBucket, 0, len(order))
	for _, label := range order {
		result = append(result, LatencyHistogramBucket{Range: label, Count: counts[label]})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryLatencyScatter — (total_tokens, duration_ms) sample
// ---------------------------------------------------------------------------

func (q *Querier) QueryLatencyScatter(from, to time.Time, limit int) ([]LatencyScatterPoint, error) {
	rows, err := q.query(`
		SELECT total_tokens, duration_ms
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND duration_ms IS NOT NULL
		  AND total_tokens > 0
		ORDER BY RANDOM()
		LIMIT ?
	`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query latency scatter: %w", err)
	}
	defer rows.Close()

	var result []LatencyScatterPoint
	for rows.Next() {
		var p LatencyScatterPoint
		if err := rows.Scan(&p.TotalTokens, &p.DurationMs); err != nil {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryTokenThroughputHeatmap — SUM(total_tokens) per hour×dow
// ---------------------------------------------------------------------------

func (q *Querier) QueryTokenThroughputHeatmap(from, to time.Time) ([]TokenThroughputCell, error) {
	rows, err := q.query(fmt.Sprintf(`
		SELECT
		  %s AS hour,
		  %s AS dow,
		  COALESCE(SUM(total_tokens), 0) AS total_tok
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY hour, dow
	`, q.sqlHour("created_at"), q.sqlDow("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query token throughput heatmap: %w", err)
	}
	defer rows.Close()

	var result []TokenThroughputCell
	for rows.Next() {
		var c TokenThroughputCell
		if err := rows.Scan(&c.Hour, &c.Day, &c.Value); err != nil {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryUpstreamShare — traffic share by upstream URL
// ---------------------------------------------------------------------------

func (q *Querier) QueryUpstreamShare(from, to time.Time) ([]UpstreamShareRow, error) {
	rows, err := q.query(`
		SELECT upstream_url, COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND upstream_url IS NOT NULL AND upstream_url != ''
		GROUP BY upstream_url
		ORDER BY cnt DESC
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query upstream share: %w", err)
	}
	defer rows.Close()

	var result []UpstreamShareRow
	for rows.Next() {
		var r UpstreamShareRow
		if err := rows.Scan(&r.URL, &r.Requests); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryUpstreamLatencyTrend — per-upstream daily avg latency
// ---------------------------------------------------------------------------

func (q *Querier) QueryUpstreamLatencyTrend(from, to time.Time) ([]UpstreamLatencyTrendRow, error) {
	rows, err := q.query(fmt.Sprintf(`
		SELECT upstream_url, %s AS day, AVG(duration_ms) AS avg_lat
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND upstream_url IS NOT NULL AND upstream_url != ''
		  AND duration_ms IS NOT NULL
		GROUP BY upstream_url, day
		ORDER BY day, upstream_url
	`, q.sqlDate("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query upstream latency trend: %w", err)
	}
	defer rows.Close()

	// Count requests per URL first to limit to top 8
	urlCounts := make(map[string]int)
	var result []UpstreamLatencyTrendRow
	for rows.Next() {
		var r UpstreamLatencyTrendRow
		if err := rows.Scan(&r.URL, &r.Date, &r.AvgLatency); err != nil {
			continue
		}
		urlCounts[r.URL]++
		result = append(result, r)
	}

	// If more than 8 distinct URLs, keep only the 8 most frequent
	if len(urlCounts) > 8 {
		type urlCount struct {
			url   string
			count int
		}
		var sorted []urlCount
		for u, c := range urlCounts {
			sorted = append(sorted, urlCount{u, c})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
		topURLs := make(map[string]bool)
		for i := 0; i < 8 && i < len(sorted); i++ {
			topURLs[sorted[i].url] = true
		}
		filtered := result[:0]
		for _, r := range result {
			if topURLs[r.URL] {
				filtered = append(filtered, r)
			}
		}
		result = filtered
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryCostPerTokenTrend — daily cost/token ratio
// ---------------------------------------------------------------------------

func (q *Querier) QueryCostPerTokenTrend(from, to time.Time) ([]CostPerTokenRow, error) {
	rows, err := q.query(fmt.Sprintf(`
		SELECT %s AS day,
		       CASE WHEN SUM(total_tokens) > 0
		            THEN SUM(cost_usd) / SUM(total_tokens)
		            ELSE 0 END AS cpt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY day
		ORDER BY day
	`, q.sqlDate("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query cost per token trend: %w", err)
	}
	defer rows.Close()

	var result []CostPerTokenRow
	for rows.Next() {
		var r CostPerTokenRow
		if err := rows.Scan(&r.Date, &r.CostPerToken); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryIORatioTrend — daily avg input/output ratio
// ---------------------------------------------------------------------------

func (q *Querier) QueryIORatioTrend(from, to time.Time) ([]IORatioTrendRow, error) {
	rows, err := q.query(fmt.Sprintf(`
		SELECT %s AS day,
		       AVG(CAST(input_tokens AS REAL) / NULLIF(output_tokens, 0)) AS io_ratio
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND output_tokens > 0
		GROUP BY day
		ORDER BY day
	`, q.sqlDate("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query io ratio trend: %w", err)
	}
	defer rows.Close()

	var result []IORatioTrendRow
	for rows.Next() {
		var r IORatioTrendRow
		if err := rows.Scan(&r.Date, &r.IORatio); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryPeakRPM — max requests per minute in the period
// ---------------------------------------------------------------------------

// QueryPeakRPM returns the maximum requests-per-minute in the period.
func (q *Querier) QueryPeakRPM(from, to time.Time) (int64, error) {
	row := q.queryRow(fmt.Sprintf(`
		SELECT COALESCE(MAX(cnt), 0) FROM (
		  SELECT COUNT(*) AS cnt
		  FROM usage_logs
		  WHERE created_at >= ? AND created_at < ?
		  GROUP BY %s
		)
	`, q.sqlMinuteGroup("created_at")), from, to)
	var v int64
	if err := row.Scan(&v); err != nil {
		return 0, fmt.Errorf("query peak rpm: %w", err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryModelDailyTrend — per-model daily request counts and tokens
// ---------------------------------------------------------------------------

// QueryModelDailyTrend returns per-model daily request counts and tokens.
func (q *Querier) QueryModelDailyTrend(from, to time.Time) ([]ModelDailyRow, error) {
	rows, err := q.query(fmt.Sprintf(`
		SELECT %s AS day, model, COUNT(*) AS cnt, COALESCE(SUM(total_tokens),0) AS tok
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND model IS NOT NULL AND model != ''
		GROUP BY day, model
		ORDER BY day, model
	`, q.sqlDate("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query model daily trend: %w", err)
	}
	defer rows.Close()

	var result []ModelDailyRow
	for rows.Next() {
		var r ModelDailyRow
		if err := rows.Scan(&r.Date, &r.Model, &r.Count, &r.Tokens); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryModelTokenBoxPlots — per-model input or output token box plot
// column must be "input_tokens" or "output_tokens"
// ---------------------------------------------------------------------------

func (q *Querier) QueryModelTokenBoxPlots(from, to time.Time, column string) ([]TokenBoxPlotRow, error) {
	if column != "input_tokens" && column != "output_tokens" {
		return nil, fmt.Errorf("invalid column: %s", column)
	}

	// Get distinct models
	mrows, err := q.db.Query(`
		SELECT DISTINCT model FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND model IS NOT NULL AND model != ''
		ORDER BY model
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query models: %w", err)
	}
	var models []string
	for mrows.Next() {
		var m string
		if err := mrows.Scan(&m); err != nil {
			continue
		}
		models = append(models, m)
	}
	mrows.Close()

	var result []TokenBoxPlotRow
	for _, model := range models {
		// Fetch all values for this model (sorted for percentile computation)
		vrows, err := q.db.Query(fmt.Sprintf(`
			SELECT %s FROM usage_logs
			WHERE created_at >= ? AND created_at < ?
			  AND model = ?
			  AND %s IS NOT NULL AND %s > 0
			ORDER BY %s
		`, column, column, column, column), from, to, model)
		if err != nil {
			continue
		}
		var vals []int64
		for vrows.Next() {
			var v int64
			if err := vrows.Scan(&v); err != nil {
				continue
			}
			vals = append(vals, v)
		}
		vrows.Close()

		if len(vals) < 4 {
			continue
		}

		result = append(result, TokenBoxPlotRow{
			Model:  model,
			Min:    vals[0],
			Q1:     percentile(vals, 25),
			Median: percentile(vals, 50),
			Q3:     percentile(vals, 75),
			Max:    vals[len(vals)-1],
			Count:  len(vals),
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QuerySourceNodeDist — traffic by source_node
// ---------------------------------------------------------------------------

func (q *Querier) QuerySourceNodeDist(from, to time.Time) ([]SourceNodeRow, error) {
	rows, err := q.query(`
		SELECT COALESCE(source_node, 'unknown'), COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY source_node
		ORDER BY cnt DESC
	`, from, to)
	if err != nil {
		// Column may not exist in older schemas — return empty silently
		return nil, nil
	}
	defer rows.Close()

	var result []SourceNodeRow
	for rows.Next() {
		var r SourceNodeRow
		if err := rows.Scan(&r.SourceNode, &r.Requests); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 8: QueryStreamingBoxPlot — streaming vs non-streaming latency box plots
// ---------------------------------------------------------------------------

func (q *Querier) QueryStreamingBoxPlot(from, to time.Time) ([]StreamingBoxPlotRow, error) {
	rows, err := q.query(`
		SELECT is_streaming, duration_ms
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND duration_ms IS NOT NULL
		ORDER BY is_streaming, duration_ms
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query streaming box plot: %w", err)
	}
	defer rows.Close()

	streaming := make([]int64, 0)
	nonStreaming := make([]int64, 0)

	for rows.Next() {
		var isStreaming bool
		var dur int64
		if err := rows.Scan(&isStreaming, &dur); err != nil {
			continue
		}
		if isStreaming {
			streaming = append(streaming, dur)
		} else {
			nonStreaming = append(nonStreaming, dur)
		}
	}

	var result []StreamingBoxPlotRow
	for _, pair := range []struct {
		label string
		vals  []int64
	}{
		{"流式", streaming},
		{"非流式", nonStreaming},
	} {
		if len(pair.vals) < 4 {
			continue
		}
		sort.Slice(pair.vals, func(i, j int) bool { return pair.vals[i] < pair.vals[j] })
		result = append(result, StreamingBoxPlotRow{
			Label:  pair.label,
			Min:    pair.vals[0],
			Q1:     percentile(pair.vals, 25),
			Median: percentile(pair.vals, 50),
			Q3:     percentile(pair.vals, 75),
			Max:    pair.vals[len(pair.vals)-1],
			Count:  len(pair.vals),
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 9: QueryUserTierDist — classify users into 4 activity tiers
// ---------------------------------------------------------------------------

func (q *Querier) QueryUserTierDist(from, to time.Time) ([]UserTierRow, error) {
	// Get per-user request counts and token totals in period
	rows, err := q.query(`
		SELECT user_id, COUNT(*) AS reqs, COALESCE(SUM(total_tokens),0) AS tok
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query user tier dist: %w", err)
	}
	defer rows.Close()

	type userStat struct {
		reqs int64
		tok  int64
	}
	var users []userStat
	var totalTokens int64
	for rows.Next() {
		var uid string
		var s userStat
		if err := rows.Scan(&uid, &s.reqs, &s.tok); err != nil {
			continue
		}
		users = append(users, s)
		totalTokens += s.tok
	}
	if len(users) == 0 {
		return nil, nil
	}

	// Sort by reqs descending to find tier boundaries
	sort.Slice(users, func(i, j int) bool { return users[i].reqs > users[j].reqs })

	// Tier thresholds (based on request count quartiles):
	// Top 10%  → 超级用户, Next 25% → 活跃用户, Next 40% → 普通用户, Bottom 25% → 非活跃
	n := len(users)
	superEnd := max1(1, n/10)
	activeEnd := max1(superEnd+1, n*35/100)
	normalEnd := max1(activeEnd+1, n*75/100)

	type tierDef struct {
		name     string
		from, to int // indices [from, to)
	}
	tiers := []tierDef{
		{"超级用户", 0, superEnd},
		{"活跃用户", superEnd, activeEnd},
		{"普通用户", activeEnd, normalEnd},
		{"非活跃", normalEnd, n},
	}

	var result []UserTierRow
	for _, td := range tiers {
		if td.from >= td.to {
			continue
		}
		var tierTok int64
		minReqs, maxReqs := users[td.from].reqs, users[td.from].reqs
		for i := td.from; i < td.to; i++ {
			tierTok += users[i].tok
			if users[i].reqs < minReqs {
				minReqs = users[i].reqs
			}
			if users[i].reqs > maxReqs {
				maxReqs = users[i].reqs
			}
		}
		share := 0.0
		if totalTokens > 0 {
			share = float64(tierTok) / float64(totalTokens) * 100
		}
		result = append(result, UserTierRow{
			Tier:       td.name,
			UserCount:  td.to - td.from,
			TokenShare: share,
			MinReqs:    minReqs,
			MaxReqs:    maxReqs,
		})
	}
	return result, nil
}

func max1(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Phase 9: QueryUserTokenPercentiles — per-user token consumption percentiles
// ---------------------------------------------------------------------------

func (q *Querier) QueryUserTokenPercentiles(from, to time.Time) ([]UserTokenPercentileRow, error) {
	rows, err := q.query(`
		SELECT COALESCE(SUM(total_tokens),0) AS tok
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id
		ORDER BY tok
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query user token percentiles: %w", err)
	}
	defer rows.Close()

	var vals []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			continue
		}
		vals = append(vals, v)
	}
	if len(vals) == 0 {
		return nil, nil
	}

	pcts := []struct {
		label string
		p     float64
	}{
		{"P50", 50}, {"P75", 75}, {"P90", 90}, {"P95", 95}, {"P99", 99},
	}
	result := make([]UserTokenPercentileRow, 0, len(pcts))
	for _, p := range pcts {
		result = append(result, UserTokenPercentileRow{
			Percentile: p.label,
			Tokens:     percentile(vals, p.p),
		})
	}
	return result, nil
}
