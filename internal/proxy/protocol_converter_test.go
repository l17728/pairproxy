package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockResponseWriter 实现 http.ResponseWriter 接口用于测试
type mockResponseWriter struct {
	buf        bytes.Buffer
	header     http.Header
	statusCode int
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{
		header: make(http.Header),
	}
}

func (m *mockResponseWriter) Header() http.Header {
	return m.header
}

func (m *mockResponseWriter) Write(data []byte) (int, error) {
	return m.buf.Write(data)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func (m *mockResponseWriter) String() string {
	return m.buf.String()
}

func TestShouldConvertProtocol(t *testing.T) {
	tests := []struct {
		name           string
		requestPath    string
		targetProvider string
		want           bool
	}{
		{
			name:           "anthropic path + ollama target → convert",
			requestPath:    "/v1/messages",
			targetProvider: "ollama",
			want:           true,
		},
		{
			name:           "anthropic path + openai target → convert",
			requestPath:    "/v1/messages",
			targetProvider: "openai",
			want:           true,
		},
		{
			name:           "anthropic path + anthropic target → no convert",
			requestPath:    "/v1/messages",
			targetProvider: "anthropic",
			want:           false,
		},
		{
			name:           "openai path + ollama target → no convert",
			requestPath:    "/v1/chat/completions",
			targetProvider: "ollama",
			want:           false,
		},
		{
			name:           "empty provider → no convert",
			requestPath:    "/v1/messages",
			targetProvider: "",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldConvertProtocol(tt.requestPath, tt.targetProvider)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertAnthropicToOpenAIRequest(t *testing.T) {
	logger := zap.NewNop()

	t.Run("simple text message", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{
					"role":    "user",
					"content": "Hello, world!",
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, newPath, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id")
		require.NoError(t, err)
		assert.Equal(t, "/v1/chat/completions", newPath)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		assert.Equal(t, "claude-3-5-sonnet-20241022", openaiReq["model"])
		assert.Equal(t, float64(1024), openaiReq["max_tokens"])

		messages := openaiReq["messages"].([]interface{})
		require.Len(t, messages, 1)

		msg := messages[0].(map[string]interface{})
		assert.Equal(t, "user", msg["role"])
		assert.Equal(t, "Hello, world!", msg["content"])
	})

	t.Run("with system message", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"system":     "You are a helpful assistant.",
			"messages": []interface{}{
				map[string]interface{}{
					"role":    "user",
					"content": "Hello!",
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		messages := openaiReq["messages"].([]interface{})
		require.Len(t, messages, 2)

		// First message should be system
		systemMsg := messages[0].(map[string]interface{})
		assert.Equal(t, "system", systemMsg["role"])
		assert.Equal(t, "You are a helpful assistant.", systemMsg["content"])

		// Second message should be user
		userMsg := messages[1].(map[string]interface{})
		assert.Equal(t, "user", userMsg["role"])
		assert.Equal(t, "Hello!", userMsg["content"])
	})

	t.Run("structured content blocks", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": "First part",
						},
						map[string]interface{}{
							"type": "text",
							"text": "Second part",
						},
					},
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		messages := openaiReq["messages"].([]interface{})
		require.Len(t, messages, 1)

		msg := messages[0].(map[string]interface{})
		assert.Equal(t, "user", msg["role"])
		assert.Equal(t, "First part\nSecond part", msg["content"])
	})

	t.Run("streaming request with stream_options injection", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"stream":     true,
			"messages": []interface{}{
				map[string]interface{}{
					"role":    "user",
					"content": "Hello!",
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		assert.True(t, openaiReq["stream"].(bool))

		// Verify stream_options was injected
		streamOpts := openaiReq["stream_options"].(map[string]interface{})
		assert.True(t, streamOpts["include_usage"].(bool))
	})

	t.Run("empty body", func(t *testing.T) {
		converted, newPath, err := convertAnthropicToOpenAIRequest([]byte{}, logger, "test-req-id")
		require.NoError(t, err)
		assert.Equal(t, "/v1/chat/completions", newPath)
		assert.Empty(t, converted)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		body := []byte(`{invalid json}`)
		converted, newPath, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id")
		assert.Error(t, err)
		assert.Equal(t, "/v1/chat/completions", newPath)
		assert.Equal(t, body, converted) // Should return original
	})
}

func TestConvertOpenAIToAnthropicResponse(t *testing.T) {
	logger := zap.NewNop()

	t.Run("successful response", func(t *testing.T) {
		openaiResp := map[string]interface{}{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"model":   "gpt-4",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Hello! How can I help you?",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     float64(10),
				"completion_tokens": float64(8),
				"total_tokens":      float64(18),
			},
		}
		body, _ := json.Marshal(openaiResp)

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req-id")
		require.NoError(t, err)

		var anthropicResp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &anthropicResp))

		assert.Equal(t, "chatcmpl-123", anthropicResp["id"])
		assert.Equal(t, "message", anthropicResp["type"])
		assert.Equal(t, "assistant", anthropicResp["role"])
		assert.Equal(t, "gpt-4", anthropicResp["model"])

		content := anthropicResp["content"].([]interface{})
		require.Len(t, content, 1)
		contentBlock := content[0].(map[string]interface{})
		assert.Equal(t, "text", contentBlock["type"])
		assert.Equal(t, "Hello! How can I help you?", contentBlock["text"])

		usage := anthropicResp["usage"].(map[string]interface{})
		assert.Equal(t, float64(10), usage["input_tokens"])
		assert.Equal(t, float64(8), usage["output_tokens"])

		assert.Equal(t, "end_turn", anthropicResp["stop_reason"])
	})

	t.Run("empty body", func(t *testing.T) {
		converted, err := convertOpenAIToAnthropicResponse([]byte{}, logger, "test-req-id")
		require.NoError(t, err)
		assert.Empty(t, converted)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		body := []byte(`{invalid}`)
		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req-id")
		assert.Error(t, err)
		assert.Equal(t, body, converted) // Should return original
	})
}

func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name    string
		content interface{}
		want    string
	}{
		{
			name:    "simple string",
			content: "Hello, world!",
			want:    "Hello, world!",
		},
		{
			name: "single text block",
			content: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Hello!",
				},
			},
			want: "Hello!",
		},
		{
			name: "multiple text blocks",
			content: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "First",
				},
				map[string]interface{}{
					"type": "text",
					"text": "Second",
				},
			},
			want: "First\nSecond",
		},
		{
			name: "mixed blocks (only text extracted)",
			content: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Text content",
				},
				map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type": "base64",
						"data": "...",
					},
				},
			},
			want: "Text content",
		},
		{
			name:    "nil content",
			content: nil,
			want:    "<nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextContent(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertFinishReason(t *testing.T) {
	tests := []struct {
		openaiReason string
		want         string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"content_filter", "content_filter"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.openaiReason, func(t *testing.T) {
			got := convertFinishReason(tt.openaiReason)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIToAnthropicStreamConverter(t *testing.T) {
	logger := zap.NewNop()

	t.Run("complete streaming response", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-123")

		// Simulate OpenAI SSE chunks
		chunks := []string{
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":" world"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}

		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()

		// Verify message_start event
		assert.Contains(t, output, "event: message_start")
		assert.Contains(t, output, `"type":"message_start"`)

		// Verify content_block_start event
		assert.Contains(t, output, "event: content_block_start")

		// Verify content_block_delta events
		assert.Contains(t, output, "event: content_block_delta")
		assert.Contains(t, output, `"text":"Hello"`)
		assert.Contains(t, output, `"text":" world"`)

		// Verify content_block_stop event
		assert.Contains(t, output, "event: content_block_stop")

		// Verify message_delta event with usage
		assert.Contains(t, output, "event: message_delta")
		assert.Contains(t, output, `"output_tokens":5`)

		// Verify message_stop event
		assert.Contains(t, output, "event: message_stop")
	})

	t.Run("empty chunks", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-456")

		_, err := converter.Write([]byte("\n\n"))
		require.NoError(t, err)

		// Should not crash, output may be empty
		assert.NotPanics(t, func() {
			converter.Write([]byte(""))
		})
	})

	t.Run("malformed JSON chunk", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-789")

		// Should not crash on malformed JSON
		_, err := converter.Write([]byte(`data: {invalid json}` + "\n\n"))
		require.NoError(t, err)

		// Should still handle valid chunks after malformed ones
		_, err = converter.Write([]byte(`data: [DONE]` + "\n\n"))
		require.NoError(t, err)

		output := w.String()
		assert.Contains(t, output, "event: message_stop")
	})
}

// TestProtocolConversionRoundTrip 测试完整的请求-响应转换流程
func TestProtocolConversionRoundTrip(t *testing.T) {
	logger := zap.NewNop()

	// 1. Anthropic request → OpenAI request
	anthropicReq := map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 1024,
		"system":     "You are helpful.",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "What is 2+2?",
			},
		},
	}
	anthropicBody, _ := json.Marshal(anthropicReq)

	openaiBody, newPath, err := convertAnthropicToOpenAIRequest(anthropicBody, logger, "test-req")
	require.NoError(t, err)
	assert.Equal(t, "/v1/chat/completions", newPath)

	var openaiReq map[string]interface{}
	require.NoError(t, json.Unmarshal(openaiBody, &openaiReq))
	assert.Equal(t, "claude-3-5-sonnet-20241022", openaiReq["model"])

	// 2. Simulate OpenAI response
	openaiResp := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"model":   "claude-3-5-sonnet-20241022",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "2+2 equals 4.",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(15),
			"completion_tokens": float64(6),
			"total_tokens":      float64(21),
		},
	}
	openaiRespBody, _ := json.Marshal(openaiResp)

	// 3. OpenAI response → Anthropic response
	anthropicRespBody, err := convertOpenAIToAnthropicResponse(openaiRespBody, logger, "test-req")
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	require.NoError(t, json.Unmarshal(anthropicRespBody, &anthropicResp))

	// Verify final Anthropic response structure
	assert.Equal(t, "message", anthropicResp["type"])
	assert.Equal(t, "assistant", anthropicResp["role"])

	content := anthropicResp["content"].([]interface{})
	require.Len(t, content, 1)
	contentBlock := content[0].(map[string]interface{})
	assert.Equal(t, "text", contentBlock["type"])
	assert.Equal(t, "2+2 equals 4.", contentBlock["text"])

	usage := anthropicResp["usage"].(map[string]interface{})
	assert.Equal(t, float64(15), usage["input_tokens"])
	assert.Equal(t, float64(6), usage["output_tokens"])

	assert.Equal(t, "end_turn", anthropicResp["stop_reason"])
}

// ---------------------------------------------------------------------------
// P0 新增测试：工具调用请求/响应转换
// ---------------------------------------------------------------------------

func TestConvertFinishReasonToolCalls(t *testing.T) {
	tests := []struct {
		openaiReason string
		want         string
	}{
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
	}
	for _, tt := range tests {
		t.Run(tt.openaiReason, func(t *testing.T) {
			got := convertFinishReason(tt.openaiReason)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProcessAssistantContent(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msgs := processAssistantContent("Hello")
		require.Len(t, msgs, 1)
		assert.Equal(t, "assistant", msgs[0]["role"])
		assert.Equal(t, "Hello", msgs[0]["content"])
	})

	t.Run("pure tool_use → tool_calls with null content", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":  "tool_use",
				"id":    "toolu_01",
				"name":  "get_weather",
				"input": map[string]interface{}{"city": "Beijing"},
			},
		}
		msgs := processAssistantContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "assistant", msgs[0]["role"])
		assert.Nil(t, msgs[0]["content"]) // null when only tool calls

		toolCalls := msgs[0]["tool_calls"].([]map[string]interface{})
		require.Len(t, toolCalls, 1)
		assert.Equal(t, "toolu_01", toolCalls[0]["id"])
		assert.Equal(t, "function", toolCalls[0]["type"])
		funcMap := toolCalls[0]["function"].(map[string]interface{})
		assert.Equal(t, "get_weather", funcMap["name"])
		// input（对象）→ arguments（JSON 字符串）
		args := funcMap["arguments"].(string)
		var parsedArgs map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(args), &parsedArgs))
		assert.Equal(t, "Beijing", parsedArgs["city"])
	})

	t.Run("mixed text + tool_use", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "text", "text": "Let me check."},
			map[string]interface{}{
				"type":  "tool_use",
				"id":    "toolu_02",
				"name":  "get_weather",
				"input": map[string]interface{}{"city": "Shanghai"},
			},
		}
		msgs := processAssistantContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "Let me check.", msgs[0]["content"])
		toolCalls := msgs[0]["tool_calls"].([]map[string]interface{})
		require.Len(t, toolCalls, 1)
		assert.Equal(t, "toolu_02", toolCalls[0]["id"])
	})

	t.Run("parallel tool_use blocks preserve order", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":  "tool_use",
				"id":    "toolu_01",
				"name":  "func_a",
				"input": map[string]interface{}{"x": 1},
			},
			map[string]interface{}{
				"type":  "tool_use",
				"id":    "toolu_02",
				"name":  "func_b",
				"input": map[string]interface{}{"y": 2},
			},
		}
		msgs := processAssistantContent(content)
		require.Len(t, msgs, 1)
		toolCalls := msgs[0]["tool_calls"].([]map[string]interface{})
		require.Len(t, toolCalls, 2)
		assert.Equal(t, "toolu_01", toolCalls[0]["id"])
		assert.Equal(t, "toolu_02", toolCalls[1]["id"])
	})
}

func TestProcessUserContent(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msgs := processUserContent("Hello")
		require.Len(t, msgs, 1)
		assert.Equal(t, "user", msgs[0]["role"])
		assert.Equal(t, "Hello", msgs[0]["content"])
	})

	t.Run("pure tool_result → role:tool message", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "toolu_01",
				"content":     "Sunny, 25°C",
			},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "tool", msgs[0]["role"])
		assert.Equal(t, "toolu_01", msgs[0]["tool_call_id"])
		assert.Equal(t, "Sunny, 25°C", msgs[0]["content"])
	})

	t.Run("mixed tool_result + text → tool msg first, user msg last", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "toolu_01",
				"content":     "Result: 42",
			},
			map[string]interface{}{"type": "text", "text": "Thanks!"},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 2)
		assert.Equal(t, "tool", msgs[0]["role"])
		assert.Equal(t, "toolu_01", msgs[0]["tool_call_id"])
		assert.Equal(t, "user", msgs[1]["role"])
		assert.Equal(t, "Thanks!", msgs[1]["content"])
	})

	t.Run("tool_result with block content ([]contentBlock)", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "toolu_03",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Block text result"},
				},
			},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "tool", msgs[0]["role"])
		assert.Equal(t, "Block text result", msgs[0]["content"])
	})
}

// TestConvertAnthropicToolsConversion tests input_schema → parameters rename and wrapping.
func TestConvertAnthropicToolsConversion(t *testing.T) {
	t.Run("basic tool: input_schema → parameters", func(t *testing.T) {
		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"city": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"city"},
		}
		tools := []interface{}{
			map[string]interface{}{
				"name":         "get_weather",
				"description":  "Get current weather",
				"input_schema": schema,
			},
		}
		result := convertAnthropicTools(tools)
		require.Len(t, result, 1)
		assert.Equal(t, "function", result[0]["type"])

		funcDef := result[0]["function"].(map[string]interface{})
		assert.Equal(t, "get_weather", funcDef["name"])
		assert.Equal(t, "Get current weather", funcDef["description"])
		_, hasParams := funcDef["parameters"]
		assert.True(t, hasParams, "parameters key must exist")
		_, hasInputSchema := funcDef["input_schema"]
		assert.False(t, hasInputSchema, "input_schema key must be removed")
	})

	t.Run("cache_control is dropped", func(t *testing.T) {
		tools := []interface{}{
			map[string]interface{}{
				"name":          "search",
				"input_schema":  map[string]interface{}{"type": "object"},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		}
		result := convertAnthropicTools(tools)
		require.Len(t, result, 1)
		funcDef := result[0]["function"].(map[string]interface{})
		_, hasCache := funcDef["cache_control"]
		assert.False(t, hasCache, "cache_control must be stripped")
	})

	t.Run("multiple tools preserve order", func(t *testing.T) {
		tools := []interface{}{
			map[string]interface{}{"name": "tool_a", "input_schema": map[string]interface{}{"type": "object"}},
			map[string]interface{}{"name": "tool_b", "input_schema": map[string]interface{}{"type": "object"}},
		}
		result := convertAnthropicTools(tools)
		require.Len(t, result, 2)
		assert.Equal(t, "tool_a", result[0]["function"].(map[string]interface{})["name"])
		assert.Equal(t, "tool_b", result[1]["function"].(map[string]interface{})["name"])
	})
}

// TestConvertAnthropicToolChoiceConversion tests all tool_choice variants.
func TestConvertAnthropicToolChoiceConversion(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  interface{}
	}{
		{
			name:  "auto",
			input: map[string]interface{}{"type": "auto"},
			want:  "auto",
		},
		{
			name:  "any → required",
			input: map[string]interface{}{"type": "any"},
			want:  "required",
		},
		{
			name:  "none",
			input: map[string]interface{}{"type": "none"},
			want:  "none",
		},
		{
			name:  "specific tool",
			input: map[string]interface{}{"type": "tool", "name": "get_weather"},
			want: map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": "get_weather"},
			},
		},
		{
			name:  "unknown type → nil",
			input: map[string]interface{}{"type": "unknown"},
			want:  nil,
		},
		{
			name:  "non-map input → nil",
			input: "invalid",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertAnthropicToolChoice(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// P0 新增测试：非流式工具调用响应转换
// ---------------------------------------------------------------------------

func TestConvertOpenAIToAnthropicResponseToolCalls(t *testing.T) {
	logger := zap.NewNop()

	t.Run("tool_calls → tool_use content blocks", func(t *testing.T) {
		openaiResp := map[string]interface{}{
			"id":    "chatcmpl-tool123",
			"model": "gpt-4",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []interface{}{
							map[string]interface{}{
								"id":   "call_01",
								"type": "function",
								"function": map[string]interface{}{
									"name":      "get_weather",
									"arguments": `{"city":"Beijing"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     float64(20),
				"completion_tokens": float64(15),
			},
		}
		body, _ := json.Marshal(openaiResp)

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req")
		require.NoError(t, err)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))

		assert.Equal(t, "tool_use", resp["stop_reason"])

		content := resp["content"].([]interface{})
		require.Len(t, content, 1)
		toolBlock := content[0].(map[string]interface{})
		assert.Equal(t, "tool_use", toolBlock["type"])
		assert.Equal(t, "call_01", toolBlock["id"])
		assert.Equal(t, "get_weather", toolBlock["name"])
		// arguments（JSON 字符串）→ input（解析后对象）
		input := toolBlock["input"].(map[string]interface{})
		assert.Equal(t, "Beijing", input["city"])
	})

	t.Run("text + tool_calls → text block + tool_use block", func(t *testing.T) {
		openaiResp := map[string]interface{}{
			"id":    "chatcmpl-combo",
			"model": "gpt-4",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Let me look that up.",
						"tool_calls": []interface{}{
							map[string]interface{}{
								"id":   "call_02",
								"type": "function",
								"function": map[string]interface{}{
									"name":      "search",
									"arguments": `{"query":"weather"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     float64(10),
				"completion_tokens": float64(5),
			},
		}
		body, _ := json.Marshal(openaiResp)

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req")
		require.NoError(t, err)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))

		content := resp["content"].([]interface{})
		require.Len(t, content, 2)
		textBlock := content[0].(map[string]interface{})
		assert.Equal(t, "text", textBlock["type"])
		assert.Equal(t, "Let me look that up.", textBlock["text"])
		toolBlock := content[1].(map[string]interface{})
		assert.Equal(t, "tool_use", toolBlock["type"])
		assert.Equal(t, "call_02", toolBlock["id"])
	})
}

// TestConvertOpenAIToAnthropicResponseCachedTokens tests prompt_tokens_details usage split.
func TestConvertOpenAIToAnthropicResponseCachedTokens(t *testing.T) {
	logger := zap.NewNop()

	openaiResp := map[string]interface{}{
		"id":    "chatcmpl-cache",
		"model": "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Response",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(100),
			"completion_tokens": float64(20),
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": float64(80),
			},
		},
	}
	body, _ := json.Marshal(openaiResp)

	converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req")
	require.NoError(t, err)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(converted, &resp))

	usage := resp["usage"].(map[string]interface{})
	// input_tokens = prompt_tokens - cached_tokens = 100 - 80 = 20
	assert.Equal(t, float64(20), usage["input_tokens"])
	assert.Equal(t, float64(20), usage["output_tokens"])
	// cache_read_input_tokens = cached_tokens = 80
	assert.Equal(t, float64(80), usage["cache_read_input_tokens"])
	assert.Equal(t, float64(0), usage["cache_creation_input_tokens"])
}

// ---------------------------------------------------------------------------
// P0 新增测试：流式缓冲策略 — input_tokens 准确性
// ---------------------------------------------------------------------------

func TestOpenAIToAnthropicStreamConverterTokenAccuracy(t *testing.T) {
	logger := zap.NewNop()

	t.Run("input_tokens = prompt_tokens - cached_tokens", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-aaa0")

		chunks := []string{
			`data: {"id":"chatcmpl-tok1","choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-tok1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":80}}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()
		// input_tokens = 100 - 80 = 20
		assert.Contains(t, output, `"input_tokens":20`)
		assert.Contains(t, output, `"cache_read_input_tokens":80`)
		assert.Contains(t, output, `"output_tokens":10`)
	})

	t.Run("no cached tokens: input_tokens = prompt_tokens", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-bbb0")

		chunks := []string{
			`data: {"id":"chatcmpl-tok2","choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-tok2","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":8}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()
		assert.Contains(t, output, `"input_tokens":50`)
		assert.Contains(t, output, `"output_tokens":8`)
	})

	t.Run("usage emitted AFTER content → message_start still has accurate tokens", func(t *testing.T) {
		// OpenAI sometimes emits a separate usage-only chunk after [DONE] chunk;
		// in the buffered strategy usage arrives before [DONE], so tokens are always accurate.
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-ccc0")

		// usage is in the finish_reason chunk, then [DONE]
		chunks := []string{
			`data: {"id":"chatcmpl-u1","choices":[{"delta":{"content":"A"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-u1","choices":[{"delta":{"content":"B"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-u1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()
		// message_start appears first in output
		startIdx := strings.Index(output, "event: message_start")
		assert.Greater(t, startIdx, -1)
		// Verify tokens are correct despite usage arriving late
		assert.Contains(t, output, `"input_tokens":30`)
		assert.Contains(t, output, `"output_tokens":5`)
		// Both text chunks should appear as separate deltas
		assert.Contains(t, output, `"text":"A"`)
		assert.Contains(t, output, `"text":"B"`)
	})
}

// ---------------------------------------------------------------------------
// P0 新增测试：流式工具调用转换
// ---------------------------------------------------------------------------

func TestOpenAIToAnthropicStreamConverterToolCalls(t *testing.T) {
	logger := zap.NewNop()

	t.Run("single tool call streaming", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-tc10")

		chunks := []string{
			// 首块：tool call id + name
			`data: {"id":"chatcmpl-tc1","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
			// arguments 流式片段
			`data: {"id":"chatcmpl-tc1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-tc1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Beijing\"}"}}]},"finish_reason":null}]}` + "\n\n",
			// finish
			`data: {"id":"chatcmpl-tc1","choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":15}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()
		// tool_use 内容块
		assert.Contains(t, output, `"type":"tool_use"`)
		assert.Contains(t, output, `"id":"call_abc"`)
		assert.Contains(t, output, `"name":"get_weather"`)
		// input_json_delta 事件
		assert.Contains(t, output, `"type":"input_json_delta"`)
		assert.Contains(t, output, `"partial_json"`)
		// stop_reason → tool_use
		assert.Contains(t, output, `"stop_reason":"tool_use"`)
	})

	t.Run("parallel tool calls preserve order", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-par0")

		chunks := []string{
			`data: {"id":"chatcmpl-par","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"func_a","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-par","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"func_b","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-par","choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":20}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk))
			require.NoError(t, err)
		}

		output := w.String()
		assert.Contains(t, output, `"name":"func_a"`)
		assert.Contains(t, output, `"name":"func_b"`)
		// func_a 在 func_b 之前
		assert.Less(t, strings.Index(output, `"name":"func_a"`), strings.Index(output, `"name":"func_b"`))
	})
}

// ---------------------------------------------------------------------------
// P0 新增测试：完整请求转换（含工具定义）
// ---------------------------------------------------------------------------

func TestConvertAnthropicToOpenAIRequestWithTools(t *testing.T) {
	logger := zap.NewNop()

	t.Run("assistant message with tool_use → tool_calls", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "What's the weather?"},
				map[string]interface{}{
					"role": "assistant",
					"content": []interface{}{
						map[string]interface{}{
							"type":  "tool_use",
							"id":    "toolu_01",
							"name":  "get_weather",
							"input": map[string]interface{}{"city": "Beijing"},
						},
					},
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req1")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		messages := openaiReq["messages"].([]interface{})
		require.Len(t, messages, 2)

		assistantMsg := messages[1].(map[string]interface{})
		assert.Equal(t, "assistant", assistantMsg["role"])
		toolCalls := assistantMsg["tool_calls"].([]interface{})
		require.Len(t, toolCalls, 1)
		tc := toolCalls[0].(map[string]interface{})
		assert.Equal(t, "toolu_01", tc["id"])
		funcDef := tc["function"].(map[string]interface{})
		assert.Equal(t, "get_weather", funcDef["name"])
		args := funcDef["arguments"].(string)
		var parsed map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(args), &parsed))
		assert.Equal(t, "Beijing", parsed["city"])
	})

	t.Run("user message with tool_result → role:tool message", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type":        "tool_result",
							"tool_use_id": "toolu_01",
							"content":     "Sunny, 25°C",
						},
					},
				},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req2")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		messages := openaiReq["messages"].([]interface{})
		require.Len(t, messages, 1)
		toolMsg := messages[0].(map[string]interface{})
		assert.Equal(t, "tool", toolMsg["role"])
		assert.Equal(t, "toolu_01", toolMsg["tool_call_id"])
		assert.Equal(t, "Sunny, 25°C", toolMsg["content"])
	})

	t.Run("tools definition: input_schema → parameters", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "Hi"}},
			"tools": []interface{}{
				map[string]interface{}{
					"name":        "get_weather",
					"description": "Weather data",
					"input_schema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"city": map[string]interface{}{"type": "string"}},
						"required":   []interface{}{"city"},
					},
				},
			},
			"tool_choice": map[string]interface{}{"type": "auto"},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req3")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		tools := openaiReq["tools"].([]interface{})
		require.Len(t, tools, 1)
		toolDef := tools[0].(map[string]interface{})
		assert.Equal(t, "function", toolDef["type"])
		funcDef := toolDef["function"].(map[string]interface{})
		assert.Equal(t, "get_weather", funcDef["name"])
		_, hasParams := funcDef["parameters"]
		assert.True(t, hasParams, "parameters must exist")
		_, hasInputSchema := funcDef["input_schema"]
		assert.False(t, hasInputSchema, "input_schema must not appear")

		// tool_choice: auto → "auto"
		assert.Equal(t, "auto", openaiReq["tool_choice"])
	})

	t.Run("tool_choice any → required with parallel_tool_calls:false", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "Hi"}},
			"tools":      []interface{}{map[string]interface{}{"name": "t", "input_schema": map[string]interface{}{"type": "object"}}},
			"tool_choice": map[string]interface{}{
				"type":                    "any",
				"disable_parallel_tool_use": true,
			},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req4")
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		assert.Equal(t, "required", openaiReq["tool_choice"])
		assert.Equal(t, false, openaiReq["parallel_tool_calls"])
	})
}
