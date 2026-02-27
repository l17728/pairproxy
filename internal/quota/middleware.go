package quota

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// quotaExceededResponse 429 响应体。
type quotaExceededResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	ResetAt string `json:"reset_at"` // RFC3339
}

// NewMiddleware 返回配额检查中间件。
// 该中间件必须在 AuthMiddleware 之后插入（需要 context 中有 claims）。
// userIDFromCtx 函数从 context 中提取用户 ID（由 proxy 包提供）。
func NewMiddleware(
	logger *zap.Logger,
	checker *Checker,
	userIDFromCtx func(r *http.Request) string,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := userIDFromCtx(r)
			if userID == "" {
				// 没有 user_id（未认证请求在 AuthMiddleware 已拦截，此处防御性处理）
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			if err := checker.Check(userID); err != nil {
				var qErr *ExceededError
				if errors.As(err, &qErr) {
					logger.Warn("request blocked: quota exceeded",
						zap.String("user_id", userID),
						zap.String("kind", qErr.Kind),
						zap.Int64("used", qErr.Current),
						zap.Int64("limit", qErr.Limit),
					)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-RateLimit-Limit", itoa(qErr.Limit))
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", qErr.ResetAt.UTC().Format(time.RFC3339))
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(quotaExceededResponse{
						Error: "quota_exceeded",
						Message: qErr.Error(),
						ResetAt: qErr.ResetAt.UTC().Format(time.RFC3339),
					})
					return
				}
				// 非 ExceededError（内部错误）→ 放行（fail-open）
				logger.Warn("quota check error, bypassing",
					zap.String("user_id", userID),
					zap.Error(err),
				)
			}

			logger.Debug("quota check passed",
				zap.String("user_id", userID),
				zap.Duration("duration", time.Since(start)),
			)

			next.ServeHTTP(w, r)
		})
	}
}

// itoa 将 int64 转为字符串（避免引入 strconv 依赖仅为此一处使用）。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 20)
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
