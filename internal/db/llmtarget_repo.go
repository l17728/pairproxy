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

// GetByURL 根据 URL 查询 LLM target
func (r *LLMTargetRepo) GetByURL(url string) (*LLMTarget, error) {
	var target LLMTarget
	if err := r.db.Where("url = ?", url).First(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		r.logger.Error("failed to get llm target by url",
			zap.String("url", url),
			zap.Error(err))
		return nil, fmt.Errorf("get llm target by url: %w", err)
	}
	return &target, nil
}

// GetByID 根据 ID 查询 LLM target
func (r *LLMTargetRepo) GetByID(id string) (*LLMTarget, error) {
	var target LLMTarget
	if err := r.db.Where("id = ?", id).First(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		r.logger.Error("failed to get llm target by id",
			zap.String("id", id),
			zap.Error(err))
		return nil, fmt.Errorf("get llm target by id: %w", err)
	}
	return &target, nil
}

// ListAll 列出所有 LLM targets
func (r *LLMTargetRepo) ListAll() ([]*LLMTarget, error) {
	var targets []*LLMTarget
	if err := r.db.Order("created_at DESC").Find(&targets).Error; err != nil {
		r.logger.Error("failed to list llm targets", zap.Error(err))
		return nil, fmt.Errorf("list llm targets: %w", err)
	}

	r.logger.Debug("listed llm targets", zap.Int("count", len(targets)))
	return targets, nil
}

// URLExists 检查 URL 是否已存在
func (r *LLMTargetRepo) URLExists(url string) (bool, error) {
	var count int64
	if err := r.db.Model(&LLMTarget{}).Where("url = ?", url).Count(&count).Error; err != nil {
		r.logger.Error("failed to check url exists",
			zap.String("url", url),
			zap.Error(err))
		return false, fmt.Errorf("check url exists: %w", err)
	}
	return count > 0, nil
}
