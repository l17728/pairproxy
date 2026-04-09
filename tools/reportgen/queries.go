package main

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Querier wraps a database connection and provides all report query methods.
type Querier struct {
	db           *sql.DB
	driver       string // "sqlite" | "postgres"
	userMap      map[string]string // user_id -> username
	userGroupMap map[string]string // user_id -> group_id
	groupNameMap map[string]string // group_id -> group_name
}

// NewQuerier opens the database (SQLite or PostgreSQL) and loads user/group lookup maps.
// For SQLite, dsn is the file path. For PostgreSQL, dsn is the connection string.
func NewQuerier(driver, dsn string) (*Querier, error) {
	if driver == "" {
		driver = "sqlite"
	}

	var driverName string
	switch driver {
	case "postgres":
		driverName = "postgres"
	default:
		driverName = "sqlite"
		driver = "sqlite"
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// SQLite only: Enable WAL mode for better read performance
	if driver == "sqlite" {
		_, _ = db.Exec("PRAGMA journal_mode=WAL")
	}

	q := &Querier{db: db, driver: driver}
	if err := q.loadMaps(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load maps: %w", err)
	}
	return q, nil
}

// rebind replaces ? placeholders with $1, $2, … for PostgreSQL.
// For SQLite, returns the query unchanged.
func (q *Querier) rebind(query string) string {
	if q.driver != "postgres" {
		return query
	}
	n := 0
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteByte(query[i])
		}
	}
	return b.String()
}

// sqlDate returns a SQL expression that casts created_at to a date string 'YYYY-MM-DD'.
func (q *Querier) sqlDate(col string) string {
	if q.driver == "postgres" {
		return "TO_CHAR(" + col + ", 'YYYY-MM-DD')"
	}
	return "DATE(" + col + ")"
}

// sqlHour returns a SQL expression extracting the hour (0-23) from a timestamp column.
func (q *Querier) sqlHour(col string) string {
	if q.driver == "postgres" {
		return "EXTRACT(HOUR FROM " + col + ")::INTEGER"
	}
	return "CAST(strftime('%H', " + col + ") AS INTEGER)"
}

// sqlDow returns a SQL expression extracting the day-of-week (0=Sunday … 6=Saturday).
func (q *Querier) sqlDow(col string) string {
	if q.driver == "postgres" {
		return "EXTRACT(DOW FROM " + col + ")::INTEGER"
	}
	return "CAST(strftime('%w', " + col + ") AS INTEGER)"
}

// sqlYearMonth returns a SQL expression returning 'YYYY-MM' for a timestamp column.
func (q *Querier) sqlYearMonth(col string) string {
	if q.driver == "postgres" {
		return "TO_CHAR(" + col + ", 'YYYY-MM')"
	}
	return "strftime('%Y-%m', " + col + ")"
}

// sqlCurrentDate returns a SQL expression for the current date (driver-specific).
func (q *Querier) sqlCurrentDate() string {
	if q.driver == "postgres" {
		return "CURRENT_DATE"
	}
	return "DATE('now')"
}

// sqlCurrentYearMonth returns a SQL expression for the current year-month string.
func (q *Querier) sqlCurrentYearMonth() string {
	if q.driver == "postgres" {
		return "TO_CHAR(NOW(), 'YYYY-MM')"
	}
	return "strftime('%Y-%m', 'now')"
}

// sqlMinuteGroup returns a SQL expression grouping a timestamp by minute ('YYYY-MM-DD HH:MM').
func (q *Querier) sqlMinuteGroup(col string) string {
	if q.driver == "postgres" {
		return "TO_CHAR(DATE_TRUNC('minute', " + col + "), 'YYYY-MM-DD HH24:MI')"
	}
	return "strftime('%Y-%m-%d %H:%M', " + col + ")"
}

// Close closes the underlying database connection.
func (q *Querier) Close() error {
	return q.db.Close()
}

// query is a convenience wrapper that rebbinds ? placeholders before executing.
func (q *Querier) query(sql string, args ...interface{}) (*sql.Rows, error) {
	return q.db.Query(q.rebind(sql), args...)
}

// queryRow is a convenience wrapper that rebinds ? placeholders before executing.
func (q *Querier) queryRow(sql string, args ...interface{}) *sql.Row {
	return q.db.QueryRow(q.rebind(sql), args...)
}

func (q *Querier) loadMaps() error {
	q.userMap = make(map[string]string)
	q.userGroupMap = make(map[string]string)
	q.groupNameMap = make(map[string]string)

	// Load groups
	grows, err := q.db.Query("SELECT id, name FROM groups")
	if err != nil {
		return fmt.Errorf("query groups: %w", err)
	}
	defer grows.Close()
	for grows.Next() {
		var id, name string
		if err := grows.Scan(&id, &name); err != nil {
			continue
		}
		q.groupNameMap[id] = name
	}

	// Load users
	urows, err := q.db.Query("SELECT id, username, group_id FROM users")
	if err != nil {
		return fmt.Errorf("query users: %w", err)
	}
	defer urows.Close()
	for urows.Next() {
		var id, username string
		var gid sql.NullString
		if err := urows.Scan(&id, &username, &gid); err != nil {
			continue
		}
		q.userMap[id] = username
		if gid.Valid {
			q.userGroupMap[id] = gid.String
		}
	}
	return nil
}

// username resolves user_id to username, falling back to the raw ID.
func (q *Querier) username(uid string) string {
	if name, ok := q.userMap[uid]; ok {
		return name
	}
	return uid
}

// groupName resolves user_id to its group name.
func (q *Querier) groupName(uid string) string {
	gid := q.userGroupMap[uid]
	return q.groupNameMap[gid]
}

// CountRegisteredUsers returns the number of active registered users.
func (q *Querier) CountRegisteredUsers() int {
	var n int
	row := q.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_active = 1")
	if err := row.Scan(&n); err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// KPI
// ---------------------------------------------------------------------------

// QueryKPI computes the key performance indicators for the period and the previous period.
func (q *Querier) QueryKPI(from, to time.Time) (KPIData, error) {
	var k KPIData

	row := q.queryRow(`
		SELECT
			COUNT(*)                                    AS total_requests,
			COALESCE(SUM(input_tokens), 0)               AS total_input,
			COALESCE(SUM(output_tokens), 0)              AS total_output,
			COALESCE(SUM(total_tokens), 0)               AS total_tokens,
			COALESCE(SUM(cost_usd), 0)                   AS total_cost,
			COUNT(DISTINCT user_id)                      AS active_users,
			COALESCE(SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END), 0) AS error_count,
			COALESCE(AVG(CASE WHEN status_code IN (200,201,204) THEN duration_ms END), 0) AS avg_latency,
			COALESCE(SUM(CASE WHEN is_streaming = 1 THEN 1 ELSE 0 END), 0) AS stream_cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?`, from, to)

	var streamCnt int64
	if err := row.Scan(&k.TotalRequests, &k.TotalInput, &k.TotalOutput, &k.TotalTokens,
		&k.TotalCost, &k.ActiveUsers, &k.ErrorCount, &k.AvgLatencyMs, &streamCnt); err != nil {
		return k, fmt.Errorf("query kpi: %w", err)
	}

	if k.TotalRequests > 0 {
		k.ErrorRate = float64(k.ErrorCount) / float64(k.TotalRequests) * 100
		k.StreamingPct = float64(streamCnt) / float64(k.TotalRequests) * 100
	}
	k.RegisteredUsers = q.CountRegisteredUsers()

	// P95 / P99 latency (computed in Go)
	durations := q.queryDurations(from, to)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	k.P95LatencyMs = percentile(durations, 95)
	k.P99LatencyMs = percentile(durations, 99)

	// Previous period for change rates
	pf, pt := QueryParams{From: from, To: to}.PrevPeriod()
	var prevReqs int64
	var prevTokens, prevCost float64
	var prevUsers int
	var prevErrors int64
	var prevLatency float64

	prow := q.queryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0),
			COUNT(DISTINCT user_id),
			COALESCE(SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN status_code IN (200,201,204) THEN duration_ms END),0)
		FROM usage_logs WHERE created_at >= ? AND created_at < ?`, pf, pt)
	_ = prow.Scan(&prevReqs, &prevTokens, &prevCost, &prevUsers, &prevErrors, &prevLatency)

	k.PrevTotalRequests = prevReqs
	k.RequestsChange = changeRate(float64(k.TotalRequests), float64(prevReqs))
	k.TokensChange = changeRate(float64(k.TotalTokens), prevTokens)
	k.CostChange = changeRate(k.TotalCost, prevCost)
	k.UsersChange = changeRate(float64(k.ActiveUsers), float64(prevUsers))
	if prevReqs > 0 {
		prevErrRate := float64(prevErrors) / float64(prevReqs) * 100
		k.ErrorRateChange = changeRate(k.ErrorRate, prevErrRate)
	}
	k.LatencyChange = changeRate(k.AvgLatencyMs, prevLatency)

	return k, nil
}

func (q *Querier) queryDurations(from, to time.Time) []int64 {
	rows, err := q.query(
		"SELECT duration_ms FROM usage_logs WHERE created_at >= ? AND created_at < ? AND status_code IN (200,201,204)",
		from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ds []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err == nil {
			ds = append(ds, d)
		}
	}
	return ds
}

// ---------------------------------------------------------------------------
// Daily Trend
// ---------------------------------------------------------------------------

// QueryDailyTrend returns per-day aggregates.
func (q *Querier) QueryDailyTrend(from, to time.Time) ([]DailyRow, error) {
	query := q.rebind(fmt.Sprintf(`
		SELECT
			%s AS day,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0),
			COUNT(*),
			COUNT(DISTINCT user_id),
			COALESCE(SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN status_code IN (200,201,204) THEN duration_ms END),0)
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY day ORDER BY day`, q.sqlDate("created_at")))
	rows, err := q.db.Query(query, from, to)
	if err != nil {
		return nil, fmt.Errorf("query daily trend: %w", err)
	}
	defer rows.Close()

	var result []DailyRow
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(&r.Date, &r.InputTokens, &r.OutputTokens, &r.TotalTokens,
			&r.CostUSD, &r.Requests, &r.ActiveUsers, &r.Errors, &r.AvgLatencyMs); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Heatmap
// ---------------------------------------------------------------------------

// QueryHeatmap returns request counts grouped by hour (0-23) and day-of-week (0-6).
func (q *Querier) QueryHeatmap(from, to time.Time) ([]HeatmapCell, error) {
	query := q.rebind(fmt.Sprintf(`
		SELECT
			%s AS hour,
			%s AS dow,
			COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY hour, dow`, q.sqlHour("created_at"), q.sqlDow("created_at")))
	rows, err := q.db.Query(query, from, to)
	if err != nil {
		return nil, fmt.Errorf("query heatmap: %w", err)
	}
	defer rows.Close()

	var result []HeatmapCell
	for rows.Next() {
		var c HeatmapCell
		if err := rows.Scan(&c.Hour, &c.Day, &c.Value); err != nil {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Top Users
// ---------------------------------------------------------------------------

// QueryTopUsers returns the top N users ordered by "tokens", "cost", or "requests".
func (q *Querier) QueryTopUsers(from, to time.Time, orderBy string, limit int) ([]TopUserRow, error) {
	var orderExpr string
	switch orderBy {
	case "cost":
		orderExpr = "SUM(ul.cost_usd) DESC"
	case "requests":
		orderExpr = "COUNT(*) DESC"
	default:
		orderExpr = "SUM(ul.total_tokens) DESC"
	}

	query := fmt.Sprintf(`
		SELECT
			ul.user_id,
			COALESCE(SUM(ul.total_tokens),0) AS total_t,
			COALESCE(SUM(ul.cost_usd),0)     AS total_c,
			COUNT(*)                          AS reqs,
			COALESCE(SUM(ul.input_tokens),0),
			COALESCE(SUM(ul.output_tokens),0)
		FROM usage_logs ul
		WHERE ul.created_at >= ? AND ul.created_at < ?
		GROUP BY ul.user_id
		ORDER BY %s
		LIMIT ?`, orderExpr)

	rows, err := q.db.Query(query, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query top users: %w", err)
	}
	defer rows.Close()

	var result []TopUserRow
	for rows.Next() {
		var uid string
		var r TopUserRow
		if err := rows.Scan(&uid, &r.Value, &r.CostUSD, &r.Requests, &r.InputTokens, &r.OutputTokens); err != nil {
			continue
		}
		r.Username = q.username(uid)
		r.GroupName = q.groupName(uid)
		// For cost ordering, swap Value
		if orderBy == "cost" {
			r.Value = r.CostUSD
		} else if orderBy == "requests" {
			r.Value = float64(r.Requests)
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Model Distribution
// ---------------------------------------------------------------------------

// QueryModelDistribution returns per-model aggregates.
// 优先用 llm_targets.name 做展示名称，若无匹配则直接使用 model 字段；
// 同一 upstream_url 的所有请求合并为一个 target 名，避免因客户端传入不同模型名导致分裂。
func (q *Querier) QueryModelDistribution(from, to time.Time) ([]ModelRow, error) {
	rows, err := q.query(`
		SELECT
			COALESCE(lt.name, ul.model, '未知模型')  AS model,
			COUNT(*)                                 AS cnt,
			COALESCE(SUM(ul.input_tokens),0),
			COALESCE(SUM(ul.output_tokens),0),
			COALESCE(SUM(ul.total_tokens),0),
			COALESCE(SUM(ul.cost_usd),0),
			COALESCE(AVG(CASE WHEN ul.status_code IN (200,201,204) THEN ul.duration_ms END),0),
			SUM(CASE WHEN ul.status_code NOT IN (200,201,204) THEN 1 ELSE 0 END)
		FROM usage_logs ul
		LEFT JOIN llm_targets lt ON lt.url = ul.upstream_url
		WHERE ul.created_at >= ? AND ul.created_at < ?
		GROUP BY COALESCE(lt.name, ul.model, '未知模型')
		ORDER BY cnt DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query model dist: %w", err)
	}
	defer rows.Close()

	var result []ModelRow
	for rows.Next() {
		var r ModelRow
		var errCount int64
		if err := rows.Scan(&r.Model, &r.Count, &r.InputTokens, &r.OutputTokens,
			&r.TotalTokens, &r.CostUSD, &r.AvgLatencyMs, &errCount); err != nil {
			continue
		}
		if r.Count > 0 {
			r.ErrorRate = float64(errCount) / float64(r.Count) * 100
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Group Comparison
// ---------------------------------------------------------------------------

// QueryGroupComparison returns per-group aggregates by joining users and groups.
func (q *Querier) QueryGroupComparison(from, to time.Time) ([]GroupRow, error) {
	rows, err := q.query(`
		SELECT
			u.group_id,
			COALESCE(SUM(ul.total_tokens),0)  AS total_t,
			COALESCE(SUM(ul.cost_usd),0)       AS total_c,
			COUNT(*)                            AS reqs,
			COALESCE(SUM(ul.input_tokens),0),
			COALESCE(SUM(ul.output_tokens),0),
			COUNT(DISTINCT ul.user_id)          AS active_users
		FROM usage_logs ul
		JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= ? AND ul.created_at < ?
		GROUP BY u.group_id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query group comparison: %w", err)
	}
	defer rows.Close()

	var result []GroupRow
	for rows.Next() {
		var r GroupRow
		if err := rows.Scan(&r.GroupID, &r.TotalTokens, &r.CostUSD, &r.Requests,
			&r.InputTokens, &r.OutputTokens, &r.ActiveUsers); err != nil {
			continue
		}
		r.GroupName = q.groupNameMap[r.GroupID]
		// Count total users in group
		var cnt int
		_ = q.queryRow("SELECT COUNT(*) FROM users WHERE group_id = ? AND is_active = 1", r.GroupID).Scan(&cnt)
		r.Users = cnt
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Upstream Stats
// ---------------------------------------------------------------------------

// QueryUpstreamStats returns per-upstream aggregates.
func (q *Querier) QueryUpstreamStats(from, to time.Time) ([]UpstreamRow, error) {
	rows, err := q.query(`
		SELECT
			upstream_url,
			COUNT(*)                                 AS cnt,
			COALESCE(AVG(CASE WHEN status_code IN (200,201,204) THEN duration_ms END),0),
			COALESCE(SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0)
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY upstream_url
		ORDER BY cnt DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query upstream: %w", err)
	}
	defer rows.Close()

	var result []UpstreamRow
	for rows.Next() {
		var r UpstreamRow
		var errCount int64
		if err := rows.Scan(&r.URL, &r.Requests, &r.AvgLatency, &errCount, &r.TotalTokens, &r.CostUSD); err != nil {
			continue
		}
		if r.Requests > 0 {
			r.ErrorRate = float64(errCount) / float64(r.Requests) * 100
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Status Code Distribution
// ---------------------------------------------------------------------------

// QueryStatusCodeDist returns request counts per HTTP status code.
func (q *Querier) QueryStatusCodeDist(from, to time.Time) ([]StatusCodeRow, error) {
	rows, err := q.query(`
		SELECT status_code, COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY status_code
		ORDER BY cnt DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query status codes: %w", err)
	}
	defer rows.Close()

	var result []StatusCodeRow
	for rows.Next() {
		var r StatusCodeRow
		if err := rows.Scan(&r.StatusCode, &r.Count); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Slow Requests
// ---------------------------------------------------------------------------

// QuerySlowRequests returns the slowest N requests.
func (q *Querier) QuerySlowRequests(from, to time.Time, limit int) ([]SlowRequestRow, error) {
	rows, err := q.query(`
		SELECT
			created_at, user_id, model,
			input_tokens, output_tokens, duration_ms, status_code,
			COALESCE(upstream_url,'')
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		ORDER BY duration_ms DESC
		LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query slow requests: %w", err)
	}
	defer rows.Close()

	var result []SlowRequestRow
	for rows.Next() {
		var r SlowRequestRow
		var uid string
		var createdAt time.Time
		if err := rows.Scan(&createdAt, &uid, &r.Model, &r.InputTokens, &r.OutputTokens,
			&r.DurationMs, &r.StatusCode, &r.UpstreamURL); err != nil {
			continue
		}
		r.CreatedAt = createdAt.Format("2006-01-02 15:04:05")
		r.Username = q.username(uid)
		result = append(result, r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Streaming Ratio
// ---------------------------------------------------------------------------

// QueryStreamingRatio compares streaming vs non-streaming request counts and latencies.
func (q *Querier) QueryStreamingRatio(from, to time.Time) (StreamingRatioData, error) {
	var s StreamingRatioData
	row := q.queryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN is_streaming = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN is_streaming = 0 OR is_streaming IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN is_streaming = 1 AND status_code IN (200,201,204) THEN duration_ms END),0),
			COALESCE(AVG(CASE WHEN (is_streaming = 0 OR is_streaming IS NULL) AND status_code IN (200,201,204) THEN duration_ms END),0)
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?`, from, to)

	if err := row.Scan(&s.StreamingCount, &s.NonStreamingCount,
		&s.StreamingAvgLatency, &s.NonStreamingAvgLatency); err != nil {
		return s, fmt.Errorf("query streaming ratio: %w", err)
	}

	total := s.StreamingCount + s.NonStreamingCount
	if total > 0 {
		s.StreamingPct = float64(s.StreamingCount) / float64(total) * 100
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Engagement
// ---------------------------------------------------------------------------

// QueryEngagement computes user engagement metrics.
func (q *Querier) QueryEngagement(from, to time.Time, registeredUsers int) (EngagementData, error) {
	var e EngagementData

	// DAU: average daily distinct users
	rows, err := q.db.Query(q.rebind(fmt.Sprintf(`
		SELECT %s, COUNT(DISTINCT user_id)
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY 1`, q.sqlDate("created_at"))), from, to)
	if err != nil {
		return e, fmt.Errorf("query engagement dau: %w", err)
	}
	defer rows.Close()

	var totalDAU int
	var dayCount int
	for rows.Next() {
		var dateStr string
		var cnt int
		if err := rows.Scan(&dateStr, &cnt); err != nil {
			continue
		}
		totalDAU += cnt
		dayCount++
	}
	if dayCount > 0 {
		e.DAU = totalDAU / dayCount
	}

	// WAU / MAU = distinct users in period
	_ = q.queryRow(`
		SELECT COUNT(DISTINCT user_id) FROM usage_logs
		WHERE created_at >= ? AND created_at < ?`, from, to).Scan(&e.WAU)
	e.MAU = e.WAU

	if e.MAU > 0 {
		e.Stickness = float64(e.DAU) / float64(e.MAU) * 100
	}
	if registeredUsers > 0 {
		e.AdoptionRate = float64(e.WAU) / float64(registeredUsers) * 100
		e.ZeroUseCount = registeredUsers - e.WAU
		if e.ZeroUseCount < 0 {
			e.ZeroUseCount = 0
		}
		e.ZeroUsePct = float64(e.ZeroUseCount) / float64(registeredUsers) * 100
	}

	// Power users: top 5% by request count
	prows, err := q.query(`
		SELECT user_id, COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id
		ORDER BY cnt DESC`, from, to)
	if err == nil {
		defer prows.Close()
		var allUsers []struct {
			uid string
			cnt int
		}
		for prows.Next() {
			var uid string
			var cnt int
			if err := prows.Scan(&uid, &cnt); err == nil {
				allUsers = append(allUsers, struct {
					uid string
					cnt int
				}{uid, cnt})
			}
		}
		cutoff := int(math.Ceil(float64(len(allUsers)) * 0.05))
		if cutoff < 1 {
			cutoff = 1
		}
		if cutoff > len(allUsers) {
			cutoff = len(allUsers)
		}
		for i := 0; i < cutoff; i++ {
			e.PowerUsers = append(e.PowerUsers, q.username(allUsers[i].uid))
		}
	}

	// New users this period
	_ = q.queryRow(`
		SELECT COUNT(*) FROM users
		WHERE created_at >= ? AND created_at < ? AND is_active = 1`, from, to).Scan(&e.NewUsersThisPeriod)

	return e, nil
}

// ---------------------------------------------------------------------------
// User Frequency Buckets
// ---------------------------------------------------------------------------

// QueryUserFreqBuckets buckets users by their request count.
func (q *Querier) QueryUserFreqBuckets(from, to time.Time) ([]HistogramBucket, error) {
	rows, err := q.query(`
		SELECT user_id, COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query user freq: %w", err)
	}
	defer rows.Close()

	// Initialize all buckets
	buckets := []HistogramBucket{
		{Range: "0", Count: 0},
		{Range: "1-5", Count: 0},
		{Range: "6-20", Count: 0},
		{Range: "21-50", Count: 0},
		{Range: "51-100", Count: 0},
		{Range: "100+", Count: 0},
	}

	for rows.Next() {
		var uid string
		var cnt int
		if err := rows.Scan(&uid, &cnt); err != nil {
			continue
		}
		switch {
		case cnt >= 100:
			buckets[5].Count++
		case cnt >= 51:
			buckets[4].Count++
		case cnt >= 21:
			buckets[3].Count++
		case cnt >= 6:
			buckets[2].Count++
		case cnt >= 1:
			buckets[1].Count++
		}
	}

	// Count zero-use users: registered but absent from the query
	activeUserCount := 0
	for _, b := range buckets {
		activeUserCount += b.Count
	}
	registered := q.CountRegisteredUsers()
	buckets[0].Count = registered - activeUserCount
	if buckets[0].Count < 0 {
		buckets[0].Count = 0
	}

	return buckets, nil
}

// ---------------------------------------------------------------------------
// I/O Ratio Buckets
// ---------------------------------------------------------------------------

// QueryIORatioBuckets buckets requests by their input/output token ratio.
func (q *Querier) QueryIORatioBuckets(from, to time.Time) ([]HistogramBucket, error) {
	rows, err := q.query(`
		SELECT input_tokens, output_tokens
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND output_tokens > 0`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query io ratio: %w", err)
	}
	defer rows.Close()

	buckets := []HistogramBucket{
		{Range: "<0.5", Count: 0},
		{Range: "0.5-1", Count: 0},
		{Range: "1-3", Count: 0},
		{Range: "3-10", Count: 0},
		{Range: ">10", Count: 0},
	}

	for rows.Next() {
		var inp, out int64
		if err := rows.Scan(&inp, &out); err != nil {
			continue
		}
		ratio := float64(inp) / float64(out)
		switch {
		case ratio > 10:
			buckets[4].Count++
		case ratio > 3:
			buckets[3].Count++
		case ratio > 1:
			buckets[2].Count++
		case ratio >= 0.5:
			buckets[1].Count++
		default:
			buckets[0].Count++
		}
	}
	return buckets, nil
}

// ---------------------------------------------------------------------------
// Pareto Data
// ---------------------------------------------------------------------------

// QueryParetoData returns cumulative token contribution per user.
func (q *Querier) QueryParetoData(from, to time.Time) ([]ParetoRow, error) {
	rows, err := q.query(`
		SELECT user_id, SUM(total_tokens) AS total
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id
		ORDER BY total DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query pareto: %w", err)
	}
	defer rows.Close()

	type uv struct {
		username string
		tokens   int64
	}
	var items []uv
	var grandTotal int64
	for rows.Next() {
		var uid string
		var t int64
		if err := rows.Scan(&uid, &t); err != nil {
			continue
		}
		items = append(items, uv{q.username(uid), t})
		grandTotal += t
	}

	var result []ParetoRow
	var cumulative int64
	for _, it := range items {
		cumulative += it.tokens
		pct := 0.0
		if grandTotal > 0 {
			pct = float64(cumulative) / float64(grandTotal) * 100
		}
		result = append(result, ParetoRow{
			Username:      it.username,
			TotalTokens:   it.tokens,
			CumulativePct: math.Round(pct*10) / 10,
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func changeRate(curr, prev float64) float64 {
	if prev == 0 {
		return 0
	}
	return math.Round((curr-prev)/prev*10000) / 100
}

// ---------------------------------------------------------------------------
// Phase 2: Latency Box Plot (by model)
// ---------------------------------------------------------------------------

// QueryLatencyBoxPlotByModel returns latency distribution by model (Q1, Median, Q3, etc.)
func (q *Querier) QueryLatencyBoxPlotByModel(from, to time.Time) ([]LatencyBoxPlotRow, error) {
	rows, err := q.db.Query(q.rebind(fmt.Sprintf(`
		SELECT COALESCE(lt.name, ul.model, '未知模型') AS model, ul.duration_ms
		FROM usage_logs ul
		LEFT JOIN llm_targets lt ON lt.url = ul.upstream_url
		WHERE ul.created_at >= ? AND ul.created_at < ?
		  AND ul.status_code IN (200, 201, 204)
		ORDER BY model, ul.duration_ms
	`)), from, to)
	if err != nil {
		return nil, fmt.Errorf("query latency boxplot: %w", err)
	}
	defer rows.Close()

	type modelData struct {
		model      string
		durations  []int64
	}
	modelMap := make(map[string][]int64)
	for rows.Next() {
		var model string
		var duration int64
		if err := rows.Scan(&model, &duration); err != nil {
			continue
		}
		modelMap[model] = append(modelMap[model], duration)
	}

	var result []LatencyBoxPlotRow
	for model, durations := range modelMap {
		if len(durations) == 0 {
			continue
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

		boxplot := LatencyBoxPlotRow{
			Model:  model,
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

	// Sort by median latency descending (slowest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Median > result[j].Median
	})

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 2: Latency Percentile Trend (by date)
// ---------------------------------------------------------------------------

// QueryLatencyPercentileTrend returns P50/P95/P99 latency per day.
func (q *Querier) QueryLatencyPercentileTrend(from, to time.Time) ([]LatencyPercentileRow, error) {
	rows, err := q.db.Query(q.rebind(fmt.Sprintf(`
		SELECT %s AS day, duration_ms
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND status_code IN (200, 201, 204)
		ORDER BY day, duration_ms
	`, q.sqlDate("created_at"))), from, to)
	if err != nil {
		return nil, fmt.Errorf("query latency percentile: %w", err)
	}
	defer rows.Close()

	type dailyData struct {
		date       string
		durations  []int64
	}
	dayMap := make(map[string][]int64)
	for rows.Next() {
		var day string
		var duration int64
		if err := rows.Scan(&day, &duration); err != nil {
			continue
		}
		dayMap[day] = append(dayMap[day], duration)
	}

	var result []LatencyPercentileRow
	// Iterate over days in order
	currentDay := from
	for currentDay.Before(to) {
		dayStr := currentDay.Format("2006-01-02")
		durations, ok := dayMap[dayStr]
		if !ok || len(durations) == 0 {
			currentDay = currentDay.AddDate(0, 0, 1)
			continue
		}

		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

		row := LatencyPercentileRow{
			Date:  dayStr,
			P50:   percentile(durations, 50),
			P95:   percentile(durations, 95),
			P99:   percentile(durations, 99),
			Count: len(durations),
		}
		result = append(result, row)
		currentDay = currentDay.AddDate(0, 0, 1)
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Phase 2: Daily Latency Trend (for cost prediction visualization)
// ---------------------------------------------------------------------------

// QueryDailyLatencyTrend returns average latency per day (for trend chart).
func (q *Querier) QueryDailyLatencyTrend(from, to time.Time) ([]DailyLatencyRow, error) {
	rows, err := q.db.Query(q.rebind(fmt.Sprintf(`
		SELECT
			%s AS day,
			COALESCE(AVG(CASE WHEN status_code IN (200,201,204) THEN duration_ms END), 0) AS avg_lat,
			COALESCE(MAX(duration_ms), 0) AS max_lat,
			COALESCE(SUM(cost_usd), 0) AS cost
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY day
		ORDER BY day
	`, q.sqlDate("created_at"))), from, to)
	if err != nil {
		return nil, fmt.Errorf("query daily latency trend: %w", err)
	}
	defer rows.Close()

	var result []DailyLatencyRow
	for rows.Next() {
		var day string
		var avgLat, maxLat float64
		var cost float64
		if err := rows.Scan(&day, &avgLat, &maxLat, &cost); err != nil {
			continue
		}
		result = append(result, DailyLatencyRow{
			Date:       day,
			AvgLatency: math.Round(avgLat*10) / 10,
			MaxLatency: int64(maxLat),
			CostUSD:    math.Round(cost*100) / 100,
		})
	}

	return result, nil
}


// ---------------------------------------------------------------------------
// Phase 7: User Request Count Box Plot
// ---------------------------------------------------------------------------

// QueryUserRequestBoxPlot computes box plot statistics over per-user request counts.
func (q *Querier) QueryUserRequestBoxPlot(from, to time.Time) (UserRequestBoxPlotData, error) {
	var result UserRequestBoxPlotData

	rows, err := q.query(`
		SELECT user_id, COUNT(*) AS cnt
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id
		ORDER BY cnt
	`, from, to)
	if err != nil {
		return result, fmt.Errorf("query user request boxplot: %w", err)
	}
	defer rows.Close()

	var counts []int64
	var total int64
	for rows.Next() {
		var uid string
		var cnt int64
		if err := rows.Scan(&uid, &cnt); err != nil {
			continue
		}
		counts = append(counts, cnt)
		total += cnt
	}

	n := len(counts)
	if n == 0 {
		return result, nil
	}

	// counts is already sorted by ORDER BY cnt
	result.Count = n
	result.Min = counts[0]
	result.Max = counts[n-1]
	result.Median = percentile(counts, 50)
	result.Q1 = percentile(counts, 25)
	result.Q3 = percentile(counts, 75)
	result.IQR = result.Q3 - result.Q1
	result.Mean = math.Round(float64(total)/float64(n)*10) / 10

	return result, nil
}

// ---------------------------------------------------------------------------
// Error Requests: full list for drill-down table
// ---------------------------------------------------------------------------

// QueryErrorRequests returns all non-2xx requests in the period (capped at 500).
func (q *Querier) QueryErrorRequests(from, to time.Time) ([]ErrorRequestRow, error) {
	rows, err := q.query(`
		SELECT
			created_at, user_id, model, status_code,
			duration_ms, input_tokens,
			COALESCE(upstream_url,''),
			COALESCE(request_id,'')
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		  AND status_code NOT IN (200, 201, 204)
		ORDER BY created_at DESC
		LIMIT 500
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query error requests: %w", err)
	}
	defer rows.Close()

	var result []ErrorRequestRow
	for rows.Next() {
		var r ErrorRequestRow
		var uid string
		var createdAt time.Time
		if err := rows.Scan(&createdAt, &uid, &r.Model, &r.StatusCode,
			&r.DurationMs, &r.InputTokens, &r.UpstreamURL, &r.RequestID); err != nil {
			continue
		}
		r.CreatedAt = createdAt.Format("2006-01-02 15:04:05")
		r.Username = q.username(uid)
		result = append(result, r)
	}
	return result, nil
}
