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
// parseMaxFileSize
// ---------------------------------------------------------------------------

func TestParseMaxFileSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		err   bool
	}{
		{"200MB", 200 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"512KB", 512 * 1024, false},
		{"1024", 1024, false},
		{"0", 0, false},
		{"", 0, false},
		{"-1MB", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseMaxFileSize(tt.input)
		if tt.err {
			if err == nil {
				t.Errorf("parseMaxFileSize(%q) expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMaxFileSize(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseMaxFileSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Writer: basic submit + flush
// ---------------------------------------------------------------------------

func newTestWriter(t *testing.T, bufSize int) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "10MB",
		BufferSize:    bufSize,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatal(err)
	}
	return w, dir
}

func TestWriterSubmitAndShutdown(t *testing.T) {
	w, dir := newTestWriter(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// 提交几条记录
	for i := 0; i < 5; i++ {
		w.Submit(Record{
			ID:             "cr_test_" + string(rune('a'+i)),
			Timestamp:      time.Now().UTC(),
			Instance:       "9000",
			User:           "alice",
			ModelRequested: "claude-sonnet",
			ModelActual:    "claude-sonnet",
			Target:         "https://api.anthropic.com",
			Provider:       "anthropic",
			Messages:       []Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}},
			InputTokens:    100,
			OutputTokens:   50,
			DurationMs:     1000,
		})
	}

	// 等待 flush
	time.Sleep(200 * time.Millisecond)
	cancel()
	w.Wait()

	// 验证文件存在且包含 5 行
	today := time.Now().UTC().Format("2006-01-02")
	pattern := filepath.Join(dir, today, "sproxy_9000.jsonl")
	data, err := os.ReadFile(pattern)
	if err != nil {
		t.Fatalf("failed to read corpus file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}

	// 验证 JSON 可解析
	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Errorf("failed to unmarshal record: %v", err)
	}
	if rec.User != "alice" {
		t.Errorf("expected user alice, got %s", rec.User)
	}
}

func TestWriterDropWhenFull(t *testing.T) {
	w, _ := newTestWriter(t, 1) // bufferSize=1, channel cap=2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 不启动 writer，channel 不会被消费
	_ = ctx

	// 填满 channel（cap=2）+ 额外提交应被丢弃
	for i := 0; i < 10; i++ {
		w.Submit(Record{ID: "cr_drop"})
	}

	if w.DroppedCount() == 0 {
		t.Error("expected some records to be dropped")
	}
}

// ---------------------------------------------------------------------------
// Writer: date rotation
// ---------------------------------------------------------------------------

func TestWriterFileRotationBySize(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "500", // 500 bytes — 很小，强制轮转
		BufferSize:    100,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// 提交足够多的记录触发轮转
	for i := 0; i < 20; i++ {
		w.Submit(Record{
			ID:             "cr_rot_" + string(rune('a'+i)),
			Timestamp:      time.Now().UTC(),
			Instance:       "9000",
			User:           "bob",
			ModelRequested: "gpt-4o",
			Target:         "https://api.openai.com",
			Provider:       "openai",
			Messages:       []Message{{Role: "user", Content: "test rotation with enough content to exceed 500 bytes"}},
			InputTokens:    100,
			OutputTokens:   50,
			DurationMs:     500,
		})
	}

	time.Sleep(300 * time.Millisecond)
	cancel()
	w.Wait()

	// 验证产生了多个文件
	today := time.Now().UTC().Format("2006-01-02")
	entries, _ := os.ReadDir(filepath.Join(dir, today))
	if len(entries) < 2 {
		t.Errorf("expected multiple rotated files, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// Writer: graceful shutdown drains pending records
// ---------------------------------------------------------------------------

func TestWriterGracefulShutdownDrain(t *testing.T) {
	w, dir := newTestWriter(t, 1000) // 大 buffer，不会自动 flush
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// 提交记录后立即 cancel — 依赖 drain 逻辑写入
	for i := 0; i < 3; i++ {
		w.Submit(Record{
			ID:             fmt.Sprintf("cr_drain_%d", i),
			Timestamp:      time.Now().UTC(),
			Instance:       "9000",
			User:           "alice",
			ModelRequested: "claude",
			ModelActual:    "claude",
			Target:         "https://api.anthropic.com",
			Provider:       "anthropic",
			Messages:       []Message{{Role: "user", Content: "drain test"}},
			InputTokens:    10,
			OutputTokens:   20,
			DurationMs:     100,
		})
	}

	// 立即取消，不等 flush interval
	cancel()
	w.Wait()

	// 验证所有 3 条记录都被 drain 写入
	today := time.Now().UTC().Format("2006-01-02")
	pattern := filepath.Join(dir, today, "*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("no corpus files after drain")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 drained records, got %d", len(lines))
	}
}

// ---------------------------------------------------------------------------
// Writer: invalid config
// ---------------------------------------------------------------------------

func TestWriterInvalidMaxFileSize(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          t.TempDir(),
		MaxFileSize:   "abc",
		BufferSize:    10,
		FlushInterval: 50 * time.Millisecond,
	}
	_, err := New(logger, cfg, "9000")
	if err == nil {
		t.Error("expected error for invalid max_file_size")
	}
}

func TestWriterNegativeMaxFileSize(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          t.TempDir(),
		MaxFileSize:   "-5MB",
		BufferSize:    10,
		FlushInterval: 50 * time.Millisecond,
	}
	_, err := New(logger, cfg, "9000")
	if err == nil {
		t.Error("expected error for negative max_file_size")
	}
}
