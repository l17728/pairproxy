package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/config"
)

// ---------------------------------------------------------------------------
// extractMessages
// ---------------------------------------------------------------------------

func TestExtractMessages(t *testing.T) {
	// OpenAI 格式（string content）
	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`
	msgs := extractMessages([]byte(body))
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("unexpected msg[0]: %+v", msgs[0])
	}

	// Anthropic 格式（content block array）
	body2 := `{"messages":[{"role":"user","content":[{"type":"text","text":"world"}]}]}`
	msgs2 := extractMessages([]byte(body2))
	if len(msgs2) != 1 || msgs2[0].Content != "world" {
		t.Errorf("unexpected Anthropic msg: %+v", msgs2)
	}

	// 空 body
	if msgs3 := extractMessages(nil); msgs3 != nil {
		t.Errorf("expected nil for empty body, got %v", msgs3)
	}
}

// ---------------------------------------------------------------------------
// Collector: Anthropic streaming model extraction
// ---------------------------------------------------------------------------

func TestCollectorAnthropicStreaming(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"What is 2+2?"}]}`
	c := NewCollector(w, "9000", "req-1", "alice", "eng", "claude-sonnet-4-20250514",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// message_start: model + input tokens
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":100}}}` + "\n\n"))

	// content_block_delta: text
	c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"The answer"}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":" is 4."}}` + "\n\n"))

	// message_delta: output tokens
	c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":200}}` + "\n\n"))

	// message_stop
	c.FeedChunk([]byte(`data: {"type":"message_stop"}` + "\n\n"))

	c.Finish(200, 1500)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelRequested != "claude-sonnet-4-20250514" {
		t.Errorf("model_requested = %q", rec.ModelRequested)
	}
	if rec.ModelActual != "claude-sonnet-4-20250514" {
		t.Errorf("model_actual = %q", rec.ModelActual)
	}
	if rec.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", rec.InputTokens)
	}
	if rec.OutputTokens != 200 {
		t.Errorf("output_tokens = %d, want 200", rec.OutputTokens)
	}
	// messages: user + assistant
	if len(rec.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(rec.Messages))
	}
	if rec.Messages[1].Content != "The answer is 4." {
		t.Errorf("assistant content = %q", rec.Messages[1].Content)
	}
}

// ---------------------------------------------------------------------------
// Collector: OpenAI streaming model extraction
// ---------------------------------------------------------------------------

func TestCollectorOpenAIStreaming(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	c := NewCollector(w, "9001", "req-2", "bob", "dev", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	// 首个 chunk 带 model
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":"Hello"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":" there"}}]}` + "\n\n"))
	// usage chunk
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":150}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 800)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o-2024-08-06" {
		t.Errorf("model_actual = %q, want gpt-4o-2024-08-06", rec.ModelActual)
	}
	if rec.Messages[len(rec.Messages)-1].Content != "Hello there" {
		t.Errorf("assistant content = %q", rec.Messages[len(rec.Messages)-1].Content)
	}
	if rec.InputTokens != 50 || rec.OutputTokens != 150 {
		t.Errorf("tokens = (%d, %d), want (50, 150)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: non-streaming Anthropic
// ---------------------------------------------------------------------------

func TestCollectorNonStreamingAnthropic(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"claude-haiku","messages":[{"role":"user","content":"ping"}]}`
	c := NewCollector(w, "9000", "req-3", "carol", "", "claude-haiku",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	respBody := `{"model":"claude-haiku-20250301","content":[{"type":"text","text":"pong"}],"usage":{"input_tokens":10,"output_tokens":80}}`
	c.SetNonStreamingResponse([]byte(respBody))
	c.Finish(200, 500)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "claude-haiku-20250301" {
		t.Errorf("model_actual = %q", rec.ModelActual)
	}
	if rec.Messages[1].Content != "pong" {
		t.Errorf("assistant = %q", rec.Messages[1].Content)
	}
	if rec.InputTokens != 10 || rec.OutputTokens != 80 {
		t.Errorf("tokens = (%d, %d), want (10, 80)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: non-streaming OpenAI
// ---------------------------------------------------------------------------

func TestCollectorNonStreamingOpenAI(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}`
	c := NewCollector(w, "9001", "req-4", "dave", "ops", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	respBody := `{"model":"gpt-4o","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":60}}`
	c.SetNonStreamingResponse([]byte(respBody))
	c.Finish(200, 300)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o" {
		t.Errorf("model_actual = %q", rec.ModelActual)
	}
	if rec.InputTokens != 5 || rec.OutputTokens != 60 {
		t.Errorf("tokens = (%d, %d), want (5, 60)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: quality filters
// ---------------------------------------------------------------------------

func TestCollectorFilterErrorStatus(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-err", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"error"}],"usage":{"input_tokens":1,"output_tokens":100}}`))
	c.Finish(500, 100) // 500 → 应被过滤

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	assertNoRecords(t, dir)
}

func TestCollectorFilterMinTokens(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	w.MinOutputTokens = 100 // 要求至少 100 output tokens
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-min", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"short"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	c.Finish(200, 100) // output_tokens=5 < 100 → 过滤

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	assertNoRecords(t, dir)
}

func TestCollectorFilterExcludeGroup(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	w.ExcludeGroups = []string{"test"}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-grp", "alice", "test", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"filtered"}],"usage":{"input_tokens":10,"output_tokens":200}}`))
	c.Finish(200, 100) // group=test → 过滤

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	assertNoRecords(t, dir)
}

func TestCollectorFinishIdempotent(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-idem", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":80}}`))
	c.Finish(200, 100)
	c.Finish(200, 100) // 第二次调用应为 no-op

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	// readAllRecords 确认恰好只有 1 条，第二次 Finish 不得重复写入
	recs := readAllRecords(t, dir)
	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 record (Finish idempotent), got %d", len(recs))
	}
	if recs[0].User != "alice" {
		t.Errorf("unexpected user: %s", recs[0].User)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newCollectorTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "10MB",
		BufferSize:    100,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return w, dir
}

// pollUntilWritten 轮询 WrittenCount 直到达到 expected 或超时。
// 用于替代 time.Sleep，避免在高负载 CI 环境下的 flaky 失败。
func pollUntilWritten(t *testing.T, w *Writer, expected int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.WrittenCount() >= expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("pollUntilWritten: WrittenCount = %d after %v, want >= %d",
		w.WrittenCount(), timeout, expected)
}

// readAllRecords 读取 dir 下今日子目录的所有 JSONL 文件，返回全部记录。
// 与 readFirstRecord 不同，可用于验证"恰好写了 N 条"的幂等性场景。
func readAllRecords(t *testing.T, dir string) []Record {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	pattern := filepath.Join(dir, today, "*.jsonl")
	matches, _ := filepath.Glob(pattern)
	var all []Record
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("readAllRecords: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var rec Record
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("readAllRecords: unmarshal failed: %v\nline: %s", err, line)
			}
			all = append(all, rec)
		}
	}
	return all
}

func readFirstRecord(t *testing.T, dir string) Record {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	pattern := filepath.Join(dir, today, "*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("no corpus files found")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("corpus file is empty")
	}
	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("failed to unmarshal: %v\nline: %s", err, lines[0])
	}
	return rec
}

func assertNoRecords(t *testing.T, dir string) {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	dayDir := filepath.Join(dir, today)
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		// 目录不存在 = 没有记录，符合预期
		return
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dayDir, e.Name()))
		content := strings.TrimSpace(string(data))
		if content != "" {
			t.Errorf("expected no records, but found content in %s:\n%s", e.Name(), content)
		}
	}
}

// ---------------------------------------------------------------------------
// Collector: Anthropic cache tokens
// ---------------------------------------------------------------------------

func TestCollectorAnthropicCacheTokens(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"cached?"}]}`
	c := NewCollector(w, "9000", "req-cache", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// message_start with cache tokens
	c.FeedChunk([]byte(fmt.Sprintf(`data: {"type":"message_start","message":{"model":"claude-sonnet","usage":{"input_tokens":50,"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}}` + "\n\n")))
	c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"cached response"}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":60}}` + "\n\n"))

	c.Finish(200, 1000)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	// input_tokens = 50 + 30 + 20 = 100
	if rec.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100 (50+30+20)", rec.InputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: malformed JSON tolerance
// ---------------------------------------------------------------------------

func TestCollectorMalformedChunks(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"test"}]}`
	c := NewCollector(w, "9000", "req-bad", "alice", "", "claude-sonnet",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// 各种畸形 chunk — 不应 panic
	c.FeedChunk([]byte("data: {invalid json}\n\n"))
	c.FeedChunk([]byte("data: \n\n"))
	c.FeedChunk([]byte(": comment line\n\n"))
	c.FeedChunk([]byte("event: ping\n\n"))
	c.FeedChunk(nil)
	c.FeedChunk([]byte{})

	// 正常 chunk 仍然能工作
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"claude-sonnet","usage":{"input_tokens":10}}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"survived"}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":50}}` + "\n\n"))

	c.Finish(200, 500)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.Messages[len(rec.Messages)-1].Content != "survived" {
		t.Errorf("expected 'survived', got %q", rec.Messages[len(rec.Messages)-1].Content)
	}
}

// ---------------------------------------------------------------------------
// Collector: Ollama provider (uses OpenAI path)
// ---------------------------------------------------------------------------

func TestCollectorOllamaStreaming(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`
	c := NewCollector(w, "9002", "req-ollama", "eve", "", "llama3",
		"http://localhost:11434", "ollama", []byte(reqBody), time.Now())

	c.FeedChunk([]byte(`data: {"model":"llama3:latest","choices":[{"delta":{"content":"Hey"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"llama3:latest","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":40}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 600)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.Provider != "ollama" {
		t.Errorf("provider = %q, want ollama", rec.Provider)
	}
	if rec.ModelActual != "llama3:latest" {
		t.Errorf("model_actual = %q, want llama3:latest", rec.ModelActual)
	}
	if rec.ModelRequested != "llama3" {
		t.Errorf("model_requested = %q", rec.ModelRequested)
	}
	if rec.InputTokens != 8 || rec.OutputTokens != 40 {
		t.Errorf("tokens = (%d, %d), want (8, 40)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: empty assistant text → filtered
// ---------------------------------------------------------------------------

func TestCollectorFilterEmptyAssistant(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-empty", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	// message_start 但无 content_block_delta
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"claude","usage":{"input_tokens":10}}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":50}}` + "\n\n"))
	c.Finish(200, 100)

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	assertNoRecords(t, dir)
}

// ---------------------------------------------------------------------------
// Record: field completeness
// ---------------------------------------------------------------------------

func TestRecordFieldCompleteness(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	start := time.Now()
	reqBody := `{"messages":[{"role":"user","content":"check fields"}]}`
	c := NewCollector(w, "inst-42", "req-fields", "bob", "team-a", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), start)

	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":"response"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[],"usage":{"prompt_tokens":25,"completion_tokens":75}}` + "\n\n"))
	c.Finish(200, 1234)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)

	// 验证所有字段都被正确填充
	if rec.ID == "" {
		t.Error("ID is empty")
	}
	if rec.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
	if rec.Instance != "inst-42" {
		t.Errorf("Instance = %q", rec.Instance)
	}
	if rec.User != "bob" {
		t.Errorf("User = %q", rec.User)
	}
	if rec.Group != "team-a" {
		t.Errorf("Group = %q", rec.Group)
	}
	if rec.ModelRequested != "gpt-4o" {
		t.Errorf("ModelRequested = %q", rec.ModelRequested)
	}
	if rec.ModelActual != "gpt-4o-2024-08-06" {
		t.Errorf("ModelActual = %q", rec.ModelActual)
	}
	if rec.Target != "https://api.openai.com" {
		t.Errorf("Target = %q", rec.Target)
	}
	if rec.Provider != "openai" {
		t.Errorf("Provider = %q", rec.Provider)
	}
	if rec.InputTokens != 25 {
		t.Errorf("InputTokens = %d", rec.InputTokens)
	}
	if rec.OutputTokens != 75 {
		t.Errorf("OutputTokens = %d", rec.OutputTokens)
	}
	if rec.DurationMs != 1234 {
		t.Errorf("DurationMs = %d", rec.DurationMs)
	}
	if len(rec.Messages) != 2 {
		t.Fatalf("Messages len = %d", len(rec.Messages))
	}
	if rec.Messages[0].Role != "user" || rec.Messages[0].Content != "check fields" {
		t.Errorf("Messages[0] = %+v", rec.Messages[0])
	}
	if rec.Messages[1].Role != "assistant" || rec.Messages[1].Content != "response" {
		t.Errorf("Messages[1] = %+v", rec.Messages[1])
	}
}

// ---------------------------------------------------------------------------
// Collector: OpenAI modelFound 防重写——后续 chunk 不应覆盖首次 model
// ---------------------------------------------------------------------------
//
// 这是对已修复 bug 的精确回归测试：
// 当 feedOpenAIChunk 中 "if !c.modelFound && chunk.Model != """" 条件被意外
// 删除时，第三个 chunk 的 "gpt-4o-mini" 会覆盖首次设置的 "gpt-4o-2024-08-06"，
// 导致 rec.ModelActual 错误。此测试能在运行期检测该逻辑 bug。

func TestFeedOpenAIChunkModelNotOverwritten(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	c := NewCollector(w, "9001", "req-modelguard", "alice", "", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	// 第 1 个 chunk：设置真实 model（应被记录）
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":"Hello"}}]}` + "\n\n"))
	// 第 2 个 chunk：携带不同 model 名（应被忽略，因为 modelFound 已为 true）
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06-WRONG","choices":[{"delta":{"content":" world"}}]}` + "\n\n"))
	// 第 3 个 chunk：又一个不同的 model（也应被忽略）
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":50}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 800)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	// 必须是首次 chunk 中的 model，后续 chunk 的 model 不应覆盖
	if rec.ModelActual != "gpt-4o-2024-08-06" {
		t.Errorf("ModelActual = %q, want \"gpt-4o-2024-08-06\" (later chunks must not overwrite first model)", rec.ModelActual)
	}
	// 文本内容应来自两个 content chunk
	if rec.Messages[len(rec.Messages)-1].Content != "Hello world" {
		t.Errorf("assistant content = %q, want \"Hello world\"", rec.Messages[len(rec.Messages)-1].Content)
	}
}

// ---------------------------------------------------------------------------
// Collector: OpenAI model 为空字符串时不触发 modelFound
// ---------------------------------------------------------------------------
//
// 验证 "chunk.Model != """" 条件：首个 chunk model 为空时不应锁定 modelFound，
// 后续携带真实 model 的 chunk 仍应能正常设置 ModelActual。

func TestFeedOpenAIChunkModelEmptyStringSkipped(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}`
	c := NewCollector(w, "9001", "req-emptymodel", "bob", "", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	// 第 1 个 chunk：model 为空字符串，不应设置 modelFound
	c.FeedChunk([]byte(`data: {"model":"","choices":[{"delta":{"content":"part1"}}]}` + "\n\n"))
	// 第 2 个 chunk：携带真实 model（因为 modelFound 仍为 false，应被采纳）
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-11-20","choices":[{"delta":{"content":" part2"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-11-20","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":30}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 400)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o-2024-11-20" {
		t.Errorf("ModelActual = %q, want \"gpt-4o-2024-11-20\" (empty model chunk should not lock modelFound)", rec.ModelActual)
	}
}

// ---------------------------------------------------------------------------
// Collector: Anthropic modelFound 防重写
// ---------------------------------------------------------------------------

func TestFeedAnthropicChunkModelNotOverwritten(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"claude-sonnet","messages":[{"role":"user","content":"hi"}]}`
	c := NewCollector(w, "9000", "req-anthmodelguard", "carol", "", "claude-sonnet",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// 第 1 个 message_start：设置真实 model
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}` + "\n\n"))
	// 第 2 个 message_start（异常场景）：携带不同 model，应被忽略
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"claude-haiku-WRONG","usage":{"input_tokens":0}}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"answer"}}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":60}}` + "\n\n"))

	c.Finish(200, 1000)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "claude-sonnet-4-20250514" {
		t.Errorf("ModelActual = %q, want \"claude-sonnet-4-20250514\"", rec.ModelActual)
	}
}

// ---------------------------------------------------------------------------
// Collector: OpenAI 路径 malformed chunk 后能正常恢复
// ---------------------------------------------------------------------------
//
// 已有 TestCollectorMalformedChunks 只覆盖 Anthropic provider，
// 此测试补充 OpenAI 路径的 malformed 容错验证。

func TestFeedOpenAIChunkMalformedThenRecover(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"recover test"}]}`
	c := NewCollector(w, "9001", "req-oai-malformed", "dave", "", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	// malformed chunks — 不应 panic，不应干扰后续正常 chunk
	c.FeedChunk([]byte("data: {invalid json}\n\n"))
	c.FeedChunk([]byte("data: \n\n"))
	c.FeedChunk(nil)
	c.FeedChunk([]byte{})

	// 正常 chunk：在 malformed 之后仍应被正确处理
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":"recovered"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"gpt-4o-2024-08-06","choices":[],"usage":{"prompt_tokens":15,"completion_tokens":80}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 600)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o-2024-08-06" {
		t.Errorf("ModelActual = %q, want \"gpt-4o-2024-08-06\"", rec.ModelActual)
	}
	if rec.Messages[len(rec.Messages)-1].Content != "recovered" {
		t.Errorf("assistant content = %q, want \"recovered\"", rec.Messages[len(rec.Messages)-1].Content)
	}
	if rec.InputTokens != 15 || rec.OutputTokens != 80 {
		t.Errorf("tokens = (%d, %d), want (15, 80)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: SetNonStreamingResponse 空 body 不 panic，不写入记录
// ---------------------------------------------------------------------------

func TestSetNonStreamingResponseEmptyBody(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`

	// nil body
	c1 := NewCollector(w, "9000", "req-nilbody", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c1.SetNonStreamingResponse(nil) // 不应 panic
	c1.Finish(200, 100)             // 无 assistant text → 被过滤

	// 空 slice body
	c2 := NewCollector(w, "9000", "req-emptybody", "alice", "", "",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())
	c2.SetNonStreamingResponse([]byte{}) // 不应 panic
	c2.Finish(200, 100)                  // 无 assistant text → 被过滤

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	// 两个 collector 均无 assistant text，应全部被过滤
	assertNoRecords(t, dir)
}

// ---------------------------------------------------------------------------
// Writer: WrittenCount 累计计数正确性
// ---------------------------------------------------------------------------

func TestWriterWrittenCount(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "10MB",
		BufferSize:    100,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	makeRecord := func(id string) Record {
		return Record{
			ID:             id,
			Timestamp:      time.Now().UTC(),
			Instance:       "9000",
			User:           "tester",
			ModelRequested: "gpt-4o",
			ModelActual:    "gpt-4o",
			Target:         "https://api.openai.com",
			Provider:       "openai",
			Messages:       []Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}},
			InputTokens:    10,
			OutputTokens:   20,
			DurationMs:     100,
		}
	}

	// 初始值应为 0
	if got := w.WrittenCount(); got != 0 {
		t.Errorf("initial WrittenCount = %d, want 0", got)
	}

	// 提交 5 条
	for i := 0; i < 5; i++ {
		w.Submit(makeRecord(fmt.Sprintf("cr_wc_%02d", i)))
	}
	time.Sleep(200 * time.Millisecond)
	if got := w.WrittenCount(); got != 5 {
		t.Errorf("after 5 submits: WrittenCount = %d, want 5", got)
	}

	// 再提交 3 条
	for i := 5; i < 8; i++ {
		w.Submit(makeRecord(fmt.Sprintf("cr_wc_%02d", i)))
	}
	time.Sleep(200 * time.Millisecond)
	if got := w.WrittenCount(); got != 8 {
		t.Errorf("after 8 submits: WrittenCount = %d, want 8", got)
	}

	// 无丢弃
	if got := w.DroppedCount(); got != 0 {
		t.Errorf("DroppedCount = %d, want 0", got)
	}

	cancel()
	w.Wait()
}

// ---------------------------------------------------------------------------
// contentToString edge cases
// ---------------------------------------------------------------------------

func TestContentToString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain string", `"hello"`, "hello"},
		{"text blocks", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab"},
		{"mixed blocks", `[{"type":"image","text":"x"},{"type":"text","text":"only"}]`, "only"},
		{"empty array", `[]`, ""},
		{"empty string", `""`, ""},
		{"number", `42`, ""},
		{"null", `null`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contentToString([]byte(tt.input))
			if got != tt.want {
				t.Errorf("contentToString(%s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractMessages edge cases
// ---------------------------------------------------------------------------

func TestExtractMessagesEdgeCases(t *testing.T) {
	// malformed JSON
	if msgs := extractMessages([]byte("{bad")); msgs != nil {
		t.Errorf("expected nil for bad JSON, got %v", msgs)
	}
	// no messages field
	if msgs := extractMessages([]byte(`{"model":"x"}`)); len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
	// empty messages array
	msgs := extractMessages([]byte(`{"messages":[]}`))
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// 规则 1 补充：非流式路径 once-set 防重写（parseNonStreamingAnthropic / OpenAI）
// ---------------------------------------------------------------------------
//
// parseNonStreamingAnthropic 和 parseNonStreamingOpenAI 内部也有
// if !c.modelFound && resp.Model != "" 保护，需要验证当 SetNonStreamingResponse
// 被调用两次时（罕见但可能的竞态场景），第二次不会覆盖已设置的 ModelActual。

func TestParseNonStreamingAnthropicModelNotOverwritten(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"q"}]}`
	c := NewCollector(w, "9000", "req-ns-anth-guard", "alice", "", "claude-sonnet",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// 第一次调用：设置 model
	c.SetNonStreamingResponse([]byte(`{"model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"first"}],"usage":{"input_tokens":10,"output_tokens":60}}`))
	// 第二次调用（模拟异常重复调用）：不应覆盖 ModelActual
	c.SetNonStreamingResponse([]byte(`{"model":"claude-haiku-WRONG","content":[{"type":"text","text":" second"}],"usage":{"input_tokens":5,"output_tokens":10}}`))
	c.Finish(200, 500)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "claude-sonnet-4-20250514" {
		t.Errorf("ModelActual = %q, want \"claude-sonnet-4-20250514\" (second call must not overwrite)", rec.ModelActual)
	}
}

func TestParseNonStreamingOpenAIModelNotOverwritten(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"q"}]}`
	c := NewCollector(w, "9001", "req-ns-oai-guard", "bob", "", "gpt-4o",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())

	// 第一次调用：设置 model
	c.SetNonStreamingResponse([]byte(`{"model":"gpt-4o-2024-08-06","choices":[{"message":{"content":"first"}}],"usage":{"prompt_tokens":10,"completion_tokens":60}}`))
	// 第二次调用（模拟异常重复调用）：不应覆盖 ModelActual
	c.SetNonStreamingResponse([]byte(`{"model":"gpt-4o-mini-WRONG","choices":[{"message":{"content":" second"}}],"usage":{"prompt_tokens":5,"completion_tokens":10}}`))
	c.Finish(200, 500)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o-2024-08-06" {
		t.Errorf("ModelActual = %q, want \"gpt-4o-2024-08-06\" (second call must not overwrite)", rec.ModelActual)
	}
}

// ---------------------------------------------------------------------------
// 规则 4 补充：Ollama provider symmetry
// ---------------------------------------------------------------------------

// TestCollectorOllamaMalformedThenRecover 验证 ollama 路径的 malformed chunk 容错。
// ollama 走 OpenAI 代码路径（feedOpenAIChunk），此测试从 provider 语义层面
// 独立确认 ollama 场景的健壮性。
func TestCollectorOllamaMalformedThenRecover(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"llama3","messages":[{"role":"user","content":"recover?"}]}`
	c := NewCollector(w, "9002", "req-ollama-malformed", "eve", "", "llama3",
		"http://localhost:11434", "ollama", []byte(reqBody), time.Now())

	// malformed chunks
	c.FeedChunk([]byte("data: {bad json}\n\n"))
	c.FeedChunk(nil)
	c.FeedChunk([]byte("data: \n\n"))

	// 正常 chunk：malformed 后应能恢复
	c.FeedChunk([]byte(`data: {"model":"llama3:latest","choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
	c.FeedChunk([]byte(`data: {"model":"llama3:latest","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":35}}` + "\n\n"))
	c.FeedChunk([]byte(`data: [DONE]` + "\n\n"))

	c.Finish(200, 400)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", rec.Provider)
	}
	if rec.ModelActual != "llama3:latest" {
		t.Errorf("ModelActual = %q, want \"llama3:latest\"", rec.ModelActual)
	}
	if rec.Messages[len(rec.Messages)-1].Content != "ok" {
		t.Errorf("assistant content = %q, want \"ok\"", rec.Messages[len(rec.Messages)-1].Content)
	}
}

// TestCollectorOllamaNonStreaming 验证 ollama 非流式响应（走 parseNonStreamingOpenAI 路径）。
func TestCollectorOllamaNonStreaming(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`
	c := NewCollector(w, "9002", "req-ollama-ns", "eve", "", "llama3",
		"http://localhost:11434", "ollama", []byte(reqBody), time.Now())

	respBody := `{"model":"llama3:latest","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":4,"completion_tokens":55}}`
	c.SetNonStreamingResponse([]byte(respBody))
	c.Finish(200, 300)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", rec.Provider)
	}
	if rec.ModelActual != "llama3:latest" {
		t.Errorf("ModelActual = %q, want \"llama3:latest\"", rec.ModelActual)
	}
	if rec.Messages[len(rec.Messages)-1].Content != "hello" {
		t.Errorf("assistant content = %q, want \"hello\"", rec.Messages[len(rec.Messages)-1].Content)
	}
	if rec.InputTokens != 4 || rec.OutputTokens != 55 {
		t.Errorf("tokens = (%d, %d), want (4, 55)", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Collector: statusCode 边界 — 400 被过滤，399 通过
// ---------------------------------------------------------------------------

func TestCollectorFilterStatusCode400Boundary(t *testing.T) {
	// 400 → 过滤
	t.Run("400_filtered", func(t *testing.T) {
		w, dir := newCollectorTestWriter(t)
		ctx, cancel := context.WithCancel(context.Background())
		w.Start(ctx)

		reqBody := `{"messages":[{"role":"user","content":"x"}]}`
		c := NewCollector(w, "9000", "req-400", "alice", "", "",
			"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
		c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"bad request"}],"usage":{"input_tokens":5,"output_tokens":100}}`))
		c.Finish(400, 100)

		time.Sleep(100 * time.Millisecond)
		cancel()
		w.Wait()

		assertNoRecords(t, dir)
	})

	// 399 → 通过
	t.Run("399_passes", func(t *testing.T) {
		w, dir := newCollectorTestWriter(t)
		ctx, cancel := context.WithCancel(context.Background())
		w.Start(ctx)
		defer func() { cancel(); w.Wait() }()

		reqBody := `{"messages":[{"role":"user","content":"x"}]}`
		c := NewCollector(w, "9000", "req-399", "alice", "", "",
			"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
		c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"almost error"}],"usage":{"input_tokens":5,"output_tokens":100}}`))
		c.Finish(399, 100)

		pollUntilWritten(t, w, 1, 2*time.Second)
		cancel()
		w.Wait()

		recs := readAllRecords(t, dir)
		if len(recs) != 1 {
			t.Errorf("expected 1 record for statusCode=399, got %d", len(recs))
		}
	})
}

// ---------------------------------------------------------------------------
// Collector: ExcludeGroups=[""] 匹配空字符串 group
// ---------------------------------------------------------------------------

func TestCollectorExcludeGroupEmptyString(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	w.ExcludeGroups = []string{""}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	// group="" 应被 ExcludeGroups=[""] 过滤
	c := NewCollector(w, "9000", "req-emptygrp", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"filtered"}],"usage":{"input_tokens":10,"output_tokens":200}}`))
	c.Finish(200, 100)

	time.Sleep(100 * time.Millisecond)
	cancel()
	w.Wait()

	assertNoRecords(t, dir)
}

// ---------------------------------------------------------------------------
// Collector: 非流式 Anthropic cache tokens 累积
// ---------------------------------------------------------------------------

func TestCollectorNonStreamingAnthropicCacheTokens(t *testing.T) {
	w, dir := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"cached?"}]}`
	c := NewCollector(w, "9000", "req-ns-cache", "alice", "", "claude-sonnet",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

	// 非流式 Anthropic 响应包含 cache token 字段
	respBody := `{"model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"cached answer"}],"usage":{"input_tokens":50,"output_tokens":80,"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}`
	c.SetNonStreamingResponse([]byte(respBody))
	c.Finish(200, 600)

	pollUntilWritten(t, w, 1, 2*time.Second)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	// input_tokens = 50 + 30 + 20 = 100
	if rec.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100 (50+30+20)", rec.InputTokens)
	}
	if rec.OutputTokens != 80 {
		t.Errorf("output_tokens = %d, want 80", rec.OutputTokens)
	}
	if rec.ModelActual != "claude-sonnet-4-20250514" {
		t.Errorf("model_actual = %q", rec.ModelActual)
	}
}

// ---------------------------------------------------------------------------
// Collector: FeedChunk / SetNonStreamingResponse 在 Finish 之后调用不 panic
// ---------------------------------------------------------------------------

func TestCollectorFeedChunkAfterFinish(t *testing.T) {
	w, _ := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-after-finish", "alice", "", "",
		"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":60}}`))
	c.Finish(200, 100)

	// Finish 之后再调用 FeedChunk — 不应 panic，WrittenCount 应保持 1
	c.FeedChunk([]byte(`data: {"type":"message_start","message":{"model":"x","usage":{"input_tokens":1}}}` + "\n\n"))
	c.FeedChunk(nil)

	pollUntilWritten(t, w, 1, 2*time.Second)

	if w.WrittenCount() != 1 {
		t.Errorf("WrittenCount = %d, want 1 (FeedChunk after Finish must not produce extra records)", w.WrittenCount())
	}
}

func TestCollectorSetNonStreamingAfterFinish(t *testing.T) {
	w, _ := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	reqBody := `{"messages":[{"role":"user","content":"x"}]}`
	c := NewCollector(w, "9000", "req-sns-after-finish", "alice", "", "",
		"https://api.openai.com", "openai", []byte(reqBody), time.Now())
	c.SetNonStreamingResponse([]byte(`{"model":"gpt-4o","choices":[{"message":{"content":"first"}}],"usage":{"prompt_tokens":5,"completion_tokens":60}}`))
	c.Finish(200, 200)

	// Finish 之后再调用 SetNonStreamingResponse — 不应 panic，不产生第二条记录
	c.SetNonStreamingResponse([]byte(`{"model":"gpt-4o","choices":[{"message":{"content":"second"}}],"usage":{"prompt_tokens":5,"completion_tokens":60}}`))

	pollUntilWritten(t, w, 1, 2*time.Second)

	if w.WrittenCount() != 1 {
		t.Errorf("WrittenCount = %d, want 1 (SetNonStreamingResponse after Finish must not produce extra records)", w.WrittenCount())
	}
}

// ---------------------------------------------------------------------------
// Collector: concurrent FeedChunk + Finish (P0 race test)
// ---------------------------------------------------------------------------

func TestCollectorConcurrentFeedAndFinish(t *testing.T) {
	w, _ := newCollectorTestWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	const goroutines = 10
	done := make(chan struct{}, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()

			reqBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"q%d"}]}`, id)
			c := NewCollector(w, "9000", fmt.Sprintf("req-conc-%02d", id), "alice", "", "claude",
				"https://api.anthropic.com", "anthropic", []byte(reqBody), time.Now())

			// Interleave FeedChunk and Finish from different goroutines
			c.FeedChunk([]byte(fmt.Sprintf(`data: {"type":"message_start","message":{"model":"claude-sonnet","usage":{"input_tokens":%d}}}`, id+1) + "\n\n"))
			c.FeedChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}` + "\n\n"))
			c.FeedChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":50}}` + "\n\n"))
			c.Finish(200, 100)
			// Extra Finish call — must be no-op, no panic
			c.Finish(200, 100)
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	// All goroutines submitted; wait for writes
	pollUntilWritten(t, w, goroutines, 5*time.Second)

	if got := w.WrittenCount(); got != goroutines {
		t.Errorf("WrittenCount = %d, want %d", got, goroutines)
	}
}
