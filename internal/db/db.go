package db

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Open 打开 SQLite 数据库连接，配置 WAL 模式和连接参数
// path 为 SQLite 文件路径，":memory:" 表示内存数据库（测试用）
func Open(logger *zap.Logger, path string) (*gorm.DB, error) {
	logger = logger.Named("db")
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

	// SQLite 写操作串行，多 writer 无意义；读可以并发
	// 设置: 1 个写连接 + 最多 4 个读连接
	sqlDB.SetMaxOpenConns(1) // WAL 模式下写串行，单连接足够
	sqlDB.SetMaxIdleConns(4)

	// 配置 SQLite PRAGMA
	pragmas := []string{
		"PRAGMA journal_mode=WAL",      // WAL 模式：读写并发
		"PRAGMA busy_timeout=5000",     // 锁等待超时 5s
		"PRAGMA synchronous=NORMAL",    // WAL 模式下 NORMAL 兼顾性能与安全
		"PRAGMA cache_size=-64000",     // 64MB 页缓存
		"PRAGMA foreign_keys=ON",       // 启用外键约束
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
