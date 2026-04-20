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

	// 数据迁移前置：llm_bindings.target_id 和 group_target_set_members.target_id
	// 在旧版本中不存在（旧版使用 target_url 字段）。
	// AutoMigrate 向已有行的表添加 NOT NULL 列时会直接报错，必须先以 nullable 形式预建列，
	// AutoMigrate 发现列已存在时会跳过，不再尝试施加 NOT NULL 约束。
	// 回填逻辑（migrateBindingTargetID / migrateGroupTargetSetMemberTargetID）在后面执行。
	preMigrateNullableColumn(logger, db, "llm_bindings", "target_id")
	preMigrateNullableColumn(logger, db, "group_target_set_members", "target_id")

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

	// 数据迁移：llm_targets URL 唯一化（废弃同 URL 多 APIKey 场景）。
	// 若存在同 URL 的多条记录，保留最早创建的那条；将指向被删除记录的 llm_bindings 重定向到保留记录；
	// 删除多余记录。之后 AutoMigrate 将添加 URL 的唯一索引。
	if err := deduplicateLLMTargetsByURL(logger, db); err != nil {
		logger.Warn("llm_targets URL dedup failed (non-fatal)", zap.Error(err))
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

	// 索引迁移：废弃 idx_llm_target_url_apikey 复合索引，改为 URL 单列唯一索引。
	// AutoMigrate 已通过 model tag 创建 idx_llm_targets_url，此处仅清理旧索引。
	dropLLMTargetCompositeIndex(logger, db)

	// 数据迁移：llm_bindings.target_url → target_id
	// 若存在 target_url 列但 target_id 列值为空，则通过 JOIN 填充；无法匹配的行（孤儿）删除。
	if err := migrateBindingTargetID(logger, db); err != nil {
		logger.Warn("llm_bindings target_id migration failed (non-fatal)", zap.Error(err))
	}

	// 数据迁移：group_target_set_members TargetURL → TargetID
	if err := migrateGroupTargetSetMemberTargetID(logger, db); err != nil {
		logger.Warn("group_target_set_members target_id migration failed (non-fatal)", zap.Error(err))
	}

	// 回填完成后补 NOT NULL 约束（预建列时为 nullable，数据填充后恢复意图约束）
	postMigrateSetNotNull(logger, db, "llm_bindings", "target_id")
	postMigrateSetNotNull(logger, db, "group_target_set_members", "target_id")

	logger.Info("database migrations completed")
	return nil
}

// preMigrateNullableColumn 在 AutoMigrate 之前将指定列以 nullable TEXT 形式预加入表中。
// 目的：防止 AutoMigrate 向已有行的表添加 NOT NULL 列时因现存 NULL 值报错。
// AutoMigrate 发现列已存在时会跳过，不会再尝试施加 NOT NULL 约束。
// 若表不存在（全新安装）则忽略错误，AutoMigrate 会完整建表。
func preMigrateNullableColumn(logger *zap.Logger, db *gorm.DB, table, column string) {
	var sql string
	switch DriverName(db) {
	case "postgres":
		sql = fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s TEXT`, table, column)
	default: // sqlite
		sql = fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s TEXT`, table, column)
	}
	if err := db.Exec(sql).Error; err != nil {
		// 表不存在或列已存在均属正常（全新安装 / 已迁移），记 debug 后继续
		logger.Debug("preMigrateNullableColumn skipped (table absent or column exists)",
			zap.String("table", table),
			zap.String("column", column),
			zap.Error(err),
		)
	} else {
		logger.Info("pre-migrated nullable column",
			zap.String("table", table),
			zap.String("column", column),
		)
	}
}

// postMigrateSetNotNull 在数据回填完成后为指定列补设 NOT NULL 约束。
// 仅在 PostgreSQL 下执行（SQLite 不支持 ALTER COLUMN SET NOT NULL）。
// 若列仍有 NULL 值（孤儿行未删净），记 warn 后继续，不阻断启动。
func postMigrateSetNotNull(logger *zap.Logger, db *gorm.DB, table, column string) {
	if DriverName(db) != "postgres" {
		return // SQLite 不支持此语法，且 SQLite FK 约束由 PRAGMA 层面保证
	}
	sql := fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s SET NOT NULL`, table, column)
	if err := db.Exec(sql).Error; err != nil {
		logger.Warn("postMigrateSetNotNull failed (column may still contain NULLs)",
			zap.String("table", table),
			zap.String("column", column),
			zap.Error(err),
		)
	} else {
		logger.Debug("NOT NULL constraint applied",
			zap.String("table", table),
			zap.String("column", column),
		)
	}
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

// deduplicateLLMTargetsByURL 合并同 URL 的多条 llm_targets 记录。
// 对每组同 URL 的记录，保留 created_at 最早的一条（保持原有绑定稳定），
// 将 llm_bindings 中指向被删除记录的 target_id 更新为保留记录的 ID，
// 然后删除多余记录。
func deduplicateLLMTargetsByURL(logger *zap.Logger, db *gorm.DB) error {
	// 找出有重复 URL 的记录
	type urlGroup struct {
		URL string
	}
	var duplicates []urlGroup
	if err := db.Raw(`
		SELECT url FROM llm_targets
		GROUP BY url
		HAVING COUNT(*) > 1
	`).Scan(&duplicates).Error; err != nil {
		return fmt.Errorf("find duplicate urls: %w", err)
	}

	if len(duplicates) == 0 {
		return nil
	}

	logger.Info("deduplicating llm_targets", zap.Int("duplicate_url_count", len(duplicates)))

	for _, dup := range duplicates {
		// 取该 URL 下所有记录，按 created_at ASC 排序（保留最早的）
		type targetRow struct {
			ID string
		}
		var rows []targetRow
		if err := db.Raw(`SELECT id FROM llm_targets WHERE url = ? ORDER BY created_at ASC`, dup.URL).Scan(&rows).Error; err != nil {
			logger.Warn("dedup: failed to list targets for url", zap.String("url", dup.URL), zap.Error(err))
			continue
		}
		if len(rows) <= 1 {
			continue
		}

		keepID := rows[0].ID
		removeIDs := make([]string, 0, len(rows)-1)
		for _, r := range rows[1:] {
			removeIDs = append(removeIDs, r.ID)
		}

		// 更新绑定：将指向 removeIDs 的 llm_bindings.target_id 改为 keepID
		if err := db.Exec(`UPDATE llm_bindings SET target_id = ? WHERE target_id IN ?`, keepID, removeIDs).Error; err != nil {
			logger.Warn("dedup: failed to redirect bindings", zap.String("url", dup.URL), zap.Error(err))
		}

		// 删除多余记录
		if err := db.Exec(`DELETE FROM llm_targets WHERE id IN ?`, removeIDs).Error; err != nil {
			logger.Warn("dedup: failed to delete duplicate targets", zap.String("url", dup.URL), zap.Error(err))
			continue
		}

		logger.Info("dedup: merged duplicate targets",
			zap.String("url", dup.URL),
			zap.String("kept_id", keepID),
			zap.Strings("removed_ids", removeIDs))
	}
	return nil
}

// dropLLMTargetCompositeIndex 删除废弃的 (url, api_key_id) 复合索引。
// 迁移到 URL 单列唯一索引后，旧复合索引变为冗余。
func dropLLMTargetCompositeIndex(logger *zap.Logger, db *gorm.DB) {
	var sql string
	switch DriverName(db) {
	case "postgres":
		sql = `DROP INDEX IF EXISTS idx_llm_target_url_apikey`
	default: // sqlite
		sql = `DROP INDEX IF EXISTS idx_llm_target_url_apikey`
	}
	if err := db.Exec(sql).Error; err != nil {
		logger.Debug("drop composite index (may not exist)", zap.Error(err))
	} else {
		logger.Info("dropped deprecated composite index idx_llm_target_url_apikey")
	}
}
