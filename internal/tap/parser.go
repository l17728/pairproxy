// Package tap 提供对 LLM 响应流的拦截和解析能力，零缓冲地统计 token 用量。
// 本文件定义 ResponseParser 统一接口及按 provider 创建解析器的工厂函数。
package tap

// ResponseParser 是 LLM 响应解析器的统一接口。
// 支持 streaming（SSE）和非 streaming（JSON）两种响应格式。
// 不同的 LLM provider 需要不同的解析器实现。
type ResponseParser interface {
	// Feed 向解析器输入一段响应字节（streaming 模式下逐 chunk 调用）。
	// 解析器内部维护跨 chunk 行缓冲；在检测到流结束标记时触发 OnCompleteFunc 回调。
	Feed(chunk []byte)

	// ParseNonStreaming 解析完整的非 streaming JSON 响应体，
	// 返回 inputTokens 和 outputTokens。
	// 解析失败时返回 0, 0（不返回 error，保持接口简洁）。
	ParseNonStreaming(body []byte) (inputTokens, outputTokens int)

	// InputTokens 返回已解析的输入 token 数量（streaming 结束后有效）。
	InputTokens() int

	// OutputTokens 返回已解析的输出 token 数量（streaming 结束后有效）。
	OutputTokens() int
}

// NewResponseParser 根据 provider 名称创建对应的 ResponseParser。
//
// 支持的 provider：
//   - "anthropic"（默认）：Anthropic Claude API（/v1/messages）
//   - "openai"：OpenAI Chat Completions API（/v1/chat/completions）
//   - "ollama"：Ollama 本地推理服务（与 OpenAI 格式兼容）
//
// 未知 provider 回退到 Anthropic 解析器，保持向后兼容。
func NewResponseParser(provider string, onDone OnCompleteFunc) ResponseParser {
	switch provider {
	case "openai", "ollama":
		return NewOpenAISSEParser(onDone)
	default:
		// "anthropic" 或空字符串均使用 Anthropic 解析器
		return NewAnthropicSSEParser(onDone)
	}
}
