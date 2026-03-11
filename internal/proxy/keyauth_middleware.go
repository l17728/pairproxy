package proxy

import (
	"context"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/keygen"
)

// ActiveUserLister 是 KeyAuthMiddleware 依赖的用户查询接口。
// 由 *db.UserRepo 实现（通过 DBUserLister 适配器桥接）。
type ActiveUserLister interface {
	ListActive() ([]keygen.UserEntry, error)
}

// NewKeyAuthMiddleware 构建 API Key 认证中间件。
//
// 支持两种认证头格式（自动识别）：
//   - OpenAI 格式：Authorization: Bearer sk-pp-<48chars>
//   - Anthropic 格式：x-api-key: sk-pp-<48chars>
//
// 验证成功后将 *auth.JWTClaims 注入 context（与 AuthMiddleware 相同的 key），
// 下游 serveProxy 可无感知地复用。
//
// 中间件链：cache.Get → (miss) ListActive + ValidateAndGetUser → cache.Set → 注入 claims → next
// TODO: 当用户被禁用时（admin disable 操作），应调用 cache.InvalidateUser(username)
// 以立即失效缓存条目。当前实现在 TTL 期间内仍允许被禁用用户通过。
func NewKeyAuthMiddleware(
	logger *zap.Logger,
	users ActiveUserLister,
	cache *keygen.KeyCache,
	next http.Handler,
) http.Handler {
	log := logger.Named("key_auth")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := RequestIDFromContext(r.Context())

		// 1. 提取 API Key（支持 OpenAI 和 Anthropic 两种格式）
		token := extractDirectAPIKey(r)
		if token == "" {
			log.Warn("direct auth: missing api key",
				zap.String("request_id", reqID),
				zap.String("path", r.URL.Path),
			)
			writeJSONError(w, http.StatusUnauthorized, "missing_authorization",
				"Authorization: Bearer <sk-pp-key> or x-api-key: <sk-pp-key> required")
			return
		}

		// 2. 格式预检（前缀 + 长度 + 字符集）
		if !keygen.IsValidFormat(token) {
			log.Warn("direct auth: invalid key format",
				zap.String("request_id", reqID),
				zap.String("key_prefix", safePrefix(token, 12)),
			)
			writeJSONError(w, http.StatusUnauthorized, "invalid_key_format",
				"API key must be in format sk-pp-<48 alphanumeric chars>")
			return
		}

		// 3. 查缓存（issue 4 fix：缓存返回 *CachedUser，直接提取字段）
		var userID, username string
		var groupID *string

		if cache != nil {
			if cached := cache.Get(token); cached != nil {
				userID = cached.UserID
				username = cached.Username
				groupID = cached.GroupID
				log.Debug("direct auth: cache hit",
					zap.String("request_id", reqID),
					zap.String("username", username),
				)
			}
		}

		// 4. 缓存未命中：遍历用户验证
		if userID == "" {
			activeUsers, err := users.ListActive()
			if err != nil {
				log.Error("direct auth: failed to list active users",
					zap.String("request_id", reqID),
					zap.Error(err),
				)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "user lookup failed")
				return
			}

			matched, valErr := keygen.ValidateAndGetUser(token, activeUsers)
			if valErr != nil {
				log.Warn("direct auth: key collision",
					zap.String("request_id", reqID),
					zap.Error(valErr),
				)
				writeJSONError(w, http.StatusUnauthorized, "key_collision",
					"api key matches multiple users; contact administrator")
				return
			}
			if matched == nil {
				log.Warn("direct auth: no matching user",
					zap.String("request_id", reqID),
					zap.String("key_prefix", safePrefix(token, 12)),
				)
				writeJSONError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
				return
			}

			userID = matched.ID
			username = matched.Username
			groupID = matched.GroupID

			// 写缓存
			if cache != nil {
				cache.Set(token, &keygen.CachedUser{
					UserID:   userID,
					Username: username,
					GroupID:  groupID,
				})
			}

			log.Info("direct auth: key validated",
				zap.String("request_id", reqID),
				zap.String("username", username),
				zap.String("user_id", userID),
			)
		}

		// 5. 构建 claims（复用 ctxKeyClaims，下游 serveProxy 无感知）
		groupIDStr := ""
		if groupID != nil {
			groupIDStr = *groupID
		}
		claims := &auth.JWTClaims{
			UserID:   userID,
			Username: username,
			GroupID:  groupIDStr,
		}
		ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)

		log.Debug("direct auth: claims injected",
			zap.String("request_id", reqID),
			zap.String("user_id", userID),
			zap.String("group_id", groupIDStr),
		)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractDirectAPIKey 从请求头提取 API Key，支持两种格式：
//   - OpenAI：Authorization: Bearer <key>
//   - Anthropic：x-api-key: <key>（无 Bearer 前缀）
func extractDirectAPIKey(r *http.Request) string {
	if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
		return strings.TrimPrefix(authHdr, "Bearer ")
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

// safePrefix 安全截取字符串前 n 字符（避免越界）。
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
