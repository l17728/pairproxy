package lb

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
)

const (
	defaultFailThreshold  = 3            // 连续失败次数阈值
	defaultCheckInterval  = 30 * time.Second
	defaultCheckTimeout   = 5 * time.Second
	defaultHealthPath     = "/health"
	defaultProbeCacheTTL  = 2 * time.Hour // 探活策略缓存有效期
)

// TargetCredential 健康检查认证凭证（按 provider 类型注入不同认证头）。
type TargetCredential struct {
	APIKey   string // 明文 key（健康检查时直接使用）
	Provider string // "anthropic" | "openai" | "ollama" | ""（空=无认证）
}

// HealthChecker 对 Balancer 中的目标节点进行主动健康检查。
//
// 主动检查：每隔 interval 对每个节点发 GET /health（或自定义路径），200 视为健康。
// 智能探活：未配置 health_check_path 的 target 自动尝试多种探活策略（/health、
//   /v1/models、/v1/messages 等），找到有效策略后缓存，后续直接复用。
// 认证支持：支持 Bearer 认证（OpenAI/OpenAI-compatible）和 x-api-key 认证（Anthropic）。
// 被动熔断：调用方通过 RecordSuccess/RecordFailure 上报结果，
// 连续 failThreshold 次失败后将节点标记为不健康。
// 自动恢复：recoveryDelay > 0 时，节点进入不健康状态后等待 recoveryDelay，然后发起即时健康检查；
// 检查通过才恢复健康，检查失败则继续标记为不健康并再次等待（检查驱动，非半开状态直接放行）。
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
	healthPaths   map[string]string              // targetID → 用户显式配置的 health check path
	credentials   map[string]TargetCredential    // targetID → credential（用于主动健康检查认证）
	providers     map[string]string              // targetID → provider（用于探活策略选择）

	// 智能探活：对未配置 health_check_path 的 target 自动发现有效探活策略
	prober     *Prober
	probeCache *ProbeCache

	mu       sync.Mutex
	failures map[string]int // 连续失败计数

	wg     sync.WaitGroup  // tracks in-flight check goroutines
	stopCh chan struct{}    // closed when Start's ctx is cancelled; signals recovery goroutines to exit
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
		h.prober = NewProber(d, h.logger)
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

// WithRecoveryDelay 设置熔断后自动恢复的检查延迟（默认 0=禁用）。
// 当节点被标记为不健康后，等待 d 时长，然后发起即时健康检查：
//   - 检查通过 → 节点恢复健康；
//   - 检查失败 → 节点继续不健康，再次等待 d 时长后重试。
// 注意：recoveryDelay goroutine 依赖 Start() 关闭的 stopCh 信号来感知关闭；
// 若仅使用被动熔断（不调用 Start），应将 recoveryDelay 设为 0（默认值）。
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
	named := logger.Named("health_checker")
	hc := &HealthChecker{
		balancer:      balancer,
		logger:        named,
		interval:      defaultCheckInterval,
		timeout:       defaultCheckTimeout,
		healthPath:    defaultHealthPath,
		failThreshold: defaultFailThreshold,
		failures:      make(map[string]int),
		client:        &http.Client{Timeout: defaultCheckTimeout},
		healthPaths:   make(map[string]string),
		credentials:   make(map[string]TargetCredential),
		providers:     make(map[string]string),
		prober:        NewProber(defaultCheckTimeout, named),
		probeCache:    NewProbeCache(defaultProbeCacheTTL),
		stopCh:        make(chan struct{}),
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
// 配置变更时同时清理失去显式路径的 target 的探活缓存，强制重新探活。
func (hc *HealthChecker) UpdateHealthPaths(paths map[string]string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	// 失去显式路径的 target 将重新走智能探活；清理旧缓存避免用到过期策略
	for id := range hc.healthPaths {
		if _, stillExplicit := paths[id]; !stillExplicit {
			hc.probeCache.invalidate(id)
		}
	}
	hc.healthPaths = make(map[string]string, len(paths))
	for k, v := range paths {
		hc.healthPaths[k] = v
	}
}

// UpdateCredentials 在运行时原子替换 credentials 映射（targetID → TargetCredential）。
// 供目标列表变更（增删启停）时调用，无需重启进程。
// 同时将 provider 信息同步到 providers map，供智能探活策略选择使用。
// 配置变更时同时清理相关 target 的探活缓存，强制重新探测。
func (hc *HealthChecker) UpdateCredentials(creds map[string]TargetCredential) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// 找出新旧 map 中有差异的 targetID（新增、删除、key 变更），清理其探活缓存
	for id, oldCred := range hc.credentials {
		newCred, exists := creds[id]
		if !exists || newCred.APIKey != oldCred.APIKey || newCred.Provider != oldCred.Provider {
			hc.probeCache.invalidate(id)
		}
	}
	for id := range creds {
		if _, exists := hc.credentials[id]; !exists {
			hc.probeCache.invalidate(id) // 新 target：清理（以防万一）
		}
	}

	hc.credentials = make(map[string]TargetCredential, len(creds))
	hc.providers = make(map[string]string, len(creds))
	for k, v := range creds {
		hc.credentials[k] = v
		hc.providers[k] = v.Provider
	}
	hc.logger.Info("credentials updated",
		zap.Int("count", len(creds)),
	)
}

// Start 启动主动健康检查循环。
// 调用方应在完成后通过取消 ctx 来停止循环，然后调用 Wait 等待所有 goroutine 完成。
// 每次调用 Start 会重置 stopCh，使本次循环启动的 recoveryDelay goroutine 可以感知关闭信号。
// 不支持同时调用多次 Start（每次 Start 必须等待上一次停止后再调用）。
func (hc *HealthChecker) Start(ctx context.Context) {
	// 每次启动重新创建 stopCh，避免多次 Start 导致 close 已关闭 channel 的 panic。
	hc.stopCh = make(chan struct{})
	hc.wg.Add(1)
	go hc.loop(ctx)
}

// CheckTarget 立即对指定 target 发起一次主动健康检查（异步，不阻塞调用方）。
// 供新 target 加入时立即验证，无需等待下一个 ticker 周期（最长 30s）。
// 若 target 不在 balancer 中，则不做任何事。
// 优先使用用户显式配置的 health_check_path；否则走智能探活。
func (hc *HealthChecker) CheckTarget(id string) {
	targets := hc.balancer.Targets()

	hc.mu.Lock()
	paths := make(map[string]string, len(hc.healthPaths))
	for k, v := range hc.healthPaths {
		paths[k] = v
	}
	creds := make(map[string]TargetCredential, len(hc.credentials))
	for k, v := range hc.credentials {
		creds[k] = v
	}
	providers := make(map[string]string, len(hc.providers))
	for k, v := range hc.providers {
		providers[k] = v
	}
	hc.mu.Unlock()

	for _, t := range targets {
		if t.ID != id {
			continue
		}
		cred := creds[id]
		provider := providers[id]
		explicitPath, hasExplicit := paths[id]

		if hasExplicit && explicitPath != "" {
			hc.wg.Add(1)
			go func(tgt Target, p string, c TargetCredential) {
				defer hc.wg.Done()
				hc.checkOneExplicit(tgt, p, &c)
			}(t, explicitPath, cred)
		} else {
			hc.wg.Add(1)
			go func(tgt Target, c TargetCredential, prov string) {
				defer hc.wg.Done()
				hc.checkOneSmart(tgt, &c, prov)
			}(t, cred, provider)
		}
		return
	}
}

func (hc *HealthChecker) loop(ctx context.Context) {
	// CRITICAL: defer hc.wg.Done() matches the hc.wg.Add(1) in Start().
	// This ensures the WaitGroup counter is decremented when the loop exits,
	// allowing Wait() to return when all goroutines (main loop + children) are complete.
	defer hc.wg.Done()
	// Signal recovery goroutines to stop when the loop exits.
	defer close(hc.stopCh)

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

	// 持有锁期间拷贝 healthPaths/credentials/providers/healthPath，避免在检查期间持锁
	hc.mu.Lock()
	paths := make(map[string]string, len(hc.healthPaths))
	for k, v := range hc.healthPaths {
		paths[k] = v
	}
	creds := make(map[string]TargetCredential, len(hc.credentials))
	for k, v := range hc.credentials {
		creds[k] = v
	}
	providers := make(map[string]string, len(hc.providers))
	for k, v := range hc.providers {
		providers[k] = v
	}
	globalHealthPath := hc.healthPath // 在锁内拷贝，避免并发写入时的数据竞争
	hc.mu.Unlock()

	for _, t := range targets {
		explicitPath, hasExplicit := paths[t.ID]
		// WithHealthPath 设置的全局路径（非默认值）也视为显式配置
		if !hasExplicit && globalHealthPath != "" && globalHealthPath != defaultHealthPath {
			explicitPath = globalHealthPath
			hasExplicit = true
		}
		cred := creds[t.ID]
		provider := providers[t.ID]

		if hasExplicit && explicitPath != "" {
			// 用户显式配置了 health_check_path：直接使用，不走自动探测
			hc.wg.Add(1)
			go func(tgt Target, p string, c TargetCredential) {
				defer hc.wg.Done()
				hc.checkOneExplicit(tgt, p, &c)
			}(t, explicitPath, cred)
		} else {
			// 无显式路径：走智能探活（缓存优先，缓存未命中则 discover）
			hc.wg.Add(1)
			go func(tgt Target, c TargetCredential, prov string) {
				defer hc.wg.Done()
				hc.checkOneSmart(tgt, &c, prov)
			}(t, cred, provider)
		}
	}
}

// checkOneExplicit 使用用户显式配置的路径执行健康检查（注入认证）。
func (hc *HealthChecker) checkOneExplicit(t Target, healthPath string, cred *TargetCredential) {
	if cred == nil {
		// 从 credentials 取
		hc.mu.Lock()
		c, ok := hc.credentials[t.ID]
		hc.mu.Unlock()
		if ok {
			cred = &c
		}
	}
	hc.checkOneWithPath(t, healthPath, cred)
}

// checkOneSmart 智能探活：优先使用缓存策略，缓存未命中则自动 discover。
func (hc *HealthChecker) checkOneSmart(t Target, cred *TargetCredential, provider string) {
	// 1. 查缓存
	entry := hc.probeCache.get(t.ID)
	if entry != nil {
		if entry.method == nil {
			// 遗留的 nil-method 缓存条目（旧版本写入）：
			// 当前版本不再写入此类条目；若缓存中存在，清除后重新 Discover。
			hc.probeCache.invalidate(t.ID)
		} else {
			// 缓存命中：直接用已知策略
			hasCredential := cred != nil && cred.APIKey != ""
			checkCtx, checkCancel := context.WithTimeout(context.Background(), hc.timeout)
			result := hc.prober.CheckWithMethod(checkCtx, t.Addr, t.ID, entry.method, cred)
			checkCancel() // 立即释放 timer，不用 defer（函数此后有多条路径，defer 会延迟释放）
			if result.okWithAuth(hasCredential) {
				hc.logger.Debug("health check ok", zap.String("target", t.ID))
				hc.recordSuccess(t.ID)
			} else if result.definitivelyUnhealthy() {
				hc.logger.Debug("health check failed (connection error)",
					zap.String("target", t.ID),
					zap.Error(result.err),
				)
				hc.probeCache.invalidate(t.ID)
				hc.recordFailure(t.ID)
			} else {
				// 非 ok 的 HTTP 状态：key 失效或路径变更，清缓存重新 discover
				hc.logger.Debug("health check non-ok status",
					zap.String("target", t.ID),
					zap.Int("status", result.status),
				)
				hc.probeCache.invalidate(t.ID)
				hc.recordFailure(t.ID)
			}
			return
		}
	}

	// 2. 缓存未命中（或 unreachable 被清除）：自动发现有效策略
	hc.logger.Info("smart probe: discovering health check method for target",
		zap.String("target", t.ID),
		zap.String("addr", t.Addr),
		zap.String("provider", provider),
	)

	// 给 discover 足够的时间：每个策略 1 个 timeout
	methods := selectMethods(provider) // 按 provider 过滤后的策略列表
	discoverTimeout := hc.timeout * time.Duration(len(methods)+1)
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	found, unreachable := hc.prober.Discover(ctx, t.Addr, t.ID, provider, cred)
	cancel() // 立即释放 timer（Discover 已返回，ctx 不再使用）

	if unreachable {
		// 连接层失败（拒绝连接/超时）：服务不可达
		// 不缓存 unreachable 标记——下次心跳直接重试 Discover，
		// 避免 2h TTL 内服务恢复后仍无法被重新探活（死锁）。
		hc.recordFailure(t.ID)
		return
	}

	if found == nil {
		// 服务有 HTTP 响应但所有路径均不匹配（如全部 5xx）——
		// 不缓存此结果：与 unreachable 一样，每次心跳重试 Discover，
		// 以便服务路径恢复后（5xx→200）能及时被重新探活。
		hc.logger.Warn("smart probe: no suitable health check method found, recording failure",
			zap.String("target", t.ID),
			zap.String("provider", provider),
		)
		hc.recordFailure(t.ID)
		return
	}

	// 找到有效策略：写入缓存，然后用该策略执行一次实际健康检查
	hc.probeCache.set(t.ID, found)
	// 注意：Discover 阶段 401/403 视为"端点存在"，但心跳阶段需用真实凭证判断
	hasCredential := cred != nil && cred.APIKey != ""
	checkCtx, checkCancel := context.WithTimeout(context.Background(), hc.timeout)
	result := hc.prober.CheckWithMethod(checkCtx, t.Addr, t.ID, found, cred)
	checkCancel() // 立即释放 timer
	if result.okWithAuth(hasCredential) {
		hc.logger.Debug("smart probe: initial health check ok after discovery", zap.String("target", t.ID))
		hc.recordSuccess(t.ID)
	} else {
		hc.logger.Debug("smart probe: initial health check failed after discovery",
			zap.String("target", t.ID),
			zap.Int("status", result.status),
		)
		hc.recordFailure(t.ID)
	}
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
	injectCredential(req, &cred)
	switch cred.Provider {
	case "anthropic":
		hc.logger.Debug("injected Anthropic auth", zap.String("target", targetID))
	default:
		hc.logger.Debug("injected Bearer auth",
			zap.String("target", targetID),
			zap.String("provider", cred.Provider),
		)
	}
}

func (hc *HealthChecker) checkOneWithPath(t Target, healthPath string, cred *TargetCredential) {
	url := buildProbeURL(t.Addr, healthPath)

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

	// 注入认证信息
	if cred != nil {
		injectCredential(req, cred)
	} else {
		hc.injectAuth(req, t.ID)
	}

	resp, err := hc.client.Do(req)
	if err != nil {
		hc.logger.Debug("health check failed",
			zap.String("target", t.ID),
			zap.Error(err),
		)
		hc.recordFailure(t.ID)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

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
		// 自动恢复：等待 recoveryDelay 后根据是否配置主动健康检查选择恢复策略。
		// 只在首次达到阈值时（count == failThreshold）启动恢复 goroutine，
		// 避免后续连续失败重复启动多个 goroutine 造成堆积。
		if hc.recoveryDelay > 0 && count == hc.failThreshold {
			hc.wg.Add(1)
			go func() {
				defer hc.wg.Done()
				select {
				case <-time.After(hc.recoveryDelay):
				case <-hc.stopCh:
					// 主循环已停止（进程关闭），跳过恢复——避免 Wait() 阻塞整个 recoveryDelay
					return
				}
				hc.mu.Lock()
				// 若失败计数已被 RecordSuccess 重置（表示已被另一路径恢复），跳过
				if hc.failures[id] < hc.failThreshold {
					hc.mu.Unlock()
					return
				}
				// 判断是否配置了主动健康检查（有认证凭证或显式路径）
				_, hasCredential := hc.credentials[id]
				_, hasExplicitPath := hc.healthPaths[id]
				hasActiveCheck := hasCredential || hasExplicitPath

				if hasActiveCheck {
					// 检查驱动恢复（有主动健康检查配置）：重置到阈值-1，由 CheckTarget 结果决定；
					// CheckTarget 成功 → recordSuccess → MarkHealthy；
					// CheckTarget 失败 → failures 回到 failThreshold，再次触发恢复 goroutine。
					// 避免 API Key 失效、认证错误等场景出现假阳性健康状态。
					hc.failures[id] = hc.failThreshold - 1
					hc.mu.Unlock()
					hc.logger.Info("target recovery check triggered",
						zap.String("target", id),
						zap.Duration("recovery_delay", hc.recoveryDelay),
					)
					hc.CheckTarget(id)
				} else {
					// 半开恢复（纯被动熔断，无主动检查配置）：直接放行，让真实流量检验健康状态；
					// 若下次真实请求失败，RecordFailure 会再次触发熔断。
					hc.failures[id] = 0
					hc.mu.Unlock()
					hc.balancer.MarkHealthy(id)
					hc.logger.Info("target auto-recovered after delay (passive mode)",
						zap.String("target", id),
						zap.Duration("recovery_delay", hc.recoveryDelay),
					)
				}
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
