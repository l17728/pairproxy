package keygen

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// UserEntry 是 ValidateAndGetUser 所需的最小用户信息，
// 解耦了 keygen 包与 db 包的直接依赖。
type UserEntry struct {
	ID       string
	Username string
	IsActive bool
	GroupID  *string // 所属分组 ID（可为 nil）
}

// IsValidFormat 检查 Key 是否满足格式要求（前缀、总长度、字符集）。
// 不涉及用户身份验证，仅作格式预筛。
func IsValidFormat(key string) bool {
	if !strings.HasPrefix(key, KeyPrefix) {
		return false
	}
	body := key[len(KeyPrefix):]
	if len(body) != KeyBodyLen {
		return false
	}
	for _, c := range body {
		if !strings.ContainsRune(Charset, c) {
			return false
		}
	}
	return true
}

// ValidateAndGetUser 验证 Key 并返回匹配的用户。
//
// 验证逻辑：
//  1. 对 users 中每个活跃用户，提取其用户名的字母数字字符序列（含重复次数）
//  2. 检查 Key 主体（转小写）是否包含该序列的所有字符（含重复次数）
//  3. 选择匹配的最长用户名（最多字母数字字符数）
//  4. 若相同长度有多个用户匹配，返回 collision error
//
// 前提：调用方应确保 users 中的所有用户名均已通过 ValidateUsername 验证，
// 以避免字母数字字符极少的用户名（如 "----ab"）被意外匹配。
// 短用户名的碰撞问题属于已知设计限制，后续可通过修改 Key 生成算法解决。
//
// 返回 (nil, nil) 表示无匹配用户。
func ValidateAndGetUser(key string, users []UserEntry) (*UserEntry, error) {
	if !IsValidFormat(key) {
		return nil, nil
	}
	body := strings.ToLower(key[len(KeyPrefix):])

	var matched *UserEntry
	maxLen := 0
	collisionCount := 0

	for i := range users {
		u := &users[i]
		if !u.IsActive {
			continue
		}
		chars := ExtractAlphanumeric(u.Username)
		if len(chars) == 0 {
			continue
		}
		if !ContainsAllCharsWithCount(body, chars) {
			continue
		}
		l := len(chars)
		if l > maxLen {
			maxLen = l
			matched = u
			collisionCount = 1
		} else if l == maxLen {
			collisionCount++
		}
	}

	if collisionCount > 1 {
		zap.L().Warn("api key collision detected",
			zap.Int("collision_count", collisionCount),
			zap.Int("fingerprint_len", maxLen),
		)
		return nil, fmt.Errorf("collision detected: %d users share the same fingerprint length %d", collisionCount, maxLen)
	}

	if matched != nil {
		zap.L().Debug("api key validated",
			zap.String("username", matched.Username),
			zap.Int("fingerprint_len", maxLen),
		)
	}
	return matched, nil
}

// ValidateUsername 验证用户名是否满足 API Key 生成的最低要求。
// 规则：≥4字符，至少2个不同的字母数字字符。
func ValidateUsername(username string) error {
	if len(username) < 4 {
		return fmt.Errorf("username must be at least 4 characters, got %d", len(username))
	}
	chars := ExtractAlphanumeric(username)
	if len(chars) < 2 {
		return fmt.Errorf("username must contain at least 2 alphanumeric characters")
	}
	unique := make(map[byte]bool)
	for _, c := range chars {
		unique[c] = true
	}
	if len(unique) < 2 {
		return fmt.Errorf("username must contain at least 2 different alphanumeric characters")
	}
	return nil
}

// ContainsAllCharsWithCount 检查字符串 s 是否包含 chars 中每个字符所需的最低数量。
// 例如 chars=[]byte("aaab") 要求 s 中至少出现 3 个 'a' 和 1 个 'b'。
// s 和 chars 均已预期为小写。
func ContainsAllCharsWithCount(s string, chars []byte) bool {
	need := make(map[byte]int, len(chars))
	for _, c := range chars {
		need[c]++
	}
	have := make(map[byte]int, len(chars))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		have[c]++
	}
	for ch, n := range need {
		if have[ch] < n {
			return false
		}
	}
	return true
}
