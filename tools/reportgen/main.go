package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	var dbPath, pgDSN, pgHost, pgUser, pgPassword, pgDBName, pgSSLMode string
	var pgPort int
	var fromStr, toStr, outputPath, templatePath string

	// SQLite
	flag.StringVar(&dbPath, "db", "", "SQLite 数据库文件路径（使用 SQLite 时必填）")
	// PostgreSQL — 方案一：完整 DSN
	flag.StringVar(&pgDSN, "pg-dsn", "", "PostgreSQL 完整 DSN，如 postgres://user:pass@host:5432/dbname")
	// PostgreSQL — 方案二：独立字段
	flag.StringVar(&pgHost, "pg-host", "localhost", "PostgreSQL 主机名")
	flag.IntVar(&pgPort, "pg-port", 5432, "PostgreSQL 端口")
	flag.StringVar(&pgUser, "pg-user", "", "PostgreSQL 用户名")
	flag.StringVar(&pgPassword, "pg-password", "", "PostgreSQL 密码")
	flag.StringVar(&pgDBName, "pg-dbname", "", "PostgreSQL 数据库名")
	flag.StringVar(&pgSSLMode, "pg-sslmode", "disable", "PostgreSQL SSL 模式（disable|require|verify-full）")

	flag.StringVar(&fromStr, "from", "", "开始日期，格式 YYYY-MM-DD（必填）")
	flag.StringVar(&toStr, "to", "", "结束日期，格式 YYYY-MM-DD（必填）")
	flag.StringVar(&outputPath, "output", "report.html", "输出 HTML 文件路径")
	flag.StringVar(&templatePath, "template", "templates/report.html", "HTML 模板文件路径")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "PairProxy 报告生成器 — 从数据库生成可视化分析报告\n\n")
		fmt.Fprintf(os.Stderr, "用法（SQLite）:\n")
		fmt.Fprintf(os.Stderr, "  reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07 -output weekly.html\n\n")
		fmt.Fprintf(os.Stderr, "用法（PostgreSQL DSN）:\n")
		fmt.Fprintf(os.Stderr, "  reportgen -pg-dsn \"postgres://user:pass@host:5432/dbname\" -from 2026-04-01 -to 2026-04-07\n\n")
		fmt.Fprintf(os.Stderr, "用法（PostgreSQL 独立字段）:\n")
		fmt.Fprintf(os.Stderr, "  reportgen -pg-host localhost -pg-user app -pg-password secret -pg-dbname pairproxy -from 2026-04-01 -to 2026-04-07\n\n")
		fmt.Fprintf(os.Stderr, "选项:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// 确定驱动与数据源
	driver := "sqlite"
	dsn := ""
	if pgDSN != "" || pgUser != "" || pgDBName != "" {
		driver = "postgres"
		if pgDSN != "" {
			dsn = pgDSN
		} else {
			dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
				pgHost, pgPort, pgUser, pgPassword, pgDBName, pgSSLMode)
		}
	} else {
		dsn = dbPath
	}

	// Validate required flags
	if dsn == "" || fromStr == "" || toStr == "" {
		fmt.Fprintf(os.Stderr, "错误：必须指定数据库（-db 或 -pg-dsn 或 -pg-user/-pg-dbname），以及 -from、-to\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// SQLite: validate file exists
	if driver == "sqlite" && !fileExists(dsn) {
		fmt.Fprintf(os.Stderr, "错误：数据库文件不存在: %s\n", dsn)
		os.Exit(1)
	}

	// Parse dates
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误：无效的开始日期格式: %s（需要 YYYY-MM-DD）\n", fromStr)
		os.Exit(1)
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误：无效的结束日期格式: %s（需要 YYYY-MM-DD）\n", toStr)
		os.Exit(1)
	}
	to = endOfDay(to)

	if !from.Before(to) {
		fmt.Fprintf(os.Stderr, "错误：开始日期必须早于结束日期\n")
		os.Exit(1)
	}

	// Resolve template path
	tmplPath, err := filepath.Abs(templatePath)
	if err != nil {
		tmplPath = templatePath
	}

	// Resolve output path
	outPath, err := filepath.Abs(outputPath)
	if err != nil {
		outPath = outputPath
	}

	params := QueryParams{
		DBPath: dsn,
		DSN:    dsn,
		Driver: driver,
		From:   from,
		To:     to,
	}

	if err := GenerateReport(params, tmplPath, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 报告已生成: %s\n", outPath)
}

func endOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
