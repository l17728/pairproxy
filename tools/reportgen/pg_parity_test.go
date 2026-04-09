package main

// pg_parity_test.go — SQLite vs PostgreSQL output parity tests.
//
// Every TestPGParity_<QueryName> test:
//   1. Seeds IDENTICAL data into both a PostgreSQL DB (via newPGTestDB) and a
//      fresh SQLite file-DB (via seedParityDB + seedParityData).
//   2. Calls the same Query* method on both Queriers.
//   3. Asserts that every meaningful field of every row is equal.
//
// All tests skip automatically when POSTGRES_TEST_DSN is not set.
// Run them with:
//   POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/reportgen_test?sslmode=disable" \
//     go test -v -run TestPGParity

import (
	"database/sql"
	"fmt"
	"math"
	"testing"
	"time"
)

// ── shared seed helpers ───────────────────────────────────────────────────────

// parityFrom / parityTo define the fixed query window used by all parity tests.
var (
	parityFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	parityTo   = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

// seedParityDB creates a fresh SQLite file-DB with the standard schema and
// returns an open Querier for it.
func seedParityDB(t *testing.T) (*sql.DB, *Querier) {
	t.Helper()
	dsn := tempDB(t)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("parity sqlite open: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT UNIQUE, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 100000, monthly_limit INTEGER DEFAULT 3000000, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT DEFAULT 'openai', api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, raw, stmt)
	}
	raw.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("parity NewQuerier sqlite: %v", err)
	}
	// Return a fresh raw handle for seeding
	raw2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("parity sqlite reopen: %v", err)
	}
	t.Cleanup(func() { raw2.Close(); q.Close() })
	return raw2, q
}

// seedRow describes one usage_log record. ts must be an ISO-8601 UTC string
// ("2026-03-15 10:00:00") so both drivers interpret it identically.
type seedRow struct {
	rid       string
	userID    string
	model     string
	inTok     int
	outTok    int
	totTok    int
	stream    bool
	upstream  string
	status    int
	durMs     int
	cost      float64
	sourceNode string
	ts        string // "YYYY-MM-DD HH:MM:SS" UTC
}

// parityRows is the canonical dataset used across all parity tests.
var parityRows = []seedRow{
	// alice (user_id=1): 3 streaming success rows
	{"r1", "1", "gpt-4o", 100, 50, 150, true, "https://api.openai.com", 200, 500, 0.001, "node-1", "2026-03-10 09:00:00"},
	{"r2", "1", "gpt-4o", 120, 60, 180, true, "https://api.openai.com", 200, 600, 0.002, "node-1", "2026-03-15 14:00:00"},
	{"r3", "1", "claude-3", 90, 40, 130, true, "https://api.anthropic.com", 200, 450, 0.003, "node-2", "2026-03-20 18:00:00"},
	// bob (user_id=2): 2 non-streaming success rows
	{"r4", "2", "gpt-4o", 200, 80, 280, false, "https://api.openai.com", 200, 300, 0.004, "node-1", "2026-03-12 10:00:00"},
	{"r5", "2", "gpt-4o", 150, 70, 220, false, "https://api.openai.com", 200, 280, 0.003, "node-2", "2026-03-18 11:00:00"},
	// alice: 1 error row
	{"r6", "1", "gpt-4o", 0, 0, 0, false, "https://api.openai.com", 500, 50, 0.0, "node-1", "2026-03-22 08:00:00"},
	// bob: 1 rate-limit row
	{"r7", "2", "gpt-4o", 0, 0, 0, false, "https://api.openai.com", 429, 30, 0.0, "node-2", "2026-03-25 09:00:00"},
}

// insertParityRows inserts the canonical rows into a SQLite DB (using ? placeholders).
func insertParityRows(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO groups (id, name, daily_token_limit, monthly_token_limit) VALUES (1, '工程', 500000, 10000000)`)
	mustExec(t, db, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (1, 'alice', 1, 1, 100000, 3000000)`)
	mustExec(t, db, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (2, 'bob', 1, 1, 50000, 1000000)`)
	mustExec(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	mustExec(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (2, 'Claude', 'https://api.anthropic.com', 'anthropic')`)
	for _, r := range parityRows {
		stream := 0
		if r.stream {
			stream = 1
		}
		mustExec(t, db,
			`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.rid, r.userID, r.model, r.inTok, r.outTok, r.totTok, stream, r.upstream, r.status, r.durMs, r.cost, r.sourceNode, r.ts)
	}
}

// insertParityRowsPG inserts the canonical rows into a PostgreSQL DB (using $N placeholders).
func insertParityRowsPG(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExecPG(t, db, `INSERT INTO groups (id, name, daily_token_limit, monthly_token_limit) VALUES (1, '工程', 500000, 10000000)`)
	mustExecPG(t, db, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (1, 'alice', 1, TRUE, 100000, 3000000)`)
	mustExecPG(t, db, `INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit) VALUES (2, 'bob', 1, TRUE, 50000, 1000000)`)
	mustExecPG(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	mustExecPG(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (2, 'Claude', 'https://api.anthropic.com', 'anthropic')`)
	for _, r := range parityRows {
		mustExecPG(t, db,
			`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			r.rid, r.userID, r.model, r.inTok, r.outTok, r.totTok, r.stream, r.upstream, r.status, r.durMs, r.cost, r.sourceNode, r.ts+"+00")
	}
}

// floatEq returns true if two floats are equal within 0.0001 tolerance.
func floatEq(a, b float64) bool { return math.Abs(a-b) < 0.0001 }

// ── QueryDailyTrend ───────────────────────────────────────────────────────────

func TestPGParity_QueryDailyTrend(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryDailyTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryDailyTrend: %v", err)
	}
	sqRows, err := sqQ.QueryDailyTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryDailyTrend: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count mismatch: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Date != sq.Date {
			t.Errorf("[%d] Date: PG=%q SQLite=%q", i, pg.Date, sq.Date)
		}
		if pg.TotalTokens != sq.TotalTokens {
			t.Errorf("[%d] TotalTokens: PG=%d SQLite=%d", i, pg.TotalTokens, sq.TotalTokens)
		}
		if pg.Requests != sq.Requests {
			t.Errorf("[%d] Requests: PG=%d SQLite=%d", i, pg.Requests, sq.Requests)
		}
		if !floatEq(pg.CostUSD, sq.CostUSD) {
			t.Errorf("[%d] CostUSD: PG=%.6f SQLite=%.6f", i, pg.CostUSD, sq.CostUSD)
		}
		if pg.Errors != sq.Errors {
			t.Errorf("[%d] Errors: PG=%d SQLite=%d", i, pg.Errors, sq.Errors)
		}
	}
}

// ── QueryHeatmap ─────────────────────────────────────────────────────────────

func TestPGParity_QueryHeatmap(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryHeatmap(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryHeatmap: %v", err)
	}
	sqRows, err := sqQ.QueryHeatmap(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryHeatmap: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count mismatch: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	pgTot, sqTot := int64(0), int64(0)
	for _, r := range pgRows {
		pgTot += r.Value
	}
	for _, r := range sqRows {
		sqTot += r.Value
	}
	if pgTot != sqTot {
		t.Errorf("total heatmap value: PG=%d SQLite=%d", pgTot, sqTot)
	}
}

// ── QueryTopUsers ─────────────────────────────────────────────────────────────

func TestPGParity_QueryTopUsers(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	for _, orderBy := range []string{"tokens", "cost", "requests"} {
		pgRows, err := pgQ.QueryTopUsers(parityFrom, parityTo, orderBy, 10)
		if err != nil {
			t.Fatalf("PG QueryTopUsers(%s): %v", orderBy, err)
		}
		sqRows, err := sqQ.QueryTopUsers(parityFrom, parityTo, orderBy, 10)
		if err != nil {
			t.Fatalf("SQLite QueryTopUsers(%s): %v", orderBy, err)
		}

		if len(pgRows) != len(sqRows) {
			t.Fatalf("orderBy=%s row count: PG=%d SQLite=%d", orderBy, len(pgRows), len(sqRows))
		}
		for i, pg := range pgRows {
			sq := sqRows[i]
			if pg.Username != sq.Username {
				t.Errorf("orderBy=%s [%d] Username: PG=%q SQLite=%q", orderBy, i, pg.Username, sq.Username)
			}
			if pg.Requests != sq.Requests {
				t.Errorf("orderBy=%s [%d] Requests: PG=%d SQLite=%d", orderBy, i, pg.Requests, sq.Requests)
			}
			if pg.InputTokens != sq.InputTokens {
				t.Errorf("orderBy=%s [%d] InputTokens: PG=%d SQLite=%d", orderBy, i, pg.InputTokens, sq.InputTokens)
			}
			if pg.OutputTokens != sq.OutputTokens {
				t.Errorf("orderBy=%s [%d] OutputTokens: PG=%d SQLite=%d", orderBy, i, pg.OutputTokens, sq.OutputTokens)
			}
			if !floatEq(pg.CostUSD, sq.CostUSD) {
				t.Errorf("orderBy=%s [%d] CostUSD: PG=%.6f SQLite=%.6f", orderBy, i, pg.CostUSD, sq.CostUSD)
			}
		}
	}
}

// ── QueryModelDistribution ────────────────────────────────────────────────────

func TestPGParity_QueryModelDistribution(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryModelDistribution(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryModelDistribution: %v", err)
	}
	sqRows, err := sqQ.QueryModelDistribution(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryModelDistribution: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Model != sq.Model {
			t.Errorf("[%d] Model: PG=%q SQLite=%q", i, pg.Model, sq.Model)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
		if pg.TotalTokens != sq.TotalTokens {
			t.Errorf("[%d] TotalTokens: PG=%d SQLite=%d", i, pg.TotalTokens, sq.TotalTokens)
		}
		if !floatEq(pg.CostUSD, sq.CostUSD) {
			t.Errorf("[%d] CostUSD: PG=%.6f SQLite=%.6f", i, pg.CostUSD, sq.CostUSD)
		}
	}
}

// ── QueryGroupComparison ──────────────────────────────────────────────────────

func TestPGParity_QueryGroupComparison(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryGroupComparison(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryGroupComparison: %v", err)
	}
	sqRows, err := sqQ.QueryGroupComparison(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryGroupComparison: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.GroupName != sq.GroupName {
			t.Errorf("[%d] GroupName: PG=%q SQLite=%q", i, pg.GroupName, sq.GroupName)
		}
		if pg.TotalTokens != sq.TotalTokens {
			t.Errorf("[%d] TotalTokens: PG=%d SQLite=%d", i, pg.TotalTokens, sq.TotalTokens)
		}
		if pg.Requests != sq.Requests {
			t.Errorf("[%d] Requests: PG=%d SQLite=%d", i, pg.Requests, sq.Requests)
		}
		if !floatEq(pg.CostUSD, sq.CostUSD) {
			t.Errorf("[%d] CostUSD: PG=%.6f SQLite=%.6f", i, pg.CostUSD, sq.CostUSD)
		}
	}
}

// ── QueryUpstreamStats ────────────────────────────────────────────────────────

func TestPGParity_QueryUpstreamStats(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryUpstreamStats(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryUpstreamStats: %v", err)
	}
	sqRows, err := sqQ.QueryUpstreamStats(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryUpstreamStats: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.URL != sq.URL {
			t.Errorf("[%d] URL: PG=%q SQLite=%q", i, pg.URL, sq.URL)
		}
		if pg.Requests != sq.Requests {
			t.Errorf("[%d] Requests: PG=%d SQLite=%d", i, pg.Requests, sq.Requests)
		}
		if pg.TotalTokens != sq.TotalTokens {
			t.Errorf("[%d] TotalTokens: PG=%d SQLite=%d", i, pg.TotalTokens, sq.TotalTokens)
		}
		if !floatEq(pg.CostUSD, sq.CostUSD) {
			t.Errorf("[%d] CostUSD: PG=%.6f SQLite=%.6f", i, pg.CostUSD, sq.CostUSD)
		}
		if !floatEq(pg.ErrorRate, sq.ErrorRate) {
			t.Errorf("[%d] ErrorRate: PG=%.4f SQLite=%.4f", i, pg.ErrorRate, sq.ErrorRate)
		}
	}
}

// ── QueryStatusCodeDist ───────────────────────────────────────────────────────

func TestPGParity_QueryStatusCodeDist(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryStatusCodeDist(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryStatusCodeDist: %v", err)
	}
	sqRows, err := sqQ.QueryStatusCodeDist(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryStatusCodeDist: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.StatusCode != sq.StatusCode {
			t.Errorf("[%d] StatusCode: PG=%d SQLite=%d", i, pg.StatusCode, sq.StatusCode)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
	}
}

// ── QuerySlowRequests ─────────────────────────────────────────────────────────

func TestPGParity_QuerySlowRequests(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QuerySlowRequests(parityFrom, parityTo, 10)
	if err != nil {
		t.Fatalf("PG QuerySlowRequests: %v", err)
	}
	sqRows, err := sqQ.QuerySlowRequests(parityFrom, parityTo, 10)
	if err != nil {
		t.Fatalf("SQLite QuerySlowRequests: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.DurationMs != sq.DurationMs {
			t.Errorf("[%d] DurationMs: PG=%d SQLite=%d", i, pg.DurationMs, sq.DurationMs)
		}
		if pg.Username != sq.Username {
			t.Errorf("[%d] Username: PG=%q SQLite=%q", i, pg.Username, sq.Username)
		}
		if pg.StatusCode != sq.StatusCode {
			t.Errorf("[%d] StatusCode: PG=%d SQLite=%d", i, pg.StatusCode, sq.StatusCode)
		}
	}
}

// ── QueryStreamingRatio ───────────────────────────────────────────────────────

func TestPGParity_QueryStreamingRatio(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgR, err := pgQ.QueryStreamingRatio(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryStreamingRatio: %v", err)
	}
	sqR, err := sqQ.QueryStreamingRatio(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryStreamingRatio: %v", err)
	}

	if pgR.StreamingCount != sqR.StreamingCount {
		t.Errorf("StreamingCount: PG=%d SQLite=%d", pgR.StreamingCount, sqR.StreamingCount)
	}
	if pgR.NonStreamingCount != sqR.NonStreamingCount {
		t.Errorf("NonStreamingCount: PG=%d SQLite=%d", pgR.NonStreamingCount, sqR.NonStreamingCount)
	}
	if !floatEq(pgR.StreamingPct, sqR.StreamingPct) {
		t.Errorf("StreamingPct: PG=%.4f SQLite=%.4f", pgR.StreamingPct, sqR.StreamingPct)
	}
	if !floatEq(pgR.StreamingAvgLatency, sqR.StreamingAvgLatency) {
		t.Errorf("StreamingAvgLatency: PG=%.2f SQLite=%.2f", pgR.StreamingAvgLatency, sqR.StreamingAvgLatency)
	}
	if !floatEq(pgR.NonStreamingAvgLatency, sqR.NonStreamingAvgLatency) {
		t.Errorf("NonStreamingAvgLatency: PG=%.2f SQLite=%.2f", pgR.NonStreamingAvgLatency, sqR.NonStreamingAvgLatency)
	}
}

// ── QueryEngagement ───────────────────────────────────────────────────────────

func TestPGParity_QueryEngagement(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgR, err := pgQ.QueryEngagement(parityFrom, parityTo, 2)
	if err != nil {
		t.Fatalf("PG QueryEngagement: %v", err)
	}
	sqR, err := sqQ.QueryEngagement(parityFrom, parityTo, 2)
	if err != nil {
		t.Fatalf("SQLite QueryEngagement: %v", err)
	}

	if pgR.MAU != sqR.MAU {
		t.Errorf("MAU: PG=%d SQLite=%d", pgR.MAU, sqR.MAU)
	}
	if pgR.NewUsersThisPeriod != sqR.NewUsersThisPeriod {
		t.Errorf("NewUsersThisPeriod: PG=%d SQLite=%d", pgR.NewUsersThisPeriod, sqR.NewUsersThisPeriod)
	}
	if !floatEq(pgR.AdoptionRate, sqR.AdoptionRate) {
		t.Errorf("AdoptionRate: PG=%.4f SQLite=%.4f", pgR.AdoptionRate, sqR.AdoptionRate)
	}
}

// ── QueryUserFreqBuckets ──────────────────────────────────────────────────────

func TestPGParity_QueryUserFreqBuckets(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryUserFreqBuckets(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryUserFreqBuckets: %v", err)
	}
	sqRows, err := sqQ.QueryUserFreqBuckets(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryUserFreqBuckets: %v", err)
	}

	// Total counts across buckets must match.
	pgTot, sqTot := 0, 0
	for _, r := range pgRows {
		pgTot += r.Count
	}
	for _, r := range sqRows {
		sqTot += r.Count
	}
	if pgTot != sqTot {
		t.Errorf("total bucket count: PG=%d SQLite=%d", pgTot, sqTot)
	}
	if len(pgRows) != len(sqRows) {
		t.Fatalf("bucket count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Range != sq.Range {
			t.Errorf("[%d] Range: PG=%q SQLite=%q", i, pg.Range, sq.Range)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
	}
}

// ── QueryIORatioBuckets ───────────────────────────────────────────────────────

func TestPGParity_QueryIORatioBuckets(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryIORatioBuckets(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryIORatioBuckets: %v", err)
	}
	sqRows, err := sqQ.QueryIORatioBuckets(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryIORatioBuckets: %v", err)
	}

	pgTot, sqTot := 0, 0
	for _, r := range pgRows {
		pgTot += r.Count
	}
	for _, r := range sqRows {
		sqTot += r.Count
	}
	if pgTot != sqTot {
		t.Errorf("total bucket count: PG=%d SQLite=%d", pgTot, sqTot)
	}
	if len(pgRows) != len(sqRows) {
		t.Fatalf("bucket count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Range != sq.Range {
			t.Errorf("[%d] Range: PG=%q SQLite=%q", i, pg.Range, sq.Range)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
	}
}

// ── QueryParetoData ───────────────────────────────────────────────────────────

func TestPGParity_QueryParetoData(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryParetoData(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryParetoData: %v", err)
	}
	sqRows, err := sqQ.QueryParetoData(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryParetoData: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Username != sq.Username {
			t.Errorf("[%d] Username: PG=%q SQLite=%q", i, pg.Username, sq.Username)
		}
		if pg.TotalTokens != sq.TotalTokens {
			t.Errorf("[%d] TotalTokens: PG=%d SQLite=%d", i, pg.TotalTokens, sq.TotalTokens)
		}
		if !floatEq(pg.CumulativePct, sq.CumulativePct) {
			t.Errorf("[%d] CumulativePct: PG=%.4f SQLite=%.4f", i, pg.CumulativePct, sq.CumulativePct)
		}
	}
}

// ── QueryLatencyBoxPlotByModel ────────────────────────────────────────────────

func TestPGParity_QueryLatencyBoxPlotByModel(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryLatencyBoxPlotByModel(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryLatencyBoxPlotByModel: %v", err)
	}
	sqRows, err := sqQ.QueryLatencyBoxPlotByModel(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryLatencyBoxPlotByModel: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Model != sq.Model {
			t.Errorf("[%d] Model: PG=%q SQLite=%q", i, pg.Model, sq.Model)
		}
		if pg.Min != sq.Min {
			t.Errorf("[%d] Min: PG=%d SQLite=%d", i, pg.Min, sq.Min)
		}
		if pg.Max != sq.Max {
			t.Errorf("[%d] Max: PG=%d SQLite=%d", i, pg.Max, sq.Max)
		}
		if pg.Median != sq.Median {
			t.Errorf("[%d] Median: PG=%d SQLite=%d", i, pg.Median, sq.Median)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
	}
}

// ── QueryLatencyPercentileTrend ────────────────────────────────────────────────

func TestPGParity_QueryLatencyPercentileTrend(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryLatencyPercentileTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryLatencyPercentileTrend: %v", err)
	}
	sqRows, err := sqQ.QueryLatencyPercentileTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryLatencyPercentileTrend: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Date != sq.Date {
			t.Errorf("[%d] Date: PG=%q SQLite=%q", i, pg.Date, sq.Date)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
		// P50/P95/P99 are approximations — allow ±1 ms tolerance due to percentile algorithm differences
		if abs64(pg.P50-sq.P50) > 1 {
			t.Errorf("[%d] P50: PG=%d SQLite=%d", i, pg.P50, sq.P50)
		}
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ── QueryDailyLatencyTrend ────────────────────────────────────────────────────

func TestPGParity_QueryDailyLatencyTrend(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryDailyLatencyTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryDailyLatencyTrend: %v", err)
	}
	sqRows, err := sqQ.QueryDailyLatencyTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryDailyLatencyTrend: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Date != sq.Date {
			t.Errorf("[%d] Date: PG=%q SQLite=%q", i, pg.Date, sq.Date)
		}
		if pg.MaxLatency != sq.MaxLatency {
			t.Errorf("[%d] MaxLatency: PG=%d SQLite=%d", i, pg.MaxLatency, sq.MaxLatency)
		}
		if !floatEq(pg.AvgLatency, sq.AvgLatency) {
			t.Errorf("[%d] AvgLatency: PG=%.2f SQLite=%.2f", i, pg.AvgLatency, sq.AvgLatency)
		}
	}
}

// ── QueryUserRequestBoxPlot ───────────────────────────────────────────────────

func TestPGParity_QueryUserRequestBoxPlot(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgR, err := pgQ.QueryUserRequestBoxPlot(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryUserRequestBoxPlot: %v", err)
	}
	sqR, err := sqQ.QueryUserRequestBoxPlot(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryUserRequestBoxPlot: %v", err)
	}

	if pgR.Count != sqR.Count {
		t.Errorf("Count: PG=%d SQLite=%d", pgR.Count, sqR.Count)
	}
	if pgR.Min != sqR.Min {
		t.Errorf("Min: PG=%d SQLite=%d", pgR.Min, sqR.Min)
	}
	if pgR.Max != sqR.Max {
		t.Errorf("Max: PG=%d SQLite=%d", pgR.Max, sqR.Max)
	}
	if !floatEq(pgR.Mean, sqR.Mean) {
		t.Errorf("Mean: PG=%.4f SQLite=%.4f", pgR.Mean, sqR.Mean)
	}
	if pgR.Median != sqR.Median {
		t.Errorf("Median: PG=%d SQLite=%d", pgR.Median, sqR.Median)
	}
}

// ── QueryErrorRequests ────────────────────────────────────────────────────────

func TestPGParity_QueryErrorRequests(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryErrorRequests(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryErrorRequests: %v", err)
	}
	sqRows, err := sqQ.QueryErrorRequests(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryErrorRequests: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.StatusCode != sq.StatusCode {
			t.Errorf("[%d] StatusCode: PG=%d SQLite=%d", i, pg.StatusCode, sq.StatusCode)
		}
		if pg.Username != sq.Username {
			t.Errorf("[%d] Username: PG=%q SQLite=%q", i, pg.Username, sq.Username)
		}
		if pg.RequestID != sq.RequestID {
			t.Errorf("[%d] RequestID: PG=%q SQLite=%q", i, pg.RequestID, sq.RequestID)
		}
	}
}

// ── QueryEngagementTrend ──────────────────────────────────────────────────────

func TestPGParity_QueryEngagementTrend(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryEngagementTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryEngagementTrend: %v", err)
	}
	sqRows, err := sqQ.QueryEngagementTrend(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryEngagementTrend: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Date != sq.Date {
			t.Errorf("[%d] Date: PG=%q SQLite=%q", i, pg.Date, sq.Date)
		}
		if pg.DAU != sq.DAU {
			t.Errorf("[%d] DAU: PG=%d SQLite=%d", i, pg.DAU, sq.DAU)
		}
	}
}

// ── QueryQuotaUsage ───────────────────────────────────────────────────────────

func TestPGParity_QueryQuotaUsage(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryQuotaUsage(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryQuotaUsage: %v", err)
	}
	sqRows, err := sqQ.QueryQuotaUsage(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryQuotaUsage: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Username != sq.Username {
			t.Errorf("[%d] Username: PG=%q SQLite=%q", i, pg.Username, sq.Username)
		}
		if pg.DailyLimit != sq.DailyLimit {
			t.Errorf("[%d] DailyLimit: PG=%d SQLite=%d", i, pg.DailyLimit, sq.DailyLimit)
		}
		if pg.MonthlyLimit != sq.MonthlyLimit {
			t.Errorf("[%d] MonthlyLimit: PG=%d SQLite=%d", i, pg.MonthlyLimit, sq.MonthlyLimit)
		}
		// MonthlyUsed: both drivers calculate vs the same rows in the range;
		// because the query uses sqlCurrentYearMonth() the value only matches
		// when the test runs in the seeded month (2026-03). Accept identical
		// values (both zero if run outside 2026-03, both non-zero if run inside).
		if pg.MonthlyUsed != sq.MonthlyUsed {
			t.Errorf("[%d] MonthlyUsed: PG=%d SQLite=%d", i, pg.MonthlyUsed, sq.MonthlyUsed)
		}
	}
}

// ── QueryLatencyBoxPlotByUpstream ─────────────────────────────────────────────

func TestPGParity_QueryLatencyBoxPlotByUpstream(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryLatencyBoxPlotByUpstream(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryLatencyBoxPlotByUpstream: %v", err)
	}
	sqRows, err := sqQ.QueryLatencyBoxPlotByUpstream(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryLatencyBoxPlotByUpstream: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.Model != sq.Model { // Model field holds upstream URL in this query
			t.Errorf("[%d] Model(URL): PG=%q SQLite=%q", i, pg.Model, sq.Model)
		}
		if pg.Min != sq.Min {
			t.Errorf("[%d] Min: PG=%d SQLite=%d", i, pg.Min, sq.Min)
		}
		if pg.Max != sq.Max {
			t.Errorf("[%d] Max: PG=%d SQLite=%d", i, pg.Max, sq.Max)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
	}
}

// ── QueryGroupTokenDistribution ───────────────────────────────────────────────

func TestPGParity_QueryGroupTokenDistribution(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	pgRows, err := pgQ.QueryGroupTokenDistribution(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("PG QueryGroupTokenDistribution: %v", err)
	}
	sqRows, err := sqQ.QueryGroupTokenDistribution(parityFrom, parityTo)
	if err != nil {
		t.Fatalf("SQLite QueryGroupTokenDistribution: %v", err)
	}

	if len(pgRows) != len(sqRows) {
		t.Fatalf("row count: PG=%d SQLite=%d", len(pgRows), len(sqRows))
	}
	for i, pg := range pgRows {
		sq := sqRows[i]
		if pg.GroupName != sq.GroupName {
			t.Errorf("[%d] GroupName: PG=%q SQLite=%q", i, pg.GroupName, sq.GroupName)
		}
		if pg.Count != sq.Count {
			t.Errorf("[%d] Count: PG=%d SQLite=%d", i, pg.Count, sq.Count)
		}
		if pg.Min != sq.Min {
			t.Errorf("[%d] Min: PG=%d SQLite=%d", i, pg.Min, sq.Min)
		}
		if pg.Max != sq.Max {
			t.Errorf("[%d] Max: PG=%d SQLite=%d", i, pg.Max, sq.Max)
		}
		if pg.Median != sq.Median {
			t.Errorf("[%d] Median: PG=%d SQLite=%d", i, pg.Median, sq.Median)
		}
	}
}

// ── end-to-end: all 23 Query* in one shot ────────────────────────────────────

// TestPGParity_AllQueries seeds both backends with the canonical dataset and
// runs every Query* function, asserting no error and that both return the same
// number of rows / aggregate values. This is the comprehensive smoke test that
// catches any query that was missed in the individual parity tests above.
func TestPGParity_AllQueries(t *testing.T) {
	pgDB, pgQ := newPGTestDB(t)
	insertParityRowsPG(t, pgDB)

	sqDB, sqQ := seedParityDB(t)
	insertParityRows(t, sqDB)

	type check struct {
		name string
		run  func() (int, int64, error) // returns (rowCount, totalTokensOrValue, error)
	}

	checks := []check{
		{"QueryKPI", func() (int, int64, error) {
			pg, e := pgQ.QueryKPI(parityFrom, parityTo)
			if e != nil {
				return 0, 0, fmt.Errorf("PG: %w", e)
			}
			sq, e := sqQ.QueryKPI(parityFrom, parityTo)
			if e != nil {
				return 0, 0, fmt.Errorf("SQLite: %w", e)
			}
			if pg.TotalRequests != sq.TotalRequests {
				return 0, 0, fmt.Errorf("TotalRequests PG=%d SQLite=%d", pg.TotalRequests, sq.TotalRequests)
			}
			if pg.TotalTokens != sq.TotalTokens {
				return 0, 0, fmt.Errorf("TotalTokens PG=%d SQLite=%d", pg.TotalTokens, sq.TotalTokens)
			}
			return 1, pg.TotalTokens, nil
		}},
		{"QueryDailyTrend", func() (int, int64, error) {
			pg, e := pgQ.QueryDailyTrend(parityFrom, parityTo)
			sq, e2 := sqQ.QueryDailyTrend(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryTopUsers", func() (int, int64, error) {
			pg, e := pgQ.QueryTopUsers(parityFrom, parityTo, "tokens", 10)
			sq, e2 := sqQ.QueryTopUsers(parityFrom, parityTo, "tokens", 10)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryModelDistribution", func() (int, int64, error) {
			pg, e := pgQ.QueryModelDistribution(parityFrom, parityTo)
			sq, e2 := sqQ.QueryModelDistribution(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryGroupComparison", func() (int, int64, error) {
			pg, e := pgQ.QueryGroupComparison(parityFrom, parityTo)
			sq, e2 := sqQ.QueryGroupComparison(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryUpstreamStats", func() (int, int64, error) {
			pg, e := pgQ.QueryUpstreamStats(parityFrom, parityTo)
			sq, e2 := sqQ.QueryUpstreamStats(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryStatusCodeDist", func() (int, int64, error) {
			pg, e := pgQ.QueryStatusCodeDist(parityFrom, parityTo)
			sq, e2 := sqQ.QueryStatusCodeDist(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QuerySlowRequests", func() (int, int64, error) {
			pg, e := pgQ.QuerySlowRequests(parityFrom, parityTo, 10)
			sq, e2 := sqQ.QuerySlowRequests(parityFrom, parityTo, 10)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryStreamingRatio", func() (int, int64, error) {
			pg, e := pgQ.QueryStreamingRatio(parityFrom, parityTo)
			sq, e2 := sqQ.QueryStreamingRatio(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if pg.StreamingCount != sq.StreamingCount {
				return 0, 0, fmt.Errorf("StreamingCount PG=%d SQLite=%d", pg.StreamingCount, sq.StreamingCount)
			}
			return 1, pg.StreamingCount, nil
		}},
		{"QueryEngagement", func() (int, int64, error) {
			pg, e := pgQ.QueryEngagement(parityFrom, parityTo, 2)
			sq, e2 := sqQ.QueryEngagement(parityFrom, parityTo, 2)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if pg.MAU != sq.MAU {
				return 0, 0, fmt.Errorf("MAU PG=%d SQLite=%d", pg.MAU, sq.MAU)
			}
			return 1, int64(pg.MAU), nil
		}},
		{"QueryUserFreqBuckets", func() (int, int64, error) {
			pg, e := pgQ.QueryUserFreqBuckets(parityFrom, parityTo)
			sq, e2 := sqQ.QueryUserFreqBuckets(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryIORatioBuckets", func() (int, int64, error) {
			pg, e := pgQ.QueryIORatioBuckets(parityFrom, parityTo)
			sq, e2 := sqQ.QueryIORatioBuckets(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryParetoData", func() (int, int64, error) {
			pg, e := pgQ.QueryParetoData(parityFrom, parityTo)
			sq, e2 := sqQ.QueryParetoData(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryLatencyBoxPlotByModel", func() (int, int64, error) {
			pg, e := pgQ.QueryLatencyBoxPlotByModel(parityFrom, parityTo)
			sq, e2 := sqQ.QueryLatencyBoxPlotByModel(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryLatencyPercentileTrend", func() (int, int64, error) {
			pg, e := pgQ.QueryLatencyPercentileTrend(parityFrom, parityTo)
			sq, e2 := sqQ.QueryLatencyPercentileTrend(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryDailyLatencyTrend", func() (int, int64, error) {
			pg, e := pgQ.QueryDailyLatencyTrend(parityFrom, parityTo)
			sq, e2 := sqQ.QueryDailyLatencyTrend(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryUserRequestBoxPlot", func() (int, int64, error) {
			pg, e := pgQ.QueryUserRequestBoxPlot(parityFrom, parityTo)
			sq, e2 := sqQ.QueryUserRequestBoxPlot(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if pg.Count != sq.Count {
				return 0, 0, fmt.Errorf("Count PG=%d SQLite=%d", pg.Count, sq.Count)
			}
			return pg.Count, pg.Max, nil
		}},
		{"QueryErrorRequests", func() (int, int64, error) {
			pg, e := pgQ.QueryErrorRequests(parityFrom, parityTo)
			sq, e2 := sqQ.QueryErrorRequests(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryEngagementTrend", func() (int, int64, error) {
			pg, e := pgQ.QueryEngagementTrend(parityFrom, parityTo)
			sq, e2 := sqQ.QueryEngagementTrend(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryQuotaUsage", func() (int, int64, error) {
			pg, e := pgQ.QueryQuotaUsage(parityFrom, parityTo)
			sq, e2 := sqQ.QueryQuotaUsage(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryLatencyBoxPlotByUpstream", func() (int, int64, error) {
			pg, e := pgQ.QueryLatencyBoxPlotByUpstream(parityFrom, parityTo)
			sq, e2 := sqQ.QueryLatencyBoxPlotByUpstream(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
		{"QueryGroupTokenDistribution", func() (int, int64, error) {
			pg, e := pgQ.QueryGroupTokenDistribution(parityFrom, parityTo)
			sq, e2 := sqQ.QueryGroupTokenDistribution(parityFrom, parityTo)
			if e != nil || e2 != nil {
				return 0, 0, fmt.Errorf("PG=%v SQLite=%v", e, e2)
			}
			if len(pg) != len(sq) {
				return 0, 0, fmt.Errorf("len PG=%d SQLite=%d", len(pg), len(sq))
			}
			return len(pg), 0, nil
		}},
	}

	for _, c := range checks {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rows, val, err := c.run()
			if err != nil {
				t.Errorf("MISMATCH: %v", err)
			} else {
				t.Logf("OK — rows=%d val=%d", rows, val)
			}
		})
	}
}
