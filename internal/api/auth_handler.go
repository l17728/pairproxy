package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// AuthConfig 认证配置
type AuthConfig struct {
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// DefaultAuthConfig 默认 TTL 配置
var DefaultAuthConfig = AuthConfig{
	AccessTokenTTL:  24 * time.Hour,
	RefreshTokenTTL: 7 * 24 * time.Hour,
}

// AuthHandler 处理登录、刷新、登出 HTTP 请求
type AuthHandler struct {
	logger       *zap.Logger
	jwtMgr       *auth.Manager
	userRepo     *db.UserRepo
	tokenRepo    *db.RefreshTokenRepo
	cfg          AuthConfig
	loginLimiter *LoginLimiter // 登录失败速率限制器
	provider     auth.Provider // 可选；非 nil 时使用 Provider 认证（LDAP 等）+ JIT 自动创建用户
}

// NewAuthHandler 创建 AuthHandler
func NewAuthHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	userRepo *db.UserRepo,
	tokenRepo *db.RefreshTokenRepo,
	cfg AuthConfig,
) *AuthHandler {
	return &AuthHandler{
		logger:       logger.Named("auth_handler"),
		jwtMgr:       jwtMgr,
		userRepo:     userRepo,
		tokenRepo:    tokenRepo,
		cfg:          cfg,
		loginLimiter: NewLoginLimiter(5, time.Minute, 5*time.Minute),
	}
}

// RegisterRoutes 注册路由到 ServeMux
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", h.handleLogin)
	mux.HandleFunc("POST /auth/refresh", h.handleRefresh)
	mux.HandleFunc("POST /auth/logout", h.handleLogout)
}

// SetProvider 设置外部认证提供者（如 LDAP）。
// 设置后登录流程将使用 Provider.Authenticate 进行认证，并自动 JIT 创建用户（首次登录时）。
// 若不调用此方法，AuthHandler 使用默认的本地数据库认证。
func (h *AuthHandler) SetProvider(p auth.Provider) {
	h.provider = p
}

// ---------------------------------------------------------------------------
// Request / Response 结构
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	TokenType    string `json:"token_type"`
	Username     string `json:"username"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// ---------------------------------------------------------------------------
// POST /auth/login
// ---------------------------------------------------------------------------

func (h *AuthHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	clientIP := realIP(r)

	// 速率限制检查：IP 是否被锁定
	if allowed, retryAfter := h.loginLimiter.Check(clientIP); !allowed {
		h.logger.Warn("login blocked: too many failures",
			zap.String("remote_ip", clientIP),
			zap.Duration("retry_after", retryAfter),
		)
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
		writeJSONError(w, http.StatusTooManyRequests, "too_many_attempts",
			fmt.Sprintf("too many failed login attempts; try again in %.0f seconds", retryAfter.Seconds()))
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Debug("login: invalid request body", zap.Error(err))
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}

	h.logger.Info("login attempt", zap.String("username", req.Username), zap.String("remote_ip", clientIP))

	var user *db.User

	if h.provider != nil {
		// Provider 认证路径（LDAP 等）：验证凭据 + JIT 自动创建用户
		providerUser, authErr := h.provider.Authenticate(r.Context(), req.Username, req.Password)
		if authErr != nil {
			h.logger.Warn("login failed: provider authentication error",
				zap.String("username", req.Username),
				zap.String("remote_ip", clientIP),
				zap.Error(authErr),
			)
			h.loginLimiter.RecordFailure(clientIP)
			writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
			return
		}

		// JIT 配置：按 ExternalID 查找已有用户记录
		existing, dbErr := h.userRepo.GetByExternalID(providerUser.AuthProvider, providerUser.ExternalID)
		if dbErr != nil {
			h.logger.Error("login: JIT lookup failed",
				zap.String("username", req.Username),
				zap.Error(dbErr),
			)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "database error")
			return
		}

		if existing == nil {
			// 首次登录：自动创建用户记录（JIT provisioning）
			h.logger.Info("login: JIT provisioning new user",
				zap.String("username", req.Username),
				zap.String("auth_provider", providerUser.AuthProvider),
				zap.String("external_id", providerUser.ExternalID),
			)
			newUser := &db.User{
				Username:     req.Username,
				PasswordHash: "", // Provider 认证不使用本地密码
				AuthProvider: providerUser.AuthProvider,
				ExternalID:   providerUser.ExternalID,
				IsActive:     true,
			}
			if createErr := h.userRepo.Create(newUser); createErr != nil {
				h.logger.Error("login: JIT create user failed",
					zap.String("username", req.Username),
					zap.Error(createErr),
				)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create user")
				return
			}
			user = newUser
		} else {
			user = existing
		}
	} else {
		// 本地认证路径（默认）：查询数据库并验证 bcrypt 密码
		var dbErr error
		user, dbErr = h.userRepo.GetByUsername(req.Username)
		if dbErr != nil {
			h.logger.Error("login: DB error", zap.String("username", req.Username), zap.Error(dbErr))
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "database error")
			return
		}

		// 不区分"用户不存在"和"密码错误"，统一返回 401（防止用户枚举）
		if user == nil || !auth.VerifyPassword(h.logger, user.PasswordHash, req.Password) {
			h.logger.Warn("login failed: invalid credentials",
				zap.String("username", req.Username),
				zap.String("remote_ip", clientIP),
			)
			h.loginLimiter.RecordFailure(clientIP)
			writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
			return
		}
	}

	if !user.IsActive {
		h.logger.Warn("login failed: user inactive",
			zap.String("username", req.Username),
			zap.String("remote_ip", clientIP),
		)
		h.loginLimiter.RecordFailure(clientIP)
		writeJSONError(w, http.StatusForbidden, "account_disabled", "this account has been disabled")
		return
	}

	// 签发 access token
	groupID := ""
	if user.GroupID != nil {
		groupID = *user.GroupID
	}
	claims := auth.JWTClaims{
		UserID:   user.ID,
		Username: user.Username,
		GroupID:  groupID,
		Role:     "user",
	}
	accessToken, err := h.jwtMgr.Sign(claims, h.cfg.AccessTokenTTL)
	if err != nil {
		h.logger.Error("login: failed to sign access token", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "token generation failed")
		return
	}

	// 创建 refresh token（持久化到 DB）
	refreshJTI := uuid.New().String()
	rtRecord := &db.RefreshToken{
		JTI:       refreshJTI,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(h.cfg.RefreshTokenTTL),
	}
	if err := h.tokenRepo.Create(rtRecord); err != nil {
		h.logger.Error("login: failed to save refresh token", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "token generation failed")
		return
	}

	// 异步更新最后登录时间（非致命错误忽略）
	go func() {
		_ = h.userRepo.UpdateLastLogin(user.ID, time.Now())
	}()

	// 登录成功，清除该 IP 的失败记录
	h.loginLimiter.RecordSuccess(clientIP)

	h.logger.Info("login successful",
		zap.String("user_id", user.ID),
		zap.String("username", user.Username),
		zap.String("remote_ip", clientIP),
	)

	writeJSON(w, http.StatusOK, loginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshJTI,
		ExpiresIn:    int64(h.cfg.AccessTokenTTL.Seconds()),
		TokenType:    "Bearer",
		Username:     user.Username,
	})
}

// ---------------------------------------------------------------------------
// POST /auth/refresh
// ---------------------------------------------------------------------------

func (h *AuthHandler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	h.logger.Debug("refresh attempt", zap.String("jti", req.RefreshToken))

	rt, err := h.tokenRepo.GetByJTI(req.RefreshToken)
	if err != nil {
		h.logger.Error("refresh: DB error", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}
	if rt == nil || rt.Revoked || time.Now().After(rt.ExpiresAt) {
		h.logger.Warn("refresh: invalid or revoked token", zap.String("jti", req.RefreshToken))
		writeJSONError(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}

	// 查出用户，签发新 access token
	user, err := h.userRepo.GetByID(rt.UserID)
	if err != nil || user == nil {
		h.logger.Error("refresh: user not found", zap.String("user_id", rt.UserID))
		writeJSONError(w, http.StatusUnauthorized, "user_not_found", "associated user no longer exists")
		return
	}
	if !user.IsActive {
		writeJSONError(w, http.StatusForbidden, "account_disabled", "this account has been disabled")
		return
	}

	groupID := ""
	if user.GroupID != nil {
		groupID = *user.GroupID
	}
	newToken, err := h.jwtMgr.Sign(auth.JWTClaims{
		UserID:   user.ID,
		Username: user.Username,
		GroupID:  groupID,
		Role:     "user",
	}, h.cfg.AccessTokenTTL)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "token generation failed")
		return
	}

	h.logger.Info("token refreshed", zap.String("user_id", user.ID))

	writeJSON(w, http.StatusOK, refreshResponse{
		AccessToken: newToken,
		ExpiresIn:   int64(h.cfg.AccessTokenTTL.Seconds()),
		TokenType:   "Bearer",
	})
}

// ---------------------------------------------------------------------------
// POST /auth/logout
// ---------------------------------------------------------------------------

func (h *AuthHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 从 Authorization 头提取当前 access token 的 JTI（加入黑名单）
	accessTokenStr := extractBearerToken(r)
	if accessTokenStr != "" {
		if claims, err := h.jwtMgr.Parse(accessTokenStr); err == nil {
			h.jwtMgr.Blacklist(claims.JTI, claims.ExpiresAt.Time)
			h.logger.Info("access token blacklisted",
				zap.String("user_id", claims.UserID),
				zap.String("jti", claims.JTI),
			)
		} else if !errors.Is(err, auth.ErrTokenExpired) {
			// 过期 token 无需拦截，其他错误记录警告
			h.logger.Warn("logout: could not parse access token", zap.Error(err))
		}
	}

	// 撤销 refresh token（如果提供）
	var req logoutRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RefreshToken != "" {
		if err := h.tokenRepo.Revoke(req.RefreshToken); err != nil {
			h.logger.Error("logout: failed to revoke refresh token",
				zap.String("jti", req.RefreshToken),
				zap.Error(err),
			)
		} else {
			h.logger.Info("refresh token revoked", zap.String("jti", req.RefreshToken))
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

func extractBearerToken(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && v[:len(prefix)] == prefix {
		return v[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}
