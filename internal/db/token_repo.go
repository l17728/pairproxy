package db

import (
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RefreshTokenRepo 管理刷新令牌的持久化存储
type RefreshTokenRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewRefreshTokenRepo 创建 RefreshTokenRepo
func NewRefreshTokenRepo(db *gorm.DB, logger *zap.Logger) *RefreshTokenRepo {
	return &RefreshTokenRepo{db: db, logger: logger.Named("token_repo")}
}

// Create 持久化一条新的刷新令牌
func (r *RefreshTokenRepo) Create(rt *RefreshToken) error {
	if rt.CreatedAt.IsZero() {
		rt.CreatedAt = time.Now()
	}
	if err := r.db.Create(rt).Error; err != nil {
		r.logger.Error("failed to create refresh token",
			zap.String("user_id", rt.UserID),
			zap.Error(err),
		)
		return fmt.Errorf("create refresh token: %w", err)
	}
	r.logger.Debug("refresh token created",
		zap.String("jti", rt.JTI),
		zap.String("user_id", rt.UserID),
		zap.Time("expires_at", rt.ExpiresAt),
	)
	return nil
}

// GetByJTI 按 JTI 查询（不存在返回 nil, nil）
func (r *RefreshTokenRepo) GetByJTI(jti string) (*RefreshToken, error) {
	var rt RefreshToken
	err := r.db.Where("jti = ?", jti).First(&rt).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.logger.Debug("refresh token not found", zap.String("jti", jti))
			return nil, nil
		}
		r.logger.Error("failed to get refresh token", zap.String("jti", jti), zap.Error(err))
		return nil, fmt.Errorf("get refresh token %q: %w", jti, err)
	}
	return &rt, nil
}

// Revoke 将指定 JTI 的刷新令牌标记为已撤销
func (r *RefreshTokenRepo) Revoke(jti string) error {
	result := r.db.Model(&RefreshToken{}).
		Where("jti = ?", jti).
		Update("revoked", true)
	if result.Error != nil {
		r.logger.Error("failed to revoke refresh token",
			zap.String("jti", jti),
			zap.Error(result.Error),
		)
		return fmt.Errorf("revoke refresh token %q: %w", jti, result.Error)
	}
	r.logger.Info("refresh token revoked",
		zap.String("jti", jti),
		zap.Int64("rows_affected", result.RowsAffected),
	)
	return nil
}

// RevokeAllForUser 撤销指定用户的所有刷新令牌（强制下线）
func (r *RefreshTokenRepo) RevokeAllForUser(userID string) error {
	result := r.db.Model(&RefreshToken{}).
		Where("user_id = ? AND revoked = ?", userID, false).
		Update("revoked", true)
	if result.Error != nil {
		r.logger.Error("failed to revoke all tokens for user",
			zap.String("user_id", userID),
			zap.Error(result.Error),
		)
		return fmt.Errorf("revoke all tokens for user %q: %w", userID, result.Error)
	}
	r.logger.Info("all refresh tokens revoked for user",
		zap.String("user_id", userID),
		zap.Int64("count", result.RowsAffected),
	)
	return nil
}

// DeleteExpired 删除所有已过期的令牌（定期清理用）
func (r *RefreshTokenRepo) DeleteExpired() (int64, error) {
	result := r.db.Where("expires_at < ?", time.Now()).Delete(&RefreshToken{})
	if result.Error != nil {
		r.logger.Error("failed to delete expired tokens", zap.Error(result.Error))
		return 0, fmt.Errorf("delete expired tokens: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		r.logger.Info("expired refresh tokens deleted", zap.Int64("count", result.RowsAffected))
	}
	return result.RowsAffected, nil
}
