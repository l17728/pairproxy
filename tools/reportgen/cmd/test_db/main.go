package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := "sample.db"
	os.Remove(dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")

	db.Exec(`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, group_id INTEGER, is_active BOOLEAN DEFAULT 1, auth_provider TEXT DEFAULT 'local', created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS groups (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, daily_token_limit INTEGER DEFAULT 0, monthly_token_limit INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS usage_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT, user_id TEXT, model TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, is_streaming BOOLEAN DEFAULT 0, upstream_url TEXT, status_code INTEGER DEFAULT 200, duration_ms INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, source_node TEXT, synced BOOLEAN DEFAULT 0, created_at DATETIME)`)

	groups := []struct{ name string; daily int; monthly int }{
		{"engineering", 1000000, 20000000},
		{"design", 500000, 10000000},
		{"product", 800000, 15000000},
	}
	for _, g := range groups {
		db.Exec("INSERT INTO groups (name, daily_token_limit, monthly_token_limit) VALUES (?, ?, ?)", g.name, g.daily, g.monthly)
	}

	users := []struct {
		name    string
		groupID int
		active  bool
		joined  int
	}{
		{"alice", 1, true, 60},
		{"bob", 1, true, 45},
		{"carol", 2, true, 30},
		{"dave", 2, true, 20},
		{"eve", 1, true, 10},
		{"frank", 3, true, 5},
		{"grace", 3, false, 90},
		{"heidi", 1, false, 90},
	}
	for _, u := range users {
		joinDate := time.Now().AddDate(0, 0, -u.joined).Format("2006-01-02 10:00:00")
		active := 0
		if u.active {
			active = 1
		}
		db.Exec("INSERT INTO users (username, group_id, is_active, created_at) VALUES (?, ?, ?, ?)",
			u.name, u.groupID, active, joinDate)
	}

	models := []string{"claude-sonnet-4-5", "claude-opus-4-5", "gpt-4o"}
	upstreams := []string{"https://api.anthropic.com", "https://api.openai.com/v1"}

	rand.Seed(time.Now().UnixNano())

	now := time.Now()
	for day := 0; day < 7; day++ {
		t := now.AddDate(0, 0, -day)
		dateStr := t.Format("2006-01-02")
		userIDs := []int{1, 2, 3, 4, 5, 6}
		reqsPerDay := 15 + day*3 + rand.Intn(10)
		for i := 0; i < reqsPerDay; i++ {
			uid := userIDs[i%len(userIDs)]
			model := models[i%len(models)]
			upstream := upstreams[i%len(upstreams)]
			input := 2000 + rand.Intn(8000)
			output := 800 + rand.Intn(1500)
			total := input + output
			isStream := 0
			if rand.Intn(10) > 2 {
				isStream = 1
			}
			status := 200
			if rand.Intn(100) < 3 {
				status = 429
			}
			if rand.Intn(100) < 1 {
				status = 500
			}
			duration := 300 + rand.Intn(5000)
			cost := float64(total) * 0.003
			hour := 7 + rand.Intn(16)
			minute := rand.Intn(60)
			ts := fmt.Sprintf("%s %02d:%02d:%02d", dateStr, hour, minute, 0)

			db.Exec(`INSERT INTO usage_logs (request_id, user_id, model, input_tokens, output_tokens, total_tokens, is_streaming, upstream_url, status_code, duration_ms, cost_usd, source_node, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				fmt.Sprintf("req-%d-%d", day, i), fmt.Sprintf("%d", uid), model,
				input, output, total, isStream, upstream,
				status, duration, cost, "node-1", ts)
		}
	}
	fmt.Println("OK:", dbPath, "rows:", countLogs(db, "SELECT COUNT(*) FROM usage_logs"))
}

func countLogs(db *sql.DB, query string) int {
	var count int
	db.QueryRow(query).Scan(&count)
	return count
}
