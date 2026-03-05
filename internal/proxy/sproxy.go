package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/metrics"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/tap"
	"github.com/l17728/pairproxy/internal/version"
)

// LLMTarget 代表一个 LLM 后端（含 API Key 和 provider 类型）。
type LLMTarget struct {
	URL      string
	APIKey   string
	Provider string // "anthropic"（默认）| "openai" | "ollama"
	Name     string // 可选显示名，空则用 URL
	Weight   int    // 负载均衡权重（≥1）
}

// LLMTargetStatus 向 Admin/Dashboard 暴露的 LLM 目标运行时状态。
type LLMTargetStatus struct {
	URL      string
	Name     string
	Provider string
	Weight   int
	Healthy  bool
	Draining bool // 是否处于排水模式
}

// SProxy s-proxy 核心处理器
type SProxy struct {
	logger          *zap.Logger
	jwtMgr          *auth.Manager
	writer          *db.UsageWriter
	targets         []LLMTarget
	idx             atomic.Uint32 // 轮询计数器（无 LLM 均衡器时使用）
	transport       http.RoundTripper
	clusterMgr      *cluster.Manager // 可选，nil 表示单节点模式（不注入路由头）
	sourceNode      string           // 来源节点标识（用于 usage_logs）
	quotaChecker    *quota.Checker   // 可选，nil 表示不检查配额
	startTime       time.Time        // 进程启动时间（供 /health 返回 uptime）
	activeRequests  atomic.Int64     // 当前正在处理的代理请求数
	sqlDB           *sql.DB          // 可选，用于 /health 检查 DB 可达性
	apiKeyResolver  func(userID string) (apiKey string, found bool) // 可选，动态 API Key 解析

	// 排水模式控制
	draining     atomic.Bool   // 排水模式标志
	drainReason  string        // 排水原因（用于日志和状态查询）
	drainStarted time.Time     // 排水开始时间

	// LLM 均衡 + 绑定（可选）
	llmBalancer     *lb.WeightedRandomBalancer                       // 加权随机负载均衡
	llmHC           *lb.HealthChecker                                // 健康检查（被动熔断 + 自动恢复）
	bindingResolver func(userID, groupID string) (string, bool)      // 用户/分组 → target URL
	maxRetries      int                                               // RetryTransport 最大重试次数

	debugLogger atomic.Pointer[zap.Logger] // 可选，非 nil 时将转发内容写入独立 debug 文件
	notifier    *alert.Notifier             // 可选，非 nil 时发送 high_load/load_recovered 告警
}

// NewSProxy 创建 SProxy。
// targets 至少需要一个 LLM 后端。
func NewSProxy(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
) (*SProxy, error) {
	return newSProxy(logger, jwtMgr, writer, targets, nil, "local")
}

// NewSProxyWithCluster 创建带集群管理器的 SProxy（sp-1 模式）。
func NewSProxyWithCluster(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
	clusterMgr *cluster.Manager,
	sourceNode string,
) (*SProxy, error) {
	return newSProxy(logger, jwtMgr, writer, targets, clusterMgr, sourceNode)
}

func newSProxy(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
	clusterMgr *cluster.Manager,
	sourceNode string,
) (*SProxy, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one LLM target is required")
	}
	sp := &SProxy{
		logger:     logger.Named("sproxy"),
		jwtMgr:     jwtMgr,
		writer:     writer,
		targets:    targets,
		transport:  http.DefaultTransport,
		clusterMgr: clusterMgr,
		sourceNode: sourceNode,
		startTime:  time.Now(),
		maxRetries: 2,
	}
	return sp, nil
}

// SetQuotaChecker 设置配额检查器（可选；设置后每次请求前检查配额）。
func (sp *SProxy) SetQuotaChecker(checker *quota.Checker) {
	sp.quotaChecker = checker
}

// SetDB 设置数据库连接供健康检查使用（可选）。
// 健康检查时会通过 PingContext 验证数据库可达性。
func (sp *SProxy) SetDB(gormDB interface{ DB() (*sql.DB, error) }) {
	if sqlDB, err := gormDB.DB(); err == nil {
		sp.sqlDB = sqlDB
		sp.logger.Debug("health check: database connection set for ping")
	} else {
		sp.logger.Warn("health check: failed to get underlying sql.DB", zap.Error(err))
	}
}

// SetAPIKeyResolver 设置动态 API Key 解析器（可选）。
// fn 根据 userID 返回解密后的 API Key；found=false 时回退到配置文件中的静态 Key。
func (sp *SProxy) SetAPIKeyResolver(fn func(userID string) (string, bool)) {
	sp.apiKeyResolver = fn
}

// SetLLMHealthChecker 设置 LLM 负载均衡器和健康检查器（可选）。
// 设置后启用基于健康状态的加权随机路由和被动熔断；不设置则退化为简单轮询。
func (sp *SProxy) SetLLMHealthChecker(bal *lb.WeightedRandomBalancer, hc *lb.HealthChecker) {
	sp.llmBalancer = bal
	sp.llmHC = hc
}

// SetBindingResolver 设置用户/分组 LLM 绑定解析器（可选）。
// fn 根据 userID + groupID 返回绑定的 target URL；未绑定时 found=false，回退到负载均衡。
func (sp *SProxy) SetBindingResolver(fn func(userID, groupID string) (string, bool)) {
	sp.bindingResolver = fn
}

// SetMaxRetries 设置 RetryTransport 的最大重试次数（默认 2）。
func (sp *SProxy) SetMaxRetries(n int) {
	sp.maxRetries = n
}

// SetTransport 设置底层 HTTP transport（测试用；默认 http.DefaultTransport）。
func (sp *SProxy) SetTransport(t http.RoundTripper) {
	sp.transport = t
}

// SetDebugLogger 设置 debug 文件日志器。
// 非 nil 时，每个请求的转发内容（请求体、响应体、SSE chunks）均会写入该 logger。
func (sp *SProxy) SetDebugLogger(l *zap.Logger) {
	sp.debugLogger.Store(l)
}

// SyncAndSetDebugLogger 先 Sync 旧 logger（flush 缓冲区），再原子切换为新 logger。
// 供 SIGHUP 热重载时调用；传入 nil 表示关闭 debug 日志。
func (sp *SProxy) SyncAndSetDebugLogger(l *zap.Logger) {
	if old := sp.debugLogger.Load(); old != nil {
		_ = old.Sync()
	}
	sp.debugLogger.Store(l)
}

// Drain 进入排水模式。
// 排水模式下，节点仍可处理现有请求，但不再接受新流量（通过集群路由表通知其他节点）。
func (sp *SProxy) Drain() error {
	if sp.draining.Load() {
		return nil // 已经在排水模式
	}
	sp.draining.Store(true)
	sp.drainStarted = time.Now()
	sp.drainReason = "admin requested"

	sp.logger.Info("node entering drain mode",
		zap.Int64("active_requests", sp.activeRequests.Load()),
	)

	// 如果有集群管理器，通知其他节点
	if sp.clusterMgr != nil && sp.sourceNode != "" {
		sp.clusterMgr.DrainNode(sp.sourceNode)
	}

	return nil
}

// Undrain 退出排水模式，恢复正常流量接收。
func (sp *SProxy) Undrain() error {
	if !sp.draining.Load() {
		return nil // 不在排水模式
	}
	sp.draining.Store(false)
	sp.drainReason = ""

	sp.logger.Info("node exited drain mode",
		zap.Int64("active_requests", sp.activeRequests.Load()),
	)

	// 如果有集群管理器，通知其他节点
	if sp.clusterMgr != nil && sp.sourceNode != "" {
		sp.clusterMgr.UndrainNode(sp.sourceNode)
	}

	return nil
}

// IsDraining 返回当前是否处于排水模式。
func (sp *SProxy) IsDraining() bool {
	return sp.draining.Load()
}

// DrainStatus 返回排水模式的详细状态。
type DrainStatus struct {
	Draining       bool      `json:"draining"`
	ActiveRequests int64     `json:"active_requests"`
	DrainStarted   time.Time `json:"drain_started,omitempty"`
	DrainReason    string    `json:"drain_reason,omitempty"`
}

// GetDrainStatus 返回排水模式的详细状态。
func (sp *SProxy) GetDrainStatus() DrainStatus {
	return DrainStatus{
		Draining:       sp.draining.Load(),
		ActiveRequests: sp.activeRequests.Load(),
		DrainStarted:   sp.drainStarted,
		DrainReason:    sp.drainReason,
	}
}

// ActiveRequests 返回当前活跃请求数。
func (sp *SProxy) ActiveRequests() int64 {
	return sp.activeRequests.Load()
}

// SetNotifier 设置告警通知器（可选）。
// 设置后，StartActiveRequestsMonitor 会在活跃请求数越过/恢复阈值时触发 webhook 告警。
func (sp *SProxy) SetNotifier(n *alert.Notifier) {
	sp.notifier = n
}

// StartActiveRequestsMonitor 启动活跃请求数阈值监控。
// threshold=0 或 notifier=nil 时为 no-op（不启动 goroutine）。
// 内部每 10 秒采样一次；越过阈值时触发 EventHighLoad，恢复后触发 EventLoadRecovered。
// 边沿触发：持续超载只触发一次告警，不产生告警风暴。
func StartActiveRequestsMonitor(
	ctx context.Context,
	sp *SProxy,
	threshold int64,
	notifier *alert.Notifier,
	sourceNode string,
	logger *zap.Logger,
) {
	startActiveRequestsMonitor(ctx, sp, threshold, notifier, sourceNode, logger, 10*time.Second)
}

// startActiveRequestsMonitor 是可测试的内部实现，interval 可由测试注入短周期。
func startActiveRequestsMonitor(
	ctx context.Context,
	sp *SProxy,
	threshold int64,
	notifier *alert.Notifier,
	sourceNode string,
	logger *zap.Logger,
	interval time.Duration,
) {
	if threshold <= 0 || notifier == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var overThreshold bool
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := sp.activeRequests.Load()
				if !overThreshold && current >= threshold {
					overThreshold = true
					logger.Warn("active_requests exceeded alert threshold",
						zap.Int64("active_requests", current),
						zap.Int64("threshold", threshold),
					)
					notifier.Notify(alert.Event{
						Kind:    alert.EventHighLoad,
						Message: fmt.Sprintf("active requests %d exceeded threshold %d", current, threshold),
						Labels: map[string]string{
							"node":      sourceNode,
							"current":   strconv.FormatInt(current, 10),
							"threshold": strconv.FormatInt(threshold, 10),
						},
					})
				} else if overThreshold && current < threshold {
					overThreshold = false
					logger.Info("active_requests recovered below alert threshold",
						zap.Int64("active_requests", current),
						zap.Int64("threshold", threshold),
					)
					notifier.Notify(alert.Event{
						Kind:    alert.EventLoadRecovered,
						Message: fmt.Sprintf("active requests %d recovered below threshold %d", current, threshold),
						Labels: map[string]string{
							"node":      sourceNode,
							"current":   strconv.FormatInt(current, 10),
							"threshold": strconv.FormatInt(threshold, 10),
						},
					})
				}
			}
		}
	}()
	logger.Info("active requests monitor started", zap.Int64("threshold", threshold))
}

// LLMTargetStatuses 返回当前所有 LLM 目标的运行时状态（含健康状态）。
// 若未配置均衡器，则所有目标视为健康（无主动/被动检查）。
func (sp *SProxy) LLMTargetStatuses() []LLMTargetStatus {
	if sp.llmBalancer == nil {
		result := make([]LLMTargetStatus, len(sp.targets))
		for i, t := range sp.targets {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			result[i] = LLMTargetStatus{
				URL:      t.URL,
				Name:     t.Name,
				Provider: t.Provider,
				Weight:   w,
				Healthy:  true,
				Draining: false,
			}
		}
		return result
	}

	lbTargets := sp.llmBalancer.Targets()
	result := make([]LLMTargetStatus, 0, len(lbTargets))
	for _, t := range lbTargets {
		st := LLMTargetStatus{
			URL:      t.ID,
			Weight:   t.Weight,
			Healthy:  t.Healthy,
			Draining: t.Draining,
		}
		for _, lt := range sp.targets {
			if lt.URL == t.ID {
				st.Name = lt.Name
				st.Provider = lt.Provider
				break
			}
		}
		result = append(result, st)
	}
	return result
}

// Handler 构建并返回完整的 s-proxy HTTP 处理链：
//
//	RecoveryMiddleware → RequestIDMiddleware → AuthMiddleware → [QuotaMiddleware] → ActiveRequestCounter → SProxyHandler
func (sp *SProxy) Handler() http.Handler {
	core := http.HandlerFunc(sp.serveProxy)

	var afterAuth http.Handler = core
	if sp.quotaChecker != nil {
		// QuotaMiddleware 放在 AuthMiddleware 之后，此时 context 中已有 claims
		quotaMW := quota.NewMiddleware(sp.logger, sp.quotaChecker, func(r *http.Request) string {
			if claims := ClaimsFromContext(r.Context()); claims != nil {
				return claims.UserID
			}
			return ""
		})
		afterAuth = quotaMW(core)
	}

	// 活跃请求计数器：在配额检查之后、实际代理之前开始计数。
	// 计数范围包含认证、配额检查和实际代理的全部时间（代表"正在处理的请求"）。
	withCounter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sp.activeRequests.Add(1)
		defer sp.activeRequests.Add(-1)
		afterAuth.ServeHTTP(w, r)
	})

	withAuth := AuthMiddleware(sp.logger, sp.jwtMgr, withCounter)
	withReqID := RequestIDMiddleware(sp.logger, withAuth)
	return RecoveryMiddleware(sp.logger, withReqID)
}

// healthResponse /health 响应结构
type healthResponse struct {
	Status         string `json:"status"`            // "ok" | "degraded"
	Version        string `json:"version"`           // 版本字符串
	UptimeSeconds  int64  `json:"uptime_seconds"`    // 进程运行时长（秒）
	ActiveRequests int64  `json:"active_requests"`   // 当前正在处理的代理请求数
	QueueDepth     int    `json:"usage_queue_depth"` // 用量写入 channel 中的待处理记录数
	DBReachable    bool   `json:"db_reachable"`      // 数据库是否可达
}

// HealthHandler 返回 s-proxy 健康检查处理器，供 /health 注册使用。
//
// 响应示例（全部正常）：
//
//	HTTP 200 {"status":"ok","version":"v1.5.0 (abc1234) ...","uptime_seconds":3600,
//	           "active_requests":5,"usage_queue_depth":12,"db_reachable":true}
//
// 响应示例（DB 不可达）：
//
//	HTTP 503 {"status":"degraded","db_reachable":false,...}
func (sp *SProxy) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uptime := int64(time.Since(sp.startTime).Seconds())
		activeReqs := sp.activeRequests.Load()

		queueDepth := 0
		if sp.writer != nil {
			queueDepth = sp.writer.QueueDepth()
		}

		dbReachable := true
		if sp.sqlDB != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := sp.sqlDB.PingContext(ctx); err != nil {
				dbReachable = false
				sp.logger.Warn("health check: database ping failed",
					zap.Error(err),
				)
			}
		}

		status := "ok"
		httpStatus := http.StatusOK
		if !dbReachable {
			status = "degraded"
			httpStatus = http.StatusServiceUnavailable
			sp.logger.Warn("health check: reporting degraded status",
				zap.Int64("uptime_seconds", uptime),
				zap.Bool("db_reachable", dbReachable),
			)
		}

		resp := healthResponse{
			Status:         status,
			Version:        version.Short(),
			UptimeSeconds:  uptime,
			ActiveRequests: activeReqs,
			QueueDepth:     queueDepth,
			DBReachable:    dbReachable,
		}

		sp.logger.Debug("health check requested",
			zap.String("status", status),
			zap.Int64("uptime_seconds", uptime),
			zap.Int64("active_requests", activeReqs),
			zap.Int("queue_depth", queueDepth),
			zap.Bool("db_reachable", dbReachable),
		)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// pickLLMTarget 选择下一个 LLM target，支持用户/分组绑定和负载均衡。
//
// 选择优先级：
//  1. 用户/分组绑定（bindingResolver）→ 若绑定 target 健康且未尝试过
//  2. 加权随机负载均衡（llmBalancer）→ 过滤已尝试 + 不健康 + provider 不匹配
//  3. 回退简单轮询（无均衡器时）
func (sp *SProxy) pickLLMTarget(path, userID, groupID string, tried []string) (*lb.LLMTargetInfo, error) {
	triedSet := make(map[string]bool, len(tried))
	for _, u := range tried {
		triedSet[u] = true
	}

	// 1. 用户/分组绑定优先
	if sp.bindingResolver != nil {
		boundURL, found := sp.bindingResolver(userID, groupID)
		if found && !triedSet[boundURL] {
			healthy := true
			if sp.llmBalancer != nil {
				healthy = false
				for _, t := range sp.llmBalancer.Targets() {
					if t.ID == boundURL {
						healthy = t.Healthy
						break
					}
				}
			}
			if healthy {
				sp.logger.Debug("using bound LLM target",
					zap.String("user_id", userID),
					zap.String("group_id", groupID),
					zap.String("url", boundURL),
				)
				return sp.llmTargetInfoForURL(boundURL), nil
			}
			sp.logger.Warn("bound LLM target unhealthy, falling back to load balancer",
				zap.String("bound_url", boundURL),
				zap.String("user_id", userID),
			)
		}
	}

	// 2. 加权随机均衡（支持 tried 过滤 + provider 过滤）
	if sp.llmBalancer != nil {
		return sp.weightedPickExcluding(path, triedSet)
	}

	// 3. 回退：简单轮询（未配置均衡器时）
	candidates := sp.candidatesByPath(path)
	if len(candidates) == 0 {
		candidates = sp.targets
	}
	// 过滤已尝试目标
	var available []LLMTarget
	for _, t := range candidates {
		if !triedSet[t.URL] {
			available = append(available, t)
		}
	}
	if len(available) == 0 {
		return nil, lb.ErrNoHealthyTarget
	}
	n := sp.idx.Add(1)
	t := available[int(n-1)%len(available)]
	sp.logger.Debug("picked LLM target (round-robin)",
		zap.String("url", t.URL),
		zap.String("path", path),
	)
	return &lb.LLMTargetInfo{URL: t.URL, APIKey: t.APIKey}, nil
}

// weightedPickExcluding 从 llmBalancer 中选取健康 target，排除 tried，并应用 provider 过滤。
func (sp *SProxy) weightedPickExcluding(path string, tried map[string]bool) (*lb.LLMTargetInfo, error) {
	all := sp.llmBalancer.Targets()
	preferred := preferredProvidersByPath(path)

	filter := func(targets []lb.Target, providerFilter map[string]bool) []lb.Target {
		var out []lb.Target
		for _, t := range targets {
			if !t.Healthy || tried[t.ID] {
				continue
			}
			if providerFilter != nil {
				prov := sp.providerForURL(t.ID)
				if !providerFilter[prov] {
					continue
				}
			}
			out = append(out, t)
		}
		return out
	}

	candidates := filter(all, preferred)
	// 若 provider 过滤后无候选，回退到全量健康 target（保持兼容性）
	if len(candidates) == 0 && preferred != nil {
		candidates = filter(all, nil)
	}
	if len(candidates) == 0 {
		return nil, lb.ErrNoHealthyTarget
	}

	// 加权随机选取
	total := 0
	for _, c := range candidates {
		total += c.Weight
	}
	r := rand.IntN(total)
	for i := range candidates {
		r -= candidates[i].Weight
		if r < 0 {
			sp.logger.Debug("picked LLM target (weighted random)",
				zap.String("url", candidates[i].ID),
				zap.String("path", path),
				zap.Int("candidates", len(candidates)),
			)
			return sp.llmTargetInfoForURL(candidates[i].ID), nil
		}
	}
	// 理论上不会到达
	return sp.llmTargetInfoForURL(candidates[0].ID), nil
}

// llmTargetInfoForURL 根据 URL 查找对应的 LLMTargetInfo（含 APIKey）。
func (sp *SProxy) llmTargetInfoForURL(targetURL string) *lb.LLMTargetInfo {
	for _, t := range sp.targets {
		if t.URL == targetURL {
			return &lb.LLMTargetInfo{URL: t.URL, APIKey: t.APIKey}
		}
	}
	return &lb.LLMTargetInfo{URL: targetURL}
}

// providerForURL 根据 URL 查找对应 target 的 Provider。
func (sp *SProxy) providerForURL(targetURL string) string {
	for _, t := range sp.targets {
		if t.URL == targetURL {
			return t.Provider
		}
	}
	return ""
}

// candidatesByPath 按请求路径过滤匹配 provider 的 targets（legacy 路径使用）。
func (sp *SProxy) candidatesByPath(path string) []LLMTarget {
	preferred := preferredProvidersByPath(path)
	if preferred == nil {
		return nil
	}
	var out []LLMTarget
	for _, t := range sp.targets {
		if preferred[t.Provider] {
			out = append(out, t)
		}
	}
	return out
}

// preferredProvidersByPath 根据 API 路径返回期望的 provider 集合。
func preferredProvidersByPath(path string) map[string]bool {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return map[string]bool{"openai": true, "ollama": true}
	case strings.HasPrefix(path, "/v1/messages"):
		return map[string]bool{"": true, "anthropic": true}
	}
	return nil
}

// buildRetryTransport 构建 RetryTransport（当 llmBalancer 已配置时）。
func (sp *SProxy) buildRetryTransport(userID, groupID string) http.RoundTripper {
	if sp.llmBalancer == nil {
		return sp.transport
	}
	maxRetries := sp.maxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	return &lb.RetryTransport{
		Inner:      sp.transport,
		MaxRetries: maxRetries,
		PickNext: func(path string, tried []string) (*lb.LLMTargetInfo, error) {
			return sp.pickLLMTarget(path, userID, groupID, tried)
		},
		OnSuccess: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordSuccess(targetURL)
			}
		},
		OnFailure: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordFailure(targetURL)
			}
		},
		Logger: sp.logger,
	}
}

// serveProxy 核心代理逻辑：
//  1. 从 context 取 claims（已由 AuthMiddleware 验证）
//  2. 删除 X-PairProxy-Auth，注入真实 Authorization
//  3. 用 TeeResponseWriter 包装 ResponseWriter（同时转发 + 解析 token）
//  4. 反向代理到 LLM
//  5. （sp-1 模式）在响应中注入路由表更新头
func (sp *SProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		sp.logger.Error("claims missing in context", zap.String("request_id", reqID))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "claims missing")
		return
	}

	// OTel span：记录代理请求的完整生命周期
	ctx, span := otel.Tracer("pairproxy.sproxy").Start(r.Context(), "pairproxy.proxy")
	defer span.End()
	span.SetAttributes(
		attribute.String("user_id", claims.UserID),
		attribute.String("path", r.URL.Path),
	)
	r = r.WithContext(ctx)
	clientRoutingVersion := parseRoutingVersion(r.Header.Get("X-Routing-Version"))
	// 移除路由版本头，不转发给 LLM
	r.Header.Del("X-Routing-Version")

	// debug 日志：记录客户端发来的原始请求（body 可能在下面被读取）
	var debugReqBody []byte

	// F-3: 单次请求大小限制 + 并发请求限制
	// OpenAI 兼容：同时在此阶段注入 stream_options（无论是否有 quotaChecker）
	var bodyBytes []byte
	needBodyRead := sp.quotaChecker != nil || strings.HasPrefix(r.URL.Path, "/v1/chat/completions")

	if needBodyRead && r.Body != nil && r.ContentLength != 0 {
		var readErr error
		bodyBytes, readErr = io.ReadAll(r.Body)
		r.Body.Close()

		if readErr == nil {
			debugReqBody = bodyBytes

			// OpenAI 流式请求：注入 stream_options.include_usage: true
			if strings.HasPrefix(r.URL.Path, "/v1/chat/completions") {
				originalSize := len(bodyBytes)
				bodyBytes = injectOpenAIStreamOptions(r.URL.Path, bodyBytes, sp.logger, reqID)
				if len(bodyBytes) != originalSize {
					sp.logger.Debug("OpenAI streaming request detected, stream_options injected",
						zap.String("request_id", reqID),
						zap.String("path", r.URL.Path),
						zap.Int("original_size", originalSize),
						zap.Int("modified_size", len(bodyBytes)),
					)
				}
			}

			// Quota checker: max_tokens 检查
			if sp.quotaChecker != nil {
				var reqBody struct {
					MaxTokens int64 `json:"max_tokens"`
				}
				if jsonErr := json.Unmarshal(bodyBytes, &reqBody); jsonErr == nil && reqBody.MaxTokens > 0 {
					if sizeErr := sp.quotaChecker.CheckRequestSize(claims.UserID, reqBody.MaxTokens); sizeErr != nil {
						sp.logger.Warn("request rejected: request size limit",
							zap.String("request_id", reqID),
							zap.String("user_id", claims.UserID),
							zap.Int64("max_tokens", reqBody.MaxTokens),
						)
						writeJSONError(w, http.StatusTooManyRequests, "quota_exceeded", sizeErr.Error())
						return
					}
				}
			}

			// 还原 body（可能已被 injectOpenAIStreamOptions 修改）
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
		}
	}

	if sp.quotaChecker != nil {
		// 2. 并发请求限制：TryAcquire 槽，请求结束后自动 Release
		release, concErr := sp.quotaChecker.TryAcquireConcurrent(claims.UserID)
		if concErr != nil {
			sp.logger.Warn("request rejected: concurrent limit",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
			)
			writeJSONError(w, http.StatusTooManyRequests, "quota_exceeded", concErr.Error())
			return
		}
		defer release()
	}

	// 每次请求捕获一次 debug logger 快照，保证单请求内行为一致（SIGHUP 切换时不会半途改变）。
	dl := sp.debugLogger.Load()

	// debug 日志：← client request（body 未被上面读取时，在此补读）
	if dl != nil {
		if debugReqBody == nil && r.Body != nil {
			debugReqBody, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(debugReqBody))
		} else if len(bodyBytes) > 0 {
			// 使用已读取的 bodyBytes（可能已被 injectOpenAIStreamOptions 修改）
			debugReqBody = bodyBytes
		}
		dl.Debug("← client request",
			zap.String("request_id", reqID),
			zap.String("user_id", claims.UserID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			sanitizeHeaders(r.Header),
			zap.ByteString("body", truncate(debugReqBody, debugBodyMaxBytes)),
		)
	}

	firstInfo, pickErr := sp.pickLLMTarget(r.URL.Path, claims.UserID, claims.GroupID, nil)
	if pickErr != nil {
		sp.logger.Error("no LLM target available",
			zap.String("request_id", reqID),
			zap.String("user_id", claims.UserID),
			zap.Error(pickErr),
		)
		span.SetStatus(codes.Error, "no upstream available")
		writeJSONError(w, http.StatusBadGateway, "no_upstream", "no healthy LLM target available")
		return
	}
	targetURL, err := url.Parse(firstInfo.URL)
	if err != nil {
		sp.logger.Error("invalid LLM target URL",
			zap.String("request_id", reqID),
			zap.String("url", firstInfo.URL),
			zap.Error(err),
		)
		span.SetStatus(codes.Error, "invalid upstream URL")
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "invalid upstream URL")
		return
	}
	targetProvider := sp.providerForURL(firstInfo.URL)

	// 补充 span attributes（target 确定后）
	span.SetAttributes(
		attribute.String("provider", targetProvider),
		attribute.String("upstream_url", firstInfo.URL),
	)

	startTime := time.Now()

	// 预填充 UsageRecord 模板（除 token 数/状态码/时长外的字段）
	// model 优先从 X-PairProxy-Model 头取（cproxy 注入），其次从请求 body 取（OpenAI 格式客户端）
	model := extractModel(r)
	if model == "" && len(bodyBytes) > 0 {
		model = extractModelFromBody(bodyBytes)
	}
	usageRecord := db.UsageRecord{
		RequestID:   reqID,
		UserID:      claims.UserID,
		Model:       model,
		UpstreamURL: firstInfo.URL,
		SourceNode:  sp.sourceNode,
		CreatedAt:   time.Now(),
	}
	if usageRecord.Model != "" {
		span.SetAttributes(attribute.String("model", usageRecord.Model))
	}

	// 用 TeeResponseWriter 包装（streaming + non-streaming 均适用）
	// provider 决定解析器类型（Anthropic SSE / OpenAI SSE / Ollama SSE）
	var onChunk func([]byte)
	if dl != nil {
		onChunk = func(chunk []byte) {
			dl.Debug("← LLM stream chunk",
				zap.String("request_id", reqID),
				zap.ByteString("data", truncate(chunk, debugBodyMaxBytes)),
			)
		}
	}
	tw := tap.NewTeeResponseWriter(w, sp.logger, sp.writer, usageRecord, targetProvider, startTime, onChunk)

	// 构建 transport（配置均衡器时使用 RetryTransport；否则使用基础 transport）
	transport := sp.buildRetryTransport(claims.UserID, claims.GroupID)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// 删除客户端认证头（X-PairProxy-Auth 或 Authorization），注入真实 API Key
			// F-5: 优先使用 DB 中的动态 API Key，未找到则回退到配置文件中的静态 Key
			req.Header.Del("X-PairProxy-Auth")
			req.Header.Del("Authorization") // 清理客户端的 Bearer JWT，避免泄漏给上游
			apiKey := firstInfo.APIKey
			if sp.apiKeyResolver != nil {
				if k, ok := sp.apiKeyResolver(claims.UserID); ok {
					apiKey = k
					sp.logger.Debug("using dynamic api key for user",
						zap.String("user_id", claims.UserID),
					)
				}
			}
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Del("X-Forwarded-For")

			sp.logger.Debug("proxying request to LLM",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.String("target", firstInfo.URL),
				zap.String("path", req.URL.Path),
				zap.String("method", req.Method),
			)
			if dl != nil {
				dl.Debug("→ LLM request",
					zap.String("request_id", reqID),
					zap.String("method", req.Method),
					zap.String("target", firstInfo.URL+req.URL.Path),
					sanitizeHeaders(req.Header),
				)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			durationMs := time.Since(startTime).Milliseconds()
			ct := resp.Header.Get("Content-Type")
			isStreaming := strings.Contains(ct, "text/event-stream")

			// 记录延迟到 metrics 追踪器
			if tracker := metrics.GetGlobalLatencyTracker(); tracker != nil {
				tracker.ObserveProxyLatency(durationMs)
				tracker.ObserveLLMLatency(durationMs)
			}

			sp.logger.Info("LLM response received",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.Int("status", resp.StatusCode),
				zap.Bool("streaming", isStreaming),
				zap.Int64("duration_ms", durationMs),
			)

			if dl != nil {
				dl.Debug("← LLM response",
					zap.String("request_id", reqID),
					zap.Int("status", resp.StatusCode),
					zap.Bool("streaming", isStreaming),
					sanitizeHeaders(resp.Header),
				)
			}

			if !isStreaming {
				// 非 streaming：读取完整 body，解析 token，然后重新放回（ReverseProxy 需要）
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(body))

				if readErr != nil {
					sp.logger.Warn("failed to read non-streaming body",
						zap.String("request_id", reqID),
						zap.Error(readErr),
					)
				}

				if dl != nil {
					dl.Debug("← LLM response body",
						zap.String("request_id", reqID),
						zap.ByteString("body", truncate(body, debugBodyMaxBytes)),
					)
				}

				// 尝试从 body 补充 model 字段（Director 阶段请求 body 已转发，只能在此处补充）
				if usageRecord.Model == "" {
					if m := extractModelFromBody(body); m != "" {
						usageRecord.Model = m
						tw.UpdateModel(m)
						sp.logger.Debug("model extracted from response body",
							zap.String("request_id", reqID),
							zap.String("model", m),
						)
					} else {
						sp.logger.Debug("model field not found in request or response",
							zap.String("request_id", reqID),
						)
					}
				}

				// 通过 TeeWriter 记录（token 解析 + 写入 UsageWriter）
				tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
			}
			// streaming 情况：TeeResponseWriter.Write() 会自动 Feed SSE 解析器，
			// 在 message_stop 事件时异步记录；onChunk 回调已记录每条 chunk

			// sp-1 模式：向 c-proxy 注入路由表更新
			if sp.clusterMgr != nil {
				sp.clusterMgr.InjectResponseHeaders(resp.Header, clientRoutingVersion)
				if resp.Header.Get("X-Routing-Update") != "" {
					sp.logger.Debug("routing table injected into response",
						zap.String("request_id", reqID),
						zap.Int64("client_version", clientRoutingVersion),
					)
				}
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			durationMs := time.Since(startTime).Milliseconds()
			sp.logger.Error("upstream request failed",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.String("target", firstInfo.URL),
				zap.Int64("duration_ms", durationMs),
				zap.Error(err),
			)
			// 记录失败请求（token 数为 0）
			errRecord := usageRecord
			errRecord.StatusCode = http.StatusBadGateway
			errRecord.DurationMs = durationMs
			sp.writer.Record(errRecord)

			writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		},
		// FlushInterval=-1：立即刷新（SSE 流式响应必须）
		FlushInterval: -1,
		Transport:     transport,
	}

	proxy.ServeHTTP(tw, r)
}

// parseRoutingVersion 将字符串版本号解析为 int64，解析失败返回 0。
func parseRoutingVersion(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// extractModel 从请求头部或 JSON body 中提取模型名称。
// 优先级：X-PairProxy-Model 头 > 请求 body 中的 model 字段。
func extractModel(r *http.Request) string {
	if m := r.Header.Get("X-PairProxy-Model"); m != "" {
		return m
	}
	return ""
}

// extractModelFromBody 从 JSON body 中提取 model 字段（供未来扩展使用）。
// body 必须已被完整读取。
func extractModelFromBody(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Model
	}
	return ""
}
