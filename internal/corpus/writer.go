package corpus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/config"
)

// Writer 异步 JSONL 语料写入器。每个 sproxy 实例一个。
// 遵循 UsageWriter 模式：buffered channel + worker goroutine + ticker flush。
type Writer struct {
	logger     *zap.Logger
	ch         chan Record
	bufferSize int
	interval   time.Duration
	done       chan struct{}
	dropped    atomic.Int64

	// 文件管理
	basePath    string
	instanceID  string
	maxFileSize int64
	mu          sync.Mutex // 保护文件状态
	currentFile *os.File
	bufWriter   *bufio.Writer
	currentSize int64
	currentDate string // "2006-01-02"
	seqNum      int    // 同日轮转序号

	// 质量过滤配置（传递给 Collector 使用）
	MinOutputTokens int
	ExcludeGroups   []string
}

// New 创建 Writer，不启动后台 goroutine。
func New(logger *zap.Logger, cfg config.CorpusConfig, instanceID string) (*Writer, error) {
	maxBytes, err := parseMaxFileSize(cfg.MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("corpus: invalid max_file_size %q: %w", cfg.MaxFileSize, err)
	}

	// 确保基础目录存在
	if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
		return nil, fmt.Errorf("corpus: failed to create base dir %q: %w", cfg.Path, err)
	}

	w := &Writer{
		logger:          logger.Named("corpus"),
		ch:              make(chan Record, cfg.BufferSize*2),
		bufferSize:      cfg.BufferSize,
		interval:        cfg.FlushInterval,
		done:            make(chan struct{}),
		basePath:        cfg.Path,
		instanceID:      instanceID,
		maxFileSize:     maxBytes,
		MinOutputTokens: cfg.MinOutputTokens,
		ExcludeGroups:   cfg.ExcludeGroups,
	}
	return w, nil
}

// Start 启动后台写入 goroutine。ctx 取消时触发 drain + 关闭。
func (w *Writer) Start(ctx context.Context) {
	go w.runLoop(ctx)
}

// Wait 阻塞直到后台 goroutine 退出（用于优雅关闭）。
func (w *Writer) Wait() {
	<-w.done
}

// Submit 非阻塞发送记录到 channel。满则丢弃并记录 WARN。
func (w *Writer) Submit(r Record) {
	select {
	case w.ch <- r:
	default:
		n := w.dropped.Add(1)
		if n%100 == 1 {
			w.logger.Warn("corpus record dropped (channel full)",
				zap.Int64("total_dropped", n),
				zap.Int("queue_depth", len(w.ch)),
			)
		}
	}
}

// QueueDepth 返回当前 channel 深度。
func (w *Writer) QueueDepth() int {
	return len(w.ch)
}

// DroppedCount 返回累计丢弃记录数。
func (w *Writer) DroppedCount() int64 {
	return w.dropped.Load()
}

func (w *Writer) runLoop(ctx context.Context) {
	defer close(w.done)
	defer w.closeFile()

	w.logger.Info("corpus writer started",
		zap.String("path", w.basePath),
		zap.String("instance", w.instanceID),
	)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	batch := make([]Record, 0, w.bufferSize)

	for {
		select {
		case r := <-w.ch:
			batch = append(batch, r)
			if len(batch) >= w.bufferSize {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-ctx.Done():
			// drain 剩余
			for {
				select {
				case r := <-w.ch:
					batch = append(batch, r)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) > 0 {
				w.writeBatch(batch)
			}
			w.logger.Info("corpus writer stopped",
				zap.String("instance", w.instanceID),
				zap.Int64("total_dropped", w.dropped.Load()),
			)
			return
		}
	}
}

func (w *Writer) writeBatch(batch []Record) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for i := range batch {
		if err := w.ensureFile(); err != nil {
			w.logger.Error("corpus: failed to open file", zap.Error(err))
			return
		}
		data, err := json.Marshal(&batch[i])
		if err != nil {
			w.logger.Warn("corpus: failed to marshal record",
				zap.String("id", batch[i].ID),
				zap.Error(err),
			)
			continue
		}
		data = append(data, '\n')
		n, writeErr := w.bufWriter.Write(data)
		if writeErr != nil {
			w.logger.Error("corpus: write failed", zap.Error(writeErr))
			return
		}
		w.currentSize += int64(n)
	}
	// 每批次结束后 flush bufio
	if w.bufWriter != nil {
		if err := w.bufWriter.Flush(); err != nil {
			w.logger.Error("corpus: flush failed", zap.Error(err))
		}
	}
}

// ensureFile 确保当前文件可写。日期变更或超大小时轮转。
// 调用方必须持有 w.mu。
func (w *Writer) ensureFile() error {
	today := time.Now().UTC().Format("2006-01-02")

	// 日期变更：关闭旧文件，重置序号
	if w.currentDate != today {
		w.doCloseFile()
		w.currentDate = today
		w.seqNum = 0
		w.currentSize = 0
	}

	// 大小轮转
	if w.currentFile != nil && w.maxFileSize > 0 && w.currentSize >= w.maxFileSize {
		w.doCloseFile()
		w.seqNum++
		w.currentSize = 0
	}

	if w.currentFile != nil {
		return nil
	}

	// 构建路径：corpus/<date>/sproxy_<instance>[_NNN].jsonl
	dir := filepath.Join(w.basePath, today)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	var filename string
	if w.seqNum == 0 {
		filename = fmt.Sprintf("sproxy_%s.jsonl", w.instanceID)
	} else {
		filename = fmt.Sprintf("sproxy_%s_%03d.jsonl", w.instanceID, w.seqNum)
	}
	path := filepath.Join(dir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	// 获取已有文件大小（append 模式）
	info, _ := f.Stat()
	if info != nil {
		w.currentSize = info.Size()
	}

	w.currentFile = f
	w.bufWriter = bufio.NewWriterSize(f, 64*1024) // 64KB buffer
	w.logger.Info("corpus file opened",
		zap.String("path", path),
		zap.Int64("existing_size", w.currentSize),
	)
	return nil
}

func (w *Writer) closeFile() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.doCloseFile()
}

// doCloseFile 关闭当前文件（调用方必须持有 w.mu）。
func (w *Writer) doCloseFile() {
	if w.bufWriter != nil {
		_ = w.bufWriter.Flush()
		w.bufWriter = nil
	}
	if w.currentFile != nil {
		w.logger.Debug("corpus file closed",
			zap.String("path", w.currentFile.Name()),
			zap.Int64("size", w.currentSize),
		)
		_ = w.currentFile.Sync()
		_ = w.currentFile.Close()
		w.currentFile = nil
	}
}

// parseMaxFileSize 解析 "200MB"、"1GB" 等大小字符串为字节数。
func parseMaxFileSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil // 0 = 不限制
	}
	s = strings.ToUpper(s)

	var multiplier int64 = 1
	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	default:
		// 纯数字，视为字节
	}

	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative size: %d", n)
	}
	return n * multiplier, nil
}
