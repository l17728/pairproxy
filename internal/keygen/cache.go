package keygen

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"go.uber.org/zap"
)

// CachedUser 是 KeyCache 存储的用户信息快照，
// 包含构建 auth.JWTClaims 所需的全部字段。
type CachedUser struct {
	UserID   string
	Username string
	GroupID  *string   // 对应 db.User.GroupID（可为 nil）
	CachedAt time.Time
}

// KeyCache 是 API Key → 用户信息的 LRU 缓存，避免每次请求遍历所有用户。
type KeyCache struct {
	mu    sync.RWMutex
	inner *lru.Cache[string, *CachedUser]
	ttl   time.Duration
}

// NewKeyCache 创建 KeyCache。
//   - size: 最大缓存条目数（超出时 LRU 淘汰最久未访问的条目）
//   - ttl:  缓存有效期（0 表示永不过期）
func NewKeyCache(size int, ttl time.Duration) (*KeyCache, error) {
	inner, err := lru.New[string, *CachedUser](size)
	if err != nil {
		return nil, err
	}
	return &KeyCache{inner: inner, ttl: ttl}, nil
}

// Get 从缓存中取用户信息。未命中或已过期返回 nil。
func (c *KeyCache) Get(key string) *CachedUser {
	c.mu.RLock()
	entry, ok := c.inner.Get(key)
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	if c.ttl > 0 && time.Since(entry.CachedAt) > c.ttl {
		// 升级为写锁前重新验证，防止 TOCTOU：另一个 goroutine 可能在
		// RUnlock 之后、Lock 之前写入新值
		c.mu.Lock()
		if fresh, ok := c.inner.Peek(key); ok && time.Since(fresh.CachedAt) > c.ttl {
			c.inner.Remove(key)
			zap.L().Debug("api key cache TTL expired", zap.String("username", entry.Username))
		}
		c.mu.Unlock()
		return nil
	}
	return entry
}

// Set 将用户信息写入缓存。
func (c *KeyCache) Set(key string, user *CachedUser) {
	user.CachedAt = time.Now()
	c.mu.Lock()
	c.inner.Add(key, user)
	c.mu.Unlock()
	zap.L().Debug("api key cached",
		zap.String("username", user.Username),
		zap.String("user_id", user.UserID),
	)
}

// InvalidateUser 删除指定用户名对应的所有缓存条目（用户禁用或重新生成 Key 时调用）。
func (c *KeyCache) InvalidateUser(username string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := c.inner.Keys()
	removed := 0
	for _, k := range keys {
		if entry, ok := c.inner.Peek(k); ok && entry.Username == username {
			c.inner.Remove(k)
			removed++
		}
	}
	zap.L().Info("api key cache invalidated for user",
		zap.String("username", username),
		zap.Int("removed_entries", removed),
	)
}

// InvalidateByUserID 删除指定 userID 对应的所有缓存条目（密码重置后立即踢出旧 Key）。
func (c *KeyCache) InvalidateByUserID(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := c.inner.Keys()
	removed := 0
	for _, k := range keys {
		if entry, ok := c.inner.Peek(k); ok && entry.UserID == userID {
			c.inner.Remove(k)
			removed++
		}
	}
	zap.L().Info("api key cache invalidated by user_id",
		zap.String("user_id", userID),
		zap.Int("removed_entries", removed),
	)
}
