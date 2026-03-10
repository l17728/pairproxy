package eventlog

import (
	"go.uber.org/zap/zapcore"
)

// Core is a zapcore.Core that captures WARN and above log entries into a Log.
// It is designed to be used with zapcore.NewTee alongside the normal output core.
type Core struct {
	log    *Log
	fields []zapcore.Field
}

// NewCore returns a zapcore.Core that writes WARN+ entries to log.
func NewCore(log *Log) zapcore.Core {
	return &Core{log: log}
}

// Enabled reports whether the given level is at least WarnLevel.
func (c *Core) Enabled(lvl zapcore.Level) bool {
	return lvl >= zapcore.WarnLevel
}

// With returns a copy of this Core with additional fields accumulated.
func (c *Core) With(fields []zapcore.Field) zapcore.Core {
	combined := make([]zapcore.Field, len(c.fields)+len(fields))
	copy(combined, c.fields)
	copy(combined[len(c.fields):], fields)
	return &Core{log: c.log, fields: combined}
}

// Check adds this Core to the CheckedEntry if the level is enabled.
func (c *Core) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

// Write encodes the log entry and its fields into an Event and appends it to the Log.
func (c *Core) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Merge pre-accumulated fields with per-call fields.
	all := make([]zapcore.Field, 0, len(c.fields)+len(fields))
	all = append(all, c.fields...)
	all = append(all, fields...)

	// Encode fields to map[string]any using zap's built-in map encoder.
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range all {
		f.AddTo(enc)
	}

	caller := ""
	if entry.Caller.Defined {
		caller = entry.Caller.TrimmedPath()
	}

	e := Event{
		Time:    entry.Time,
		Level:   levelFromZap(entry.Level),
		Logger:  entry.LoggerName,
		Caller:  caller,
		Message: entry.Message,
		Fields:  enc.Fields,
	}
	c.log.Append(e)
	return nil
}

// Sync is a no-op for an in-memory store.
func (c *Core) Sync() error { return nil }

func levelFromZap(lvl zapcore.Level) Level {
	if lvl >= zapcore.ErrorLevel {
		return LevelError
	}
	return LevelWarn
}
