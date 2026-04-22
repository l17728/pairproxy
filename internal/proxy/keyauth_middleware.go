package proxy

import (
	"context"
	"encoding/json"
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
	// IsUserActive 查询单个用户的 is_active 状态，用于缓存命中后的二次校验。
	// 返回 (false, nil) 表示用户存在但已被禁用；返回 (false, err) 表示查询失败。
	IsUserActive(userID string) (bool, error)
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
// 中间件链：cache.Get → (hit) IsUserActive 二次校验 → (miss) ListActive + ValidateAndGetUser → cache.Set → 注入 claims → next
// 缓存命中后仍调用 IsUserActive 校验，确保用户被禁用后立即拒绝，不等 TTL 自然过期。
// API Key 由用户自己的 PasswordHash 派生（HMAC-SHA256）；legacySecret 非 nil 时还会用旧版
// 共享密钥做兜底校验，保证从旧版迁移时已分发的 Key 仍可使用。
func NewKeyAuthMiddleware(
	logger *zap.Logger,
	users ActiveUserLister,
	cache *keygen.KeyCache,
	next http.Handler,
	legacySecret []byte,
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
			writeDirectAuthError(w, r, http.StatusUnauthorized, "authentication_error",
				"Authorization: Bearer <sk-pp-key> or x-api-key: <sk-pp-key> required")
			return
		}

		// 2. 格式预检（前缀 + 长度 + 字符集）
		if !keygen.IsValidFormat(token) {
			log.Warn("direct auth: invalid key format",
				zap.String("request_id", reqID),
				zap.String("key_prefix", safePrefix(token, 12)),
			)
			writeDirectAuthError(w, r, http.StatusUnauthorized, "authentication_error",
				"invalid x-api-key: must be sk-pp-<48 alphanumeric chars>")
			return
		}

		// 3. 查缓存（issue 4 fix：缓存返回 *CachedUser，直接提取字段）
		var userID, username string
		var groupID *string

		if cache != nil {
			if cached := cache.Get(token); cached != nil {
				// 二次校验：用户可能在缓存 TTL 内被管理员禁用
				active, activeErr := users.IsUserActive(cached.UserID)
				if activeErr != nil {
					log.Error("direct auth: failed to verify user active status",
						zap.String("request_id", reqID),
						zap.String("username", cached.Username),
						zap.Error(activeErr),
					)
					writeJSONError(w, http.StatusInternalServerError, "internal_error", "user verification failed")
					return
				}
				if !active {
					cache.InvalidateUser(cached.Username)
					log.Warn("direct auth: cached user is disabled, cache evicted",
						zap.String("request_id", reqID),
						zap.String("username", cached.Username),
						zap.String("user_id", cached.UserID),
					)
					writeDirectAuthError(w, r, http.StatusUnauthorized, "permission_error", "user account has been disabled")
					return
				}
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
				writeDirectAuthError(w, r, http.StatusUnauthorized, "authentication_error",
					"api key matches multiple users; contact administrator")
				return
			}
			// 新版 Key 未命中时，用旧版共享 keygenSecret 兜底（向后兼容旧版迁移场景）
			if matched == nil && len(legacySecret) >= 32 {
				matched = keygen.ValidateWithLegacySecret(token, activeUsers, legacySecret)
				if matched != nil {
					log.Info("direct auth: key validated via legacy secret (consider regenerating key)",
						zap.String("request_id", reqID),
						zap.String("username", matched.Username),
					)
				}
			}
			if matched == nil {
				log.Warn("direct auth: no matching user",
					zap.String("request_id", reqID),
					zap.String("key_prefix", safePrefix(token, 12)),
				)
				writeDirectAuthError(w, r, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
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

// writeDirectAuthError 根据请求路径写入对应协议格式的认证错误响应。
//
// 直连客户端（Claude Code、OpenAI SDK 等）只认识自身协议的错误格式；
// 如果收到 {"error":"invalid_api_key","message":"..."} 这种通用格式，
// 它们会把错误当成未知/可重试错误而不停重试，用户看不到有意义的提示。
//
// 映射规则：
//   - /v1/chat/completions → OpenAI format
//   - 其余（/v1/messages、/anthropic/*）→ Anthropic format（默认）
func writeDirectAuthError(w http.ResponseWriter, r *http.Request, httpStatus int, authErrType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if strings.Contains(r.URL.Path, "/chat/completions") {
		// OpenAI 错误格式
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": message,
				"type":    "invalid_request_error",
				"code":    authErrType,
			},
		})
	} else {
		// Anthropic 错误格式（Claude Code 期待的格式）
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    authErrType,
				"message": message,
			},
		})
	}
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
