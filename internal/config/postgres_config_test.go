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

// TestValidate_PostgresPortBoundaryValues 验证 PG 模式下 Port 字段边界值校验：
//   - 0 合法（由 applySProxyDefaults 填充默认值 5432）
//   - 1 合法（最小端口）
//   - 65535 合法（最大端口）
//   - -1 非法（小于 1）
//   - 65536 非法（大于 65535）
func TestValidate_PostgresPortBoundaryValues(t *testing.T) {
	build := func(port int) *SProxyFullConfig {
		cfg := &SProxyFullConfig{}
		cfg.Database.Driver = "postgres"
		cfg.Database.Host = "pg"
		cfg.Database.User = "app"
		cfg.Database.DBName = "db"
		cfg.Database.Port = port
		cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
		cfg.Listen.Port = 9000
		return cfg
	}

	t.Run("port=0 valid (default)", func(t *testing.T) {
		cfg := build(0)
		applySProxyDefaults(cfg) // 填充默认 Port=5432
		assert.NoError(t, cfg.Validate())
	})
	t.Run("port=1 valid", func(t *testing.T) {
		cfg := build(1)
		assert.NoError(t, cfg.Validate())
	})
	t.Run("port=65535 valid", func(t *testing.T) {
		cfg := build(65535)
		assert.NoError(t, cfg.Validate())
	})
	t.Run("port=-1 invalid", func(t *testing.T) {
		cfg := build(-1)
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "port")
	})
	t.Run("port=65536 invalid", func(t *testing.T) {
		cfg := build(65536)
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "port")
	})
}

// TestValidate_PostgresMissingIndividualFields 验证缺少任一个字段（Host/User/DBName）时报错。I-3
func TestValidate_PostgresMissingIndividualFields(t *testing.T) {
	base := func() *SProxyFullConfig {
		cfg := &SProxyFullConfig{}
		cfg.Database.Driver = "postgres"
		cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
		cfg.Listen.Port = 9000
		return cfg
	}

	t.Run("host missing", func(t *testing.T) {
		cfg := base()
		cfg.Database.User = "app"
		cfg.Database.DBName = "db"
		// Host 为空
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "postgres")
	})
	t.Run("user missing", func(t *testing.T) {
		cfg := base()
		cfg.Database.Host = "pg"
		cfg.Database.DBName = "db"
		// User 为空
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "postgres")
	})
	t.Run("dbname missing", func(t *testing.T) {
		cfg := base()
		cfg.Database.Host = "pg"
		cfg.Database.User = "app"
		// DBName 为空
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "postgres")
	})
}

// TestValidate_PostgresDSNSetPortIgnored 验证 DSN 非空时 Port 越界校验被跳过。I-4
// 这保证了 DSN 模式下用户设置无效 Port 不会引发错误（DSN 已包含完整连接信息）。
func TestValidate_PostgresDSNSetPortIgnored(t *testing.T) {
	cfg := &SProxyFullConfig{}
	cfg.Database.Driver = "postgres"
	cfg.Database.DSN = "host=pg user=app dbname=db"
	cfg.Database.Port = -1 // 越界，但 DSN 已设置，应被忽略
	cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
	cfg.Listen.Port = 9000

	err := cfg.Validate()
	assert.NoError(t, err, "port check should be skipped when DSN is provided")
}

// TestValidate_PostgresSSLModeValidValues 验证每个合法 SSLMode 值均通过校验。M-3
// 合法值：disable, allow, prefer, require, verify-ca, verify-full, ""（空字符串）
func TestValidate_PostgresSSLModeValidValues(t *testing.T) {
	valid := []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full", ""}
	for _, mode := range valid {
		mode := mode
		t.Run("sslmode="+mode, func(t *testing.T) {
			cfg := &SProxyFullConfig{}
			cfg.Database.Driver = "postgres"
			cfg.Database.Host = "pg"
			cfg.Database.User = "app"
			cfg.Database.DBName = "db"
			cfg.Database.SSLMode = mode
			cfg.Auth.JWTSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			cfg.LLM.Targets = []LLMTarget{{URL: "http://pg", APIKey: "key"}}
			cfg.Listen.Port = 9000
			assert.NoError(t, cfg.Validate(), "sslmode %q should be valid", mode)
		})
	}
}
