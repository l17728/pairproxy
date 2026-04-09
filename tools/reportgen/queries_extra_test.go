package main

// Additional SQLite integration tests for previously untested query functions.
// Each test uses newTestDB / mustExec helpers from integration_test.go.
// Tests focus on: (1) no error on empty DB, (2) correct values with seed data.

import (
	"database/sql"
	"testing"
	"time"
)

var (
	testFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	testTo   = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

// seedStandardSchema creates a fresh file-based SQLite DB with the full schema.
// Returns the DSN path; caller must open a Querier against it afterwards.
func seedStandardSchema(t *testing.T) string {
	t.Helper()
	dsn := tempDB(t)
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 100000, monthly_limit INTEGER DEFAULT 3000000, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, fileDB, stmt)
	}
	fileDB.Close()
	return dsn
}

// seedUsageData populates a test DB with one group, two users and several usage_log rows.
func seedUsageData(t *testing.T, dsn string) {
	t.Helper()
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db for seeding: %v", err)
	}
	mustExec(t, fileDB, `INSERT INTO groups (id, name) VALUES (1, '工程')`)
	mustExec(t, fileDB, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (1, 'alice', 1, 1, 100000, 3000000)`)
	mustExec(t, fileDB, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (2, 'bob', 1, 1, 50000, 1000000)`)
	mustExec(t, fileDB, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	// 3 streaming requests from alice
	for i, ts := range []string{"2026-03-10 09:00:00", "2026-03-15 14:00:00", "2026-03-20 18:00:00"} {
		rid := "rs" + string(rune('1'+i))
		mustExec(t, fileDB,
			`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES (?, '1', 'gpt-4o', 100, 50, 150, 1, 'https://api.openai.com', 200, 500, 0.001, 'node-1', ?)`,
			rid, ts)
	}
	// 2 non-streaming requests from bob
	for i, ts := range []string{"2026-03-12 10:00:00", "2026-03-18 11:00:00"} {
		rid := "rn" + string(rune('1'+i))
		mustExec(t, fileDB,
			`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES (?, '2', 'gpt-4o', 200, 80, 280, 0, 'https://api.openai.com', 200, 300, 0.002, 'node-2', ?)`,
			rid, ts)
	}
	// 1 error request from alice
	mustExec(t, fileDB,
		`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES ('re1', '1', 'gpt-4o', 0, 0, 0, 0, 'https://api.openai.com', 500, 50, 0.0, 'node-1', '2026-03-22 08:00:00')`)
	fileDB.Close()
}

// ── QueryTopUsers ─────────────────────────────────────────────────────────────

func TestQueryTopUsersEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryTopUsers(testFrom, testTo, "tokens", 10)
	if err != nil {
		t.Fatalf("QueryTopUsers empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestQueryTopUsersWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryTopUsers(testFrom, testTo, "tokens", 10)
	if err != nil {
		t.Fatalf("QueryTopUsers: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one user row, got 0")
	}
	// alice has 3 requests × 150 tokens = 450; bob has 2 × 280 = 560 → bob first
	if rows[0].Username != "bob" {
		t.Errorf("expected bob (560 tokens) at top, got %q", rows[0].Username)
	}
	// All rows should have Username populated
	for _, r := range rows {
		if r.Username == "" {
			t.Errorf("row has empty username: %+v", r)
		}
	}

	// Cost ordering
	costRows, err := q.QueryTopUsers(testFrom, testTo, "cost", 10)
	if err != nil {
		t.Fatalf("QueryTopUsers(cost): %v", err)
	}
	if len(costRows) == 0 {
		t.Fatal("expected cost rows, got 0")
	}
	// bob: 2×0.002=0.004; alice: 3×0.001=0.003 → bob first
	if costRows[0].Username != "bob" {
		t.Errorf("expected bob (cost 0.004) at top, got %q", costRows[0].Username)
	}
}

// ── QueryGroupComparison ──────────────────────────────────────────────────────

func TestQueryGroupComparisonEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryGroupComparison(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryGroupComparison empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestQueryGroupComparisonWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryGroupComparison(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryGroupComparison: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one group row, got 0")
	}
	g := rows[0]
	if g.GroupName != "工程" {
		t.Errorf("expected group name '工程', got %q", g.GroupName)
	}
	// 5 requests total (3+2), 6 error-free (5 total minus 1 error = 5 in range)
	if g.TotalTokens <= 0 {
		t.Errorf("expected positive total tokens, got %d", g.TotalTokens)
	}
}

// ── QueryStreamingRatio ───────────────────────────────────────────────────────

func TestQueryStreamingRatioEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	sr, err := q.QueryStreamingRatio(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryStreamingRatio empty: %v", err)
	}
	if sr.StreamingCount != 0 || sr.NonStreamingCount != 0 {
		t.Errorf("expected 0/0 on empty DB, got %d/%d", sr.StreamingCount, sr.NonStreamingCount)
	}
}

func TestQueryStreamingRatioWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	sr, err := q.QueryStreamingRatio(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryStreamingRatio: %v", err)
	}
	if sr.StreamingCount != 3 {
		t.Errorf("expected 3 streaming requests, got %d", sr.StreamingCount)
	}
	if sr.NonStreamingCount != 3 {
		t.Errorf("expected 3 non-streaming requests, got %d", sr.NonStreamingCount)
	}
	total := float64(sr.StreamingCount + sr.NonStreamingCount)
	wantPct := float64(sr.StreamingCount) / total * 100
	if sr.StreamingPct < wantPct-0.01 || sr.StreamingPct > wantPct+0.01 {
		t.Errorf("streaming pct: got %.4f, want ~%.4f", sr.StreamingPct, wantPct)
	}
}

// ── QueryDailyTrend (SQLite) ──────────────────────────────────────────────────

func TestQueryDailyTrendEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryDailyTrend(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryDailyTrend empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestQueryDailyTrendWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryDailyTrend(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryDailyTrend: %v", err)
	}
	// Seed data has events on 5 distinct dates in March 2026
	if len(rows) < 5 {
		t.Errorf("expected at least 5 daily rows, got %d", len(rows))
	}
	// Each row must have a date and positive request count
	for _, r := range rows {
		if r.Date == "" {
			t.Errorf("row has empty date: %+v", r)
		}
		if r.Requests <= 0 {
			t.Errorf("row %q has non-positive request count: %d", r.Date, r.Requests)
		}
	}
}

// ── QueryEngagement ───────────────────────────────────────────────────────────

func TestQueryEngagementEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	e, err := q.QueryEngagement(testFrom, testTo, 0)
	if err != nil {
		t.Fatalf("QueryEngagement empty: %v", err)
	}
	if e.DAU != 0 || e.WAU != 0 || e.MAU != 0 {
		t.Errorf("expected 0 DAU/WAU/MAU on empty DB, got %d/%d/%d", e.DAU, e.WAU, e.MAU)
	}
}

func TestQueryEngagementWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	e, err := q.QueryEngagement(testFrom, testTo, 2)
	if err != nil {
		t.Fatalf("QueryEngagement: %v", err)
	}
	// MAU must be ≤ total registered (2) and > 0 since both users have activity
	if e.MAU <= 0 {
		t.Errorf("expected MAU > 0, got %d", e.MAU)
	}
	if e.MAU > 2 {
		t.Errorf("expected MAU ≤ 2 registered users, got %d", e.MAU)
	}
}

// ── QueryQuotaUsage ───────────────────────────────────────────────────────────

func TestQueryQuotaUsageEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryQuotaUsage(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryQuotaUsage empty: %v", err)
	}
	// Empty usage_logs → no rows (users have limits but zero usage)
	_ = rows
}

func TestQueryQuotaUsageWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryQuotaUsage(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryQuotaUsage: %v", err)
	}
	// Both users have daily_limit set; alice has 3 streaming + 1 error request
	found := map[string]bool{}
	for _, r := range rows {
		found[r.Username] = true
		if r.MonthlyLimit <= 0 {
			t.Errorf("user %q: expected positive monthly_limit, got %d", r.Username, r.MonthlyLimit)
		}
		if r.MonthlyUsed < 0 {
			t.Errorf("user %q: monthly_used should be >= 0, got %d", r.Username, r.MonthlyUsed)
		}
	}
	for _, u := range []string{"alice", "bob"} {
		if !found[u] {
			t.Errorf("expected user %q in quota results, not found", u)
		}
	}
}

// ── QueryGroupTokenDistribution ───────────────────────────────────────────────

func TestQueryGroupTokenDistributionEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryGroupTokenDistribution(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryGroupTokenDistribution empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestQueryGroupTokenDistributionWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryGroupTokenDistribution(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryGroupTokenDistribution: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one group token distribution row")
	}
	g := rows[0]
	if g.GroupName != "工程" {
		t.Errorf("expected group '工程', got %q", g.GroupName)
	}
	// Both alice and bob are in the group
	if g.Count < 2 {
		t.Errorf("expected at least 2 users in group, got %d", g.Count)
	}
}

// ── QueryModelTokenBoxPlots ───────────────────────────────────────────────────

func TestQueryModelTokenBoxPlotsEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryModelTokenBoxPlots(testFrom, testTo, "input_tokens")
	if err != nil {
		t.Fatalf("QueryModelTokenBoxPlots empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestQueryModelTokenBoxPlotsWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	for _, col := range []string{"input_tokens", "output_tokens"} {
		rows, err := q.QueryModelTokenBoxPlots(testFrom, testTo, col)
		if err != nil {
			t.Fatalf("QueryModelTokenBoxPlots(%s): %v", col, err)
		}
		if len(rows) == 0 {
			t.Fatalf("QueryModelTokenBoxPlots(%s): expected rows, got 0", col)
		}
		r := rows[0]
		if r.Model == "" {
			t.Errorf("QueryModelTokenBoxPlots(%s): row has empty model name", col)
		}
		if r.Count <= 0 {
			t.Errorf("QueryModelTokenBoxPlots(%s): count should be > 0, got %d", col, r.Count)
		}
		if r.Median <= 0 {
			t.Errorf("QueryModelTokenBoxPlots(%s): median should be > 0, got %d", col, r.Median)
		}
	}
}

// ── QuerySourceNodeDist ───────────────────────────────────────────────────────

func TestQuerySourceNodeDistWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QuerySourceNodeDist(testFrom, testTo)
	if err != nil {
		t.Fatalf("QuerySourceNodeDist: %v", err)
	}
	// Seed has node-1 (4 requests) and node-2 (2 requests)
	if len(rows) < 2 {
		t.Errorf("expected at least 2 source nodes, got %d", len(rows))
	}
	nodeMap := map[string]int64{}
	for _, r := range rows {
		nodeMap[r.SourceNode] = r.Requests
	}
	if nodeMap["node-1"] != 4 {
		t.Errorf("expected node-1 count=4, got %d", nodeMap["node-1"])
	}
	if nodeMap["node-2"] != 2 {
		t.Errorf("expected node-2 count=2, got %d", nodeMap["node-2"])
	}
}

// ── QueryUpstreamStats ────────────────────────────────────────────────────────

func TestQueryUpstreamStatsWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryUpstreamStats(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryUpstreamStats: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected upstream rows, got 0")
	}
	u := rows[0]
	if u.URL == "" {
		t.Error("upstream URL is empty")
	}
	if u.Requests <= 0 {
		t.Errorf("expected positive request count, got %d", u.Requests)
	}
	// 1 of 6 requests is a 500 → error rate ~16.67%
	if u.ErrorRate < 0 || u.ErrorRate > 100 {
		t.Errorf("error rate out of range: %.2f", u.ErrorRate)
	}
}

// ── QueryStatusCodeDist ───────────────────────────────────────────────────────

func TestQueryStatusCodeDistWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryStatusCodeDist(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryStatusCodeDist: %v", err)
	}
	codes := map[int]int64{}
	for _, r := range rows {
		codes[r.StatusCode] = r.Count
	}
	if codes[200] != 5 {
		t.Errorf("expected 5 status-200 rows, got %d", codes[200])
	}
	if codes[500] != 1 {
		t.Errorf("expected 1 status-500 row, got %d", codes[500])
	}
}

// ── QueryPeakRPM ──────────────────────────────────────────────────────────────

func TestQueryPeakRPMEmpty(t *testing.T) {
	dsn := seedStandardSchema(t)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rpm, err := q.QueryPeakRPM(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryPeakRPM empty: %v", err)
	}
	if rpm != 0 {
		t.Errorf("expected 0 peak RPM on empty DB, got %d", rpm)
	}
}

func TestQueryPeakRPMWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rpm, err := q.QueryPeakRPM(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryPeakRPM: %v", err)
	}
	// Each seed row is in a different minute, so peak RPM ≥ 1
	if rpm < 1 {
		t.Errorf("expected peak RPM >= 1, got %d", rpm)
	}
}

// ── QuerySlowRequests ──────────────────────────────────────────────────────────

func TestQuerySlowRequestsWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QuerySlowRequests(testFrom, testTo, 10)
	if err != nil {
		t.Fatalf("QuerySlowRequests: %v", err)
	}
	// Streaming requests have duration_ms=500; non-streaming=300; error=50
	// All 6 should be returned (limit=10)
	if len(rows) == 0 {
		t.Error("expected slow request rows, got 0")
	}
	// Highest duration first: 500ms
	if rows[0].DurationMs != 500 {
		t.Errorf("expected top slow request to have duration_ms=500, got %d", rows[0].DurationMs)
	}
}

// ── QueryErrorRequests ────────────────────────────────────────────────────────

func TestQueryErrorRequestsWithData(t *testing.T) {
	dsn := seedStandardSchema(t)
	seedUsageData(t, dsn)
	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	rows, err := q.QueryErrorRequests(testFrom, testTo)
	if err != nil {
		t.Fatalf("QueryErrorRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 error request, got %d", len(rows))
	}
	if rows[0].StatusCode != 500 {
		t.Errorf("expected status_code=500, got %d", rows[0].StatusCode)
	}
}
