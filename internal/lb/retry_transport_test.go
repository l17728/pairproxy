package lb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// mockRoundTripper — 可控的 http.RoundTripper 测试桩
// ---------------------------------------------------------------------------

type mockRoundTripper struct {
	responses []*http.Response
	errors    []error
	calls     int
	bodies    []string // 每次调用读取的 body 内容
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	idx := m.calls
	m.calls++

	// 记录 body
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		m.bodies = append(m.bodies, string(b))
	} else {
		m.bodies = append(m.bodies, "")
	}

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
	}, nil
}

func makeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// closeResponses 关闭 mock 中所有未被上层消费的 response body（用于 t.Cleanup）。
// NopCloser 多次 Close 是安全的。
func closeResponses(resps []*http.Response) {
	for _, r := range resps {
		if r != nil && r.Body != nil {
			r.Body.Close()
		}
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_SuccessFirstAttempt
// ---------------------------------------------------------------------------

func TestRetryTransport_SuccessFirstAttempt(t *testing.T) {
	inner := &mockRoundTripper{
		responses: []*http.Response{makeResp(200, `{"ok":true}`)},
	}
	t.Cleanup(func() { closeResponses(inner.responses) })
	successCalled := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		OnSuccess:  func(_ string) { successCalled++ },
		Logger:     zaptest.NewLogger(t),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", bytes.NewBufferString(`{"model":"claude"}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	resp.Body.Close()
	if inner.calls != 1 {
		t.Errorf("expected 1 call, got %d", inner.calls)
	}
	if successCalled != 1 {
		t.Errorf("expected OnSuccess called once, got %d", successCalled)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_RetryOnConnectionError
// ---------------------------------------------------------------------------

func TestRetryTransport_RetryOnConnectionError(t *testing.T) {
	connErr := errors.New("connection refused")
	inner := &mockRoundTripper{
		errors:    []error{connErr, nil},
		responses: []*http.Response{nil, makeResp(200, `{"ok":true}`)},
	}
	t.Cleanup(func() { closeResponses(inner.responses) })

	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext: func(_ string, tried []string) (*LLMTargetInfo, error) {
			pickCalls++
			return &LLMTargetInfo{URL: "http://target2", APIKey: "key2"}, nil
		},
		OnFailure: func(_ string) {},
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", bytes.NewBufferString(`{"data":1}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	resp.Body.Close()
	if inner.calls != 2 {
		t.Errorf("expected 2 calls, got %d", inner.calls)
	}
	if pickCalls != 1 {
		t.Errorf("expected PickNext called once, got %d", pickCalls)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_RetryOn5xx
// ---------------------------------------------------------------------------

func TestRetryTransport_RetryOn5xx(t *testing.T) {
	inner := &mockRoundTripper{
		responses: []*http.Response{
			makeResp(500, `{"error":"internal"}`),
			makeResp(200, `{"ok":true}`),
		},
	}
	t.Cleanup(func() { closeResponses(inner.responses) })

	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return &LLMTargetInfo{URL: "http://target2", APIKey: "key2"}, nil
		},
		OnFailure: func(_ string) {},
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if inner.calls != 2 {
		t.Errorf("expected 2 inner calls, got %d", inner.calls)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_NoRetryOn4xx
// ---------------------------------------------------------------------------

func TestRetryTransport_NoRetryOn4xx(t *testing.T) {
	inner := &mockRoundTripper{
		responses: []*http.Response{
			makeResp(429, `{"error":"rate_limit"}`),
		},
	}
	t.Cleanup(func() { closeResponses(inner.responses) })
	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext:   func(_ string, _ []string) (*LLMTargetInfo, error) { pickCalls++; return nil, nil },
		OnSuccess:  func(_ string) {},
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error for 4xx: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if pickCalls != 0 {
		t.Errorf("PickNext should not be called for 4xx, got %d calls", pickCalls)
	}
	if inner.calls != 1 {
		t.Errorf("expected 1 inner call, got %d", inner.calls)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_NoRetryOnContextCanceled
// ---------------------------------------------------------------------------

func TestRetryTransport_NoRetryOnContextCanceled(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{context.Canceled},
	}
	pickCalls := 0
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext:   func(_ string, _ []string) (*LLMTargetInfo, error) { pickCalls++; return nil, nil },
		Logger:     zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://target1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if pickCalls != 0 {
		t.Errorf("PickNext should not be called on context.Canceled, got %d calls", pickCalls)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_MaxRetriesExhausted
// ---------------------------------------------------------------------------

func TestRetryTransport_MaxRetriesExhausted(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{
			errors.New("conn refused 1"),
			errors.New("conn refused 2"),
			errors.New("conn refused 3"),
		},
	}
	pickIdx := 0
	targets := []string{"http://t2", "http://t3"}
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 2,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			if pickIdx >= len(targets) {
				return nil, ErrNoHealthyTarget
			}
			tgt := &LLMTargetInfo{URL: targets[pickIdx], APIKey: "k"}
			pickIdx++
			return tgt, nil
		},
		OnFailure: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error when all targets exhausted")
	}
	if inner.calls != 3 {
		t.Errorf("expected 3 inner calls (1 + 2 retries), got %d", inner.calls)
	}
}

// ---------------------------------------------------------------------------
// TestRetryTransport_BodyRestoredOnRetry
// ---------------------------------------------------------------------------

func TestRetryTransport_BodyRestoredOnRetry(t *testing.T) {
	inner := &mockRoundTripper{
		errors: []error{errors.New("conn refused"), nil},
		responses: []*http.Response{
			nil,
			makeResp(200, `{"ok":true}`),
		},
	}
	t.Cleanup(func() { closeResponses(inner.responses) })

	const requestBody = `{"model":"claude","messages":[]}`
	rt := &RetryTransport{
		Inner:      inner,
		MaxRetries: 1,
		PickNext: func(_ string, _ []string) (*LLMTargetInfo, error) {
			return &LLMTargetInfo{URL: "http://t2", APIKey: "k2"}, nil
		},
		OnFailure: func(_ string) {},
		OnSuccess: func(_ string) {},
		Logger:    zaptest.NewLogger(t),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://t1/v1/messages", bytes.NewBufferString(requestBody))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	resp.Body.Close()

	// 两次调用都应该能读到相同的 body
	if len(inner.bodies) != 2 {
		t.Fatalf("expected 2 body reads, got %d", len(inner.bodies))
	}
	// 第一次尝试 (conn refused) body 应正常读取到
	// 第二次重试 body 应与原始一致
	if inner.bodies[1] != requestBody {
		t.Errorf("retry body = %q, want %q", inner.bodies[1], requestBody)
	}
}
