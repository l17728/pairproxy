package db

import (
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/l17728/pairproxy/internal/config"
)

// Open 打开 SQLite 数据库连接（使用内置默认连接池，适合测试和简单场景）。
// path 为 SQLite 文件路径，":memory:" 表示内存数据库（测试用）。
// 生产环境请使用 OpenWithConfig 以传入完整连接池配置。
func Open(logger *zap.Logger, path string) (*gorm.DB, error) {
	return OpenWithConfig(logger, config.DatabaseConfig{Path: path})
}

// OpenWithConfig 打开 SQLite 数据库连接，支持完整的连接池配置。
//
// 连接池默认值（cfg 字段为零值时生效）：
//   - MaxOpenConns:    25（":memory:" 时为 1）
//   - MaxIdleConns:    10
//   - ConnMaxLifetime: 1h
//   - ConnMaxIdleTime: 10m
//
// SQLite WAL 模式允许多个并发读连接（但写操作仍需排他锁）。
// 设置 MaxOpenConns > 1 可充分利用 WAL 并发读能力；
// busy_timeout=5000 保证写锁争用时读操作等待而非立即报错。
func OpenWithConfig(logger *zap.Logger, cfg config.DatabaseConfig) (*gorm.DB, error) {
	logger = logger.Named("db")
	path := cfg.Path
	logger.Info("opening SQLite database", zap.String("path", path))

	// 将 zap logger 适配为 gorm logger（仅输出 error 级别，减少噪音）
	gormLog := gormlogger.Default.LogMode(gormlogger.Silent)

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormLog,
	})
	if err != nil {
		logger.Error("failed to open database",
			zap.String("path", path),
			zap.Error(err),
		)
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}

	// 获取底层 sql.DB 以配置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql.DB: %w", err)
	}

	// ── 连接池参数 ──────────────────────────────────────────────────────────
	// ":memory:" 每个连接拥有独立的内存库，必须用单连接保证同一个库实例。
	// 文件库（WAL 模式）支持多个并发读连接，MaxOpenConns 应设 > 1。
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		if path == ":memory:" {
			maxOpen = 1
		} else {
			maxOpen = 25 // 生产默认：允许最多 25 个并发连接
		}
	}

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = time.Hour
	}

	connMaxIdleTime := cfg.ConnMaxIdleTime
	if connMaxIdleTime <= 0 {
		connMaxIdleTime = 10 * time.Minute
	}

	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)
	sqlDB.SetConnMaxIdleTime(connMaxIdleTime)
	// ────────────────────────────────────────────────────────────────────────

	// 配置 SQLite PRAGMA
	pragmas := []string{
		"PRAGMA journal_mode=WAL",   // WAL 模式：读写并发
		"PRAGMA busy_timeout=5000",  // 锁等待超时 5s（写锁争用时读操作等待而非报错）
		"PRAGMA synchronous=NORMAL", // WAL 模式下 NORMAL 兼顾性能与安全
		"PRAGMA cache_size=-64000",  // 64MB 页缓存
		"PRAGMA foreign_keys=ON",    // 启用外键约束
	}
	for _, pragma := range pragmas {
		if err := db.Exec(pragma).Error; err != nil {
			logger.Warn("failed to set pragma",
				zap.String("pragma", pragma),
				zap.Error(err),
			)
		}
	}

	logger.Info("SQLite database opened successfully",
		zap.String("path", path),
		zap.String("journal_mode", "WAL"),
		zap.Int("max_open_conns", maxOpen),
		zap.Int("max_idle_conns", maxIdle),
		zap.Duration("conn_max_lifetime", connMaxLifetime),
		zap.Duration("conn_max_idle_time", connMaxIdleTime),
	)
	return db, nil
}

// Migrate 执行数据库迁移，创建所有表和索引
func Migrate(logger *zap.Logger, db *gorm.DB) error {
	logger = logger.Named("migrate")
	logger.Info("running database migrations")

	// AutoMigrate 创建/更新表结构
	models := []interface{}{
		&Group{},
		&User{},
		&RefreshToken{},
		&UsageLog{},
		&Peer{},
		&AuditLog{},         // P2-3: 管理操作审计日志
		&APIKey{},           // F-5: 多 API Key 管理
		&APIKeyAssignment{}, // F-5: API Key 分配
		&LLMBinding{},       // LLM 绑定管理
		&LLMTarget{},        // LLM 目标动态管理
	}

	for _, model := range models {
		if err := db.AutoMigrate(model); err != nil {
			logger.Error("AutoMigrate failed",
				zap.String("model", fmt.Sprintf("%T", model)),
				zap.Error(err),
			)
			return fmt.Errorf("migrate %T: %w", model, err)
		}
		logger.Debug("migrated model", zap.String("model", fmt.Sprintf("%T", model)))
	}

	// 创建额外的复合索引（AutoMigrate 不支持复合索引）
	extraIndexes := []struct {
		table string
		name  string
		cols  string
	}{
		{"usage_logs", "idx_usage_user_date", "(user_id, created_at)"},
		// idx_usage_user_id 支持 GetUserAllTimeStats 按 user_id GROUP BY 的快速聚合
		{"usage_logs", "idx_usage_user_id", "(user_id)"},
	}
	for _, idx := range extraIndexes {
		sql := fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON %s %s",
			idx.name, idx.table, idx.cols,
		)
		if err := db.Exec(sql).Error; err != nil {
			logger.Warn("failed to create index",
				zap.String("index", idx.name),
				zap.Error(err),
			)
			// 索引创建失败不阻止启动（只影响查询性能）
		} else {
			logger.Debug("index ensured", zap.String("index", idx.name))
		}
	}

	logger.Info("database migrations completed")
	return nil
}
