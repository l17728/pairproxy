package lb

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
)

const (
	defaultFailThreshold = 3            // 连续失败次数阈值
	defaultCheckInterval = 30 * time.Second
	defaultCheckTimeout  = 5 * time.Second
	defaultHealthPath    = "/health"
)

// TargetCredential 健康检查认证凭证（按 provider 类型注入不同认证头）。
type TargetCredential struct {
	APIKey   string // 明文 key（健康检查时直接使用）
	Provider string // "anthropic" | "openai" | "ollama" | ""（空=无认证）
}

// HealthChecker 对 Balancer 中的目标节点进行主动健康检查。
//
// 主动检查：每隔 interval 对每个节点发 GET /health（或自定义路径），200 视为健康。
// 认证支持：支持 Bearer 认证（OpenAI/OpenAI-compatible）和 x-api-key 认证（Anthropic）。
// 被动熔断：调用方通过 RecordSuccess/RecordFailure 上报结果，
// 连续 failThreshold 次失败后将节点标记为不健康。
// 自动恢复：recoveryDelay > 0 时，节点进入不健康状态后自动在 recoveryDelay 后恢复（半开状态）。
type HealthChecker struct {
	balancer      Balancer
	client        *http.Client
	logger        *zap.Logger
	interval      time.Duration
	timeout       time.Duration
	healthPath    string
	failThreshold int
	notifier      *alert.Notifier // 可选，nil 时不发告警
	recoveryDelay time.Duration   // 0=禁用自动恢复；>0=熔断后自动恢复延迟
	healthPaths   map[string]string // targetID → health check path（空=跳过主动检查）
	credentials   map[string]TargetCredential // targetID → credential（用于主动健康检查认证）

	mu       sync.Mutex
	failures map[string]int // 连续失败计数

	wg sync.WaitGroup // tracks in-flight check goroutines
}

// Wait blocks until all in-flight health check goroutines have finished.
// Call after cancelling the context passed to Start.
func (hc *HealthChecker) Wait() { hc.wg.Wait() }

// HealthCheckerOption 用于配置 HealthChecker。
type HealthCheckerOption func(*HealthChecker)

// WithInterval 设置主动检查间隔（默认 30s）。
func WithInterval(d time.Duration) HealthCheckerOption {
	return func(h *HealthChecker) { h.interval = d }
}

// WithTimeout 设置单次检查超时（默认 5s）。
func WithTimeout(d time.Duration) HealthCheckerOption {
	return func(h *HealthChecker) {
		h.timeout = d
		h.client = &http.Client{Timeout: d}
	}
}

// WithFailThreshold 设置被动熔断阈值（默认 3）。
func WithFailThreshold(n int) HealthCheckerOption {
	return func(h *HealthChecker) { h.failThreshold = n }
}

// WithHealthPath 设置健康检查路径（默认 /health）。
func WithHealthPath(p string) HealthCheckerOption {
	return func(h *HealthChecker) { h.healthPath = p }
}

// WithRecoveryDelay 设置熔断后自动恢复延迟（默认 0=禁用）。
// 当节点被标记为不健康后，经过 d 时长会自动重置为健康（半开状态）；
// 若下次真实请求依然失败，节点会再次进入不健康状态。
func WithRecoveryDelay(d time.Duration) HealthCheckerOption {
	return func(h *HealthChecker) { h.recoveryDelay = d }
}

// WithHealthPaths 设置各 target 的主动健康检查路径（targetID → path）。
// 无 path 或 path 为空的 target 在主动检查循环中跳过；仅使用被动熔断。
// 此 option 优先级高于 WithHealthPath（对有显式 path 的 target）。
func WithHealthPaths(paths map[string]string) HealthCheckerOption {
	return func(h *HealthChecker) {
		h.healthPaths = make(map[string]string, len(paths))
		for k, v := range paths {
			h.healthPaths[k] = v
		}
	}
}

// WithCredentials 设置各 target 的健康检查认证凭证（targetID → TargetCredential）。
// 凭证用于对接需要认证的大厂 API（OpenAI、Anthropic、阿里百炼、火山引擎等）。
// 无凭证或 APIKey 为空的 target 不注入认证头（适用本地 vLLM/sglang）。
func WithCredentials(creds map[string]TargetCredential) HealthCheckerOption {
	return func(h *HealthChecker) {
		h.credentials = make(map[string]TargetCredential, len(creds))
		for k, v := range creds {
			h.credentials[k] = v
		}
	}
}

// NewHealthChecker 创建并返回 HealthChecker。
func NewHealthChecker(balancer Balancer, logger *zap.Logger, opts ...HealthCheckerOption) *HealthChecker {
	hc := &HealthChecker{
		balancer:      balancer,
		logger:        logger.Named("health_checker"),
		interval:      defaultCheckInterval,
		timeout:       defaultCheckTimeout,
		healthPath:    defaultHealthPath,
		failThreshold: defaultFailThreshold,
		failures:      make(map[string]int),
		client:        &http.Client{Timeout: defaultCheckTimeout},
		healthPaths:   make(map[string]string),
		credentials:   make(map[string]TargetCredential),
	}
	for _, opt := range opts {
		opt(hc)
	}
	return hc
}

// SetNotifier 设置告警通知器（可选；nil 则不发告警）。
// 节点变为不健康时发送 EventNodeDown，恢复时发送 EventNodeRecovered。
func (hc *HealthChecker) SetNotifier(n *alert.Notifier) {
	hc.notifier = n
}

// UpdateHealthPaths 在运行时原子替换 healthPaths 映射（targetID → health check path）。
// 供目标列表变更（增删启停）时调用，无需重启进程。
func (hc *HealthChecker) UpdateHealthPaths(paths map[string]string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.healthPaths = make(map[string]string, len(paths))
	for k, v := range paths {
		hc.healthPaths[k] = v
	}
}

// UpdateCredentials 在运行时原子替换 credentials 映射（targetID → TargetCredential）。
// 供目标列表变更（增删启停）时调用，无需重启进程。
func (hc *HealthChecker) UpdateCredentials(creds map[string]TargetCredential) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.credentials = make(map[string]TargetCredential, len(creds))
	for k, v := range creds {
		hc.credentials[k] = v
	}
	hc.logger.Info("credentials updated",
		zap.Int("count", len(creds)),
	)
}

// Start 启动主动健康检查循环。
// 调用方应在完成后通过取消 ctx 来停止循环，然后调用 Wait 等待所有 goroutine 完成。
func (hc *HealthChecker) Start(ctx context.Context) {
	hc.wg.Add(1)
	go hc.loop(ctx)
}

// CheckTarget 立即对指定 target 发起一次主动健康检查（异步，不阻塞调用方）。
// 供新 target 加入时立即验证，无需等待下一个 ticker 周期（最长 30s）。
// 若 target 不在 balancer 中或无对应 health path，则不做任何事。
func (hc *HealthChecker) CheckTarget(id string) {
	targets := hc.balancer.Targets()

	hc.mu.Lock()
	paths := make(map[string]string, len(hc.healthPaths))
	for k, v := range hc.healthPaths {
		paths[k] = v
	}
	hc.mu.Unlock()

	for _, t := range targets {
		if t.ID != id {
			continue
		}
		if len(paths) > 0 {
			path, ok := paths[id]
			if !ok || path == "" {
				return // 该 target 无主动检查路径
			}
			hc.wg.Add(1)
			go func(tgt Target, p string) {
				defer hc.wg.Done()
				hc.checkOneWithPath(tgt, p)
			}(t, path)
		} else {
			hc.wg.Add(1)
			go func(tgt Target) {
				defer hc.wg.Done()
				hc.checkOne(tgt)
			}(t)
		}
		return
	}
}

func (hc *HealthChecker) loop(ctx context.Context) {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	// 启动时立即检查一轮
	hc.checkAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.checkAll()
		}
	}
}

func (hc *HealthChecker) checkAll() {
	targets := hc.balancer.Targets()

	// 持有锁期间拷贝 healthPaths，避免在 checkOneWithPath 期间持锁
	hc.mu.Lock()
	paths := make(map[string]string, len(hc.healthPaths))
	for k, v := range hc.healthPaths {
		paths[k] = v
	}
	hc.mu.Unlock()

	for _, t := range targets {
		// 若 healthPaths 非空，仅检查其中有路径的 target
		if len(paths) > 0 {
			path, ok := paths[t.ID]
			if !ok || path == "" {
				continue // 无主动检查路径，依赖被动熔断
			}
			hc.wg.Add(1)
			go func(tgt Target, p string) {
				defer hc.wg.Done()
				hc.checkOneWithPath(tgt, p)
			}(t, path)
		} else {
			hc.wg.Add(1)
			go func(tgt Target) {
				defer hc.wg.Done()
				hc.checkOne(tgt)
			}(t)
		}
	}
}

func (hc *HealthChecker) checkOne(t Target) {
	hc.checkOneWithPath(t, hc.healthPath)
}

// injectAuth 根据 provider 类型将认证信息注入 HTTP 请求。
// Anthropic：注入 x-api-key 头和 anthropic-version 头；
// 其他（OpenAI/OpenAI-compatible）：注入 Authorization: Bearer 头；
// 无凭证：不注入任何认证头（用于本地 vLLM/sglang 等无认证端点）。
func (hc *HealthChecker) injectAuth(req *http.Request, targetID string) {
	hc.mu.Lock()
	cred, ok := hc.credentials[targetID]
	hc.mu.Unlock()
	if !ok || cred.APIKey == "" {
		hc.logger.Debug("no credential for target", zap.String("target", targetID))
		return
	}

	switch cred.Provider {
	case "anthropic":
		// Anthropic 使用 x-api-key 而非标准 Bearer，且需要 anthropic-version 版本头
		req.Header.Set("x-api-key", cred.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		hc.logger.Debug("injected Anthropic auth", zap.String("target", targetID))
	default:
		// OpenAI、OpenAI-compatible（DashScope、Ark 等）、ollama、空字符串等
		req.Header.Set("Authorization", "Bearer "+cred.APIKey)
		hc.logger.Debug("injected Bearer auth",
			zap.String("target", targetID),
			zap.String("provider", cred.Provider),
		)
	}
}

func (hc *HealthChecker) checkOneWithPath(t Target, healthPath string) {
	url := t.Addr + healthPath

	// 使用带超时的 context，避免健康检查无限阻塞
	ctx, cancel := context.WithTimeout(context.Background(), hc.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		hc.logger.Debug("health check: failed to create request",
			zap.String("target", t.ID),
			zap.Error(err),
		)
		hc.recordFailure(t.ID)
		return
	}

	// 注入认证信息（根据 provider 类型）
	hc.injectAuth(req, t.ID)

	resp, err := hc.client.Do(req)
	if err != nil {
		hc.logger.Debug("health check failed",
			zap.String("target", t.ID),
			zap.Error(err),
		)
		hc.recordFailure(t.ID)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		hc.logger.Debug("health check ok", zap.String("target", t.ID))
		hc.recordSuccess(t.ID)
	} else {
		hc.logger.Debug("health check non-200",
			zap.String("target", t.ID),
			zap.Int("status", resp.StatusCode),
		)
		hc.recordFailure(t.ID)
	}
}

// RecordSuccess 被动上报：请求成功，重置连续失败计数，恢复健康状态。
func (hc *HealthChecker) RecordSuccess(id string) {
	hc.recordSuccess(id)
}

// RecordFailure 被动上报：请求失败，增加连续失败计数，达阈值则标记不健康。
func (hc *HealthChecker) RecordFailure(id string) {
	hc.recordFailure(id)
}

func (hc *HealthChecker) recordSuccess(id string) {
	hc.mu.Lock()
	wasUnhealthy := hc.failures[id] >= hc.failThreshold
	hc.failures[id] = 0
	hc.mu.Unlock()

	if wasUnhealthy {
		hc.logger.Info("target recovered", zap.String("target", id))
		if hc.notifier != nil {
			hc.notifier.Notify(alert.Event{
				Kind:    alert.EventNodeRecovered,
				Message: "s-proxy target recovered: " + id,
				Labels:  map[string]string{"target": id},
			})
		}
	}
	hc.balancer.MarkHealthy(id)
}

func (hc *HealthChecker) recordFailure(id string) {
	hc.mu.Lock()
	hc.failures[id]++
	count := hc.failures[id]
	hc.mu.Unlock()

	if count >= hc.failThreshold {
		hc.logger.Warn("target marked unhealthy",
			zap.String("target", id),
			zap.Int("consecutive_failures", count),
		)
		hc.balancer.MarkUnhealthy(id)
		if hc.notifier != nil {
			hc.notifier.Notify(alert.Event{
				Kind:    alert.EventNodeDown,
				Message: "s-proxy target marked unhealthy: " + id,
				Labels: map[string]string{
					"target":   id,
					"failures": strconv.Itoa(count),
				},
			})
		}
		// 自动恢复（半开状态）：经过 recoveryDelay 后自动重置为健康
		if hc.recoveryDelay > 0 {
			go func() {
				time.Sleep(hc.recoveryDelay)
				hc.mu.Lock()
				// 若失败计数已被 RecordSuccess 重置（表示已被另一路径恢复），跳过
				if hc.failures[id] < hc.failThreshold {
					hc.mu.Unlock()
					return
				}
				hc.failures[id] = 0
				hc.mu.Unlock()
				hc.balancer.MarkHealthy(id)
				hc.logger.Info("target auto-recovered after delay",
					zap.String("target", id),
					zap.Duration("recovery_delay", hc.recoveryDelay),
				)
			}()
		}
	} else {
		hc.logger.Debug("target failure recorded",
			zap.String("target", id),
			zap.Int("consecutive_failures", count),
			zap.Int("threshold", hc.failThreshold),
		)
	}
}
