package main

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 4: DAU/WAU/MAU Engagement Trend
// ---------------------------------------------------------------------------

// QueryEngagementTrend returns daily active users, weekly, and monthly active users per day.
func (q *Querier) QueryEngagementTrend(from, to time.Time) ([]EngagementTrendRow, error) {
	// Get all usage logs in date range
	rows, err := q.query(fmt.Sprintf(`
		SELECT DISTINCT %s AS day, user_id
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		ORDER BY day, user_id
	`, q.sqlDate("created_at")), from, to)
	if err != nil {
		return nil, fmt.Errorf("query engagement trend: %w", err)
	}
	defer rows.Close()

	// Map: date -> set of user_ids
	type dateUsers struct {
		date     string
		userIDs  map[string]bool
	}
	dateMap := make(map[string]map[string]bool)
	var allDates []string
	for rows.Next() {
		var dateStr, userID string
		if err := rows.Scan(&dateStr, &userID); err != nil {
			continue
		}
		if dateMap[dateStr] == nil {
			dateMap[dateStr] = make(map[string]bool)
			allDates = append(allDates, dateStr)
		}
		dateMap[dateStr][userID] = true
	}

	sort.Strings(allDates)

	var result []EngagementTrendRow
	for _, dateStr := range allDates {
		// DAU: active users on this day
		dau := len(dateMap[dateStr])

		// WAU: active users in this week (7 days ending on this date)
		wau := make(map[string]bool)
		currentDate, _ := time.Parse("2006-01-02", dateStr)
		for i := 0; i < 7; i++ {
			dayStr := currentDate.AddDate(0, 0, -i).Format("2006-01-02")
			for uid := range dateMap[dayStr] {
				wau[uid] = true
			}
		}

		// MAU: active users in this month (up to this date)
		mau := make(map[string]bool)
		monthStart := currentDate.AddDate(0, 0, -currentDate.Day()+1)
		for {
			if monthStart.Format("2006-01") != currentDate.Format("2006-01") {
				break
			}
			dayStr := monthStart.Format("2006-01-02")
			for uid := range dateMap[dayStr] {
				mau[uid] = true
			}
			monthStart = monthStart.AddDate(0, 0, 1)
		}

		result = append(result, EngagementTrendRow{
			Date: dateStr,
			DAU:  dau,
			WAU:  len(wau),
			MAU:  len(mau),
		})
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 4: Quota Usage Per User
// ---------------------------------------------------------------------------

// QueryQuotaUsage returns quota usage per user (requires daily_limit, monthly_limit in users table).
func (q *Querier) QueryQuotaUsage(from, to time.Time) ([]QuotaUsageRow, error) {
	// Check if users table has daily_limit and monthly_limit columns
	// If not, this query will fail gracefully (best-effort)
	rows, err := q.query(fmt.Sprintf(`
		SELECT
			u.id,
			u.username,
			COALESCE(u.daily_limit, 0) AS daily_limit,
			COALESCE(u.monthly_limit, 0) AS monthly_limit,
			COALESCE(SUM(CASE WHEN %s = %s THEN ul.total_tokens ELSE 0 END), 0) AS daily_used,
			COALESCE(SUM(CASE WHEN %s = %s THEN ul.total_tokens ELSE 0 END), 0) AS monthly_used
		FROM users u
		LEFT JOIN usage_logs ul ON CAST(u.id AS TEXT) = ul.user_id AND ul.created_at >= ? AND ul.created_at < ?
		WHERE u.is_active = TRUE
		GROUP BY u.id, u.username, u.daily_limit, u.monthly_limit
		ORDER BY monthly_used DESC, u.username ASC
	`, q.sqlDate("ul.created_at"), q.sqlCurrentDate(),
		q.sqlYearMonth("ul.created_at"), q.sqlCurrentYearMonth()), from, to)
	if err != nil {
		// If columns don't exist, return empty (graceful degradation)
		return nil, nil
	}
	defer rows.Close()

	var result []QuotaUsageRow
	for rows.Next() {
		var userID, username string
		var dailyLimit, monthlyLimit, dailyUsed, monthlyUsed int64
		if err := rows.Scan(&userID, &username, &dailyLimit, &monthlyLimit, &dailyUsed, &monthlyUsed); err != nil {
			continue
		}

		dailyPercent := 0.0
		monthlyPercent := 0.0
		if dailyLimit > 0 {
			dailyPercent = math.Round(float64(dailyUsed)/float64(dailyLimit)*10000) / 100
		}
		if monthlyLimit > 0 {
			monthlyPercent = math.Round(float64(monthlyUsed)/float64(monthlyLimit)*10000) / 100
		}

		result = append(result, QuotaUsageRow{
			UserID:              userID,
			Username:            username,
			DailyLimit:          dailyLimit,
			MonthlyLimit:        monthlyLimit,
			DailyUsed:           dailyUsed,
			MonthlyUsed:         monthlyUsed,
			DailyUsagePercent:   dailyPercent,
			MonthlyUsagePercent: monthlyPercent,
		})
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 4 Medium: Latency Box Plot by Upstream
// ---------------------------------------------------------------------------

// QueryLatencyBoxPlotByUpstream returns latency distribution by upstream endpoint.
func (q *Querier) QueryLatencyBoxPlotByUpstream(from, to time.Time) ([]LatencyBoxPlotRow, error) {
	rows, err := q.query(`
		SELECT COALESCE(lt.name, ul.upstream_url, '未知上游') AS upstream, ul.duration_ms
		FROM usage_logs ul
		LEFT JOIN llm_targets lt ON lt.url = ul.upstream_url
		WHERE ul.created_at >= ? AND ul.created_at < ?
		  AND ul.status_code IN (200, 201, 204)
		ORDER BY upstream, ul.duration_ms
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query latency by upstream: %w", err)
	}
	defer rows.Close()

	upstreamMap := make(map[string][]int64)
	for rows.Next() {
		var upstream string
		var duration int64
		if err := rows.Scan(&upstream, &duration); err != nil {
			continue
		}
		upstreamMap[upstream] = append(upstreamMap[upstream], duration)
	}

	var result []LatencyBoxPlotRow
	for upstream, durations := range upstreamMap {
		if len(durations) == 0 {
			continue
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

		boxplot := LatencyBoxPlotRow{
			Model:  upstream, // reuse Model field for upstream URL
			Min:    durations[0],
			Max:    durations[len(durations)-1],
			Median: percentile(durations, 50),
			Q1:     percentile(durations, 25),
			Q3:     percentile(durations, 75),
			Count:  len(durations),
		}
		boxplot.IQR = boxplot.Q3 - boxplot.Q1
		result = append(result, boxplot)
	}

	// Sort by median latency descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Median > result[j].Median
	})

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 4 Medium: Group Token Distribution Box Plot
// ---------------------------------------------------------------------------

// QueryGroupTokenDistribution returns token distribution per user within each group.
type GroupTokenDistribution struct {
	GroupID string              `json:"group_id"`
	GroupName string            `json:"group_name"`
	Min     int64              `json:"min"`
	Q1      int64              `json:"q1"`
	Median  int64              `json:"median"`
	Q3      int64              `json:"q3"`
	Max     int64              `json:"max"`
	IQR     int64              `json:"iqr"`
	Count   int                `json:"count"` // number of users in group
}

func (q *Querier) QueryGroupTokenDistribution(from, to time.Time) ([]GroupTokenDistribution, error) {
	// Get total tokens per user per group
	rows, err := q.query(`
		SELECT g.id, g.name, u.id, SUM(ul.total_tokens) as total
		FROM users u
		LEFT JOIN groups g ON u.group_id = g.id
		LEFT JOIN usage_logs ul ON CAST(u.id AS TEXT) = ul.user_id AND ul.created_at >= ? AND ul.created_at < ?
		WHERE u.is_active = TRUE
		GROUP BY g.id, g.name, u.id
		ORDER BY g.id, total DESC
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query group token distribution: %w", err)
	}
	defer rows.Close()

	type groupUsers struct {
		groupID    string
		groupName  string
		userTokens []int64
	}
	groupMap := make(map[string]*groupUsers)

	for rows.Next() {
		var groupID, groupName, userID string
		var total sql.NullInt64
		if err := rows.Scan(&groupID, &groupName, &userID, &total); err != nil {
			continue
		}

		if groupMap[groupID] == nil {
			groupMap[groupID] = &groupUsers{
				groupID:    groupID,
				groupName:  groupName,
				userTokens: []int64{},
			}
		}

		if total.Valid {
			groupMap[groupID].userTokens = append(groupMap[groupID].userTokens, total.Int64)
		}
	}

	var result []GroupTokenDistribution
	for _, gu := range groupMap {
		if len(gu.userTokens) == 0 {
			continue
		}
		sort.Slice(gu.userTokens, func(i, j int) bool { return gu.userTokens[i] < gu.userTokens[j] })

		gtd := GroupTokenDistribution{
			GroupID: gu.groupID,
			GroupName: gu.groupName,
			Min:     gu.userTokens[0],
			Max:     gu.userTokens[len(gu.userTokens)-1],
			Median:  percentile(gu.userTokens, 50),
			Q1:      percentile(gu.userTokens, 25),
			Q3:      percentile(gu.userTokens, 75),
			Count:   len(gu.userTokens),
		}
		gtd.IQR = gtd.Q3 - gtd.Q1
		result = append(result, gtd)
	}

	return result, nil
}
