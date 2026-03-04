package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)


// newObserverLogger 返回一个捕获所有 DEBUG+ 日志的 in-memory logger。
func newObserverLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// ---------------------------------------------------------------------------
// sanitizeHeaders
// ---------------------------------------------------------------------------

func TestSanitizeHeaders_RemovesSensitiveKeys(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret")
	h.Set("X-Pairproxy-Auth", "jwt-token")
	h.Set("Cookie", "session=abc")

	field := sanitizeHeaders(h)
	m, ok := field.Interface.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", field.Interface)
	}
	for _, key := range []string{"Authorization", "X-Pairproxy-Auth", "Cookie"} {
		if _, found := m[key]; found {
			t.Errorf("sensitive header %q should be stripped but was present", key)
		}
	}
}

func TestSanitizeHeaders_PreservesNonSensitiveHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Request-Id", "req-123")

	field := sanitizeHeaders(h)
	m, ok := field.Interface.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", field.Interface)
	}
	if m["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want %q", m["Content-Type"], "application/json")
	}
	if m["X-Request-Id"] != "req-123" {
		t.Errorf("X-Request-Id = %q, want %q", m["X-Request-Id"], "req-123")
	}
}

func TestSanitizeHeaders_EmptyHeaders(t *testing.T) {
	field := sanitizeHeaders(http.Header{})
	m, ok := field.Interface.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", field.Interface)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// ---------------------------------------------------------------------------
// truncate
// ---------------------------------------------------------------------------

func TestTruncate_ShortBodyUnchanged(t *testing.T) {
	b := []byte("hello world")
	got := truncate(b, 100)
	if !bytes.Equal(got, b) {
		t.Errorf("expected unchanged body %q, got %q", b, got)
	}
}

func TestTruncate_ExactlyAtLimit(t *testing.T) {
	b := bytes.Repeat([]byte("x"), 64)
	got := truncate(b, 64)
	if len(got) != 64 {
		t.Errorf("expected len 64, got %d", len(got))
	}
}

func TestTruncate_LongBodyTruncated(t *testing.T) {
	b := bytes.Repeat([]byte("a"), debugBodyMaxBytes+100)
	got := truncate(b, debugBodyMaxBytes)
	if len(got) != debugBodyMaxBytes {
		t.Errorf("expected len %d, got %d", debugBodyMaxBytes, len(got))
	}
}

func TestTruncate_EmptyBody(t *testing.T) {
	got := truncate(nil, debugBodyMaxBytes)
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// debugResponseWriter
// ---------------------------------------------------------------------------

func TestDebugResponseWriter_LogsChunks(t *testing.T) {
	dl, logs := newObserverLogger()
	underlying := httptest.NewRecorder()

	drw := &debugResponseWriter{
		ResponseWriter: underlying,
		logger:         dl,
		reqID:          "test-req-id",
	}

	chunk := []byte("data: hello\n\n")
	n, err := drw.Write(chunk)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(chunk) {
		t.Errorf("Write returned %d, want %d", n, len(chunk))
	}

	// Verify underlying writer received the data.
	if underlying.Body.String() != string(chunk) {
		t.Errorf("underlying body = %q, want %q", underlying.Body.String(), string(chunk))
	}

	// Verify debug log entry was written.
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Message, "chunk") {
		t.Errorf("log message = %q, expected to contain 'chunk'", entries[0].Message)
	}
	if entries[0].Level != zapcore.DebugLevel {
		t.Errorf("log level = %v, want Debug", entries[0].Level)
	}
}

func TestDebugResponseWriter_ForwardsWrite(t *testing.T) {
	dl, _ := newObserverLogger()
	underlying := httptest.NewRecorder()

	drw := &debugResponseWriter{ResponseWriter: underlying, logger: dl, reqID: "r1"}
	payload := []byte("response body")
	drw.Write(payload) //nolint:errcheck

	if got := underlying.Body.Bytes(); !bytes.Equal(got, payload) {
		t.Errorf("underlying body = %q, want %q", got, payload)
	}
}

func TestDebugResponseWriter_FlushDelegates(t *testing.T) {
	dl, _ := newObserverLogger()
	flushed := false
	fw := &flushableRecorder{ResponseRecorder: httptest.NewRecorder(), onFlush: func() { flushed = true }}

	drw := &debugResponseWriter{ResponseWriter: fw, logger: dl, reqID: "r2"}
	drw.Flush()

	if !flushed {
		t.Error("expected Flush to be delegated to underlying ResponseWriter")
	}
}

// flushableRecorder is a test helper that records Flush calls.
type flushableRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (f *flushableRecorder) Flush() { f.onFlush() }

// ---------------------------------------------------------------------------
// CProxy.SetDebugLogger — debug output written to observer logger
// ---------------------------------------------------------------------------

func TestCProxy_SetDebugLogger_LogsClientRequest(t *testing.T) {
	// Mock s-proxy that accepts any request.
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1"}`) //nolint:errcheck
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	dl, logs := newObserverLogger()
	cp.SetDebugLogger(dl)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude"}`))
	req.Header.Set("Authorization", "Bearer dummy")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	cp.Handler().ServeHTTP(rr, req)

	// At least one "← client request" entry must exist.
	found := false
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "client request") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected '← client request' debug log; got entries: %v", logs.All())
	}
}

func TestCProxy_NoDebugLogger_DoesNotPanic(t *testing.T) {
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1"}`) //nolint:errcheck
	}))
	defer mockSProxy.Close()

	// Do NOT set a debug logger — default nil must be safe.
	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer dummy")
	rr := httptest.NewRecorder()

	// Must not panic.
	cp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestCProxy_SyncAndSetDebugLogger_SwapsLogger(t *testing.T) {
	mockSProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`) //nolint:errcheck
	}))
	defer mockSProxy.Close()

	cp, _ := newTestCProxy(t, mockSProxy.URL, validToken())

	dl1, logs1 := newObserverLogger()
	dl2, logs2 := newObserverLogger()

	cp.SetDebugLogger(dl1)

	// First request → captured by dl1.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req1.Header.Set("Authorization", "Bearer dummy")
	cp.Handler().ServeHTTP(httptest.NewRecorder(), req1)

	prevCount := logs1.Len()
	if prevCount == 0 {
		t.Fatal("expected dl1 to capture at least one entry before swap")
	}

	// Swap to dl2.
	cp.SetDebugLogger(dl2)

	// Second request → captured by dl2, dl1 count stays same.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req2.Header.Set("Authorization", "Bearer dummy")
	cp.Handler().ServeHTTP(httptest.NewRecorder(), req2)

	if logs1.Len() != prevCount {
		t.Errorf("dl1 received new entries after swap: had %d, now %d", prevCount, logs1.Len())
	}
	if logs2.Len() == 0 {
		t.Error("expected dl2 to capture entries after swap, got none")
	}
}

// ---------------------------------------------------------------------------
// SProxy.SetDebugLogger / SyncAndSetDebugLogger
// ---------------------------------------------------------------------------

func TestSProxy_SetDebugLogger_LogsClientRequest(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","content":[],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`) //nolint:errcheck
	}))
	defer mockLLM.Close()

	sp, jwtMgr, _, _ := newIntegrationSProxy(t, mockLLM.URL)

	dl, logs := newObserverLogger()
	sp.SetDebugLogger(dl)

	token := signToken(t, jwtMgr, "debug-user", "alice")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	// Expect "← client request" log entry.
	found := false
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "client request") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected '← client request' debug log; got entries: %v", logs.All())
	}
}

func TestSProxy_SetDebugLogger_LogsLLMResponse(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","content":[],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`) //nolint:errcheck
	}))
	defer mockLLM.Close()

	sp, jwtMgr, _, _ := newIntegrationSProxy(t, mockLLM.URL)

	dl, logs := newObserverLogger()
	sp.SetDebugLogger(dl)

	token := signToken(t, jwtMgr, "debug-user2", "bob")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	sp.Handler().ServeHTTP(httptest.NewRecorder(), req)

	// Expect both "→ LLM request" and "← LLM response" entries.
	var hasLLMReq, hasLLMResp bool
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "LLM request") {
			hasLLMReq = true
		}
		if strings.Contains(e.Message, "LLM response") {
			hasLLMResp = true
		}
	}
	if !hasLLMReq {
		t.Error("expected '→ LLM request' debug log entry")
	}
	if !hasLLMResp {
		t.Error("expected '← LLM response' debug log entry")
	}
}

func TestSProxy_NoDebugLogger_DoesNotPanic(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","content":[],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`) //nolint:errcheck
	}))
	defer mockLLM.Close()

	sp, jwtMgr, _, _ := newIntegrationSProxy(t, mockLLM.URL)
	// No SetDebugLogger call — must not panic.

	token := signToken(t, jwtMgr, "no-debug-user", "carol")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PairProxy-Auth", token)

	rr := httptest.NewRecorder()
	sp.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestSProxy_SyncAndSetDebugLogger_SwapsLogger(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","content":[],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`) //nolint:errcheck
	}))
	defer mockLLM.Close()

	sp, jwtMgr, _, _ := newIntegrationSProxy(t, mockLLM.URL)

	dl1, logs1 := newObserverLogger()
	dl2, logs2 := newObserverLogger()

	sp.SyncAndSetDebugLogger(dl1)

	doRequest := func(userID string) {
		token := signToken(t, jwtMgr, userID, userID)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages",
			strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-PairProxy-Auth", token)
		sp.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	doRequest("user-a")
	countAfterFirst := logs1.Len()
	if countAfterFirst == 0 {
		t.Fatal("dl1 should have captured entries before swap")
	}

	// Swap to dl2 — SyncAndSetDebugLogger must not panic (even if Sync is a no-op for in-memory logger).
	sp.SyncAndSetDebugLogger(dl2)

	doRequest("user-b")

	if logs1.Len() != countAfterFirst {
		t.Errorf("dl1 received new entries after swap: was %d, now %d", countAfterFirst, logs1.Len())
	}
	if logs2.Len() == 0 {
		t.Error("dl2 should have captured entries after swap")
	}
}

func TestSProxy_SyncAndSetDebugLogger_DisableWithNil(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","content":[],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`) //nolint:errcheck
	}))
	defer mockLLM.Close()

	sp, jwtMgr, _, _ := newIntegrationSProxy(t, mockLLM.URL)

	dl, logs := newObserverLogger()
	sp.SyncAndSetDebugLogger(dl)

	doRequest := func(userID string) {
		token := signToken(t, jwtMgr, userID, userID)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages",
			strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-PairProxy-Auth", token)
		sp.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	doRequest("user-x")
	if logs.Len() == 0 {
		t.Fatal("expected logs before disable")
	}

	// Disable (nil).
	sp.SyncAndSetDebugLogger(nil)

	countBefore := logs.Len()
	doRequest("user-y") // must not panic

	if logs.Len() != countBefore {
		t.Errorf("debug logger still active after SyncAndSetDebugLogger(nil): got %d extra entries", logs.Len()-countBefore)
	}
}

// ---------------------------------------------------------------------------
// debugBodyMaxBytes constant sanity check
// ---------------------------------------------------------------------------

func TestDebugBodyMaxBytes_Is64KB(t *testing.T) {
	const want = 64 * 1024
	if debugBodyMaxBytes != want {
		t.Errorf("debugBodyMaxBytes = %d, want %d", debugBodyMaxBytes, want)
	}
}

