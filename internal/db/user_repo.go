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

// List 列出所有分组
func (r *GroupRepo) List() ([]Group, error) {
	var groups []Group
	if err := r.db.Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}
