package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/cluster"
	"github.com/pairproxy/pairproxy/internal/lb"
)

// CProxy c-proxy 核心处理器。
type CProxy struct {
	logger     *zap.Logger
	tokenStore *auth.TokenStore
	tokenDir   string
	balancer   lb.Balancer
	transport  http.RoundTripper

	routingVersion atomic.Int64 // c-proxy 本地已知的路由表版本
	cacheDir       string       // 路由表缓存目录（空串=不持久化）
}

// NewCProxy 创建 CProxy。
// tokenDir: token 文件所在目录（通常是 ~/.pairproxy）
// balancer: 上游 s-proxy 负载均衡器
// cacheDir: 路由表持久化目录（可为空）
func NewCProxy(
	logger *zap.Logger,
	tokenStore *auth.TokenStore,
	tokenDir string,
	balancer lb.Balancer,
	cacheDir string,
) (*CProxy, error) {
	cp := &CProxy{
		logger:     logger.Named("cproxy"),
		tokenStore: tokenStore,
		tokenDir:   tokenDir,
		balancer:   balancer,
		transport:  http.DefaultTransport,
		cacheDir:   cacheDir,
	}

	// 尝试从本地缓存恢复路由表版本
	if cacheDir != "" {
		if cached, err := cluster.LoadFromDir(cacheDir); err == nil && cached != nil {
			cp.routingVersion.Store(cached.Version)
			cp.applyRoutingTable(cached)
			logger.Named("cproxy").Info("routing table restored from cache",
				zap.Int64("version", cached.Version),
				zap.Int("entries", len(cached.Entries)),
			)
		}
	}

	return cp, nil
}

// Handler 构建完整 c-proxy 处理链：
//
//	RecoveryMiddleware → RequestIDMiddleware → CProxyHandler
func (cp *CProxy) Handler() http.Handler {
	core := http.HandlerFunc(cp.serveProxy)
	withReqID := RequestIDMiddleware(cp.logger, core)
	return RecoveryMiddleware(cp.logger, withReqID)
}

// serveProxy 核心代理逻辑：
//  1. 加载并验证本地 token
//  2. 删除原始 Authorization，注入 X-PairProxy-Auth
//  3. 反向代理到 s-proxy（保留 SSE streaming）
//  4. 读取响应头中的路由更新并应用到 Balancer
func (cp *CProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	// 加载本地 token
	tf, err := cp.tokenStore.Load(cp.tokenDir)
	if err != nil {
		cp.logger.Error("failed to load token",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusInternalServerError, "token_load_error", "failed to load local token")
		return
	}
	if !cp.tokenStore.IsValid(tf) {
		cp.logger.Warn("no valid token available",
			zap.String("request_id", reqID),
		)
		writeJSONError(w, http.StatusUnauthorized, "not_authenticated",
			"no valid token found; run 'cproxy login' first")
		return
	}

	target, err := cp.balancer.Pick()
	if err != nil {
		cp.logger.Error("no healthy s-proxy available",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusBadGateway, "no_healthy_target", "no healthy s-proxy available")
		return
	}

	targetURL, err := url.Parse(target.Addr)
	if err != nil {
		cp.logger.Error("invalid s-proxy target URL",
			zap.String("request_id", reqID),
			zap.String("url", target.Addr),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "invalid s-proxy URL")
		return
	}

	localVersion := cp.routingVersion.Load()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// 删除 Claude Code 设置的假 API Key，注入用户 JWT
			req.Header.Del("Authorization")
			req.Header.Set("X-PairProxy-Auth", tf.AccessToken)

			// 告知 s-proxy 本地路由版本（s-proxy 决定是否下发更新）
			req.Header.Set("X-Routing-Version", strconv.FormatInt(localVersion, 10))

			cp.logger.Debug("proxying request to s-proxy",
				zap.String("request_id", reqID),
				zap.String("target", target.Addr),
				zap.String("path", req.URL.Path),
				zap.String("method", req.Method),
			)
		},
		ModifyResponse: func(resp *http.Response) error {
			cp.processRoutingHeaders(resp, reqID)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			cp.logger.Error("s-proxy request failed",
				zap.String("request_id", reqID),
				zap.String("target", target.Addr),
				zap.Error(err),
			)
			writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		},
		// 支持 SSE：需要支持 Flush，使用 FlushInterval=-1 实现立即 flush
		FlushInterval: -1,
		Transport:     cp.transport,
	}

	proxy.ServeHTTP(w, r)
}

// processRoutingHeaders 从响应头中读取路由更新并应用。
func (cp *CProxy) processRoutingHeaders(resp *http.Response, reqID string) {
	verStr := resp.Header.Get("X-Routing-Version")
	if verStr == "" {
		return
	}

	serverVersion, err := strconv.ParseInt(verStr, 10, 64)
	if err != nil {
		cp.logger.Warn("invalid X-Routing-Version header",
			zap.String("request_id", reqID),
			zap.String("value", verStr),
		)
		return
	}

	localVersion := cp.routingVersion.Load()
	if serverVersion <= localVersion {
		return // 无需更新
	}

	encoded := resp.Header.Get("X-Routing-Update")
	if encoded == "" {
		// 版本更新但没有路由表数据，只记录版本
		cp.routingVersion.Store(serverVersion)
		return
	}

	rt, err := cluster.DecodeRoutingTable(encoded)
	if err != nil {
		cp.logger.Warn("failed to decode routing table from header",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return
	}

	cp.logger.Info("routing table updated",
		zap.String("request_id", reqID),
		zap.Int64("old_version", localVersion),
		zap.Int64("new_version", rt.Version),
		zap.Int("entries", len(rt.Entries)),
	)

	cp.applyRoutingTable(rt)

	// 从响应头移除路由更新（不暴露给客户端）
	resp.Header.Del("X-Routing-Version")
	resp.Header.Del("X-Routing-Update")
}

// applyRoutingTable 将路由表应用到 Balancer 并持久化。
func (cp *CProxy) applyRoutingTable(rt *cluster.RoutingTable) {
	targets := make([]lb.Target, len(rt.Entries))
	for i, e := range rt.Entries {
		targets[i] = lb.Target{
			ID:      e.ID,
			Addr:    e.Addr,
			Weight:  e.Weight,
			Healthy: e.Healthy,
		}
	}
	cp.balancer.UpdateTargets(targets)
	cp.routingVersion.Store(rt.Version)

	if cp.cacheDir != "" {
		go func() {
			if err := rt.SaveToDir(cp.cacheDir); err != nil {
				cp.logger.Warn("failed to cache routing table", zap.Error(err))
			}
		}()
	}
}
