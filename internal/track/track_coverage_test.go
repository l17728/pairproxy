package track

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Tracker.Dir (0% → covered)
// ---------------------------------------------------------------------------

func TestTracker_Dir_ReturnsConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	trackDir := filepath.Join(dir, "track")
	tr, err := New(trackDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr.Dir() != trackDir {
		t.Errorf("Dir() = %q, want %q", tr.Dir(), trackDir)
	}
}

// ---------------------------------------------------------------------------
// Tracker.New — directory-creation failure branch
// ---------------------------------------------------------------------------

func TestTracker_New_CreatesSubdirectories(t *testing.T) {
	dir := t.TempDir()
	trackDir := filepath.Join(dir, "nested", "track")
	tr, err := New(trackDir)
	if err != nil {
		t.Fatalf("New with nested path: %v", err)
	}
	if tr.Dir() != trackDir {
		t.Errorf("Dir() = %q, want %q", tr.Dir(), trackDir)
	}
	// Verify subdirs exist
	for _, sub := range []string{usersDir(trackDir), convsDir(trackDir)} {
		if _, err := os.Stat(sub); err != nil {
			t.Errorf("subdir %q not created: %v", sub, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Tracker.Enable — validates invalid username paths
// ---------------------------------------------------------------------------

func TestTracker_Enable_InvalidUsername_Empty(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Enable(""); err == nil {
		t.Error("Enable with empty username should return error")
	}
}

func TestTracker_Enable_InvalidUsername_DotDot(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Enable("../escape"); err == nil {
		t.Error("Enable with path traversal username should return error")
	}
}

func TestTracker_Enable_InvalidUsername_Slash(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Enable("a/b"); err == nil {
		t.Error("Enable with slash in username should return error")
	}
}

func TestTracker_Enable_InvalidUsername_Backslash(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Enable("a\\b"); err == nil {
		t.Error("Enable with backslash in username should return error")
	}
}

// ---------------------------------------------------------------------------
// Tracker.Disable — validates invalid username paths
// ---------------------------------------------------------------------------

func TestTracker_Disable_InvalidUsername_Empty(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Disable(""); err == nil {
		t.Error("Disable with empty username should return error")
	}
}

func TestTracker_Disable_InvalidUsername_DotDot(t *testing.T) {
	tr := openTestTracker(t)
	if err := tr.Disable("../evil"); err == nil {
		t.Error("Disable with path traversal should return error")
	}
}

// ---------------------------------------------------------------------------
// Tracker.ListTracked — directory-contains-subdirectory scenario
// ---------------------------------------------------------------------------

func TestTracker_ListTracked_IgnoresSubdirectories(t *testing.T) {
	tr := openTestTracker(t)

	// Create a legitimate tracked user
	if err := tr.Enable("alice"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Manually create a subdirectory inside the users dir (should be ignored)
	subdir := filepath.Join(usersDir(tr.dir), "not-a-user-dir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	got, err := tr.ListTracked()
	if err != nil {
		t.Fatalf("ListTracked: %v", err)
	}
	// Only alice should appear, not the directory
	for _, name := range got {
		if name == "not-a-user-dir" {
			t.Error("ListTracked should not include subdirectories")
		}
	}
	if len(got) != 1 || got[0] != "alice" {
		t.Errorf("expected [alice], got %v", got)
	}
}

// ---------------------------------------------------------------------------
// CaptureSession.doFlush — directory auto-creation
// ---------------------------------------------------------------------------

func TestCaptureSession_DoFlush_CreatesDir(t *testing.T) {
	base := t.TempDir()
	// Use a non-existent nested directory — doFlush should create it
	convDir := filepath.Join(base, "deep", "nested", "conv")
	cs := NewCaptureSession(convDir, "req-auto-dir", "alice", []byte("{}"), "anthropic")
	cs.Flush()

	entries, err := os.ReadDir(convDir)
	if err != nil {
		t.Fatalf("ReadDir after Flush: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 file in auto-created dir, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// extractAnthropicChunkText — uncovered branches
// ---------------------------------------------------------------------------

func TestExtractAnthropicChunkText_InvalidJSON(t *testing.T) {
	text, done := extractAnthropicChunkText([]byte("{invalid"))
	if text != "" || done {
		t.Errorf("invalid JSON: got text=%q done=%v, want empty/false", text, done)
	}
}

func TestExtractAnthropicChunkText_NonTextDelta(t *testing.T) {
	// content_block_delta with a non-text_delta type should return empty
	payload := []byte(`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}`)
	text, done := extractAnthropicChunkText(payload)
	if text != "" || done {
		t.Errorf("non-text delta: got text=%q done=%v, want empty/false", text, done)
	}
}

func TestExtractAnthropicChunkText_NilDelta(t *testing.T) {
	// content_block_delta with null delta
	payload := []byte(`{"type":"content_block_delta","delta":null}`)
	text, done := extractAnthropicChunkText(payload)
	if text != "" || done {
		t.Errorf("nil delta: got text=%q done=%v, want empty/false", text, done)
	}
}

func TestExtractAnthropicChunkText_OtherType(t *testing.T) {
	payload := []byte(`{"type":"message_start","message":{}}`)
	text, done := extractAnthropicChunkText(payload)
	if text != "" || done {
		t.Errorf("other type: got text=%q done=%v, want empty/false", text, done)
	}
}

// ---------------------------------------------------------------------------
// extractSSEText — uncovered branches
// ---------------------------------------------------------------------------

func TestExtractSSEText_EmptyDataLine(t *testing.T) {
	// Line "data:" with nothing after it should be skipped
	chunk := []byte("data:\n\n")
	text, done := extractSSEText(chunk, "anthropic")
	if text != "" || done {
		t.Errorf("empty data line: got text=%q done=%v", text, done)
	}
}

func TestExtractSSEText_NonDataLine(t *testing.T) {
	// Lines not starting with "data:" are skipped
	chunk := []byte("event: content_block_delta\n")
	text, done := extractSSEText(chunk, "anthropic")
	if text != "" || done {
		t.Errorf("non-data line: got text=%q done=%v", text, done)
	}
}

func TestExtractSSEText_DoneWithOpenAI(t *testing.T) {
	// [DONE] sentinel works for openai provider
	chunk := []byte("data: [DONE]\n\n")
	_, done := extractSSEText(chunk, "openai")
	if !done {
		t.Error("expected done=true for [DONE] sentinel")
	}
}

func TestExtractSSEText_OpenAI_FinishReason_Stop(t *testing.T) {
	// OpenAI chunk with finish_reason — currently extractOpenAIChunkText returns
	// done=false always (no finish_reason handling in code), so just verify no panic.
	chunk := []byte(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\n\n")
	text, _ := extractSSEText(chunk, "openai")
	if text != "hi" {
		t.Errorf("expected text 'hi', got %q", text)
	}
}

func TestExtractSSEText_OpenAI_InvalidJSON(t *testing.T) {
	chunk := []byte("data: {invalid}\n\n")
	text, done := extractSSEText(chunk, "openai")
	if text != "" || done {
		t.Errorf("invalid JSON openai: got text=%q done=%v", text, done)
	}
}

func TestExtractSSEText_CRLF(t *testing.T) {
	// Lines terminated with \r\n
	chunk := []byte("data: {\"type\":\"message_stop\"}\r\n\r\n")
	_, done := extractSSEText(chunk, "anthropic")
	if !done {
		t.Error("expected done=true for message_stop with CRLF line ending")
	}
}

// ---------------------------------------------------------------------------
// extractMessages — edge cases
// ---------------------------------------------------------------------------

func TestExtractMessages_EmptyBody(t *testing.T) {
	msgs := extractMessages(nil)
	if msgs != nil {
		t.Errorf("nil body: expected nil messages, got %v", msgs)
	}
}

func TestExtractMessages_EmptyBodyBytes(t *testing.T) {
	msgs := extractMessages([]byte{})
	if msgs != nil {
		t.Errorf("empty body: expected nil messages, got %v", msgs)
	}
}

func TestExtractMessages_InvalidJSON(t *testing.T) {
	msgs := extractMessages([]byte("{invalid}"))
	if msgs != nil {
		t.Errorf("invalid JSON: expected nil messages, got %v", msgs)
	}
}

func TestExtractMessages_NoMessages(t *testing.T) {
	msgs := extractMessages([]byte(`{"model":"claude-3"}`))
	if len(msgs) != 0 {
		t.Errorf("no messages key: expected 0 messages, got %v", msgs)
	}
}

// ---------------------------------------------------------------------------
// extractModel — edge cases
// ---------------------------------------------------------------------------

func TestExtractModel_EmptyBody(t *testing.T) {
	model := extractModel(nil)
	if model != "" {
		t.Errorf("nil body: expected empty model, got %q", model)
	}
}

func TestExtractModel_EmptyBodyBytes(t *testing.T) {
	model := extractModel([]byte{})
	if model != "" {
		t.Errorf("empty body: expected empty model, got %q", model)
	}
}

func TestExtractModel_NoModelField(t *testing.T) {
	model := extractModel([]byte(`{"messages":[]}`))
	if model != "" {
		t.Errorf("no model field: expected empty, got %q", model)
	}
}

// ---------------------------------------------------------------------------
// extractNonStreamingText — missing branches
// ---------------------------------------------------------------------------

func TestExtractNonStreamingText_EmptyBody(t *testing.T) {
	text := extractNonStreamingText(nil, "anthropic")
	if text != "" {
		t.Errorf("nil body: expected empty, got %q", text)
	}
}

func TestExtractNonStreamingText_OpenAI_EmptyChoices(t *testing.T) {
	body := []byte(`{"choices":[]}`)
	text := extractNonStreamingText(body, "openai")
	if text != "" {
		t.Errorf("empty choices: expected empty, got %q", text)
	}
}

func TestExtractNonStreamingText_Anthropic_InvalidJSON(t *testing.T) {
	text := extractNonStreamingText([]byte("{invalid}"), "anthropic")
	if text != "" {
		t.Errorf("invalid JSON: expected empty, got %q", text)
	}
}

func TestExtractNonStreamingText_Anthropic_MultipleBlocks(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}`)
	text := extractNonStreamingText(body, "anthropic")
	if text != "Hello world" {
		t.Errorf("multiple blocks: got %q, want %q", text, "Hello world")
	}
}

func TestExtractNonStreamingText_Anthropic_NonTextBlocks(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","id":"t1"},{"type":"text","text":"ok"}]}`)
	text := extractNonStreamingText(body, "anthropic")
	if text != "ok" {
		t.Errorf("non-text blocks: got %q, want %q", text, "ok")
	}
}

// ---------------------------------------------------------------------------
// extractNonStreamingTokens — missing branches
// ---------------------------------------------------------------------------

func TestExtractNonStreamingTokens_EmptyBody(t *testing.T) {
	in, out := extractNonStreamingTokens(nil, "anthropic")
	if in != 0 || out != 0 {
		t.Errorf("nil body: expected 0,0, got %d,%d", in, out)
	}
}

func TestExtractNonStreamingTokens_OpenAI_NilUsage(t *testing.T) {
	body := []byte(`{"choices":[]}`)
	in, out := extractNonStreamingTokens(body, "openai")
	if in != 0 || out != 0 {
		t.Errorf("nil usage: expected 0,0, got %d,%d", in, out)
	}
}

func TestExtractNonStreamingTokens_Anthropic_NilUsage(t *testing.T) {
	body := []byte(`{"content":[]}`)
	in, out := extractNonStreamingTokens(body, "anthropic")
	if in != 0 || out != 0 {
		t.Errorf("anthropic nil usage: expected 0,0, got %d,%d", in, out)
	}
}

func TestExtractNonStreamingTokens_Ollama_Provider(t *testing.T) {
	// "ollama" uses the openai branch
	body := []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	in, out := extractNonStreamingTokens(body, "ollama")
	if in != 5 || out != 3 {
		t.Errorf("ollama: got %d,%d, want 5,3", in, out)
	}
}

// ---------------------------------------------------------------------------
// contentToString — edge cases
// ---------------------------------------------------------------------------

func TestContentToString_NullRaw(t *testing.T) {
	result := contentToString(json.RawMessage("null"))
	// null decodes to empty string (not a plain string, not blocks)
	// The code tries json.Unmarshal to string (fails), then to blocks (empty).
	if result != "" {
		t.Errorf("null raw: expected empty, got %q", result)
	}
}

func TestContentToString_EmptyRaw(t *testing.T) {
	result := contentToString(json.RawMessage(nil))
	if result != "" {
		t.Errorf("nil raw: expected empty, got %q", result)
	}
}

func TestContentToString_BlocksNonText(t *testing.T) {
	raw := json.RawMessage(`[{"type":"image","source":{}},{"type":"text","text":"hello"}]`)
	result := contentToString(raw)
	if result != "hello" {
		t.Errorf("blocks with non-text: got %q, want 'hello'", result)
	}
}

func TestContentToString_InvalidNotStringNotBlocks(t *testing.T) {
	// A number — not a string, not an array of blocks
	raw := json.RawMessage(`42`)
	result := contentToString(raw)
	if result != "" {
		t.Errorf("number raw: expected empty, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// CaptureSession.Flush — textBuf fallback for streaming session
// ---------------------------------------------------------------------------

func TestCaptureSession_Flush_TextBufFallback(t *testing.T) {
	dir := t.TempDir()
	cs := NewCaptureSession(dir, "req-fallback", "alice", []byte("{}"), "anthropic")

	// Feed a chunk that adds text but doesn't finish the stream
	chunk := []byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
	cs.FeedChunk(chunk)

	// Manually flush without a message_stop — should write textBuf content
	cs.Flush()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var rec ConversationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if rec.Response != "partial" {
		t.Errorf("textBuf fallback: got response %q, want 'partial'", rec.Response)
	}
}

// ---------------------------------------------------------------------------
// CaptureSession.Flush — already flushed (skip double-write)
// ---------------------------------------------------------------------------

func TestCaptureSession_Flush_SkipsIfAlreadyFlushed(t *testing.T) {
	dir := t.TempDir()
	cs := NewCaptureSession(dir, "req-skip", "alice", []byte("{}"), "anthropic")

	cs.Flush()
	cs.Flush() // second call should be no-op

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("double Flush: expected 1 file, got %d", len(entries))
	}
}
