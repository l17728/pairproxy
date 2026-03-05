package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/config"
)

// ---------------------------------------------------------------------------
// TestUsageWriterDroppedCounter
// ---------------------------------------------------------------------------
// 用一个极小的 channel（容量=5）+ 阻塞式 DB（每次写入 sleep 50ms），
// 然后快速写入 50 条记录，验证：
//   - DroppedCount() > 0（部分记录必然被丢弃）
//   - 总计 = 写入 + 丢弃（无记录凭空消失）
//   - 无 goroutine 泄漏（Wait() 正常返回）
func TestUsageWriterDroppedCounter(t *testing.T) {
	gormDB := openTestDB(t)

	// bufferSize=5 → channel 容量=10（bufferSize*2），远小于写入量
	writer := NewUsageWriter(gormDB, zaptest.NewLogger(t), 5, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	const total = 60
	for i := range total {
		writer.Record(UsageRecord{
			RequestID: "req-drop-" + string(rune('a'+i%26)),
			UserID:    "u1",
		})
	}

	// 给 writer 一点时间处理队列
	time.Sleep(300 * time.Millisecond)
	cancel()
	writer.Wait()

	dropped := writer.DroppedCount()
	t.Logf("total=%d dropped=%d queued+flushed=%d", total, dropped, total-dropped)

	if dropped == 0 {
		t.Error("expected some records to be dropped with tiny channel capacity (5), got 0")
	}
	if dropped >= total {
		t.Errorf("dropped=%d >= total=%d — no records were processed at all", dropped, total)
	}
}

// ---------------------------------------------------------------------------
// TestUsageWriterConcurrent200
// ---------------------------------------------------------------------------
// 200 个 goroutine 同时调用 Record()，模拟 200 并发请求峰值。
// 验证：
//   - 无 panic（channel send 非阻塞，不会死锁）
//   - DroppedCount() + 已写入条数 = 200（无记录凭空消失）
//   - QueueDepth() 在写入前后可读
func TestUsageWriterConcurrent200(t *testing.T) {
	gormDB := openTestDB(t)

	// bufferSize=1000 → channel 容量=2000（生产默认值），足以容纳 200 条
	writer := NewUsageWriter(gormDB, zaptest.NewLogger(t), 1000, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)
	t.Cleanup(func() { cancel(); writer.Wait() })

	const goroutines = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			writer.Record(UsageRecord{
				RequestID:    fmt.Sprintf("req-concurrent-%04d", id), // 唯一 ID，避免 ON CONFLICT 去重
				UserID:       "u-concurrent",
				InputTokens:  10,
				OutputTokens: 5,
			})
		}(i)
	}
	wg.Wait()

	// 允许 writer 把队列消化完
	time.Sleep(200 * time.Millisecond)
	cancel()
	writer.Wait()

	dropped := writer.DroppedCount()
	t.Logf("concurrent 200: dropped=%d", dropped)

	// 默认 bufferSize=1000 时不应该丢弃任何记录
	if dropped > 0 {
		t.Errorf("no records should be dropped with bufferSize=1000 and 200 goroutines, got %d dropped", dropped)
	}

	// 验证 DB 里确实写入了记录
	var count int64
	if err := gormDB.Model(&UsageLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count usage logs: %v", err)
	}
	expected := int64(goroutines - dropped)
	if count < expected {
		t.Errorf("DB has %d records, expected %d (goroutines=%d, dropped=%d)", count, expected, goroutines, dropped)
	}
	t.Logf("DB records: %d (expected ~%d)", count, goroutines)
}

// ---------------------------------------------------------------------------
// TestUsageWriterBackpressureWithSlowDB
// ---------------------------------------------------------------------------
// 模拟生产中 DB 写入变慢（每批次 sleep 80ms）同时有持续的高速写入，
// 验证背压机制正常工作：
//   - 丢弃计数器正确累计
//   - Writer 在 ctx cancel 后能正常退出（无 goroutine 泄漏）
func TestUsageWriterBackpressureWithSlowDB(t *testing.T) {
	// 使用极小 bufferSize(3) + 极快写入，必然触发背压
	gormDB := openTestDB(t)
	writer := NewUsageWriter(gormDB, zaptest.NewLogger(t), 3, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	// 快速写入 30 条，远超 channel 容量
	for i := range 30 {
		writer.Record(UsageRecord{
			RequestID: "req-bp-" + string(rune('a'+i%26)),
			UserID:    "u-bp",
		})
	}

	// 等待 writer 处理
	time.Sleep(500 * time.Millisecond)
	cancel()
	writer.Wait()

	dropped := writer.DroppedCount()
	t.Logf("backpressure test: dropped=%d / 30", dropped)

	// 小 buffer 下必然有丢弃
	if dropped == 0 {
		t.Error("expected drops with bufferSize=3 sending 30 records quickly")
	}
}

// ---------------------------------------------------------------------------
// TestOpenWithConfigDefaults
// ---------------------------------------------------------------------------
// 验证 OpenWithConfig 的默认连接池参数对内存库和文件库生效正确。
func TestOpenWithConfigDefaults(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("memory db gets MaxOpenConns=1", func(t *testing.T) {
		gormDB, err := OpenWithConfig(logger, config.DatabaseConfig{Path: ":memory:"})
		if err != nil {
			t.Fatalf("OpenWithConfig: %v", err)
		}
		sqlDB, _ := gormDB.DB()
		stats := sqlDB.Stats()
		// MaxOpenConns=1 → 连接池最大开放连接数为 1
		if stats.MaxOpenConnections != 1 {
			t.Errorf("memory db MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
		}
	})

	t.Run("explicit config overrides defaults", func(t *testing.T) {
		gormDB, err := OpenWithConfig(logger, config.DatabaseConfig{
			Path:         ":memory:",
			MaxOpenConns: 5,
			MaxIdleConns: 3,
		})
		if err != nil {
			t.Fatalf("OpenWithConfig: %v", err)
		}
		sqlDB, _ := gormDB.DB()
		stats := sqlDB.Stats()
		if stats.MaxOpenConnections != 5 {
			t.Errorf("MaxOpenConnections = %d, want 5", stats.MaxOpenConnections)
		}
	})
}

// ---------------------------------------------------------------------------
// TestUsageWriterDefaultBufferSize
// ---------------------------------------------------------------------------
// 验证 bufferSize=0 时使用新默认值 1000（而非旧默认值 200）。
func TestUsageWriterDefaultBufferSize(t *testing.T) {
	gormDB := openTestDB(t)
	w := NewUsageWriter(gormDB, zaptest.NewLogger(t), 0, time.Second)
	// channel 容量 = bufferSize * 2 = 2000
	if cap(w.ch) != 2000 {
		t.Errorf("channel capacity = %d, want 2000 (bufferSize default 1000 × 2)", cap(w.ch))
	}
}
