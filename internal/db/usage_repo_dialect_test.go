package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newRepoWithDriver 构建一个 driver 字段固定为指定值的 UsageRepo（用于方言测试）
func newRepoWithDriver(driver string) *UsageRepo {
	return &UsageRepo{
		db:     &gorm.DB{},
		logger: zap.NewNop().Named("test"),
		driver: driver,
	}
}

// TestDateExpr_SQLite 验证 SQLite 下 dateExpr 返回 DATE() 函数
func TestDateExpr_SQLite(t *testing.T) {
	r := newRepoWithDriver("sqlite")
	assert.Equal(t, "DATE(created_at)", r.dateExpr("created_at"))
	assert.Equal(t, "DATE(updated_at)", r.dateExpr("updated_at"))
}

// TestDateExpr_Postgres 验证 PostgreSQL 下 dateExpr 返回 DATE_TRUNC
func TestDateExpr_Postgres(t *testing.T) {
	r := newRepoWithDriver("postgres")
	assert.Equal(t, "DATE_TRUNC('day', created_at)::DATE", r.dateExpr("created_at"))
}

// TestMonthsActiveExpr_SQLite 验证 SQLite 下 monthsActiveExpr 返回 julianday
func TestMonthsActiveExpr_SQLite(t *testing.T) {
	r := newRepoWithDriver("sqlite")
	expr := r.monthsActiveExpr("created_at")
	assert.Contains(t, expr, "julianday")
	assert.Contains(t, expr, "created_at")
}

// TestMonthsActiveExpr_Postgres 验证 PostgreSQL 下 monthsActiveExpr 返回 EXTRACT
func TestMonthsActiveExpr_Postgres(t *testing.T) {
	r := newRepoWithDriver("postgres")
	expr := r.monthsActiveExpr("created_at")
	assert.Contains(t, expr, "EXTRACT")
	assert.Contains(t, expr, "EPOCH")
	assert.Contains(t, expr, "created_at")
}
