package db

import (
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TargetAlertRepo 管理告警事件
type TargetAlertRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewTargetAlertRepo 创建 TargetAlertRepo
func NewTargetAlertRepo(db *gorm.DB, logger *zap.Logger) *TargetAlertRepo {
	return &TargetAlertRepo{
		db:     db,
		logger: logger.Named("target_alert_repo"),
	}
}

// AlertFilters 告警过滤条件
type AlertFilters struct {
	TargetURL string
	Severity  string
	AlertType string
	Limit     int
	Offset    int
}

// Create 创建告警
func (r *TargetAlertRepo) Create(alert *TargetAlert) error {
	if alert.ID == "" {
		return fmt.Errorf("alert ID cannot be empty")
	}
	if alert.TargetURL == "" {
		return fmt.Errorf("target URL cannot be empty")
	}

	alert.CreatedAt = time.Now()
	if alert.LastOccurrence == nil {
		alert.LastOccurrence = &alert.CreatedAt
	}

	if err := r.db.Create(alert).Error; err != nil {
		r.logger.Error("failed to create alert",
			zap.String("id", alert.ID),
			zap.String("target_url", alert.TargetURL),
			zap.Error(err),
		)
		return fmt.Errorf("create alert: %w", err)
	}

	r.logger.Debug("alert created",
		zap.String("id", alert.ID),
		zap.String("target_url", alert.TargetURL),
		zap.String("severity", alert.Severity),
	)
	return nil
}

// ListActive 查询活跃告警
func (r *TargetAlertRepo) ListActive(filters AlertFilters) ([]TargetAlert, error) {
	var alerts []TargetAlert
	query := r.db.Where("resolved_at IS NULL")

	if filters.TargetURL != "" {
		query = query.Where("target_url = ?", filters.TargetURL)
	}
	if filters.Severity != "" {
		query = query.Where("severity = ?", filters.Severity)
	}
	if filters.AlertType != "" {
		query = query.Where("alert_type = ?", filters.AlertType)
	}

	if filters.Limit == 0 {
		filters.Limit = 50
	}

	if err := query.Order("created_at DESC").
		Limit(filters.Limit).
		Offset(filters.Offset).
		Find(&alerts).Error; err != nil {
		r.logger.Error("failed to list active alerts",
			zap.String("target_url", filters.TargetURL),
			zap.Error(err),
		)
		return nil, fmt.Errorf("list active alerts: %w", err)
	}

	return alerts, nil
}

// ListHistory 查询历史告警
func (r *TargetAlertRepo) ListHistory(days int, page, pageSize int) ([]TargetAlert, error) {
	if pageSize == 0 {
		pageSize = 50
	}
	if page == 0 {
		page = 1
	}

	offset := (page - 1) * pageSize
	cutoffTime := time.Now().AddDate(0, 0, -days)

	var alerts []TargetAlert
	if err := r.db.Where("created_at >= ?", cutoffTime).
		Order("created_at DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&alerts).Error; err != nil {
		r.logger.Error("failed to list alert history",
			zap.Int("days", days),
			zap.Error(err),
		)
		return nil, fmt.Errorf("list alert history: %w", err)
	}

	return alerts, nil
}

// Resolve 标记告警为已解决
func (r *TargetAlertRepo) Resolve(alertID string) error {
	now := time.Now()
	if err := r.db.Model(&TargetAlert{}).
		Where("id = ?", alertID).
		Update("resolved_at", now).Error; err != nil {
		r.logger.Error("failed to resolve alert",
			zap.String("id", alertID),
			zap.Error(err),
		)
		return fmt.Errorf("resolve alert: %w", err)
	}

	r.logger.Debug("alert resolved", zap.String("id", alertID))
	return nil
}

// AlertStats 告警统计信息
type AlertStats struct {
	ByTarget map[string]TargetStats `json:"by_target"`
	ByGroup  map[string]GroupStats  `json:"by_group"`
}

// TargetStats 单个 target 的统计
type TargetStats struct {
	TotalAlerts      int     `json:"total_alerts"`
	ErrorRate        float64 `json:"error_rate"`
	AvgDurationSecs  int     `json:"avg_duration_seconds"`
	ActiveAlertCount int     `json:"active_alert_count"`
}

// GroupStats 单个 group 的统计
type GroupStats struct {
	AffectedAlertCount int `json:"affected_alerts"`
}

// GetStats 获取统计信息
func (r *TargetAlertRepo) GetStats(days int) (*AlertStats, error) {
	cutoffTime := time.Now().AddDate(0, 0, -days)

	// 按 target 统计
	var targetStats []struct {
		TargetURL       string
		TotalAlerts     int
		ActiveAlerts    int
		AvgDurationSecs int
	}

	if err := r.db.Model(&TargetAlert{}).
		Where("created_at >= ?", cutoffTime).
		Select("target_url, COUNT(*) as total_alerts, "+
			"SUM(CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END) as active_alerts, "+
			"AVG(CAST((COALESCE(resolved_at, CURRENT_TIMESTAMP) - created_at) AS INTEGER)) as avg_duration_secs").
		Group("target_url").
		Scan(&targetStats).Error; err != nil {
		r.logger.Error("failed to get target stats", zap.Error(err))
		return nil, fmt.Errorf("get target stats: %w", err)
	}

	stats := &AlertStats{
		ByTarget: make(map[string]TargetStats),
		ByGroup:  make(map[string]GroupStats),
	}

	for _, ts := range targetStats {
		errorRate := 0.0
		if ts.TotalAlerts > 0 {
			errorRate = float64(ts.ActiveAlerts) / float64(ts.TotalAlerts)
		}
		stats.ByTarget[ts.TargetURL] = TargetStats{
			TotalAlerts:     ts.TotalAlerts,
			ErrorRate:       errorRate,
			AvgDurationSecs: ts.AvgDurationSecs,
			ActiveAlertCount: ts.ActiveAlerts,
		}
	}

	// 按 group 统计
	var alerts []TargetAlert
	if err := r.db.Where("created_at >= ?", cutoffTime).
		Find(&alerts).Error; err != nil {
		r.logger.Error("failed to get alerts for group stats", zap.Error(err))
		return stats, nil // 返回部分结果
	}

	for _, alert := range alerts {
		var groups []string
		if err := json.Unmarshal([]byte(alert.AffectedGroups), &groups); err != nil {
			continue
		}
		for _, group := range groups {
			gs := stats.ByGroup[group]
			gs.AffectedAlertCount++
			stats.ByGroup[group] = gs
		}
	}

	return stats, nil
}

// Cleanup 清理旧数据
func (r *TargetAlertRepo) Cleanup(olderThan time.Duration) (int, error) {
	cutoffTime := time.Now().Add(-olderThan)

	result := r.db.Where("resolved_at IS NOT NULL AND resolved_at < ?", cutoffTime).
		Delete(&TargetAlert{})

	if result.Error != nil {
		r.logger.Error("failed to cleanup old alerts",
			zap.Duration("older_than", olderThan),
			zap.Error(result.Error),
		)
		return 0, fmt.Errorf("cleanup alerts: %w", result.Error)
	}

	r.logger.Info("cleaned up old alerts",
		zap.Duration("older_than", olderThan),
		zap.Int64("deleted_count", result.RowsAffected),
	)

	return int(result.RowsAffected), nil
}

// GetByID 根据 ID 获取告警
func (r *TargetAlertRepo) GetByID(id string) (*TargetAlert, error) {
	var alert TargetAlert
	if err := r.db.First(&alert, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		r.logger.Error("failed to get alert by ID",
			zap.String("id", id),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get alert by ID: %w", err)
	}
	return &alert, nil
}

// GetOrCreateAlert 获取或创建告警（用于去重）
func (r *TargetAlertRepo) GetOrCreateAlert(alert *TargetAlert) (*TargetAlert, error) {
	if alert.AlertKey == "" {
		// 如果没有 alert_key，直接创建新告警
		if err := r.Create(alert); err != nil {
			return nil, err
		}
		return alert, nil
	}

	// 查找现有的未解决告警
	var existing TargetAlert
	if err := r.db.Where("alert_key = ? AND resolved_at IS NULL", alert.AlertKey).
		First(&existing).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 不存在，创建新告警
			if err := r.Create(alert); err != nil {
				return nil, err
			}
			return alert, nil
		}
		return nil, fmt.Errorf("get or create alert: %w", err)
	}

	// 更新现有告警的计数和最后发生时间
	now := time.Now()
	if err := r.db.Model(&existing).Updates(map[string]interface{}{
		"occurrence_count": existing.OccurrenceCount + 1,
		"last_occurrence":  now,
	}).Error; err != nil {
		r.logger.Error("failed to update alert occurrence",
			zap.String("id", existing.ID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("update alert occurrence: %w", err)
	}

	return &existing, nil
}
