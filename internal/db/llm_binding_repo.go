package db

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// LLMBindingRepo 管理 LLMBinding 记录（用户/分组 ↔ LLM target 绑定）。
type LLMBindingRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewLLMBindingRepo 创建 LLMBindingRepo。
func NewLLMBindingRepo(db *gorm.DB, logger *zap.Logger) *LLMBindingRepo {
	return &LLMBindingRepo{
		db:     db,
		logger: logger.Named("llm_binding_repo"),
	}
}

// Set 创建或替换**用户级**绑定（1:1 语义）。
// 同一 userID 的旧绑定会先被删除，再创建新绑定，保证用户级绑定始终至多一条。
// userID 必须非 nil；分组级多绑定请使用 AddGroupBinding。
func (r *LLMBindingRepo) Set(targetID string, userID, groupID *string) error {
	if userID == nil && groupID == nil {
		return fmt.Errorf("llm_binding: userID and groupID cannot both be nil")
	}
	if userID == nil {
		return fmt.Errorf("llm_binding: Set() is for user-level bindings only; use AddGroupBinding for group bindings")
	}

	// 查 target URL 冗余写入（便于直接读库）；找不到时回退用 targetID 本身（URL-as-ID 场景）
	targetURL := targetID
	var tgt LLMTarget
	if err := r.db.Where("id = ?", targetID).First(&tgt).Error; err == nil {
		targetURL = tgt.URL
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		// 删除已有的同维度绑定
		if userID != nil {
			if err := tx.Where("user_id = ?", *userID).Delete(&LLMBinding{}).Error; err != nil {
				return fmt.Errorf("delete old user binding: %w", err)
			}
		} else {
			if err := tx.Where("group_id = ?", *groupID).Delete(&LLMBinding{}).Error; err != nil {
				return fmt.Errorf("delete old group binding: %w", err)
			}
		}

		// 创建新绑定
		b := &LLMBinding{
			ID:        uuid.NewString(),
			TargetID:  targetID,
			TargetURL: targetURL,
			UserID:    userID,
			GroupID:   groupID,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(b).Error; err != nil {
			return fmt.Errorf("create llm binding: %w", err)
		}

		r.logger.Info("llm binding set",
			zap.String("target_id", targetID),
			zap.Any("user_id", userID),
			zap.Any("group_id", groupID),
		)
		return nil
	})
}

// FindForUser 查找用户的 LLM target 绑定，用户级优先于分组级。
// 返回 (targetID, true, nil) 若找到；("", false, nil) 若无绑定；("", false, err) 若 DB 错误。
// 防御性检查：Set() 应通过 delete-then-insert 保证每个 userID/groupID 至多一条绑定；
// 若发现多条，记录 Error 日志并取第一条（保证行为确定性）。
func (r *LLMBindingRepo) FindForUser(userID, groupID string) (targetID string, found bool, err error) {
	// 1. 先查用户级绑定
	if userID != "" {
		var bindings []LLMBinding
		if dbErr := r.db.Where("user_id = ?", userID).Find(&bindings).Error; dbErr != nil {
			return "", false, fmt.Errorf("find user llm binding: %w", dbErr)
		}
		if len(bindings) > 1 {
			r.logger.Error("data integrity violation: multiple user bindings found",
				zap.String("user_id", userID),
				zap.Int("count", len(bindings)),
			)
		}
		if len(bindings) > 0 {
			r.logger.Debug("llm binding found (user)", zap.String("user_id", userID), zap.String("target_id", bindings[0].TargetID))
			return bindings[0].TargetID, true, nil
		}
	}

	// 2. 再查分组级绑定（单条：兼容旧路径；多条：由调用方通过 FindAllForGroup 处理智能路由）
	if groupID != "" {
		var bindings []LLMBinding
		if dbErr := r.db.Where("group_id = ?", groupID).Order("created_at ASC").Find(&bindings).Error; dbErr != nil {
			return "", false, fmt.Errorf("find group llm binding: %w", dbErr)
		}
		if len(bindings) > 1 {
			// 分组多绑定属于合法状态，调用方应改用 FindAllForGroup 走智能路由分支
			r.logger.Debug("group has multiple bindings; caller should use FindAllForGroup for smart routing",
				zap.String("group_id", groupID),
				zap.Int("count", len(bindings)),
			)
		}
		if len(bindings) > 0 {
			r.logger.Debug("llm binding found (group)", zap.String("group_id", groupID), zap.String("target_id", bindings[0].TargetID))
			return bindings[0].TargetID, true, nil
		}
	}

	return "", false, nil
}

// List 返回全部 LLMBinding 记录（按创建时间升序）。
func (r *LLMBindingRepo) List() ([]LLMBinding, error) {
	var bindings []LLMBinding
	if err := r.db.Order("created_at ASC").Find(&bindings).Error; err != nil {
		return nil, fmt.Errorf("list llm bindings: %w", err)
	}
	r.logger.Debug("listed llm bindings", zap.Int("count", len(bindings)))
	return bindings, nil
}

// Delete 按 ID 删除绑定。
func (r *LLMBindingRepo) Delete(id string) error {
	if err := r.db.Delete(&LLMBinding{}, "id = ?", id).Error; err != nil {
		return fmt.Errorf("delete llm binding %q: %w", id, err)
	}
	r.logger.Info("llm binding deleted", zap.String("id", id))
	return nil
}

// AddGroupBinding 为分组追加一条 LLM target 绑定（1:N 语义）。
// 约束：同一分组内所有已绑定 target 的 provider 必须与新 target 一致，否则返回错误。
// 同一 (group_id, target_id) 重复调用是幂等的（已绑定则直接返回 nil）。
func (r *LLMBindingRepo) AddGroupBinding(targetID, groupID string) error {
	if targetID == "" || groupID == "" {
		return fmt.Errorf("llm_binding: targetID and groupID must not be empty")
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		// 查新 target 的 provider
		var newTarget LLMTarget
		if err := tx.Where("id = ?", targetID).First(&newTarget).Error; err != nil {
			return fmt.Errorf("llm_binding: target %q not found: %w", targetID, err)
		}

		// 检查幂等：已存在相同绑定则跳过
		var existing []LLMBinding
		if err := tx.Where("group_id = ?", groupID).Find(&existing).Error; err != nil {
			return fmt.Errorf("llm_binding: query existing group bindings: %w", err)
		}
		for _, b := range existing {
			if b.TargetID == targetID {
				r.logger.Debug("group binding already exists, skipping",
					zap.String("group_id", groupID),
					zap.String("target_id", targetID),
				)
				return nil
			}
		}

		// provider 一致性校验
		if len(existing) > 0 {
			var firstTarget LLMTarget
			if err := tx.Where("id = ?", existing[0].TargetID).First(&firstTarget).Error; err == nil {
				if firstTarget.Provider != newTarget.Provider {
					return fmt.Errorf(
						"llm_binding: provider conflict — group %q already has provider %q, cannot add target with provider %q",
						groupID, firstTarget.Provider, newTarget.Provider,
					)
				}
			}
		}

		// 追加新绑定
		b := &LLMBinding{
			ID:        uuid.NewString(),
			TargetID:  targetID,
			TargetURL: newTarget.URL,
			GroupID:   &groupID,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(b).Error; err != nil {
			return fmt.Errorf("create group llm binding: %w", err)
		}

		r.logger.Info("group llm binding added",
			zap.String("target_id", targetID),
			zap.String("group_id", groupID),
			zap.String("provider", newTarget.Provider),
		)
		return nil
	})
}

// RemoveGroupBinding 按绑定主键删除单条分组绑定。
func (r *LLMBindingRepo) RemoveGroupBinding(bindingID string) error {
	if bindingID == "" {
		return fmt.Errorf("llm_binding: bindingID must not be empty")
	}
	if err := r.db.Delete(&LLMBinding{}, "id = ?", bindingID).Error; err != nil {
		return fmt.Errorf("remove group llm binding %q: %w", bindingID, err)
	}
	r.logger.Info("group llm binding removed", zap.String("binding_id", bindingID))
	return nil
}

// FindAllForGroup 返回指定分组的全部 LLM target ID 列表（按创建时间升序）。
// 用于分组多绑定场景的智能路由。
func (r *LLMBindingRepo) FindAllForGroup(groupID string) ([]string, error) {
	if groupID == "" {
		return nil, nil
	}
	var bindings []LLMBinding
	if err := r.db.Where("group_id = ?", groupID).Order("created_at ASC").Find(&bindings).Error; err != nil {
		return nil, fmt.Errorf("find group llm bindings: %w", err)
	}
	targetIDs := make([]string, 0, len(bindings))
	for _, b := range bindings {
		targetIDs = append(targetIDs, b.TargetID)
	}
	return targetIDs, nil
}

// EvenDistribute 将 userIDs 中**尚无用户级绑定**的用户轮询分配到 targetIDs。
// 已有用户级绑定的用户（如直连用户手动设置的固定绑定）会被跳过，不受影响。
// user[i] → targetIDs[i % len(targetIDs)]，在单个事务中完成。
// targetIDs 为空时返回 error。
func (r *LLMBindingRepo) EvenDistribute(userIDs []string, targetIDs []string) error {
	if len(targetIDs) == 0 {
		return fmt.Errorf("llm_binding: targetIDs must not be empty")
	}
	if len(userIDs) == 0 {
		r.logger.Info("even distribute: no users to distribute")
		return nil
	}

	// 查出已有用户级绑定的 userID 集合，distribute 跳过这些用户
	var existingBindings []LLMBinding
	if err := r.db.Where("user_id IN ?", userIDs).Find(&existingBindings).Error; err != nil {
		return fmt.Errorf("query existing user bindings: %w", err)
	}
	alreadyBound := make(map[string]bool, len(existingBindings))
	for _, b := range existingBindings {
		if b.UserID != nil {
			alreadyBound[*b.UserID] = true
		}
	}

	// 过滤出无绑定的用户
	var toAssign []string
	for _, uid := range userIDs {
		if !alreadyBound[uid] {
			toAssign = append(toAssign, uid)
		}
	}

	if len(toAssign) == 0 {
		r.logger.Info("even distribute: all users already have bindings, nothing to do",
			zap.Int("skipped", len(userIDs)),
		)
		return nil
	}

	r.logger.Info("even distribute: skipping users with existing bindings",
		zap.Int("total", len(userIDs)),
		zap.Int("skipped", len(alreadyBound)),
		zap.Int("to_assign", len(toAssign)),
	)

	// 批量查 targetID → URL（冗余写入）
	targetURLMap := make(map[string]string, len(targetIDs))
	var tgts []LLMTarget
	if err := r.db.Where("id IN ?", targetIDs).Find(&tgts).Error; err == nil {
		for _, t := range tgts {
			targetURLMap[t.ID] = t.URL
		}
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for i, uid := range toAssign {
			targetID := targetIDs[i%len(targetIDs)]
			targetURL := targetURLMap[targetID]
			if targetURL == "" {
				targetURL = targetID // URL-as-ID 兜底
			}
			uidCopy := uid
			b := &LLMBinding{
				ID:        uuid.NewString(),
				TargetID:  targetID,
				TargetURL: targetURL,
				UserID:    &uidCopy,
				CreatedAt: now,
			}
			if err := tx.Create(b).Error; err != nil {
				return fmt.Errorf("create binding for user %q: %w", uid, err)
			}
		}

		r.logger.Info("even distribution completed",
			zap.Int("assigned", len(toAssign)),
			zap.Int("targets", len(targetIDs)),
		)
		return nil
	})
}
