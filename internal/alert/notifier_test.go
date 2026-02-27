package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestNotifierSend verifies that Notify POSTs the event to the webhook URL.
func TestNotifierSend(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var evt Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			t.Errorf("decode event: %v", err)
		}
		if evt.Kind != EventQuotaExceeded {
			t.Errorf("kind = %q, want %q", evt.Kind, EventQuotaExceeded)
		}
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		At:      time.Now(),
		Message: "daily quota exceeded",
		Labels:  map[string]string{"user_id": "u1"},
	})

	// 等待 goroutine 完成（最多 500ms）
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Error("webhook was never called")
	}
}

// TestNotifierNoURL verifies that empty URL is a no-op.
func TestNotifierNoURL(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "")
	// Should not panic or block
	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "test"})
	// Give goroutine time to potentially run (should not start)
	time.Sleep(50 * time.Millisecond)
}

// TestNotifierBadURL verifies graceful handling of unreachable URL.
func TestNotifierBadURL(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "http://127.0.0.1:1") // port 1 should be unreachable
	n.Notify(Event{Kind: EventNodeDown, At: time.Now(), Message: "test"})
	// Give it time to fail gracefully
	time.Sleep(200 * time.Millisecond)
	// If we get here without panic, the test passes
}
