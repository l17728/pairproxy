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

// Create 创建新的 API Key 记录（encryptedValue 已加密/混淆）。
// keyScheme 指定存储格式："aes"（AES-256-GCM）或 "obfuscated"（config-sync 路径，默认）。
func (r *APIKeyRepo) Create(name, encryptedValue, provider, keyScheme string) (*APIKey, error) {
	if provider == "" {
		provider = "anthropic"
	}
	if keyScheme == "" {
		keyScheme = "obfuscated"
	}
	key := &APIKey{
		ID:             uuid.New().String(),
		Name:           name,
		EncryptedValue: encryptedValue,
		KeyScheme:      keyScheme,
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

// FindByProviderAndValue 按 (provider, encrypted_value) 查找 API Key。
// 用于 config-sync 时检查相同 key 值是否已存在，避免重复创建。
// 返回 nil 表示不存在（不是错误）。
// 防御性检查：若找到多条（无 UNIQUE 约束），记录 Error 并返回 ErrAmbiguous。
func (r *APIKeyRepo) FindByProviderAndValue(provider, encryptedValue string) (*APIKey, error) {
	var keys []APIKey
	if err := r.db.Where("provider = ? AND encrypted_value = ?", provider, encryptedValue).Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("find api key by provider and value: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	if len(keys) > 1 {
		r.logger.Error("data integrity issue: multiple api keys with same provider and encrypted_value",
			zap.String("provider", provider),
			zap.Int("count", len(keys)),
		)
		return nil, fmt.Errorf("ambiguous api key: %d keys found for provider %q", len(keys), provider)
	}
	return &keys[0], nil
}
