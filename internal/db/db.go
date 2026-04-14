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
		// PostgreSQL 的 AutoMigrate 会自动为 XxxID 字段创建 FK 约束，
		// 但其生成的是 CREATE UNIQUE INDEX（非 UNIQUE CONSTRAINT），
		// PostgreSQL 要求 FK 引用目标必须是 UNIQUE CONSTRAINT 或 PRIMARY KEY，
		// 导致升级时报 "no unique constraint matching given keys for reference table groups"。
		// 禁用后由应用层保证引用完整性（fail-open 设计，与 SQLite 行为对齐）。
		DisableForeignKeyConstraintWhenMigrating: isPostgres,
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

	// 数据清理：users.external_id 旧版为 string（空字符串默认），新版为 *string（NULL 默认）。
	// AutoMigrate 将列改为 nullable 但不会把 "" 转成 NULL，导致
	// (auth_provider, external_id) 复合唯一索引因重复的 ('local','') 创建失败。
	// 在 AutoMigrate 前将空字符串规范化为 NULL，确保升级路径幂等。
	if err := db.Exec(`UPDATE users SET external_id = NULL WHERE external_id = ''`).Error; err != nil {
		logger.Warn("users.external_id normalization failed (non-fatal, table may not exist yet)",
			zap.Error(err))
	} else {
		logger.Debug("users.external_id: empty strings normalized to NULL")
	}

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

	// 数据迁移：llm_bindings.target_url → target_id
	// 若存在 target_url 列但 target_id 列值为空，则通过 JOIN 填充；无法匹配的行（孤儿）删除。
	if err := migrateBindingTargetID(logger, db); err != nil {
		logger.Warn("llm_bindings target_id migration failed (non-fatal)", zap.Error(err))
	}

	// 数据迁移：group_target_set_members TargetURL → TargetID
	if err := migrateGroupTargetSetMemberTargetID(logger, db); err != nil {
		logger.Warn("group_target_set_members target_id migration failed (non-fatal)", zap.Error(err))
	}

	logger.Info("database migrations completed")
	return nil
}

// migrateBindingTargetID 将 llm_bindings 表中的 target_url（旧列）迁移到 target_id（新列）。
// 通过 JOIN llm_targets ON url = target_url 填充 target_id；
// 无法匹配的孤儿行（target 已删除）直接删除。
func migrateBindingTargetID(logger *zap.Logger, db *gorm.DB) error {
	// 检查旧列 target_url 是否存在（仅在 SQLite 下可用 PRAGMA，PG 用 information_schema）
	// 若不存在则直接跳过（已迁移完毕或全新安装）
	var hasTargetURL bool
	switch DriverName(db) {
	case "sqlite":
		var cols []struct{ Name string }
		if err := db.Raw("PRAGMA table_info(llm_bindings)").Scan(&cols).Error; err != nil {
			return fmt.Errorf("check llm_bindings columns: %w", err)
		}
		for _, c := range cols {
			if c.Name == "target_url" {
				hasTargetURL = true
				break
			}
		}
	case "postgres":
		var count int64
		q := `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='llm_bindings' AND column_name='target_url'`
		if err := db.Raw(q).Scan(&count).Error; err != nil {
			return fmt.Errorf("check llm_bindings columns (pg): %w", err)
		}
		hasTargetURL = count > 0
	}
	if !hasTargetURL {
		logger.Debug("llm_bindings: no target_url column, migration skipped")
		return nil
	}

	// 填充 target_id（仅 target_id 为空时）
	updateSQL := `UPDATE llm_bindings
		SET target_id = (SELECT id FROM llm_targets WHERE url = llm_bindings.target_url LIMIT 1)
		WHERE (target_id IS NULL OR target_id = '') AND target_url != ''`
	if DriverName(db) == "postgres" {
		updateSQL = `UPDATE llm_bindings
		SET target_id = lt.id
		FROM llm_targets lt
		WHERE lt.url = llm_bindings.target_url
		  AND (llm_bindings.target_id IS NULL OR llm_bindings.target_id = '')`
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		return fmt.Errorf("populate llm_bindings.target_id: %w", err)
	}

	// 删除孤儿行（target_url 无对应 llm_targets 记录）
	deleteSQL := `DELETE FROM llm_bindings WHERE target_id IS NULL OR target_id = ''`
	if res := db.Exec(deleteSQL); res.Error != nil {
		return fmt.Errorf("delete orphan llm_bindings: %w", res.Error)
	} else if res.RowsAffected > 0 {
		logger.Warn("deleted orphan llm_bindings (no matching target)",
			zap.Int64("count", res.RowsAffected),
		)
	}

	logger.Info("llm_bindings target_id migration completed")
	return nil
}

// migrateGroupTargetSetMemberTargetID 将 group_target_set_members 中的 target_url 迁移到 target_id。
func migrateGroupTargetSetMemberTargetID(logger *zap.Logger, db *gorm.DB) error {
	// 检查旧列 target_url 是否存在
	var hasTargetURL bool
	switch DriverName(db) {
	case "sqlite":
		var cols []struct{ Name string }
		if err := db.Raw("PRAGMA table_info(group_target_set_members)").Scan(&cols).Error; err != nil {
			return fmt.Errorf("check group_target_set_members columns: %w", err)
		}
		for _, c := range cols {
			if c.Name == "target_url" {
				hasTargetURL = true
				break
			}
		}
	case "postgres":
		var count int64
		q := `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='group_target_set_members' AND column_name='target_url'`
		if err := db.Raw(q).Scan(&count).Error; err != nil {
			return fmt.Errorf("check group_target_set_members columns (pg): %w", err)
		}
		hasTargetURL = count > 0
	}
	if !hasTargetURL {
		logger.Debug("group_target_set_members: no target_url column, migration skipped")
		return nil
	}

	updateSQL := `UPDATE group_target_set_members
		SET target_id = (SELECT id FROM llm_targets WHERE url = group_target_set_members.target_url LIMIT 1)
		WHERE (target_id IS NULL OR target_id = '') AND target_url != ''`
	if DriverName(db) == "postgres" {
		updateSQL = `UPDATE group_target_set_members
		SET target_id = lt.id
		FROM llm_targets lt
		WHERE lt.url = group_target_set_members.target_url
		  AND (group_target_set_members.target_id IS NULL OR group_target_set_members.target_id = '')`
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		return fmt.Errorf("populate group_target_set_members.target_id: %w", err)
	}

	deleteSQL := `DELETE FROM group_target_set_members WHERE target_id IS NULL OR target_id = ''`
	if res := db.Exec(deleteSQL); res.Error != nil {
		return fmt.Errorf("delete orphan group_target_set_members: %w", res.Error)
	} else if res.RowsAffected > 0 {
		logger.Warn("deleted orphan group_target_set_members (no matching target)",
			zap.Int64("count", res.RowsAffected),
		)
	}

	logger.Info("group_target_set_members target_id migration completed")
	return nil
}
