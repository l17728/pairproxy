package tap

import (
	"testing"
)

// ---------------------------------------------------------------------------
// OpenAISSEParser: Feed / streaming
// ---------------------------------------------------------------------------

func TestOpenAISSEParser_Streaming_Basic(t *testing.T) {
	var gotIn, gotOut int
	p := NewOpenAISSEParser(func(in, out int) {
		gotIn, gotOut = in, out
	})

	sse := BuildOpenAISSE(100, 50, []string{"Hello", " world"})
	p.Feed([]byte(sse))

	if p.InputTokens() != 100 {
		t.Errorf("InputTokens() = %d, want 100", p.InputTokens())
	}
	if p.OutputTokens() != 50 {
		t.Errorf("OutputTokens() = %d, want 50", p.OutputTokens())
	}
	if gotIn != 100 || gotOut != 50 {
		t.Errorf("callback got (%d, %d), want (100, 50)", gotIn, gotOut)
	}
}

func TestOpenAISSEParser_Streaming_NoUsage(t *testing.T) {
	// 数据块没有 usage 字段（旧式格式，不带 include_usage=true）
	callbackCalled := false
	p := NewOpenAISSEParser(func(in, out int) {
		callbackCalled = true
	})

	sse := "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"
	sse += "data: [DONE]\n\n"
	p.Feed([]byte(sse))

	// 回调应被触发（token 为 0，因为无 usage 字段）
	if !callbackCalled {
		t.Error("callback should be triggered on [DONE]")
	}
	if p.InputTokens() != 0 || p.OutputTokens() != 0 {
		t.Errorf("tokens should be 0 when no usage, got (%d, %d)", p.InputTokens(), p.OutputTokens())
	}
}

func TestOpenAISSEParser_Streaming_ChunkBoundary(t *testing.T) {
	var gotIn, gotOut int
	p := NewOpenAISSEParser(func(in, out int) {
		gotIn, gotOut = in, out
	})

	sse := BuildOpenAISSE(200, 80, []string{"word1", "word2", "word3"})
	data := []byte(sse)

	// 按 16 字节 chunk 分批写入
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		p.Feed(data[i:end])
	}

	if gotIn != 200 {
		t.Errorf("callback inputTokens = %d, want 200", gotIn)
	}
	if gotOut != 80 {
		t.Errorf("callback outputTokens = %d, want 80", gotOut)
	}
}

func TestOpenAISSEParser_Streaming_CallbackOnlyOnce(t *testing.T) {
	calls := 0
	p := NewOpenAISSEParser(func(in, out int) { calls++ })

	sse := BuildOpenAISSE(10, 5, []string{"hi"})
	p.Feed([]byte(sse))
	// 再次 Feed 相同内容（done=true，应忽略）
	p.Feed([]byte(sse))

	if calls != 1 {
		t.Errorf("callback called %d times, want 1", calls)
	}
}

func TestOpenAISSEParser_Streaming_MultipleUsageBlocks(t *testing.T) {
	// 当有多个包含 usage 的块时，应使用最后一次（模拟 delta usage 更新场景）
	p := NewOpenAISSEParser(nil)

	// 第一个 usage 块
	p.Feed([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n"))
	// 第二个（更新的）usage 块
	p.Feed([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8,\"total_tokens\":18}}\n\n"))
	p.Feed([]byte("data: [DONE]\n\n"))

	if p.InputTokens() != 10 {
		t.Errorf("InputTokens() = %d, want 10 (last usage wins)", p.InputTokens())
	}
	if p.OutputTokens() != 8 {
		t.Errorf("OutputTokens() = %d, want 8 (last usage wins)", p.OutputTokens())
	}
}

func TestOpenAISSEParser_Streaming_InvalidJSON(t *testing.T) {
	// 无效 JSON 应被静默跳过，不崩溃
	p := NewOpenAISSEParser(nil)
	p.Feed([]byte("data: not-valid-json\n\n"))
	p.Feed([]byte("data: [DONE]\n\n"))
	// 不 panic，token 为 0
	if p.InputTokens() != 0 {
		t.Errorf("InputTokens() = %d, want 0 for invalid JSON", p.InputTokens())
	}
}

// ---------------------------------------------------------------------------
// OpenAISSEParser: ParseNonStreaming
// ---------------------------------------------------------------------------

func TestOpenAIParser_NonStreaming_Basic(t *testing.T) {
	p := NewOpenAISSEParser(nil)
	body := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
	}`)
	in, out := p.ParseNonStreaming(body)
	if in != 100 {
		t.Errorf("inputTokens = %d, want 100", in)
	}
	if out != 50 {
		t.Errorf("outputTokens = %d, want 50", out)
	}
}

func TestOpenAIParser_NonStreaming_NullUsage(t *testing.T) {
	p := NewOpenAISSEParser(nil)
	body := []byte(`{"id":"chatcmpl-123","usage":null}`)
	in, out := p.ParseNonStreaming(body)
	if in != 0 || out != 0 {
		t.Errorf("expected 0,0 for null usage, got %d,%d", in, out)
	}
}

func TestOpenAIParser_NonStreaming_MissingUsage(t *testing.T) {
	p := NewOpenAISSEParser(nil)
	body := []byte(`{"id":"chatcmpl-123","object":"chat.completion"}`)
	in, out := p.ParseNonStreaming(body)
	if in != 0 || out != 0 {
		t.Errorf("expected 0,0 for missing usage, got %d,%d", in, out)
	}
}

func TestOpenAIParser_NonStreaming_InvalidJSON(t *testing.T) {
	p := NewOpenAISSEParser(nil)
	in, out := p.ParseNonStreaming([]byte("not json"))
	if in != 0 || out != 0 {
		t.Errorf("expected 0,0 for invalid JSON, got %d,%d", in, out)
	}
}

// ---------------------------------------------------------------------------
// BuildOpenAISSE helper
// ---------------------------------------------------------------------------

func TestBuildOpenAISSE_ContainsDONE(t *testing.T) {
	sse := BuildOpenAISSE(10, 5, []string{"hello"})
	if len(sse) == 0 {
		t.Fatal("BuildOpenAISSE returned empty string")
	}
	// 应包含 [DONE] 和 prompt_tokens
	for _, want := range []string{"data: [DONE]", "prompt_tokens", "completion_tokens"} {
		found := false
		for i := 0; i+len(want) <= len(sse); i++ {
			if sse[i:i+len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("BuildOpenAISSE output should contain %q", want)
		}
	}
}

func TestBuildOpenAISSE_RoundTrip(t *testing.T) {
	// 构建 SSE 再用解析器解析，确保 token 数一致
	p := NewOpenAISSEParser(nil)
	sse := BuildOpenAISSE(123, 456, []string{"a", "b", "c"})
	p.Feed([]byte(sse))
	if p.InputTokens() != 123 {
		t.Errorf("InputTokens() = %d, want 123", p.InputTokens())
	}
	if p.OutputTokens() != 456 {
		t.Errorf("OutputTokens() = %d, want 456", p.OutputTokens())
	}
}

// ---------------------------------------------------------------------------
// OpenAISSEParser via ResponseParser interface
// ---------------------------------------------------------------------------

func TestOpenAIParser_ViaInterface(t *testing.T) {
	var p ResponseParser = NewOpenAISSEParser(nil)
	sse := BuildOpenAISSE(10, 5, []string{"hi"})
	p.Feed([]byte(sse))
	if p.InputTokens() != 10 {
		t.Errorf("InputTokens() = %d via interface, want 10", p.InputTokens())
	}
}
