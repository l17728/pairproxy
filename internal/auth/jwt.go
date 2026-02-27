package auth

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 公共错误类型，供调用方 errors.Is 使用
var (
	ErrTokenExpired   = errors.New("token expired")
	ErrInvalidToken   = errors.New("invalid token")
	ErrTokenRevoked   = errors.New("token revoked")
)

// JWTClaims JWT payload 定义
type JWTClaims struct {
	UserID   string `json:"sub"`
	Username string `json:"username"`
	GroupID  string `json:"group_id"`
	Role     string `json:"role"`     // "user" | "admin"
	JTI      string `json:"jti"`      // 唯一 ID，用于撤销
	jwt.RegisteredClaims
}

// Manager JWT 签发、验证与黑名单管理
type Manager struct {
	secret    []byte
	logger    *zap.Logger
	blacklist *Blacklist
}

// NewManager 创建 JWT Manager
// secret 为签名密钥，不得为空
func NewManager(logger *zap.Logger, secret string) (*Manager, error) {
	if secret == "" {
		return nil, errors.New("jwt secret must not be empty")
	}
	m := &Manager{
		secret:    []byte(secret),
		logger:    logger.Named("jwt"),
		blacklist: NewBlacklist(logger),
	}
	logger.Info("JWT manager initialized")
	return m, nil
}

// Sign 签发 JWT，自动生成唯一 JTI
func (m *Manager) Sign(claims JWTClaims, ttl time.Duration) (string, error) {
	now := time.Now()
	claims.JTI = uuid.New().String()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		m.logger.Error("failed to sign JWT",
			zap.String("user_id", claims.UserID),
			zap.Error(err),
		)
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	m.logger.Debug("JWT signed",
		zap.String("user_id", claims.UserID),
		zap.String("jti", claims.JTI),
		zap.Duration("ttl", ttl),
	)
	return signed, nil
}

// Parse 解析并验证 JWT，返回 claims
// 返回的 error 可通过 errors.Is 区分: ErrTokenExpired / ErrInvalidToken / ErrTokenRevoked
func (m *Manager) Parse(tokenStr string) (*JWTClaims, error) {
	var claims JWTClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})

	if err != nil {
		// 区分过期错误和其他错误
		if errors.Is(err, jwt.ErrTokenExpired) {
			m.logger.Debug("JWT expired", zap.String("err", err.Error()))
			return nil, fmt.Errorf("%w: %w", ErrTokenExpired, err)
		}
		m.logger.Debug("JWT invalid", zap.String("err", err.Error()))
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if !token.Valid {
		m.logger.Warn("JWT parsed but not valid")
		return nil, ErrInvalidToken
	}

	// 黑名单检查
	if m.blacklist.IsBlocked(claims.JTI) {
		m.logger.Info("JWT JTI is blacklisted",
			zap.String("jti", claims.JTI),
			zap.String("user_id", claims.UserID),
		)
		return nil, ErrTokenRevoked
	}

	m.logger.Debug("JWT parsed successfully",
		zap.String("user_id", claims.UserID),
		zap.String("jti", claims.JTI),
	)
	return &claims, nil
}

// Blacklist 将 JTI 加入黑名单（TTL 后自动清理）
func (m *Manager) Blacklist(jti string, expiry time.Time) {
	m.blacklist.Add(jti, expiry)
	m.logger.Info("JWT JTI blacklisted",
		zap.String("jti", jti),
		zap.Time("expiry", expiry),
	)
}

// IsBlacklisted 检查 JTI 是否在黑名单中
func (m *Manager) IsBlacklisted(jti string) bool {
	return m.blacklist.IsBlocked(jti)
}

// StartCleanup 启动后台黑名单清理（ctx 取消时停止）
func (m *Manager) StartCleanup(ctx interface{ Done() <-chan struct{} }) {
	m.blacklist.StartCleanup(ctx)
}

// ---------------------------------------------------------------------------
// Blacklist 内存黑名单
// ---------------------------------------------------------------------------

type blacklistEntry struct {
	expiry time.Time
}

// Blacklist 线程安全的 JTI 黑名单，TTL 后自动清理
type Blacklist struct {
	mu      sync.RWMutex
	entries map[string]blacklistEntry
	logger  *zap.Logger
}

// NewBlacklist 创建新的 Blacklist
func NewBlacklist(logger *zap.Logger) *Blacklist {
	return &Blacklist{
		entries: make(map[string]blacklistEntry),
		logger:  logger.Named("blacklist"),
	}
}

// Add 将 JTI 加入黑名单，到期时间为 expiry
func (b *Blacklist) Add(jti string, expiry time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[jti] = blacklistEntry{expiry: expiry}
	b.logger.Debug("JTI added to blacklist",
		zap.String("jti", jti),
		zap.Time("expiry", expiry),
	)
}

// IsBlocked 检查 JTI 是否在黑名单中（自动跳过已过期条目）
func (b *Blacklist) IsBlocked(jti string) bool {
	b.mu.RLock()
	entry, ok := b.entries[jti]
	b.mu.RUnlock()

	if !ok {
		return false
	}
	if time.Now().After(entry.expiry) {
		// 懒删除：已过期，视为不在黑名单（后台 cleanup 会清理）
		return false
	}
	return true
}

// cleanup 删除所有已过期的黑名单条目
func (b *Blacklist) cleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	removed := 0
	for jti, entry := range b.entries {
		if now.After(entry.expiry) {
			delete(b.entries, jti)
			removed++
		}
	}
	if removed > 0 {
		b.logger.Debug("blacklist cleanup completed", zap.Int("removed", removed))
	}
}

// StartCleanup 启动后台清理 goroutine，每 5 分钟运行一次
func (b *Blacklist) StartCleanup(ctx interface{ Done() <-chan struct{} }) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		b.logger.Debug("blacklist cleanup goroutine started")
		for {
			select {
			case <-ticker.C:
				b.cleanup()
			case <-ctx.Done():
				b.logger.Debug("blacklist cleanup goroutine stopped")
				return
			}
		}
	}()
}
