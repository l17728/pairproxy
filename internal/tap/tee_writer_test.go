package tap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// setupTeeWriter 创建一个带内存 DB 的 TeeResponseWriter，供测试复用。
func setupTeeWriter(t *testing.T, rr *httptest.ResponseRecorder) (*TeeResponseWriter, *db.UsageWriter, context.CancelFunc) {
	t.Helper()
	logger := zaptest.NewLogger(t)

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

	record := db.UsageRecord{
		RequestID:   "req-tee-test",
		UserID:      "user-tee",
		Model:       "claude-3-5-sonnet",
		UpstreamURL: "https://api.anthropic.com",
		SourceNode:  "local",
	}

	tw := NewTeeResponseWriter(rr, logger, writer, record, "")
	return tw, writer, cancel
}

// ---------------------------------------------------------------------------
// TestTeeWriterBytesUnchanged：原始 Writer 收到的字节与输入完全一致
// ---------------------------------------------------------------------------

func TestTeeWriterBytesUnchanged(t *testing.T) {
	rr := httptest.NewRecorder()
	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	input := "hello, world"
	n, err := tw.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write returned %d, want %d", n, len(input))
	}
	if rr.Body.String() != input {
		t.Errorf("body = %q, want %q", rr.Body.String(), input)
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterParserReceivesAll：SSE 解析器收到所有字节（streaming 模式）
// ---------------------------------------------------------------------------

func TestTeeWriterParserReceivesAll(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")

	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// 先设置 Content-Type，让 TeeWriter 识别为 streaming
	tw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	sse := BuildAnthropicSSE(100, 50, []string{"hello"})
	_, err := tw.Write([]byte(sse))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// 解析器应该已解析到 input_tokens
	if tw.parser.InputTokens() != 100 {
		t.Errorf("parser.InputTokens() = %d, want 100", tw.parser.InputTokens())
	}
	if tw.parser.OutputTokens() != 50 {
		t.Errorf("parser.OutputTokens() = %d, want 50", tw.parser.OutputTokens())
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterFlushPropagates：Flush 调用透传到底层 Writer
// ---------------------------------------------------------------------------

func TestTeeWriterFlushPropagates(t *testing.T) {
	rr := httptest.NewRecorder()
	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// httptest.ResponseRecorder 实现了 http.Flusher
	tw.Flush()
	if !rr.Flushed {
		t.Error("Flush should propagate to underlying ResponseRecorder")
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterUsageRecordedStreaming：完整 SSE 序列后，UsageWriter 收到正确记录
// ---------------------------------------------------------------------------

func TestTeeWriterUsageRecordedStreaming(t *testing.T) {
	rr := httptest.NewRecorder()
	logger := zaptest.NewLogger(t)

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	record := db.UsageRecord{
		RequestID:  "req-streaming-1",
		UserID:     "user-streaming",
		Model:      "claude-3",
		SourceNode: "local",
	}

	tw := NewTeeResponseWriter(rr, logger, writer, record, "")
	// 设置 streaming Content-Type
	tw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
	tw.WriteHeader(http.StatusOK)

	// 模拟分块写入完整 SSE
	sse := BuildAnthropicSSE(200, 80, []string{"Hello", " Claude"})
	chunkSize := 32
	data := []byte(sse)
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		_, _ = tw.Write(data[i:end])
	}

	// 停止 writer，等待写入
	cancel()
	writer.Wait()

	// 验证 DB 中有记录
	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-streaming", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	log := logs[0]
	if log.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want 200", log.InputTokens)
	}
	if log.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", log.OutputTokens)
	}
	if !log.IsStreaming {
		t.Error("IsStreaming should be true")
	}
	if log.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", log.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterUsageRecordedNonStreaming：非 streaming 响应由 RecordNonStreaming 记录
// ---------------------------------------------------------------------------

func TestTeeWriterUsageRecordedNonStreaming(t *testing.T) {
	rr := httptest.NewRecorder()
	logger := zaptest.NewLogger(t)

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)

	record := db.UsageRecord{
		RequestID:  "req-nonstreaming-1",
		UserID:     "user-ns",
		Model:      "claude-3-opus",
		SourceNode: "local",
	}

	tw := NewTeeResponseWriter(rr, logger, writer, record, "")
	tw.ResponseWriter.Header().Set("Content-Type", "application/json")
	tw.WriteHeader(http.StatusOK)

	// 写入非 streaming JSON 响应
	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":50,"output_tokens":20}}`)
	_, _ = tw.Write(body)

	// 调用方负责非 streaming 的用量记录
	tw.RecordNonStreaming(body, http.StatusOK, 123)

	// 停止 writer
	cancel()
	writer.Wait()

	// 验证 DB
	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "user-ns", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	log := logs[0]
	if log.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", log.InputTokens)
	}
	if log.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", log.OutputTokens)
	}
	if log.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming response")
	}
	if log.DurationMs != 123 {
		t.Errorf("DurationMs = %d, want 123", log.DurationMs)
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterNonStreamingNotFedToParser：非 streaming 内容不触发 SSE 解析
// ---------------------------------------------------------------------------

func TestTeeWriterNonStreamingNotFedToParser(t *testing.T) {
	rr := httptest.NewRecorder()
	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// Content-Type 为 application/json（非 streaming）
	tw.ResponseWriter.Header().Set("Content-Type", "application/json")

	// 写入一个看起来像 SSE 的 JSON（不应被解析为 SSE）
	tw.WriteHeader(http.StatusOK)
	_, _ = tw.Write([]byte(`{"type":"message_stop"}`))

	// 解析器不应触发
	if tw.parser.InputTokens() != 0 || tw.parser.OutputTokens() != 0 {
		t.Error("SSE parser should not be fed for non-streaming responses")
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterWriteHeaderStatus：WriteHeader 正确记录状态码
// ---------------------------------------------------------------------------

func TestTeeWriterWriteHeaderStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	tw.WriteHeader(http.StatusCreated)
	if tw.StatusCode() != http.StatusCreated {
		t.Errorf("StatusCode() = %d, want 201", tw.StatusCode())
	}
	if rr.Code != http.StatusCreated {
		t.Errorf("rr.Code = %d, want 201", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// TestTeeWriterLargeSSEStream：大量 chunk 的鲁棒性测试
// ---------------------------------------------------------------------------

func TestTeeWriterLargeSSEStream(t *testing.T) {
	rr := httptest.NewRecorder()
	logger := zaptest.NewLogger(t)

	gormDB, _ := db.Open(logger, ":memory:")
	_ = db.Migrate(logger, gormDB)

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)
	writer.Start(ctx)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// 生成 500 个 chunk 的长 SSE
	chunks := make([]string, 500)
	for i := range chunks {
		chunks[i] = "word"
	}
	sse := BuildAnthropicSSE(1000, 500, chunks)

	record := db.UsageRecord{RequestID: "req-large", UserID: "user-large", SourceNode: "local"}
	tw := NewTeeResponseWriter(rr, logger, writer, record, "")
	tw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	// 按 64 字节 chunk 写入
	data := []byte(sse)
	for i := 0; i < len(data); i += 64 {
		end := i + 64
		if end > len(data) {
			end = len(data)
		}
		_, _ = tw.Write(data[i:end])
	}

	if tw.parser.InputTokens() != 1000 {
		t.Errorf("inputTokens = %d, want 1000", tw.parser.InputTokens())
	}
	if tw.parser.OutputTokens() != 500 {
		t.Errorf("outputTokens = %d, want 500", tw.parser.OutputTokens())
	}

	// 验证原始 writer 收到的内容完整
	if !strings.Contains(rr.Body.String(), "message_stop") {
		t.Error("output body should contain message_stop")
	}
}
