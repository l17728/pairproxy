//go:build postgres

package db

// PostgreSQL 集成测试（需要真实 PG 实例）
// 运行方式：
//   docker run -d --name pg-test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=pairproxy \
//     -p 5432:5432 postgres:16-alpine
//   export POSTGRES_TEST_DSN="host=localhost user=postgres password=test dbname=pairproxy port=5432 sslmode=disable"
//   go test -tags=postgres ./internal/db/... -v -run Postgres
//
// 这些测试在 CI 中默认跳过（无 postgres build tag），仅在有真实 PG 时运行。
//
// TODO（后续添加）：
//   - TestOpenWithConfig_Postgres_Connect
//   - TestUsageRepo_DailyTokens_Postgres
//   - TestUsageRepo_UserAllTimeStats_Postgres

import "testing"

func TestPostgresPlaceholder(t *testing.T) {
	// 占位符：确保构建标签机制正常工作
	t.Skip("PostgreSQL integration tests require -tags=postgres and POSTGRES_TEST_DSN")
}
