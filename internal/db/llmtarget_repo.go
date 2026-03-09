package db

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// LLMTargetRepo LLM Target 数据库操作
type LLMTargetRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewLLMTargetRepo 创建 LLMTargetRepo
func NewLLMTargetRepo(db *gorm.DB, logger *zap.Logger) *LLMTargetRepo {
	return &LLMTargetRepo{
		db:     db,
		logger: logger.Named("llmtarget_repo"),
	}
}

// Create 创建新的 LLM target
func (r *LLMTargetRepo) Create(target *LLMTarget) error {
	if err := r.db.Create(target).Error; err != nil {
		r.logger.Error("failed to create llm target",
			zap.String("url", target.URL),
			zap.Error(err))
		return fmt.Errorf("create llm target: %w", err)
	}

	r.logger.Info("llm target created",
		zap.String("id", target.ID),
		zap.String("url", target.URL),
		zap.String("source", target.Source))

	return nil
}

// GetByURL 根据 URL 查询 LLM target（临时实现，Task 1.4 将完善）
func (r *LLMTargetRepo) GetByURL(url string) (*LLMTarget, error) {
	var target LLMTarget
	if err := r.db.Where("url = ?", url).First(&target).Error; err != nil {
		return nil, err
	}
	return &target, nil
}
