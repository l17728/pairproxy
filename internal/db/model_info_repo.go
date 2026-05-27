package db

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ModelInfoRepo 提供 model_infos 表的查询能力。
// 该表由管理员手动维护，model_router 路由决策时只读查询。
type ModelInfoRepo struct {
	db *gorm.DB
}

// NewModelInfoRepo 创建 ModelInfoRepo。
func NewModelInfoRepo(db *gorm.DB) *ModelInfoRepo {
	return &ModelInfoRepo{db: db}
}

// ListMultimodal 返回所有支持多模态输入的模型（is_multimodal=true）。
// 用于多模态请求路径：将结果与 candidateModels 求交集后作为路由候选。
func (r *ModelInfoRepo) ListMultimodal() ([]ModelInfo, error) {
	var infos []ModelInfo
	if err := r.db.Where("is_multimodal = ?", true).Find(&infos).Error; err != nil {
		return nil, fmt.Errorf("model_info_repo: list multimodal: %w", err)
	}
	return infos, nil
}

// ListNonMultimodal 返回所有不支持多模态输入的模型（is_multimodal=false）。
// 用于纯文本请求路径：将结果与 candidateModels 求交集后作为路由候选，
// 避免将文本请求路由到多模态（通常更贵）的模型。
func (r *ModelInfoRepo) ListNonMultimodal() ([]ModelInfo, error) {
	var infos []ModelInfo
	if err := r.db.Where("is_multimodal = ?", false).Find(&infos).Error; err != nil {
		return nil, fmt.Errorf("model_info_repo: list non-multimodal: %w", err)
	}
	return infos, nil
}

// GetScaleByName 按模型名查询参数规模（"big" | "middle" | "small"）。
// 若模型不在表中，返回 ("", nil)；调用方应将空字符串视为 unknown。
func (r *ModelInfoRepo) GetScaleByName(modelName string) (string, error) {
	var info ModelInfo
	err := r.db.Where("model_name = ?", modelName).First(&info).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("model_info_repo: get scale for %q: %w", modelName, err)
	}
	return info.ModelScale, nil
}
