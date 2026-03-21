package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/corpus"
)

// mockClassifierTarget 固定返回指定 URL 和 apiKey 的测试用分类器目标
type mockClassifierTarget struct {
	url    string
	apiKey string
	err    error
}

func (m *mockClassifierTarget) Pick(_ context.Context) (string, string, error) {
	return m.url, m.apiKey, m.err
}

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func makeRules(n int) []RouteRule {
	rules := make([]RouteRule, n)
	for i := range rules {
		rules[i] = RouteRule{
			ID:          fmt.Sprintf("rule-%d", i),
			Name:        fmt.Sprintf("rule%d", i),
			Description: fmt.Sprintf("description for rule %d", i),
			TargetURLs:  []string{fmt.Sprintf("https://target%d.example.com", i)},
			Priority:    i,
			IsActive:    true,
		}
	}
	return rules
}

// anthropicResponse 构造简单的 Anthropic /v1/messages 响应
func anthropicResponse(text string) string {
	resp := map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// TestRoute_SkipsClassifierSubRequest 分类器子请求不触发语义路由（防递归）
func TestRoute_SkipsClassifierSubRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse("0"))
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(1),
		&mockClassifierTarget{url: srv.URL}, 3*time.Second, "")

	ctx := WithClassifierContext(context.Background())
	result := sr.Route(ctx, []corpus.Message{{Role: "user", Content: "hello"}})

	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
	if called {
		t.Error("classifier should not be called for sub-requests")
	}
}

// TestRoute_NoActiveRules 无激活规则时直接返回 nil
func TestRoute_NoActiveRules(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), nil, &mockClassifierTarget{url: srv.URL}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})

	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
	if called {
		t.Error("classifier should not be called when no rules")
	}
}

// TestRoute_Timeout 分类器超时时返回 nil（fallback）
func TestRoute_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 睡眠超过超时时间
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(1),
		&mockClassifierTarget{url: srv.URL}, 100*time.Millisecond, "")

	start := time.Now()
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})
	elapsed := time.Since(start)

	if result != nil {
		t.Errorf("expected nil on timeout, got %v", result)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("timeout not respected: elapsed %v", elapsed)
	}
}

// TestRoute_HTTP500 分类器返回 500 时 fallback
func TestRoute_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(2),
		&mockClassifierTarget{url: srv.URL}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})

	if result != nil {
		t.Errorf("expected nil on HTTP 500, got %v", result)
	}
}

// TestRoute_Match 分类器返回 "1" 时返回 rules[1].TargetURLs
func TestRoute_Match(t *testing.T) {
	rules := makeRules(3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// rules 按 priority 降序排列后索引可能变化；priority=0,1,2 → 降序 [2,1,0]
		// 返回 "1" 匹配的是 priority=1 的规则（排序后 index=1）
		fmt.Fprint(w, anthropicResponse("1"))
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), rules, &mockClassifierTarget{url: srv.URL}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "code question"}})

	if result == nil {
		t.Fatal("expected candidate URLs, got nil")
	}
	// 降序排列后 index=1 对应 priority=1 → rule1
	expected := []string{"https://target1.example.com"}
	if len(result) != 1 || result[0] != expected[0] {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

// TestRoute_NoMatch 分类器返回 "-1" 时返回 nil
func TestRoute_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse("-1"))
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(2),
		&mockClassifierTarget{url: srv.URL}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})

	if result != nil {
		t.Errorf("expected nil on -1, got %v", result)
	}
}

// TestRoute_OutOfRange 分类器返回越界索引时 fallback
func TestRoute_OutOfRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse("99"))
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(2),
		&mockClassifierTarget{url: srv.URL}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})

	if result != nil {
		t.Errorf("expected nil on out-of-range index, got %v", result)
	}
}

// TestBuildPrompt_LastN 超过 maxPromptMessages 条时只取最后 N 条
func TestBuildPrompt_LastN(t *testing.T) {
	sr := NewSemanticRouter(testLogger(), nil, nil, 3*time.Second, "")
	rules := makeRules(1)

	messages := make([]corpus.Message, 10)
	for i := range messages {
		messages[i] = corpus.Message{Role: "user", Content: fmt.Sprintf("message %d", i)}
	}

	prompt := sr.buildPrompt(messages, rules)

	// 前 5 条消息不应出现在 prompt 中
	for i := 0; i < 5; i++ {
		if contains(prompt, fmt.Sprintf("message %d", i)) {
			t.Errorf("prompt should not contain early message %d", i)
		}
	}
	// 后 5 条消息应出现
	for i := 5; i < 10; i++ {
		if !contains(prompt, fmt.Sprintf("message %d", i)) {
			t.Errorf("prompt should contain recent message %d", i)
		}
	}
}

// TestSetRules_HotReload SetRules 后新规则立即生效
func TestSetRules_HotReload(t *testing.T) {
	newTargetURL := "https://new-target.example.com"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse("0"))
	}))
	defer srv.Close()

	sr := NewSemanticRouter(testLogger(), makeRules(1),
		&mockClassifierTarget{url: srv.URL}, 3*time.Second, "")

	// 热更新规则
	newRules := []RouteRule{{
		ID:         "new-rule",
		Name:       "new-rule",
		Description: "new rule description",
		TargetURLs: []string{newTargetURL},
		Priority:   100,
		IsActive:   true,
	}}
	sr.SetRules(newRules)

	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})
	if result == nil {
		t.Fatal("expected result after hot reload")
	}
	if len(result) != 1 || result[0] != newTargetURL {
		t.Errorf("expected %v, got %v", []string{newTargetURL}, result)
	}
}

// TestPickError 分类器 Pick 失败时 fallback
func TestPickError(t *testing.T) {
	sr := NewSemanticRouter(testLogger(), makeRules(1),
		&mockClassifierTarget{err: fmt.Errorf("no healthy target")}, 3*time.Second, "")
	result := sr.Route(context.Background(), []corpus.Message{{Role: "user", Content: "hello"}})
	if result != nil {
		t.Errorf("expected nil on Pick error, got %v", result)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
