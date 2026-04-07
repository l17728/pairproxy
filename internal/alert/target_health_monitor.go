package alert

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	Interval           time.Duration
	Timeout            time.Duration
	FailureThreshold   int
	SuccessThreshold   int
	Path               string
}

// TargetHealthMonitor 后台健康检查
type TargetHealthMonitor struct {
	repo              *db.GroupTargetSetRepo
	llmTargetRepo     *db.LLMTargetRepo // 可选，用于将 TargetID 解析为 URL
	alertManager      *TargetAlertManager
	config            HealthCheckConfig
	logger            *zap.Logger
	httpClient        *http.Client

	// 状态跟踪
	targetStatus      map[string]*TargetHealthStatus
	mu                sync.RWMutex

	// 上下文和取消
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
}

// TargetHealthStatus 单个 target 的健康状态
type TargetHealthStatus struct {
	URL                 string
	Healthy             bool
	LastCheckTime       time.Time
	ConsecutiveFailures int
	ConsecutiveSuccesses int
	LastError           string
}

// NewTargetHealthMonitor 创建健康检查监控器
func NewTargetHealthMonitor(
	repo *db.GroupTargetSetRepo,
	alertManager *TargetAlertManager,
	config HealthCheckConfig,
	logger *zap.Logger,
	opts ...func(*TargetHealthMonitor),
) *TargetHealthMonitor {
	if config.Interval == 0 {
		config.Interval = 30 * time.Second
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}
	if config.FailureThreshold == 0 {
		config.FailureThreshold = 3
	}
	if config.SuccessThreshold == 0 {
		config.SuccessThreshold = 2
	}
	if config.Path == "" {
		config.Path = "/health"
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &TargetHealthMonitor{
		repo:         repo,
		alertManager: alertManager,
		config:       config,
		logger:       logger.Named("target_health_monitor"),
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		targetStatus: make(map[string]*TargetHealthStatus),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithLLMTargetRepo 选项函数，注入 LLMTargetRepo 以将 TargetID 解析为 URL
func WithLLMTargetRepo(r *db.LLMTargetRepo) func(*TargetHealthMonitor) {
	return func(m *TargetHealthMonitor) {
		m.llmTargetRepo = r
	}
}

// Start 启动健康检查
func (m *TargetHealthMonitor) Start(ctx context.Context) {
	go m.monitorLoop()
	m.logger.Info("health monitor started",
		zap.Duration("interval", m.config.Interval),
	)
}

// Stop 停止健康检查
func (m *TargetHealthMonitor) Stop() {
	m.cancel()
	<-m.done
	m.logger.Info("health monitor stopped")
}

// monitorLoop 监控循环
func (m *TargetHealthMonitor) monitorLoop() {
	defer close(m.done)

	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	// 首次立即执行
	m.checkAllTargets()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkAllTargets()
		}
	}
}

// checkAllTargets 检查所有 targets
func (m *TargetHealthMonitor) checkAllTargets() {
	// 获取所有 target sets
	sets, err := m.repo.ListAll()
	if err != nil {
		m.logger.Error("failed to list target sets", zap.Error(err))
		return
	}

	// 收集所有 targets
	targetURLs := make(map[string]bool)
	for _, set := range sets {
		members, err := m.repo.ListMembers(set.ID)
		if err != nil {
			m.logger.Error("failed to list members",
				zap.String("target_set_id", set.ID),
				zap.Error(err),
			)
			continue
		}

		for _, member := range members {
			if !member.IsActive {
				continue
			}
			// TargetURL 是 gorm:"-" 字段（不存储），需通过 TargetID 解析实际 URL
			targetURL := member.TargetURL
			if targetURL == "" && m.llmTargetRepo != nil && member.TargetID != "" {
				if t, err2 := m.llmTargetRepo.GetByID(member.TargetID); err2 == nil && t != nil {
					targetURL = t.URL
				} else {
					m.logger.Warn("failed to resolve target URL from ID",
						zap.String("target_id", member.TargetID),
						zap.Error(err2),
					)
				}
			}
			if targetURL != "" {
				targetURLs[targetURL] = true
			}
		}
	}

	// 并发检查所有 targets
	var wg sync.WaitGroup
	for url := range targetURLs {
		wg.Add(1)
		go func(targetURL string) {
			defer wg.Done()
			m.checkTarget(targetURL)
		}(url)
	}

	wg.Wait()
}

// checkTarget 检查单个 target
func (m *TargetHealthMonitor) checkTarget(targetURL string) {
	status := m.getOrCreateStatus(targetURL)

	// 执行健康检查
	healthy := m.performHealthCheck(targetURL)

	m.mu.Lock()
	defer m.mu.Unlock()

	oldHealthy := status.Healthy
	status.LastCheckTime = time.Now()

	if healthy {
		status.ConsecutiveSuccesses++
		status.ConsecutiveFailures = 0
		status.LastError = ""

		// 检查是否从不健康恢复
		if !oldHealthy && status.ConsecutiveSuccesses >= m.config.SuccessThreshold {
			status.Healthy = true
			m.logger.Info("target recovered",
				zap.String("target_url", targetURL),
				zap.Int("consecutive_successes", status.ConsecutiveSuccesses),
			)

			// 推送恢复事件
			if m.alertManager != nil {
				event := AlertEvent{
					Type:      "target_health_changed",
					TargetURL: targetURL,
					Healthy:   true,
					Reason:    "target recovered",
					Timestamp: time.Now(),
				}
				m.alertManager.eventCh <- event
			}

			// 更新数据库
			if err := m.repo.UpdateTargetHealth(targetURL, true); err != nil {
				m.logger.Error("failed to update target health",
					zap.String("target_url", targetURL),
					zap.Error(err),
				)
			}
		}
	} else {
		status.ConsecutiveFailures++
		status.ConsecutiveSuccesses = 0

		// 检查是否达到失败阈值
		if oldHealthy && status.ConsecutiveFailures >= m.config.FailureThreshold {
			status.Healthy = false
			m.logger.Warn("target unhealthy",
				zap.String("target_url", targetURL),
				zap.Int("consecutive_failures", status.ConsecutiveFailures),
			)

			// 推送故障事件
			if m.alertManager != nil {
				event := AlertEvent{
					Type:      "target_health_changed",
					TargetURL: targetURL,
					Healthy:   false,
					Reason:    "health check failed",
					Timestamp: time.Now(),
				}
				m.alertManager.eventCh <- event

				// 记录健康检查失败
				m.alertManager.RecordHealthCheckFail(targetURL, status.LastError)
			}

			// 更新数据库
			if err := m.repo.UpdateTargetHealth(targetURL, false); err != nil {
				m.logger.Error("failed to update target health",
					zap.String("target_url", targetURL),
					zap.Error(err),
				)
			}
		}
	}
}

// performHealthCheck 执行健康检查
func (m *TargetHealthMonitor) performHealthCheck(targetURL string) bool {
	// 构建健康检查 URL
	healthURL := targetURL + m.config.Path

	ctx, cancel := context.WithTimeout(m.ctx, m.config.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		m.updateLastError(targetURL, fmt.Sprintf("failed to create request: %v", err))
		return false
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		m.updateLastError(targetURL, fmt.Sprintf("request failed: %v", err))
		return false
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		m.updateLastError(targetURL, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return false
	}

	return true
}

// getOrCreateStatus 获取或创建状态
func (m *TargetHealthMonitor) getOrCreateStatus(targetURL string) *TargetHealthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	if status, ok := m.targetStatus[targetURL]; ok {
		return status
	}

	status := &TargetHealthStatus{
		URL:     targetURL,
		Healthy: true,
	}
	m.targetStatus[targetURL] = status
	return status
}

// updateLastError 更新最后的错误信息
func (m *TargetHealthMonitor) updateLastError(targetURL string, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if status, ok := m.targetStatus[targetURL]; ok {
		status.LastError = errMsg
	}
}

// GetStatus 获取 target 的健康状态
func (m *TargetHealthMonitor) GetStatus(targetURL string) *TargetHealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if status, ok := m.targetStatus[targetURL]; ok {
		// 返回副本以避免并发修改
		statusCopy := *status
		return &statusCopy
	}

	return nil
}

// GetAllStatus 获取所有 targets 的健康状态
func (m *TargetHealthMonitor) GetAllStatus() map[string]*TargetHealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*TargetHealthStatus, len(m.targetStatus))
	for url, status := range m.targetStatus {
		statusCopy := *status
		result[url] = &statusCopy
	}
	return result
}
