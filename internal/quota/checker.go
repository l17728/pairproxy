package quota

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/db"
)

// ExceededError 超出配额或频率限制时返回的错误（携带重置时间信息）。
type ExceededError struct {
	Kind    string    // "daily" | "monthly" | "rate_limit" | "request_size" | "concurrent"
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
	logger             *zap.Logger
	userRepo           *db.UserRepo
	usageRepo          *db.UsageRepo
	cache              *QuotaCache
	rateLimiter        *RateLimiter        // 可选，nil 时跳过速率限制检查
	notifier           *alert.Notifier     // 可选，nil 时不发告警
	concurrentCounter  *ConcurrentCounter  // 并发请求计数器
}

// NewChecker 创建 Checker。
func NewChecker(
	logger *zap.Logger,
	userRepo *db.UserRepo,
	usageRepo *db.UsageRepo,
	cache *QuotaCache,
) *Checker {
	return &Checker{
		logger:            logger.Named("quota_checker"),
		userRepo:          userRepo,
		usageRepo:         usageRepo,
		cache:             cache,
		rateLimiter:       NewRateLimiter(),
		concurrentCounter: NewConcurrentCounter(),
	}
}

// SetNotifier 设置告警通知器（可选；nil 则不发告警）
func (c *Checker) SetNotifier(n *alert.Notifier) {
	c.notifier = n
}

// Check 检查 userID 的用量是否超出配额。
// 返回 nil 表示未超限；返回 *ExceededError 表示超限。
// 其他错误为内部错误（应放行请求，避免误杀）。
func (c *Checker) Check(ctx context.Context, userID string) error {
	_, span := otel.Tracer("pairproxy.quota").Start(ctx, "pairproxy.quota.check",
		// attribute.String("user_id", userID) 放在结果后设置
	)
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID))

	// 获取用户分组及配额限制
	user, err := c.userRepo.GetByID(userID)
	if err != nil {
		c.logger.Warn("quota check: failed to get user, bypassing",
			zap.String("user_id", userID),
			zap.Error(err),
		)
		span.SetAttributes(attribute.String("result", "bypass_error"))
		return nil // 内部错误 → 放行（fail-open）
	}
	if user == nil {
		c.logger.Warn("quota check: user not found, bypassing",
			zap.String("user_id", userID),
		)
		span.SetAttributes(attribute.String("result", "bypass_not_found"))
		return nil // 未知用户 → 放行
	}

	// 无分组或分组无配额 → 无限制
	if user.GroupID == nil {
		c.logger.Debug("quota check: no group, unlimited",
			zap.String("user_id", userID),
		)
		span.SetAttributes(attribute.String("result", "unlimited"))
		return nil
	}

	group := user.Group
	if group.DailyTokenLimit == nil && group.MonthlyTokenLimit == nil && group.RequestsPerMinute == nil {
		c.logger.Debug("quota check: no limits set, unlimited",
			zap.String("user_id", userID),
			zap.String("group", group.Name),
		)
		span.SetAttributes(attribute.String("result", "unlimited"))
		return nil
	}

	// 1. 优先检查 RPM（不依赖 DB，不受 DB 错误影响）
	// RPM 必须先于 token 配额检查，否则当 DB 故障 fail-open 时 RPM 也会被跳过。
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
				span.SetAttributes(attribute.String("result", "exceeded"), attribute.String("kind", "rate_limit"))
				return &ExceededError{
					Kind:    "rate_limit",
					Current: int64(count),
					Limit:   int64(rpm),
					ResetAt: resetAt,
				}
			}
		}
	}

	// 2. 检查 token 配额（需要查 DB，DB 错误时 fail-open）
	var daily, monthly int64
	if group.DailyTokenLimit != nil || group.MonthlyTokenLimit != nil {
		var err error
		daily, monthly, err = c.getUsage(userID)
		if err != nil {
			c.logger.Warn("quota check: failed to get usage, bypassing token quota",
				zap.String("user_id", userID),
				zap.Error(err),
			)
			span.SetAttributes(attribute.String("result", "bypass_error"))
			return nil // fail-open：DB 故障不阻断请求，但 RPM 已在上面检查
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
		span.SetAttributes(attribute.String("result", "exceeded"), attribute.String("kind", "daily"))
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
		span.SetAttributes(attribute.String("result", "exceeded"), attribute.String("kind", "monthly"))
		return &ExceededError{
			Kind:    "monthly",
			Current: monthly,
			Limit:   *group.MonthlyTokenLimit,
			ResetAt: resetAt,
		}
	}

	span.SetAttributes(attribute.String("result", "ok"))
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

// CheckRequestSize 检查请求中声明的 max_tokens 是否超出分组限制。
// requestedMaxTokens 为请求体中的 max_tokens 字段值；为 0 表示未指定，跳过检查。
// 返回 *ExceededError (Kind="request_size") 或 nil。
func (c *Checker) CheckRequestSize(userID string, requestedMaxTokens int64) error {
	if requestedMaxTokens <= 0 {
		return nil // 请求未指定 max_tokens，无需检查
	}
	user, err := c.userRepo.GetByID(userID)
	if err != nil || user == nil || user.GroupID == nil {
		return nil // fail-open
	}
	limit := user.Group.MaxTokensPerRequest
	if limit == nil || *limit <= 0 {
		return nil // 无限制
	}
	if requestedMaxTokens > *limit {
		c.logger.Warn("request size limit exceeded",
			zap.String("user_id", userID),
			zap.Int64("requested_max_tokens", requestedMaxTokens),
			zap.Int64("limit", *limit),
		)
		return &ExceededError{
			Kind:    "request_size",
			Current: requestedMaxTokens,
			Limit:   *limit,
			ResetAt: time.Time{}, // 请求大小超限无需等待，直接提示缩小
		}
	}
	return nil
}

// TryAcquireConcurrent 尝试为 userID 获取一个并发请求槽。
// 返回 release 函数（请求结束后必须调用）和 error（nil 表示成功获取）。
// 若无并发限制或用户无分组，返回 noop release 和 nil。
func (c *Checker) TryAcquireConcurrent(userID string) (release func(), err error) {
	noop := func() {}
	user, err := c.userRepo.GetByID(userID)
	if err != nil || user == nil || user.GroupID == nil {
		return noop, nil // fail-open
	}
	limit := user.Group.ConcurrentRequests
	if limit == nil || *limit <= 0 {
		return noop, nil // 无限制
	}
	if !c.concurrentCounter.TryAcquire(userID, *limit) {
		c.logger.Warn("concurrent request limit exceeded",
			zap.String("user_id", userID),
			zap.Int("current", c.concurrentCounter.Count(userID)),
			zap.Int("limit", *limit),
		)
		return noop, &ExceededError{
			Kind:    "concurrent",
			Current: int64(c.concurrentCounter.Count(userID)),
			Limit:   int64(*limit),
			ResetAt: time.Now().Add(30 * time.Second), // 建议 30s 后重试
		}
	}
	return func() { c.concurrentCounter.Release(userID) }, nil
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
// 注意：所有时间边界必须使用 UTC，与数据库存储格式保持一致。
// 使用本地时区会导致日期边界偏移（例如 UTC+8 服务器上每日统计窗口错误 8 小时）。
func (c *Checker) getUsage(userID string) (daily, monthly int64, err error) {
	if entry := c.cache.get(userID); entry != nil {
		c.logger.Debug("quota cache hit",
			zap.String("user_id", userID),
			zap.Int64("daily_used", entry.dailyUsed),
			zap.Int64("monthly_used", entry.monthlyUsed),
		)
		return entry.dailyUsed, entry.monthlyUsed, nil
	}
	c.logger.Debug("quota cache miss, querying DB", zap.String("user_id", userID))
	// 必须使用 UTC：DB 中 created_at 存储为 UTC，本地时区会导致 dayStart/monthStart 与 DB 时区不一致
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

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
