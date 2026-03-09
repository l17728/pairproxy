package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
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
