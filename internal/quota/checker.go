package quota

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/alert"
	"github.com/pairproxy/pairproxy/internal/db"
)

// ExceededError 超出配额或频率限制时返回的错误（携带重置时间信息）。
type ExceededError struct {
	Kind    string    // "daily" | "monthly" | "rate_limit"
	Current int64     // 当前值（token 数量或请求次数）
	Limit   int64     // 上限
	ResetAt time.Time // 何时可重试
}

func (e *ExceededError) Error() string {
	return fmt.Sprintf("%s quota exceeded: used %d / %d tokens, resets at %s",
		e.Kind, e.Current, e.Limit, e.ResetAt.UTC().Format(time.RFC3339))
}

// Checker 检查用户是否超出配额或请求频率限制。
//
// 检查流程（依次）：
//  1. 每日 token 配额（缓存优先）
//  2. 每月 token 配额（缓存优先）
//  3. 每分钟请求次数（内存滑动窗口）
type Checker struct {
	logger      *zap.Logger
	userRepo    *db.UserRepo
	usageRepo   *db.UsageRepo
	cache       *QuotaCache
	rateLimiter *RateLimiter    // 可选，nil 时跳过速率限制检查
	notifier    *alert.Notifier // 可选，nil 时不发告警
}

// NewChecker 创建 Checker。
func NewChecker(
	logger *zap.Logger,
	userRepo *db.UserRepo,
	usageRepo *db.UsageRepo,
	cache *QuotaCache,
) *Checker {
	return &Checker{
		logger:      logger.Named("quota_checker"),
		userRepo:    userRepo,
		usageRepo:   usageRepo,
		cache:       cache,
		rateLimiter: NewRateLimiter(),
	}
}

// SetNotifier 设置告警通知器（可选；nil 则不发告警）
func (c *Checker) SetNotifier(n *alert.Notifier) {
	c.notifier = n
}

// Check 检查 userID 的用量是否超出配额。
// 返回 nil 表示未超限；返回 *ExceededError 表示超限。
// 其他错误为内部错误（应放行请求，避免误杀）。
func (c *Checker) Check(userID string) error {
	// 获取用户分组及配额限制
	user, err := c.userRepo.GetByID(userID)
	if err != nil {
		c.logger.Warn("quota check: failed to get user, bypassing",
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return nil // 内部错误 → 放行（fail-open）
	}
	if user == nil {
		c.logger.Warn("quota check: user not found, bypassing",
			zap.String("user_id", userID),
		)
		return nil // 未知用户 → 放行
	}

	// 无分组或分组无配额 → 无限制
	if user.GroupID == nil {
		c.logger.Debug("quota check: no group, unlimited",
			zap.String("user_id", userID),
		)
		return nil
	}

	group := user.Group
	if group.DailyTokenLimit == nil && group.MonthlyTokenLimit == nil && group.RequestsPerMinute == nil {
		c.logger.Debug("quota check: no limits set, unlimited",
			zap.String("user_id", userID),
			zap.String("group", group.Name),
		)
		return nil
	}

	// 仅当有 token 限额时才查 DB 用量
	var daily, monthly int64
	if group.DailyTokenLimit != nil || group.MonthlyTokenLimit != nil {
		var err error
		daily, monthly, err = c.getUsage(userID)
		if err != nil {
			c.logger.Warn("quota check: failed to get usage, bypassing",
				zap.String("user_id", userID),
				zap.Error(err),
			)
			return nil // fail-open
		}
		c.logger.Debug("quota check",
			zap.String("user_id", userID),
			zap.String("group", group.Name),
			zap.Int64("daily_used", daily),
			zap.Int64("monthly_used", monthly),
			zap.Any("daily_limit", group.DailyTokenLimit),
			zap.Any("monthly_limit", group.MonthlyTokenLimit),
		)
	}

	// 检查每日配额
	if group.DailyTokenLimit != nil && daily >= *group.DailyTokenLimit {
		resetAt := todayEnd()
		c.logger.Warn("daily quota exceeded",
			zap.String("user_id", userID),
			zap.Int64("used", daily),
			zap.Int64("limit", *group.DailyTokenLimit),
		)
		c.notify(alert.EventQuotaExceeded, "daily token quota exceeded", userID, map[string]string{
			"kind":  "daily",
			"used":  fmt.Sprintf("%d", daily),
			"limit": fmt.Sprintf("%d", *group.DailyTokenLimit),
		})
		return &ExceededError{
			Kind:    "daily",
			Current: daily,
			Limit:   *group.DailyTokenLimit,
			ResetAt: resetAt,
		}
	}

	// 检查每月配额
	if group.MonthlyTokenLimit != nil && monthly >= *group.MonthlyTokenLimit {
		resetAt := monthEnd()
		c.logger.Warn("monthly quota exceeded",
			zap.String("user_id", userID),
			zap.Int64("used", monthly),
			zap.Int64("limit", *group.MonthlyTokenLimit),
		)
		c.notify(alert.EventQuotaExceeded, "monthly token quota exceeded", userID, map[string]string{
			"kind":  "monthly",
			"used":  fmt.Sprintf("%d", monthly),
			"limit": fmt.Sprintf("%d", *group.MonthlyTokenLimit),
		})
		return &ExceededError{
			Kind:    "monthly",
			Current: monthly,
			Limit:   *group.MonthlyTokenLimit,
			ResetAt: resetAt,
		}
	}

	// 检查每分钟请求频率（RPM）
	if group.RequestsPerMinute != nil && c.rateLimiter != nil {
		rpm := *group.RequestsPerMinute
		if rpm > 0 {
			if allowed, count := c.rateLimiter.Allow(userID, rpm); !allowed {
				resetAt := c.rateLimiter.ResetAt(userID)
				c.logger.Warn("request rate limit exceeded",
					zap.String("user_id", userID),
					zap.Int("count", count),
					zap.Int("limit", rpm),
				)
				c.notify(alert.EventRateLimited, "request rate limit exceeded", userID, map[string]string{
					"count": fmt.Sprintf("%d", count),
					"limit": fmt.Sprintf("%d", rpm),
				})
				return &ExceededError{
					Kind:    "rate_limit",
					Current: int64(count),
					Limit:   int64(rpm),
					ResetAt: resetAt,
				}
			}
		}
	}

	return nil
}

// notify 发送告警事件（若 notifier 为 nil 则跳过）
func (c *Checker) notify(kind, message, userID string, extra map[string]string) {
	if c.notifier == nil {
		return
	}
	labels := make(map[string]string, len(extra)+1)
	labels["user_id"] = userID
	for k, v := range extra {
		labels[k] = v
	}
	c.notifier.Notify(alert.Event{
		Kind:    kind,
		Message: message,
		Labels:  labels,
	})
}

// InvalidateCache 驱逐指定用户的缓存（例如在用量记录后强制刷新）。
func (c *Checker) InvalidateCache(userID string) {
	c.cache.invalidate(userID)
}

// PurgeRateLimiter 清理速率限制器中的过期窗口（供后台定时器调用）。
func (c *Checker) PurgeRateLimiter() {
	if c.rateLimiter != nil {
		c.rateLimiter.Purge()
	}
}

// getUsage 返回用户今日和本月的 token 用量（缓存优先）。
func (c *Checker) getUsage(userID string) (daily, monthly int64, err error) {
	if entry := c.cache.get(userID); entry != nil {
		return entry.dailyUsed, entry.monthlyUsed, nil
	}

	// 缓存 miss → 查 DB
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	dailyIn, dailyOut, err := c.usageRepo.SumTokens(userID, dayStart, now)
	if err != nil {
		return 0, 0, fmt.Errorf("sum daily tokens: %w", err)
	}
	daily = dailyIn + dailyOut

	monthIn, monthOut, err := c.usageRepo.SumTokens(userID, monthStart, now)
	if err != nil {
		return 0, 0, fmt.Errorf("sum monthly tokens: %w", err)
	}
	monthly = monthIn + monthOut

	c.cache.set(userID, daily, monthly)
	return daily, monthly, nil
}

// todayEnd 返回今天 23:59:59 UTC（每日配额重置时间）。
func todayEnd() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

// monthEnd 返回本月最后一天 23:59:59 UTC（每月配额重置时间）。
func monthEnd() time.Time {
	now := time.Now().UTC()
	firstOfNextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return firstOfNextMonth
}
