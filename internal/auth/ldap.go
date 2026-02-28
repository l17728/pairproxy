package auth

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/go-ldap/ldap/v3"
	"go.uber.org/zap"
)

// LDAPConfig LDAP 服务器连接配置。
type LDAPConfig struct {
	// ServerAddr LDAP 服务器地址（host:port，如 "ldap.example.com:389"）
	ServerAddr string
	// BaseDN 搜索根（如 "dc=example,dc=com"）
	BaseDN string
	// BindDN 服务账户 DN（用于搜索用户；空则匿名绑定）
	BindDN string
	// BindPassword 服务账户密码（支持 ${ENV_VAR}）
	BindPassword string
	// UserFilter 搜索过滤器（%s 替换为转义后的用户名），如 "(uid=%s)"
	UserFilter string
	// UseTLS 是否使用 LDAPS（端口 636）
	UseTLS bool
}

// ldapConn 最小化 LDAP 连接接口，便于单元测试中注入 mock。
type ldapConn interface {
	Bind(username, password string) error
	Search(searchRequest *ldap.SearchRequest) (*ldap.SearchResult, error)
	StartTLS(config *tls.Config) error
	Close() error
}

// ldapDialFn 连接工厂函数类型。
type ldapDialFn func(serverAddr string, useTLS bool) (ldapConn, error)

// defaultDial 生产环境使用的真实 LDAP 连接工厂。
func defaultDial(serverAddr string, useTLS bool) (ldapConn, error) {
	scheme := "ldap"
	if useTLS {
		scheme = "ldaps"
	}
	conn, err := ldap.DialURL(fmt.Sprintf("%s://%s", scheme, serverAddr))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// LDAPProvider LDAP 认证提供者。
//
// 认证流程：
//  1. 以服务账户（BindDN）绑定（或匿名绑定）
//  2. 根据 UserFilter 搜索用户 DN
//  3. 以用户 DN + 明文密码重新绑定验证
type LDAPProvider struct {
	cfg    LDAPConfig
	logger *zap.Logger
	dial   ldapDialFn
}

// NewLDAPProvider 创建 LDAPProvider。
func NewLDAPProvider(logger *zap.Logger, cfg LDAPConfig) *LDAPProvider {
	return &LDAPProvider{
		cfg:    cfg,
		logger: logger.Named("ldap_provider"),
		dial:   defaultDial,
	}
}

// Authenticate 通过 LDAP 验证用户名和密码，成功返回 *ProviderUser。
func (p *LDAPProvider) Authenticate(ctx context.Context, username, password string) (*ProviderUser, error) {
	p.logger.Debug("ldap authenticate start", zap.String("username", username))

	// 1. 建立连接
	conn, err := p.dial(p.cfg.ServerAddr, p.cfg.UseTLS)
	if err != nil {
		p.logger.Error("ldap: dial failed",
			zap.String("server", p.cfg.ServerAddr),
			zap.Error(err),
		)
		return nil, fmt.Errorf("ldap dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// 2. 服务账户绑定（用于搜索用户 DN）
	if p.cfg.BindDN != "" {
		if err := conn.Bind(p.cfg.BindDN, p.cfg.BindPassword); err != nil {
			p.logger.Error("ldap: service bind failed",
				zap.String("bind_dn", p.cfg.BindDN),
				zap.Error(err),
			)
			return nil, fmt.Errorf("ldap service bind: %w", err)
		}
	}

	// 3. 搜索用户 DN
	filter := fmt.Sprintf(p.cfg.UserFilter, ldap.EscapeFilter(username))
	searchReq := ldap.NewSearchRequest(
		p.cfg.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1, 0, false,
		filter,
		[]string{"dn", "uid", "cn", "mail"},
		nil,
	)
	result, err := conn.Search(searchReq)
	if err != nil {
		p.logger.Warn("ldap: search failed",
			zap.String("username", username),
			zap.String("filter", filter),
			zap.Error(err),
		)
		return nil, fmt.Errorf("ldap search: %w", err)
	}
	if len(result.Entries) == 0 {
		p.logger.Debug("ldap: user not found", zap.String("username", username))
		return nil, fmt.Errorf("user not found in LDAP directory")
	}

	userEntry := result.Entries[0]
	userDN := userEntry.DN

	// 4. 以用户 DN + 明文密码重新绑定（验证密码）
	if err := conn.Bind(userDN, password); err != nil {
		p.logger.Debug("ldap: user bind failed (wrong password?)",
			zap.String("username", username),
			zap.String("dn", userDN),
		)
		return nil, fmt.Errorf("ldap authentication failed")
	}

	// 5. 提取用户属性
	uid := userEntry.GetAttributeValue("uid")
	if uid == "" {
		uid = username // uid 属性不存在时回退到用户名
	}
	email := userEntry.GetAttributeValue("mail")

	p.logger.Info("ldap: authentication successful",
		zap.String("username", username),
		zap.String("uid", uid),
		zap.String("dn", userDN),
	)

	return &ProviderUser{
		ExternalID:   uid,
		Username:     username,
		Email:        email,
		AuthProvider: "ldap",
	}, nil
}
