package proxy

import (
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/keygen"
	"github.com/l17728/pairproxy/internal/quota"
)

// DirectServer 是 DirectProxyHandler 依赖的代理接口，由 *SProxy 实现。
// 解耦便于测试。
type DirectServer interface {
	ServeDirect(w http.ResponseWriter, r *http.Request)
}

// directHandlerWrapper 将 http.Handler 包装为指针类型，
// 使得 HandlerOpenAI/HandlerAnthropic 的返回值可通过指针比较验证为同一实例。
type directHandlerWrapper struct {
	inner http.Handler
}

func (w *directHandlerWrapper) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.inner.ServeHTTP(rw, r)
}

// DirectProxyHandler 处理 API Key 直连请求（无需 cproxy 客户端）。
//
// 使用前通过 NewDirectProxyHandler 构造，HandlerOpenAI / HandlerAnthropic
// 在构造时即完成中间件链的组装（问题3修复：不在每次请求时重建）。
type DirectProxyHandler struct {
	logger           *zap.Logger
	openAIHandler    *directHandlerWrapper // 预构建，复用（指针可比较）
	anthropicHandler *directHandlerWrapper // 预构建，复用（指针可比较）
}

// NewDirectProxyHandler 构造 DirectProxyHandler，同时完成中间件链预构建。
//
//   - server: *SProxy（实现 DirectServer 接口）
//   - users: ActiveUserLister（*db.UserRepo 通过适配器实现）
//   - cache: *keygen.KeyCache（可为 nil）
//   - legacySecret: 旧版共享 keygen_secret 原始字节（可为 nil）；非 nil 时作为
//     per-user 校验的兜底，保证从旧版迁移时已分发的 Key 仍可使用
//   - quotaChecker: 配额检查器（可为 nil，nil 时跳过配额检查）
func NewDirectProxyHandler(
	logger *zap.Logger,
	server DirectServer,
	users ActiveUserLister,
	cache *keygen.KeyCache,
	legacySecret []byte,
	quotaChecker *quota.Checker,
) *DirectProxyHandler {
	log := logger.Named("direct_proxy")

	// Anthropic 协议处理器（路径重写 /anthropic/* → /v1/*）
	anthropicCore := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original := r.URL.Path
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/anthropic")
		if r.URL.RawPath != "" {
			r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/anthropic")
		}
		log.Debug("anthropic path rewritten",
			zap.String("original", original),
			zap.String("rewritten", r.URL.Path),
		)
		server.ServeDirect(w, r)
	})

	// OpenAI 协议处理器（路径不变）
	openAICore := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("openai direct request",
			zap.String("path", r.URL.Path),
		)
		server.ServeDirect(w, r)
	})

	// 组装中间件链（从内到外）：core → [Quota] → KeyAuth → RequestID → Recovery
	buildChain := func(core http.Handler) http.Handler {
		inner := core
		// QuotaMiddleware 插在 KeyAuth 之后（context 中已有 claims）、core 之前
		if quotaChecker != nil {
			quotaMW := quota.NewMiddleware(log, quotaChecker, func(r *http.Request) string {
				if claims := ClaimsFromContext(r.Context()); claims != nil {
					return claims.UserID
				}
				return ""
			})
			inner = quotaMW(core)
		}
		withAuth := NewKeyAuthMiddleware(log, users, cache, inner, legacySecret)
		withReqID := RequestIDMiddleware(log, withAuth)
		return RecoveryMiddleware(log, withReqID)
	}

	return &DirectProxyHandler{
		logger:           log,
		openAIHandler:    &directHandlerWrapper{inner: buildChain(openAICore)},
		anthropicHandler: &directHandlerWrapper{inner: buildChain(anthropicCore)},
	}
}

// HandlerOpenAI 返回 OpenAI 协议直连 handler（预构建，每次返回同一实例）。
// 认证头：Authorization: Bearer sk-pp-<48chars>
func (h *DirectProxyHandler) HandlerOpenAI() *directHandlerWrapper {
	return h.openAIHandler
}

// HandlerAnthropic 返回 Anthropic 协议直连 handler（预构建，每次返回同一实例）。
// 认证头：x-api-key: sk-pp-<48chars>
// 路径：/anthropic/v1/messages → /v1/messages（自动重写）
func (h *DirectProxyHandler) HandlerAnthropic() *directHandlerWrapper {
	return h.anthropicHandler
}
