package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
)

// contextKey is an unexported type for context keys in this package.
type contextKey int

const (
	ctxKeyRequestID contextKey = iota
	ctxKeyClaims
	ctxKeyModel // 请求体中提取的 LLM 模型名称，由 cproxy 写入，Director 读取后注入 X-PairProxy-Model
)

// RequestIDFromContext 从 context 中取 request_id。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// ClaimsFromContext 从 context 中取 JWT claims。
func ClaimsFromContext(ctx context.Context) *auth.JWTClaims {
	if v, ok := ctx.Value(ctxKeyClaims).(*auth.JWTClaims); ok {
		return v
	}
	return nil
}

// ---------------------------------------------------------------------------
// RequestIDMiddleware
// ---------------------------------------------------------------------------

// RequestIDMiddleware 为每个请求生成 UUID 写入 context 和响应头 X-Request-ID。
func RequestIDMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 优先沿用上游传入的 request-id
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, reqID)
		logger.Debug("request received",
			zap.String("request_id", reqID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------------------
// AuthMiddleware（s-proxy 用）
// ---------------------------------------------------------------------------

// AuthMiddleware 验证请求头 X-PairProxy-Auth 或 Authorization: Bearer 中的 JWT，提取 claims 写入 context。
// 优先级：X-PairProxy-Auth > Authorization: Bearer（向后兼容 cproxy）。
// 验证失败返回 401，通过后继续处理。
func AuthMiddleware(logger *zap.Logger, jwtMgr *auth.Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := RequestIDFromContext(r.Context())
		token := r.Header.Get("X-PairProxy-Auth")
		authSource := "X-PairProxy-Auth"

		// 若 X-PairProxy-Auth 缺失，尝试从 Authorization: Bearer 提取
		if token == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
				authSource = "Authorization Bearer"
			}
		}

		if token == "" {
			logger.Warn("missing authentication header",
				zap.String("request_id", reqID),
				zap.String("path", r.URL.Path),
				zap.String("method", r.Method),
				zap.String("remote_addr", r.RemoteAddr),
			)
			writeJSONError(w, http.StatusUnauthorized, "missing_auth_header", "X-PairProxy-Auth or Authorization: Bearer header is required")
			return
		}

		claims, err := jwtMgr.Parse(token)
		if err != nil {
			logger.Warn("invalid JWT",
				zap.String("request_id", reqID),
				zap.String("auth_source", authSource),
				zap.Error(err),
			)
			writeJSONError(w, http.StatusUnauthorized, "invalid_token", err.Error())
			return
		}

		logger.Debug("JWT authenticated",
			zap.String("request_id", reqID),
			zap.String("user_id", claims.UserID),
			zap.String("username", claims.Username),
			zap.String("auth_source", authSource),
		)

		ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------------------
// RecoveryMiddleware
// ---------------------------------------------------------------------------

// RecoveryMiddleware 捕获 panic，返回 500，避免进程崩溃。
// 注意：http.ErrAbortHandler 必须重新 panic，让 net/http 正常清理连接（客户端断连场景）。
func RecoveryMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// http.ErrAbortHandler 是 net/http 主动发出的终止信号（客户端断连），
				// 必须重新 panic 让 HTTP server 正确关闭连接，不能当作真正的错误处理。
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				reqID := RequestIDFromContext(r.Context())
				logger.Error("panic recovered",
					zap.String("request_id", reqID),
					zap.Any("panic", rec),
				)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "an unexpected error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// writeJSONError 写入标准 JSON 错误响应。
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: code, Message: message})
}

// writeQuotaError 写入结构化配额错误响应（包含 kind/current/limit/reset_at 字段）。
func writeQuotaError(w http.ResponseWriter, kind string, current, limit int64, resetAt time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":    "quota_exceeded",
		"kind":     kind,
		"current":  current,
		"limit":    limit,
		"reset_at": resetAt.UTC().Format(time.RFC3339),
	})
}
