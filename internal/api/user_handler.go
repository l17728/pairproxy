package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// userCtxKey context key 类型（避免与其他包冲突）
type userCtxKeyType int

const userCtxKeyClaims userCtxKeyType = iota

// UserHandler 用户自助服务 API（普通用户可访问）
type UserHandler struct {
	logger    *zap.Logger
	jwtMgr    *auth.Manager
	userRepo  *db.UserRepo
	groupRepo *db.GroupRepo
	usageRepo *db.UsageRepo
}

// NewUserHandler 创建 UserHandler
func NewUserHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	usageRepo *db.UsageRepo,
) *UserHandler {
	return &UserHandler{
		logger:    logger.Named("user_handler"),
		jwtMgr:    jwtMgr,
		userRepo:  userRepo,
		groupRepo: groupRepo,
		usageRepo: usageRepo,
	}
}

// RegisterRoutes 注册路由
func (h *UserHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/user/quota-status", h.requireUser(http.HandlerFunc(h.handleQuotaStatus)))
	mux.Handle("GET /api/user/usage-history", h.requireUser(http.HandlerFunc(h.handleUsageHistory)))
}

// requireUser 中间件：验证 Bearer token 中的用户 JWT（普通用户即可，无需 admin 角色）
func (h *UserHandler) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Authorization: Bearer <token> required")
			return
		}

		claims, err := h.jwtMgr.Parse(token)
		if err != nil {
			h.logger.Warn("user auth failed",
				zap.String("path", r.URL.Path),
				zap.Error(err),
			)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), userCtxKeyClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// claimsFromCtx 从 context 中取用户 JWT claims
func claimsFromCtx(ctx context.Context) *auth.JWTClaims {
	if v, ok := ctx.Value(userCtxKeyClaims).(*auth.JWTClaims); ok {
		return v
	}
	return nil
}

// ---------------------------------------------------------------------------
// 配额状态查询
// ---------------------------------------------------------------------------

// userQuotaResponse 用户配额状态响应
type userQuotaResponse struct {
	DailyLimit   int64 `json:"daily_limit"`   // 0 = 不限
	DailyUsed    int64 `json:"daily_used"`
	MonthlyLimit int64 `json:"monthly_limit"` // 0 = 不限
	MonthlyUsed  int64 `json:"monthly_used"`
	RPMLimit     int   `json:"rpm_limit"` // 0 = 不限
}

func (h *UserHandler) handleQuotaStatus(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromCtx(r.Context())
	if claims == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing claims")
		return
	}

	user, err := h.userRepo.GetByID(claims.UserID)
	if err != nil {
		h.logger.Error("failed to get user", zap.String("user_id", claims.UserID), zap.Error(err))
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	resp := userQuotaResponse{}

	// 查询今日用量
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayEnd := todayStart.Add(24 * time.Hour)
	dailyInput, dailyOutput, _ := h.usageRepo.SumTokens(claims.UserID, todayStart, todayEnd)
	resp.DailyUsed = dailyInput + dailyOutput

	// 查询本月用量
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	monthEnd := monthStart.AddDate(0, 1, 0)
	monthlyInput, monthlyOutput, _ := h.usageRepo.SumTokens(claims.UserID, monthStart, monthEnd)
	resp.MonthlyUsed = monthlyInput + monthlyOutput

	// 查询配额限制（从 group）
	if user.GroupID != nil && *user.GroupID != "" {
		if group, err := h.groupRepo.GetByID(*user.GroupID); err == nil {
			if group.DailyTokenLimit != nil {
				resp.DailyLimit = *group.DailyTokenLimit
			}
			if group.MonthlyTokenLimit != nil {
				resp.MonthlyLimit = *group.MonthlyTokenLimit
			}
			if group.RequestsPerMinute != nil {
				resp.RPMLimit = *group.RequestsPerMinute
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ---------------------------------------------------------------------------
// 用量历史查询
// ---------------------------------------------------------------------------

type usageHistoryResponse struct {
	History []db.DailyTokenRow `json:"history"`
}

func (h *UserHandler) handleUsageHistory(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromCtx(r.Context())
	if claims == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing claims")
		return
	}

	// 解析 days 参数（默认 30 天）
	days := 30
	if daysStr := r.URL.Query().Get("days"); daysStr != "" {
		if parsed, err := strconv.Atoi(daysStr); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	now := time.Now()
	from := now.AddDate(0, 0, -days).Truncate(24 * time.Hour)

	history, err := h.usageRepo.DailyTokens(from, now, claims.UserID)
	if err != nil {
		h.logger.Error("failed to get user usage history",
			zap.String("user_id", claims.UserID),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to query usage")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usageHistoryResponse{History: history})
}
