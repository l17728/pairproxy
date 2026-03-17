package eventlog

import (
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

// ---------------------------------------------------------------------------
// Core.Sync (0% → covered)
// ---------------------------------------------------------------------------

func TestCore_Sync_IsNoOp(t *testing.T) {
	l := New(10)
	c := NewCore(l)
	// Sync should be a no-op and return nil
	if err := c.Sync(); err != nil {
		t.Errorf("Sync() should return nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Core.Check — disabled level (< WarnLevel) branch
// ---------------------------------------------------------------------------

func TestCore_Check_DisabledLevel_ReturnsNilCE(t *testing.T) {
	l := New(10)
	c := NewCore(l)

	// Debug is below WarnLevel → Check should not add this core
	entry := zapcore.Entry{
		Level:   zapcore.DebugLevel,
		Time:    time.Now(),
		Message: "debug msg",
	}
	ce := c.Check(entry, nil)
	// When the level is disabled, ce should be returned unchanged (nil in this case)
	if ce != nil {
		t.Error("Check should return nil CheckedEntry for disabled levels")
	}
}

func TestCore_Check_EnabledLevel_WarnCaptures(t *testing.T) {
	l := New(10)
	c := NewCore(l)

	// Write directly through Enabled + Write to cover the Check-enabled branch indirectly
	if !c.Enabled(zapcore.WarnLevel) {
		t.Error("Enabled(WarnLevel) should return true")
	}

	entry := zapcore.Entry{
		Level:   zapcore.WarnLevel,
		Time:    time.Now(),
		Message: "warn-check-msg",
	}
	if err := c.Write(entry, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := l.Recent(1)
	if len(got) != 1 || got[0].Message != "warn-check-msg" {
		t.Errorf("expected 1 warn event, got %v", got)
	}
}

func TestCore_Check_EnabledLevel_ErrorCaptures(t *testing.T) {
	l := New(10)
	c := NewCore(l)

	if !c.Enabled(zapcore.ErrorLevel) {
		t.Error("Enabled(ErrorLevel) should return true")
	}

	entry := zapcore.Entry{
		Level:   zapcore.ErrorLevel,
		Time:    time.Now(),
		Message: "error-check-msg",
	}
	if err := c.Write(entry, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := l.Recent(1)
	if len(got) != 1 || got[0].Level != LevelError {
		t.Errorf("expected 1 error event, got %v", got)
	}
}

func TestCore_Check_InfoLevel_DoesNotCapture(t *testing.T) {
	l := New(10)
	c := NewCore(l)

	if c.Enabled(zapcore.InfoLevel) {
		t.Error("Enabled(InfoLevel) should return false")
	}

	// Check with nil ce and Info level → ce stays nil
	entry := zapcore.Entry{
		Level:   zapcore.InfoLevel,
		Time:    time.Now(),
		Message: "info msg",
	}
	result := c.Check(entry, nil)
	if result != nil {
		t.Error("Check(InfoLevel) should return nil (level disabled)")
	}

	// Nothing should be in the log
	got := l.Recent(10)
	if len(got) != 0 {
		t.Errorf("Check with InfoLevel should not append to log, got %d entries", len(got))
	}
}

// ---------------------------------------------------------------------------
// Log.Recent — exact-count boundary (n == len(all))
// ---------------------------------------------------------------------------

func TestLog_Recent_ExactCount(t *testing.T) {
	l := New(10)
	for i := range 5 {
		l.Append(Event{Time: time.Now(), Message: string(rune('a' + i))})
	}
	// n == number stored → should return all
	got := l.Recent(5)
	if len(got) != 5 {
		t.Errorf("Recent(5) with 5 stored: want 5, got %d", len(got))
	}
}

func TestLog_Recent_PartialCount(t *testing.T) {
	l := New(10)
	for i := range 5 {
		l.Append(Event{Time: time.Now(), Message: string(rune('a' + i))})
	}
	// n < stored → should return the most recent n
	got := l.Recent(2)
	if len(got) != 2 {
		t.Errorf("Recent(2) with 5 stored: want 2, got %d", len(got))
	}
	// Most recent should be the last two appended
	if got[1].Message != "e" {
		t.Errorf("newest in Recent(2) = %q, want 'e'", got[1].Message)
	}
	if got[0].Message != "d" {
		t.Errorf("second newest in Recent(2) = %q, want 'd'", got[0].Message)
	}
}

func TestLog_Recent_NegativeCount_ReturnsAll(t *testing.T) {
	l := New(10)
	for i := range 3 {
		l.Append(Event{Time: time.Now(), Message: string(rune('a' + i))})
	}
	got := l.Recent(-1)
	if len(got) != 3 {
		t.Errorf("Recent(-1): want all 3, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// levelFromZap — boundary verification
// ---------------------------------------------------------------------------

func TestLevelFromZap_Warn(t *testing.T) {
	if levelFromZap(zapcore.WarnLevel) != LevelWarn {
		t.Error("WarnLevel should map to LevelWarn")
	}
}

func TestLevelFromZap_Error(t *testing.T) {
	if levelFromZap(zapcore.ErrorLevel) != LevelError {
		t.Error("ErrorLevel should map to LevelError")
	}
}

func TestLevelFromZap_DPanic(t *testing.T) {
	if levelFromZap(zapcore.DPanicLevel) != LevelError {
		t.Error("DPanicLevel should map to LevelError")
	}
}

func TestLevelFromZap_Panic(t *testing.T) {
	if levelFromZap(zapcore.PanicLevel) != LevelError {
		t.Error("PanicLevel should map to LevelError")
	}
}

// ---------------------------------------------------------------------------
// Core.With — accumulated fields propagate through chain
// ---------------------------------------------------------------------------

func TestCore_With_AccumulatesFields(t *testing.T) {
	l := New(10)
	c := NewCore(l)

	// Add first layer of fields
	c2 := c.With([]zapcore.Field{zapcore.Field{
		Key:    "layer1",
		Type:   zapcore.StringType,
		String: "value1",
	}})

	// Add second layer of fields
	c3 := c2.With([]zapcore.Field{zapcore.Field{
		Key:    "layer2",
		Type:   zapcore.StringType,
		String: "value2",
	}})

	// Write through the layered core
	entry := zapcore.Entry{
		Level:   zapcore.WarnLevel,
		Time:    time.Now(),
		Message: "layered msg",
	}
	if err := c3.Write(entry, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := l.Recent(1)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Fields["layer1"] == nil {
		t.Error("accumulated field 'layer1' should be in event fields")
	}
	if got[0].Fields["layer2"] == nil {
		t.Error("accumulated field 'layer2' should be in event fields")
	}
}

// ---------------------------------------------------------------------------
// Log.orderedLocked — full-buffer wrap-around consistency
// ---------------------------------------------------------------------------

func TestLog_OrderedLocked_FullBufferOrdering(t *testing.T) {
	cap := 3
	l := New(cap)

	msgs := []string{"a", "b", "c", "d", "e"}
	for _, m := range msgs {
		l.Append(Event{Time: time.Now(), Message: m})
	}

	// After 5 appends to cap-3 buffer, oldest should be "c", newest "e"
	got := l.Recent(100)
	if len(got) != cap {
		t.Fatalf("want %d events, got %d", cap, len(got))
	}
	if got[0].Message != "c" {
		t.Errorf("oldest = %q, want 'c'", got[0].Message)
	}
	if got[2].Message != "e" {
		t.Errorf("newest = %q, want 'e'", got[2].Message)
	}
}
