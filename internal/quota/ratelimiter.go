package quota

import (
	"sync"
	"time"
)

// WindowLimit defines a sliding-window rate limit: at most Limit requests per Window.
type WindowLimit struct {
	Window time.Duration
	Limit  int
}

// RateLimiter is a multi-window sliding-window rate limiter.
// Thread-safe; state is in-memory only (resets on process restart).
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time // userID → recorded request timestamps (chronological)
}

// NewRateLimiter creates a RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{windows: make(map[string][]time.Time)}
}

// AllowWindows checks whether a new request is permitted under all given window limits.
// Limits are checked in the order provided; the first exceeded limit is returned.
// If all limits pass, the current timestamp is recorded and (true, ...) is returned.
// If a limit is exceeded, returns (false, exceeded limit, current count in that window, reset time).
func (r *RateLimiter) AllowWindows(userID string, limits []WindowLimit) (allowed bool, exceeded WindowLimit, current int, resetAt time.Time) {
	if len(limits) == 0 {
		return true, WindowLimit{}, 0, time.Time{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// Determine the longest window so we can prune older timestamps.
	maxWindow := limits[0].Window
	for _, l := range limits[1:] {
		if l.Window > maxWindow {
			maxWindow = l.Window
		}
	}

	// Prune timestamps older than the longest window (in-place, reuse backing array).
	cutoff := now.Add(-maxWindow)
	times := r.windows[userID]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	// Check each window limit. Timestamps in valid are in chronological order.
	for _, lim := range limits {
		wCutoff := now.Add(-lim.Window)
		count := 0
		var firstInWindow time.Time
		for _, t := range valid {
			if t.After(wCutoff) {
				if firstInWindow.IsZero() {
					firstInWindow = t // oldest timestamp in this window
				}
				count++
			}
		}
		if count >= lim.Limit {
			r.windows[userID] = valid
			reset := now.Add(lim.Window)
			if !firstInWindow.IsZero() {
				reset = firstInWindow.Add(lim.Window)
			}
			return false, lim, count, reset
		}
	}

	r.windows[userID] = append(valid, now)
	return true, WindowLimit{}, 0, time.Time{}
}

// Purge removes entries whose timestamps are all older than maxAge.
// Call periodically (e.g., every minute) to prevent unbounded memory growth.
func (r *RateLimiter) Purge(maxAge time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for uid, times := range r.windows {
		var valid []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(r.windows, uid)
		} else {
			r.windows[uid] = valid
		}
	}
}
