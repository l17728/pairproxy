// sqlite2pg: copies all data from a SQLite mock.db into a PostgreSQL database
// for report comparison testing.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

func main() {
	sqlitePath := "mock_compare.db"
	pgDSN := "host=127.0.0.1 port=5433 user=postgres dbname=reportgen_compare sslmode=disable"
	if len(os.Args) > 1 {
		sqlitePath = os.Args[1]
	}
	if len(os.Args) > 2 {
		pgDSN = os.Args[2]
	}

	sqDB, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open sqlite: %v\n", err)
		os.Exit(1)
	}
	defer sqDB.Close()

	pgDB, err := sql.Open("postgres", pgDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open postgres: %v\n", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	if err := pgDB.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "ping postgres: %v\n", err)
		os.Exit(1)
	}

	// Create schema
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS groups (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			daily_token_limit INTEGER DEFAULT 0,
			monthly_token_limit INTEGER DEFAULT 0,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			group_id INTEGER,
			is_active BOOLEAN DEFAULT TRUE,
			auth_provider TEXT DEFAULT 'local',
			daily_limit INTEGER DEFAULT 0,
			monthly_limit INTEGER DEFAULT 0,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			last_login_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS llm_targets (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			url TEXT NOT NULL UNIQUE,
			is_active BOOLEAN DEFAULT TRUE,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id SERIAL PRIMARY KEY,
			user_id INTEGER,
			key TEXT NOT NULL UNIQUE,
			daily_token_limit INTEGER DEFAULT 0,
			monthly_token_limit INTEGER DEFAULT 0,
			is_active BOOLEAN DEFAULT TRUE,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS usage_logs (
			id SERIAL PRIMARY KEY,
			request_id TEXT,
			user_id TEXT,
			model TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			is_streaming BOOLEAN DEFAULT FALSE,
			upstream_url TEXT,
			status_code INTEGER DEFAULT 200,
			duration_ms INTEGER DEFAULT 0,
			cost_usd NUMERIC(12,8) DEFAULT 0,
			source_node TEXT,
			synced BOOLEAN DEFAULT FALSE,
			created_at TIMESTAMPTZ
		)`,
	}

	// Drop and recreate for clean state
	drops := []string{
		`DROP TABLE IF EXISTS usage_logs`,
		`DROP TABLE IF EXISTS api_keys`,
		`DROP TABLE IF EXISTS llm_targets`,
		`DROP TABLE IF EXISTS users`,
		`DROP TABLE IF EXISTS groups`,
	}
	for _, d := range drops {
		if _, err := pgDB.Exec(d); err != nil {
			fmt.Fprintf(os.Stderr, "drop: %v\n", err)
			os.Exit(1)
		}
	}
	for _, d := range ddl {
		if _, err := pgDB.Exec(d); err != nil {
			fmt.Fprintf(os.Stderr, "create: %v\n", err)
			os.Exit(1)
		}
	}

	// Copy groups
	rows, err := sqDB.Query("SELECT id, name, daily_token_limit, monthly_token_limit, created_at FROM groups")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query groups: %v\n", err)
		os.Exit(1)
	}
	n := 0
	for rows.Next() {
		var id int
		var name, createdAt string
		var daily, monthly int
		rows.Scan(&id, &name, &daily, &monthly, &createdAt)
		pgDB.Exec(`INSERT INTO groups (id, name, daily_token_limit, monthly_token_limit, created_at) VALUES ($1,$2,$3,$4,$5)`,
			id, name, daily, monthly, createdAt)
		n++
	}
	rows.Close()
	fmt.Printf("groups: %d rows\n", n)

	// Copy llm_targets
	rows, err = sqDB.Query("SELECT id, name, url, is_active, created_at FROM llm_targets")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query llm_targets: %v\n", err)
		os.Exit(1)
	}
	n = 0
	for rows.Next() {
		var id, isActive int
		var name, url, createdAt string
		rows.Scan(&id, &name, &url, &isActive, &createdAt)
		pgDB.Exec(`INSERT INTO llm_targets (id, name, url, is_active, created_at) VALUES ($1,$2,$3,$4,$5)`,
			id, name, url, isActive == 1, createdAt)
		n++
	}
	rows.Close()
	fmt.Printf("llm_targets: %d rows\n", n)

	// Copy users
	rows, err = sqDB.Query("SELECT id, username, group_id, is_active, daily_limit, monthly_limit, created_at FROM users")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query users: %v\n", err)
		os.Exit(1)
	}
	n = 0
	for rows.Next() {
		var id, isActive, daily, monthly int
		var username, createdAt string
		var groupID sql.NullInt64
		rows.Scan(&id, &username, &groupID, &isActive, &daily, &monthly, &createdAt)
		var gid interface{}
		if groupID.Valid {
			gid = groupID.Int64
		}
		pgDB.Exec(`INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, username, gid, isActive == 1, daily, monthly, createdAt)
		n++
	}
	rows.Close()
	fmt.Printf("users: %d rows\n", n)

	// Copy api_keys
	rows, err = sqDB.Query("SELECT id, user_id, key, daily_token_limit, monthly_token_limit, is_active, created_at FROM api_keys")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query api_keys: %v\n", err)
		os.Exit(1)
	}
	n = 0
	for rows.Next() {
		var id, userID, isActive, daily, monthly int
		var key, createdAt string
		rows.Scan(&id, &userID, &key, &daily, &monthly, &isActive, &createdAt)
		pgDB.Exec(`INSERT INTO api_keys (id, user_id, key, daily_token_limit, monthly_token_limit, is_active, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, userID, key, daily, monthly, isActive == 1, createdAt)
		n++
	}
	rows.Close()
	fmt.Printf("api_keys: %d rows\n", n)

	// Copy usage_logs row by row (using individual execs to handle invalid rows gracefully)
	rows, err = sqDB.Query(`SELECT request_id, user_id, model, input_tokens, output_tokens, total_tokens,
		is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at
		FROM usage_logs ORDER BY id`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query usage_logs: %v\n", err)
		os.Exit(1)
	}

	n = 0
	skipped := 0
	for rows.Next() {
		var reqID, userID, upstreamURL, createdAt string
		var model, source sql.NullString
		var inp, out, total, isStreaming, status, dur int
		var cost float64
		rows.Scan(&reqID, &userID, &model, &inp, &out, &total,
			&isStreaming, &upstreamURL, &status, &dur, &cost, &source, &createdAt)

		var modelVal interface{}
		if model.Valid {
			modelVal = model.String
		}
		var srcVal interface{}
		if source.Valid {
			srcVal = source.String
		}

		// SQLite stores timestamps as "YYYY-MM-DD HH:MM:SS" (UTC, no zone)
		// Clamp invalid hours (e.g. "30:00:00" -> "06:00:00") then append UTC offset
		tsStr := createdAt
		if len(tsStr) == 19 {
			// Parse and clamp hour if out of range
			var ymd, hms string
			if n2, _ := fmt.Sscanf(tsStr, "%10s %8s", &ymd, &hms); n2 == 2 {
				var h, m, s int
				fmt.Sscanf(hms, "%d:%d:%d", &h, &m, &s)
				h = h % 24
				tsStr = fmt.Sprintf("%s %02d:%02d:%02d+00", ymd, h, m, s)
			} else {
				tsStr += "+00"
			}
		}

		_, execErr := pgDB.Exec(`INSERT INTO usage_logs
			(request_id, user_id, model, input_tokens, output_tokens, total_tokens,
			 is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			reqID, userID, modelVal, inp, out, total,
			isStreaming == 1, upstreamURL, status, dur, cost, srcVal, tsStr)
		if execErr != nil {
			skipped++
			if skipped <= 3 {
				fmt.Fprintf(os.Stderr, "  skip row (ts=%s): %v\n", tsStr, execErr)
			}
		} else {
			n++
		}
		if (n+skipped)%1000 == 0 {
			fmt.Printf("  usage_logs: inserted=%d skipped=%d...\n", n, skipped)
		}
	}
	rows.Close()
	fmt.Printf("usage_logs: inserted=%d skipped=%d\n", n, skipped)
	fmt.Println("Done.")
	fmt.Printf("usage_logs: %d rows\n", n)
	fmt.Println("Done.")
}
