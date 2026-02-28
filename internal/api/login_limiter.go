package api

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginAttempt 记录某 IP 的登录失败状态。
type loginAttempt struct {
	failures  int
	firstFail time.Time
	lockUntil time.Time
}

// LoginLimiter 基于 IP 的登录失败速率限制器。
// 默认策略：5 次失败（在 1 分钟内）→ 锁定 5 分钟。
type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginAttempt
	maxFail int
	window  time.Duration
	lockFor time.Duration
}

// NewLoginLimiter 创建 LoginLimiter。
// maxFail: 触发锁定的最大失败次数（在 window 内）
// window:  计数窗口（默认 1 分钟）
// lockFor: 锁定时长（默认 5 分钟）
func NewLoginLimiter(maxFail int, window, lockFor time.Duration) *LoginLimiter {
	if maxFail <= 0 {
		maxFail = 5
	}
	if window <= 0 {
		window = time.Minute
	}
	if lockFor <= 0 {
		lockFor = 5 * time.Minute
	}
	return &LoginLimiter{
		entries: make(map[string]*loginAttempt),
		maxFail: maxFail,
		window:  window,
		lockFor: lockFor,
	}
}

// Check 检查某 IP 是否被锁定。
// 返回 (allowed bool, retryAfter time.Duration)。
// retryAfter 只在 !allowed 时有意义。
func (l *LoginLimiter) Check(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[ip]
	if !ok {
		return true, 0
	}
	now := time.Now()

	// 锁定期尚未结束
	if !entry.lockUntil.IsZero() && now.Before(entry.lockUntil) {
		return false, entry.lockUntil.Sub(now)
	}

	// 锁定已过期或计数窗口已超出，清除旧状态
	if !entry.lockUntil.IsZero() || now.Sub(entry.firstFail) > l.window {
		delete(l.entries, ip)
	}

	return true, 0
}

// RecordFailure 记录一次登录失败，若超过阈值则触发锁定。
func (l *LoginLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	entry, ok := l.entries[ip]
	if !ok || now.Sub(entry.firstFail) > l.window {
		// 新条目或窗口已过期，重新计数
		l.entries[ip] = &loginAttempt{
			failures:  1,
			firstFail: now,
		}
		return
	}

	entry.failures++
	if entry.failures >= l.maxFail {
		entry.lockUntil = now.Add(l.lockFor)
	}
}

// RecordSuccess 登录成功，清除该 IP 的失败记录。
func (l *LoginLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// Purge 清除所有已过期的条目（供后台定期调用）。
func (l *LoginLimiter) Purge() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for ip, entry := range l.entries {
		if !entry.lockUntil.IsZero() {
			if now.After(entry.lockUntil) {
				delete(l.entries, ip)
			}
		} else if now.Sub(entry.firstFail) > l.window {
			delete(l.entries, ip)
		}
	}
}

// realIP 从请求中提取真实客户端 IP（支持 X-Forwarded-For / X-Real-IP 透传）。
// 若均无法获取则回退到 RemoteAddr（去掉端口）。
func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For 可能包含多个 IP，第一个为原始客户端
		parts := strings.SplitN(xff, ",", 2)
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// RemoteAddr 格式为 "IP:port"
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx >= 0 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}
