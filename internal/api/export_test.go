package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// setupExportTest 创建带预置数据的导出测试环境
func setupExportTest(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "export-test-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	// 直接写入若干 UsageLog
	usageRepo := db.NewUsageRepo(gormDB, logger)
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		log := db.UsageLog{
			RequestID:    "export-req-" + string([]byte{byte('a' + i)}),
			UserID:       "export-user",
			Model:        "claude-3",
			InputTokens:  100 + i*10,
			OutputTokens: 50 + i*5,
			TotalTokens:  150 + i*15,
			StatusCode:   200,
			IsStreaming:  i%2 == 0,
			DurationMs:   int64(100 + i*10),
			SourceNode:   "local",
			CreatedAt:    base.Add(time.Duration(i) * time.Hour),
		}
		if err := gormDB.Create(&log).Error; err != nil {
			t.Fatalf("seed log %d: %v", i, err)
		}
	}

	handler := NewAdminHandler(logger, jwtMgr,
		db.NewUserRepo(gormDB, logger),
		db.NewGroupRepo(gormDB, logger),
		usageRepo,
		db.NewAuditRepo(logger, gormDB),
		"", time.Hour,
	)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tok, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}

	return mux, tok
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_JSON
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_JSON(t *testing.T) {
	mux, tok := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/export?format=json&from=2024-06-01&to=2024-06-30", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "ndjson") {
		t.Errorf("Content-Type = %q, want to contain 'ndjson'", ct)
	}

	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, should be attachment", cd)
	}

	// 验证 NDJSON：每行一个有效 JSON 对象
	rows := 0
	scanner := bufio.NewScanner(strings.NewReader(rr.Body.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid JSON line %d: %v\nline: %s", rows+1, err, line)
		}
		// 验证必要字段存在
		for _, field := range []string{"request_id", "user_id", "input_tokens", "created_at"} {
			if _, ok := obj[field]; !ok {
				t.Errorf("line %d missing field %q", rows+1, field)
			}
		}
		rows++
	}

	if rows != 5 {
		t.Errorf("exported %d rows, want 5", rows)
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_CSV
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_CSV(t *testing.T) {
	mux, tok := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/export?format=csv&from=2024-06-01&to=2024-06-30", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want 'text/csv'", ct)
	}

	body := rr.Body.String()
	// 去除 UTF-8 BOM
	body = strings.TrimPrefix(body, "\xEF\xBB\xBF")

	lines := strings.Split(strings.TrimSpace(body), "\n")
	// 1 header + 5 data rows
	if len(lines) != 6 {
		t.Fatalf("CSV has %d lines (incl. header), want 6\n%s", len(lines), body)
	}

	// 验证列头
	header := lines[0]
	for _, col := range []string{"request_id", "user_id", "input_tokens", "created_at"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing column %q: %s", col, header)
		}
	}

	// 验证数据行包含期望内容
	if !strings.Contains(body, "export-user") {
		t.Errorf("CSV body should contain 'export-user'")
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_DefaultFormat
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_DefaultFormat(t *testing.T) {
	mux, tok := setupExportTest(t)

	// 不传 format 参数，应默认为 json
	req := httptest.NewRequest(http.MethodGet, "/api/admin/export?from=2024-06-01&to=2024-06-30", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "ndjson") {
		t.Errorf("default format should be ndjson, got %q", rr.Header().Get("Content-Type"))
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_InvalidFormat
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_InvalidFormat(t *testing.T) {
	mux, tok := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/export?format=xml", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid format", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_InvalidDate
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_InvalidDate(t *testing.T) {
	mux, tok := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/export?from=not-a-date", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid date", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_Unauthorized
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_Unauthorized(t *testing.T) {
	mux, _ := setupExportTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/export?format=json", nil)
	// 不带 token

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without auth", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestAdminExportEndpoint_EmptyRange
// ---------------------------------------------------------------------------

func TestAdminExportEndpoint_EmptyRange(t *testing.T) {
	mux, tok := setupExportTest(t)

	// 查询没有数据的时间段
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/export?format=json&from=2020-01-01&to=2020-01-02", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for empty range", rr.Code)
	}
	// NDJSON 应为空（无行）
	body := strings.TrimSpace(rr.Body.String())
	if body != "" {
		t.Errorf("expected empty body for no-data range, got %q", body)
	}
}
