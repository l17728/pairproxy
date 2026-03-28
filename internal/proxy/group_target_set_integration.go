package proxy

import (
	"context"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/db"
)

// GroupTargetSetIntegration 集成 Group Target Set 功能到 SProxy
type GroupTargetSetIntegration struct {
	selector       *GroupTargetSelector
	alertManager   *alert.TargetAlertManager
	healthMonitor  *alert.TargetHealthMonitor
	logger         *zap.Logger
}

// NewGroupTargetSetIntegration 创建集成层
func NewGroupTargetSetIntegration(
	repo *db.GroupTargetSetRepo,
	alertRepo *db.TargetAlertRepo,
	alertConfig alert.TargetAlertConfig,
	healthCheckConfig alert.HealthCheckConfig,
	logger *zap.Logger,
) *GroupTargetSetIntegration {
	selector := NewGroupTargetSelector(repo, logger)
	alertManager := alert.NewTargetAlertManager(alertRepo, alertConfig, logger)
	healthMonitor := alert.NewTargetHealthMonitor(repo, alertManager, healthCheckConfig, logger)

	return &GroupTargetSetIntegration{
		selector:      selector,
		alertManager:  alertManager,
		healthMonitor: healthMonitor,
		logger:        logger.Named("group_target_set_integration"),
	}
}

// Start 启动集成层（启动后台 goroutines）
func (i *GroupTargetSetIntegration) Start(ctx context.Context) {
	i.alertManager.Start(ctx)
	i.healthMonitor.Start(ctx)
	i.logger.Info("group target set integration started")
}

// Stop 停止集成层
func (i *GroupTargetSetIntegration) Stop() {
	i.alertManager.Stop()
	i.healthMonitor.Stop()
	i.logger.Info("group target set integration stopped")
}

// SelectTarget 为指定 Group 选择 target
func (i *GroupTargetSetIntegration) SelectTarget(
	ctx context.Context,
	groupID string,
	tried []string,
) (*SelectedTarget, bool, error) {
	result, hasMore, err := i.selector.SelectTarget(ctx, groupID, tried)
	if err != nil {
		i.logger.Warn("select target failed",
			zap.String("group_id", groupID),
			zap.Strings("tried", tried),
			zap.Error(err),
		)
		return nil, false, err
	}
	i.logger.Debug("target selected",
		zap.String("group_id", groupID),
		zap.String("target_url", result.URL),
		zap.Bool("has_more", hasMore),
	)
	return result, hasMore, nil
}

// RecordError 记录 target 错误
func (i *GroupTargetSetIntegration) RecordError(
	targetURL string,
	statusCode int,
	err error,
	affectedGroups []string,
) {
	i.logger.Debug("recording target error",
		zap.String("target_url", targetURL),
		zap.Int("status_code", statusCode),
	)
	i.alertManager.RecordError(targetURL, statusCode, err, affectedGroups)
}

// RecordSuccess 记录成功
func (i *GroupTargetSetIntegration) RecordSuccess(targetURL string) {
	i.logger.Debug("recording target success",
		zap.String("target_url", targetURL),
	)
	i.alertManager.RecordSuccess(targetURL)
}

// GetActiveAlerts 获取活跃告警
func (i *GroupTargetSetIntegration) GetActiveAlerts() []alert.ActiveAlert {
	return i.alertManager.GetActiveAlerts()
}

// SubscribeAlerts 订阅告警事件
func (i *GroupTargetSetIntegration) SubscribeAlerts() <-chan alert.AlertEvent {
	return i.alertManager.SubscribeEvents()
}

// GetHealthStatus 获取 target 的健康状态
func (i *GroupTargetSetIntegration) GetHealthStatus(targetURL string) *alert.TargetHealthStatus {
	return i.healthMonitor.GetStatus(targetURL)
}

// GetAllHealthStatus 获取所有 targets 的健康状态
func (i *GroupTargetSetIntegration) GetAllHealthStatus() map[string]*alert.TargetHealthStatus {
	return i.healthMonitor.GetAllStatus()
}
