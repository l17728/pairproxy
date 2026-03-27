package db

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GroupTargetSetRepo 管理 Group-Target Set 关系
type GroupTargetSetRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewGroupTargetSetRepo 创建 GroupTargetSetRepo
func NewGroupTargetSetRepo(db *gorm.DB, logger *zap.Logger) *GroupTargetSetRepo {
	return &GroupTargetSetRepo{
		db:     db,
		logger: logger.Named("group_target_set_repo"),
	}
}

// Create 创建新的 target set
func (r *GroupTargetSetRepo) Create(set *GroupTargetSet) error {
	if set.ID == "" {
		return fmt.Errorf("target set ID cannot be empty")
	}
	if set.Name == "" {
		return fmt.Errorf("target set name cannot be empty")
	}

	set.CreatedAt = time.Now()
	set.UpdatedAt = time.Now()

	if err := r.db.Create(set).Error; err != nil {
		r.logger.Error("failed to create target set",
			zap.String("id", set.ID),
			zap.String("name", set.Name),
			zap.Error(err),
		)
		return fmt.Errorf("create target set: %w", err)
	}

	r.logger.Debug("target set created",
		zap.String("id", set.ID),
		zap.String("name", set.Name),
	)
	return nil
}

// GetByID 根据 ID 获取 target set
func (r *GroupTargetSetRepo) GetByID(id string) (*GroupTargetSet, error) {
	var set GroupTargetSet
	if err := r.db.Preload("Members").First(&set, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		r.logger.Error("failed to get target set by ID",
			zap.String("id", id),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get target set by ID: %w", err)
	}
	return &set, nil
}

// GetByName 根据名称获取 target set
func (r *GroupTargetSetRepo) GetByName(name string) (*GroupTargetSet, error) {
	var set GroupTargetSet
	if err := r.db.Preload("Members").First(&set, "name = ?", name).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		r.logger.Error("failed to get target set by name",
			zap.String("name", name),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get target set by name: %w", err)
	}
	return &set, nil
}

// GetByGroupID 根据 group ID 获取 target set
func (r *GroupTargetSetRepo) GetByGroupID(groupID string) (*GroupTargetSet, error) {
	var set GroupTargetSet
	if err := r.db.Preload("Members").First(&set, "group_id = ?", groupID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		r.logger.Error("failed to get target set by group ID",
			zap.String("group_id", groupID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get target set by group ID: %w", err)
	}
	return &set, nil
}

// GetDefault 获取默认 target set
func (r *GroupTargetSetRepo) GetDefault() (*GroupTargetSet, error) {
	var set GroupTargetSet
	if err := r.db.Preload("Members").First(&set, "is_default = ? AND group_id IS NULL", true).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		r.logger.Error("failed to get default target set", zap.Error(err))
		return nil, fmt.Errorf("get default target set: %w", err)
	}
	return &set, nil
}

// Update 更新 target set
func (r *GroupTargetSetRepo) Update(set *GroupTargetSet) error {
	if set.ID == "" {
		return fmt.Errorf("target set ID cannot be empty")
	}

	set.UpdatedAt = time.Now()

	if err := r.db.Model(set).Updates(set).Error; err != nil {
		r.logger.Error("failed to update target set",
			zap.String("id", set.ID),
			zap.Error(err),
		)
		return fmt.Errorf("update target set: %w", err)
	}

	r.logger.Debug("target set updated", zap.String("id", set.ID))
	return nil
}

// Delete 删除 target set（级联删除 members）
func (r *GroupTargetSetRepo) Delete(id string) error {
	if err := r.db.Delete(&GroupTargetSet{}, "id = ?", id).Error; err != nil {
		r.logger.Error("failed to delete target set",
			zap.String("id", id),
			zap.Error(err),
		)
		return fmt.Errorf("delete target set: %w", err)
	}

	r.logger.Debug("target set deleted", zap.String("id", id))
	return nil
}

// ListAll 列出所有 target sets
func (r *GroupTargetSetRepo) ListAll() ([]GroupTargetSet, error) {
	var sets []GroupTargetSet
	if err := r.db.Preload("Members").Find(&sets).Error; err != nil {
		r.logger.Error("failed to list all target sets", zap.Error(err))
		return nil, fmt.Errorf("list all target sets: %w", err)
	}
	return sets, nil
}

// AddMember 添加 member 到 target set
func (r *GroupTargetSetRepo) AddMember(setID string, member *GroupTargetSetMember) error {
	if setID == "" {
		return fmt.Errorf("target set ID cannot be empty")
	}
	if member.TargetURL == "" {
		return fmt.Errorf("target URL cannot be empty")
	}

	member.TargetSetID = setID
	member.CreatedAt = time.Now()
	member.HealthStatus = "unknown"

	if err := r.db.Create(member).Error; err != nil {
		r.logger.Error("failed to add member",
			zap.String("target_set_id", setID),
			zap.String("target_url", member.TargetURL),
			zap.Error(err),
		)
		return fmt.Errorf("add member: %w", err)
	}

	r.logger.Debug("member added",
		zap.String("target_set_id", setID),
		zap.String("target_url", member.TargetURL),
	)
	return nil
}

// RemoveMember 从 target set 移除 member
func (r *GroupTargetSetRepo) RemoveMember(setID string, targetURL string) error {
	if err := r.db.Delete(&GroupTargetSetMember{}, "target_set_id = ? AND target_url = ?", setID, targetURL).Error; err != nil {
		r.logger.Error("failed to remove member",
			zap.String("target_set_id", setID),
			zap.String("target_url", targetURL),
			zap.Error(err),
		)
		return fmt.Errorf("remove member: %w", err)
	}

	r.logger.Debug("member removed",
		zap.String("target_set_id", setID),
		zap.String("target_url", targetURL),
	)
	return nil
}

// UpdateMember 更新 member 的权重和优先级
func (r *GroupTargetSetRepo) UpdateMember(setID string, targetURL string, weight, priority int) error {
	if err := r.db.Model(&GroupTargetSetMember{}).
		Where("target_set_id = ? AND target_url = ?", setID, targetURL).
		Updates(map[string]interface{}{
			"weight":   weight,
			"priority": priority,
		}).Error; err != nil {
		r.logger.Error("failed to update member",
			zap.String("target_set_id", setID),
			zap.String("target_url", targetURL),
			zap.Error(err),
		)
		return fmt.Errorf("update member: %w", err)
	}

	r.logger.Debug("member updated",
		zap.String("target_set_id", setID),
		zap.String("target_url", targetURL),
		zap.Int("weight", weight),
		zap.Int("priority", priority),
	)
	return nil
}

// ListMembers 列出 target set 的所有 members
func (r *GroupTargetSetRepo) ListMembers(setID string) ([]GroupTargetSetMember, error) {
	var members []GroupTargetSetMember
	if err := r.db.Where("target_set_id = ?", setID).Find(&members).Error; err != nil {
		r.logger.Error("failed to list members",
			zap.String("target_set_id", setID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("list members: %w", err)
	}
	return members, nil
}

// TargetWithWeight 用于路由选择的 target 信息
type TargetWithWeight struct {
	URL      string
	Weight   int
	Priority int
	Healthy  bool
}

// GetAvailableTargetsForGroup 获取 Group 可用的 targets
func (r *GroupTargetSetRepo) GetAvailableTargetsForGroup(groupID string) ([]TargetWithWeight, error) {
	var members []GroupTargetSetMember
	query := r.db

	// 如果 groupID 为空，获取默认 target set
	if groupID == "" {
		query = query.Where("target_set_id IN (SELECT id FROM group_target_sets WHERE is_default = ? AND group_id IS NULL)", true)
	} else {
		query = query.Where("target_set_id IN (SELECT id FROM group_target_sets WHERE group_id = ?)", groupID)
	}

	if err := query.Where("is_active = ?", true).Find(&members).Error; err != nil {
		r.logger.Error("failed to get available targets",
			zap.String("group_id", groupID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get available targets: %w", err)
	}

	targets := make([]TargetWithWeight, len(members))
	for i, m := range members {
		targets[i] = TargetWithWeight{
			URL:      m.TargetURL,
			Weight:   m.Weight,
			Priority: m.Priority,
			Healthy:  m.HealthStatus == "healthy",
		}
	}

	return targets, nil
}
