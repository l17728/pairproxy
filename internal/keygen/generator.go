// Package keygen 提供 PairProxy API Key 的生成与验证功能。
//
// Key 格式：sk-pp-<48字符Base62>，总长度 54 字符。
// v2.15.0 算法升级：使用 HMAC-SHA256 签名保证无碰撞。
package keygen

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"

	"go.uber.org/zap"
)

const (
	// KeyPrefix 是所有 PairProxy API Key 的固定前缀。
	KeyPrefix = "sk-pp-"
	// KeyBodyLen 是 Key 前缀之后的主体长度（Base62 字符）。
	KeyBodyLen = 48
	// KeyTotalLen 是 Key 的总长度（前缀 + 主体）。
	KeyTotalLen = len(KeyPrefix) + KeyBodyLen
	// Charset 是 Key 主体允许使用的字符集（Base62: 0-9A-Za-z）。
	Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// base62Radix 是 Base62 编码的基数。
	base62Radix = 62
)

// GenerateKey 根据用户名和密钥生成一个 API Key（HMAC-SHA256 算法）。
//
// 算法：
//  1. 计算 HMAC-SHA256(secret, username) → 32 字节签名
//  2. Base62 编码签名 → ~43 字符
//  3. 填充/截断到 KeyBodyLen (48) 字符
//  4. 拼接前缀 "sk-pp-" 返回
//
// 特性：
//   - 确定性：相同 username + secret 总是生成相同 key
//   - 无碰撞：HMAC-SHA256 密码学保证（碰撞概率 < 2^-143）
func GenerateKey(username string, secret []byte) (string, error) {
	if username == "" {
		return "", fmt.Errorf("username cannot be empty")
	}
	if len(secret) < 32 {
		return "", fmt.Errorf("secret must be at least 32 bytes (got %d)", len(secret))
	}

	// 计算 HMAC-SHA256
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(username))
	signature := h.Sum(nil)

	// Base62 编码并填充/截断到 KeyBodyLen
	body := encodeBase62HMAC(signature)

	key := KeyPrefix + body
	zap.L().Debug("api key generated (hmac)",
		zap.String("username", username),
		zap.Int("key_length", len(key)),
		zap.Int("signature_bytes", len(signature)),
	)
	return key, nil
}

// encodeBase62HMAC 将 HMAC 签名编码为 Base62 并填充/截断到 KeyBodyLen。
//
// 使用大整数除法实现 Base62 编码（无外部依赖）。
// Base62 编码 32 字节 HMAC 通常产生 ~43 字符，我们需要精确 48 字符：
//   - 如果 < 48：右侧填充 '0'
//   - 如果 > 48：截断到前 48 字符（保留最大熵）
func encodeBase62HMAC(data []byte) string {
	// 将字节数组转为大整数
	num := new(big.Int).SetBytes(data)
	base := big.NewInt(base62Radix)
	mod := new(big.Int)
	zero := big.NewInt(0)

	var result []byte
	for num.Cmp(zero) > 0 {
		num.DivMod(num, base, mod)
		result = append(result, Charset[mod.Int64()])
	}

	// 反转（大端序）
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	encoded := string(result)
	if len(encoded) < KeyBodyLen {
		// 右填充到 KeyBodyLen
		encoded = encoded + strings.Repeat("0", KeyBodyLen-len(encoded))
	} else if len(encoded) > KeyBodyLen {
		// 截断到 KeyBodyLen（保留左侧高熵部分）
		encoded = encoded[:KeyBodyLen]
	}
	return encoded
}
