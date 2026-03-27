package db

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/l17728/pairproxy/internal/config"
)

// 包级变量：编译一次，复用（避免每次调用重新编译）
var (
	// kvPasswordRe 匹配 key=value 格式 DSN 中的 password 值
	// (\S*) 而非 (\S+)：允许空密码值
	kvPasswordRe = regexp.MustCompile(`(password\s*=\s*)(\S*)`)
	// urlPasswordRe 匹配 URL 格式 DSN 中的密码部分
	urlPasswordRe = regexp.MustCompile(`(://[^:@]*:)([^@]*)(@)`)
)

// Open 打开 SQLite 数据库连接（使用内置默认连接池，适合测试和简单场景）。
// path 为 SQLite 文件路径，":memory:" 表示内存数据库（测试用）。
// 生产环境请使用 OpenWithConfig 以传入完整连接池配置。
func Open(logger *zap.Logger, path string) (*gorm.DB, error) {
	return OpenWithConfig(logger, config.DatabaseConfig{Path: path})
}

// buildPostgresDSN 构建 PostgreSQL 连接字符串。
// 若 cfg.DSN 非空则直接使用（优先级最高）；
// 否则从独立字段拼接（Host/Port/User/Password/DBName/SSLMode）。
func buildPostgresDSN(cfg config.DatabaseConfig) string {
	if cfg.DSN != "" {
		return cfg.DSN
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode,
	)
}

// maskDSN 对 DSN 字符串进行脱敏处理，将 password= 的值替换为 ***。
// 用于日志输出，防止密码泄露。
// 示例：
//
//	"host=pg user=app password=secret dbname=db" → "host=pg user=app password=*** dbname=db"
//	"postgres://app:secret@pg/db"                → "postgres://app:***@pg/db"
func maskDSN(dsn string) string {
	result := kvPasswordRe.ReplaceAllString(dsn, "${1}***")
	result = urlPasswordRe.ReplaceAllString(result, "${1}***${3}")
	return result
}

// DriverName 返回当前 gorm.DB 使用的数据库驱动名称。
// 返回值为 "sqlite" 或 "postgres"（与 DatabaseConfig.Driver 字段约定一致）。
// 供 repo 层在需要方言特定 SQL 时使用。
// 若 db 为 nil（如测试中未初始化），返回 "sqlite" 作为安全默认值。
func DriverName(db *gorm.DB) string {
	if db == nil {
		return "sqlite"
	}
	name := db.Dialector.Name()
	// glebarez/sqlite 驱动返回 "sqlite"，postgres 驱动返回 "postgres"
	if strings.HasPrefix(name, "sqlite") {
		return "sqlite"
	}
	return name
}

// OpenWithConfig 打开数据库连接，支持 SQLite 和 PostgreSQL。
//
// 驱动选择（cfg.Driver）：
//   - "" 或 "sqlite"：打开 SQLite 文件数据库（向后兼容）
//   - "postgres"：打开 PostgreSQL 数据库
//
// 连接池默认值（cfg 字段为零值时生效）：
//   - SQLite MaxOpenConns: 25（":memory:" 时为 1）
//   - PostgreSQL MaxOpenConns: 50
//   - MaxIdleConns:    10
//   - ConnMaxLifetime: 1h
//   - ConnMaxIdleTime: 10m
func OpenWithConfig(logger *zap.Logger, cfg config.DatabaseConfig) (*gorm.DB, error) {
	logger = logger.Named("db")

	// 将 zap logger 适配为 gorm logger（仅输出 error 级别，减少噪音）
	gormLog := gormlogger.Default.LogMode(gormlogger.Silent)

	var dialector gorm.Dialector
	var logLabel string // 用于日志输出（不含密码）
	isPostgres := cfg.Driver == "postgres"

	if isPostgres {
		dsn := buildPostgresDSN(cfg)
		logLabel = maskDSN(dsn)
		dialector = postgres.Open(dsn)
		logger.Info("opening PostgreSQL database", zap.String("dsn", logLabel))
	} else {
		// SQLite（默认）
		logLabel = cfg.Path
		dialector = sqlite.Open(cfg.Path)
		logger.Info("opening SQLite database", zap.String("path", logLabel))
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: gormLog,
	})
	if err != nil {
		logger.Error("failed to open database",
			zap.String("target", logLabel),
			zap.Error(err),
		)
		return nil, fmt.Errorf("open database %q: %w", logLabel, err)
	}

	// 获取底层 sql.DB 以配置连接池
	sqlDB, err := db.DB()
	if err != nil {
		logger.Error("failed to get underlying sql.DB",
			zap.String("target", logLabel),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get underlying sql.DB: %w", err)
	}

	// ── 连接池参数 ──────────────────────────────────────────────────────────
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		if isPostgres {
			maxOpen = 50 // PG MVCC 支持高并发
		} else if cfg.Path == ":memory:" {
			maxOpen = 1 // 内存库：每连接独立实例，必须单连接
		} else {
			maxOpen = 25 // SQLite WAL 模式：允许最多 25 个并发连接
		}
	}

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	if maxIdle > maxOpen {
		logger.Warn("max_idle_conns exceeds max_open_conns, capping to max_open_conns",
			zap.Int("configured_max_idle", cfg.MaxIdleConns),
			zap.Int("max_open_conns", maxOpen),
			zap.Int("capped_to", maxOpen),
		)
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

	// SQLite PRAGMA（仅 SQLite 模式下执行）
	if !isPostgres {
		pragmas := []string{
			"PRAGMA journal_mode=WAL",   // WAL 模式：读写并发
			"PRAGMA busy_timeout=5000",  // 锁等待超时 5s
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
	}

	if isPostgres {
		logger.Info("PostgreSQL database opened successfully",
			zap.String("dsn", logLabel),
			zap.Int("max_open_conns", maxOpen),
			zap.Int("max_idle_conns", maxIdle),
			zap.Duration("conn_max_lifetime", connMaxLifetime),
			zap.Duration("conn_max_idle_time", connMaxIdleTime),
		)
	} else {
		logger.Info("SQLite database opened successfully",
			zap.String("path", logLabel),
			zap.String("journal_mode", "WAL"),
			zap.Int("max_open_conns", maxOpen),
			zap.Int("max_idle_conns", maxIdle),
			zap.Duration("conn_max_lifetime", connMaxLifetime),
			zap.Duration("conn_max_idle_time", connMaxIdleTime),
		)
	}
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
		&SemanticRoute{},    // 语义路由规则
		&GroupTargetSet{},   // Group-Target Set 绑定
		&GroupTargetSetMember{}, // Target Set 成员
		&TargetAlert{},      // Target 告警事件
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
