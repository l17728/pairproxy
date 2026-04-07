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

// Set 创建或替换绑定。
// 同一 userID 或 groupID 的旧绑定会先被删除，再创建新绑定。
// userID 和 groupID 至少有一个非 nil。
func (r *LLMBindingRepo) Set(targetID string, userID, groupID *string) error {
	if userID == nil && groupID == nil {
		return fmt.Errorf("llm_binding: userID and groupID cannot both be nil")
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
func (r *LLMBindingRepo) FindForUser(userID, groupID string) (targetID string, found bool, err error) {
	// 1. 先查用户级绑定
	if userID != "" {
		var b LLMBinding
		err := r.db.Where("user_id = ?", userID).First(&b).Error
		if err == nil {
			r.logger.Debug("llm binding found (user)", zap.String("user_id", userID), zap.String("target_id", b.TargetID))
			return b.TargetID, true, nil
		}
		if err != gorm.ErrRecordNotFound {
			return "", false, fmt.Errorf("find user llm binding: %w", err)
		}
	}

	// 2. 再查分组级绑定
	if groupID != "" {
		var b LLMBinding
		err := r.db.Where("group_id = ?", groupID).First(&b).Error
		if err == nil {
			r.logger.Debug("llm binding found (group)", zap.String("group_id", groupID), zap.String("target_id", b.TargetID))
			return b.TargetID, true, nil
		}
		if err != gorm.ErrRecordNotFound {
			return "", false, fmt.Errorf("find group llm binding: %w", err)
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

	return r.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for i, uid := range toAssign {
			targetID := targetIDs[i%len(targetIDs)]
			uidCopy := uid
			b := &LLMBinding{
				ID:        uuid.NewString(),
				TargetID:  targetID,
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
