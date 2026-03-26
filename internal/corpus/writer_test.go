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

// ---------------------------------------------------------------------------
// Writer: relative path is resolved to absolute path
// ---------------------------------------------------------------------------
//
// 验证 corpus.path 为相对路径时，Writer 会将其转换为绝对路径，
// 确保集群中各节点不受 CWD 影响而写入到意外位置。

func TestWriterRelativePathResolvedToAbsolute(t *testing.T) {
	// 使用系统临时目录作为基准，构造一个相对路径
	// （t.TempDir() 返回绝对路径，需手动构造相对路径场景）
	absBase := t.TempDir()

	// 切换工作目录到 absBase，使得 "./subdir" 为有效相对路径
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(absBase); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          "./corpus-rel",  // 相对路径
		MaxFileSize:   "10MB",
		BufferSize:    10,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// basePath 必须是绝对路径
	if !filepath.IsAbs(w.basePath) {
		t.Errorf("basePath should be absolute, got %q", w.basePath)
	}

	// 实际路径必须等于 absBase/corpus-rel
	wantAbs := filepath.Join(absBase, "corpus-rel")
	if w.basePath != wantAbs {
		t.Errorf("basePath = %q, want %q", w.basePath, wantAbs)
	}

	// 确认目录已被创建
	if _, statErr := os.Stat(w.basePath); os.IsNotExist(statErr) {
		t.Errorf("base dir %q was not created", w.basePath)
	}
}

// TestWriterAbsolutePathUnchanged 验证绝对路径配置时不会被修改。
func TestWriterAbsolutePathUnchanged(t *testing.T) {
	absDir := t.TempDir()
	logger := zaptest.NewLogger(t)
	cfg := config.CorpusConfig{
		Path:          absDir, // 已是绝对路径
		MaxFileSize:   "10MB",
		BufferSize:    10,
		FlushInterval: 50 * time.Millisecond,
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if w.basePath != absDir {
		t.Errorf("basePath = %q, want %q (absolute path should be unchanged)", w.basePath, absDir)
	}
}
// Writer: multi-instance concurrent writes (cluster scenario)
// ---------------------------------------------------------------------------
//
// 模拟集群部署场景：两个 Writer 实例（分别代表 primary:9000 和 worker:9001）
// 并发写入同一个 basePath 目录（模拟共享存储或在同一宿主机上测试）。
// 验证：
//   1. 两者生成的文件名互不冲突（sproxy_9000.jsonl vs sproxy_9001.jsonl）
//   2. 并发写入不发生 race condition（go test -race 覆盖）
//   3. 各自文件包含且仅包含本实例提交的记录数

func TestWriterMultiInstanceConcurrent(t *testing.T) {
	// 使用同一个 basePath 模拟共享存储场景
	sharedDir := t.TempDir()
	logger := zaptest.NewLogger(t)

	makeCfg := func() config.CorpusConfig {
		return config.CorpusConfig{
			Path:          sharedDir,
			MaxFileSize:   "10MB",
			BufferSize:    100,
			FlushInterval: 50 * time.Millisecond,
		}
	}

	// 创建两个独立的 Writer，分别代表 primary(9000) 和 worker(9001)
	wPrimary, err := New(logger, makeCfg(), "9000")
	if err != nil {
		t.Fatalf("failed to create primary writer: %v", err)
	}
	wWorker, err := New(logger, makeCfg(), "9001")
	if err != nil {
		t.Fatalf("failed to create worker writer: %v", err)
	}

	ctxPrimary, cancelPrimary := context.WithCancel(context.Background())
	ctxWorker, cancelWorker := context.WithCancel(context.Background())
	wPrimary.Start(ctxPrimary)
	wWorker.Start(ctxWorker)

	const recordsPerInstance = 10

	// 并发提交记录：两个 goroutine 同时写入各自的 Writer
	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < recordsPerInstance; i++ {
			wPrimary.Submit(Record{
				ID:             fmt.Sprintf("cr_primary_%02d", i),
				Timestamp:      time.Now().UTC(),
				Instance:       "9000",
				User:           "alice",
				ModelRequested: "claude-sonnet",
				ModelActual:    "claude-sonnet",
				Target:         "https://api.anthropic.com",
				Provider:       "anthropic",
				Messages:       []Message{{Role: "user", Content: "primary msg"}, {Role: "assistant", Content: "primary resp"}},
				InputTokens:    100,
				OutputTokens:   80,
				DurationMs:     500,
			})
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < recordsPerInstance; i++ {
			wWorker.Submit(Record{
				ID:             fmt.Sprintf("cr_worker_%02d", i),
				Timestamp:      time.Now().UTC(),
				Instance:       "9001",
				User:           "bob",
				ModelRequested: "gpt-4o",
				ModelActual:    "gpt-4o",
				Target:         "https://api.openai.com",
				Provider:       "openai",
				Messages:       []Message{{Role: "user", Content: "worker msg"}, {Role: "assistant", Content: "worker resp"}},
				InputTokens:    50,
				OutputTokens:   60,
				DurationMs:     300,
			})
		}
	}()

	// 等待两个 goroutine 完成提交
	<-done
	<-done

	// 等待 flush interval 后关闭
	time.Sleep(200 * time.Millisecond)
	cancelPrimary()
	cancelWorker()
	wPrimary.Wait()
	wWorker.Wait()

	today := time.Now().UTC().Format("2006-01-02")
	dayDir := filepath.Join(sharedDir, today)

	// 验证文件名不冲突：两个实例各自生成独立文件
	primaryFile := filepath.Join(dayDir, "sproxy_9000.jsonl")
	workerFile := filepath.Join(dayDir, "sproxy_9001.jsonl")

	primaryData, err := os.ReadFile(primaryFile)
	if err != nil {
		t.Fatalf("primary corpus file not found (%s): %v", primaryFile, err)
	}
	workerData, err := os.ReadFile(workerFile)
	if err != nil {
		t.Fatalf("worker corpus file not found (%s): %v", workerFile, err)
	}

	// 验证记录数：各自包含且仅包含本实例的记录
	primaryLines := strings.Split(strings.TrimSpace(string(primaryData)), "\n")
	if len(primaryLines) != recordsPerInstance {
		t.Errorf("primary: expected %d records, got %d", recordsPerInstance, len(primaryLines))
	}
	workerLines := strings.Split(strings.TrimSpace(string(workerData)), "\n")
	if len(workerLines) != recordsPerInstance {
		t.Errorf("worker: expected %d records, got %d", recordsPerInstance, len(workerLines))
	}

	// 验证记录内容：primary 文件只含 instance=9000，worker 文件只含 instance=9001
	for i, line := range primaryLines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("primary line %d: unmarshal failed: %v", i, err)
			continue
		}
		if rec.Instance != "9000" {
			t.Errorf("primary line %d: expected instance=9000, got %s", i, rec.Instance)
		}
		if rec.User != "alice" {
			t.Errorf("primary line %d: expected user=alice, got %s", i, rec.User)
		}
	}
	for i, line := range workerLines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("worker line %d: unmarshal failed: %v", i, err)
			continue
		}
		if rec.Instance != "9001" {
			t.Errorf("worker line %d: expected instance=9001, got %s", i, rec.Instance)
		}
		if rec.User != "bob" {
			t.Errorf("worker line %d: expected user=bob, got %s", i, rec.User)
		}
	}

	// 验证目录中恰好只有这两个文件（无交叉污染）
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatalf("cannot read day dir: %v", err)
	}
	jsonlCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonlCount++
		}
	}
	if jsonlCount != 2 {
		t.Errorf("expected exactly 2 jsonl files in shared dir, got %d", jsonlCount)
	}
}

// ---------------------------------------------------------------------------
// Writer: QueueDepth 返回值正确性（规则 3 — Exported API）
// ---------------------------------------------------------------------------

func TestWriterQueueDepth(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	// 使用极小 buffer + 极长 flush interval，使记录在 channel 中积压
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "10MB",
		BufferSize:    5,
		FlushInterval: 10 * time.Second, // 不会自动 flush
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// 未 Start，直接验证初始 QueueDepth
	if got := w.QueueDepth(); got != 0 {
		t.Errorf("initial QueueDepth = %d, want 0", got)
	}

	// channel 容量 = BufferSize*2 = 10，直接往 channel 里发而不消费
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	makeRecord := func(id string) Record {
		return Record{
			ID: id, Timestamp: time.Now().UTC(), Instance: "9000",
			User: "tester", ModelRequested: "gpt-4o", ModelActual: "gpt-4o",
			Target: "https://api.openai.com", Provider: "openai",
			Messages:     []Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}},
			InputTokens:  5,
			OutputTokens: 10,
			DurationMs:   50,
		}
	}

	// 提交少量记录后，QueueDepth 应 >= 0（可能已被消费）
	for i := 0; i < 3; i++ {
		w.Submit(makeRecord(fmt.Sprintf("cr_qd_%02d", i)))
	}
	// QueueDepth 是瞬时值，只需不超过 channel 容量且不为负
	depth := w.QueueDepth()
	if depth < 0 {
		t.Errorf("QueueDepth = %d, must not be negative", depth)
	}
	if depth > cap(w.ch) {
		t.Errorf("QueueDepth = %d, exceeds channel capacity %d", depth, cap(w.ch))
	}

	cancel()
	w.Wait()

	// 停止后 channel 已清空，QueueDepth 应为 0
	if got := w.QueueDepth(); got != 0 {
		t.Errorf("QueueDepth after shutdown = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Writer: DroppedCount 专项验证（规则 3 — Exported API，从间接提升为直接）
// ---------------------------------------------------------------------------

func TestWriterDroppedCount(t *testing.T) {
	dir := t.TempDir()
	logger := zaptest.NewLogger(t)
	// channel 容量 = BufferSize*2 = 2，极小以触发丢弃
	cfg := config.CorpusConfig{
		Path:          dir,
		MaxFileSize:   "10MB",
		BufferSize:    1,
		FlushInterval: 10 * time.Second, // 长 interval，不自动消费
	}
	w, err := New(logger, cfg, "9000")
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// 初始丢弃计数为 0
	if got := w.DroppedCount(); got != 0 {
		t.Errorf("initial DroppedCount = %d, want 0", got)
	}

	// 不启动 goroutine（不 Start），直接塞满 channel 再继续提交触发丢弃
	makeRecord := func(id string) Record {
		return Record{
			ID: id, Timestamp: time.Now().UTC(), Instance: "9000",
			User: "tester", ModelRequested: "gpt-4o", ModelActual: "gpt-4o",
			Target: "https://api.openai.com", Provider: "openai",
			Messages:     []Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}},
			InputTokens:  5,
			OutputTokens: 10,
			DurationMs:   50,
		}
	}

	// 填满 channel（容量 = 2）
	w.Submit(makeRecord("cr_drop_00"))
	w.Submit(makeRecord("cr_drop_01"))

	// 第 3、4 条必须被丢弃
	w.Submit(makeRecord("cr_drop_02"))
	w.Submit(makeRecord("cr_drop_03"))

	dropped := w.DroppedCount()
	if dropped < 2 {
		t.Errorf("DroppedCount = %d, want >= 2 after overfilling channel (capacity=2)", dropped)
	}

	// 丢弃数不超过实际提交的溢出量
	if dropped > 2 {
		t.Errorf("DroppedCount = %d, want exactly 2 (submitted 4, capacity 2)", dropped)
	}
}
