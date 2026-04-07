package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// APIKeyRepo 提供 API Key 的 CRUD 和分配查询接口。
type APIKeyRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewAPIKeyRepo 创建 APIKeyRepo。
func NewAPIKeyRepo(db *gorm.DB, logger *zap.Logger) *APIKeyRepo {
	return &APIKeyRepo{db: db, logger: logger.Named("apikey_repo")}
}

// Create 创建新的 API Key 记录（encryptedValue 已加密）。
func (r *APIKeyRepo) Create(name, encryptedValue, provider string) (*APIKey, error) {
	if provider == "" {
		provider = "anthropic"
	}
	key := &APIKey{
		ID:             uuid.New().String(),
		Name:           name,
		EncryptedValue: encryptedValue,
		Provider:       provider,
		IsActive:       true,
		CreatedAt:      time.Now(),
	}
	if err := r.db.Create(key).Error; err != nil {
		r.logger.Error("failed to create api key",
			zap.String("name", name),
			zap.Error(err),
		)
		return nil, fmt.Errorf("create api key %q: %w", name, err)
	}
	r.logger.Info("api key created",
		zap.String("id", key.ID),
		zap.String("name", name),
		zap.String("provider", provider),
	)
	return key, nil
}

// GetByName 按名称查询 API Key。
func (r *APIKeyRepo) GetByName(name string) (*APIKey, error) {
	var key APIKey
	err := r.db.Where("name = ?", name).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get api key %q: %w", name, err)
	}
	return &key, nil
}

// GetByID 按 ID 查询 API Key。
func (r *APIKeyRepo) GetByID(id string) (*APIKey, error) {
	var key APIKey
	err := r.db.Where("id = ?", id).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get api key by id %q: %w", id, err)
	}
	return &key, nil
}

// List 列出所有 API Key（包含非活跃记录）。
func (r *APIKeyRepo) List() ([]APIKey, error) {
	var keys []APIKey
	if err := r.db.Order("created_at ASC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// Assign 将 API Key 分配给指定用户或分组。
// userID 和 groupID 至少一个非空；若同时提供，优先级由 FindForUser 的查询顺序决定。
// 重复调用视为 upsert（先删除旧分配，再创建新分配）。
func (r *APIKeyRepo) Assign(keyID string, userID, groupID *string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 删除同 user/group 的旧分配
		q := tx.Where("api_key_id = ?", keyID)
		if userID != nil {
			q = q.Where("user_id = ?", *userID)
		} else {
			q = q.Where("user_id IS NULL")
		}
		if groupID != nil {
			q = q.Where("group_id = ?", *groupID)
		} else {
			q = q.Where("group_id IS NULL")
		}
		if err := q.Delete(&APIKeyAssignment{}).Error; err != nil {
			r.logger.Error("assign: failed to remove old assignment", zap.Error(err))
			return fmt.Errorf("remove old assignment: %w", err)
		}

		assign := &APIKeyAssignment{
			ID:       uuid.New().String(),
			APIKeyID: keyID,
			UserID:   userID,
			GroupID:  groupID,
		}
		if err := tx.Create(assign).Error; err != nil {
			r.logger.Error("failed to assign api key",
				zap.String("key_id", keyID),
				zap.Error(err),
			)
			return fmt.Errorf("assign api key: %w", err)
		}
		r.logger.Info("api key assigned",
			zap.String("key_id", keyID),
			zap.Any("user_id", userID),
			zap.Any("group_id", groupID),
		)
		return nil
	})
}

// Revoke 停用指定 API Key（软删除，不影响历史记录）。
func (r *APIKeyRepo) Revoke(id string) error {
	result := r.db.Model(&APIKey{}).Where("id = ?", id).Update("is_active", false)
	if result.Error != nil {
		r.logger.Error("failed to revoke api key",
			zap.String("id", id),
			zap.Error(result.Error),
		)
		return fmt.Errorf("revoke api key %q: %w", id, result.Error)
	}
	r.logger.Info("api key revoked", zap.String("id", id))
	return nil
}

// FindForUser 查找适用于指定用户的 API Key（用户级分配优先于分组级）。
// userID 为用户 ID；groupID 为该用户的分组 ID（空字符串表示无分组）。
// 返回 nil 表示无匹配记录，调用方应回退到配置文件中的静态 API Key。
func (r *APIKeyRepo) FindForUser(userID, groupID string) (*APIKey, error) {
	// 1. 先查用户级分配
	if userID != "" {
		key, err := r.findByAssignment(userID, true)
		if err != nil {
			return nil, err
		}
		if key != nil {
			r.logger.Debug("api key found: user assignment",
				zap.String("user_id", userID),
				zap.String("key_name", key.Name),
			)
			return key, nil
		}
	}

	// 2. 再查分组级分配
	if groupID != "" {
		key, err := r.findByAssignment(groupID, false)
		if err != nil {
			return nil, err
		}
		if key != nil {
			r.logger.Debug("api key found: group assignment",
				zap.String("group_id", groupID),
				zap.String("key_name", key.Name),
			)
			return key, nil
		}
	}

	return nil, nil
}

// findByAssignment 通过 user_id 或 group_id 查找活跃的 API Key。
func (r *APIKeyRepo) findByAssignment(id string, isUser bool) (*APIKey, error) {
	var assign APIKeyAssignment
	q := r.db
	if isUser {
		q = q.Where("user_id = ?", id)
	} else {
		q = q.Where("group_id = ?", id)
	}
	err := q.First(&assign).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find assignment: %w", err)
	}

	var key APIKey
	err = r.db.Where("id = ? AND is_active = ?", assign.APIKeyID, true).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // key was revoked
		}
		return nil, fmt.Errorf("find api key by assignment: %w", err)
	}
	return &key, nil
}

// FindByProviderAndValue 按 (provider, encrypted_value) 查找 API Key。
// 用于 config-sync 时检查相同 key 值是否已存在，避免重复创建。
// 返回 nil 表示不存在（不是错误）。
func (r *APIKeyRepo) FindByProviderAndValue(provider, encryptedValue string) (*APIKey, error) {
	var key APIKey
	err := r.db.Where("provider = ? AND encrypted_value = ?", provider, encryptedValue).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find api key by provider and value: %w", err)
	}
	return &key, nil
}
