package auth

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// HashPassword 将明文密码 hash 为 bcrypt 字符串
// 空密码视为无效输入，返回 error
func HashPassword(logger *zap.Logger, plain string) (string, error) {
	if plain == "" {
		logger.Warn("HashPassword called with empty password")
		return "", errors.New("password must not be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		logger.Error("bcrypt GenerateFromPassword failed", zap.Error(err))
		return "", fmt.Errorf("hash password: %w", err)
	}
	logger.Debug("password hashed successfully")
	return string(hash), nil
}

// VerifyPassword 验证明文密码是否与 bcrypt hash 匹配
// 不匹配返回 false，不返回 error（避免调用方区分 hash 错误与密码错误）
func VerifyPassword(logger *zap.Logger, hash, plain string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	if err != nil {
		if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			// 非正常的验证错误（如 hash 格式损坏），记录为警告
			logger.Warn("bcrypt compare unexpected error", zap.Error(err))
		}
		return false
	}
	return true
}
