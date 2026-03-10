package eventlog

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ---- eventlog ring buffer tests ----

func TestLog_AppendAndRecent(t *testing.T) {
	l := New(5)
	for i := range 3 {
		l.Append(Event{Time: time.Now(), Message: string(rune('a' + i))})
	}
	got := l.Recent(10)
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
}

func TestLog_RingOverflow(t *testing.T) {
	l := New(3)
	for i := range 5 {
		l.Append(Event{Time: time.Now(), Message: string(rune('a' + i))})
	}
	got := l.Recent(10)
	if len(got) != 3 {
		t.Fatalf("after overflow want 3 events, got %d", len(got))
	}
	// Oldest entry should be 'c' (index 2), newest 'e' (index 4).
	if got[0].Message != "c" {
		t.Errorf("oldest = %q, want %q", got[0].Message, "c")
	}
	if got[2].Message != "e" {
		t.Errorf("newest = %q, want %q", got[2].Message, "e")
	}
}

func TestLog_Since(t *testing.T) {
	l := New(10)
	base := time.Now()
	l.Append(Event{Time: base.Add(-2 * time.Second), Message: "old"})
	l.Append(Event{Time: base.Add(-1 * time.Second), Message: "mid"})
	l.Append(Event{Time: base, Message: "now"})

	got := l.Since(base.Add(-1*time.Second - time.Millisecond))
	if len(got) != 2 {
		t.Fatalf("want 2 events since (base-1s-1ms), got %d", len(got))
	}
	if got[0].Message != "mid" {
		t.Errorf("first = %q, want mid", got[0].Message)
	}
}

func TestLog_IDs_Monotonic(t *testing.T) {
	l := New(10)
	l.Append(Event{})
	l.Append(Event{})
	l.Append(Event{})
	got := l.Recent(3)
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("IDs not monotonic: %d <= %d", got[i].ID, got[i-1].ID)
		}
	}
}

func TestLog_ConcurrentAppend_NoRace(t *testing.T) {
	l := New(50)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Append(Event{Time: time.Now(), Message: "x"})
		}()
	}
	wg.Wait()
	got := l.Recent(100)
	if len(got) == 0 {
		t.Error("expected at least one event")
	}
}

// ---- zap Core tests ----

func newTestLogger(l *Log) *zap.Logger {
	// Combine a no-op base core with our eventlog core.
	noop := zapcore.NewNopCore()
	evtCore := NewCore(l)
	combined := zapcore.NewTee(noop, evtCore)
	return zap.New(combined)
}

func TestCore_CapturesWarnAndError(t *testing.T) {
	l := New(10)
	logger := newTestLogger(l)

	logger.Info("this should not appear")
	logger.Debug("also not")
	logger.Warn("watch out", zap.String("key", "val"))
	logger.Error("something broke", zap.Int("code", 42))

	got := l.Recent(10)
	if len(got) != 2 {
		t.Fatalf("want 2 events (warn+error), got %d", len(got))
	}
	if got[0].Level != LevelWarn {
		t.Errorf("first level = %q, want warn", got[0].Level)
	}
	if got[1].Level != LevelError {
		t.Errorf("second level = %q, want error", got[1].Level)
	}
}

func TestCore_Fields(t *testing.T) {
	l := New(10)
	logger := newTestLogger(l)
	logger.Warn("msg", zap.String("user", "alice"), zap.Int("status", 403))

	got := l.Recent(1)
	if len(got) != 1 {
		t.Fatal("expected 1 event")
	}
	e := got[0]
	if e.Fields["user"] != "alice" {
		t.Errorf("Fields[user] = %v, want alice", e.Fields["user"])
	}
	// zap encodes Int as int64
	if e.Fields["status"] == nil {
		t.Error("Fields[status] is nil")
	}
}

func TestCore_WithFields(t *testing.T) {
	l := New(10)
	base := newTestLogger(l)
	named := base.With(zap.String("component", "proxy"))
	named.Warn("hello")

	got := l.Recent(1)
	if len(got) != 1 {
		t.Fatal("expected 1 event")
	}
	if got[0].Fields["component"] != "proxy" {
		t.Errorf("Fields[component] = %v, want proxy", got[0].Fields["component"])
	}
}

func TestCore_InfoNotCaptured(t *testing.T) {
	l := New(10)
	logger := newTestLogger(l)
	for range 10 {
		logger.Info("ignored")
	}
	if len(l.Recent(10)) != 0 {
		t.Error("INFO logs should not be captured")
	}
}
