package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplySProxyDefaults_PGDefaults 验证 PG 模式下默认值填充
func TestApplySProxyDefaults_PGDefaults(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Listen.Port = 9000
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 34 chars
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}

	applySProxyDefaults(cfg)

	assert.Equal(t, "postgres", cfg.Database.Driver)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "disable", cfg.Database.SSLMode)
	assert.Equal(t, 50, cfg.Database.MaxOpenConns)
}

// TestApplySProxyDefaults_SQLiteDefault 验证省略 Driver 时默认为 sqlite
func TestApplySProxyDefaults_SQLiteDefault(t *testing.T) {
	cfg := &SProxyFullConfig{}
	applySProxyDefaults(cfg)
	assert.Equal(t, "sqlite", cfg.Database.Driver)
}

// TestApplySProxyDefaults_PGDoesNotOverrideExistingValues 验证 PG 用户设置的值不被覆盖
func TestApplySProxyDefaults_PGDoesNotOverrideExistingValues(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Database.Port = 5433
	cfg.Database.SSLMode = "require"
	cfg.Database.MaxOpenConns = 100

	applySProxyDefaults(cfg)

	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "require", cfg.Database.SSLMode)
	assert.Equal(t, 100, cfg.Database.MaxOpenConns)
}

// TestValidate_PostgresDSNOK 验证 PG 模式下 DSN 非空时校验通过
func TestValidate_PostgresDSNOK(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Database.DSN = "host=pg user=app dbname=db"
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000
	applySProxyDefaults(cfg)

	err := cfg.Validate()
	assert.NoError(t, err)
}

// TestValidate_PostgresMissingDSNAndFields 验证 PG 模式下缺少连接信息时报错
func TestValidate_PostgresMissingDSNAndFields(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	// DSN 和独立字段均为空
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000
	applySProxyDefaults(cfg)

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres")
}

// TestValidate_PostgresFieldsOK 验证 PG 模式下独立字段满足时校验通过
func TestValidate_PostgresFieldsOK(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Database.Host = "pg.company.com"
	cfg.Database.User = "pairproxy"
	cfg.Database.DBName = "pairproxy"
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000
	applySProxyDefaults(cfg)

	err := cfg.Validate()
	assert.NoError(t, err)
}

// TestValidate_PostgresInvalidSSLMode 验证 PG 模式下无效 SSLMode 报错
func TestValidate_PostgresInvalidSSLMode(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Database.Host = "pg"
	cfg.Database.User = "app"
	cfg.Database.DBName = "db"
	cfg.Database.SSLMode = "invalid-mode"
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslmode")
}

// TestValidate_SQLitePathRequired 验证 SQLite 模式下 Path 必填（向后兼容）
func TestValidate_SQLitePathRequired(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.Path = "" // 故意为空
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.path")
}
