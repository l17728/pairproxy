package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

const tokenFileName = "token.json"

// TokenFile 存储在本地的 token 信息（c-proxy 使用）
type TokenFile struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ServerAddr   string    `json:"server_addr"` // 登录时使用的 s-proxy 地址
	Username     string    `json:"username"`     // 登录用户名（展示用）
}

// TokenStore 本地 token 文件的读写接口实现
type TokenStore struct {
	logger           *zap.Logger
	refreshThreshold time.Duration // 提前多久视为过期，默认 30min
}

// NewTokenStore 创建 TokenStore
func NewTokenStore(logger *zap.Logger, refreshThreshold time.Duration) *TokenStore {
	if refreshThreshold == 0 {
		refreshThreshold = 30 * time.Minute
	}
	return &TokenStore{
		logger:           logger.Named("token_store"),
		refreshThreshold: refreshThreshold,
	}
}

// DefaultTokenDir 返回跨平台默认 token 目录
// Windows: %APPDATA%\pairproxy
// Linux:   ~/.config/pairproxy
func DefaultTokenDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".pairproxy")
	}
	return filepath.Join(base, "pairproxy")
}

// tokenPath 返回 token 文件完整路径
func tokenPath(dir string) string {
	return filepath.Join(dir, tokenFileName)
}

// Load 从指定目录加载 token.json
// 文件不存在时返回 nil, nil（非 error）
func (s *TokenStore) Load(dir string) (*TokenFile, error) {
	path := tokenPath(dir)
	s.logger.Debug("loading token file", zap.String("path", path))

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.logger.Debug("token file not found", zap.String("path", path))
			return nil, nil
		}
		s.logger.Error("failed to read token file",
			zap.String("path", path),
			zap.Error(err),
		)
		return nil, fmt.Errorf("read token file %q: %w", path, err)
	}

	var tf TokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		s.logger.Error("failed to parse token file",
			zap.String("path", path),
			zap.Error(err),
		)
		return nil, fmt.Errorf("parse token file %q: %w", path, err)
	}

	s.logger.Debug("token file loaded",
		zap.String("server_addr", tf.ServerAddr),
		zap.Time("expires_at", tf.ExpiresAt),
	)
	return &tf, nil
}

// Save 将 token 写入文件，并尽力设置 0600 权限（Windows 忽略权限错误）
func (s *TokenStore) Save(dir string, tf *TokenFile) error {
	// 确保目录存在
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.logger.Error("failed to create token dir",
			zap.String("dir", dir),
			zap.Error(err),
		)
		return fmt.Errorf("create token dir %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	path := tokenPath(dir)
	if err := os.WriteFile(path, data, 0600); err != nil {
		s.logger.Error("failed to write token file",
			zap.String("path", path),
			zap.Error(err),
		)
		return fmt.Errorf("write token file %q: %w", path, err)
	}

	// 显式 chmod（Windows 下 WriteFile 的 0600 可能不生效，再次尝试）
	// 忽略错误：Windows 不支持 Unix 权限模型
	_ = os.Chmod(path, 0600)

	s.logger.Info("token file saved",
		zap.String("path", path),
		zap.String("server_addr", tf.ServerAddr),
		zap.Time("expires_at", tf.ExpiresAt),
	)
	return nil
}

// Delete 删除本地 token 文件（logout 时调用）
func (s *TokenStore) Delete(dir string) error {
	path := tokenPath(dir)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Error("failed to delete token file",
			zap.String("path", path),
			zap.Error(err),
		)
		return fmt.Errorf("delete token file %q: %w", path, err)
	}
	s.logger.Info("token file deleted", zap.String("path", path))
	return nil
}

// IsValid 检查 access_token 是否有效
// 判断标准：token 非空 且 未达到 refreshThreshold 前的过期时间
func (s *TokenStore) IsValid(tf *TokenFile) bool {
	if tf == nil || tf.AccessToken == "" {
		s.logger.Debug("token is nil or access_token empty")
		return false
	}
	// 提前 refreshThreshold 视为将过期
	deadline := tf.ExpiresAt.Add(-s.refreshThreshold)
	remaining := time.Until(deadline)
	valid := remaining > 0

	s.logger.Debug("token validity check",
		zap.Time("expires_at", tf.ExpiresAt),
		zap.Duration("remaining_before_refresh", remaining),
		zap.Bool("valid", valid),
	)
	return valid
}

// NeedsRefresh 检查 access_token 是否需要刷新（即将过期但尚未过期）
func (s *TokenStore) NeedsRefresh(tf *TokenFile) bool {
	if tf == nil || tf.AccessToken == "" || tf.RefreshToken == "" {
		return false
	}
	now := time.Now()
	// 在 refreshThreshold 内：需要刷新
	// 已完全过期：也需要刷新（靠 refresh_token 换新）
	return now.After(tf.ExpiresAt.Add(-s.refreshThreshold))
}
