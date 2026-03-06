package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/lb"
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

	refreshMu sync.Mutex // 防止并发刷新（P2-4）

	debugLogger atomic.Pointer[zap.Logger] // 可选，非 nil 时将转发内容写入独立 debug 文件
}

// SetDebugLogger 设置 debug 文件日志器。
// 非 nil 时，每个请求的转发内容（请求体、响应体、streaming chunks）均会写入该 logger。
func (cp *CProxy) SetDebugLogger(l *zap.Logger) {
	cp.debugLogger.Store(l)
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
		transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second, // TCP 握手超时
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second, // sproxy 首包超时（防悬挂）
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ForceAttemptHTTP2:     false, // s-proxy 不需要 HTTP/2
		},
		cacheDir:   cacheDir,
	}

	// 尝试从本地缓存恢复路由表版本
	if cacheDir != "" {
		if cached, err := cluster.LoadFromDir(cacheDir); err == nil && cached != nil {
			cp.routingVersion.Store(cached.Version)
			if len(cached.Entries) > 0 {
				// 仅在缓存有实际条目时才覆盖 balancer，避免空缓存抹除配置初始目标
				cp.applyRoutingTable(cached)
			}
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
		if cp.tokenStore.NeedsRefresh(tf) {
			// Token is within the refresh window or just expired — auto-refresh with 5s timeout.
			cp.logger.Info("token near expiry, attempting auto-refresh",
				zap.String("request_id", reqID),
				zap.String("username", tf.Username),
			)
			newTF, err := cp.tryRefresh(r.Context(), tf)
			if err != nil {
				cp.logger.Warn("token auto-refresh failed",
					zap.String("request_id", reqID),
					zap.Error(err),
				)
				writeJSONError(w, http.StatusUnauthorized, "not_authenticated",
					"token expired and auto-refresh failed; run 'cproxy login' again")
				return
			}
			tf = newTF
		} else {
			cp.logger.Warn("no valid token available",
				zap.String("request_id", reqID),
			)
			writeJSONError(w, http.StatusUnauthorized, "not_authenticated",
				"no valid token found; run 'cproxy login' first")
			return
		}
	}

	// 每次请求捕获一次 debug logger 快照，保证单请求内行为一致（SIGHUP 切换时不会半途改变）。
	dl := cp.debugLogger.Load()

	// debug 日志：← client request（Claude Code 发来的原始请求）
	if dl != nil && r.Body != nil {
		debugBody, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(debugBody))
		dl.Debug("← client request",
			zap.String("request_id", reqID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			sanitizeHeaders(r.Header),
			zap.ByteString("body", truncate(debugBody, debugBodyMaxBytes)),
		)
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
			if dl != nil {
				dl.Debug("→ s-proxy request",
					zap.String("request_id", reqID),
					zap.String("method", req.Method),
					zap.String("target", target.Addr+req.URL.Path),
					sanitizeHeaders(req.Header),
				)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			cp.processRoutingHeaders(resp, reqID)
			if dl != nil {
				dl.Debug("← s-proxy response",
					zap.String("request_id", reqID),
					zap.Int("status", resp.StatusCode),
					sanitizeHeaders(resp.Header),
				)
			}
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

	// 包装 ResponseWriter：streaming 响应时逐 chunk 记录到 debug 日志
	rw := http.ResponseWriter(w)
	if dl != nil {
		rw = &debugResponseWriter{ResponseWriter: w, logger: dl, reqID: reqID}
	}
	proxy.ServeHTTP(rw, r)
}

// tryRefresh 使用 refresh_token 向 s-proxy 换取新的 access_token。
// 使用互斥锁防止并发刷新（仅一个 goroutine 实际发出请求，其余等待后复用结果）。
// HTTP 请求使用 5s context 超时（P2-4）。
func (cp *CProxy) tryRefresh(ctx context.Context, tf *auth.TokenFile) (*auth.TokenFile, error) {
	cp.refreshMu.Lock()
	defer cp.refreshMu.Unlock()

	// 获取锁后重新加载：其他 goroutine 可能已完成刷新。
	if current, err := cp.tokenStore.Load(cp.tokenDir); err == nil && cp.tokenStore.IsValid(current) {
		cp.logger.Debug("token already refreshed by another goroutine")
		return current, nil
	}

	if tf.ServerAddr == "" {
		return nil, fmt.Errorf("token has no server_addr; cannot refresh")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	refreshURL := strings.TrimRight(tf.ServerAddr, "/") + "/auth/refresh"
	body, _ := json.Marshal(map[string]string{"refresh_token": tf.RefreshToken})
	req, err := http.NewRequestWithContext(refreshCtx, http.MethodPost, refreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh: upstream returned %d", resp.StatusCode)
	}

	var refreshResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if refreshResp.AccessToken == "" {
		return nil, fmt.Errorf("refresh response missing access_token")
	}

	newTF := &auth.TokenFile{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: refreshResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(refreshResp.ExpiresIn) * time.Second),
		ServerAddr:   tf.ServerAddr,
		Username:     tf.Username,
	}

	if err := cp.tokenStore.Save(cp.tokenDir, newTF); err != nil {
		// 非致命：token 已更新，即使持久化失败本次请求仍可进行
		cp.logger.Warn("failed to persist refreshed token", zap.Error(err))
	}

	cp.logger.Info("token auto-refreshed",
		zap.String("username", newTF.Username),
		zap.Time("new_expires_at", newTF.ExpiresAt),
	)
	return newTF, nil
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
			ID:       e.ID,
			Addr:     e.Addr,
			Weight:   e.Weight,
			Healthy:  e.Healthy,
			Draining: e.Draining,
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

// Balancer returns the load balancer for testing purposes
func (cp *CProxy) Balancer() lb.Balancer {
	return cp.balancer
}

// ApplyRoutingTable applies a routing table for testing purposes
func (cp *CProxy) ApplyRoutingTable(rt *cluster.RoutingTable) {
	cp.applyRoutingTable(rt)
}
