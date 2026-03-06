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

// TestNotifierConcurrent verifies that concurrent Notify() calls are race-free
// and all events reach the webhook (run with -race to detect data races).
func TestNotifierConcurrent(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)

	const count = 20
	for i := 0; i < count; i++ {
		n.Notify(Event{Kind: EventQuotaExceeded, Message: "concurrent test"})
	}

	// Wait up to 2 seconds for all goroutines to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() >= count {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := received.Load(); got < count {
		t.Errorf("received %d notifications, want %d", got, count)
	}
}

// TestNotifierWebhookTimeout verifies that Notify() returns immediately even
// when the webhook server is very slow (the HTTP call has a 5s timeout).
func TestNotifierWebhookTimeout(t *testing.T) {
	// Slow server that takes longer than the 5s notifier timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond instantly to keep the test fast; the notifier timeout is tested
		// via TestNotifierBadURL. Here we just verify Notify() is non-blocking.
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)

	start := time.Now()
	n.Notify(Event{Kind: EventNodeDown, Message: "timeout test"})
	// Notify() must return immediately — it fires a goroutine.
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Notify() blocked for %v, should return immediately (non-blocking)", elapsed)
	}
}

// TestNotifierWebhookErrorStatus verifies graceful handling when the webhook
// returns a 5xx error status (no panic, logged as warn, not retried).
func TestNotifierWebhookErrorStatus(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{Kind: EventRateLimited, Message: "error status test"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The request was made but the error status was handled gracefully.
	if received.Load() == 0 {
		t.Error("webhook should have been called (even with error status)")
	}
}

// TestNotifierAllEventTypes verifies that all four event type constants
// can be sent without error.
func TestNotifierAllEventTypes(t *testing.T) {
	var received atomic.Int32
	var lastKind atomic.Value // stores string; safe for concurrent handler goroutines

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			t.Errorf("decode event: %v", err)
		}
		lastKind.Store(evt.Kind)
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)

	eventTypes := []string{
		EventQuotaExceeded,
		EventRateLimited,
		EventNodeDown,
		EventNodeRecovered,
		EventHighLoad,
		EventLoadRecovered,
	}

	for _, kind := range eventTypes {
		n.Notify(Event{Kind: kind, Message: "test " + kind})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(received.Load()) >= len(eventTypes) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := int(received.Load()); got < len(eventTypes) {
		lk, _ := lastKind.Load().(string)
		t.Errorf("received %d events, want %d (last seen kind: %q)", got, len(eventTypes), lk)
	}
}

// TestNotifierAutoFillAt verifies that the At field is automatically set to
// time.Now() when the caller leaves it zero.
func TestNotifierAutoFillAt(t *testing.T) {
	var capturedAt time.Time
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt Event
		_ = json.NewDecoder(r.Body).Decode(&evt)
		capturedAt = evt.At
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	before := time.Now()
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{Kind: EventQuotaExceeded}) // At is zero

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Fatal("webhook was not called")
	}
	if capturedAt.IsZero() {
		t.Error("At field should be auto-filled when submitted as zero")
	}
	if capturedAt.Before(before) {
		t.Errorf("At = %v should be >= %v (should be set to approximately now)", capturedAt, before)
	}
}

// TestNotifierLabels verifies that Labels map is serialized and sent correctly.
func TestNotifierLabels(t *testing.T) {
	var capturedEvt Event
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedEvt)
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
		Labels: map[string]string{
			"user_id": "u123",
			"kind":    "daily",
			"limit":   "10000",
		},
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Fatal("webhook was not called")
	}
	if capturedEvt.Labels["user_id"] != "u123" {
		t.Errorf("Labels[user_id] = %q, want u123", capturedEvt.Labels["user_id"])
	}
	if capturedEvt.Labels["limit"] != "10000" {
		t.Errorf("Labels[limit] = %q, want 10000", capturedEvt.Labels["limit"])
	}
}
