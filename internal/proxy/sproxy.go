package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/tap"
	"github.com/l17728/pairproxy/internal/version"
)

// LLMTarget 代表一个 LLM 后端（含 API Key 和 provider 类型）。
type LLMTarget struct {
	URL      string
	APIKey   string
	Provider string // "anthropic"（默认）| "openai" | "ollama"
}

// SProxy s-proxy 核心处理器
type SProxy struct {
	logger          *zap.Logger
	jwtMgr          *auth.Manager
	writer          *db.UsageWriter
	targets         []LLMTarget
	idx             atomic.Uint32 // 轮询计数器（LLM 目标通常只有一个，简单轮询即可）
	transport       http.RoundTripper
	clusterMgr      *cluster.Manager // 可选，nil 表示单节点模式（不注入路由头）
	sourceNode      string           // 来源节点标识（用于 usage_logs）
	quotaChecker    *quota.Checker   // 可选，nil 表示不检查配额
	startTime       time.Time        // 进程启动时间（供 /health 返回 uptime）
	activeRequests  atomic.Int64     // 当前正在处理的代理请求数
	sqlDB           *sql.DB          // 可选，用于 /health 检查 DB 可达性
	apiKeyResolver  func(userID string) (apiKey string, found bool) // 可选，动态 API Key 解析
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

// pickTarget 按请求路径选出匹配 provider 的目标，再轮询选择。
// 路由规则：
//   - /v1/messages           → Anthropic（provider="" 或 "anthropic"）
//   - /v1/chat/completions   → OpenAI / Ollama（provider="openai" 或 "ollama"）
//   - 其他路径              → 不过滤，全部候选
//
// 如果过滤后候选为空（未配置对应 provider 的目标），回退到全量轮询。
func (sp *SProxy) pickTarget(r *http.Request) LLMTarget {
	path := r.URL.Path

	// 按路径推断期望的 provider 类型
	var preferredProviders map[string]bool
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		preferredProviders = map[string]bool{"openai": true, "ollama": true}
	case strings.HasPrefix(path, "/v1/messages"):
		preferredProviders = map[string]bool{"": true, "anthropic": true}
	}

	// 过滤候选目标
	var candidates []LLMTarget
	if preferredProviders != nil {
		for _, t := range sp.targets {
			if preferredProviders[t.Provider] {
				candidates = append(candidates, t)
			}
		}
	}
	// 无过滤结果时回退到全量（向后兼容：所有目标均无 provider 字段时也能正常工作）
	if len(candidates) == 0 {
		candidates = sp.targets
	}

	n := sp.idx.Add(1)
	t := candidates[int(n-1)%len(candidates)]
	sp.logger.Debug("picked LLM target",
		zap.String("url", t.URL),
		zap.String("provider", t.Provider),
		zap.String("path", path),
		zap.Int("candidates", len(candidates)),
		zap.Int("total_targets", len(sp.targets)),
	)
	return t
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

	// F-3: 单次请求大小限制 + 并发请求限制
	if sp.quotaChecker != nil {
		// 1. 单次请求 max_tokens 检查：读取请求 body 中的 max_tokens 字段，还原 body
		if r.Body != nil && r.ContentLength != 0 {
			bodyBytes, readErr := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
			if readErr == nil {
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
		}

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

	target := sp.pickTarget(r)
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		sp.logger.Error("invalid LLM target URL",
			zap.String("request_id", reqID),
			zap.String("url", target.URL),
			zap.Error(err),
		)
		span.SetStatus(codes.Error, "invalid upstream URL")
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "invalid upstream URL")
		return
	}

	// 补充 span attributes（target 确定后）
	span.SetAttributes(
		attribute.String("provider", target.Provider),
		attribute.String("upstream_url", target.URL),
	)

	startTime := time.Now()

	// 预填充 UsageRecord 模板（除 token 数/状态码/时长外的字段）
	usageRecord := db.UsageRecord{
		RequestID:   reqID,
		UserID:      claims.UserID,
		Model:       extractModel(r),
		UpstreamURL: target.URL,
		SourceNode:  sp.sourceNode,
		CreatedAt:   time.Now(),
	}
	if usageRecord.Model != "" {
		span.SetAttributes(attribute.String("model", usageRecord.Model))
	}

	// 用 TeeResponseWriter 包装（streaming + non-streaming 均适用）
	// provider 决定解析器类型（Anthropic SSE / OpenAI SSE / Ollama SSE）
	tw := tap.NewTeeResponseWriter(w, sp.logger, sp.writer, usageRecord, target.Provider)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// 删除 c-proxy 注入的认证头，注入真实 API Key
			// F-5: 优先使用 DB 中的动态 API Key，未找到则回退到配置文件中的静态 Key
			req.Header.Del("X-PairProxy-Auth")
			apiKey := target.APIKey
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
				zap.String("target", target.URL),
				zap.String("path", req.URL.Path),
				zap.String("method", req.Method),
			)
		},
		ModifyResponse: func(resp *http.Response) error {
			durationMs := time.Since(startTime).Milliseconds()
			ct := resp.Header.Get("Content-Type")
			isStreaming := strings.Contains(ct, "text/event-stream")

			sp.logger.Info("LLM response received",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.Int("status", resp.StatusCode),
				zap.Bool("streaming", isStreaming),
				zap.Int64("duration_ms", durationMs),
			)

			if !isStreaming {
				// 非 streaming：读取完整 body，解析 token，然后重新放回（ReverseProxy 需要）
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(strings.NewReader(string(body)))

				if readErr != nil {
					sp.logger.Warn("failed to read non-streaming body",
						zap.String("request_id", reqID),
						zap.Error(readErr),
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
			// 在 message_stop 事件时异步记录

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
				zap.String("target", target.URL),
				zap.Int64("duration_ms", durationMs),
				zap.Error(err),
			)
			// 记录失败请求（token 数为 0）
			errRecord := usageRecord
			errRecord.StatusCode = http.StatusBadGateway
			errRecord.DurationMs = durationMs
			sp.writer.Record(errRecord)

			writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		},
		// FlushInterval=-1：立即刷新（SSE 流式响应必须）
		FlushInterval: -1,
		Transport:     sp.transport,
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
