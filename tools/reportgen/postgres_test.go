package main

// PostgreSQL integration tests.
//
// These tests run only when POSTGRES_TEST_DSN is set, e.g.:
//
//	POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/reportgen_test?sslmode=disable" \
//	  go test -v -run TestPG
//
// Each test mirrors an existing SQLite integration test so results can be
// compared directly, ensuring PostgreSQL and SQLite behave identically.

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// pgDSN returns the PostgreSQL DSN from the environment, or skips the test.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set; skipping PostgreSQL tests")
	}
	return dsn
}

// pgSchema holds all CREATE TABLE statements in PostgreSQL syntax.
// Column types differ from SQLite (SERIAL vs INTEGER PRIMARY KEY AUTOINCREMENT,
// TIMESTAMPTZ vs DATETIME, etc.) but the logical schema is identical.
var pgSchema = []string{
	`CREATE TABLE IF NOT EXISTS groups (
		id                  SERIAL PRIMARY KEY,
		name                TEXT NOT NULL,
		daily_token_limit   INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		created_at          TIMESTAMPTZ DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS users (
		id             SERIAL PRIMARY KEY,
		username        TEXT NOT NULL UNIQUE,
		group_id        INTEGER,
		is_active       BOOLEAN DEFAULT TRUE,
		daily_limit     INTEGER DEFAULT 0,
		monthly_limit   INTEGER DEFAULT 0,
		created_at      TIMESTAMPTZ DEFAULT NOW(),
		last_login_at   TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS llm_targets (
		id          SERIAL PRIMARY KEY,
		name        TEXT NOT NULL,
		url         TEXT NOT NULL UNIQUE,
		is_active   BOOLEAN DEFAULT TRUE,
		provider    TEXT DEFAULT 'openai',
		api_key_id  INTEGER,
		created_at  TIMESTAMPTZ DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id                  SERIAL PRIMARY KEY,
		user_id             INTEGER,
		key                 TEXT NOT NULL UNIQUE,
		daily_token_limit   INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		is_active           BOOLEAN DEFAULT TRUE,
		encrypted_value     TEXT,
		created_at          TIMESTAMPTZ DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS usage_logs (
		id            SERIAL PRIMARY KEY,
		request_id    TEXT,
		user_id       TEXT,
		model         TEXT,
		input_tokens  INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		total_tokens  INTEGER DEFAULT 0,
		is_streaming  BOOLEAN DEFAULT FALSE,
		upstream_url  TEXT,
		status_code   INTEGER DEFAULT 200,
		duration_ms   INTEGER DEFAULT 0,
		cost_usd      REAL DEFAULT 0,
		source_node   TEXT,
		synced        BOOLEAN DEFAULT FALSE,
		created_at    TIMESTAMPTZ
	)`,
}

// pgDropSchema removes all test tables (in reverse dependency order).
var pgDropSchema = []string{
	`DROP TABLE IF EXISTS usage_logs`,
	`DROP TABLE IF EXISTS api_keys`,
	`DROP TABLE IF EXISTS llm_targets`,
	`DROP TABLE IF EXISTS users`,
	`DROP TABLE IF EXISTS groups`,
}

// newPGTestDB opens a PostgreSQL connection, wipes and recreates the schema,
// and registers cleanup. Returns the raw *sql.DB and a ready Querier.
func newPGTestDB(t *testing.T) (*sql.DB, *Querier) {
	t.Helper()
	dsn := pgDSN(t)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("pg open: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("pg ping: %v — is PostgreSQL running at %s?", err, dsn)
	}

	// Drop then recreate schema for a clean slate.
	for _, stmt := range pgDropSchema {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("drop schema: %v", err)
		}
	}
	for _, stmt := range pgSchema {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("create schema: %v", err)
		}
	}

	q, err := NewQuerier("postgres", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("NewQuerier postgres: %v", err)
	}

	t.Cleanup(func() {
		q.Close()
		// Best-effort drop; ignore errors.
		for _, stmt := range pgDropSchema {
			db.Exec(stmt)
		}
		db.Close()
	})

	return db, q
}

// mustExecPG executes a statement against a PostgreSQL *sql.DB, failing the test on error.
func mustExecPG(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("pg exec %q: %v", query, err)
	}
}

// ── mirror: TestQueryKPIEmptyDB ───────────────────────────────────────────────

// TestPGQueryKPIEmptyDB mirrors TestQueryKPIEmptyDB for PostgreSQL.
// An empty database must return zero values without error (no NULL-scan crash).
func TestPGQueryKPIEmptyDB(t *testing.T) {
	_, q := newPGTestDB(t)

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI on empty PG DB: %v", err)
	}
	if kpi.TotalRequests != 0 {
		t.Errorf("empty DB: expected 0 requests, got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 0 {
		t.Errorf("empty DB: expected 0 tokens, got %d", kpi.TotalTokens)
	}
	if kpi.ErrorCount != 0 {
		t.Errorf("empty DB: expected 0 errors, got %d", kpi.ErrorCount)
	}
	if kpi.StreamingPct != 0 {
		t.Errorf("empty DB: expected 0 streaming pct, got %f", kpi.StreamingPct)
	}
}

// ── mirror: TestQueryKPIWithData ─────────────────────────────────────────────

// TestPGQueryKPIWithData mirrors TestQueryKPIWithData for PostgreSQL.
// Verifies that request counts, token sums, and error counts match expectations.
func TestPGQueryKPIWithData(t *testing.T) {
	db, q := newPGTestDB(t)

	mustExecPG(t, db, `INSERT INTO groups (id, name) VALUES (1, 'eng')`)
	mustExecPG(t, db, `INSERT INTO users (id, username, group_id, is_active) VALUES (1, 'alice', 1, TRUE)`)
	mustExecPG(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	// Row 1: success, 150 tokens
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens,
		  upstream_url, status_code, duration_ms, cost_usd, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		"r1", "1", "gpt-4o", 100, 50, 150, "https://api.openai.com", 200, 500, 0.001,
		"2026-03-15 10:00:00+00")
	// Row 2: error (429), 300 tokens
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens,
		  upstream_url, status_code, duration_ms, cost_usd, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		"r2", "1", "gpt-4o", 200, 100, 300, "https://api.openai.com", 429, 50, 0.0,
		"2026-03-15 11:00:00+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI PG: %v", err)
	}
	if kpi.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 450 {
		t.Errorf("expected 450 total tokens, got %d", kpi.TotalTokens)
	}
	if kpi.ErrorCount != 1 {
		t.Errorf("expected 1 error request, got %d", kpi.ErrorCount)
	}
}

// ── mirror: TestQueryKPIZeroTokenRequests ────────────────────────────────────

// TestPGQueryKPIZeroTokenRequests mirrors TestQueryKPIZeroTokenRequests for PostgreSQL.
// Zero-token 401 rows must be counted in TotalRequests but not add to TotalTokens/TotalCost.
func TestPGQueryKPIZeroTokenRequests(t *testing.T) {
	db, q := newPGTestDB(t)

	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens,
		  upstream_url, status_code, duration_ms, cost_usd, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		"r1", "1", "gpt-4o", 0, 0, 0, "https://api.openai.com", 401, 30, 0.0,
		"2026-03-15 10:00:00+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI PG zero-token: %v", err)
	}
	if kpi.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 0 {
		t.Errorf("expected 0 tokens, got %d", kpi.TotalTokens)
	}
	if kpi.TotalCost != 0.0 {
		t.Errorf("expected 0.0 cost, got %f", kpi.TotalCost)
	}
	if kpi.ErrorCount != 1 {
		t.Errorf("expected 1 error (401), got %d", kpi.ErrorCount)
	}
}

// ── mirror: TestQueryModelDistributionNullModel ───────────────────────────────

// TestPGQueryModelDistributionNullModel mirrors TestQueryModelDistributionNullModel for PostgreSQL.
// A NULL model with no matching llm_targets entry must fall back to '未知模型'.
func TestPGQueryModelDistributionNullModel(t *testing.T) {
	db, q := newPGTestDB(t)

	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, created_at)
		 VALUES ($1, $2, NULL, $3, $4, $5, $6)`,
		"r1", "1", "https://unknown.example.com", 200, 100, "2026-03-15 10:00:00+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	dist, err := q.QueryModelDistribution(from, to)
	if err != nil {
		t.Fatalf("QueryModelDistribution PG NULL model: %v", err)
	}
	if len(dist) == 0 {
		t.Fatal("expected '未知模型' entry, got empty distribution")
	}
	if dist[0].Model != "未知模型" {
		t.Errorf("expected '未知模型' for NULL model, got %q", dist[0].Model)
	}
}

// ── mirror: TestQueryModelDistributionJoinsLLMTargets ────────────────────────

// TestPGQueryModelDistributionJoinsLLMTargets mirrors the SQLite JOIN test for PostgreSQL.
// The model name must be resolved from llm_targets.name, not from usage_logs.model.
func TestPGQueryModelDistributionJoinsLLMTargets(t *testing.T) {
	db, q := newPGTestDB(t)

	mustExecPG(t, db,
		`INSERT INTO llm_targets (id, name, url, provider) VALUES ($1, $2, $3, $4)`,
		1, "Claude Sonnet", "https://api.anthropic.com", "anthropic")
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"r1", "1", "raw-model-id", "https://api.anthropic.com", 200, 100, "2026-03-15 10:00:00+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	dist, err := q.QueryModelDistribution(from, to)
	if err != nil {
		t.Fatalf("QueryModelDistribution PG: %v", err)
	}
	if len(dist) == 0 {
		t.Fatal("expected at least one model in distribution")
	}
	if dist[0].Model == "raw-model-id" {
		t.Errorf("model distribution shows raw model ID instead of llm_targets.name; got %q", dist[0].Model)
	}
	if dist[0].Model != "Claude Sonnet" {
		t.Errorf("expected 'Claude Sonnet' from llm_targets, got %q", dist[0].Model)
	}
}

// ── PostgreSQL-specific dialect tests ─────────────────────────────────────────

// TestPGDailyTrend verifies QueryDailyTrend returns one row per day for PostgreSQL.
// Also checks the date string format is 'YYYY-MM-DD' (TO_CHAR dialect).
func TestPGDailyTrend(t *testing.T) {
	db, q := newPGTestDB(t)

	// Insert 3 rows across 2 different days
	for i, row := range []struct {
		ts     string
		tokens int
	}{
		{"2026-03-10 10:00:00+00", 100},
		{"2026-03-10 14:00:00+00", 200},
		{"2026-03-11 09:00:00+00", 150},
	} {
		mustExecPG(t, db,
			`INSERT INTO usage_logs (request_id, user_id, model, total_tokens, status_code, duration_ms, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			fmt.Sprintf("r%d", i), "1", "gpt-4o", row.tokens, 200, 300, row.ts)
	}

	from := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)

	rows, err := q.QueryDailyTrend(from, to)
	if err != nil {
		t.Fatalf("QueryDailyTrend PG: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 day rows, got %d", len(rows))
	}
	if rows[0].Date != "2026-03-10" {
		t.Errorf("day[0] date: got %q, want '2026-03-10'", rows[0].Date)
	}
	if rows[0].TotalTokens != 300 {
		t.Errorf("day[0] tokens: got %d, want 300", rows[0].TotalTokens)
	}
	if rows[1].Date != "2026-03-11" {
		t.Errorf("day[1] date: got %q, want '2026-03-11'", rows[1].Date)
	}
	if rows[1].TotalTokens != 150 {
		t.Errorf("day[1] tokens: got %d, want 150", rows[1].TotalTokens)
	}
}

// TestPGHeatmap verifies that QueryHeatmap correctly extracts hour and DOW using
// PostgreSQL's EXTRACT() dialect (not SQLite's strftime).
func TestPGHeatmap(t *testing.T) {
	db, q := newPGTestDB(t)

	// Monday 2026-03-09 at 14:xx UTC: DOW=1 (PostgreSQL EXTRACT(DOW) is 0=Sunday)
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, total_tokens, status_code, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"r1", "1", "gpt-4o", 100, 200, 300, "2026-03-09 14:30:00+00")

	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	cells, err := q.QueryHeatmap(from, to)
	if err != nil {
		t.Fatalf("QueryHeatmap PG: %v", err)
	}
	if len(cells) == 0 {
		t.Fatal("expected heatmap cells, got 0")
	}
	c := cells[0]
	if c.Hour != 14 {
		t.Errorf("heatmap hour: got %d, want 14", c.Hour)
	}
	// Monday = DOW 1 in PostgreSQL EXTRACT(DOW FROM ...) [0=Sunday, 1=Monday...]
	if c.Day != 1 {
		t.Errorf("heatmap dow: got %d, want 1 (Monday)", c.Day)
	}
	if c.Value != 1 {
		t.Errorf("heatmap count: got %d, want 1", c.Value)
	}
}

// TestPGStreamingRatio verifies that QueryStreamingRatio correctly counts
// streaming vs non-streaming requests in PostgreSQL (BOOLEAN semantics differ).
func TestPGStreamingRatio(t *testing.T) {
	db, q := newPGTestDB(t)

	// 2 streaming, 1 non-streaming
	for i, streaming := range []bool{true, true, false} {
		mustExecPG(t, db,
			`INSERT INTO usage_logs (request_id, user_id, model, is_streaming, status_code, duration_ms, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			fmt.Sprintf("r%d", i), "1", "gpt-4o", streaming, 200, 400, "2026-03-15 10:00:00+00")
	}

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	ratio, err := q.QueryStreamingRatio(from, to)
	if err != nil {
		t.Fatalf("QueryStreamingRatio PG: %v", err)
	}
	if ratio.StreamingCount != 2 {
		t.Errorf("streaming count: got %d, want 2", ratio.StreamingCount)
	}
	if ratio.NonStreamingCount != 1 {
		t.Errorf("non-streaming count: got %d, want 1", ratio.NonStreamingCount)
	}
	wantPct := float64(2) / float64(3) * 100
	if ratio.StreamingPct < wantPct-0.1 || ratio.StreamingPct > wantPct+0.1 {
		t.Errorf("streaming pct: got %.2f, want ~%.2f", ratio.StreamingPct, wantPct)
	}
}

// TestPGUpstreamStats verifies that QueryUpstreamStats correctly aggregates
// per-upstream request counts, error rates, and token sums in PostgreSQL.
func TestPGUpstreamStats(t *testing.T) {
	db, q := newPGTestDB(t)

	// upstream-A: 2 requests, 1 error
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"r1", "1", "gpt-4o", "https://upstream-a.example.com", 200, 100, 500, "2026-03-15 10:00:00+00")
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"r2", "1", "gpt-4o", "https://upstream-a.example.com", 500, 0, 100, "2026-03-15 10:01:00+00")
	// upstream-B: 1 success
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"r3", "1", "gpt-4o", "https://upstream-b.example.com", 200, 200, 300, "2026-03-15 10:02:00+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	stats, err := q.QueryUpstreamStats(from, to)
	if err != nil {
		t.Fatalf("QueryUpstreamStats PG: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 upstream entries, got %d", len(stats))
	}
	// Results ordered by request count DESC → upstream-A first
	a := stats[0]
	if a.Requests != 2 {
		t.Errorf("upstream-A requests: got %d, want 2", a.Requests)
	}
	if a.ErrorRate < 49 || a.ErrorRate > 51 {
		t.Errorf("upstream-A error rate: got %.1f%%, want ~50%%", a.ErrorRate)
	}
	if a.TotalTokens != 100 {
		t.Errorf("upstream-A tokens: got %d, want 100", a.TotalTokens)
	}
}

// TestPGTimeRangeBoundary verifies that the half-open interval [from, to) works
// correctly in PostgreSQL — a record exactly at `to` must NOT be included.
func TestPGTimeRangeBoundary(t *testing.T) {
	db, q := newPGTestDB(t)

	// Exactly at boundary (should be excluded)
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, total_tokens, status_code, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"at-boundary", "1", "gpt-4o", 999, 200, 100, "2026-04-01 00:00:00+00")
	// One second before boundary (should be included)
	mustExecPG(t, db,
		`INSERT INTO usage_logs (request_id, user_id, model, total_tokens, status_code, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"before-boundary", "1", "gpt-4o", 100, 200, 100, "2026-03-31 23:59:59+00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI PG boundary: %v", err)
	}
	if kpi.TotalRequests != 1 {
		t.Errorf("expected 1 request (before boundary only), got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 100 {
		t.Errorf("expected 100 tokens (before-boundary row), got %d", kpi.TotalTokens)
	}
}

// TestPGQueryKPIMatchesSQLite is the key parity test:
// seeds identical data into both a SQLite file-DB and a PostgreSQL DB,
// runs QueryKPI on both, and asserts every field is identical.
func TestPGQueryKPIMatchesSQLite(t *testing.T) {
	// ── PostgreSQL side ──
	pgDB, pgQ := newPGTestDB(t)

	rows := []struct {
		rid     string
		tokens  int
		status  int
		stream  bool
		costUSD float64
		durMS   int
		ts      string
	}{
		{"r1", 150, 200, true, 0.001, 500, "2026-03-15 10:00:00"},
		{"r2", 300, 429, false, 0.0, 50, "2026-03-15 11:00:00"},
		{"r3", 500, 200, true, 0.005, 1200, "2026-03-20 09:00:00"},
		{"r4", 0, 401, false, 0.0, 30, "2026-03-25 08:00:00"},
	}

	for _, r := range rows {
		mustExecPG(t, pgDB,
			`INSERT INTO usage_logs
			  (request_id, user_id, model, input_tokens, output_tokens, total_tokens,
			   is_streaming, upstream_url, status_code, duration_ms, cost_usd, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			r.rid, "1", "gpt-4o",
			r.tokens/3, r.tokens-r.tokens/3, r.tokens,
			r.stream, "https://api.openai.com", r.status, r.durMS, r.costUSD,
			r.ts+"+00")
	}

	// ── SQLite side ──
	sqliteDSN := tempDB(t)
	sqliteDB, err := sql.Open("sqlite", sqliteDSN)
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, sqliteDB, stmt)
	}
	for _, r := range rows {
		mustExec(t, sqliteDB,
			`INSERT INTO usage_logs
			  (request_id, user_id, model, input_tokens, output_tokens, total_tokens,
			   is_streaming, upstream_url, status_code, duration_ms, cost_usd, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.rid, "1", "gpt-4o",
			r.tokens/3, r.tokens-r.tokens/3, r.tokens,
			func() int {
				if r.stream {
					return 1
				}
				return 0
			}(),
			"https://api.openai.com", r.status, r.durMS, r.costUSD, r.ts)
	}
	sqliteDB.Close()

	sqliteQ, err := NewQuerier("sqlite", sqliteDSN)
	if err != nil {
		t.Fatalf("NewQuerier sqlite: %v", err)
	}
	defer sqliteQ.Close()

	// ── Compare results ──
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	pgKPI, err := pgQ.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI PG: %v", err)
	}
	sqKPI, err := sqliteQ.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI SQLite: %v", err)
	}

	if pgKPI.TotalRequests != sqKPI.TotalRequests {
		t.Errorf("TotalRequests: PG=%d SQLite=%d", pgKPI.TotalRequests, sqKPI.TotalRequests)
	}
	if pgKPI.TotalTokens != sqKPI.TotalTokens {
		t.Errorf("TotalTokens: PG=%d SQLite=%d", pgKPI.TotalTokens, sqKPI.TotalTokens)
	}
	if pgKPI.ErrorCount != sqKPI.ErrorCount {
		t.Errorf("ErrorCount: PG=%d SQLite=%d", pgKPI.ErrorCount, sqKPI.ErrorCount)
	}
	if pgKPI.StreamingPct != sqKPI.StreamingPct {
		t.Errorf("StreamingPct: PG=%.4f SQLite=%.4f", pgKPI.StreamingPct, sqKPI.StreamingPct)
	}
	// Cost should be equal within floating-point tolerance
	if diff := pgKPI.TotalCost - sqKPI.TotalCost; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCost: PG=%.6f SQLite=%.6f (diff %.6f)", pgKPI.TotalCost, sqKPI.TotalCost, diff)
	}

	t.Logf("Parity OK — TotalRequests=%d TotalTokens=%d ErrorCount=%d StreamingPct=%.4f TotalCost=%.4f",
		pgKPI.TotalRequests, pgKPI.TotalTokens, pgKPI.ErrorCount, pgKPI.StreamingPct, pgKPI.TotalCost)
}

// TestPGNewQuerierDriverField verifies that NewQuerier correctly sets driver="postgres".
func TestPGNewQuerierDriverField(t *testing.T) {
	dsn := pgDSN(t)
	// Ping first — we don't need full schema for this test.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("pg open: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("pg ping failed: %v", err)
	}
	db.Close()

	// Drop/recreate schema so NewQuerier.loadMaps() succeeds.
	_, q := newPGTestDB(t)
	if q.driver != "postgres" {
		t.Errorf("expected driver 'postgres', got %q", q.driver)
	}
}
