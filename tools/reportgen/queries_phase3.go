package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 3: Retention Cohort Analysis
// ---------------------------------------------------------------------------

// QueryRetentionData returns retention cohort analysis (simplified: daily active by first-use week).
func (q *Querier) QueryRetentionData(from, to time.Time) ([]RetentionRow, error) {
	// Get first use date for each user in the period
	userFirstUseRows, err := q.query(fmt.Sprintf(`
		SELECT user_id, %s AS first_date
		FROM usage_logs
		WHERE created_at < ?
		GROUP BY user_id
	`, q.sqlDate("MIN(created_at)")), to)
	if err != nil {
		return nil, fmt.Errorf("query user first use: %w", err)
	}
	defer userFirstUseRows.Close()

	type userCohort struct {
		userID       string
		firstUseDate string
	}
	var userCohorts []userCohort
	for userFirstUseRows.Next() {
		var uid, fdate string
		if err := userFirstUseRows.Scan(&uid, &fdate); err != nil {
			continue
		}
		userCohorts = append(userCohorts, userCohort{uid, fdate})
	}

	// For each cohort, count active users by days since birth
	cohortMap := make(map[string]map[int]int) // cohort_date -> {days_since_birth: count}
	for _, uc := range userCohorts {
		if cohortMap[uc.firstUseDate] == nil {
			cohortMap[uc.firstUseDate] = make(map[int]int)
		}

		// Count active days for this user within the period
		activeRows, _ := q.query(fmt.Sprintf(`
			SELECT DISTINCT %s FROM usage_logs
			WHERE user_id = ? AND created_at >= ? AND created_at < ?
		`, q.sqlDate("created_at")), uc.userID, from, to)
		if activeRows != nil {
			for activeRows.Next() {
				var dateStr string
				if err := activeRows.Scan(&dateStr); err == nil {
					// Calculate days since birth
					cohortTime, _ := time.Parse("2006-01-02", uc.firstUseDate)
					activeDayTime, _ := time.Parse("2006-01-02", dateStr)
					daysSince := int(activeDayTime.Sub(cohortTime).Hours() / 24)
					if daysSince >= 0 && daysSince <= 30 {
						cohortMap[uc.firstUseDate][daysSince]++
					}
				}
			}
			activeRows.Close()
		}
	}

	// Get initial cohort sizes
	cohortSizes := make(map[string]int)
	for _, uc := range userCohorts {
		cohortSizes[uc.firstUseDate]++
	}

	var result []RetentionRow
	for cohortDate := range cohortMap {
		cohortSize := cohortSizes[cohortDate]
		for daysSince := 0; daysSince <= 30; daysSince++ {
			activeCount := cohortMap[cohortDate][daysSince]
			retentionRate := 0.0
			if cohortSize > 0 {
				retentionRate = float64(activeCount) / float64(cohortSize) * 100
			}
			result = append(result, RetentionRow{
				FirstUseDate:   cohortDate,
				DaysSinceBirth: daysSince,
				ActiveUsers:    activeCount,
				RetentionRate:  math.Round(retentionRate*10) / 10,
			})
		}
	}

	// Sort by cohort date then days since birth
	sort.Slice(result, func(i, j int) bool {
		if result[i].FirstUseDate != result[j].FirstUseDate {
			return result[i].FirstUseDate < result[j].FirstUseDate
		}
		return result[i].DaysSinceBirth < result[j].DaysSinceBirth
	})

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 3: Input/Output Scatter Plot
// ---------------------------------------------------------------------------

// QueryIOScatterPlot returns sampled input/output token pairs (limit to 1000 to avoid huge JSON).
func (q *Querier) QueryIOScatterPlot(from, to time.Time, limit int) ([]IOScatterPoint, error) {
	rows, err := q.query(`
		SELECT input_tokens, output_tokens FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		ORDER BY RANDOM()
		LIMIT ?
	`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query io scatter: %w", err)
	}
	defer rows.Close()

	var result []IOScatterPoint
	for rows.Next() {
		var inp, out int64
		if err := rows.Scan(&inp, &out); err != nil {
			continue
		}
		result = append(result, IOScatterPoint{InputTokens: inp, OutputTokens: out})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 3: Model Cost Breakdown
// ---------------------------------------------------------------------------

// QueryModelCostBreakdown returns cost distribution by model.
func (q *Querier) QueryModelCostBreakdown(from, to time.Time) ([]ModelCostRow, error) {
	rows, err := q.query(`
		SELECT model, COALESCE(SUM(cost_usd), 0), COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY model
		ORDER BY SUM(cost_usd) DESC
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query model cost: %w", err)
	}
	defer rows.Close()

	var results []ModelCostRow
	var totalCost float64
	for rows.Next() {
		var model string
		var cost float64
		var count int64
		var tokens int64
		if err := rows.Scan(&model, &cost, &count, &tokens); err != nil {
			continue
		}
		results = append(results, ModelCostRow{
			Model:    model,
			CostUSD:  math.Round(cost*100) / 100,
			Requests: count,
		})
		totalCost += cost
	}

	// Calculate percentages
	for i := range results {
		if totalCost > 0 {
			results[i].CostPercent = math.Round(results[i].CostUSD/totalCost*10000) / 100
		}
	}

	return results, nil
}
