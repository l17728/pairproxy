package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestDetectConversionDirection(t *testing.T) {
	tests := []struct {
		name           string
		requestPath    string
		targetProvider string
		want           conversionDirection
	}{
		{
			name:           "anthropic path + ollama target → AtoO",
			requestPath:    "/v1/messages",
			targetProvider: "ollama",
			want:           conversionAtoO,
		},
		{
			name:           "anthropic path + openai target → AtoO",
			requestPath:    "/v1/messages",
			targetProvider: "openai",
			want:           conversionAtoO,
		},
		{
			name:           "anthropic path + anthropic target → None",
			requestPath:    "/v1/messages",
			targetProvider: "anthropic",
			want:           conversionNone,
		},
		{
			name:           "openai path + anthropic target → OtoA",
			requestPath:    "/v1/chat/completions",
			targetProvider: "anthropic",
			want:           conversionOtoA,
		},
		{
			name:           "openai path + openai target → None",
			requestPath:    "/v1/chat/completions",
			targetProvider: "openai",
			want:           conversionNone,
		},
		{
			name:           "empty provider → None",
			requestPath:    "/v1/messages",
			targetProvider: "",
			want:           conversionNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectConversionDirection(tt.requestPath, tt.targetProvider)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertOpenAIToAnthropicRequest(t *testing.T) {
	logger := zap.NewNop()

	t.Run("basic text message with system", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [
                {"role": "system", "content": "You are helpful."},
                {"role": "user", "content": "Hello"}
            ],
            "max_tokens": 100,
            "temperature": 0.7
        }`
		out, path, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req1", nil)
		require.NoError(t, err)
		assert.Equal(t, "/v1/messages", path)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "gpt-4o", got["model"])
		assert.Equal(t, "You are helpful.", got["system"])
		msgs := got["messages"].([]interface{})
		assert.Len(t, msgs, 1)
		m0 := msgs[0].(map[string]interface{})
		assert.Equal(t, "user", m0["role"])
		assert.Equal(t, "Hello", m0["content"])
		assert.Equal(t, float64(100), got["max_tokens"])
		assert.Equal(t, float64(0.7), got["temperature"])
	})

	t.Run("multiple system messages joined", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [
                {"role": "system", "content": "Part 1."},
                {"role": "system", "content": "Part 2."},
                {"role": "user", "content": "Hi"}
            ]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req2", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "Part 1.\n\nPart 2.", got["system"])
		msgs := got["messages"].([]interface{})
		assert.Len(t, msgs, 1) // system removed from messages array
	})

	t.Run("tool role messages merged into user message", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [
                {"role": "user", "content": "Call the tool"},
                {"role": "assistant", "content": null, "tool_calls": [
                    {"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}}
                ]},
                {"role": "tool", "tool_call_id": "call_1", "content": "Sunny, 25°C"},
                {"role": "tool", "tool_call_id": "call_2", "content": "Done"}
            ]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req3", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		// Expect: user, assistant (with tool_use), user (merged tool_results)
		assert.Len(t, msgs, 3)
		last := msgs[2].(map[string]interface{})
		assert.Equal(t, "user", last["role"])
		content := last["content"].([]interface{})
		assert.Len(t, content, 2) // two tool_result blocks
		tr0 := content[0].(map[string]interface{})
		assert.Equal(t, "tool_result", tr0["type"])
		assert.Equal(t, "call_1", tr0["tool_use_id"])
		assert.Equal(t, "Sunny, 25°C", tr0["content"])
		tr1 := content[1].(map[string]interface{})
		assert.Equal(t, "tool_result", tr1["type"])
		assert.Equal(t, "call_2", tr1["tool_use_id"])
		assert.Equal(t, "Done", tr1["content"])
	})

	t.Run("tool role message with array content joined", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [
                {"role": "user", "content": "Go"},
                {"role": "assistant", "tool_calls": [
                    {"id": "c1", "type": "function", "function": {"name": "fn", "arguments": "{}"}}
                ]},
                {"role": "tool", "tool_call_id": "c1", "content": [
                    {"type": "text", "text": "Line 1"},
                    {"type": "text", "text": "Line 2"}
                ]}
            ]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req4", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		last := msgs[len(msgs)-1].(map[string]interface{})
		content := last["content"].([]interface{})
		tr := content[0].(map[string]interface{})
		assert.Equal(t, "Line 1\nLine 2", tr["content"])
	})

	t.Run("assistant message with tool_calls converted to tool_use", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [
                {"role": "user", "content": "Use tool"},
                {"role": "assistant", "content": "Calling tool", "tool_calls": [
                    {"id": "c1", "type": "function", "function": {"name": "search", "arguments": "{\"q\":\"go\"}"}}
                ]}
            ]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req5", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		asst := msgs[1].(map[string]interface{})
		assert.Equal(t, "assistant", asst["role"])
		blocks := asst["content"].([]interface{})
		// text block + tool_use block
		assert.Len(t, blocks, 2)
		text := blocks[0].(map[string]interface{})
		assert.Equal(t, "text", text["type"])
		assert.Equal(t, "Calling tool", text["text"])
		tu := blocks[1].(map[string]interface{})
		assert.Equal(t, "tool_use", tu["type"])
		assert.Equal(t, "c1", tu["id"])
		assert.Equal(t, "search", tu["name"])
		inp := tu["input"].(map[string]interface{})
		assert.Equal(t, "go", inp["q"])
	})

	t.Run("user message with image_url data URI", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [{"role": "user", "content": [
                {"type": "text", "text": "Describe this"},
                {"type": "image_url", "image_url": {"url": "data:image/png;base64,abc123"}}
            ]}]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req6", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		content := msgs[0].(map[string]interface{})["content"].([]interface{})
		img := content[1].(map[string]interface{})
		assert.Equal(t, "image", img["type"])
		src := img["source"].(map[string]interface{})
		assert.Equal(t, "base64", src["type"])
		assert.Equal(t, "image/png", src["media_type"])
		assert.Equal(t, "abc123", src["data"])
	})

	t.Run("user message with image_url https URL", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [{"role": "user", "content": [
                {"type": "image_url", "image_url": {"url": "https://example.com/img.png"}}
            ]}]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req7", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		content := msgs[0].(map[string]interface{})["content"].([]interface{})
		img := content[0].(map[string]interface{})
		src := img["source"].(map[string]interface{})
		assert.Equal(t, "url", src["type"])
		assert.Equal(t, "https://example.com/img.png", src["url"])
	})

	t.Run("tools array conversion — unwrap function wrapper", func(t *testing.T) {
		input := `{
            "model": "gpt-4o",
            "messages": [{"role": "user", "content": "Hi"}],
            "tools": [{"type": "function", "function": {
                "name": "get_weather",
                "description": "Get weather",
                "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
            }}]
        }`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req8", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		tools := got["tools"].([]interface{})
		assert.Len(t, tools, 1)
		tool := tools[0].(map[string]interface{})
		assert.Equal(t, "get_weather", tool["name"])
		assert.Equal(t, "Get weather", tool["description"])
		schema := tool["input_schema"].(map[string]interface{})
		assert.Equal(t, "object", schema["type"])
		// No "type":"function" wrapper in Anthropic format
		assert.Nil(t, tool["type"])
	})

	t.Run("tool_choice mapping — all variants", func(t *testing.T) {
		cases := []struct {
			openai   string
			wantType string
		}{
			{`"auto"`, "auto"},
			{`"none"`, "none"},
			{`"required"`, "any"},
		}
		for _, c := range cases {
			input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":` + c.openai + `}`
			out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req9", nil)
			require.NoError(t, err)
			var got map[string]interface{}
			require.NoError(t, json.Unmarshal(out, &got))
			tc := got["tool_choice"].(map[string]interface{})
			assert.Equal(t, c.wantType, tc["type"], "tool_choice=%s", c.openai)
		}
		// object form: {"type":"function","function":{"name":"X"}}
		input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"search"}}}`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req9b", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		tc := got["tool_choice"].(map[string]interface{})
		assert.Equal(t, "tool", tc["type"])
		assert.Equal(t, "search", tc["name"])
	})

	t.Run("stop string normalized to array", func(t *testing.T) {
		input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":"STOP"}`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req10", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		seqs := got["stop_sequences"].([]interface{})
		assert.Equal(t, []interface{}{"STOP"}, seqs)
		// stop array case
		input2 := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":["END","STOP"]}`
		out2, _, err := convertOpenAIToAnthropicRequest([]byte(input2), logger, "req10b", nil)
		require.NoError(t, err)
		var got2 map[string]interface{}
		require.NoError(t, json.Unmarshal(out2, &got2))
		seqs2 := got2["stop_sequences"].([]interface{})
		assert.Len(t, seqs2, 2)
	})

	t.Run("stream_options discarded", func(t *testing.T) {
		input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req11", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Nil(t, got["stream_options"], "stream_options must be discarded")
		assert.Equal(t, true, got["stream"]) // stream itself kept
	})

	t.Run("model mapping applied", func(t *testing.T) {
		input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
		mapping := map[string]string{"gpt-4o": "claude-opus-4-6"}
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req12", mapping)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "claude-opus-4-6", got["model"])
	})

	t.Run("tool message without preceding assistant is silently dropped", func(t *testing.T) {
		// Per spec: tool messages are only merged when consecutive after an assistant.
		// An orphaned tool message (e.g. at start, or after user) is dropped — document this behavior.
		input := `{"model":"gpt-4o","messages":[
            {"role": "tool", "tool_call_id": "c1", "content": "orphan"},
            {"role": "user", "content": "hi"}
        ]}`
		out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req_orphan", nil)
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		msgs := got["messages"].([]interface{})
		// Only the user message remains; orphaned tool is dropped
		assert.Len(t, msgs, 1)
		assert.Equal(t, "user", msgs[0].(map[string]interface{})["role"])
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		_, _, err := convertOpenAIToAnthropicRequest([]byte(`{invalid`), logger, "req13", nil)
		assert.Error(t, err)
	})

	t.Run("empty body returned as-is", func(t *testing.T) {
		out, path, err := convertOpenAIToAnthropicRequest(nil, logger, "req14", nil)
		require.NoError(t, err)
		assert.Equal(t, "/v1/messages", path)
		assert.Nil(t, out)
	})
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

		converted, newPath, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id", nil)
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		assert.True(t, openaiReq["stream"].(bool))

		// Verify stream_options was injected
		streamOpts := openaiReq["stream_options"].(map[string]interface{})
		assert.True(t, streamOpts["include_usage"].(bool))
	})

	t.Run("empty body", func(t *testing.T) {
		converted, newPath, err := convertAnthropicToOpenAIRequest([]byte{}, logger, "test-req-id", nil)
		require.NoError(t, err)
		assert.Equal(t, "/v1/chat/completions", newPath)
		assert.Empty(t, converted)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		body := []byte(`{invalid json}`)
		converted, newPath, err := convertAnthropicToOpenAIRequest(body, logger, "test-req-id", nil)
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

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req-id", "")
		require.NoError(t, err)

		var anthropicResp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &anthropicResp))

		assert.Equal(t, "msg_123", anthropicResp["id"])
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
		converted, err := convertOpenAIToAnthropicResponse([]byte{}, logger, "test-req-id", "")
		require.NoError(t, err)
		assert.Empty(t, converted)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		body := []byte(`{invalid}`)
		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req-id", "")
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
		{"content_filter", "end_turn"},
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
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-123", "")

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
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-456", "")

		_, err := converter.Write([]byte("\n\n"))
		require.NoError(t, err)

		// Should not crash, output may be empty
		assert.NotPanics(t, func() {
			converter.Write([]byte(""))
		})
	})

	t.Run("malformed JSON chunk", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-789", "")

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

	openaiBody, newPath, err := convertAnthropicToOpenAIRequest(anthropicBody, logger, "test-req", nil)
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
	anthropicRespBody, err := convertOpenAIToAnthropicResponse(openaiRespBody, logger, "test-req", "")
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

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req", "")
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

		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req", "")
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

	converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-req", "")
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
// P0 新增测试：流式渐进发射 — output_tokens 准确性 + 内容即时发射
// ---------------------------------------------------------------------------

func TestOpenAIToAnthropicStreamConverterTokenAccuracy(t *testing.T) {
	logger := zap.NewNop()

	// 注：渐进模式下 message_start.message.usage.input_tokens 为 0（占位）。
	// 准确 input/output tokens 由 TeeResponseWriter 直接解析 OpenAI SSE 获取，不受此影响。

	t.Run("output_tokens accurate in message_delta", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-aaa0", "")

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
		// message_start に input_tokens:0 を送る（プログレッシブモードの仕様）
		assert.Contains(t, output, `"input_tokens":0`)
		// output_tokens は message_delta に正確な値で含まれる
		assert.Contains(t, output, `"output_tokens":10`)
		// message_delta には正確な input_tokens（= prompt_tokens - cached_tokens = 100 - 80 = 20）
		assert.Contains(t, output, `"input_tokens":20`)
		// cache_read_input_tokens と cache_creation_input_tokens も message_delta に含まれる
		assert.Contains(t, output, `"cache_read_input_tokens":80`)
		assert.Contains(t, output, `"cache_creation_input_tokens":0`)
		// content が即時に送信されること
		assert.Contains(t, output, `"text":"Hi"`)
	})

	t.Run("output_tokens accurate when no cached tokens", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-bbb0", "")

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
		assert.Contains(t, output, `"output_tokens":8`)
		assert.Contains(t, output, `"text":"Hello"`)
	})

	t.Run("content deltas emitted progressively before [DONE]", func(t *testing.T) {
		// 渐進モード: usage が後から来ても、content は即座に発射される。
		// message_start は最初の content delta 到着時に発射（input_tokens=0 のプレースホルダー付き）。
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-ccc0", "")

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
		// output_tokens accurate in message_delta
		assert.Contains(t, output, `"output_tokens":5`)
		// Both text chunks appear as separate deltas (progressive emission)
		assert.Contains(t, output, `"text":"A"`)
		assert.Contains(t, output, `"text":"B"`)
		// Content deltas come before message_stop
		contentIdx := strings.Index(output, `"text":"A"`)
		stopIdx := strings.Index(output, "event: message_stop")
		assert.Less(t, contentIdx, stopIdx, "content should appear before message_stop")
	})
}

// ---------------------------------------------------------------------------
// P0 新增测试：流式工具调用转换
// ---------------------------------------------------------------------------

func TestOpenAIToAnthropicStreamConverterToolCalls(t *testing.T) {
	logger := zap.NewNop()

	t.Run("single tool call streaming", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-tc10", "")

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
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-par0", "")

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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req1", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req2", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req3", nil)
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

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req4", nil)
		require.NoError(t, err)

		var openaiReq map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &openaiReq))

		assert.Equal(t, "required", openaiReq["tool_choice"])
		assert.Equal(t, false, openaiReq["parallel_tool_calls"])
	})
}

func TestProcessUserContentWithImages(t *testing.T) {
	t.Run("base64 image → OpenAI image_url with data URI", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "text", "text": "Describe this:"},
			map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": "image/jpeg",
					"data":       "abc123",
				},
			},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "user", msgs[0]["role"])

		arr, ok := msgs[0]["content"].([]interface{})
		require.True(t, ok, "content must be []interface{} for multimodal")
		require.Len(t, arr, 2)

		textBlock := arr[0].(map[string]interface{})
		assert.Equal(t, "text", textBlock["type"])
		assert.Equal(t, "Describe this:", textBlock["text"])

		imgBlock := arr[1].(map[string]interface{})
		assert.Equal(t, "image_url", imgBlock["type"])
		imgURL := imgBlock["image_url"].(map[string]interface{})
		assert.Equal(t, "data:image/jpeg;base64,abc123", imgURL["url"])
		assert.Equal(t, "auto", imgURL["detail"])
	})

	t.Run("url image → OpenAI image_url", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type": "url",
					"url":  "https://example.com/photo.png",
				},
			},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		arr := msgs[0]["content"].([]interface{})
		require.Len(t, arr, 1)
		imgBlock := arr[0].(map[string]interface{})
		assert.Equal(t, "image_url", imgBlock["type"])
		imgURL := imgBlock["image_url"].(map[string]interface{})
		assert.Equal(t, "https://example.com/photo.png", imgURL["url"])
	})

	t.Run("image-only → no text block added", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type": "base64", "media_type": "image/png", "data": "XYZ",
				},
			},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		arr := msgs[0]["content"].([]interface{})
		require.Len(t, arr, 1)
		assert.Equal(t, "image_url", arr[0].(map[string]interface{})["type"])
	})

	t.Run("text-only still returns string content (backward compat)", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "text", "text": "Hello"},
		}
		msgs := processUserContent(content)
		require.Len(t, msgs, 1)
		assert.Equal(t, "Hello", msgs[0]["content"]) // string, not array
	})

	t.Run("preserves order: text first then image", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "text", "text": "Look at this:"},
			map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{"type": "url", "url": "https://img.example.com/a.jpg"},
			},
		}
		msgs := processUserContent(content)
		arr := msgs[0]["content"].([]interface{})
		require.Len(t, arr, 2)
		assert.Equal(t, "text", arr[0].(map[string]interface{})["type"])
		assert.Equal(t, "image_url", arr[1].(map[string]interface{})["type"])
	})
}

func TestConvertAnthropicImageBlock(t *testing.T) {
	t.Run("base64 jpeg", func(t *testing.T) {
		block := map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type": "base64", "media_type": "image/jpeg", "data": "AABB",
			},
		}
		result := convertAnthropicImageBlock(block)
		require.NotNil(t, result)
		assert.Equal(t, "image_url", result["type"])
		iu := result["image_url"].(map[string]interface{})
		assert.Equal(t, "data:image/jpeg;base64,AABB", iu["url"])
		assert.Equal(t, "auto", iu["detail"])
	})

	t.Run("base64 png", func(t *testing.T) {
		block := map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type": "base64", "media_type": "image/png", "data": "CCDD",
			},
		}
		result := convertAnthropicImageBlock(block)
		iu := result["image_url"].(map[string]interface{})
		assert.Equal(t, "data:image/png;base64,CCDD", iu["url"])
	})

	t.Run("url type", func(t *testing.T) {
		block := map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type": "url", "url": "https://example.com/pic.webp",
			},
		}
		result := convertAnthropicImageBlock(block)
		iu := result["image_url"].(map[string]interface{})
		assert.Equal(t, "https://example.com/pic.webp", iu["url"])
	})

	t.Run("nil source → nil", func(t *testing.T) {
		block := map[string]interface{}{"type": "image"}
		result := convertAnthropicImageBlock(block)
		assert.Nil(t, result)
	})

	t.Run("unknown source type → nil", func(t *testing.T) {
		block := map[string]interface{}{
			"type":   "image",
			"source": map[string]interface{}{"type": "s3_presigned"},
		}
		result := convertAnthropicImageBlock(block)
		assert.Nil(t, result)
	})
}

func TestPrefillRejection(t *testing.T) {
	logger := zap.NewNop()

	t.Run("assistant as last message → ErrPrefillNotSupported", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hello"},
				map[string]interface{}{"role": "assistant", "content": "I will"},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-prefill", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrPrefillNotSupported)
	})

	t.Run("normal conversation → no error", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hello"},
				map[string]interface{}{"role": "assistant", "content": "Hi there!"},
				map[string]interface{}{"role": "user", "content": "How are you?"},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-normal", nil)
		require.NoError(t, err)
	})

	t.Run("single user message → no error", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hi"},
			},
		}
		body, _ := json.Marshal(anthropicReq)

		_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-single", nil)
		require.NoError(t, err)
	})
}

func TestWriteAnthropicError(t *testing.T) {
	t.Run("writes correct JSON structure", func(t *testing.T) {
		w := newMockResponseWriter()
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "prefill not supported")

		assert.Equal(t, http.StatusBadRequest, w.statusCode)

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.buf.Bytes(), &body))
		assert.Equal(t, "error", body["type"])
		errObj := body["error"].(map[string]interface{})
		assert.Equal(t, "invalid_request_error", errObj["type"])
		assert.Equal(t, "prefill not supported", errObj["message"])
	})
}

func TestConvertOpenAIErrorResponse(t *testing.T) {
	logger := zap.NewNop()

	t.Run("standard OpenAI error → Anthropic error", func(t *testing.T) {
		openaiErr := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "The model does not exist",
				"type":    "invalid_request_error",
				"param":   nil,
				"code":    "model_not_found",
			},
		}
		body, _ := json.Marshal(openaiErr)

		converted := convertOpenAIErrorResponse(body, logger, "req-err")

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		assert.Equal(t, "error", resp["type"])
		errObj := resp["error"].(map[string]interface{})
		assert.Equal(t, "invalid_request_error", errObj["type"])
		assert.Equal(t, "The model does not exist", errObj["message"])
		// param and code must NOT appear in output
		_, hasParam := errObj["param"]
		_, hasCode := errObj["code"]
		assert.False(t, hasParam)
		assert.False(t, hasCode)
	})

	t.Run("rate_limit_error → rate_limit_error", func(t *testing.T) {
		openaiErr := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Rate limit exceeded",
				"type":    "rate_limit_error",
			},
		}
		body, _ := json.Marshal(openaiErr)
		converted := convertOpenAIErrorResponse(body, logger, "req-rl")
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		errObj := resp["error"].(map[string]interface{})
		assert.Equal(t, "rate_limit_error", errObj["type"])
	})

	t.Run("insufficient_quota → rate_limit_error", func(t *testing.T) {
		openaiErr := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Quota exceeded",
				"type":    "insufficient_quota",
			},
		}
		body, _ := json.Marshal(openaiErr)
		converted := convertOpenAIErrorResponse(body, logger, "req-quota")
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		errObj := resp["error"].(map[string]interface{})
		assert.Equal(t, "rate_limit_error", errObj["type"])
	})

	t.Run("server_error → api_error", func(t *testing.T) {
		openaiErr := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Internal server error",
				"type":    "server_error",
			},
		}
		body, _ := json.Marshal(openaiErr)
		converted := convertOpenAIErrorResponse(body, logger, "req-srv")
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		errObj := resp["error"].(map[string]interface{})
		assert.Equal(t, "api_error", errObj["type"])
	})

	t.Run("non-error JSON → returned unchanged", func(t *testing.T) {
		body := []byte(`{"result":"ok"}`)
		converted := convertOpenAIErrorResponse(body, logger, "req-ok")
		assert.Equal(t, body, converted)
	})

	t.Run("invalid JSON → returned unchanged", func(t *testing.T) {
		body := []byte(`not json`)
		converted := convertOpenAIErrorResponse(body, logger, "req-bad")
		assert.Equal(t, body, converted)
	})

	t.Run("empty error message → returned unchanged", func(t *testing.T) {
		body := []byte(`{"error":{}}`)
		converted := convertOpenAIErrorResponse(body, logger, "req-empty")
		assert.Equal(t, body, converted)
	})
}

func TestThinkingRejection(t *testing.T) {
	logger := zap.NewNop()

	t.Run("thinking field present → stripped silently, no error", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"thinking":   map[string]interface{}{"type": "enabled", "budget_tokens": 5000},
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Think deeply"},
			},
		}
		body, _ := json.Marshal(anthropicReq)
		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-thinking", nil)
		require.NoError(t, err)
		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &result))
		assert.NotContains(t, result, "thinking", "thinking parameter should be stripped from converted request")
	})

	t.Run("thinking field nil → no error", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"thinking":   nil,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hello"},
			},
		}
		body, _ := json.Marshal(anthropicReq)
		_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-thinking-nil", nil)
		require.NoError(t, err)
	})

	t.Run("no thinking field → no error", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hello"},
			},
		}
		body, _ := json.Marshal(anthropicReq)
		_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-no-thinking", nil)
		require.NoError(t, err)
	})
}

func TestConvertMessageID(t *testing.T) {
	t.Run("chatcmpl prefix → msg prefix", func(t *testing.T) {
		assert.Equal(t, "msg_abc123", convertMessageID("chatcmpl-abc123"))
	})
	t.Run("other format → msg prefix prepended", func(t *testing.T) {
		assert.Equal(t, "msg_xyz789", convertMessageID("xyz789"))
	})
	t.Run("empty string → msg_", func(t *testing.T) {
		assert.Equal(t, "msg_", convertMessageID(""))
	})
	t.Run("nil → msg_", func(t *testing.T) {
		assert.Equal(t, "msg_", convertMessageID(nil))
	})
	t.Run("already msg_ prefix → msg_msg_...", func(t *testing.T) {
		// 若上游已返回 msg_ 前缀（罕见），不做特殊处理，保持幂等规则一致
		assert.Equal(t, "msg_msg_abc", convertMessageID("msg_abc"))
	})
}

func TestMapModelName(t *testing.T) {
	t.Run("nil mapping → original model", func(t *testing.T) {
		assert.Equal(t, "claude-opus-4-6", mapModelName("claude-opus-4-6", nil))
	})
	t.Run("empty mapping → original model", func(t *testing.T) {
		assert.Equal(t, "claude-opus-4-6", mapModelName("claude-opus-4-6", map[string]string{}))
	})
	t.Run("exact match → mapped name", func(t *testing.T) {
		mapping := map[string]string{"claude-opus-4-6": "llama3.2"}
		assert.Equal(t, "llama3.2", mapModelName("claude-opus-4-6", mapping))
	})
	t.Run("no match + wildcard → wildcard value", func(t *testing.T) {
		mapping := map[string]string{"*": "llama3.2"}
		assert.Equal(t, "llama3.2", mapModelName("claude-haiku-4-5", mapping))
	})
	t.Run("exact match preferred over wildcard", func(t *testing.T) {
		mapping := map[string]string{
			"claude-haiku-4-5": "gemma2",
			"*":                "llama3.2",
		}
		assert.Equal(t, "gemma2", mapModelName("claude-haiku-4-5", mapping))
		assert.Equal(t, "llama3.2", mapModelName("claude-opus-4-6", mapping))
	})
	t.Run("no match + no wildcard → original model", func(t *testing.T) {
		mapping := map[string]string{"claude-haiku-4-5": "gemma2"}
		assert.Equal(t, "claude-opus-4-6", mapModelName("claude-opus-4-6", mapping))
	})
}

func TestConvertAnthropicToOpenAIRequest_ModelMapping(t *testing.T) {
	logger := zap.NewNop()

	t.Run("model name mapped via exact match", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-opus-4-6",
			"max_tokens": 100,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		}
		body, _ := json.Marshal(anthropicReq)
		mapping := map[string]string{"claude-opus-4-6": "llama3.2:8b"}

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-map-1", mapping)
		require.NoError(t, err)

		var req map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &req))
		assert.Equal(t, "llama3.2:8b", req["model"])
	})

	t.Run("model name mapped via wildcard", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-haiku-4-5",
			"max_tokens": 100,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		}
		body, _ := json.Marshal(anthropicReq)
		mapping := map[string]string{"*": "mistral"}

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-map-2", mapping)
		require.NoError(t, err)

		var req map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &req))
		assert.Equal(t, "mistral", req["model"])
	})

	t.Run("nil mapping → original model unchanged", func(t *testing.T) {
		anthropicReq := map[string]interface{}{
			"model":      "claude-opus-4-6",
			"max_tokens": 100,
			"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		}
		body, _ := json.Marshal(anthropicReq)

		converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "test-map-3", nil)
		require.NoError(t, err)

		var req map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &req))
		assert.Equal(t, "claude-opus-4-6", req["model"])
	})
}

func TestConvertOpenAIToAnthropicResponse_RequestedModel(t *testing.T) {
	logger := zap.NewNop()

	openaiResp := map[string]interface{}{
		"id":    "chatcmpl-xyz",
		"model": "llama3.2:8b", // Ollama 返回的本地模型名
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello!",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
		},
	}
	body, _ := json.Marshal(openaiResp)

	t.Run("requestedModel overrides OpenAI model field", func(t *testing.T) {
		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-rm-1", "claude-opus-4-6")
		require.NoError(t, err)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		// 应返回原始 Anthropic 模型名，而非 Ollama 本地名
		assert.Equal(t, "claude-opus-4-6", resp["model"])
	})

	t.Run("empty requestedModel → use OpenAI model field", func(t *testing.T) {
		converted, err := convertOpenAIToAnthropicResponse(body, logger, "test-rm-2", "")
		require.NoError(t, err)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(converted, &resp))
		// 没有 requestedModel 时，使用 OpenAI 响应中的 model 字段
		assert.Equal(t, "llama3.2:8b", resp["model"])
	})
}

func TestOpenAIToAnthropicStreamConverter_Model(t *testing.T) {
	logger := zap.NewNop()

	t.Run("model field injected in message_start when provided", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-model-stream1", "claude-opus-4-6")

		chunks := []string{
			`data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk + "\n"))
			require.NoError(t, err)
		}

		output := w.String()
		// message_start 事件应包含 model 字段
		assert.Contains(t, output, `"model":"claude-opus-4-6"`)
	})

	t.Run("no model field in message_start when empty", func(t *testing.T) {
		w := newMockResponseWriter()
		converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-model-stream2", "")

		chunks := []string{
			`data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			_, err := converter.Write([]byte(chunk + "\n"))
			require.NoError(t, err)
		}

		output := w.String()
		// 没有 model 参数时，message_start 中不应有 model 字段（避免空字符串污染）
		assert.NotContains(t, output, `"model":""`)
	})
}

func TestConvertAnthropicToOpenAIRequest_Temperature(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"model":"claude-3-opus","max_tokens":1024,"temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`)

	result, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-temp-1", nil)
	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &req))

	temp, ok := req["temperature"]
	if !ok {
		t.Fatal("temperature field missing in converted OpenAI request")
	}
	// JSON numbers unmarshal as float64
	if temp.(float64) != 0.7 {
		t.Errorf("temperature = %v, want 0.7", temp)
	}
}

func TestConvertAnthropicToOpenAIRequest_TopP(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"model":"claude-3-opus","max_tokens":1024,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`)

	result, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-topp-1", nil)
	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &req))

	topP, ok := req["top_p"]
	if !ok {
		t.Fatal("top_p field missing in converted OpenAI request")
	}
	if topP.(float64) != 0.9 {
		t.Errorf("top_p = %v, want 0.9", topP)
	}
}

func TestConvertAnthropicToOpenAIRequest_StopSequences(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"model":"claude-3-opus","max_tokens":1024,"stop_sequences":["STOP","END"],"messages":[{"role":"user","content":"hi"}]}`)

	result, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-stop-1", nil)
	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &req))

	stop, ok := req["stop"]
	if !ok {
		t.Fatal("stop field missing in converted OpenAI request (expected from stop_sequences)")
	}
	stopSlice, ok := stop.([]interface{})
	if !ok {
		t.Fatalf("stop field type = %T, want []interface{}", stop)
	}
	if len(stopSlice) != 2 {
		t.Fatalf("stop sequences count = %d, want 2", len(stopSlice))
	}
	if stopSlice[0].(string) != "STOP" {
		t.Errorf("stop[0] = %q, want STOP", stopSlice[0])
	}
	if stopSlice[1].(string) != "END" {
		t.Errorf("stop[1] = %q, want END", stopSlice[1])
	}
}

// ---------------------------------------------------------------------------
// Tests derived from actual production traffic captured in debug.txt.
//
// debug.txt shows two concurrent Claude Code (claude-cli/2.0.76) requests
// routed through sproxy to a GLM-5.0 Anthropic-compatible backend.
//
// Although GLM-5.0 itself uses the Anthropic API format (no conversion
// needed), the request bodies represent the exact shape Claude Code sends:
//   - "system" as an array of text blocks (sometimes with cache_control)
//   - Multi-turn messages with content-block arrays (not plain strings)
//   - A trailing assistant message acting as a prefill injection
//   - "tools": [] — empty tools array
//
// These test cases verify that the protocol converter handles all of these
// patterns correctly when routing to an OpenAI/Ollama target.
// ---------------------------------------------------------------------------

// TestConvertDebugTxt_SystemArrayWithCacheControl mirrors the actual Claude
// Code system-prompt format: system is an array of text blocks, some carrying
// cache_control.  The OpenAI request must:
//   1. Fold all blocks into a single system message joined with "\n"
//   2. Silently drop cache_control (OpenAI has no caching concept)
func TestConvertDebugTxt_SystemArrayWithCacheControl(t *testing.T) {
	logger := zap.NewNop()

	// Mirrors the real Claude Code system field structure seen in debug.txt.
	// Request 2 (89 kB) has cache_control on the first block; request 1 does
	// not — we test both variants here.
	body := []byte(`{
		"model": "GLM-5.0",
		"max_tokens": 32000,
		"stream": true,
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude.", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "Analyze if this message indicates a new conversation topic."}
		],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-debug-system", nil)
	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(converted, &req))

	messages := req["messages"].([]interface{})
	require.GreaterOrEqual(t, len(messages), 2, "expected system msg + user msg")

	sysMsg := messages[0].(map[string]interface{})
	assert.Equal(t, "system", sysMsg["role"])
	// Both system blocks must be present, joined by "\n"; cache_control dropped
	wantSystem := "You are Claude Code, Anthropic's official CLI for Claude.\nAnalyze if this message indicates a new conversation topic."
	assert.Equal(t, wantSystem, sysMsg["content"])
	// cache_control must not bleed into the converted message
	_, hasCacheControl := sysMsg["cache_control"]
	assert.False(t, hasCacheControl, "cache_control must be stripped from system message")
}

// TestConvertDebugTxt_EmptyToolsArray verifies that when the Anthropic request
// carries "tools": [] (as Claude Code always sends), the converted OpenAI
// request does NOT include a "tools" key (OpenAI rejects empty tools arrays
// on some backends).
func TestConvertDebugTxt_EmptyToolsArray(t *testing.T) {
	logger := zap.NewNop()

	// Exact shape from debug.txt request 1 body (tools key present but empty).
	body := []byte(`{
		"model": "GLM-5.0",
		"max_tokens": 32000,
		"stream": true,
		"tools": [],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	converted, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-debug-empty-tools", nil)
	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(converted, &req))

	_, hasTools := req["tools"]
	assert.False(t, hasTools, "empty Anthropic tools array must not produce a tools key in OpenAI request")
}

// TestConvertDebugTxt_PrefillAsContentBlocks mirrors debug.txt request 1
// where the trailing assistant message uses content-block array format:
//
//	{"role":"assistant","content":[{"type":"text","text":"{"}]}
//
// This is a prefill (assistant opening bracket injection), which must be
// rejected with ErrPrefillNotSupported for OpenAI/Ollama targets.
func TestConvertDebugTxt_PrefillAsContentBlocks(t *testing.T) {
	logger := zap.NewNop()

	// Exact two-message pattern from debug.txt request 1.
	body := []byte(`{
		"model": "GLM-5.0",
		"max_tokens": 32000,
		"stream": true,
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "Analyze if this message indicates a new conversation topic."}
		],
		"messages": [
			{"role": "user",      "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "{"}]}
		],
		"tools": [],
		"metadata": {"user_id": "user_abc123"},
		"max_tokens": 32000
	}`)

	_, _, err := convertAnthropicToOpenAIRequest(body, logger, "req-debug-prefill", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPrefillNotSupported,
		"trailing assistant content-block message must be rejected as prefill")
}

// TestConvertDebugTxt_MultiTurnNoLastAssistant verifies that a normal
// multi-turn conversation (the pattern in debug.txt request 2) ending with a
// user message is NOT rejected and produces correct messages.
func TestConvertDebugTxt_MultiTurnNoLastAssistant(t *testing.T) {
	logger := zap.NewNop()

	// Simplified version of debug.txt request 2 message structure:
	// user → assistant → user (no trailing assistant = no prefill)
	body := []byte(`{
		"model": "GLM-5.0",
		"max_tokens": 32000,
		"stream": true,
		"system": [{"type": "text", "text": "You are a helpful assistant."}],
		"messages": [
			{"role": "user",      "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Hello! How can I help?"}]},
			{"role": "user",      "content": "hello"}
		]
	}`)

	converted, newPath, err := convertAnthropicToOpenAIRequest(body, logger, "req-debug-multiturn", nil)
	require.NoError(t, err, "multi-turn conversation not ending with assistant must not be rejected")
	assert.Equal(t, "/v1/chat/completions", newPath)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(converted, &req))

	messages := req["messages"].([]interface{})
	// system + user + assistant + user = 4 messages
	require.Len(t, messages, 4)

	last := messages[3].(map[string]interface{})
	assert.Equal(t, "user", last["role"])
	assert.Equal(t, "hello", last["content"])

	// stream_options must be injected (stream:true)
	streamOpts, ok := req["stream_options"].(map[string]interface{})
	require.True(t, ok, "stream_options must be present for streaming request")
	assert.True(t, streamOpts["include_usage"].(bool))
}

// TestMapModelName_EmptyStringModel 验证空字符串 model 名的边界行为：
// 空字符串既不命中精确匹配键，也不命中通配符（"*"），
// 故按 "no match" 规则原样返回空字符串，不 panic。
func TestMapModelName_EmptyStringModel(t *testing.T) {
	mapping := map[string]string{"claude-3-opus": "gpt-4"}
	result := mapModelName("", mapping)
	// 空字符串在 mapping 中找不到，也没有通配符 "*"，原样返回
	assert.Equal(t, "", result, "mapModelName with empty model and no wildcard should return empty string")

	// 含通配符时，空字符串仍被通配符覆盖
	wildcardMapping := map[string]string{"*": "llama3.2"}
	result2 := mapModelName("", wildcardMapping)
	assert.Equal(t, "llama3.2", result2, "empty model with wildcard should use wildcard value")
}

// TestProtocolConversion_EmptyBody 验证空请求/响应 body 不崩溃。
func TestProtocolConversion_EmptyBody(t *testing.T) {
	logger := zap.NewNop()

	converted, path, err := convertAnthropicToOpenAIRequest([]byte{}, logger, "test", nil)
	require.NoError(t, err)
	assert.Equal(t, "/v1/chat/completions", path)
	assert.Empty(t, converted)

	converted, err = convertOpenAIToAnthropicResponse([]byte{}, logger, "test", "")
	require.NoError(t, err)
	assert.Empty(t, converted)
}

// TestProtocolConversion_MalformedJSON 验证格式错误的 JSON 返回错误且不崩溃。
func TestProtocolConversion_MalformedJSON(t *testing.T) {
	logger := zap.NewNop()

	body := []byte(`{invalid json}`)

	converted, path, err := convertAnthropicToOpenAIRequest(body, logger, "test", nil)
	assert.Error(t, err)
	assert.Equal(t, "/v1/chat/completions", path)
	assert.Equal(t, body, converted) // 原样返回

	converted, err = convertOpenAIToAnthropicResponse(body, logger, "test", "")
	assert.Error(t, err)
	assert.Equal(t, body, converted) // 原样返回
}

// TestProtocolConversion_ParallelToolCalls 验证并行工具调用（多 index）的顺序保持。
func TestProtocolConversion_ParallelToolCalls(t *testing.T) {
	logger := zap.NewNop()
	w := newMockResponseWriter()
	converter := NewOpenAIToAnthropicStreamConverter(w, logger, "test-req-par", "")

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
	// func_a (index 0) 必须先于 func_b (index 1) 出现
	assert.Less(t, strings.Index(output, `"name":"func_a"`), strings.Index(output, `"name":"func_b"`))
}

func TestConvertAnthropicToOpenAIResponseReverse(t *testing.T) {
	logger := zap.NewNop()

	t.Run("text response", func(t *testing.T) {
		input := `{
            "id": "msg_abc123",
            "type": "message",
            "role": "assistant",
            "model": "claude-opus-4-6",
            "content": [{"type": "text", "text": "Hello, world!"}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 10, "output_tokens": 5}
        }`
		out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req1", "gpt-4o")
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "chatcmpl-abc123", got["id"])
		assert.Equal(t, "chat.completion", got["object"])
		assert.Equal(t, "gpt-4o", got["model"]) // requestedModel overrides
		choices := got["choices"].([]interface{})
		assert.Len(t, choices, 1)
		ch := choices[0].(map[string]interface{})
		assert.Equal(t, "stop", ch["finish_reason"])
		msg := ch["message"].(map[string]interface{})
		assert.Equal(t, "assistant", msg["role"])
		assert.Equal(t, "Hello, world!", msg["content"])
		usage := got["usage"].(map[string]interface{})
		assert.Equal(t, float64(10), usage["prompt_tokens"])
		assert.Equal(t, float64(5), usage["completion_tokens"])
		assert.Equal(t, float64(15), usage["total_tokens"])
	})

	t.Run("tool use response", func(t *testing.T) {
		input := `{
            "id": "msg_tool1",
            "type": "message",
            "role": "assistant",
            "model": "claude-opus-4-6",
            "content": [
                {"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"q": "go lang"}}
            ],
            "stop_reason": "tool_use",
            "usage": {"input_tokens": 20, "output_tokens": 10}
        }`
		out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req2", "")
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		choices := got["choices"].([]interface{})
		ch := choices[0].(map[string]interface{})
		assert.Equal(t, "tool_calls", ch["finish_reason"])
		msg := ch["message"].(map[string]interface{})
		assert.Nil(t, msg["content"]) // no text content
		toolCalls := msg["tool_calls"].([]interface{})
		assert.Len(t, toolCalls, 1)
		tc := toolCalls[0].(map[string]interface{})
		assert.Equal(t, "toolu_1", tc["id"])
		assert.Equal(t, "function", tc["type"])
		assert.Nil(t, tc["index"], "non-streaming tool_calls must NOT include index field")
		fn := tc["function"].(map[string]interface{})
		assert.Equal(t, "search", fn["name"])
		// arguments is a JSON string
		var args map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(fn["arguments"].(string)), &args))
		assert.Equal(t, "go lang", args["q"])
	})

	t.Run("stop_reason mapping — all variants", func(t *testing.T) {
		cases := []struct{ anthropic, openai string }{
			{"end_turn", "stop"},
			{"max_tokens", "length"},
			{"tool_use", "tool_calls"},
			{"stop_sequence", "stop"},
			{"unknown_reason", "unknown_reason"},
		}
		for _, c := range cases {
			input := fmt.Sprintf(`{"id":"msg_x","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"hi"}],"stop_reason":%q,"usage":{"input_tokens":1,"output_tokens":1}}`, c.anthropic)
			out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "reqX", "")
			require.NoError(t, err, "stop_reason=%s", c.anthropic)
			var got map[string]interface{}
			require.NoError(t, json.Unmarshal(out, &got))
			ch := got["choices"].([]interface{})[0].(map[string]interface{})
			assert.Equal(t, c.openai, ch["finish_reason"], "stop_reason=%s", c.anthropic)
		}
	})

	t.Run("requestedModel propagated", func(t *testing.T) {
		input := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req3", "my-model")
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "my-model", got["model"])
	})

	t.Run("cache_read_input_tokens mapped", func(t *testing.T) {
		input := `{"id":"msg_c","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3}}`
		out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req4", "")
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		usage := got["usage"].(map[string]interface{})
		details := usage["prompt_tokens_details"].(map[string]interface{})
		assert.Equal(t, float64(3), details["cached_tokens"])
	})

	t.Run("cache_read_input_tokens zero — prompt_tokens_details omitted", func(t *testing.T) {
		input := `{"id":"msg_z","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":0}}`
		out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req_zero", "")
		require.NoError(t, err)
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		usage := got["usage"].(map[string]interface{})
		assert.Nil(t, usage["prompt_tokens_details"], "zero cache_read_input_tokens must not emit prompt_tokens_details")
	})

	t.Run("empty body returns error", func(t *testing.T) {
		_, err := convertAnthropicToOpenAIResponseReverse([]byte{}, logger, "req5", "")
		assert.Error(t, err)
	})
}

func TestConvertAnthropicErrorResponseToOpenAI(t *testing.T) {
	logger := zap.NewNop()

	t.Run("valid Anthropic error", func(t *testing.T) {
		input := `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`
		out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req1")
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &got))
		// No top-level "type" key
		assert.Nil(t, got["type"])
		errObj := got["error"].(map[string]interface{})
		assert.Equal(t, "authentication_error", errObj["type"])
		assert.Equal(t, "Invalid API key", errObj["message"])
	})

	t.Run("non-Anthropic format returned unchanged", func(t *testing.T) {
		input := `{"message":"something else"}`
		out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req2")
		assert.Equal(t, input, string(out))
	})

	t.Run("malformed JSON returned unchanged", func(t *testing.T) {
		input := `{invalid`
		out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req3")
		assert.Equal(t, input, string(out))
	})
}

// mockFlusher wraps mockResponseWriter and implements http.Flusher
type mockFlusher struct {
	*mockResponseWriter
	flushCount int
}

func (m *mockFlusher) Flush() {
	m.flushCount++
}

func TestAnthropicToOpenAIStreamConverter(t *testing.T) {
	logger := zap.NewNop()

	feedEvents := func(conv *AnthropicToOpenAIStreamConverter, events []string) {
		for _, ev := range events {
			_, err := conv.Write([]byte(ev))
			require.NoError(t, err)
		}
	}

	t.Run("text streaming", func(t *testing.T) {
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req1", "gpt-4o")

		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abc\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":10}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		feedEvents(conv, events)

		output := mf.String()
		// Should contain data: {...} chunks and [DONE]
		assert.Contains(t, output, "chat.completion.chunk")
		assert.Contains(t, output, "gpt-4o")
		assert.Contains(t, output, "Hello")
		assert.Contains(t, output, " world")
		assert.Contains(t, output, `"finish_reason":"stop"`)
		assert.Contains(t, output, `data: [DONE]`)
		// Check message_start chunk sets role and has finish_reason:null (not omitted, not "stop")
		assert.Contains(t, output, `"role":"assistant"`)
		assert.Contains(t, output, `"finish_reason":null`)
		// Check chatcmpl ID (from msg_abc)
		assert.Contains(t, output, "chatcmpl-abc")
		// Flusher called
		assert.Greater(t, mf.flushCount, 0)
	})

	t.Run("tool use streaming", func(t *testing.T) {
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req2", "gpt-4o")

		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool\",\"model\":\"claude\",\"usage\":{\"input_tokens\":15}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\",\"input\":{}}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"go\\\"}\"  }}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":8}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		feedEvents(conv, events)

		output := mf.String()
		assert.Contains(t, output, "tool_calls")
		assert.Contains(t, output, "toolu_1")
		assert.Contains(t, output, "search")
		assert.Contains(t, output, `"finish_reason":"tool_calls"`)
		assert.Contains(t, output, `data: [DONE]`)
	})

	t.Run("finish_reason mapping", func(t *testing.T) {
		cases := []struct{ anthropic, openai string }{
			{"end_turn", "stop"},
			{"max_tokens", "length"},
			{"tool_use", "tool_calls"},
			{"stop_sequence", "stop"},
		}
		for _, c := range cases {
			mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
			conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req", "m")
			events := []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\",\"usage\":{\"input_tokens\":1}}}\n\n",
				fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\n", c.anthropic),
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			}
			feedEvents(conv, events)
			assert.Contains(t, mf.String(), fmt.Sprintf(`"finish_reason":%q`, c.openai), "anthropic=%s", c.anthropic)
		}
	})

	t.Run("usage in final chunk", func(t *testing.T) {
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req3", "gpt-4o")

		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_u\",\"usage\":{\"input_tokens\":10}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":20}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		feedEvents(conv, events)

		output := mf.String()
		// Final chunk should have usage
		assert.Contains(t, output, `"prompt_tokens":10`)
		assert.Contains(t, output, `"completion_tokens":20`)
		assert.Contains(t, output, `"total_tokens":30`)
	})

	t.Run("final chunk has empty delta", func(t *testing.T) {
		// Spec: message_delta emits final chunk with delta={} (empty object)
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req5", "gpt-4o")
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_d\",\"usage\":{\"input_tokens\":5}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		feedEvents(conv, events)
		output := mf.String()
		// The final data chunk (before [DONE]) should contain "delta":{} (empty object)
		assert.Contains(t, output, `"delta":{}`)
		assert.Contains(t, output, `"finish_reason":"stop"`)
	})

	t.Run("empty response (only message_stop)", func(t *testing.T) {
		// Empty streaming response: message_start then immediately message_stop, no content events.
		// Spec testing section lists "Empty response" as a required case.
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req6", "gpt-4o")
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_e\",\"usage\":{\"input_tokens\":1}}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		feedEvents(conv, events)
		output := mf.String()
		// Should produce initial role chunk (with content:"") and [DONE], no crash
		assert.Contains(t, output, `"role":"assistant"`)
		assert.Contains(t, output, `data: [DONE]`)
		// No message_delta was emitted, so no finish_reason chunk appears
		// (no finish_reason:"stop" or finish_reason:"length" etc.)
		assert.NotContains(t, output, `"finish_reason":"stop"`)
		assert.NotContains(t, output, `"finish_reason":"length"`)
	})

	t.Run("Flush delegates to inner writer", func(t *testing.T) {
		mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
		conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req7", "gpt-4o")
		conv.Flush()
		assert.Equal(t, 1, mf.flushCount)
	})
}
