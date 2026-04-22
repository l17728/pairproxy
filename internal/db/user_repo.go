package db

import (
	"errors"
	"fmt"
	"strings"
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
		// 检查是否为唯一约束冲突
		if isDuplicateKeyError(err) {
			r.logger.Warn("user already exists",
				zap.String("username", u.Username),
				zap.String("auth_provider", u.AuthProvider),
			)
			return fmt.Errorf("user already exists: username=%q, provider=%q", u.Username, u.AuthProvider)
		}
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

// GetByUsername ⚠️ 已弃用：混合认证模式下 username 不再全局唯一。改用 GetByUsernameAndProvider。
// 此方法仅供 backward compatibility 保留。
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

// ListByUsername 返回所有拥有该用户名的用户（可能来自不同认证提供商）。
// 在混合认证场景中，若 len > 1 则存在歧义，调用方应要求提供 provider 进行区分。
func (r *UserRepo) ListByUsername(username string) ([]User, error) {
	var users []User
	if err := r.db.Preload("Group").Where("username = ?", username).Find(&users).Error; err != nil {
		r.logger.Error("failed to list users by username",
			zap.String("username", username),
			zap.Error(err),
		)
		return nil, fmt.Errorf("list users by username %q: %w", username, err)
	}
	// 记录歧义情况：同一用户名对应多个认证提供商
	if len(users) > 1 {
		providers := make([]string, len(users))
		for i, u := range users {
			providers[i] = u.AuthProvider
		}
		r.logger.Warn("ambiguous username found in multiple auth providers",
			zap.String("username", username),
			zap.Int("count", len(users)),
			zap.Strings("providers", providers),
		)
	}
	r.logger.Debug("users listed by username",
		zap.String("username", username),
		zap.Int("count", len(users)),
	)
	return users, nil
}

// GetByUsernameAndProvider 按用户名和认证提供商查询（复合唯一约束）。
// 在混合认证模式下（local + ldap），同一 username 可能对应不同 provider 的用户。
func (r *UserRepo) GetByUsernameAndProvider(username, provider string) (*User, error) {
	var u User
	err := r.db.Preload("Group").
		Where("username = ? AND auth_provider = ?", username, provider).
		First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.logger.Debug("user not found by username and provider",
				zap.String("username", username),
				zap.String("provider", provider),
			)
			return nil, nil
		}
		r.logger.Error("failed to get user by username and provider",
			zap.String("username", username),
			zap.String("provider", provider),
			zap.Error(err),
		)
		return nil, fmt.Errorf("get user %q (provider=%q): %w", username, provider, err)
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

// UpdatePassword 更新用户密码 hash，同时撤销旧版 keygenSecret 派生的 Key（legacy_key_revoked=true）。
// 用户主动改密后，旧版共享密钥派生的 Key 不应继续有效。
func (r *UserRepo) UpdatePassword(id string, hash string) error {
	result := r.db.Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"password_hash":      hash,
		"legacy_key_revoked": true,
	})
	if result.Error != nil {
		r.logger.Error("failed to update password",
			zap.String("user_id", id),
			zap.Error(result.Error),
		)
		return fmt.Errorf("update password for user %q: %w", id, result.Error)
	}
	r.logger.Info("user password updated, legacy key revoked", zap.String("user_id", id))
	return nil
}

// GetByExternalID 按外部系统 ID 和认证提供者查询用户（LDAP JIT 配置用）。
// 未找到时返回 nil, nil。
func (r *UserRepo) GetByExternalID(provider, externalID string) (*User, error) {
	var u User
	// 查询复合唯一约束 (auth_provider, external_id)
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

// ListActive 返回所有 is_active=true 的用户列表，用于 API Key 验证遍历。
func (r *UserRepo) ListActive() ([]User, error) {
	var users []User
	if err := r.db.Where("is_active = ?", true).Find(&users).Error; err != nil {
		r.logger.Error("failed to list active users", zap.Error(err))
		return nil, fmt.Errorf("list active users: %w", err)
	}
	r.logger.Debug("listed active users", zap.Int("count", len(users)))
	return users, nil
}

// ListAll 返回所有用户（含关联 Group），用于配置快照生成。
func (r *UserRepo) ListAll() ([]User, error) {
	return r.ListByGroup("")
}

// List 列出所有分组
func (r *GroupRepo) List() ([]Group, error) {
	var groups []Group
	if err := r.db.Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// isDuplicateKeyError 检查是否为数据库唯一约束冲突错误
func isDuplicateKeyError(err error) bool {
	// SQLite error code: 1555 = SQLITE_CONSTRAINT_UNIQUE
	// MySQL error code: 1062 = ER_DUP_ENTRY
	// PostgreSQL: "duplicate key" in error message
	errStr := err.Error()
	return strings.Contains(errStr, "UNIQUE constraint failed") ||
		strings.Contains(errStr, "Duplicate entry") ||
		strings.Contains(errStr, "duplicate key")
}
