package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/l17728/pairproxy/internal/config"
)

// LLMTargetModelRepo 管理 llm_target_models 表的 CRUD 操作。
type LLMTargetModelRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewLLMTargetModelRepo 创建 LLMTargetModelRepo。
func NewLLMTargetModelRepo(db *gorm.DB, logger *zap.Logger) *LLMTargetModelRepo {
	return &LLMTargetModelRepo{db: db, logger: logger.Named("llm_target_model_repo")}
}

// UpsertFromConfig 将配置文件中的模型条目同步到数据库（仅更新 source=config 的条目）。
// 相同 (target_url, model_id) 已存在则更新，不存在则插入。
// source=database 的手动添加条目不受影响。
func (r *LLMTargetModelRepo) UpsertFromConfig(targetURL string, models []config.ModelEntry) error {
	for _, m := range models {
		aliasesJSON, err := json.Marshal(m.Aliases)
		if err != nil {
			r.logger.Error("failed to marshal aliases",
				zap.String("model_id", m.ID),
				zap.Error(err),
			)
			continue
		}
		upstreamName := m.UpstreamName
		if upstreamName == "" {
			upstreamName = m.ID
		}

		// 先查找同 (target_url, model_id) 是否已有 source=config 条目
		var existing LLMTargetModel
		res := r.db.Where("target_url = ? AND model_id = ? AND source = 'config'", targetURL, m.ID).
			First(&existing)
		if res.Error == nil {
			// 更新已有配置条目
			if err := r.db.Model(&existing).Updates(map[string]interface{}{
				"aliases_json":  string(aliasesJSON),
				"is_default":    m.Default,
				"upstream_name": upstreamName,
				"is_active":     true,
			}).Error; err != nil {
				r.logger.Error("failed to update LLMTargetModel from config",
					zap.String("target_url", targetURL),
					zap.String("model_id", m.ID),
					zap.Error(err),
				)
				return fmt.Errorf("update model %s for target %s: %w", m.ID, targetURL, err)
			}
			r.logger.Debug("updated LLMTargetModel from config",
				zap.String("target_url", targetURL),
				zap.String("model_id", m.ID),
			)
		} else {
			// 插入新条目
			entry := LLMTargetModel{
				ID:           uuid.NewString(),
				TargetURL:    targetURL,
				ModelID:      m.ID,
				AliasesJSON:  string(aliasesJSON),
				IsDefault:    m.Default,
				UpstreamName: upstreamName,
				Source:       "config",
				IsActive:     true,
			}
			if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
				r.logger.Error("failed to insert LLMTargetModel from config",
					zap.String("target_url", targetURL),
					zap.String("model_id", m.ID),
					zap.Error(err),
				)
				return fmt.Errorf("insert model %s for target %s: %w", m.ID, targetURL, err)
			}
			r.logger.Debug("inserted LLMTargetModel from config",
				zap.String("target_url", targetURL),
				zap.String("model_id", m.ID),
			)
		}
	}
	return nil
}

// ListByTarget 返回指定 target 的所有活跃模型条目。
func (r *LLMTargetModelRepo) ListByTarget(targetURL string) ([]LLMTargetModel, error) {
	var models []LLMTargetModel
	if err := r.db.Where("target_url = ? AND is_active = true", targetURL).
		Order("is_default DESC, model_id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list models for target %s: %w", targetURL, err)
	}
	return models, nil
}

// ListAll 返回所有活跃的模型条目。
func (r *LLMTargetModelRepo) ListAll() ([]LLMTargetModel, error) {
	var models []LLMTargetModel
	if err := r.db.Where("is_active = true").
		Order("target_url ASC, is_default DESC, model_id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list all models: %w", err)
	}
	return models, nil
}

// FindTargetForModel 根据 modelID 查找对应的 target URL 和上游模型名。
// 精确匹配 model_id，若无则匹配 aliases JSON 中的值。
// 若多个 target 支持同一 model，返回第一个（按 target_url 排序；调用方可自行加权选择）。
func (r *LLMTargetModelRepo) FindTargetForModel(modelID string) (targetURL, upstreamName string, found bool, err error) {
	// 1. 精确匹配 model_id
	var exact LLMTargetModel
	if res := r.db.Where("model_id = ? AND is_active = true", modelID).
		Order("target_url ASC").
		First(&exact); res.Error == nil {
		up := exact.UpstreamName
		if up == "" {
			up = exact.ModelID
		}
		r.logger.Debug("model route found (exact match)",
			zap.String("model_id", modelID),
			zap.String("target_url", exact.TargetURL),
			zap.String("upstream_name", up),
		)
		return exact.TargetURL, up, true, nil
	}

	// 2. 别名匹配（扫描 aliases_json 列包含该 modelID 的行）
	// SQLite JSON_EACH / LIKE 均可；用 LIKE 简单可靠（别名中不含特殊字符）
	aliasPattern := fmt.Sprintf(`%%"%s"%%`, escapeLike(modelID))
	var alias LLMTargetModel
	if res := r.db.Where("aliases_json LIKE ? AND is_active = true", aliasPattern).
		Order("target_url ASC").
		First(&alias); res.Error == nil {
		// 双重确认：反序列化后精确匹配
		var aliases []string
		if jsonErr := json.Unmarshal([]byte(alias.AliasesJSON), &aliases); jsonErr == nil {
			for _, a := range aliases {
				if a == modelID {
					up := alias.UpstreamName
					if up == "" {
						up = alias.ModelID
					}
					r.logger.Debug("model route found (alias match)",
						zap.String("model_id", modelID),
						zap.String("matched_via", alias.ModelID),
						zap.String("target_url", alias.TargetURL),
						zap.String("upstream_name", up),
					)
					return alias.TargetURL, up, true, nil
				}
			}
		}
	}

	r.logger.Debug("no model route found", zap.String("model_id", modelID))
	return "", "", false, nil
}

// GetDefaultModelForTarget 返回指定 target 的默认模型 ID（is_default=true）。
// 若无默认模型则返回空字符串。
func (r *LLMTargetModelRepo) GetDefaultModelForTarget(targetURL string) (modelID string, upstreamName string, found bool, err error) {
	var m LLMTargetModel
	if res := r.db.Where("target_url = ? AND is_default = true AND is_active = true", targetURL).
		First(&m); res.Error == nil {
		up := m.UpstreamName
		if up == "" {
			up = m.ModelID
		}
		return m.ModelID, up, true, nil
	}
	return "", "", false, nil
}

// Create 创建一个新的模型条目（source=database）。
func (r *LLMTargetModelRepo) Create(targetURL, modelID, upstreamName string, aliases []string, isDefault bool) (*LLMTargetModel, error) {
	if upstreamName == "" {
		upstreamName = modelID
	}
	aliasesJSON, err := json.Marshal(aliases)
	if err != nil {
		return nil, fmt.Errorf("marshal aliases: %w", err)
	}
	entry := &LLMTargetModel{
		ID:           uuid.NewString(),
		TargetURL:    targetURL,
		ModelID:      modelID,
		AliasesJSON:  string(aliasesJSON),
		IsDefault:    isDefault,
		UpstreamName: upstreamName,
		Source:       "database",
		IsActive:     true,
	}
	if err := r.db.Create(entry).Error; err != nil {
		return nil, fmt.Errorf("create model %s for target %s: %w", modelID, targetURL, err)
	}
	r.logger.Info("created LLMTargetModel",
		zap.String("target_url", targetURL),
		zap.String("model_id", modelID),
	)
	return entry, nil
}

// Delete 删除指定 (target_url, model_id) 的模型条目。
func (r *LLMTargetModelRepo) Delete(targetURL, modelID string) error {
	if err := r.db.Where("target_url = ? AND model_id = ?", targetURL, modelID).
		Delete(&LLMTargetModel{}).Error; err != nil {
		return fmt.Errorf("delete model %s for target %s: %w", modelID, targetURL, err)
	}
	r.logger.Info("deleted LLMTargetModel",
		zap.String("target_url", targetURL),
		zap.String("model_id", modelID),
	)
	return nil
}

// DeleteByTarget 删除指定 target_url 的所有模型条目（force delete target 时使用）。
func (r *LLMTargetModelRepo) DeleteByTarget(targetURL string) error {
	if err := r.db.Where("target_url = ?", targetURL).
		Delete(&LLMTargetModel{}).Error; err != nil {
		return fmt.Errorf("delete all models for target %s: %w", targetURL, err)
	}
	return nil
}

// Aliases 反序列化 AliasesJSON 返回字符串切片。
func (m *LLMTargetModel) Aliases() []string {
	var aliases []string
	if err := json.Unmarshal([]byte(m.AliasesJSON), &aliases); err != nil {
		return nil
	}
	return aliases
}

// ResolvedUpstreamName 返回实际上游模型名（为空时退回到 ModelID）。
func (m *LLMTargetModel) ResolvedUpstreamName() string {
	if m.UpstreamName != "" {
		return m.UpstreamName
	}
	return m.ModelID
}

// escapeLike 对 LIKE pattern 中的特殊字符进行转义。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
