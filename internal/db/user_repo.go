package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// UserRepo 用户仓库
type UserRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewUserRepo 创建 UserRepo
func NewUserRepo(db *gorm.DB, logger *zap.Logger) *UserRepo {
	return &UserRepo{db: db, logger: logger.Named("user_repo")}
}

// Create 创建用户（自动生成 UUID）
func (r *UserRepo) Create(u *User) error {
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	if err := r.db.Create(u).Error; err != nil {
		r.logger.Error("failed to create user",
			zap.String("username", u.Username),
			zap.Error(err),
		)
		return fmt.Errorf("create user %q: %w", u.Username, err)
	}
	groupID := ""
	if u.GroupID != nil {
		groupID = *u.GroupID
	}
	r.logger.Info("user created",
		zap.String("user_id", u.ID),
		zap.String("username", u.Username),
		zap.String("group_id", groupID),
	)
	return nil
}

// GetByUsername 按用户名查询（含关联 Group）
func (r *UserRepo) GetByUsername(username string) (*User, error) {
	var u User
	err := r.db.Preload("Group").Where("username = ?", username).First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.logger.Debug("user not found", zap.String("username", username))
			return nil, nil
		}
		r.logger.Error("failed to get user by username",
			zap.String("username", username),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get user %q: %w", username, err)
	}
	return &u, nil
}

// GetByID 按 ID 查询（含关联 Group）
func (r *UserRepo) GetByID(id string) (*User, error) {
	var u User
	err := r.db.Preload("Group").Where("id = ?", id).First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.logger.Debug("user not found", zap.String("user_id", id))
			return nil, nil
		}
		r.logger.Error("failed to get user by id",
			zap.String("user_id", id),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get user by id %q: %w", id, err)
	}
	return &u, nil
}

// SetActive 设置用户启用/禁用状态
func (r *UserRepo) SetActive(id string, active bool) error {
	result := r.db.Model(&User{}).Where("id = ?", id).Update("is_active", active)
	if result.Error != nil {
		r.logger.Error("failed to set user active",
			zap.String("user_id", id),
			zap.Bool("active", active),
			zap.Error(result.Error),
		)
		return fmt.Errorf("set user %q active=%v: %w", id, active, result.Error)
	}
	r.logger.Info("user active status updated",
		zap.String("user_id", id),
		zap.Bool("active", active),
	)
	return nil
}

// UpdateLastLogin 更新最后登录时间
func (r *UserRepo) UpdateLastLogin(id string, at time.Time) error {
	if err := r.db.Model(&User{}).Where("id = ?", id).Update("last_login_at", at).Error; err != nil {
		r.logger.Warn("failed to update last_login_at",
			zap.String("user_id", id),
			zap.Error(err),
		)
		// 非致命错误，不影响登录流程
		return nil
	}
	return nil
}

// UpdatePassword 更新用户密码 hash
func (r *UserRepo) UpdatePassword(id string, hash string) error {
	result := r.db.Model(&User{}).Where("id = ?", id).Update("password_hash", hash)
	if result.Error != nil {
		r.logger.Error("failed to update password",
			zap.String("user_id", id),
			zap.Error(result.Error),
		)
		return fmt.Errorf("update password for user %q: %w", id, result.Error)
	}
	r.logger.Info("user password updated", zap.String("user_id", id))
	return nil
}

// GetByExternalID 按外部系统 ID 和认证提供者查询用户（LDAP JIT 配置用）。
// 未找到时返回 nil, nil。
func (r *UserRepo) GetByExternalID(provider, externalID string) (*User, error) {
	var u User
	err := r.db.Preload("Group").
		Where("auth_provider = ? AND external_id = ?", provider, externalID).
		First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.logger.Debug("user not found by external id",
				zap.String("provider", provider),
				zap.String("external_id", externalID),
			)
			return nil, nil
		}
		r.logger.Error("failed to get user by external id",
			zap.String("provider", provider),
			zap.String("external_id", externalID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get user by external id (%s, %s): %w", provider, externalID, err)
	}
	return &u, nil
}

// SetGroup 更新用户所属分组（groupID=nil 表示从分组移除）
func (r *UserRepo) SetGroup(userID string, groupID *string) error {
	updates := map[string]interface{}{"group_id": groupID}
	if err := r.db.Model(&User{}).Where("id = ?", userID).Updates(updates).Error; err != nil {
		r.logger.Error("failed to set user group",
			zap.String("user_id", userID),
			zap.Any("group_id", groupID),
			zap.Error(err),
		)
		return fmt.Errorf("set group for user %q: %w", userID, err)
	}
	r.logger.Info("user group updated",
		zap.String("user_id", userID),
		zap.Any("group_id", groupID),
	)
	return nil
}

// ListByGroup 按分组列出用户（groupID="" 表示列出所有用户）
func (r *UserRepo) ListByGroup(groupID string) ([]User, error) {
	var users []User
	query := r.db.Preload("Group")
	if groupID != "" {
		query = query.Where("group_id = ?", groupID)
	}
	if err := query.Find(&users).Error; err != nil {
		r.logger.Error("failed to list users",
			zap.String("group_id", groupID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("list users: %w", err)
	}
	r.logger.Debug("users listed",
		zap.String("group_id", groupID),
		zap.Int("count", len(users)),
	)
	return users, nil
}

// ---------------------------------------------------------------------------
// GroupRepo 分组仓库
// ---------------------------------------------------------------------------

// GroupRepo 分组仓库
type GroupRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewGroupRepo 创建 GroupRepo
func NewGroupRepo(db *gorm.DB, logger *zap.Logger) *GroupRepo {
	return &GroupRepo{db: db, logger: logger.Named("group_repo")}
}

// Create 创建分组
func (r *GroupRepo) Create(g *Group) error {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	if err := r.db.Create(g).Error; err != nil {
		r.logger.Error("failed to create group",
			zap.String("name", g.Name),
			zap.Error(err),
		)
		return fmt.Errorf("create group %q: %w", g.Name, err)
	}
	r.logger.Info("group created",
		zap.String("group_id", g.ID),
		zap.String("name", g.Name),
	)
	return nil
}

// GetByID 按 ID 查询分组
func (r *GroupRepo) GetByID(id string) (*Group, error) {
	var g Group
	err := r.db.Where("id = ?", id).First(&g).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get group by id %q: %w", id, err)
	}
	return &g, nil
}

// GetByName 按名称查询分组
func (r *GroupRepo) GetByName(name string) (*Group, error) {
	var g Group
	err := r.db.Where("name = ?", name).First(&g).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get group by name %q: %w", name, err)
	}
	return &g, nil
}

// SetQuota 设置分组配额（nil 表示无限制）
func (r *GroupRepo) SetQuota(id string, daily, monthly *int64, rpm *int, maxReqTokens *int64, concurrent *int) error {
	updates := map[string]interface{}{
		"daily_token_limit":     daily,
		"monthly_token_limit":   monthly,
		"requests_per_minute":   rpm,
		"max_tokens_per_request": maxReqTokens,
		"concurrent_requests":   concurrent,
	}
	if err := r.db.Model(&Group{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		r.logger.Error("failed to set group quota",
			zap.String("group_id", id),
			zap.Error(err),
		)
		return fmt.Errorf("set quota for group %q: %w", id, err)
	}
	r.logger.Info("group quota updated",
		zap.String("group_id", id),
		zap.Any("daily_limit", daily),
		zap.Any("monthly_limit", monthly),
		zap.Any("rpm", rpm),
		zap.Any("max_tokens_per_request", maxReqTokens),
		zap.Any("concurrent_requests", concurrent),
	)
	return nil
}

// Delete 删除分组（若分组内仍有用户则拒绝，除非 force=true）
func (r *GroupRepo) Delete(id string, force bool) error {
	if !force {
		var count int64
		if err := r.db.Model(&User{}).Where("group_id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("check group members: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("group has %d user(s); use --force to delete anyway (users will become ungrouped)", count)
		}
	}
	// 若 force，先将组内用户的 group_id 清空
	if force {
		if err := r.db.Model(&User{}).Where("group_id = ?", id).Update("group_id", nil).Error; err != nil {
			return fmt.Errorf("unassign users from group: %w", err)
		}
	}
	if err := r.db.Delete(&Group{}, "id = ?", id).Error; err != nil {
		r.logger.Error("failed to delete group", zap.String("group_id", id), zap.Error(err))
		return fmt.Errorf("delete group %q: %w", id, err)
	}
	r.logger.Info("group deleted", zap.String("group_id", id), zap.Bool("force", force))
	return nil
}

// GetActiveUsers 获取最近 N 天有活动的用户列表（按最后活动时间倒序）
func (r *UserRepo) GetActiveUsers(days int) ([]User, error) {
	cutoff := time.Now().AddDate(0, 0, -days)
	var users []User

	// 从 usage_logs 表查询有活动的用户，按最后活动时间倒序
	err := r.db.Preload("Group").
		Joins("INNER JOIN (SELECT DISTINCT user_id, MAX(created_at) as last_active FROM usage_logs WHERE created_at >= ? GROUP BY user_id) ul ON users.id = ul.user_id", cutoff).
		Order("ul.last_active DESC").
		Find(&users).Error

	if err != nil {
		r.logger.Error("failed to get active users",
			zap.Int("days", days),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get active users (days=%d): %w", days, err)
	}

	r.logger.Debug("active users retrieved",
		zap.Int("days", days),
		zap.Int("count", len(users)),
	)
	return users, nil
}

// List 列出所有分组
func (r *GroupRepo) List() ([]Group, error) {
	var groups []Group
	if err := r.db.Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}
