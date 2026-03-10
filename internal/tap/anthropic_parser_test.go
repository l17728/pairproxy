package tap

import (
	"testing"
)

// ---------------------------------------------------------------------------
// TestSSEParserSingleFeed：一次性喂入完整 SSE 序列
// ---------------------------------------------------------------------------

func TestSSEParserSingleFeed(t *testing.T) {
	sse := BuildAnthropicSSE(100, 50, []string{"Hello", " world"})

	var gotInput, gotOutput int
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		gotInput = in
		gotOutput = out
		called = true
	})

	parser.Feed([]byte(sse))

	if !called {
		t.Fatal("OnComplete callback was not called")
	}
	if gotInput != 100 {
		t.Errorf("inputTokens = %d, want 100", gotInput)
	}
	if gotOutput != 50 {
		t.Errorf("outputTokens = %d, want 50", gotOutput)
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserChunked：按 1 字节逐块喂入（边界鲁棒性）
// ---------------------------------------------------------------------------

func TestSSEParserChunked(t *testing.T) {
	sse := BuildAnthropicSSE(200, 75, []string{"chunk"})

	var gotInput, gotOutput int
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		gotInput = in
		gotOutput = out
		called = true
	})

	// 按 1 字节逐块喂入
	for i := 0; i < len(sse); i++ {
		parser.Feed([]byte{sse[i]})
	}

	if !called {
		t.Fatal("OnComplete callback was not called with 1-byte chunks")
	}
	if gotInput != 200 {
		t.Errorf("inputTokens = %d, want 200", gotInput)
	}
	if gotOutput != 75 {
		t.Errorf("outputTokens = %d, want 75", gotOutput)
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserSmallChunks：按 7 字节喂入（常见 chunk 边界）
// ---------------------------------------------------------------------------

func TestSSEParserSmallChunks(t *testing.T) {
	sse := BuildAnthropicSSE(333, 111, []string{"a", "b", "c"})

	var gotInput, gotOutput int
	parser := NewAnthropicSSEParser(func(in, out int) {
		gotInput = in
		gotOutput = out
	})

	data := []byte(sse)
	chunkSize := 7
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		parser.Feed(data[i:end])
	}

	if gotInput != 333 {
		t.Errorf("inputTokens = %d, want 333", gotInput)
	}
	if gotOutput != 111 {
		t.Errorf("outputTokens = %d, want 111", gotOutput)
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserNoMessageStop：未收到 message_stop 不触发回调
// ---------------------------------------------------------------------------

func TestSSEParserNoMessageStop(t *testing.T) {
	partial := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` +
		"\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` +
		"\n\n"
	// 故意省略 message_delta 和 message_stop

	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})

	parser.Feed([]byte(partial))

	if called {
		t.Error("callback should NOT be called when message_stop is absent")
	}
	if parser.InputTokens() != 10 {
		t.Errorf("inputTokens = %d, want 10 (partially parsed)", parser.InputTokens())
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserCallbackOnceOnly：message_stop 后继续 Feed 不重复触发
// ---------------------------------------------------------------------------

func TestSSEParserCallbackOnceOnly(t *testing.T) {
	sse := BuildAnthropicSSE(10, 5, nil)

	callCount := 0
	parser := NewAnthropicSSEParser(func(in, out int) {
		callCount++
	})

	parser.Feed([]byte(sse))
	// 再次 Feed（模拟多余数据）
	parser.Feed([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))

	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserZeroTokens：token 数为 0 时正常触发
// ---------------------------------------------------------------------------

func TestSSEParserZeroTokens(t *testing.T) {
	sse := BuildAnthropicSSE(0, 0, nil)

	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		called = true
	})
	parser.Feed([]byte(sse))

	if !called {
		t.Error("callback should be called even with 0 tokens")
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserWindowsLineEndings：\r\n 行尾兼容性
// ---------------------------------------------------------------------------

func TestSSEParserWindowsLineEndings(t *testing.T) {
	// 使用 \r\n 行尾
	sse := "event: message_start\r\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":0}}}` +
		"\r\n\r\n" +
		"event: message_delta\r\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":21}}` +
		"\r\n\r\n" +
		"event: message_stop\r\n" +
		`data: {"type":"message_stop"}` +
		"\r\n\r\n"

	var gotInput, gotOutput int
	parser := NewAnthropicSSEParser(func(in, out int) {
		gotInput = in
		gotOutput = out
	})
	parser.Feed([]byte(sse))

	if gotInput != 42 {
		t.Errorf("inputTokens = %d, want 42", gotInput)
	}
	if gotOutput != 21 {
		t.Errorf("outputTokens = %d, want 21", gotOutput)
	}
}

// ---------------------------------------------------------------------------
// TestNonStreamingParse：解析普通 JSON 响应体
// ---------------------------------------------------------------------------

func TestNonStreamingParse(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 150,
			"output_tokens": 25
		}
	}`)

	in, out, err := ParseNonStreaming(body)
	if err != nil {
		t.Fatalf("ParseNonStreaming: %v", err)
	}
	if in != 150 {
		t.Errorf("inputTokens = %d, want 150", in)
	}
	if out != 25 {
		t.Errorf("outputTokens = %d, want 25", out)
	}
}

// ---------------------------------------------------------------------------
// TestNonStreamingParseInvalidJSON
// ---------------------------------------------------------------------------

func TestNonStreamingParseInvalidJSON(t *testing.T) {
	_, _, err := ParseNonStreaming([]byte("not-json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestBuildAnthropicSSE：辅助函数本身的正确性
// ---------------------------------------------------------------------------

func TestBuildAnthropicSSE(t *testing.T) {
	sse := BuildAnthropicSSE(100, 50, []string{"hello"})
	if len(sse) == 0 {
		t.Fatal("BuildAnthropicSSE returned empty string")
	}

	// 验证关键事件存在
	for _, want := range []string{"message_start", "message_delta", "message_stop"} {
		found := false
		for _, line := range splitLines(sse) {
			if line == "event: "+want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SSE output missing event: %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestSSEParserCacheTokens：含 prompt caching 时输入 token 正确求和
// ---------------------------------------------------------------------------

func TestSSEParserCacheTokens(t *testing.T) {
	// 模拟 Claude Code 场景：系统提示被缓存，只有当前消息计入 input_tokens
	// 总输入 = 10(input) + 0(cache_creation) + 500(cache_read) = 510
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":500,"cache_creation_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`

	var gotInput, gotOutput int
	var called bool
	parser := NewAnthropicSSEParser(func(in, out int) {
		gotInput = in
		gotOutput = out
		called = true
	})
	parser.Feed([]byte(sse))

	if !called {
		t.Fatal("OnComplete callback was not called")
	}
	if gotInput != 510 {
		t.Errorf("inputTokens = %d, want 510 (10 input + 500 cache_read)", gotInput)
	}
	if gotOutput != 30 {
		t.Errorf("outputTokens = %d, want 30", gotOutput)
	}
}

// TestSSEParserCacheCreationTokens：首次写入缓存时 cache_creation_input_tokens 也计入总输入
func TestSSEParserCacheCreationTokens(t *testing.T) {
	// 首次请求：系统提示写入缓存
	// 总输入 = 5(input) + 1000(cache_creation) + 0(cache_read) = 1005
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_y","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":1000}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`

	var gotInput int
	parser := NewAnthropicSSEParser(func(in, _ int) { gotInput = in })
	parser.Feed([]byte(sse))

	if gotInput != 1005 {
		t.Errorf("inputTokens = %d, want 1005 (5 input + 1000 cache_creation)", gotInput)
	}
}

// TestNonStreamingCacheTokens：非流式响应含 prompt caching 时输入 token 正确求和
func TestNonStreamingCacheTokens(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "claude-opus-4-6",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 25,
			"cache_read_input_tokens": 800,
			"cache_creation_input_tokens": 0
		}
	}`)

	in, out, err := ParseNonStreaming(body)
	if err != nil {
		t.Fatalf("ParseNonStreaming: %v", err)
	}
	// 总输入 = 10 + 800 + 0 = 810
	if in != 810 {
		t.Errorf("inputTokens = %d, want 810 (10 + 800 cache_read)", in)
	}
	if out != 25 {
		t.Errorf("outputTokens = %d, want 25", out)
	}
}

// splitLines 按换行符分割字符串（辅助函数）。
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	return lines
}
