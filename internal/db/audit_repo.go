package db

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AuditRepo 操作审计日志仓库。
type AuditRepo struct {
	logger *zap.Logger
	db     *gorm.DB
}

// NewAuditRepo 创建 AuditRepo。
func NewAuditRepo(logger *zap.Logger, db *gorm.DB) *AuditRepo {
	return &AuditRepo{
		logger: logger.Named("audit_repo"),
		db:     db,
	}
}

// Create 写入一条审计记录。
func (r *AuditRepo) Create(operator, action, target, detail string) error {
	record := &AuditLog{
		Operator:  operator,
		Action:    action,
		Target:    target,
		Detail:    detail,
		CreatedAt: time.Now(),
	}
	if err := r.db.Create(record).Error; err != nil {
		r.logger.Error("audit: failed to write",
			zap.String("action", action),
			zap.String("target", target),
			zap.Error(err),
		)
		return fmt.Errorf("audit create: %w", err)
	}
	r.logger.Debug("audit record written",
		zap.String("operator", operator),
		zap.String("action", action),
		zap.String("target", target),
	)
	return nil
}

// ListRecent 返回最近 limit 条审计记录（按 created_at 降序）。
func (r *AuditRepo) ListRecent(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	var logs []AuditLog
	if err := r.db.Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		r.logger.Error("audit: failed to list", zap.Error(err))
		return nil, fmt.Errorf("audit list: %w", err)
	}
	return logs, nil
}
