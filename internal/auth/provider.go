package auth

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// Provider 认证提供者接口。
// 实现类：LocalProvider（本地数据库）、LDAPProvider（LDAP 服务器）。
type Provider interface {
	// Authenticate 验证用户名和密码。
	// 成功时返回 *ProviderUser；失败时返回 error（含密码错误、用户不存在等）。
	Authenticate(ctx context.Context, username, password string) (*ProviderUser, error)
}

// ProviderUser 认证成功后返回的用户信息。
type ProviderUser struct {
	// ExternalID 外部系统中的唯一标识（LDAP: uid 属性；本地: user.ID）
	ExternalID string
	// Username 用户名（用于显示和内部映射）
	Username string
	// Email 可选，从外部系统获取（LDAP mail 属性）
	Email string
	// AuthProvider 认证来源，"local" | "ldap"
	AuthProvider string
}

// ---------------------------------------------------------------------------
// LocalProvider — 基于本地数据库的认证（通过 bcrypt 验证密码）
// ---------------------------------------------------------------------------

// userLookup 最小接口，供 LocalProvider 查询用户（避免循环导入 db 包）
type userLookup interface {
	GetByUsername(username string) (localUser, error)
}

// localUser 本地用户最小接口
type localUser interface {
	GetID() string
	GetUsername() string
	GetPasswordHash() string
}

// LocalProvider 本地数据库认证提供者。
// 使用场景：用于单元测试或作为接口的参考实现；
// 生产中 AuthHandler 直接使用 userRepo 查询，LocalProvider 供外部调用。
type LocalProvider struct {
	logger *zap.Logger
	lookup func(username string) (id, hash string, found bool, err error)
}

// NewLocalProvider 创建 LocalProvider。
// lookupFn 应封装 userRepo.GetByUsername 调用并返回用户 ID、密码 hash、是否找到、错误。
func NewLocalProvider(logger *zap.Logger, lookupFn func(username string) (id, hash string, found bool, err error)) *LocalProvider {
	return &LocalProvider{
		logger: logger.Named("local_provider"),
		lookup: lookupFn,
	}
}

// Authenticate 验证本地用户的用户名和密码。
func (p *LocalProvider) Authenticate(_ context.Context, username, password string) (*ProviderUser, error) {
	id, hash, found, err := p.lookup(username)
	if err != nil {
		p.logger.Error("local provider: db lookup failed",
			zap.String("username", username),
			zap.Error(err),
		)
		return nil, fmt.Errorf("db lookup: %w", err)
	}
	if !found {
		p.logger.Debug("local provider: user not found", zap.String("username", username))
		return nil, fmt.Errorf("user not found")
	}
	if !VerifyPassword(p.logger, hash, password) {
		p.logger.Debug("local provider: password mismatch", zap.String("username", username))
		return nil, fmt.Errorf("invalid credentials")
	}
	p.logger.Debug("local provider: authentication successful", zap.String("username", username))
	return &ProviderUser{
		ExternalID:   id,
		Username:     username,
		AuthProvider: "local",
	}, nil
}
