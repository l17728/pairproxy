package main

import (
	"fmt"
	"math"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 6 Low: Model Capability Radar Chart
// ---------------------------------------------------------------------------

// QueryModelRadarData returns multi-dimensional performance metrics for models.
func (q *Querier) QueryModelRadarData(from, to time.Time) ([]ModelRadarData, error) {
	// Get base stats per model
	rows, err := q.query(`
		SELECT
			model,
			COUNT(*) as cnt,
			COALESCE(AVG(duration_ms), 0) as avg_lat,
			COALESCE(SUM(cost_usd), 0) as total_cost,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END) as errors,
			COUNT(DISTINCT user_id) as distinct_users
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY model
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query model radar: %w", err)
	}
	defer rows.Close()

	type modelStats struct {
		model         string
		count         int64
		avgLatency    float64
		totalCost     float64
		totalTokens   int64
		errors        int64
		distinctUsers int64
	}
	var stats []modelStats
	var totalRequests int64
	var totalUsers int64
	var maxLatency float64
	var minCostPerToken float64 = math.MaxFloat64

	for rows.Next() {
		var m modelStats
		if err := rows.Scan(&m.model, &m.count, &m.avgLatency, &m.totalCost, &m.totalTokens, &m.errors, &m.distinctUsers); err != nil {
			continue
		}
		stats = append(stats, m)
		totalRequests += m.count
		if m.distinctUsers > totalUsers {
			totalUsers = m.distinctUsers
		}

		if m.avgLatency > maxLatency {
			maxLatency = m.avgLatency
		}

		if m.totalTokens > 0 {
			costPerToken := m.totalCost / float64(m.totalTokens)
			if costPerToken < minCostPerToken {
				minCostPerToken = costPerToken
			}
		}
	}

	if len(stats) == 0 {
		return nil, nil
	}

	var result []ModelRadarData
	for _, ms := range stats {
		rd := ModelRadarData{Model: ms.model}

		// 1. Latency Score: 0-100 (lower is better)
		// invert: max latency = 0 score, min latency = 100 score
		if maxLatency > 0 {
			rd.LatencyScore = math.Round((1 - ms.avgLatency/maxLatency) * 100 * 10) / 10
		} else {
			rd.LatencyScore = 100
		}

		// 2. Cost Score: 0-100 (lower cost/token is better)
		if ms.totalTokens > 0 {
			costPerToken := ms.totalCost / float64(ms.totalTokens)
			if minCostPerToken > 0 {
				rd.CostScore = math.Round((1 - costPerToken/minCostPerToken + 1) / 2 * 100 * 10) / 10
				if rd.CostScore > 100 {
					rd.CostScore = 100
				}
			} else {
				rd.CostScore = 50
			}
		} else {
			rd.CostScore = 50
		}

		// 3. Throughput Score: 0-100 (more requests = higher score)
		if totalRequests > 0 {
			rd.ThroughputScore = math.Round(float64(ms.count) / float64(totalRequests) * 100 * 10) / 10
		}

		// 4. Reliability Score: 0-100 ((1 - error_rate) * 100)
		if ms.count > 0 {
			errorRate := float64(ms.errors) / float64(ms.count)
			rd.ReliabilityScore = math.Round((1 - errorRate) * 100 * 10) / 10
		}

		// 5. Adoption Score: distinct users using this model / max distinct users any model
		if totalUsers > 0 {
			rd.AdoptionScore = math.Round(float64(ms.distinctUsers) / float64(totalUsers) * 100 * 10) / 10
		}

		result = append(result, rd)
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 6 Low: Adoption Rate (active vs registered users)
// ---------------------------------------------------------------------------

// QueryActiveUsersInPeriod returns count of users with activity in period.
func (q *Querier) QueryActiveUsersInPeriod(from, to time.Time) (int, error) {
	var count int
	row := q.queryRow(`
		SELECT COUNT(DISTINCT user_id) FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
	`, from, to)
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("query active users: %w", err)
	}
	return count, nil
}
