package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplySProxyDefaults_PGAutoSetsPeer 验证 driver=postgres + role="" 时自动设为 "peer"
func TestApplySProxyDefaults_PGAutoSetsPeer(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	applySProxyDefaults(cfg)
	assert.Equal(t, "peer", cfg.Cluster.Role)
}

// TestApplySProxyDefaults_PGExplicitRoleNotOverridden 验证 driver=postgres + role="primary" 时保持原值
func TestApplySProxyDefaults_PGExplicitRoleNotOverridden(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Cluster.Role = "primary"
	applySProxyDefaults(cfg)
	assert.Equal(t, "primary", cfg.Cluster.Role)
}

// TestValidate_PeerRoleRequiresPG 验证 role="peer" + driver="sqlite" 时校验报错
func TestValidate_PeerRoleRequiresPG(t *testing.T) {
	cfg := minimalValidSProxyConfig()
	cfg.Cluster.Role = "peer"
	cfg.Database.Driver = "sqlite"
	cfg.Database.Path = "pairproxy.db"

	applySProxyDefaults(cfg)
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer")
	assert.Contains(t, err.Error(), "postgres")
}

// TestValidate_PeerRoleWithPG 验证 role="peer" + driver="postgres" + DSN 时校验通过
func TestValidate_PeerRoleWithPG(t *testing.T) {
	cfg := minimalValidSProxyConfig()
	cfg.Cluster.Role = "peer"
	cfg.Database.Driver = "postgres"
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/pairproxy"
	cfg.Database.Path = "" // PG 模式不需要 path

	applySProxyDefaults(cfg)
	err := cfg.Validate()
	require.NoError(t, err)
}

// minimalValidSProxyConfig 返回一个最小有效配置（满足 Validate 所有必填字段）
func minimalValidSProxyConfig() *SProxyFullConfig {
	cfg := &SProxyFullConfig{}
	cfg.Listen.Port = 9000
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 32 chars
	cfg.LLM.Targets = []LLMTarget{{URL: "http://llm", APIKey: "key"}}
	cfg.Database.Path = "pairproxy.db"
	cfg.Database.Driver = "sqlite"
	return cfg
}
