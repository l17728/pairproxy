package keygen

import (
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

// ValidateAndGetUser 验证 Key 并返回匹配的用户（HMAC-SHA256 算法）。
//
// 验证逻辑：
//  1. 格式检查（快速拒绝无效格式）
//  2. 遍历所有活跃用户
//  3. 为每个用户重新计算 HMAC key：GenerateKey(username, secret)
//  4. 比较计算出的 key 与提供的 key
//  5. 匹配则返回该用户，否则继续
//  6. 全部不匹配返回 (nil, nil)
//
// 参数：
//   - key: 待验证的 API Key
//   - users: 候选用户列表（通常是所有活跃用户）
//   - secret: HMAC 签名密钥（必须与生成时使用的密钥相同）
//
// 返回：
//   - (*UserEntry, nil): 匹配的用户
//   - (nil, nil): 无匹配用户（格式错误或 key 不属于任何用户）
//   - (nil, error): 内部错误（不应发生，已记录 WARN 日志）
//
// 性能：
//   - 最坏情况 O(n)：遍历所有用户，每次 HMAC 计算 ~1μs
//   - 典型场景：KeyCache 缓存命中率 >95%，实际验证很少触发
//
// 安全：
//   - 不匹配时静默返回 nil（不记录日志，防止信息泄露）
//   - 仅在内部错误时记录 WARN 日志
func ValidateAndGetUser(key string, users []UserEntry, secret []byte) (*UserEntry, error) {
	if !IsValidFormat(key) {
		return nil, nil
	}

	for i := range users {
		u := &users[i]
		if !u.IsActive {
			continue
		}

		expectedKey, err := GenerateKey(u.Username, secret)
		if err != nil {
			zap.L().Warn("failed to generate key for user during validation",
				zap.String("username", u.Username),
				zap.Error(err),
			)
			continue
		}

		if key == expectedKey {
			zap.L().Debug("api key validated (hmac)",
				zap.String("username", u.Username),
			)
			return u, nil
		}
	}

	// 无匹配用户，静默返回（不记录日志，防止信息泄露）
	return nil, nil
}
