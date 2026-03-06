package proxy

import (
	"encoding/json"
	"testing"

	"go.uber.org/zap"
)

func TestInjectOpenAIStreamOptions_NonOpenAIPath(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":true,"messages":[]}`)

	result := injectOpenAIStreamOptions("/v1/messages", body, logger, "test-req-1")

	if string(result) != string(body) {
		t.Errorf("expected original body for non-OpenAI path, got modified")
	}
}

func TestInjectOpenAIStreamOptions_NonStreaming(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-2")

	if string(result) != string(body) {
		t.Errorf("expected original body for non-streaming request, got modified")
	}
}

func TestInjectOpenAIStreamOptions_InjectNew(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-3")

	// 验证注入了 stream_options
	if string(result) == string(body) {
		t.Errorf("expected modified body with stream_options, got original")
	}

	// 验证 JSON 有效且包含 stream_options.include_usage: true
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("modified body is not valid JSON: %v", err)
	}

	streamOpts, ok := parsed["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("stream_options not found or wrong type")
	}

	includeUsage, ok := streamOpts["include_usage"].(bool)
	if !ok || !includeUsage {
		t.Errorf("stream_options.include_usage = %v, want true", includeUsage)
	}
}

func TestInjectOpenAIStreamOptions_IdempotentWhenTrue(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":true,"stream_options":{"include_usage":true},"messages":[]}`)

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-4")

	// 应该返回原 body（幂等）
	if string(result) != string(body) {
		t.Errorf("expected idempotent behavior when include_usage already true")
	}
}

func TestInjectOpenAIStreamOptions_OverrideFalseToTrue(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":true,"stream_options":{"include_usage":false},"messages":[]}`)

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-5")

	// 应该覆盖为 true
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("modified body is not valid JSON: %v", err)
	}

	streamOpts := parsed["stream_options"].(map[string]interface{})
	includeUsage := streamOpts["include_usage"].(bool)
	if !includeUsage {
		t.Errorf("stream_options.include_usage = false, want true (should override)")
	}
}

func TestInjectOpenAIStreamOptions_MalformedJSON(t *testing.T) {
	logger := zap.NewNop()
	body := []byte(`{"stream":true,invalid json}`)

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-6")

	// 应该降级返回原 body
	if string(result) != string(body) {
		t.Errorf("expected original body for malformed JSON, got modified")
	}
}

func TestInjectOpenAIStreamOptions_EmptyBody(t *testing.T) {
	logger := zap.NewNop()
	body := []byte{}

	result := injectOpenAIStreamOptions("/v1/chat/completions", body, logger, "test-req-7")

	if len(result) != 0 {
		t.Errorf("expected empty body returned as-is, got %d bytes", len(result))
	}
}
