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

// ---------------------------------------------------------------------------
// F-6 多 webhook 测试
// ---------------------------------------------------------------------------

// TestNotifier_MultiTarget 验证事件广播到所有目标。
func TestNotifier_MultiTarget(t *testing.T) {
	var count1, count2 atomic.Int32

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv2.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: srv1.URL},
		{URL: srv2.URL},
	}, "")
	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "test"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if count1.Load() > 0 && count2.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count1.Load() == 0 {
		t.Error("target 1 was never called")
	}
	if count2.Load() == 0 {
		t.Error("target 2 was never called")
	}
}

// TestNotifier_EventFilter 验证事件过滤：只有匹配 events 的目标收到推送。
func TestNotifier_EventFilter(t *testing.T) {
	var quotaCount, nodeCount atomic.Int32

	quotaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer quotaSrv.Close()

	nodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer nodeSrv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: quotaSrv.URL, Events: []string{EventQuotaExceeded}},
		{URL: nodeSrv.URL, Events: []string{EventNodeDown, EventNodeRecovered}},
	}, "")

	// 发送 quota_exceeded 事件
	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "quota"})
	time.Sleep(200 * time.Millisecond)

	if quotaCount.Load() == 0 {
		t.Error("quota target should have received quota_exceeded")
	}
	if nodeCount.Load() > 0 {
		t.Error("node target should NOT have received quota_exceeded")
	}

	// 发送 node_down 事件
	n.Notify(Event{Kind: EventNodeDown, At: time.Now(), Message: "node"})
	time.Sleep(200 * time.Millisecond)

	if nodeCount.Load() == 0 {
		t.Error("node target should have received node_down")
	}
}

// TestNotifier_CustomTemplate 验证自定义 Go text/template 渲染。
func TestNotifier_CustomTemplate(t *testing.T) {
	var received atomic.Int32
	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		_ = err
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tmplStr := `{"alert":"{{.Kind}}","msg":"{{.Message}}"}`
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: srv.URL, Template: tmplStr},
	}, "")
	n.Notify(Event{Kind: EventRateLimited, At: time.Now(), Message: "too fast"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Fatal("webhook was never called")
	}
	got := string(body)
	if got != `{"alert":"rate_limited","msg":"too fast"}` {
		t.Errorf("body = %q, want template-rendered body", got)
	}
}

// TestNotifier_BackwardCompat 验证旧式 AlertWebhook URL 向后兼容。
func TestNotifier_BackwardCompat(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	// 仅使用 legacyURL（无新式 targets）
	n := NewNotifierMulti(logger, nil, srv.URL)
	n.Notify(Event{Kind: EventNodeRecovered, At: time.Now(), Message: "back online"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Error("legacy webhook was never called")
	}
}

// TestNotifier_BackwardCompat_NoDup 验证旧式 URL 与新式目标重复时不重复推送。
func TestNotifier_BackwardCompat_NoDup(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	// 新式 targets 和 legacyURL 指向同一 URL，应只推送一次
	n := NewNotifierMulti(logger, []WebhookTargetConfig{{URL: srv.URL}}, srv.URL)
	n.Notify(Event{Kind: EventNodeDown, At: time.Now(), Message: "dup test"})

	time.Sleep(300 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected exactly 1 delivery, got %d", received.Load())
	}
}
