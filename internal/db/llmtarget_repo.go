package db

import (
	"fmt"

	"github.com/google/uuid"
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

// GetByURL 根据 URL 查询 LLM target（返回第一条匹配记录）。
// ⚠️ 已弃用：当 (url, api_key_id) 有复合唯一约束时，仅 URL 查询会产生歧义。
// 改用 ListByURL() 获取所有匹配记录，调用方处理歧义（len > 1 时返回错误）。
// 此方法仅供 backward compatibility 和测试保留。
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

// ListByURL 根据 URL 查询所有匹配的 LLM target（支持同 URL 多 Key 场景）。
// 返回所有记录，调用方负责判断是否有歧义（len > 1）。
func (r *LLMTargetRepo) ListByURL(url string) ([]*LLMTarget, error) {
	var targets []*LLMTarget
	if err := r.db.Where("url = ?", url).Order("created_at ASC").Find(&targets).Error; err != nil {
		r.logger.Error("failed to list llm targets by url",
			zap.String("url", url),
			zap.Error(err))
		return nil, fmt.Errorf("list llm targets by url: %w", err)
	}
	return targets, nil
}

// GetByURLAndAPIKeyID 根据 (url, api_key_id) 复合键查询 LLM target。
// apiKeyID 为 nil 时匹配 api_key_id IS NULL 的记录。
func (r *LLMTargetRepo) GetByURLAndAPIKeyID(url string, apiKeyID *string) (*LLMTarget, error) {
	var target LLMTarget
	q := r.db.Where("url = ?", url)
	if apiKeyID == nil {
		q = q.Where("api_key_id IS NULL")
	} else {
		q = q.Where("api_key_id = ?", *apiKeyID)
	}
	if err := q.First(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		r.logger.Error("failed to get llm target by url and api_key_id",
			zap.String("url", url),
			zap.Error(err))
		return nil, fmt.Errorf("get llm target by url and api_key_id: %w", err)
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

// URLExists 检查 URL 是否已存在（忽视 api_key_id）。
// ⚠️ 注意：同一 URL 可以有多个不同 api_key_id 的 target（Issue #6）。
// 如果需要检查 (url, api_key_id) 的唯一性，应使用 ComboExists。
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

// ComboExists 检查 (url, api_key_id) 复合键是否已存在。
// api_key_id 为 nil 时匹配无 API Key 的记录（IS NULL）。
// 用于创建新 target 前的重复检查，正确支持同 URL 多 Key 场景。
func (r *LLMTargetRepo) ComboExists(url string, apiKeyID *string) (bool, error) {
	var count int64
	query := r.db.Model(&LLMTarget{}).Where("url = ?", url)
	if apiKeyID == nil {
		query = query.Where("api_key_id IS NULL")
	} else {
		query = query.Where("api_key_id = ?", *apiKeyID)
	}
	if err := query.Count(&count).Error; err != nil {
		r.logger.Error("failed to check (url, api_key_id) combo exists",
			zap.String("url", url),
			zap.Any("api_key_id", apiKeyID),
			zap.Error(err))
		return false, fmt.Errorf("check combo exists: %w", err)
	}
	return count > 0, nil
}

// Seed 仅在 (URL, api_key_id) 组合不存在时插入 target。已存在则跳过，保留 WebUI 修改。
// 用于配置文件启动同步：配置文件是种子，不是权威源。
// 与 Upsert 不同的是，Seed 优先保留已存在的记录，不覆盖。
func (r *LLMTargetRepo) Seed(target *LLMTarget) error {
	exists, err := r.ComboExists(target.URL, target.APIKeyID)
	if err != nil {
		return fmt.Errorf("seed: check url exists: %w", err)
	}
	if exists {
		r.logger.Debug("seed: target already exists, skipping",
			zap.String("url", target.URL),
			zap.String("source", target.Source))
		return nil
	}

	// URL 不存在 → 首次插入，保留调用方传入的 IsEditable 值。
	// config-sourced target 由 syncConfigTargetsToDatabase 设置 IsEditable=false，
	// 不得在此处覆盖为 true，否则 WebUI 会错误地允许编辑/删除配置文件来源的 target。
	r.logger.Info("seed: inserting new config target",
		zap.String("url", target.URL),
		zap.String("provider", target.Provider))
	return r.Upsert(target)
}

// Update 更新 LLM target（仅可编辑的）
func (r *LLMTargetRepo) Update(target *LLMTarget) error {
	// 检查是否可编辑
	if !target.IsEditable {
		return fmt.Errorf("target is not editable (config-sourced)")
	}

	if err := r.db.Save(target).Error; err != nil {
		r.logger.Error("failed to update llm target",
			zap.String("id", target.ID),
			zap.String("url", target.URL),
			zap.Error(err))
		return fmt.Errorf("update llm target: %w", err)
	}

	r.logger.Info("llm target updated",
		zap.String("id", target.ID),
		zap.String("url", target.URL))

	return nil
}

// Delete 删除 LLM target（仅可编辑的）
func (r *LLMTargetRepo) Delete(id string) error {
	// 先查询检查是否可编辑
	target, err := r.GetByID(id)
	if err != nil {
		return err
	}

	if !target.IsEditable {
		return fmt.Errorf("target is not editable (config-sourced)")
	}

	if err := r.db.Delete(&LLMTarget{}, "id = ?", id).Error; err != nil {
		r.logger.Error("failed to delete llm target",
			zap.String("id", id),
			zap.Error(err))
		return fmt.Errorf("delete llm target: %w", err)
	}

	r.logger.Info("llm target deleted",
		zap.String("id", id),
		zap.String("url", target.URL))

	return nil
}

// Upsert 插入或更新 LLM target（用于配置文件同步）。
// 按 (url, api_key_id) 复合键查重，相同组合 → 更新，不同组合 → 新增。
// 这允许同一 URL 绑定多个不同的 API Key。
func (r *LLMTargetRepo) Upsert(target *LLMTarget) error {
	// 按 (url, api_key_id) 查重
	existing, err := r.GetByURLAndAPIKeyID(target.URL, target.APIKeyID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	if existing != nil {
		// 更新：保留 ID，更新其他字段
		target.ID = existing.ID
		// 使用 Select 明确指定要更新的字段，包括 boolean false 值
		if err := r.db.Model(&LLMTarget{}).Where("id = ?", target.ID).
			Select("*").
			Updates(target).Error; err != nil {
			r.logger.Error("failed to upsert (update) llm target",
				zap.String("url", target.URL),
				zap.Error(err))
			return fmt.Errorf("upsert llm target: %w", err)
		}
		r.logger.Debug("llm target upserted (updated)",
			zap.String("url", target.URL))
	} else {
		// 插入：生成新 ID
		if target.ID == "" {
			target.ID = uuid.NewString()
		}
		// GORM gotcha: boolean false 值需要两步操作
		// 1. 先 Create（会使用 default:true）
		// 2. 再显式更新 boolean false 字段
		needsEditableFix := !target.IsEditable
		needsActiveFix := !target.IsActive

		if err := r.db.Create(target).Error; err != nil {
			r.logger.Error("failed to upsert (insert) llm target",
				zap.String("url", target.URL),
				zap.Error(err))
			return fmt.Errorf("upsert llm target: %w", err)
		}

		// 修复 boolean false 值
		if needsEditableFix || needsActiveFix {
			updates := make(map[string]interface{})
			if needsEditableFix {
				updates["is_editable"] = false
			}
			if needsActiveFix {
				updates["is_active"] = false
			}
			if err := r.db.Model(&LLMTarget{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
				r.logger.Error("failed to fix boolean fields",
					zap.String("id", target.ID),
					zap.Error(err))
				return fmt.Errorf("fix boolean fields: %w", err)
			}
		}

		r.logger.Debug("llm target upserted (inserted)",
			zap.String("url", target.URL))
	}

	return nil
}

// ConfigTargetKey 标识一条配置来源的 target，用于 cleanup 时精确匹配。
type ConfigTargetKey struct {
	URL      string
	APIKeyID *string // nil 表示无 API Key
}

// DeleteConfigTargetsNotInList 删除不在列表中的配置文件来源的 targets。
// 按 (url, api_key_id) 复合键匹配，精确删除已从配置文件移除的条目。
// 支持同一 URL 有多个不同 API Key 的场景。
func (r *LLMTargetRepo) DeleteConfigTargetsNotInList(keepKeys []ConfigTargetKey) (int, error) {
	// 查出所有 source='config' 的 targets
	var configTargets []*LLMTarget
	if err := r.db.Where("source = ?", "config").Find(&configTargets).Error; err != nil {
		return 0, fmt.Errorf("list config targets: %w", err)
	}

	// 构建保留集合
	type key struct {
		url      string
		apiKeyID string // 空字符串表示 nil
	}
	keepSet := make(map[key]struct{}, len(keepKeys))
	for _, k := range keepKeys {
		apiKeyStr := ""
		if k.APIKeyID != nil {
			apiKeyStr = *k.APIKeyID
		}
		keepSet[key{url: k.URL, apiKeyID: apiKeyStr}] = struct{}{}
	}

	// 删除不在保留集合中的
	deleted := 0
	for _, t := range configTargets {
		apiKeyStr := ""
		if t.APIKeyID != nil {
			apiKeyStr = *t.APIKeyID
		}
		if _, keep := keepSet[key{url: t.URL, apiKeyID: apiKeyStr}]; keep {
			continue
		}
		if err := r.db.Delete(&LLMTarget{}, "id = ?", t.ID).Error; err != nil {
			r.logger.Error("failed to delete stale config target",
				zap.String("id", t.ID),
				zap.String("url", t.URL),
				zap.Error(err))
			continue
		}
		deleted++
		r.logger.Debug("deleted stale config target",
			zap.String("id", t.ID),
			zap.String("url", t.URL))
	}

	if deleted > 0 {
		r.logger.Info("deleted config targets not in list",
			zap.Int("deleted", deleted),
			zap.Int("kept", len(keepKeys)))
	}

	return deleted, nil
}
