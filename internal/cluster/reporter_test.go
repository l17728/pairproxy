package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pairproxy/pairproxy/internal/db"
)

func TestReporterHeartbeat(t *testing.T) {
	var received []RegisterPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != defaultRegisterPath {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var p RegisterPayload
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &p)
		received = append(received, p)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	r := NewReporter(logger, ReporterConfig{
		SP1Addr:    srv.URL,
		SelfID:     "sp-2",
		SelfAddr:   "http://sp-2:9000",
		SelfWeight: 2,
		Interval:   50 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r.Start(ctx)

	// 等待至少 2 个心跳（启动时立即 + 50ms 后）
	time.Sleep(150 * time.Millisecond)

	if len(received) < 2 {
		t.Errorf("expected ≥2 heartbeats, got %d", len(received))
	}
	if received[0].ID != "sp-2" || received[0].Addr != "http://sp-2:9000" {
		t.Errorf("unexpected payload: %+v", received[0])
	}
}

func TestReporterUsageReport(t *testing.T) {
	var receivedPayload UsageReportPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != defaultUsageReportPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	reporter := NewReporter(logger, ReporterConfig{
		SP1Addr:  srv.URL,
		SelfID:   "sp-2",
		SelfAddr: "http://sp-2:9000",
	}, nil)

	records := []db.UsageRecord{
		{RequestID: "req-1", UserID: "user-1", InputTokens: 100, OutputTokens: 50},
		{RequestID: "req-2", UserID: "user-2", InputTokens: 200, OutputTokens: 80},
	}

	if err := reporter.ReportUsage(records); err != nil {
		t.Fatalf("ReportUsage: %v", err)
	}

	if receivedPayload.SourceNode != "sp-2" {
		t.Errorf("SourceNode = %q, want 'sp-2'", receivedPayload.SourceNode)
	}
	if len(receivedPayload.Records) != 2 {
		t.Errorf("expected 2 records, got %d", len(receivedPayload.Records))
	}
	if receivedPayload.Records[0].InputTokens != 100 {
		t.Errorf("Records[0].InputTokens = %d, want 100", receivedPayload.Records[0].InputTokens)
	}
}

func TestReporterHeartbeatAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-secret" {
			t.Errorf("Authorization = %q, want 'Bearer my-secret'", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	reporter := NewReporter(logger, ReporterConfig{
		SP1Addr:      srv.URL,
		SelfID:       "sp-2",
		SelfAddr:     "http://sp-2:9000",
		SharedSecret: "my-secret",
		Interval:     1 * time.Hour, // 不触发定时器
	}, nil)

	// 只测试一次立即心跳
	reporter.sendHeartbeat()
	// 测试不 panic，且 mock server 验证了 auth header
}
