package proxy

import (
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

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/cluster"
	"github.com/pairproxy/pairproxy/internal/db"
	"github.com/pairproxy/pairproxy/internal/quota"
	"github.com/pairproxy/pairproxy/internal/tap"
)

// LLMTarget 代表一个 LLM 后端（含 API Key）。
type LLMTarget struct {
	URL    string
	APIKey string
}

// SProxy s-proxy 核心处理器
type SProxy struct {
	logger       *zap.Logger
	jwtMgr       *auth.Manager
	writer       *db.UsageWriter
	targets      []LLMTarget
	idx          atomic.Uint32 // 轮询计数器（LLM 目标通常只有一个，简单轮询即可）
	transport    http.RoundTripper
	clusterMgr   *cluster.Manager  // 可选，nil 表示单节点模式（不注入路由头）
	sourceNode   string            // 来源节点标识（用于 usage_logs）
	quotaChecker *quota.Checker    // 可选，nil 表示不检查配额
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
	}
	return sp, nil
}

// SetQuotaChecker 设置配额检查器（可选；设置后每次请求前检查配额）。
func (sp *SProxy) SetQuotaChecker(checker *quota.Checker) {
	sp.quotaChecker = checker
}

// Handler 构建并返回完整的 s-proxy HTTP 处理链：
//
//	RecoveryMiddleware → RequestIDMiddleware → AuthMiddleware → [QuotaMiddleware] → SProxyHandler
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

	withAuth := AuthMiddleware(sp.logger, sp.jwtMgr, afterAuth)
	withReqID := RequestIDMiddleware(sp.logger, withAuth)
	return RecoveryMiddleware(sp.logger, withReqID)
}

// HealthHandler 返回 s-proxy 健康检查处理器，供 /health 注册使用。
func (sp *SProxy) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":"sproxy"}`)
	}
}

// pickTarget 按轮询选择 LLM 目标。
func (sp *SProxy) pickTarget() LLMTarget {
	n := sp.idx.Add(1)
	return sp.targets[int(n-1)%len(sp.targets)]
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

	// 读取 c-proxy 发来的本地路由版本（用于决定是否下发路由更新）
	clientRoutingVersion := parseRoutingVersion(r.Header.Get("X-Routing-Version"))
	// 移除路由版本头，不转发给 LLM
	r.Header.Del("X-Routing-Version")

	target := sp.pickTarget()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		sp.logger.Error("invalid LLM target URL",
			zap.String("request_id", reqID),
			zap.String("url", target.URL),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "invalid upstream URL")
		return
	}

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

	// 用 TeeResponseWriter 包装（streaming + non-streaming 均适用）
	tw := tap.NewTeeResponseWriter(w, sp.logger, sp.writer, usageRecord)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// 删除 c-proxy 注入的认证头，注入真实 API Key
			req.Header.Del("X-PairProxy-Auth")
			req.Header.Set("Authorization", "Bearer "+target.APIKey)
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

				// 通过 TeeWriter 记录（token 解析 + 写入 UsageWriter）
				tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
			}
			// streaming 情况：TeeResponseWriter.Write() 会自动 Feed SSE 解析器，
			// 在 message_stop 事件时异步记录

			// sp-1 模式：向 c-proxy 注入路由表更新
			if sp.clusterMgr != nil {
				sp.clusterMgr.InjectResponseHeaders(resp.Header, clientRoutingVersion)
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
