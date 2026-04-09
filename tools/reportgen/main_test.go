package main

import (
	"testing"
	"time"
)

// TestEndOfDay 验证 endOfDay 函数正确返回次日的 00:00:00
func TestEndOfDay(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Time
		expected time.Time
	}{
		{
			name:     "基本日期",
			input:    time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "月末日期",
			input:    time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "年末日期",
			input:    time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "闰年2月末",
			input:    time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := endOfDay(tt.input)
			// 比较年月日时分秒
			if result.Year() != tt.expected.Year() ||
				result.Month() != tt.expected.Month() ||
				result.Day() != tt.expected.Day() ||
				result.Hour() != tt.expected.Hour() ||
				result.Minute() != tt.expected.Minute() ||
				result.Second() != tt.expected.Second() {
				t.Errorf("endOfDay(%v) = %v, 期望 %v",
					tt.input.Format("2006-01-02"),
					result.Format("2006-01-02 15:04:05"),
					tt.expected.Format("2006-01-02 15:04:05"))
			}
		})
	}
}

// TestTimeRangeInclusion 验证时间范围查询包含完整的最后一天
func TestTimeRangeInclusion(t *testing.T) {
	// 模拟用户查询 2026-04-01 到 2026-04-04
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	toInput := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	to := endOfDay(toInput)

	// 验证时间范围
	expectedTo := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	if to != expectedTo {
		t.Errorf("endOfDay 返回错误的时间: %v, 期望 %v", to.Format("2006-01-02 15:04:05"), expectedTo.Format("2006-01-02 15:04:05"))
	}

	// 验证 SQL 查询将正确包含 2026-04-04 的所有数据
	// created_at >= 2026-04-01 00:00:00 AND created_at < 2026-04-05 00:00:00
	testCases := []struct {
		name      string
		timestamp time.Time
		shouldInclude bool
	}{
		{
			name:      "2026-04-01 起始",
			timestamp: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			shouldInclude: true,
		},
		{
			name:      "2026-04-04 中间",
			timestamp: time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
			shouldInclude: true,
		},
		{
			name:      "2026-04-04 末尾 23:59:59",
			timestamp: time.Date(2026, 4, 4, 23, 59, 59, 0, time.UTC),
			shouldInclude: true,
		},
		{
			name:      "2026-04-05 00:00:00 (不应包含)",
			timestamp: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
			shouldInclude: false,
		},
		{
			name:      "2026-03-31 (在范围前)",
			timestamp: time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC),
			shouldInclude: false,
		},
	}

	for _, tc := range testCases {
		isIncluded := (tc.timestamp.After(from) || tc.timestamp.Equal(from)) && tc.timestamp.Before(to)
		if isIncluded != tc.shouldInclude {
			status := "包含"
			if !tc.shouldInclude {
				status = "不包含"
			}
			t.Errorf("%s: 时间戳 %v 应该被%s, 但实际被%v",
				tc.name,
				tc.timestamp.Format("2006-01-02 15:04:05"),
				status,
				map[bool]string{true: "包含", false: "不包含"}[isIncluded])
		}
	}
}

// BenchmarkEndOfDay 基准测试 endOfDay 函数
func BenchmarkEndOfDay(b *testing.B) {
	t := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	for i := 0; i < b.N; i++ {
		_ = endOfDay(t)
	}
}
