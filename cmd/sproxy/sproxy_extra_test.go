package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// ptrInt64Val — 覆盖各种输入情况
// ---------------------------------------------------------------------------

func TestPtrInt64Val_NilGroup(t *testing.T) {
	got := ptrInt64Val(nil, "daily")
	if got != 0 {
		t.Errorf("ptrInt64Val(nil, 'daily') = %d, want 0", got)
	}
	got = ptrInt64Val(nil, "monthly")
	if got != 0 {
		t.Errorf("ptrInt64Val(nil, 'monthly') = %d, want 0", got)
	}
}

func TestPtrInt64Val_WithGroup_Daily(t *testing.T) {
	val := int64(500)
	grp := &db.Group{ID: "g1", Name: "test", DailyTokenLimit: &val}
	got := ptrInt64Val(grp, "daily")
	if got != 500 {
		t.Errorf("ptrInt64Val(grp, 'daily') = %d, want 500", got)
	}
}

func TestPtrInt64Val_WithGroup_Monthly(t *testing.T) {
	val := int64(10000)
	grp := &db.Group{ID: "g1", Name: "test", MonthlyTokenLimit: &val}
	got := ptrInt64Val(grp, "monthly")
	if got != 10000 {
		t.Errorf("ptrInt64Val(grp, 'monthly') = %d, want 10000", got)
	}
}

func TestPtrInt64Val_UnknownField(t *testing.T) {
	grp := &db.Group{ID: "g1", Name: "test"}
	got := ptrInt64Val(grp, "unknown-field")
	if got != 0 {
		t.Errorf("ptrInt64Val(grp, 'unknown-field') = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// ptrInt64 — 覆盖 nil 和非 nil 路径
// ---------------------------------------------------------------------------

func TestPtrInt64_Nil(t *testing.T) {
	got := ptrInt64(nil)
	if got != 0 {
		t.Errorf("ptrInt64(nil) = %d, want 0", got)
	}
}

func TestPtrInt64_NonNil(t *testing.T) {
	v := int64(42)
	got := ptrInt64(&v)
	if got != 42 {
		t.Errorf("ptrInt64(&42) = %d, want 42", got)
	}
}

// ---------------------------------------------------------------------------
// printQuotaRow — 覆盖所有 3 种状态输出
// ---------------------------------------------------------------------------

func TestPrintQuotaRow_Unlimited(t *testing.T) {
	printQuotaRow("daily tokens", 100, 0)
}

func TestPrintQuotaRow_OK(t *testing.T) {
	printQuotaRow("daily tokens", 50, 100)
}

func TestPrintQuotaRow_Warning(t *testing.T) {
	printQuotaRow("daily tokens", 85, 100)
}

func TestPrintQuotaRow_Exceeded(t *testing.T) {
	printQuotaRow("daily tokens", 110, 100)
}

func TestPrintQuotaRow_ExactlyAtLimit(t *testing.T) {
	printQuotaRow("daily tokens", 100, 100)
}

// ---------------------------------------------------------------------------
// buildCore — 覆盖 buildCore 函数
// ---------------------------------------------------------------------------

func TestBuildLogger_ReturnsNonNil(t *testing.T) {
	atom := zap.NewAtomicLevel()
	core := buildCore(atom)
	if core == nil {
		t.Error("buildCore should return non-nil core")
	}
}

func TestBuildLogger_DifferentLevels(t *testing.T) {
	for _, lvlStr := range []string{"debug", "info", "warn", "error"} {
		atom := zap.NewAtomicLevel()
		if err := atom.UnmarshalText([]byte(lvlStr)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", lvlStr, err)
		}
		core := buildCore(atom)
		if core == nil {
			t.Errorf("buildCore(%q) returned nil", lvlStr)
		}
	}
}

// ---------------------------------------------------------------------------
// closeGormDB — 覆盖关闭 DB 路径
// ---------------------------------------------------------------------------

func TestCloseGormDB_NoError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	// 不应 panic
	closeGormDB(logger, gormDB)
}

// ---------------------------------------------------------------------------
// auditCLI — 覆盖审计日志写入路径
// ---------------------------------------------------------------------------

func TestAuditCLI_WritesLog(t *testing.T) {
	logger := zaptest.NewLogger(t)
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	defer closeGormDB(logger, gormDB)

	auditCLI(gormDB, logger, "test-create-user", "user:alice", "created from CLI")
}

func TestAuditCLI_DBError_LogsWarn(t *testing.T) {
	logger := zaptest.NewLogger(t)
	// 未 Migrate，表不存在 → 写入失败 → 触发 Warn 日志路径
	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer closeGormDB(logger, gormDB)
	auditCLI(gormDB, logger, "test-action", "target", "detail")
}

// ---------------------------------------------------------------------------
// buildDebugFileLogger — 覆盖日志文件构建路径
// ---------------------------------------------------------------------------

func TestBuildDebugFileLogger_ValidPath(t *testing.T) {
	// Windows 无法在测试后删除被 logger 持有的文件，使用 stderr 路径避免文件锁
	logger, err := buildDebugFileLogger("stderr")
	if err != nil {
		t.Fatalf("buildDebugFileLogger('stderr'): %v", err)
	}
	if logger == nil {
		t.Error("buildDebugFileLogger should return non-nil logger")
	}
	_ = logger.Sync()
}

// ---------------------------------------------------------------------------
// wrapOtelHTTP — 覆盖 OTEL 包装路径
// ---------------------------------------------------------------------------

func TestWrapOtelHTTP_ReturnsHandler(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := wrapOtelHTTP(inner, "test-operation")
	if wrapped == nil {
		t.Error("wrapOtelHTTP should return non-nil handler")
	}

	// 验证 wrapped handler 可正常调用
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("wrapped handler returned %d, want 200", rr.Code)
	}
}
