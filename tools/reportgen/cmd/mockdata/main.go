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
	dbPath := "mock.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}
	os.Remove(dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")

	// ── Schema ──────────────────────────────────────────────────────────────
	db.Exec(`CREATE TABLE IF NOT EXISTS groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		daily_token_limit INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		group_id INTEGER,
		is_active BOOLEAN DEFAULT 1,
		auth_provider TEXT DEFAULT 'local',
		daily_limit INTEGER DEFAULT 0,
		monthly_limit INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_login_at DATETIME
	)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS llm_targets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		url TEXT NOT NULL UNIQUE,
		is_active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		key TEXT NOT NULL UNIQUE,
		daily_token_limit INTEGER DEFAULT 0,
		monthly_token_limit INTEGER DEFAULT 0,
		is_active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS usage_logs (
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

	// ── Groups ───────────────────────────────────────────────────────────────
	type group struct {
		name    string
		daily   int
		monthly int
	}
	groups := []group{
		{"工程团队", 2000000, 40000000},
		{"产品团队", 1000000, 20000000},
		{"设计团队", 500000, 10000000},
		{"运营团队", 800000, 15000000},
	}
	for _, g := range groups {
		db.Exec("INSERT INTO groups (name, daily_token_limit, monthly_token_limit) VALUES (?,?,?)",
			g.name, g.daily, g.monthly)
	}

	// ── LLM Targets ──────────────────────────────────────────────────────────
	type target struct {
		name string
		url  string
	}
	targets := []target{
		{"Claude Sonnet", "https://api.anthropic.com/claude-sonnet"},
		{"Claude Opus", "https://api.anthropic.com/claude-opus"},
		{"GPT-4o", "https://api.openai.com/v1/gpt4o"},
		{"DeepSeek-V3", "https://api.deepseek.com/v1"},
	}
	for _, t := range targets {
		db.Exec("INSERT INTO llm_targets (name, url) VALUES (?,?)", t.name, t.url)
	}

	// ── Users ────────────────────────────────────────────────────────────────
	type user struct {
		name    string
		groupID int
		active  bool
		dayBack int
	}
	users := []user{
		{"zhang.wei", 1, true, 90},
		{"li.na", 1, true, 80},
		{"wang.fang", 1, true, 75},
		{"chen.jing", 1, true, 60},
		{"liu.yang", 2, true, 55},
		{"zhao.lei", 2, true, 50},
		{"sun.min", 2, true, 45},
		{"zhou.tao", 2, true, 40},
		{"wu.xia", 3, true, 35},
		{"zheng.hao", 3, true, 30},
		{"wang.rui", 3, true, 25},
		{"li.xue", 3, false, 20},
		{"chen.dong", 4, true, 15},
		{"liu.jie", 4, true, 10},
		{"zhang.ying", 4, true, 8},
		{"zhao.ming", 4, false, 90},
		{"qian.lin", 1, true, 5},
		{"sun.bo", 2, true, 3},
		{"zhou.yan", 1, true, 2},
		{"wu.gang", 3, true, 1},
	}
	for i, u := range users {
		joinDate := time.Now().AddDate(0, 0, -u.dayBack).Format("2006-01-02 09:00:00")
		active := 0
		if u.active {
			active = 1
		}
		db.Exec("INSERT INTO users (id, username, group_id, is_active, daily_limit, monthly_limit, created_at) VALUES (?,?,?,?,?,?,?)",
			i+1, u.name, u.groupID, active, 500000, 10000000, joinDate)
		db.Exec("INSERT INTO api_keys (user_id, key, daily_token_limit, monthly_token_limit) VALUES (?,?,?,?)",
			i+1, fmt.Sprintf("sk-mock-%04d", i+1), 500000, 10000000)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	now := time.Now()

	// upstream_url → cost multipliers (per token)
	type modelInfo struct {
		url         string
		inputCost   float64 // per 1k tokens
		outputCost  float64
		avgLatency  int // ms base
		latencyVar  int
	}
	modelInfoMap := map[string]modelInfo{
		"https://api.anthropic.com/claude-sonnet": {"https://api.anthropic.com/claude-sonnet", 0.003, 0.015, 800, 400},
		"https://api.anthropic.com/claude-opus":   {"https://api.anthropic.com/claude-opus", 0.015, 0.075, 1500, 800},
		"https://api.openai.com/v1/gpt4o":         {"https://api.openai.com/v1/gpt4o", 0.005, 0.015, 600, 300},
		"https://api.deepseek.com/v1":             {"https://api.deepseek.com/v1", 0.0002, 0.0006, 400, 200},
	}
	// upstreamToModel maps upstream URL to short model name (mirrors what the proxy writes)
	upstreamToModel := map[string]string{
		"https://api.anthropic.com/claude-sonnet": "claude-sonnet-4-5",
		"https://api.anthropic.com/claude-opus":   "claude-opus-4-6",
		"https://api.openai.com/v1/gpt4o":         "gpt-4o",
		"https://api.deepseek.com/v1":             "deepseek-v3",
	}

	upstreamURLs := []string{
		"https://api.anthropic.com/claude-sonnet",
		"https://api.anthropic.com/claude-opus",
		"https://api.openai.com/v1/gpt4o",
		"https://api.deepseek.com/v1",
	}

	// user → upstream preference weight (some users prefer certain models)
	// user index → upstream index weights
	userUpstreamWeight := [][]int{
		{5, 1, 2, 2}, // zhang.wei: mostly sonnet
		{3, 3, 2, 2}, // li.na: balanced
		{2, 1, 4, 3}, // wang.fang: prefers gpt-4o and deepseek
		{4, 2, 2, 2},
		{1, 1, 5, 3}, // liu.yang: prefers gpt-4o
		{3, 1, 3, 3},
		{2, 2, 2, 4}, // prefers deepseek
		{4, 2, 2, 2},
		{1, 5, 2, 2}, // wu.xia: prefers opus
		{3, 2, 3, 2},
		{2, 1, 3, 4},
		{3, 1, 3, 3},
		{4, 2, 2, 2},
		{2, 1, 4, 3},
		{3, 1, 3, 3},
		{2, 2, 3, 3},
		{5, 1, 2, 2},
		{3, 2, 3, 2},
		{2, 1, 3, 4},
		{4, 1, 3, 2},
	}

	// Build cumulative weight table
	pickUpstream := func(userIdx int) string {
		weights := userUpstreamWeight[userIdx%len(userUpstreamWeight)]
		total := 0
		for _, w := range weights {
			total += w
		}
		r := rng.Intn(total)
		cum := 0
		for i, w := range weights {
			cum += w
			if r < cum {
				return upstreamURLs[i]
			}
		}
		return upstreamURLs[0]
	}

	// Activity pattern: base requests per user per day based on role
	// Engineers use more, design less
	userDailyBase := []int{
		40, 35, 30, 28, // engineering heavy users
		20, 18, 16, 14, // product
		10, 8, 7, 5,    // design
		12, 10, 8, 6,   // ops
		25, 20, 22, 15, // newer users
	}

	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO usage_logs
		(request_id,user_id,model,input_tokens,output_tokens,total_tokens,
		 is_streaming,upstream_url,status_code,duration_ms,cost_usd,source_node,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)

	reqID := 0

	// Generate 30 days of data
	for day := 29; day >= 0; day-- {
		date := now.AddDate(0, 0, -day)
		dateStr := date.Format("2006-01-02")

		// Only active users generate traffic
		for uIdx, u := range users {
			if !u.active && day > 5 {
				continue // inactive users have no recent activity
			}

			// Vary daily activity: weekends lower
			weekday := date.Weekday()
			weekdayMult := 1.0
			if weekday == time.Saturday || weekday == time.Sunday {
				weekdayMult = 0.3
			}

			base := userDailyBase[uIdx]
			dailyReqs := int(float64(base)*weekdayMult) + rng.Intn(5)
			if dailyReqs < 1 {
				dailyReqs = 1
			}

			uid := fmt.Sprintf("%d", uIdx+1)
			sourceNode := "node-1"
			if uIdx%3 == 0 {
				sourceNode = "node-2"
			}

			for i := 0; i < dailyReqs; i++ {
				upstream := pickUpstream(uIdx)
				mi := modelInfoMap[upstream]

				// Token distribution: power-law-ish
				inputTokens := 500 + rng.Intn(8000)
				if rng.Intn(10) == 0 {
					inputTokens += rng.Intn(20000) // occasional large context
				}
				outputTokens := 200 + rng.Intn(2000)
				if rng.Intn(20) == 0 {
					outputTokens += rng.Intn(3000)
				}
				totalTokens := inputTokens + outputTokens

				isStreaming := 0
				if rng.Intn(10) < 7 { // 70% streaming
					isStreaming = 1
				}

				// Status: mostly 200, occasional errors
				status := 200
				errRoll := rng.Intn(100)
				if errRoll < 2 {
					status = 429
				} else if errRoll < 3 {
					status = 500
				} else if errRoll < 4 {
					status = 503
				} else if errRoll == 4 {
					status = 401
				}

				// Latency
				latency := mi.avgLatency + rng.Intn(mi.latencyVar)
				if status == 500 || status == 503 {
					latency = 3000 + rng.Intn(5000)
				} else if status == 429 {
					latency = 50 + rng.Intn(100)
				}
				// Add token-proportional latency
				latency += totalTokens / 100

				cost := float64(inputTokens)/1000*mi.inputCost + float64(outputTokens)/1000*mi.outputCost
				if status != 200 && status != 201 && status != 204 {
					cost = 0
				}

				// Spread across working hours with realistic distribution
				var hour int
				h := rng.Intn(100)
				switch {
				case h < 5:
					hour = rng.Intn(8) // night owl
				case h < 30:
					hour = 9 + rng.Intn(3) // morning peak
				case h < 60:
					hour = 13 + rng.Intn(4) // afternoon peak
				case h < 85:
					hour = 17 + rng.Intn(3) // evening
				default:
					hour = 20 + rng.Intn(4) // late night
				}
				minute := rng.Intn(60)
				second := rng.Intn(60)
				ts := fmt.Sprintf("%s %02d:%02d:%02d", dateStr, hour%24, minute, second)

				reqID++
				if _, err := stmt.Exec(
					fmt.Sprintf("req-%08d", reqID),
					uid,
					// model field: use model short name (matches what real proxy writes)
					upstreamToModel[upstream],
					inputTokens, outputTokens, totalTokens,
					isStreaming, upstream,
					status, latency, cost,
					sourceNode, ts,
				); err != nil {
					fmt.Fprintf(os.Stderr, "warn: insert req-%08d failed: %v\n", reqID, err)
				}
			}
		}

		// Inject realistic error bursts on specific days
		if day == 3 {
			// Rate limit storm on upstream claude-opus
			for j := 0; j < 25; j++ {
				reqID++
				ts := fmt.Sprintf("%s 14:%02d:%02d", dateStr, rng.Intn(60), rng.Intn(60))
				stmt.Exec(fmt.Sprintf("req-%08d", reqID), "1",
					"https://api.anthropic.com/claude-opus", 300, 0, 300,
					0, "https://api.anthropic.com/claude-opus",
					429, 80+rng.Intn(50), 0.0, "node-1", ts)
			}
		}
		if day == 7 {
			// OpenAI 503 outage window
			for j := 0; j < 15; j++ {
				reqID++
				ts := fmt.Sprintf("%s 03:%02d:%02d", dateStr, rng.Intn(60), rng.Intn(60))
				stmt.Exec(fmt.Sprintf("req-%08d", reqID), "3",
					"https://api.openai.com/v1/gpt4o", 200, 0, 200,
					0, "https://api.openai.com/v1/gpt4o",
					503, 5000+rng.Intn(3000), 0.0, "node-2", ts)
			}
		}
	}

	// Edge case: zero-token requests (rejected before processing, e.g. auth failures)
	for j := 0; j < 20; j++ {
		reqID++
		ts := now.AddDate(0, 0, -rng.Intn(30)).Format("2006-01-02") + " 10:00:00"
		if _, err := stmt.Exec(fmt.Sprintf("req-%08d", reqID), "1",
			"claude-sonnet-4-5", 0, 0, 0,
			0, "https://api.anthropic.com/claude-sonnet",
			401, 30+rng.Intn(20), 0.0, "node-1", ts); err != nil {
			fmt.Fprintf(os.Stderr, "warn: zero-token insert failed: %v\n", err)
		}
	}
	// Edge case: NULL model field (proxy may not always capture model)
	for j := 0; j < 10; j++ {
		reqID++
		ts := now.AddDate(0, 0, -rng.Intn(30)).Format("2006-01-02") + " 11:00:00"
		if _, err := stmt.Exec(fmt.Sprintf("req-%08d", reqID), "2",
			nil, 100, 50, 150,
			0, "https://api.deepseek.com/v1",
			200, 400+rng.Intn(200), 0.0002*150/1000, "node-1", ts); err != nil {
			fmt.Fprintf(os.Stderr, "warn: null-model insert failed: %v\n", err)
		}
	}

	stmt.Close()
	tx.Commit()

	var total int
	db.QueryRow("SELECT COUNT(*) FROM usage_logs").Scan(&total)
	var errCount int
	db.QueryRow("SELECT COUNT(*) FROM usage_logs WHERE status_code NOT IN (200,201,204)").Scan(&errCount)
	var totalTokens int64
	db.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM usage_logs").Scan(&totalTokens)

	fmt.Printf("✅ 数据库生成完毕: %s\n", dbPath)
	fmt.Printf("   总请求数: %d（其中错误 %d 条）\n", total, errCount)
	fmt.Printf("   总 Token: %d\n", totalTokens)
	fmt.Printf("   用户数: %d，分组数: %d，上游数: %d\n", len(users), len(groups), len(targets))
}
