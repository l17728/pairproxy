package tokenizer

import (
	"testing"
)

func TestEstimateInputTokens_AnthropicFormat(t *testing.T) {
	body := []byte(`{
		"model": "glm-4",
		"system": "You are a helpful assistant.",
		"messages": [
			{"role": "user", "content": "Hello, how are you?"},
			{"role": "assistant", "content": "I am fine, thank you!"},
			{"role": "user", "content": "What is the capital of France?"}
		]
	}`)

	n := EstimateInputTokens(body, "/v1/messages")
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
	// "Hello, how are you?" ≈ 6 tokens，整体应在 20-60 token 范围内
	if n < 20 || n > 100 {
		t.Errorf("token count %d out of expected range [20, 100]", n)
	}
}

func TestEstimateInputTokens_OpenAIFormat(t *testing.T) {
	body := []byte(`{
		"model": "glm-4",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "What is 2+2?"}
		]
	}`)

	n := EstimateInputTokens(body, "/v1/chat/completions")
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func TestEstimateInputTokens_ChineseContent(t *testing.T) {
	body := []byte(`{
		"model": "glm-4",
		"messages": [
			{"role": "user", "content": "请帮我写一首关于春天的诗歌，要求优美动人。"}
		]
	}`)

	n := EstimateInputTokens(body, "/v1/chat/completions")
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
	// 约 20 个汉字，每字约 1 token，加 overhead 应在 15-40 范围
	if n < 15 || n > 60 {
		t.Errorf("Chinese token count %d out of expected range [15, 60]", n)
	}
}

func TestEstimateInputTokens_ArrayContent(t *testing.T) {
	body := []byte(`{
		"model": "glm-4",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Describe this image."},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
			]}
		]
	}`)

	n := EstimateInputTokens(body, "/v1/chat/completions")
	if n <= 0 {
		t.Fatalf("expected positive token count for array content, got %d", n)
	}
	// 只计文本部分 "Describe this image." ≈ 4 tokens + overhead
	if n > 30 {
		t.Errorf("should only count text part, got %d tokens", n)
	}
}

func TestEstimateInputTokens_EmptyBody(t *testing.T) {
	n := EstimateInputTokens(nil, "/v1/messages")
	if n != 0 {
		t.Errorf("expected 0 for nil body, got %d", n)
	}
	n = EstimateInputTokens([]byte{}, "/v1/chat/completions")
	if n != 0 {
		t.Errorf("expected 0 for empty body, got %d", n)
	}
}

func TestEstimateInputTokens_InvalidJSON(t *testing.T) {
	n := EstimateInputTokens([]byte(`not json`), "/v1/messages")
	if n != 0 {
		t.Errorf("expected 0 for invalid JSON, got %d", n)
	}
}

func TestCharBasedEstimate_Mixed(t *testing.T) {
	// 中英文混合
	texts := []string{"Hello 世界 world 你好"}
	n := charBasedEstimate(texts)
	if n <= 0 {
		t.Errorf("expected positive estimate for mixed text, got %d", n)
	}
}
