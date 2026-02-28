package tap

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ---------------------------------------------------------------------------
// OpenAI Chat Completions API SSE 格式（streaming）
//
// 普通内容块（usage 为 null）：
//   data: {"id":"chatcmpl-123","object":"chat.completion.chunk",
//          "choices":[{"delta":{"content":"Hello"},"finish_reason":null}],
//          "usage":null}
//
// 最终 usage 块（stream_options: {include_usage: true}，或 finish_reason 块后附带）：
//   data: {"id":"chatcmpl-123","object":"chat.completion.chunk",
//          "choices":[{"delta":{},"finish_reason":"stop"}],
//          "usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}
//
// 结束标记：
//   data: [DONE]
//
// 非 streaming 响应（application/json）：
//   {"id":"chatcmpl-123","object":"chat.completion",
//    "usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},...}
// ---------------------------------------------------------------------------

// OpenAISSEParser 解析 OpenAI Chat Completions API 的 streaming 响应。
// 从包含非 null usage 字段的任意数据块中提取 token 数，以最后一次为准。
// 在 `data: [DONE]` 时触发完成回调。
//
// 设计原则：
//   - 线程不安全（由调用方保证单线程 Feed）
//   - 不分配大 buffer，跨 chunk 行缓冲最小化
//   - 回调仅触发一次（data: [DONE]）
type OpenAISSEParser struct {
	lineBuffer   []byte
	inputTokens  int
	outputTokens int
	done         bool
	onComplete   OnCompleteFunc
}

// NewOpenAISSEParser 创建 OpenAI SSE 解析器，注册完成回调。
// cb 在解析到 data: [DONE] 时调用一次。
func NewOpenAISSEParser(cb OnCompleteFunc) *OpenAISSEParser {
	return &OpenAISSEParser{onComplete: cb}
}

// Feed 向解析器输入一段字节（可以是任意大小的 chunk，包括跨行的片段）。
// 实现 ResponseParser 接口。
func (p *OpenAISSEParser) Feed(chunk []byte) {
	if p.done {
		return
	}

	// 将上次剩余的行片段与新 chunk 合并处理
	data := chunk
	if len(p.lineBuffer) > 0 {
		combined := make([]byte, len(p.lineBuffer)+len(chunk))
		copy(combined, p.lineBuffer)
		copy(combined[len(p.lineBuffer):], chunk)
		data = combined
		p.lineBuffer = p.lineBuffer[:0]
	}

	for len(data) > 0 {
		nlIdx := bytes.IndexByte(data, '\n')
		if nlIdx < 0 {
			p.lineBuffer = append(p.lineBuffer[:0], data...)
			return
		}
		line := bytes.TrimSuffix(data[:nlIdx], []byte("\r"))
		data = data[nlIdx+1:]
		p.processLine(line)
		if p.done {
			return
		}
	}
}

// InputTokens 返回已解析的输入 token 数量。实现 ResponseParser 接口。
func (p *OpenAISSEParser) InputTokens() int { return p.inputTokens }

// OutputTokens 返回已解析的输出 token 数量。实现 ResponseParser 接口。
func (p *OpenAISSEParser) OutputTokens() int { return p.outputTokens }

// ParseNonStreaming 解析 OpenAI 非 streaming JSON 响应体。实现 ResponseParser 接口。
func (p *OpenAISSEParser) ParseNonStreaming(body []byte) (inputTokens, outputTokens int) {
	var resp struct {
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Usage == nil {
		return 0, 0
	}
	return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
}

// processLine 处理单行 SSE 内容。
func (p *OpenAISSEParser) processLine(line []byte) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimLeft(bytes.TrimPrefix(line, []byte("data:")), " ")

	// [DONE] 标记流结束，触发回调
	if string(payload) == "[DONE]" {
		p.done = true
		if p.onComplete != nil {
			p.onComplete(p.inputTokens, p.outputTokens)
		}
		return
	}

	// 从包含 usage 字段的数据块中提取 token 数（以最后一次为准）
	var chunk openAIChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return
	}
	if chunk.Usage != nil {
		p.inputTokens = chunk.Usage.PromptTokens
		p.outputTokens = chunk.Usage.CompletionTokens
	}
}

// ---------------------------------------------------------------------------
// 内部 JSON 结构体
// ---------------------------------------------------------------------------

// openAIChunk SSE streaming 数据块（仅关心 usage 字段）。
type openAIChunk struct {
	Usage *openAIUsage `json:"usage"`
}

// openAIUsage OpenAI usage 字段（streaming 和非 streaming 共用）。
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ---------------------------------------------------------------------------
// 工具：完整 SSE 序列构建（测试辅助）
// ---------------------------------------------------------------------------

// BuildOpenAISSE 构建一个标准 OpenAI SSE 响应序列（测试用）。
// 最终包含一个携带 usage 数据的 chunk，以及 data: [DONE] 结束标记。
func BuildOpenAISSE(inputTokens, outputTokens int, textChunks []string) string {
	var sb strings.Builder

	// 内容块（usage 为 null）
	for _, chunk := range textChunks {
		jsonChunk, _ := json.Marshal(chunk)
		sb.WriteString(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk",`)
		sb.WriteString(`"choices":[{"delta":{"content":`)
		sb.Write(jsonChunk)
		sb.WriteString(`},"finish_reason":null}],"usage":null}`)
		sb.WriteString("\n\n")
	}

	// 最终 usage 块（finish_reason: "stop" + usage）
	sb.WriteString(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk",`)
	sb.WriteString(`"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":`)
	writeInt(&sb, inputTokens)
	sb.WriteString(`,"completion_tokens":`)
	writeInt(&sb, outputTokens)
	sb.WriteString(`,"total_tokens":`)
	writeInt(&sb, inputTokens+outputTokens)
	sb.WriteString(`}}`)
	sb.WriteString("\n\n")

	// [DONE]
	sb.WriteString("data: [DONE]\n\n")

	return sb.String()
}
