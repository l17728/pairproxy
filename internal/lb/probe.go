package lb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProbeMethod 定义一种探活方式。
type ProbeMethod struct {
	// Name 人类可读名称，用于日志。
	Name string
	// Path 追加到 target.Addr 后面的路径（例如 "/health"、"/v1/models"）。
	Path string
	// HTTPMethod GET 或 POST。
	HTTPMethod string
	// Body 仅 POST 时使用，发送的 JSON 请求体。
	Body string
	// ContentType 仅 POST 时使用。
	ContentType string
	// OKStatuses 被视为"健康"的 HTTP 状态码集合。
	// nil 表示仅 200。
	OKStatuses map[int]bool
	// ProviderHint 若非空，只对匹配 provider 的 target 使用此方式。
	// 空字符串表示通用（任意 provider 均尝试）。
	ProviderHint string
}

// probeResult 单次探活结果。
type probeResult struct {
	method *ProbeMethod
	status int   // HTTP 状态码，0 表示连接失败
	err    error // 非 nil 表示网络/协议层失败
}

// ok 报告此次探活是否成功（视为"服务健康"）。
// hasCredential 为 true 时，401/403 不视为健康（key 无效）；
func (r probeResult) okWithAuth(hasCredential bool) bool {
	if r.err != nil || r.status == 0 {
		return false
	}
	if r.method.OKStatuses != nil {
		if r.method.OKStatuses[r.status] {
			// 若有认证凭证且返回 401/403，说明 key 无效——不视为健康
			if hasCredential && (r.status == http.StatusUnauthorized || r.status == http.StatusForbidden) {
				return false
			}
			return true
		}
		return false
	}
	return r.status == http.StatusOK
}

// okForDiscovery 报告此次探活结果是否确认端点存在（用于策略发现阶段）。
// 与 okWithAuth 的区别：发现阶段只需证明端点存在，401/403 同样算"发现成功"——
// 它们证明服务在线且有认证机制，而不论当前凭证是否有效。
// 常规心跳阶段仍使用 okWithAuth(true) 来检测凭证是否有效。
func (r probeResult) okForDiscovery() bool {
	if r.err != nil || r.status == 0 {
		return false
	}
	if r.method.OKStatuses != nil {
		// 发现阶段：OKStatuses 内的任何状态码（含 401/403）均视为"端点存在"
		return r.method.OKStatuses[r.status]
	}
	return r.status == http.StatusOK
}

// definitivelyUnhealthy 报告此次探活结果是否明确证明服务不可用。
// 与 ok() 相对：ok=false 不一定是服务不健康，可能只是探活方式不对。
// 只有连接超时/拒绝才算"不确定"，收到任何 HTTP 响应（含 4xx/5xx）则
// 说明服务在线，只是认证或路径问题。
func (r probeResult) definitivelyUnhealthy() bool {
	// 收到 HTTP 响应说明服务在线（即使是 4xx）
	return r.err != nil && r.status == 0
}

// isEndpointTimeout 报告错误是否为单个端点的 HTTP 超时（区别于连接拒绝）。
// HTTP 客户端超时（http.Client.Timeout）封装在 *url.Error 中，其 Timeout() 方法返回 true。
// context.DeadlineExceeded 也视为端点超时（探活的整体 context 超时由上层处理）。
// 连接拒绝（syscall.ECONNREFUSED 等）的 *url.Error.Timeout() 返回 false。
func isEndpointTimeout(err error) bool {
	if err == nil {
		return false
	}
	// context 层面的超时/取消
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// HTTP 客户端超时（*url.Error 包装）
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Timeout()
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// 内置探活策略（按优先级排列）
// ─────────────────────────────────────────────────────────────────────────────

// builtinProbeMethods 是按优先级排列的探活策略列表。
// 对于未配置 health_check_path 的 target，HealthChecker 将依次尝试这些策略，
// 找到第一个返回 ok() 的策略后缓存，后续直接复用。
var builtinProbeMethods = []*ProbeMethod{
	// 1. 标准 /health — vLLM、sglang、自建代理
	{
		Name:       "GET /health",
		Path:       "/health",
		HTTPMethod: http.MethodGet,
		OKStatuses: map[int]bool{http.StatusOK: true},
	},
	// 2. OpenAI 兼容 /v1/models — OpenAI、火山、腾讯、小米等
	{
		Name:       "GET /models",
		Path:       "/models",
		HTTPMethod: http.MethodGet,
		// 401/403 说明服务在线但认证缺失；需认证时 200；均视为"服务可达"
		OKStatuses: map[int]bool{
			http.StatusOK:           true,
			http.StatusUnauthorized: true, // 401：服务在线，key 未注入时正常
			http.StatusForbidden:    true, // 403：同上
		},
	},
	// 3. Anthropic 专用 /v1/models
	{
		Name:         "GET /v1/models (anthropic)",
		Path:         "/v1/models",
		HTTPMethod:   http.MethodGet,
		ProviderHint: "anthropic",
		OKStatuses: map[int]bool{
			http.StatusOK:           true,
			http.StatusUnauthorized: true,
			http.StatusForbidden:    true,
			http.StatusBadRequest:   true, // 华为云 400（缺 auth header）
		},
	},
	// 4. Anthropic /v1/messages 最小 POST（无副作用：参数不完整会 400，
	//    但 400 说明服务在线）
	{
		Name:         "POST /v1/messages (anthropic)",
		Path:         "/v1/messages",
		HTTPMethod:   http.MethodPost,
		Body:         `{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`,
		ContentType:  "application/json",
		ProviderHint: "anthropic",
		OKStatuses: map[int]bool{
			http.StatusOK:           true,
			http.StatusUnauthorized: true, // 401：认证失败，但服务在线
			http.StatusForbidden:    true,
			http.StatusBadRequest:   true, // 400：参数问题，但服务在线
		},
	},
	// 5. OpenAI /v1/chat/completions 最小 POST（同上，400 说明在线）
	{
		Name:       "POST /v1/chat/completions",
		Path:       "/v1/chat/completions",
		HTTPMethod: http.MethodPost,
		Body:       `{"model":"gpt-4o-mini","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`,
		ContentType: "application/json",
		OKStatuses: map[int]bool{
			http.StatusOK:           true,
			http.StatusUnauthorized: true,
			http.StatusForbidden:    true,
			http.StatusBadRequest:   true,
		},
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// ProbeCache — 已发现策略的线程安全缓存
// ─────────────────────────────────────────────────────────────────────────────

// probeEntry 缓存一条已发现的探活策略。
type probeEntry struct {
	method       *ProbeMethod
	discoveredAt time.Time
}

// ProbeCache 缓存每个 targetID 的已发现探活策略，避免每次都重新尝试。
type ProbeCache struct {
	mu      sync.Mutex
	entries map[string]*probeEntry // targetID → entry
	ttl     time.Duration          // 条目有效期（超时后重新探测）
}

// NewProbeCache 创建 ProbeCache。
// ttl 为缓存有效期；正数（建议 2h）表示条目在 ttl 后过期并触发重新探测。
// ttl == 0 表示条目永不过期（仅用于测试场景；生产代码应传正数）。
// ttl < 0 视为 0（永不过期）。
func NewProbeCache(ttl time.Duration) *ProbeCache {
	return &ProbeCache{
		entries: make(map[string]*probeEntry),
		ttl:     ttl,
	}
}

// get 返回 targetID 对应的缓存条目（nil 表示未命中或已过期）。
// 使用写锁避免 RLock→Lock 升级产生的 TOCTOU 竞态：两个 goroutine 同时
// 读到过期条目后都尝试升级为写锁，导致双重发现。
func (c *ProbeCache) get(targetID string) *probeEntry {
	c.mu.Lock()
	e := c.entries[targetID]
	if e != nil && c.ttl > 0 && time.Since(e.discoveredAt) > c.ttl {
		delete(c.entries, targetID)
		e = nil
	}
	c.mu.Unlock()
	return e
}

// set 写入一条已发现策略。
func (c *ProbeCache) set(targetID string, method *ProbeMethod) {
	c.mu.Lock()
	c.entries[targetID] = &probeEntry{
		method:       method,
		discoveredAt: time.Now(),
	}
	c.mu.Unlock()
}

// invalidate 删除指定 target 的缓存（target 配置变更时调用）。
func (c *ProbeCache) invalidate(targetID string) {
	c.mu.Lock()
	delete(c.entries, targetID)
	c.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Prober — 执行单次探活 HTTP 请求
// ─────────────────────────────────────────────────────────────────────────────

// Prober 封装探活请求的执行逻辑。
type Prober struct {
	client  *http.Client
	timeout time.Duration
	logger  *zap.Logger
}

// NewProber 创建 Prober。
func NewProber(timeout time.Duration, logger *zap.Logger) *Prober {
	return &Prober{
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
		logger:  logger,
	}
}

// probe 用指定策略对 target 发一次探活请求。
func (p *Prober) probe(ctx context.Context, targetAddr, targetID string, method *ProbeMethod, cred *TargetCredential) probeResult {
	rawURL := buildProbeURL(targetAddr, method.Path)

	var bodyReader io.Reader
	if method.Body != "" {
		bodyReader = bytes.NewBufferString(method.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method.HTTPMethod, rawURL, bodyReader)
	if err != nil {
		return probeResult{method: method, err: err}
	}
	if method.ContentType != "" {
		req.Header.Set("Content-Type", method.ContentType)
	}
	if cred != nil {
		injectCredential(req, cred)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Debug("probe: request failed",
			zap.String("target", targetID),
			zap.String("method", method.Name),
			zap.String("url", rawURL),
			zap.Error(err),
		)
		return probeResult{method: method, err: err}
	}
	defer resp.Body.Close()
	// 耗尽 body 复用连接
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	p.logger.Debug("probe: got response",
		zap.String("target", targetID),
		zap.String("method", method.Name),
		zap.String("url", rawURL),
		zap.Int("status", resp.StatusCode),
	)
	return probeResult{method: method, status: resp.StatusCode}
}

// Discover 对 target 依次尝试所有内置策略，返回第一个成功的策略。
// 若所有策略均失败（硬性连接错误：拒绝/DNS失败），返回 nil, true（不可达）。
// 若找到有效策略，返回该策略, false。
// 若服务有响应但所有路径均不合适，返回 nil, false（依赖被动熔断）。
//
// 关键语义：单个端点超时（HTTP 客户端超时）与连接拒绝不同：
// - 超时 → 该端点可能不存在，继续尝试下一个策略
// - 连接拒绝/DNS失败 → 服务整体不可达，立即停止
func (p *Prober) Discover(ctx context.Context, targetAddr, targetID, provider string, cred *TargetCredential) (found *ProbeMethod, unreachable bool) {
	methods := selectMethods(provider)

	// 跟踪是否收到过任何 HTTP 响应（含错误状态码）或软超时
	gotHTTPResponse := false
	budgetExhausted := false

	for _, m := range methods {
		// 探活预算耗尽（整体 ctx 已超时/取消）：不能继续也不能断定不可达
		if ctx.Err() != nil {
			budgetExhausted = true
			p.logger.Debug("probe: discovery budget exhausted, stopping",
				zap.String("target", targetID),
				zap.String("method", m.Name),
				zap.Error(ctx.Err()),
			)
			break
		}

		result := p.probe(ctx, targetAddr, targetID, m, cred)
		// 发现阶段：只需确认端点存在，401/403 同样算"发现成功"
		if result.okForDiscovery() {
			p.logger.Info("probe: discovered working health check method",
				zap.String("target", targetID),
				zap.String("method", m.Name),
				zap.Int("status", result.status),
			)
			return m, false
		}
		if result.definitivelyUnhealthy() {
			if isEndpointTimeout(result.err) {
				// 单个端点超时（HTTP 客户端 Timeout）：该路径可能不存在，继续尝试下一策略
				p.logger.Debug("probe: endpoint timeout, trying next method",
					zap.String("target", targetID),
					zap.String("method", m.Name),
					zap.Error(result.err),
				)
				// 不设 gotHTTPResponse，不计为"服务在线"——仅跳过该方法
				continue
			}
			// 硬性连接失败（拒绝连接/DNS 失败）：服务整体不可达，停止
			p.logger.Warn("probe: target unreachable during discovery",
				zap.String("target", targetID),
				zap.String("method", m.Name),
				zap.Error(result.err),
			)
			return nil, true
		}
		// HTTP 响应（任意状态码）：服务在线
		gotHTTPResponse = true
		p.logger.Debug("probe: method not suitable, trying next",
			zap.String("target", targetID),
			zap.String("method", m.Name),
			zap.Int("status", result.status),
		)
	}

	// 预算耗尽但已收到 HTTP 响应（部分方法成功通信但状态不匹配）→ 服务在线，无合适路径
	if budgetExhausted && gotHTTPResponse {
		p.logger.Warn("probe: discovery budget exhausted with partial HTTP responses",
			zap.String("target", targetID),
		)
		return nil, false
	}
	// 预算耗尽且无任何响应 → 不确定，不能断定不可达（区别于硬性连接拒绝）
	if budgetExhausted {
		p.logger.Warn("probe: discovery budget exhausted without any HTTP response",
			zap.String("target", targetID),
		)
		return nil, false // 保守：不标记 unreachable，下次心跳重试
	}

	// 所有方法均试过（无超时中断）
	if !gotHTTPResponse {
		// 到达此处说明每个方法均因单端点超时被跳过（isEndpointTimeout→continue）。
		// 硬性连接失败（拒绝/DNS）在循环内已经 return nil, true，不会到达这里。
		// 端点超时说明服务可能在线但响应慢，不宜断定不可达——保守返回 nil, false。
		p.logger.Warn("probe: all methods timed out without HTTP response, not marking unreachable",
			zap.String("target", targetID),
		)
		return nil, false
	}

	// 有 HTTP 响应但无合适探活路径 → 服务在线但无法探活，用 nil 表示
	p.logger.Warn("probe: no working health check method found for target",
		zap.String("target", targetID),
		zap.String("provider", provider),
	)
	return nil, false
}

// CheckWithMethod 用已知策略（来自缓存）执行一次探活。
func (p *Prober) CheckWithMethod(ctx context.Context, targetAddr, targetID string, method *ProbeMethod, cred *TargetCredential) probeResult {
	return p.probe(ctx, targetAddr, targetID, method, cred)
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// buildProbeURL 拼接 targetAddr 和 path，处理末尾斜线和路径重复问题。
//
// 当 addr 中已包含路径前缀（如 "https://host/openai/v1"）而 path 以相同段开头
// （如 "/v1/models"）时，避免拼接出 "/openai/v1/v1/models"。
// 具体处理：找到 addrPath 末尾段与 probePath 开头段的最长公共重叠，
// 将其从 probePath 前缀中去除，再追加到 addr 后面。
//
// addr 中的查询参数和片段被剥离后仅用于路径比较，不出现在结果 URL 中。
//
// 示例：
//
//	addr="https://host/openai/v1"  path="/v1/models"  → "https://host/openai/v1/models"
//	addr="https://api.openai.com"  path="/v1/models"  → "https://api.openai.com/v1/models"
//	addr="http://host/v1?key=abc"  path="/v1/models"  → "http://host/v1/models"
func buildProbeURL(addr, path string) string {
	addr = strings.TrimRight(addr, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// 用 url.Parse 提取 addr 的 path 部分，避免查询参数和 fragment 干扰段比较
	addrPath := ""
	if parsed, err := url.Parse(addr); err == nil {
		addrPath = parsed.Path
	} else {
		// fallback：手动查找 scheme://host 后的 path（剥离查询参数和 fragment）
		if idx := strings.Index(addr, "://"); idx != -1 {
			rest := addr[idx+3:]
			if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
				p := rest[slashIdx:]
				if qIdx := strings.IndexAny(p, "?#"); qIdx != -1 {
					p = p[:qIdx]
				}
				addrPath = p
			}
		}
	}

	// 剥离 addr 中的查询参数和 fragment（base 只保留 scheme://host/path）
	addrBase := addr
	if qIdx := strings.IndexAny(addrBase, "?#"); qIdx != -1 {
		addrBase = addrBase[:qIdx]
	}
	addrBase = strings.TrimRight(addrBase, "/")

	if addrPath == "" || addrPath == "/" {
		return addrBase + path
	}

	// 找 addrPath 末尾段与 probePath 开头段的最长重叠，将其从 probePath 去除。
	// 例如 addrPath="/openai/v1"，probePath="/v1/models"：
	// "v1"（addrPath 末尾 1 段）== "v1"（probePath 开头 1 段） → 去掉 probePath 中的 "/v1"
	addrSegs := strings.Split(strings.Trim(addrPath, "/"), "/")
	pathSegs := strings.Split(strings.Trim(path, "/"), "/")

	maxOverlap := 0
	for overlap := min(len(addrSegs), len(pathSegs)); overlap > 0; overlap-- {
		match := true
		for i := 0; i < overlap; i++ {
			if addrSegs[len(addrSegs)-overlap+i] != pathSegs[i] {
				match = false
				break
			}
		}
		if match {
			maxOverlap = overlap
			break
		}
	}

	if maxOverlap == 0 {
		return addrBase + path
	}

	remaining := "/" + strings.Join(pathSegs[maxOverlap:], "/")
	if remaining == "/" {
		remaining = ""
	}
	return addrBase + remaining
}

// selectMethods 根据 provider 过滤并排序探活策略。
// provider 匹配的策略优先，通用策略兜底。
func selectMethods(provider string) []*ProbeMethod {
	var providerSpecific []*ProbeMethod
	var generic []*ProbeMethod

	for _, m := range builtinProbeMethods {
		if m.ProviderHint == "" {
			generic = append(generic, m)
		} else if strings.EqualFold(m.ProviderHint, provider) {
			providerSpecific = append(providerSpecific, m)
		}
	}

	// provider 专属策略优先
	result := make([]*ProbeMethod, 0, len(providerSpecific)+len(generic))
	result = append(result, providerSpecific...)
	result = append(result, generic...)
	return result
}

// injectCredential 将认证凭证注入 HTTP 请求。
// 独立函数供 Prober 和 HealthChecker 共用，避免重复。
// 若 APIKey 含换行符（\r\n）则跳过注入，避免 HTTP header injection 或 Go 运行时 panic。
func injectCredential(req *http.Request, cred *TargetCredential) {
	if cred == nil {
		return
	}
	key := strings.TrimSpace(cred.APIKey)
	if key == "" {
		return
	}
	if strings.ContainsAny(key, "\r\n") {
		// RFC 7230：header value 不得含换行符；Go 1.6+ 在此会 panic
		return
	}
	switch strings.ToLower(cred.Provider) {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
