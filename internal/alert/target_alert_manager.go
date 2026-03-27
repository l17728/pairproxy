package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// AlertEvent 告警事件
type AlertEvent struct {
	Type      string      `json:"type"`      // "alert_created" | "alert_resolved" | "target_health_changed"
	Alert     *db.TargetAlert `json:"alert,omitempty"`
	TargetURL string      `json:"target_url,omitempty"`
	Healthy   bool        `json:"healthy,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// ActiveAlert 活跃告警
type ActiveAlert struct {
	Alert           *db.TargetAlert
	ErrorCount      int
	SuccessCount    int
	LastErrorTime   time.Time
	LastSuccessTime time.Time
}

// TargetAlertConfig 告警配置
type TargetAlertConfig struct {
	Enabled bool
	Triggers map[string]TriggerConfig
	Recovery RecoveryConfig
	Dashboard DashboardConfig
}

// TriggerConfig 触发条件
type TriggerConfig struct {
	Type            string
	StatusCodes     []int
	Severity        string
	MinOccurrences  int
	Window          time.Duration
}

// RecoveryConfig 恢复条件
type RecoveryConfig struct {
	ConsecutiveSuccesses int
	Window               time.Duration
}

// DashboardConfig Dashboard 配置
type DashboardConfig struct {
	MaxActiveAlerts int
	Retention       time.Duration
	AutoRefresh     bool
}

// TargetAlertManager 管理 target 告警
type TargetAlertManager struct {
	repo       *db.TargetAlertRepo
	config     TargetAlertConfig
	logger     *zap.Logger

	// 内存状态
	activeAlerts map[string]*ActiveAlert
	mu           sync.RWMutex

	// 事件通道
	eventCh    chan AlertEvent
	subscribers []chan AlertEvent
	subMu      sync.RWMutex

	// 上下文和取消
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewTargetAlertManager 创建告警管理器
func NewTargetAlertManager(
	repo *db.TargetAlertRepo,
	config TargetAlertConfig,
	logger *zap.Logger,
) *TargetAlertManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &TargetAlertManager{
		repo:         repo,
		config:       config,
		logger:       logger.Named("target_alert_manager"),
		activeAlerts: make(map[string]*ActiveAlert),
		eventCh:      make(chan AlertEvent, 100),
		subscribers:  make([]chan AlertEvent, 0),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
}

// Start 启动告警管理器
func (m *TargetAlertManager) Start(ctx context.Context) {
	if !m.config.Enabled {
		m.logger.Info("alert manager disabled")
		close(m.done) // 确保 Stop() 不会阻塞
		return
	}

	go m.eventLoop()
	m.logger.Info("alert manager started")
}

// Stop 停止告警管理器
func (m *TargetAlertManager) Stop() {
	m.cancel()
	<-m.done
	m.logger.Info("alert manager stopped")
}

// eventLoop 事件处理循环
func (m *TargetAlertManager) eventLoop() {
	defer close(m.done)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case event := <-m.eventCh:
			m.broadcastEvent(event)
		case <-ticker.C:
			m.checkRecovery()
		}
	}
}

// broadcastEvent 广播事件给所有订阅者
func (m *TargetAlertManager) broadcastEvent(event AlertEvent) {
	m.subMu.RLock()
	subscribers := make([]chan AlertEvent, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.subMu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		case <-m.ctx.Done():
			return
		default:
			// 如果通道满，跳过（防止阻塞）
		}
	}
}

// RecordError 记录 target 错误
func (m *TargetAlertManager) RecordError(
	targetURL string,
	statusCode int,
	err error,
	affectedGroups []string,
) {
	if !m.config.Enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	active, exists := m.activeAlerts[targetURL]
	if !exists {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		active = &ActiveAlert{
			Alert: &db.TargetAlert{
				ID:             fmt.Sprintf("alert_%d", time.Now().UnixNano()),
				TargetURL:      targetURL,
				AlertType:      "error",
				Severity:       "error",
				StatusCode:     &statusCode,
				ErrorMessage:   errMsg,
				OccurrenceCount: 1,
			},
			ErrorCount:    1,
			LastErrorTime: time.Now(),
		}
		m.activeAlerts[targetURL] = active
	} else {
		active.ErrorCount++
		active.LastErrorTime = time.Now()
		active.SuccessCount = 0 // 重置成功计数
	}

	// 检查是否达到触发阈值
	if active.ErrorCount >= m.config.Triggers["http_error"].MinOccurrences {
		// 创建或更新告警
		groupsJSON, _ := json.Marshal(affectedGroups)
		active.Alert.AffectedGroups = string(groupsJSON)
		active.Alert.OccurrenceCount = active.ErrorCount
		active.Alert.LastOccurrence = &active.LastErrorTime

		// 保存到数据库
		if err := m.repo.Create(active.Alert); err != nil {
			m.logger.Error("failed to create alert",
				zap.String("target_url", targetURL),
				zap.Error(err),
			)
		}

		// 推送事件
		event := AlertEvent{
			Type:      "alert_created",
			Alert:     active.Alert,
			Timestamp: time.Now(),
		}
		select {
		case m.eventCh <- event:
		case <-m.ctx.Done():
		}

		m.logger.Warn("alert created",
			zap.String("target_url", targetURL),
			zap.Int("error_count", active.ErrorCount),
		)
	}
}

// RecordSuccess 记录成功
func (m *TargetAlertManager) RecordSuccess(targetURL string) {
	if !m.config.Enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	active, exists := m.activeAlerts[targetURL]
	if !exists {
		return
	}

	active.SuccessCount++
	active.LastSuccessTime = time.Now()
	active.ErrorCount = 0 // 重置错误计数

	m.logger.Debug("success recorded",
		zap.String("target_url", targetURL),
		zap.Int("success_count", active.SuccessCount),
	)
}

// checkRecovery 检查告警恢复
func (m *TargetAlertManager) checkRecovery() {
	m.mu.Lock()
	defer m.mu.Unlock()

	recoveryThreshold := m.config.Recovery.ConsecutiveSuccesses
	now := time.Now()

	for targetURL, active := range m.activeAlerts {
		if active.Alert == nil || active.Alert.ResolvedAt != nil {
			continue
		}

		// 检查是否达到恢复阈值
		if active.SuccessCount >= recoveryThreshold {
			// 标记为已解决
			if err := m.repo.Resolve(active.Alert.ID); err != nil {
				m.logger.Error("failed to resolve alert",
					zap.String("target_url", targetURL),
					zap.Error(err),
				)
				continue
			}

			active.Alert.ResolvedAt = &now

			// 推送事件
			event := AlertEvent{
				Type:      "alert_resolved",
				Alert:     active.Alert,
				Timestamp: now,
			}
			select {
			case m.eventCh <- event:
			case <-m.ctx.Done():
			}

			m.logger.Info("alert resolved",
				zap.String("target_url", targetURL),
				zap.Int("success_count", active.SuccessCount),
			)

			// 从活跃告警中移除
			delete(m.activeAlerts, targetURL)
		}
	}
}

// RecordHealthCheckFail 记录健康检查失败
func (m *TargetAlertManager) RecordHealthCheckFail(targetURL string, reason string) {
	if !m.config.Enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	active, exists := m.activeAlerts[targetURL]
	if !exists {
		active = &ActiveAlert{
			Alert: &db.TargetAlert{
				ID:           fmt.Sprintf("alert_%d", time.Now().UnixNano()),
				TargetURL:    targetURL,
				AlertType:    "health_check_failed",
				Severity:     "error",
				ErrorMessage: reason,
			},
			ErrorCount:    1,
			LastErrorTime: time.Now(),
		}
		m.activeAlerts[targetURL] = active
	} else {
		active.ErrorCount++
		active.LastErrorTime = time.Now()
	}

	m.logger.Warn("health check failed",
		zap.String("target_url", targetURL),
		zap.String("reason", reason),
	)
}

// GetActiveAlerts 获取当前活跃告警
func (m *TargetAlertManager) GetActiveAlerts() []ActiveAlert {
	m.mu.RLock()
	defer m.mu.RUnlock()

	alerts := make([]ActiveAlert, 0, len(m.activeAlerts))
	for _, alert := range m.activeAlerts {
		alerts = append(alerts, *alert)
	}
	return alerts
}

// SubscribeEvents 订阅告警事件
func (m *TargetAlertManager) SubscribeEvents() <-chan AlertEvent {
	ch := make(chan AlertEvent, 10)

	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()

	return ch
}

// UnsubscribeEvents 取消订阅
func (m *TargetAlertManager) UnsubscribeEvents(ch <-chan AlertEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(sub)
			break
		}
	}
}
