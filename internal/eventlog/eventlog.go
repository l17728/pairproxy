package eventlog

import (
	"sync"
	"time"
)

// Level represents the severity of a captured log event.
type Level string

const (
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Event is a single WARN or ERROR log entry captured from the zap logger.
type Event struct {
	ID      uint64         `json:"id"`
	Time    time.Time      `json:"time"`
	Level   Level          `json:"level"`
	Logger  string         `json:"logger"`  // zap logger name, e.g. "sproxy.proxy"
	Caller  string         `json:"caller"`  // "file.go:123"
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields"`  // structured key-value pairs
}

// Log is a thread-safe, fixed-capacity ring buffer for Event entries.
// When full, the oldest entry is overwritten.
type Log struct {
	mu     sync.RWMutex
	ring   []Event
	cap    int
	head   int    // next write position (0-indexed, wraps)
	size   int    // number of valid entries currently stored
	nextID uint64 // monotonically increasing event ID
}

// New creates a new Log with the given capacity.
func New(capacity int) *Log {
	if capacity <= 0 {
		capacity = 500
	}
	return &Log{
		ring: make([]Event, capacity),
		cap:  capacity,
	}
}

// Append adds an event to the ring buffer, assigning it a monotonic ID.
func (l *Log) Append(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.nextID++
	e.ID = l.nextID
	l.ring[l.head] = e
	l.head = (l.head + 1) % l.cap
	if l.size < l.cap {
		l.size++
	}
}

// Since returns all events whose Time is strictly after t, in ascending order.
func (l *Log) Since(t time.Time) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	all := l.orderedLocked()
	for i, e := range all {
		if e.Time.After(t) {
			return all[i:]
		}
	}
	return nil
}

// Recent returns the most recent n events in ascending time order.
// If n <= 0 or n >= l.size, all stored events are returned.
func (l *Log) Recent(n int) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	all := l.orderedLocked()
	if n <= 0 || n >= len(all) {
		return all
	}
	return all[len(all)-n:]
}

// orderedLocked returns events in ascending time order.
// Must be called with l.mu held (read or write).
func (l *Log) orderedLocked() []Event {
	if l.size == 0 {
		return nil
	}
	out := make([]Event, l.size)
	if l.size < l.cap {
		// Buffer not yet full: entries are at indices 0..size-1 in insertion order.
		copy(out, l.ring[:l.size])
	} else {
		// Buffer is full: oldest entry is at l.head (wrapped around).
		n := copy(out, l.ring[l.head:])
		copy(out[n:], l.ring[:l.head])
	}
	return out
}
