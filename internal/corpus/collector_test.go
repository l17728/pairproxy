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

	// 等待 flush
	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "claude-haiku-20250301" {
		t.Errorf("model_actual = %q", rec.ModelActual)
	}
	if rec.Messages[1].Content != "pong" {
		t.Errorf("assistant = %q", rec.Messages[1].Content)
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

	time.Sleep(200 * time.Millisecond)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.ModelActual != "gpt-4o" {
		t.Errorf("model_actual = %q", rec.ModelActual)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
	cancel()
	w.Wait()

	rec := readFirstRecord(t, dir)
	if rec.User != "alice" {
		t.Errorf("unexpected user: %s", rec.User)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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

	time.Sleep(200 * time.Millisecond)
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
