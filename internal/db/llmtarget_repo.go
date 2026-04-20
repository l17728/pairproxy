package db

import (
	"fmt"
	"time"

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

// GetByURL 根据 URL 查询 LLM target。URL 现为全局唯一，每个 URL 至多返回一条记录。
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

// ListByURL 根据 URL 查询所有匹配的 LLM target。
// 由于 URL 现为全局唯一，此方法最多返回 1 条记录（0 或 1）。
// 保留此方法以兼容旧调用方。
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

// GetByURLAndAPIKeyID 已弃用：URL 现为全局唯一，apiKeyID 参数被忽略。
// Deprecated: use GetByURL instead.
func (r *LLMTargetRepo) GetByURLAndAPIKeyID(url string, _ *string) (*LLMTarget, error) {
	return r.GetByURL(url)
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

// MaxUpdatedAt 返回 llm_targets 表中最大的 updated_at 时间戳。
// 用于 peer 模式的轮询感知：若结果比上次记录的时间戳新，则需要调用 SyncLLMTargets。
// 表为空时返回零值 time.Time{}，err 为 nil。
func (r *LLMTargetRepo) MaxUpdatedAt() (time.Time, error) {
	var result struct {
		Max *time.Time
	}
	if err := r.db.Model(&LLMTarget{}).Select("MAX(updated_at) AS max").Scan(&result).Error; err != nil {
		return time.Time{}, fmt.Errorf("max updated_at: %w", err)
	}
	if result.Max == nil {
		return time.Time{}, nil
	}
	return *result.Max, nil
}

// MarkUnsynced 将指定 target 的 is_synced 设为 false。
// 在任意写操作（Create/Update/Enable/Disable）成功后调用，
// 表示该 target 的 DB 状态尚未被运行时加载到内存。
func (r *LLMTargetRepo) MarkUnsynced(id string) error {
	if err := r.db.Model(&LLMTarget{}).Where("id = ?", id).
		Update("is_synced", false).Error; err != nil {
		r.logger.Warn("failed to mark target unsynced",
			zap.String("id", id), zap.Error(err))
		return fmt.Errorf("mark unsynced: %w", err)
	}
	return nil
}

// MarkSyncedBefore 将 updated_at <= t 的所有 target 的 is_synced 设为 true。
// 在 SyncLLMTargets 完成后调用，t 为同步开始前的时间戳，
// 确保同步期间发生的新写操作不会被误标记为已同步。
func (r *LLMTargetRepo) MarkSyncedBefore(t time.Time) error {
	if err := r.db.Model(&LLMTarget{}).Where("updated_at <= ?", t).
		Update("is_synced", true).Error; err != nil {
		r.logger.Warn("failed to mark targets synced", zap.Error(err))
		return fmt.Errorf("mark synced before: %w", err)
	}
	return nil
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

// URLExists 检查 URL 是否已存在。URL 现为全局唯一约束，此方法检查 URL 是否被占用。
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

// ComboExists 已弃用：URL 现为全局唯一，直接调用 URLExists 即可。
// Deprecated: use URLExists instead.
func (r *LLMTargetRepo) ComboExists(url string, _ *string) (bool, error) {
	return r.URLExists(url)
}

// Seed 仅在 URL 不存在时插入 target。已存在则跳过，保留 WebUI 修改。
// 用于配置文件启动同步：配置文件是种子，不是权威源。
// 与 Upsert 不同的是，Seed 优先保留已存在的记录，不覆盖。
func (r *LLMTargetRepo) Seed(target *LLMTarget) error {
	exists, err := r.URLExists(target.URL)
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

// Update 更新 LLM target。
// IsEditable 仅用于 WebUI 层拦截，repo 层不做限制，允许 CLI 等管理工具强制修改。
func (r *LLMTargetRepo) Update(target *LLMTarget) error {
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

// Delete 删除 LLM target。
// IsEditable 仅用于 WebUI 层拦截，repo 层不做限制，允许 CLI 等管理工具强制删除。
func (r *LLMTargetRepo) Delete(id string) error {
	target, err := r.GetByID(id)
	if err != nil {
		return err
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
// 按 URL 查重（URL 现为全局唯一），已存在则更新，不存在则新增。
func (r *LLMTargetRepo) Upsert(target *LLMTarget) error {
	// 按 URL 查重
	existing, err := r.GetByURL(target.URL)
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
	URL string
}

// DeleteConfigTargetsNotInList 删除不在列表中的配置文件来源的 targets。
// 按 URL 匹配（URL 现为全局唯一），精确删除已从配置文件移除的条目。
func (r *LLMTargetRepo) DeleteConfigTargetsNotInList(keepKeys []ConfigTargetKey) (int, error) {
	var configTargets []*LLMTarget
	if err := r.db.Where("source = ?", "config").Find(&configTargets).Error; err != nil {
		return 0, fmt.Errorf("list config targets: %w", err)
	}

	keepSet := make(map[string]struct{}, len(keepKeys))
	for _, k := range keepKeys {
		keepSet[k.URL] = struct{}{}
	}

	deleted := 0
	for _, t := range configTargets {
		if _, keep := keepSet[t.URL]; keep {
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
