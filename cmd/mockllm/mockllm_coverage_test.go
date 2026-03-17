package main

// mockllm_coverage_test.go — 补充覆盖未覆盖的分支：
//   - extractUserContent: 空消息列表、无 user 消息、content 为 []any 含 map[text]
//   - extractUserContent: content 为 []any 但 block 中无 text 字段
//   - handleMessages: body 读取失败（不可能通过 httptest 触发，改为 json 解析失败）
//   - handleMessages: json 解析失败
//   - serveJSON: verbose=true 路径
//   - serveSSE: verbose=true 路径
//   - estimateTokens: 空字符串、单词、多词
//   - splitIntoChunks: n<=1、空字符串、单字符、多 chunk

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// extractUserContent — 空消息列表
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_Empty(t *testing.T) {
	got := extractUserContent([]msgItem{})
	if got != "" {
		t.Errorf("empty messages: got %q, want %q", got, "")
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — 没有 user 角色的消息
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_NoUserRole(t *testing.T) {
	msgs := []msgItem{
		{Role: "assistant", Content: "hello"},
		{Role: "system", Content: "be helpful"},
	}
	got := extractUserContent(msgs)
	if got != "" {
		t.Errorf("no user role: got %q, want %q", got, "")
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — content 为字符串（正常路径）
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_StringContent(t *testing.T) {
	msgs := []msgItem{
		{Role: "user", Content: "hello world"},
	}
	got := extractUserContent(msgs)
	if got != "hello world" {
		t.Errorf("string content: got %q, want 'hello world'", got)
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — content 为 []any（content block 格式）含 text
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_ContentBlockWithText(t *testing.T) {
	// 模拟 JSON 反序列化后的 []any 格式
	rawMsg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "block text content"},
		},
	}
	// 通过 JSON 往返确保类型一致
	data, _ := json.Marshal(rawMsg)

	var raw struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(`{"messages": []}`), &raw)

	// 直接构造带 []any content 的 msgItem
	var item msgItem
	_ = json.Unmarshal(data, &item)
	msgs := []msgItem{item}

	got := extractUserContent(msgs)
	if got != "block text content" {
		t.Errorf("content block: got %q, want 'block text content'", got)
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — content 为 []any，但 block 中无 text 字段
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_ContentBlockNoText(t *testing.T) {
	rawMsg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "image", "source": "data:..."},
		},
	}
	data, _ := json.Marshal(rawMsg)
	var item msgItem
	_ = json.Unmarshal(data, &item)

	msgs := []msgItem{item}
	got := extractUserContent(msgs)
	// 没有 text 字段，返回空字符串
	if got != "" {
		t.Errorf("content block without text: got %q, want ''", got)
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — content 为 []any，但 item 不是 map（跳过）
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_ContentBlockNotMap(t *testing.T) {
	// 包含非 map 类型的 item（如 string）
	rawMsg := map[string]any{
		"role":    "user",
		"content": []any{"not a map"},
	}
	data, _ := json.Marshal(rawMsg)
	var item msgItem
	_ = json.Unmarshal(data, &item)

	msgs := []msgItem{item}
	got := extractUserContent(msgs)
	if got != "" {
		t.Errorf("non-map block: got %q, want ''", got)
	}
}

// ---------------------------------------------------------------------------
// extractUserContent — 多条消息，取最后一条 user
// ---------------------------------------------------------------------------

func TestCoverage_ExtractUserContent_LastUserMessage(t *testing.T) {
	msgs := []msgItem{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "response"},
		{Role: "user", Content: "last user message"},
	}
	got := extractUserContent(msgs)
	if got != "last user message" {
		t.Errorf("last user: got %q, want 'last user message'", got)
	}
}

// ---------------------------------------------------------------------------
// handleMessages — JSON 解析失败 → 400
// ---------------------------------------------------------------------------

func TestCoverage_HandleMessages_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(handleMessages))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json",
		bytes.NewBufferString(`{invalid json`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// serveJSON — verbose=true 路径
// ---------------------------------------------------------------------------

func TestCoverage_ServeJSON_Verbose(t *testing.T) {
	*flagVerbose = true
	defer func() { *flagVerbose = false }()

	srv := httptest.NewServer(http.HandlerFunc(handleMessages))
	defer srv.Close()

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":      "test-model",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hello verbose"}},
		"stream":     false,
	})
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json",
		bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// serveSSE — verbose=true 路径
// ---------------------------------------------------------------------------

func TestCoverage_ServeSSE_Verbose(t *testing.T) {
	*flagVerbose = true
	*flagChunks = 1
	defer func() {
		*flagVerbose = false
		*flagChunks = 1
	}()

	srv := httptest.NewServer(http.HandlerFunc(handleMessages))
	defer srv.Close()

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":      "test-model",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hello verbose stream"}},
		"stream":     true,
	})
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json",
		bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// estimateTokens — 各种输入
// ---------------------------------------------------------------------------

func TestCoverage_EstimateTokens(t *testing.T) {
	cases := []struct {
		input string
		min   int // 最小期望结果
	}{
		{"", 1},                    // 空字符串 → len(Fields(""))+1 = 0+1 = 1
		{"hello", 2},               // 1 词 → 1+1 = 2
		{"hello world foo", 4},     // 3 词 → 3+1 = 4
		{"  spaces  everywhere  ", 3}, // 2 词 → 2+1 = 3
	}
	for _, tc := range cases {
		got := estimateTokens(tc.input)
		if got < tc.min {
			t.Errorf("estimateTokens(%q) = %d, want >= %d", tc.input, got, tc.min)
		}
		if got < 1 {
			t.Errorf("estimateTokens should always return >= 1, got %d", got)
		}
	}
}

// ---------------------------------------------------------------------------
// splitIntoChunks — 各种边界情况
// ---------------------------------------------------------------------------

func TestCoverage_SplitIntoChunks(t *testing.T) {
	cases := []struct {
		s     string
		n     int
		parts int
		desc  string
	}{
		{"hello", 1, 1, "n=1 → 1 chunk"},
		{"hello", 0, 1, "n=0 → 1 chunk (n<=1)"},
		{"", 5, 1, "empty string → 1 chunk"},
		{"hello", 5, 5, "5 chars, n=5 → 5 chunks of 1"},
		{"hello world", 3, 3, "11 chars / 3 ≈ 4 per chunk → 3 chunks"},
		{"ab", 5, 2, "2 chars, n=5 → 2 chunks"},
		{"hello", 2, 2, "5 chars, n=2 → 2 chunks: 'hel'+'lo'"},
	}
	for _, tc := range cases {
		result := splitIntoChunks(tc.s, tc.n)
		if tc.s == "" {
			// 空字符串始终返回 []string{""}，长度为 1
			if len(result) != 1 {
				t.Errorf("%s: got %d chunks, want 1", tc.desc, len(result))
			}
			continue
		}
		if len(result) != tc.parts {
			t.Errorf("%s: got %d chunks, want %d", tc.desc, len(result), tc.parts)
		}
		// 验证拼接后等于原字符串
		joined := ""
		for _, p := range result {
			joined += p
		}
		if joined != tc.s {
			t.Errorf("%s: chunks don't reconstruct original: got %q, want %q",
				tc.desc, joined, tc.s)
		}
	}
}

// ---------------------------------------------------------------------------
// splitIntoChunks — n > len(runes)（超过字符数）
// ---------------------------------------------------------------------------

func TestCoverage_SplitIntoChunks_MoreChunksThanChars(t *testing.T) {
	result := splitIntoChunks("hi", 100)
	// 2 chars，最多 2 个 chunk
	joined := ""
	for _, p := range result {
		joined += p
	}
	if joined != "hi" {
		t.Errorf("chunks should reconstruct 'hi', got %q", joined)
	}
}

// ---------------------------------------------------------------------------
// trunc — 各种情况
// ---------------------------------------------------------------------------

func TestCoverage_Trunc(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
	}
	for _, tc := range cases {
		got := trunc(tc.s, tc.max)
		if got != tc.want {
			t.Errorf("trunc(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// serveJSON — 不含 user 消息（content 为空）
// ---------------------------------------------------------------------------

func TestCoverage_ServeJSON_EmptyContent(t *testing.T) {
	*flagVerbose = false
	srv := httptest.NewServer(http.HandlerFunc(handleMessages))
	defer srv.Close()

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":      "test-model",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "assistant", "content": "I am assistant"}},
		"stream":     false,
	})
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json",
		bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// 即使没有 user 消息，也应返回 200（回显空内容）
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for no user message, got %d", resp.StatusCode)
	}
}
