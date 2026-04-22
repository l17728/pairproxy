package keygen

import (
	"strings"

	"go.uber.org/zap"
)

// UserEntry 是 ValidateAndGetUser 所需的最小用户信息，
// 解耦了 keygen 包与 db 包的直接依赖。
type UserEntry struct {
	ID               string
	Username         string
	PasswordHash     string  // bcrypt 哈希；LDAP 用户为空，无法持有 sk-pp- Key
	IsActive         bool
	GroupID          *string // 所属分组 ID（可为 nil）
	LegacyKeyRevoked bool    // true = 用户已主动改密，不再接受旧版 keygenSecret 派生的 Key
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
//  3. 为每个用户用其 PasswordHash 重新计算 HMAC key：GenerateKey(username, []byte(u.PasswordHash))
//  4. 比较计算出的 key 与提供的 key
//  5. 匹配则返回该用户，否则继续
//  6. 全部不匹配返回 (nil, nil)
//
// 参数：
//   - key: 待验证的 API Key
//   - users: 候选用户列表（通常是所有活跃用户，需包含 PasswordHash）
//
// 返回：
//   - (*UserEntry, nil): 匹配的用户
//   - (nil, nil): 无匹配用户（格式错误或 key 不属于任何用户）
//   - (nil, error): 保留签名兼容性，当前实现始终返回 nil error
//
// 注意：
//   - PasswordHash 为空的用户（LDAP 用户）会被静默跳过，无法持有 sk-pp- Key
//   - 不匹配时静默返回 nil（防止信息泄露）
func ValidateAndGetUser(key string, users []UserEntry) (*UserEntry, error) {
	if !IsValidFormat(key) {
		return nil, nil
	}

	for i := range users {
		u := &users[i]
		if !u.IsActive || u.PasswordHash == "" {
			// LDAP 用户无密码哈希，静默跳过
			continue
		}

		expectedKey, err := GenerateKey(u.Username, []byte(u.PasswordHash))
		if err != nil {
			zap.L().Warn("failed to generate key for user during validation",
				zap.String("username", u.Username),
				zap.Error(err),
			)
			continue
		}

		if key == expectedKey {
			zap.L().Debug("api key validated (hmac per-user)",
				zap.String("username", u.Username),
			)
			return u, nil
		}
	}

	// 无匹配用户，静默返回（不记录日志，防止信息泄露）
	return nil, nil
}

// ValidateWithLegacySecret 使用旧版共享 keygenSecret 做向后兼容校验。
//
// 旧版算法：HMAC-SHA256(key=keygenSecret原始字节, msg=username)。
// 升级到 per-user-password 方案后，若用户尚未重新获取新 Key，
// 其旧 Key 可通过此函数继续通过校验。
// 调用方应在 ValidateAndGetUser 返回 nil 后作为兜底尝试。
//
// secret 长度 < 32 时直接返回 nil（不满足 GenerateKey 要求）。
func ValidateWithLegacySecret(key string, users []UserEntry, secret []byte) *UserEntry {
	if len(secret) < 32 || !IsValidFormat(key) {
		return nil
	}
	for i := range users {
		u := &users[i]
		if !u.IsActive || u.LegacyKeyRevoked {
			// 用户已主动改密，旧版 keygenSecret Key 不再有效
			continue
		}
		expectedKey, err := GenerateKey(u.Username, secret)
		if err != nil {
			continue
		}
		if key == expectedKey {
			zap.L().Debug("api key validated (hmac legacy secret)",
				zap.String("username", u.Username),
			)
			return u
		}
	}
	return nil
}
