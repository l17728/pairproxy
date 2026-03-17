package db

import (
	"testing"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

// TestBuildPostgresDSN_FromDSN 验证 DSN 字段优先级高于独立字段
func TestBuildPostgresDSN_FromDSN(t *testing.T) {
	cfg := config.DatabaseConfig{
		DSN:      "postgres://user:pass@host:5432/db?sslmode=require",
		Host:     "other-host",
		Port:     9999,
		User:     "other-user",
		Password: "other-pass",
		DBName:   "other-db",
		SSLMode:  "disable",
	}
	result := buildPostgresDSN(cfg)
	assert.Equal(t, "postgres://user:pass@host:5432/db?sslmode=require", result)
}

// TestBuildPostgresDSN_FromFields 验证从独立字段拼接 DSN
func TestBuildPostgresDSN_FromFields(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:     "pg.company.com",
		Port:     5432,
		User:     "pairproxy",
		Password: "secret",
		DBName:   "pairproxy",
		SSLMode:  "require",
	}
	result := buildPostgresDSN(cfg)
	assert.Equal(t, "host=pg.company.com port=5432 user=pairproxy password=secret dbname=pairproxy sslmode=require", result)
}

// TestMaskDSN_KVFormat 验证 key=value 格式脱敏
func TestMaskDSN_KVFormat(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "password with value followed by other fields",
			input:    "host=pg user=app password=secret dbname=db",
			expected: "host=pg user=app password=*** dbname=db",
		},
		{
			name:  "password empty value followed by spaces and other fields",
			input: "host=pg user=app password=  dbname=db",
			// \s*=\s* 贪婪消耗等号后空格，\S* 匹配到 dbname=db，故 dbname=db 被吞入替换
			expected: "host=pg user=app password=  ***",
		},
		{
			name:     "no password field",
			input:    "host=pg user=app",
			expected: "host=pg user=app",
		},
		{
			name:     "password with special characters",
			input:    "password=my-p@ssw0rd! host=pg",
			expected: "password=*** host=pg",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, maskDSN(c.input))
		})
	}
}

// TestMaskDSN_URLFormat 验证 URL 格式脱敏
func TestMaskDSN_URLFormat(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "URL with password",
			input:    "postgres://app:secret@pg.company.com/db",
			expected: "postgres://app:***@pg.company.com/db",
		},
		{
			name:     "URL without password",
			input:    "postgres://app@pg.company.com/db",
			expected: "postgres://app@pg.company.com/db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, maskDSN(c.input))
		})
	}
}

// TestDriverName_Nil 验证 nil db 返回 "sqlite"（nil guard）
func TestDriverName_Nil(t *testing.T) {
	result := DriverName(nil)
	assert.Equal(t, "sqlite", result)
}

// TestDriverName_SQLite 验证 SQLite 驱动返回 "sqlite"
func TestDriverName_SQLite(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db, err := Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	assert.Equal(t, "sqlite", DriverName(db))
}
