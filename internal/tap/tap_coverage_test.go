package tap

// tap_coverage_test.go — 补充覆盖以下分支：
//   - TeeWriter.Write: ResponseWriter.Write 失败时记录日志（err != nil）
//   - TeeWriter.Write: 非 streaming 且写入 n=0（不 Feed 解析器）
//   - TeeWriter.Write: onChunk 回调被调用（streaming + onChunk != nil）
//   - TeeWriter.Flush: 底层不是 http.Flusher → no-op
//   - AnthropicSSEParser.processLine: 非 data: 行（event:/id:/空行）→ 直接 return
//   - AnthropicSSEParser.processLine: "[DONE]" 字符串 → 直接 return
//   - AnthropicSSEParser.processLine: 无效 JSON → 静默忽略
//   - AnthropicSSEParser.processLine: 未知 event type（如 "content_block_start"）→ 忽略
//   - TTFBMs: firstByteAt 为零值 → 返回 0
//   - TTFBMs: startTime 为零值 → 返回 0
//   - UpdateModel: 更新 record.Model 字段

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// ---------------------------------------------------------------------------
// 测试辅助：不实现 http.Flusher 的 ResponseWriter
// ---------------------------------------------------------------------------

type noFlusherWriter struct {
	http.ResponseWriter
}

// newNoFlusherWriter 包装一个 ResponseRecorder，但不实现 http.Flusher
func newNoFlusherWriter(rr *httptest.ResponseRecorder) *noFlusherWriter {
	return &noFlusherWriter{ResponseWriter: rr}
}

// ---------------------------------------------------------------------------
// 测试辅助：会返回 error 的 ResponseWriter
// ---------------------------------------------------------------------------

type failWriter struct {
	http.ResponseWriter
	failAfter int // 前 n 次成功，第 n+1 次开始失败
	calls     int
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.failAfter {
		return 0, errors.New("write failed")
	}
	return f.ResponseWriter.Write(p)
}

func (f *failWriter) Header() http.Header {
	return f.ResponseWriter.Header()
}

func (f *failWriter) WriteHeader(code int) {
	f.ResponseWriter.WriteHeader(code)
}

// ---------------------------------------------------------------------------
// 测试辅助：创建 TeeResponseWriter（复用 setupTeeWriter 逻辑但更灵活）
// ---------------------------------------------------------------------------

func newTeeWriter(t *testing.T, inner http.ResponseWriter, provider string, onChunk func([]byte)) (*TeeResponseWriter, *db.UsageWriter, context.CancelFunc) {
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
		RequestID:  "req-cov",
		UserID:     "user-cov",
		SourceNode: "local",
	}

	tw := NewTeeResponseWriter(inner, logger, writer, record, provider, time.Time{}, onChunk)
	return tw, writer, cancel
}

// ---------------------------------------------------------------------------
// Write — ResponseWriter.Write 失败时正确返回 error
// ---------------------------------------------------------------------------

func TestCoverage_TeeWrite_WriteFails(t *testing.T) {
	rr := httptest.NewRecorder()
	fw := &failWriter{ResponseWriter: rr, failAfter: 0} // 第 1 次就失败
	tw, writer, cancel := newTeeWriter(t, fw, "", nil)
	defer func() {
		cancel()
		writer.Wait()
	}()

	n, err := tw.Write([]byte("hello"))
	if err == nil {
		t.Error("expected error from failWriter, got nil")
	}
	_ = n
}

// ---------------------------------------------------------------------------
// Write — n=0（底层写入了 0 字节）→ 不 Feed 解析器
// ---------------------------------------------------------------------------

func TestCoverage_TeeWrite_ZeroBytesWritten(t *testing.T) {
	rr := httptest.NewRecorder()
	// 让 ResponseWriter 总是返回 0 bytes written
	fw := &failWriter{ResponseWriter: rr, failAfter: 0}
	rr.Header().Set("Content-Type", "text/event-stream")

	tw, writer, cancel := newTeeWriter(t, fw, "", nil)
	defer func() {
		cancel()
		writer.Wait()
	}()
	tw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	// 发送一个完整的 SSE 数据，但底层写 0 字节（failWriter 第 1 次失败）
	sse := BuildAnthropicSSE(100, 50, []string{"hi"})
	tw.Write([]byte(sse)) //nolint:errcheck

	// 解析器不应有任何 token（因为 n=0，不 Feed）
	if tw.parser.InputTokens() != 0 {
		t.Errorf("parser should not be fed when n=0; got InputTokens=%d", tw.parser.InputTokens())
	}
}

// ---------------------------------------------------------------------------
// Write — onChunk 回调在 streaming 时被调用
// ---------------------------------------------------------------------------

func TestCoverage_TeeWrite_OnChunkCalled(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")

	chunkCount := 0
	var lastChunk []byte
	onChunk := func(p []byte) {
		chunkCount++
		lastChunk = append([]byte{}, p...)
	}

	tw, writer, cancel := newTeeWriter(t, rr, "", onChunk)
	defer func() {
		cancel()
		writer.Wait()
	}()
	tw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	data := []byte("data: {\"type\":\"ping\"}\n\n")
	tw.Write(data) //nolint:errcheck

	if chunkCount == 0 {
		t.Error("onChunk callback should have been called")
	}
	if string(lastChunk) != string(data) {
		t.Errorf("onChunk received %q, want %q", lastChunk, data)
	}
}

// ---------------------------------------------------------------------------
// Flush — 底层不实现 http.Flusher → no-op（不 panic）
// ---------------------------------------------------------------------------

func TestCoverage_TeeFlush_NoFlusher(t *testing.T) {
	rr := httptest.NewRecorder()
	inner := newNoFlusherWriter(rr)

	tw, writer, cancel := newTeeWriter(t, inner, "", nil)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// 不应 panic（底层不是 http.Flusher）
	tw.Flush()
}

// ---------------------------------------------------------------------------
// processLine — 非 data: 行被忽略（"event:" 行）
// ---------------------------------------------------------------------------

func TestCoverage_ProcessLine_EventLine_Ignored(t *testing.T) {
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	// 只发送 event: 行，没有 data: 行
	parser.Feed([]byte("event: message_start\n\n"))

	if called {
		t.Error("callback should not be called from event: line alone")
	}
	if parser.InputTokens() != 0 {
		t.Errorf("InputTokens = %d, want 0 for event-only input", parser.InputTokens())
	}
}

// ---------------------------------------------------------------------------
// processLine — "[DONE]" 字符串被忽略
// ---------------------------------------------------------------------------

func TestCoverage_ProcessLine_DONE_Ignored(t *testing.T) {
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	parser.Feed([]byte("data: [DONE]\n"))

	if called {
		t.Error("callback should not be called for [DONE]")
	}
}

// ---------------------------------------------------------------------------
// processLine — 无效 JSON 静默忽略（不 panic，不调用回调）
// ---------------------------------------------------------------------------

func TestCoverage_ProcessLine_InvalidJSON_Ignored(t *testing.T) {
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	parser.Feed([]byte("data: {invalid json}\n"))

	if called {
		t.Error("callback should not be called for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// processLine — 未知 event type 被忽略（如 "ping", "content_block_start"）
// ---------------------------------------------------------------------------

func TestCoverage_ProcessLine_UnknownType_Ignored(t *testing.T) {
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	// content_block_start 在 switch 中没有 case，应被忽略
	parser.Feed([]byte(`data: {"type":"content_block_start","index":0}` + "\n"))
	parser.Feed([]byte(`data: {"type":"ping"}` + "\n"))

	if called {
		t.Error("callback should not be called for unknown event types")
	}
}

// ---------------------------------------------------------------------------
// TTFBMs — firstByteAt 为零值（Write 未被调用）→ 返回 0
// ---------------------------------------------------------------------------

func TestCoverage_TTFBMs_BeforeFirstWrite(t *testing.T) {
	rr := httptest.NewRecorder()
	tw, writer, cancel := setupTeeWriter(t, rr)
	defer func() {
		cancel()
		writer.Wait()
	}()

	// 未调用 Write，firstByteAt 是零值
	if ttfb := tw.TTFBMs(); ttfb != 0 {
		t.Errorf("TTFBMs() before first Write = %d, want 0", ttfb)
	}
}

// ---------------------------------------------------------------------------
// TTFBMs — startTime 为零值（TeeResponseWriter 以 time.Time{} 创建）→ 返回 0
// ---------------------------------------------------------------------------

func TestCoverage_TTFBMs_ZeroStartTime(t *testing.T) {
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

	record := db.UsageRecord{RequestID: "req-ttfb", UserID: "u-ttfb"}
	// startTime = time.Time{} (零值)
	tw := NewTeeResponseWriter(rr, logger, writer, record, "", time.Time{}, nil)

	// 写入数据，设置 firstByteAt
	tw.Write([]byte("data")) //nolint:errcheck

	// startTime 是零值 → TTFBMs 返回 0
	if ttfb := tw.TTFBMs(); ttfb != 0 {
		t.Errorf("TTFBMs() with zero startTime = %d, want 0", ttfb)
	}
}

// ---------------------------------------------------------------------------
// TTFBMs — 正常情况（startTime 非零，Write 已被调用）→ 返回 > 0
// ---------------------------------------------------------------------------

func TestCoverage_TTFBMs_NormalCase(t *testing.T) {
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

	record := db.UsageRecord{RequestID: "req-ttfb2", UserID: "u-ttfb2"}
	startTime := time.Now().Add(-50 * time.Millisecond) // 50ms 前开始
	tw := NewTeeResponseWriter(rr, logger, writer, record, "", startTime, nil)

	time.Sleep(1 * time.Millisecond)
	tw.Write([]byte("data")) //nolint:errcheck

	ttfb := tw.TTFBMs()
	if ttfb <= 0 {
		t.Errorf("TTFBMs() = %d, want > 0", ttfb)
	}
}

// ---------------------------------------------------------------------------
// UpdateModel — 更新 record.Model 字段
// ---------------------------------------------------------------------------

func TestCoverage_UpdateModel(t *testing.T) {
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

	record := db.UsageRecord{
		RequestID: "req-model-upd",
		UserID:    "u-model",
		Model:     "initial-model",
	}
	tw := NewTeeResponseWriter(rr, logger, writer, record, "", time.Time{}, nil)

	tw.UpdateModel("claude-3-5-sonnet")

	// 验证 model 字段已更新（通过 RecordNonStreaming 写入 DB 后验证）
	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
	tw.RecordNonStreaming(body, http.StatusOK, 100)

	cancel()
	writer.Wait()

	repo := db.NewUsageRepo(gormDB, logger)
	logs, err := repo.Query(db.UsageFilter{UserID: "u-model", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %q, want 'claude-3-5-sonnet'", logs[0].Model)
	}
}

// ---------------------------------------------------------------------------
// AnthropicSSEParser — message_delta 中有 nil Usage（不 panic）
// ---------------------------------------------------------------------------

func TestCoverage_SSEParser_MessageDelta_NilUsage(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":50,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`
	var gotInput int
	parser := NewAnthropicSSEParser(func(in, _ int) { gotInput = in })
	parser.Feed([]byte(sse))

	// message_delta 没有 usage 字段，input_tokens 保持 message_start 的值
	if gotInput != 50 {
		t.Errorf("inputTokens = %d, want 50", gotInput)
	}
}

// ---------------------------------------------------------------------------
// Feed — 数据不含换行符（全部进入 lineBuffer）
// ---------------------------------------------------------------------------

func TestCoverage_SSEParser_NoNewline_BufferedLines(t *testing.T) {
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	// 没有换行符的数据 → 全部进入 lineBuffer
	parser.Feed([]byte(`data: {"type":"message_start"`))

	if called {
		t.Error("callback should not be called for partial (buffered) data")
	}
}
