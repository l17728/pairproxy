package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestEvent_Constants tests event type constants
func TestEvent_Constants_by_kimi2_5(t *testing.T) {
	constants := map[string]string{
		EventQuotaExceeded: "quota_exceeded",
		EventRateLimited:   "rate_limited",
		EventNodeDown:      "node_down",
		EventNodeRecovered: "node_recovered",
		EventHighLoad:      "high_load",
		EventLoadRecovered: "load_recovered",
	}

	for constVal, expected := range constants {
		if constVal != expected {
			t.Errorf("expected %s, got %s", expected, constVal)
		}
	}
}

// TestWebhookTarget_Matches tests the matches method
func TestWebhookTarget_Matches_by_kimi2_5(t *testing.T) {
	tests := []struct {
		name     string
		events   map[string]bool
		kind     string
		expected bool
	}{
		{
			name:     "nil events matches all",
			events:   nil,
			kind:     "any_event",
			expected: true,
		},
		{
			name:     "empty events matches all",
			events:   map[string]bool{},
			kind:     "any_event",
			expected: true,
		},
		{
			name:     "matching event",
			events:   map[string]bool{"test_event": true},
			kind:     "test_event",
			expected: true,
		},
		{
			name:     "non-matching event",
			events:   map[string]bool{"other_event": true},
			kind:     "test_event",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wt := webhookTarget{events: tc.events}
			result := wt.matches(tc.kind)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

// TestNewNotifier_EmptyURL tests creating notifier with empty URL
func TestNewNotifier_EmptyURL_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "")

	if n == nil {
		t.Fatal("expected notifier, got nil")
	}
	if len(n.targets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(n.targets))
	}
}

// TestNewNotifier_WithURL tests creating notifier with URL
func TestNewNotifier_WithURL_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "http://example.com/webhook")

	if n == nil {
		t.Fatal("expected notifier, got nil")
	}
	if len(n.targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(n.targets))
	}
}

// TestNewNotifierMulti_Empty tests creating multi-target notifier with empty targets
func TestNewNotifierMulti_Empty_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{}, "")

	if n == nil {
		t.Fatal("expected notifier, got nil")
	}
	if len(n.targets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(n.targets))
	}
}

// TestNewNotifierMulti_WithTargets tests creating multi-target notifier
func TestNewNotifierMulti_WithTargets_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{URL: "http://example.com/webhook1"},
		{URL: "http://example.com/webhook2"},
	}
	n := NewNotifierMulti(logger, targets, "")

	if len(n.targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(n.targets))
	}
}

// TestNewNotifierMulti_WithLegacyURL tests creating multi-target notifier with legacy URL
func TestNewNotifierMulti_WithLegacyURL_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{URL: "http://example.com/webhook1"},
	}
	n := NewNotifierMulti(logger, targets, "http://example.com/legacy")

	if len(n.targets) != 2 {
		t.Errorf("expected 2 targets (1 config + 1 legacy), got %d", len(n.targets))
	}
}

// TestNewNotifierMulti_DuplicateLegacyURL tests that duplicate legacy URL is not added
func TestNewNotifierMulti_DuplicateLegacyURL_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	url := "http://example.com/webhook"
	targets := []WebhookTargetConfig{
		{URL: url},
	}
	n := NewNotifierMulti(logger, targets, url)

	if len(n.targets) != 1 {
		t.Errorf("expected 1 target (duplicate legacy should be skipped), got %d", len(n.targets))
	}
}

// TestNewNotifierMulti_WithEvents tests event filtering
func TestNewNotifierMulti_WithEvents_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{
			URL:    "http://example.com/webhook",
			Events: []string{"event1", "event2"},
		},
	}
	n := NewNotifierMulti(logger, targets, "")

	if len(n.targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(n.targets))
	}

	target := n.targets[0]
	if len(target.events) != 2 {
		t.Errorf("expected 2 events, got %d", len(target.events))
	}
	if !target.matches("event1") {
		t.Error("expected to match event1")
	}
	if !target.matches("event2") {
		t.Error("expected to match event2")
	}
	if target.matches("event3") {
		t.Error("expected not to match event3")
	}
}

// TestNewNotifierMulti_WithInvalidTemplate tests handling of invalid template
func TestNewNotifierMulti_WithInvalidTemplate_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{
			URL:      "http://example.com/webhook",
			Template: "{{invalid", // Invalid template
		},
	}
	// Should not panic and should create notifier with nil template
	n := NewNotifierMulti(logger, targets, "")

	if len(n.targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(n.targets))
	}

	if n.targets[0].tmpl != nil {
		t.Error("expected template to be nil for invalid template")
	}
}

// TestNewNotifierMulti_WithValidTemplate tests handling of valid template
func TestNewNotifierMulti_WithValidTemplate_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{
			URL:      "http://example.com/webhook",
			Template: `{"kind":"{{.Kind}}","message":"{{.Message}}"}`,
		},
	}
	n := NewNotifierMulti(logger, targets, "")

	if len(n.targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(n.targets))
	}

	if n.targets[0].tmpl == nil {
		t.Error("expected template to be set")
	}
}

// TestNotifier_Notify_NoTargets tests notification with no targets
func TestNotifier_Notify_NoTargets_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "")

	// Should not panic
	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test message",
	})
}

// TestNotifier_Notify_SuccessfulDelivery tests successful webhook delivery
func TestNotifier_Notify_SuccessfulDelivery_by_kimi2_5(t *testing.T) {
	var received bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, server.URL)

	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test message",
	})

	// Give some time for the goroutine to execute
	time.Sleep(100 * time.Millisecond)

	if !received {
		t.Error("expected webhook to be received")
	}
}

// TestNotifier_Notify_EventFiltering tests event filtering in notifications
func TestNotifier_Notify_EventFiltering_by_kimi2_5(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{
			URL:    server.URL,
			Events: []string{EventQuotaExceeded},
		},
	}
	n := NewNotifierMulti(logger, targets, "")

	// Send matching event
	n.Notify(Event{Kind: EventQuotaExceeded, Message: "quota exceeded"})

	// Send non-matching event
	n.Notify(Event{Kind: EventRateLimited, Message: "rate limited"})

	// Give some time for the goroutines to execute
	time.Sleep(100 * time.Millisecond)

	if callCount.Load() != 1 {
		t.Errorf("expected 1 webhook call, got %d", callCount.Load())
	}
}

// TestNotifier_Notify_DefaultTimestamp tests default timestamp setting
func TestNotifier_Notify_DefaultTimestamp_by_kimi2_5(t *testing.T) {
	var mu sync.Mutex
	var receivedEvent Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		receivedEvent = ev
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, server.URL)

	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
		At:      time.Time{}, // Zero time
	})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	at := receivedEvent.At
	mu.Unlock()
	if at.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

// TestNotifier_Notify_WithLabels tests notification with labels
func TestNotifier_Notify_WithLabels_by_kimi2_5(t *testing.T) {
	var mu sync.Mutex
	var receivedEvent Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		receivedEvent = ev
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, server.URL)

	labels := map[string]string{
		"user_id": "user-123",
		"group":   "engineering",
	}

	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
		Labels:  labels,
	})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	labelsLen := len(receivedEvent.Labels)
	mu.Unlock()
	if labelsLen != 2 {
		t.Errorf("expected 2 labels, got %d", labelsLen)
	}
}

// TestNotifier_Notify_ErrorStatus tests handling of error status codes
func TestNotifier_Notify_ErrorStatus_by_kimi2_5(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, server.URL)

	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
	})

	time.Sleep(100 * time.Millisecond)

	// Should have called the webhook even though it returned error
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call, got %d", callCount.Load())
	}
}

// TestNotifier_Notify_UnreachableServer tests handling of unreachable server
func TestNotifier_Notify_UnreachableServer_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	// Use a URL that doesn't exist
	n := NewNotifier(logger, "http://localhost:59999/webhook")

	// Should not panic
	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
	})

	time.Sleep(100 * time.Millisecond)
}

// TestNotifier_Notify_CustomTemplate tests notification with custom template
func TestNotifier_Notify_CustomTemplate_by_kimi2_5(t *testing.T) {
	var body atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		body.Store(string(buf))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zaptest.NewLogger(t)
	targets := []WebhookTargetConfig{
		{
			URL:      server.URL,
			Template: `{"custom_kind":"{{.Kind}}"}`,
		},
	}
	n := NewNotifierMulti(logger, targets, "")

	n.Notify(Event{
		Kind:    EventQuotaExceeded,
		Message: "test",
	})

	time.Sleep(100 * time.Millisecond)

	got, _ := body.Load().(string)
	if got == "" {
		t.Error("expected body to be set")
	}
	if got != `{"custom_kind":"quota_exceeded"}` {
		t.Errorf("unexpected body: %s", got)
	}
}

// TestEvent_JSONMarshaling tests JSON marshaling of Event
func TestEvent_JSONMarshaling_by_kimi2_5(t *testing.T) {
	event := Event{
		Kind:    EventQuotaExceeded,
		At:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Message: "test message",
		Labels:  map[string]string{"key": "value"},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var unmarshaled Event
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if unmarshaled.Kind != event.Kind {
		t.Errorf("expected kind %s, got %s", event.Kind, unmarshaled.Kind)
	}
	if unmarshaled.Message != event.Message {
		t.Errorf("expected message %s, got %s", event.Message, unmarshaled.Message)
	}
	if len(unmarshaled.Labels) != len(event.Labels) {
		t.Errorf("expected %d labels, got %d", len(event.Labels), len(unmarshaled.Labels))
	}
}
