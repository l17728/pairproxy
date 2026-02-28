// Package tap 提供对 LLM 响应流的拦截和解析能力，零缓冲地统计 token 用量。
package tap

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ---------------------------------------------------------------------------
// Anthropic SSE 事件格式（参考官方文档）
//
//   event: message_start
//   data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}
//
//   event: content_block_delta
//   data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}
//
//   event: message_delta
//   data: {"type":"message_delta","usage":{"output_tokens":50}}
//
//   event: message_stop
//   data: {"type":"message_stop"}
// ---------------------------------------------------------------------------

// OnCompleteFunc 是 token 统计完成后的回调函数类型。
type OnCompleteFunc func(inputTokens, outputTokens int)

// AnthropicSSEParser 是一个流式 SSE 解析器，从任意大小的字节块中提取
// Anthropic 流式响应的 token 用量，在 message_stop 事件触发回调。
//
// 设计原则：
//   - 线程不安全（由调用方保证单线程 Feed）
//   - 不分配大 buffer，每次 Feed 只在内部维护行缓冲
//   - 回调仅触发一次（message_stop）
type AnthropicSSEParser struct {
	lineBuffer   []byte // 跨 chunk 的行片段
	inputTokens  int
	outputTokens int
	done         bool // 已触发回调
	onComplete   OnCompleteFunc
}

// NewAnthropicSSEParser 创建解析器，注册完成回调。
// cb 在解析到 message_stop 事件后调用一次。
func NewAnthropicSSEParser(cb OnCompleteFunc) *AnthropicSSEParser {
	return &AnthropicSSEParser{
		onComplete: cb,
	}
}

// Feed 向解析器输入一段字节（可以是任意大小的 chunk，包括跨行的片段）。
// 调用者不应在 Feed 之后修改 p。
func (p *AnthropicSSEParser) Feed(chunk []byte) {
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

	// 逐行扫描（SSE 每行以 \n 结尾，事件间以空行分隔）
	for len(data) > 0 {
		nlIdx := bytes.IndexByte(data, '\n')
		if nlIdx < 0 {
			// 没有找到换行符，将剩余内容存入行缓冲等待下次 Feed
			p.lineBuffer = append(p.lineBuffer[:0], data...)
			return
		}

		line := data[:nlIdx]
		data = data[nlIdx+1:]

		// 去除可能存在的 \r（Windows 行尾）
		line = bytes.TrimSuffix(line, []byte("\r"))
		p.processLine(line)

		if p.done {
			return
		}
	}
}

// InputTokens 返回已解析的输入 token 数量。
func (p *AnthropicSSEParser) InputTokens() int { return p.inputTokens }

// OutputTokens 返回已解析的输出 token 数量。
func (p *AnthropicSSEParser) OutputTokens() int { return p.outputTokens }

// processLine 处理单行 SSE 内容。
func (p *AnthropicSSEParser) processLine(line []byte) {
	// SSE "data:" 行
	if !bytes.HasPrefix(line, []byte("data:")) {
		return // 忽略 event:/id:/comment 行，以及空行
	}

	// 提取 data 内容（去除前缀和可能的空格）
	payload := bytes.TrimPrefix(line, []byte("data:"))
	payload = bytes.TrimLeft(payload, " ")

	// 处理 "[DONE]"（OpenAI 兼容格式，Anthropic 不使用，但防御性处理）
	if string(payload) == "[DONE]" {
		return
	}

	// 只解析我们关心的字段，使用轻量 JSON 解析
	p.parseSSEData(payload)
}

// sseEvent 用于解析 SSE data 中的 type 字段。
type sseEvent struct {
	Type    string      `json:"type"`
	Message *msgPayload `json:"message,omitempty"` // message_start
	Usage   *usageBlock `json:"usage,omitempty"`   // message_delta
}

type msgPayload struct {
	Usage *usageBlock `json:"usage,omitempty"`
}

type usageBlock struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseSSEData 解析 SSE data 字段 JSON，提取 token 信息。
func (p *AnthropicSSEParser) parseSSEData(payload []byte) {
	var event sseEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		// JSON 解析失败（可能是非完整事件），静默忽略
		return
	}

	switch event.Type {
	case "message_start":
		// {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}
		if event.Message != nil && event.Message.Usage != nil {
			p.inputTokens = event.Message.Usage.InputTokens
		}

	case "message_delta":
		// {"type":"message_delta","usage":{"output_tokens":50}}
		if event.Usage != nil {
			p.outputTokens = event.Usage.OutputTokens
		}

	case "message_stop":
		// 流结束，触发回调
		p.done = true
		if p.onComplete != nil {
			p.onComplete(p.inputTokens, p.outputTokens)
		}
	}
}

// ---------------------------------------------------------------------------
// 非 Streaming 响应解析
// ---------------------------------------------------------------------------

// nonStreamingResponse 用于解析 Anthropic 非 streaming 响应的 usage 字段。
type nonStreamingResponse struct {
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ParseNonStreaming 解析 Anthropic 普通（非 streaming）JSON 响应体，
// 返回 inputTokens 和 outputTokens。
// body 应为完整的响应 JSON，如：
//
//	{"id":"msg_1","type":"message","usage":{"input_tokens":100,"output_tokens":50},...}
func ParseNonStreaming(body []byte) (inputTokens, outputTokens int, err error) {
	var resp nonStreamingResponse
	if jsonErr := json.Unmarshal(body, &resp); jsonErr != nil {
		return 0, 0, jsonErr
	}
	return resp.Usage.InputTokens, resp.Usage.OutputTokens, nil
}

// ParseNonStreaming 实现 ResponseParser 接口：解析 Anthropic 非 streaming JSON 响应。
// 解析失败时返回 0, 0。
func (p *AnthropicSSEParser) ParseNonStreaming(body []byte) (inputTokens, outputTokens int) {
	in, out, _ := ParseNonStreaming(body)
	return in, out
}

// ---------------------------------------------------------------------------
// 工具：完整 SSE 序列构建（测试辅助）
// ---------------------------------------------------------------------------

// BuildAnthropicSSE 构建一个标准的 Anthropic SSE 响应序列（测试用）。
func BuildAnthropicSSE(inputTokens, outputTokens int, textChunks []string) string {
	var sb strings.Builder

	// message_start
	sb.WriteString("event: message_start\n")
	sb.WriteString(`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","usage":{"input_tokens":`)
	writeInt(&sb, inputTokens)
	sb.WriteString(`,"output_tokens":0}}}`)
	sb.WriteString("\n\n")

	// content_block_start
	sb.WriteString("event: content_block_start\n")
	sb.WriteString(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	sb.WriteString("\n\n")

	// content_block_delta（多个文本块）
	for _, chunk := range textChunks {
		sb.WriteString("event: content_block_delta\n")
		sb.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`)
		jsonChunk, _ := json.Marshal(chunk)
		sb.Write(jsonChunk)
		sb.WriteString(`}}`)
		sb.WriteString("\n\n")
	}

	// content_block_stop
	sb.WriteString("event: content_block_stop\n")
	sb.WriteString(`data: {"type":"content_block_stop","index":0}`)
	sb.WriteString("\n\n")

	// message_delta（含 output_tokens）
	sb.WriteString("event: message_delta\n")
	sb.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":`)
	writeInt(&sb, outputTokens)
	sb.WriteString(`}}`)
	sb.WriteString("\n\n")

	// message_stop
	sb.WriteString("event: message_stop\n")
	sb.WriteString(`data: {"type":"message_stop"}`)
	sb.WriteString("\n\n")

	return sb.String()
}

// writeInt 向 strings.Builder 写入整数。
func writeInt(sb *strings.Builder, n int) {
	if n == 0 {
		sb.WriteByte('0')
		return
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	sb.Write(buf)
}
