package config

import (
	"fmt"
	"time"
)

// Duration 是支持裸整数（秒）和 Go duration 字符串的 YAML 可解析类型。
//
// YAML 写法：
//   - 带单位字符串：300s、5m、1h
//   - 裸整数（按秒）：300、600
//   - 负整数（禁用语义）：-1
type Duration int64

// D 返回底层的 time.Duration 值，方便传入标准库函数。
func (d Duration) D() time.Duration {
	return time.Duration(d)
}

// UnmarshalYAML 支持整数（秒）和 Go duration 字符串两种写法。
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	// 先尝试整数（按秒解析，支持负数）
	var n int64
	if err := unmarshal(&n); err == nil {
		*d = Duration(time.Duration(n) * time.Second)
		return nil
	}
	// 再尝试字符串（标准 Go duration 格式）
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("duration: expected integer (seconds) or string (e.g. 300s, 5m), got neither")
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	*d = Duration(dur)
	return nil
}
