package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newTestDB opens an in-memory SQLite DB with the minimal schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `CREATE TABLE groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		daily_token_limit INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		group_id INTEGER,
		is_active BOOLEAN DEFAULT 1,
		daily_limit INTEGER DEFAULT 0,
		monthly_limit INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_login_at DATETIME
	)`)
	mustExec(t, db, `CREATE TABLE llm_targets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		url TEXT NOT NULL UNIQUE,
		is_active BOOLEAN DEFAULT 1,
		provider TEXT DEFAULT 'openai',
		api_key_id INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		key TEXT NOT NULL UNIQUE,
		daily_token_limit INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		is_active BOOLEAN DEFAULT 1,
		encrypted_value TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE usage_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id TEXT,
		user_id TEXT,
		model TEXT,
		input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		is_streaming BOOLEAN DEFAULT 0,
		upstream_url TEXT,
		status_code INTEGER DEFAULT 200,
		duration_ms INTEGER DEFAULT 0,
		cost_usd REAL DEFAULT 0,
		source_node TEXT,
		synced BOOLEAN DEFAULT 0,
		created_at DATETIME
	)`)
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// seedBasicData populates the DB with one group, one user, one llm_target,
// and one usage_log in the given date range.
func seedBasicData(t *testing.T, db *sql.DB, ts string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO groups (id, name) VALUES (1, '工程团队')`)
	mustExec(t, db, `INSERT INTO users (id, username, group_id, is_active) VALUES (1, 'alice', 1, 1)`)
	mustExec(t, db, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	mustExec(t, db, `INSERT INTO usage_logs
		(request_id, user_id, model, input_tokens, output_tokens, total_tokens,
		 is_streaming, upstream_url, status_code, duration_ms, cost_usd, created_at)
		VALUES ('req-001', '1', 'gpt-4o', 100, 50, 150, 1, 'https://api.openai.com', 200, 500, 0.001, ?)`,
		ts)
}

func tempDB(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "reportgen-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	f.Close()
	os.Remove(f.Name()) // let SQLite recreate it
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

// ── endOfDay date defaulting ─────────────────────────────────────────────────

func TestEndOfDayTimezone(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	in := time.Date(2026, 3, 31, 0, 0, 0, 0, cst)
	got := endOfDay(in)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, cst)
	if !got.Equal(want) {
		t.Errorf("endOfDay CST: got %v, want %v", got, want)
	}
}

func TestEndOfDaySameDayRange(t *testing.T) {
	// from == to (same day) should be valid: to becomes next midnight
	from := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)
	to := endOfDay(time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC))
	if !from.Before(to) {
		t.Errorf("same-day range should be valid after endOfDay: from=%v to=%v", from, to)
	}
}

// ── isContextTooLong ──────────────────────────────────────────────────────────

func TestIsContextTooLong(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		expect bool
	}{
		{"nil error", nil, false},
		{"non-llmError", fmt.Errorf("network error"), false},
		{"HTTP 413", &llmError{status: 413, body: "payload too large"}, true},
		{"HTTP 400 context window", &llmError{status: 400, body: `{"error":"context window exceeded"}`}, true},
		{"HTTP 400 context_length_exceeded", &llmError{status: 400, body: `{"error":{"code":"context_length_exceeded"}}`}, true},
		{"HTTP 400 maximum context length", &llmError{status: 400, body: "maximum context length is 128000"}, true},
		{"HTTP 400 too many tokens", &llmError{status: 400, body: "too many tokens"}, true},
		{"HTTP 400 reduce the length", &llmError{status: 400, body: "Please reduce the length"}, true},
		{"HTTP 400 unrelated error", &llmError{status: 400, body: "invalid model"}, false},
		{"HTTP 401", &llmError{status: 401, body: "invalid api key"}, false},
		{"HTTP 500", &llmError{status: 500, body: "internal server error"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isContextTooLong(c.err)
			if got != c.expect {
				t.Errorf("isContextTooLong(%v) = %v, want %v", c.err, got, c.expect)
			}
		})
	}
}

// ── LLM provider routing ──────────────────────────────────────────────────────

// TestLLMRoutingOpenAI verifies the OpenAI path hits /v1/chat/completions.
func TestLLMRoutingOpenAI(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	target := &llmTarget{URL: srv.URL, APIKey: "sk-test", Provider: "openai", Model: "gpt-4o-mini"}
	result, err := callLLM(target, &ReportData{}, false)
	if err != nil {
		t.Fatalf("callLLM openai: %v", err)
	}
	if result != "ok" {
		t.Errorf("unexpected result: %q", result)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("wrong endpoint: got %q, want /v1/chat/completions", gotPath)
	}
}

// TestLLMRoutingAnthropic verifies Anthropic path hits /v1/messages even with model set.
func TestLLMRoutingAnthropic(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"anthropic ok"}]}`)
	}))
	defer srv.Close()

	// Anthropic target WITH a model override — previously this would wrongly route to OpenAI
	target := &llmTarget{URL: srv.URL, APIKey: "sk-ant-test", Provider: "anthropic", Model: "claude-haiku-4-5"}
	result, err := callLLM(target, &ReportData{}, false)
	if err != nil {
		t.Fatalf("callLLM anthropic with model: %v", err)
	}
	if result != "anthropic ok" {
		t.Errorf("unexpected result: %q", result)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("wrong endpoint: got %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "sk-ant-test" {
		t.Errorf("x-api-key not set correctly: %q", gotAPIKey)
	}
}

// TestLLMRoutingAnthropicNoModel verifies Anthropic with no model still routes correctly.
func TestLLMRoutingAnthropicNoModel(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"default model ok"}]}`)
	}))
	defer srv.Close()

	target := &llmTarget{URL: srv.URL, APIKey: "sk-ant", Provider: "anthropic", Model: ""}
	_, err := callLLM(target, &ReportData{}, false)
	if err != nil {
		t.Fatalf("callLLM anthropic no model: %v", err)
	}
	// Verify default model was sent (not the old invalid ID)
	if model, ok := gotBody["model"].(string); ok {
		if model == "claude-haiku-4-5-20251001" {
			t.Error("default model is still the invalid 'claude-haiku-4-5-20251001' ID")
		}
		if model != "claude-haiku-4-5" {
			t.Errorf("unexpected default model: %q, want claude-haiku-4-5", model)
		}
	}
}

// TestLLMSkipOnNoKey verifies GenerateLLMInsights returns nil when no key is available.
func TestLLMSkipOnNoKey(t *testing.T) {
	os.Unsetenv("KEY_ENCRYPTION_KEY")
	params := QueryParams{Driver: "sqlite", DSN: ":memory:"}
	result := GenerateLLMInsights(&ReportData{}, params)
	if result != nil {
		t.Errorf("expected nil insight when no key available, got: %+v", result)
	}
}

// TestLLMHTTP4xxLogged verifies HTTP errors produce nil insight (not a panic).
func TestLLMHTTP4xxLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"invalid api key"}`)
	}))
	defer srv.Close()

	params := QueryParams{LLMURL: srv.URL, LLMKey: "bad-key", LLMModel: "gpt-4o-mini"}
	result := GenerateLLMInsights(&ReportData{}, params)
	if result != nil {
		t.Errorf("expected nil on 401 error, got insight: %v", result.Detail)
	}
}

// TestLLMContextTooLongRetry verifies that a 413 triggers a strip-and-retry via GenerateLLMInsights.
func TestLLMContextTooLongRetry(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(413)
			fmt.Fprint(w, "payload too large")
			return
		}
		// Second call succeeds
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"retried ok"}}]}`)
	}))
	defer srv.Close()

	// Use GenerateLLMInsights which contains the retry logic
	params := QueryParams{LLMURL: srv.URL, LLMKey: "sk-test", LLMModel: "gpt-4o-mini"}
	data := &ReportData{ErrorRequests: make([]ErrorRequestRow, 100)}
	insight := GenerateLLMInsights(data, params)
	if insight == nil {
		t.Fatalf("expected insight after retry, got nil (callCount=%d)", callCount)
	}
	if insight.Detail != "retried ok" {
		t.Errorf("unexpected insight detail: %q", insight.Detail)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (original + retry), got %d", callCount)
	}
}

// ── integration: queries against in-memory SQLite ────────────────────────────

func TestQueryKPIEmptyDB(t *testing.T) {
	db := newTestDB(t)
	dsn := tempDB(t)
	// Write schema to file-based DB for Querier
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	defer fileDB.Close()
	// Copy schema from in-memory to file
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		if _, err := fileDB.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	_ = db // suppress unused warning

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI on empty DB: %v", err)
	}
	if kpi.TotalRequests != 0 {
		t.Errorf("empty DB: expected 0 requests, got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 0 {
		t.Errorf("empty DB: expected 0 tokens, got %d", kpi.TotalTokens)
	}
}

func TestQueryKPIWithData(t *testing.T) {
	dsn := tempDB(t)
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		if _, err := fileDB.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	// Seed data
	mustExec(t, fileDB, `INSERT INTO groups (id, name) VALUES (1, 'eng')`)
	mustExec(t, fileDB, `INSERT INTO users (id, username, group_id, is_active) VALUES (1, 'alice', 1, 1)`)
	mustExec(t, fileDB, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'GPT-4o', 'https://api.openai.com', 'openai')`)
	mustExec(t, fileDB, `INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, upstream_url, status_code, duration_ms, cost_usd, created_at)
		VALUES ('r1', '1', 'gpt-4o', 100, 50, 150, 'https://api.openai.com', 200, 500, 0.001, '2026-03-15 10:00:00')`)
	mustExec(t, fileDB, `INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, upstream_url, status_code, duration_ms, cost_usd, created_at)
		VALUES ('r2', '1', 'gpt-4o', 200, 100, 300, 'https://api.openai.com', 429, 50, 0.0, '2026-03-15 11:00:00')`)
	fileDB.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI: %v", err)
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

func TestQueryModelDistributionJoinsLLMTargets(t *testing.T) {
	dsn := tempDB(t)
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, fileDB, stmt)
	}
	mustExec(t, fileDB, `INSERT INTO llm_targets (id, name, url, provider) VALUES (1, 'Claude Sonnet', 'https://api.anthropic.com', 'anthropic')`)
	// Insert with URL in upstream_url — name should be resolved via JOIN
	mustExec(t, fileDB, `INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, created_at)
		VALUES ('r1', '1', 'raw-model-id', 'https://api.anthropic.com', 200, 100, '2026-03-15 10:00:00')`)
	fileDB.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	dist, err := q.QueryModelDistribution(from, to)
	if err != nil {
		t.Fatalf("QueryModelDistribution: %v", err)
	}
	if len(dist) == 0 {
		t.Fatal("expected at least one model in distribution")
	}
	// The model name should be the friendly name from llm_targets, not the raw model ID
	if dist[0].Model == "raw-model-id" {
		t.Errorf("model distribution shows raw model ID instead of llm_targets.name; got %q", dist[0].Model)
	}
	if !strings.Contains(dist[0].Model, "Claude Sonnet") {
		t.Errorf("expected 'Claude Sonnet' from llm_targets, got %q", dist[0].Model)
	}
}

func TestQueryModelDistributionNullModel(t *testing.T) {
	dsn := tempDB(t)
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, fileDB, stmt)
	}
	// NULL model AND no matching llm_targets — should fall back to '未知模型'
	mustExec(t, fileDB, `INSERT INTO usage_logs (request_id, user_id, model, upstream_url, status_code, total_tokens, created_at)
		VALUES ('r1', '1', NULL, 'https://unknown.example.com', 200, 100, '2026-03-15 10:00:00')`)
	fileDB.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	dist, err := q.QueryModelDistribution(from, to)
	if err != nil {
		t.Fatalf("QueryModelDistribution with NULL model: %v", err)
	}
	if len(dist) == 0 {
		t.Fatal("expected '未知模型' entry, got empty distribution")
	}
	if dist[0].Model != "未知模型" {
		t.Errorf("expected '未知模型' for NULL model, got %q", dist[0].Model)
	}
}

func TestQueryKPIZeroTokenRequests(t *testing.T) {
	dsn := tempDB(t)
	fileDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		mustExec(t, fileDB, stmt)
	}
	// Zero-token 401 request
	mustExec(t, fileDB, `INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, upstream_url, status_code, duration_ms, cost_usd, created_at)
		VALUES ('r1', '1', 'gpt-4o', 0, 0, 0, 'https://api.openai.com', 401, 30, 0.0, '2026-03-15 10:00:00')`)
	fileDB.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier: %v", err)
	}
	defer q.Close()

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	kpi, err := q.QueryKPI(from, to)
	if err != nil {
		t.Fatalf("QueryKPI with zero-token request: %v", err)
	}
	if kpi.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", kpi.TotalRequests)
	}
	if kpi.TotalTokens != 0 {
		t.Errorf("expected 0 tokens for zero-token request, got %d", kpi.TotalTokens)
	}
	if kpi.TotalCost != 0.0 {
		t.Errorf("expected 0 cost for 401 request, got %f", kpi.TotalCost)
	}
}

// ── driver selection ─────────────────────────────────────────────────────────

func TestNewQuerierDriverDefault(t *testing.T) {
	dsn := tempDB(t)
	// Create minimal schema
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, s := range []string{
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, name TEXT, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, group_id INTEGER, is_active BOOLEAN DEFAULT 1, daily_limit INTEGER DEFAULT 0, monthly_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`,
		`CREATE TABLE llm_targets (id INTEGER PRIMARY KEY, name TEXT, url TEXT UNIQUE, is_active BOOLEAN DEFAULT 1, provider TEXT, api_key_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, user_id INTEGER, key TEXT UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, is_active BOOLEAN DEFAULT 1, encrypted_value TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`,
	} {
		db.Exec(s)
	}
	db.Close()

	q, err := NewQuerier("sqlite", dsn)
	if err != nil {
		t.Fatalf("NewQuerier sqlite: %v", err)
	}
	defer q.Close()
	if q.driver != "sqlite" {
		t.Errorf("expected driver 'sqlite', got %q", q.driver)
	}
}

func TestNewQuerierInvalidDSN(t *testing.T) {
	_, err := NewQuerier("sqlite", "/nonexistent/path/that/cannot/exist/db.sqlite")
	if err == nil {
		t.Error("expected error for nonexistent DB path, got nil")
	}
}

// ── safeKeyPrefix ─────────────────────────────────────────────────────────────

func TestSafeKeyPrefix(t *testing.T) {
	cases := []struct {
		key    string
		n      int
		expect string
	}{
		{"", 8, "<empty>"},
		{"sk-abc", 8, "sk-abc"},           // 6 chars, shorter than n — returned as-is
		{"sk-abcde", 8, "sk-abcde"},       // exactly n chars — returned as-is
		{"sk-abcdefg", 8, "sk-abcde"},     // 10 chars, first 8 = "sk-abcde"
		{"sk-abcdefghij", 8, "sk-abcde"},  // 13 chars, first 8 = "sk-abcde"
	}
	for _, c := range cases {
		got := safeKeyPrefix(c.key, c.n)
		if got != c.expect {
			t.Errorf("safeKeyPrefix(%q, %d) = %q, want %q", c.key, c.n, got, c.expect)
		}
	}
}

// ── truncate ──────────────────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	cases := []struct {
		s      string
		maxLen int
		expect string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello...(truncated)"},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc...(truncated)"},
	}
	for _, c := range cases {
		got := truncate(c.s, c.maxLen)
		if got != c.expect {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.maxLen, got, c.expect)
		}
	}
}
