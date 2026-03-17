package alert

// alert_coverage_test.go — 补充覆盖 send 函数的缺失分支：
//   - send: HTTP 请求失败（无法连接）
//   - send: webhook 返回 4xx/5xx
//   - send: 模板渲染失败
//   - NewNotifierMulti: 含空 URL target（跳过）
//   - NewNotifierMulti: 含无效模板（fallback 到默认 JSON）
//   - Notify: evt.At 为零值时自动设置为 time.Now()

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// send — webhook 返回 4xx 状态（WARN 路径）
// ---------------------------------------------------------------------------

func TestCoverage_Send_Webhook4xx(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusBadRequest) // 400
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "test"})

	// 等待 goroutine 完成
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if called.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if called.Load() == 0 {
		t.Error("webhook should be called even if it returns 4xx")
	}
}

// ---------------------------------------------------------------------------
// send — webhook 返回 5xx 状态
// ---------------------------------------------------------------------------

func TestCoverage_Send_Webhook5xx(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // 500
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)
	n.Notify(Event{Kind: EventNodeDown, At: time.Now(), Message: "node down"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if called.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if called.Load() == 0 {
		t.Error("webhook should be called even if it returns 5xx")
	}
}

// ---------------------------------------------------------------------------
// send — webhook URL 无法连接（WARN 路径，不 panic）
// ---------------------------------------------------------------------------

func TestCoverage_Send_Unreachable(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, "http://127.0.0.1:1") // 端口 1 不可达

	// 异步发送，不应 panic
	n.Notify(Event{Kind: EventRateLimited, At: time.Now(), Message: "rate"})

	// 给 goroutine 时间失败
	time.Sleep(200 * time.Millisecond)
	// 到达这里说明没有 panic
}

// ---------------------------------------------------------------------------
// send — 模板渲染失败（模板引用了不存在的字段）
// ---------------------------------------------------------------------------

func TestCoverage_Send_TemplateRenderFail(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 模板引用 .NonExistentField，但对于 Event 结构体来说这会出错
	// text/template 对 map 类型字段不报错，但对结构体的不存在字段会 error
	badTemplate := `{{.NonExistentField.SubField}}`
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: srv.URL, Template: badTemplate},
	}, "")

	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "test"})

	// goroutine 应该在模板渲染失败后提前 return（不向 webhook 发送请求）
	time.Sleep(200 * time.Millisecond)

	// 渲染失败 → send 提前 return → webhook 不被调用
	if called.Load() > 0 {
		t.Error("webhook should NOT be called when template render fails")
	}
}

// ---------------------------------------------------------------------------
// NewNotifierMulti — 含空 URL target → 跳过
// ---------------------------------------------------------------------------

func TestCoverage_NewNotifierMulti_EmptyURL(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: ""},           // 空 URL，应跳过
		{URL: ""},           // 再一个空 URL
	}, "")

	// 没有有效目标，Notify 是 no-op
	if len(n.targets) != 0 {
		t.Errorf("expected 0 targets (all empty URLs skipped), got %d", len(n.targets))
	}

	// 调用 Notify 不应 panic
	n.Notify(Event{Kind: EventNodeDown, At: time.Now(), Message: "test"})
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// NewNotifierMulti — 含无效模板（fallback 到默认 JSON 序列化）
// ---------------------------------------------------------------------------

func TestCoverage_NewNotifierMulti_InvalidTemplate(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		// 验证收到的是有效 JSON
		var evt Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			t.Errorf("decode event: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 无效模板（未闭合的 action）
	invalidTemplate := `{{.Kind`
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: srv.URL, Template: invalidTemplate},
	}, "")

	// 无效模板应该 WARN 并 fallback 到默认 JSON（tmpl = nil）
	// 这意味着 webhook 仍会被调用，使用默认 JSON 格式
	n.Notify(Event{Kind: EventNodeRecovered, At: time.Now(), Message: "back"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Error("webhook should be called with default JSON when template is invalid")
	}
}

// ---------------------------------------------------------------------------
// Notify — evt.At 为零值时自动填充
// ---------------------------------------------------------------------------

func TestCoverage_Notify_ZeroAtTimeFilled(t *testing.T) {
	var received atomic.Int32
	var receivedAt time.Time

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err == nil {
			receivedAt = evt.At
		}
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	n := NewNotifier(logger, srv.URL)

	before := time.Now()
	// 发送 At 为零值的事件
	n.Notify(Event{Kind: EventQuotaExceeded, Message: "test"}) // At 默认为零值

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

	after := time.Now()
	if receivedAt.Before(before) || receivedAt.After(after) {
		t.Errorf("At should be auto-filled to ~now, got %v (expected between %v and %v)",
			receivedAt, before, after)
	}
}

// ---------------------------------------------------------------------------
// Notify — 事件过滤（event 不匹配时不发送）
// ---------------------------------------------------------------------------

func TestCoverage_Notify_EventNotMatched(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := zaptest.NewLogger(t)
	// 仅监听 node_down 事件
	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: srv.URL, Events: []string{EventNodeDown}},
	}, "")

	// 发送 quota_exceeded 事件（不匹配）
	n.Notify(Event{Kind: EventQuotaExceeded, At: time.Now(), Message: "quota"})

	time.Sleep(200 * time.Millisecond)
	if called.Load() != 0 {
		t.Errorf("webhook should NOT be called for non-matching event, got %d calls", called.Load())
	}
}

// ---------------------------------------------------------------------------
// NewNotifierMulti — legacyURL 与新式 target URL 相同 → 不重复（去重）
// ---------------------------------------------------------------------------

func TestCoverage_NewNotifierMulti_LegacyDeduplicated(t *testing.T) {
	logger := zaptest.NewLogger(t)
	url := "http://example.com/hook"

	n := NewNotifierMulti(logger, []WebhookTargetConfig{
		{URL: url},
	}, url) // legacyURL 与新式 target URL 相同

	if len(n.targets) != 1 {
		t.Errorf("expected 1 target (dup removed), got %d", len(n.targets))
	}
}

// ---------------------------------------------------------------------------
// Notify — 空 targets 列表（no-op）
// ---------------------------------------------------------------------------

func TestCoverage_Notify_EmptyTargets(t *testing.T) {
	logger := zaptest.NewLogger(t)
	n := NewNotifierMulti(logger, nil, "")

	// 不应 panic，也不会发送任何 webhook
	n.Notify(Event{Kind: EventHighLoad, At: time.Now(), Message: "high load"})
	time.Sleep(50 * time.Millisecond)
}
