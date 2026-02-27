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
