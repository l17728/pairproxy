package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Protocol Conversion: Anthropic Messages API ↔ OpenAI Chat Completions API
//
// 用例：Claude CLI (Anthropic 协议) → PairProxy → Ollama (OpenAI 协议)
//
// 转换触发条件：
//   - 请求路径为 /v1/messages（Anthropic）
//   - 目标 provider 为 "ollama" 或 "openai"
// ---------------------------------------------------------------------------

// shouldConvertProtocol 判断是否需要进行协议转换。
// 当请求路径为 Anthropic 格式但目标 provider 为 OpenAI 兼容时返回 true。
func shouldConvertProtocol(requestPath, targetProvider string) bool {
	isAnthropicPath := strings.HasPrefix(requestPath, "/v1/messages")
	isOpenAITarget := targetProvider == "openai" || targetProvider == "ollama"
	return isAnthropicPath && isOpenAITarget
}

// anthropicRequest Anthropic Messages API 请求结构（简化）
type anthropicRequest struct {
	Model      string                   `json:"model"`
	Messages   []anthropicMessage       `json:"messages"`
	MaxTokens  int                      `json:"max_tokens,omitempty"`
	System     interface{}              `json:"system,omitempty"` // string 或 []systemBlock
	Stream     bool                     `json:"stream,omitempty"`
	Other      map[string]interface{}   `json:"-"` // 其他字段保留
}

// anthropicMessage Anthropic 消息格式
type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string 或 []contentBlock
}

// openaiRequest OpenAI Chat Completions API 请求结构
type openaiRequest struct {
	Model      string         `json:"model"`
	Messages   []openaiMessage `json:"messages"`
	MaxTokens  int            `json:"max_tokens,omitempty"`
	Stream     bool           `json:"stream,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// openaiMessage OpenAI 消息格式
type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// convertAnthropicToOpenAIRequest 将 Anthropic 请求转换为 OpenAI 格式。
// 返回转换后的 body 和新的请求路径。
func convertAnthropicToOpenAIRequest(body []byte, logger *zap.Logger, reqID string) ([]byte, string, error) {
	if len(body) == 0 {
		return body, "/v1/chat/completions", nil
	}

	// 解析 Anthropic 请求
	var anthropicReq map[string]interface{}
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		logger.Warn("failed to parse Anthropic request for conversion, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, "/v1/chat/completions", err
	}

	// 构建 OpenAI 请求
	openaiReq := make(map[string]interface{})

	// 1. 复制基础字段
	if model, ok := anthropicReq["model"].(string); ok {
		openaiReq["model"] = model
	}
	if maxTokens, ok := anthropicReq["max_tokens"].(float64); ok {
		openaiReq["max_tokens"] = int(maxTokens)
	}
	if stream, ok := anthropicReq["stream"].(bool); ok {
		openaiReq["stream"] = stream
		// 流式请求自动注入 stream_options
		if stream {
			openaiReq["stream_options"] = map[string]interface{}{
				"include_usage": true,
			}
		}
	}

	// 2. 转换 messages
	openaiMessages := []map[string]interface{}{}

	// 2.1 处理 system 字段（转为 system role 消息）
	if system, ok := anthropicReq["system"]; ok && system != nil {
		systemContent := extractTextContent(system)
		if systemContent != "" {
			openaiMessages = append(openaiMessages, map[string]interface{}{
				"role":    "system",
				"content": systemContent,
			})
		}
	}

	// 2.2 转换 messages 数组
	if messages, ok := anthropicReq["messages"].([]interface{}); ok {
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]interface{}); ok {
				role, _ := msgMap["role"].(string)
				content := msgMap["content"]

				openaiMsg := map[string]interface{}{
					"role":    role,
					"content": extractTextContent(content),
				}
				openaiMessages = append(openaiMessages, openaiMsg)
			}
		}
	}

	openaiReq["messages"] = openaiMessages

	// 3. 序列化为 JSON
	converted, err := json.Marshal(openaiReq)
	if err != nil {
		logger.Warn("failed to marshal converted OpenAI request, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, "/v1/chat/completions", err
	}

	logger.Info("converted Anthropic request to OpenAI format",
		zap.String("request_id", reqID),
		zap.Int("original_size", len(body)),
		zap.Int("converted_size", len(converted)),
		zap.Int("message_count", len(openaiMessages)),
		zap.Bool("has_system", len(openaiMessages) > 0 && openaiMessages[0]["role"] == "system"),
		zap.Bool("is_streaming", openaiReq["stream"] != nil && openaiReq["stream"].(bool)),
	)

	return converted, "/v1/chat/completions", nil
}

// extractTextContent 从 Anthropic content 字段提取纯文本。
// content 可能是：
//   - string: 直接返回
//   - []contentBlock: 提取所有 type="text" 的 text 字段并拼接
func extractTextContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var texts []string
		for _, block := range v {
			if blockMap, ok := block.(map[string]interface{}); ok {
				if blockType, _ := blockMap["type"].(string); blockType == "text" {
					if text, ok := blockMap["text"].(string); ok {
						texts = append(texts, text)
					}
				}
				// 注意：非 text 类型的 block（如 image）会被忽略
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// convertOpenAIToAnthropicResponse 将 OpenAI 响应转换为 Anthropic 格式。
// 用于非流式响应的转换。
func convertOpenAIToAnthropicResponse(body []byte, logger *zap.Logger, reqID string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	// 解析 OpenAI 响应
	var openaiResp map[string]interface{}
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		logger.Warn("failed to parse OpenAI response for conversion, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, err
	}

	// 构建 Anthropic 响应
	anthropicResp := map[string]interface{}{
		"id":      openaiResp["id"],
		"type":    "message",
		"role":    "assistant",
		"model":   openaiResp["model"],
	}

	// 提取 content
	var contentText string
	if choices, ok := openaiResp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					contentText = content
				}
			}
		}
	}

	anthropicResp["content"] = []map[string]interface{}{
		{
			"type": "text",
			"text": contentText,
		},
	}

	// 提取 usage
	if usage, ok := openaiResp["usage"].(map[string]interface{}); ok {
		anthropicResp["usage"] = map[string]interface{}{
			"input_tokens":  int(usage["prompt_tokens"].(float64)),
			"output_tokens": int(usage["completion_tokens"].(float64)),
		}
	}

	// 提取 stop_reason
	if choices, ok := openaiResp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if finishReason, ok := choice["finish_reason"].(string); ok {
				anthropicResp["stop_reason"] = convertFinishReason(finishReason)
			}
		}
	}

	// 序列化
	converted, err := json.Marshal(anthropicResp)
	if err != nil {
		logger.Warn("failed to marshal converted Anthropic response, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, err
	}

	logger.Debug("converted OpenAI response to Anthropic format",
		zap.String("request_id", reqID),
		zap.Int("original_size", len(body)),
		zap.Int("converted_size", len(converted)),
		zap.String("stop_reason", anthropicResp["stop_reason"].(string)),
	)

	return converted, nil
}

// convertFinishReason 将 OpenAI finish_reason 转换为 Anthropic stop_reason
func convertFinishReason(openaiReason string) string {
	switch openaiReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return openaiReason
	}
}

// OpenAIToAnthropicStreamConverter 将 OpenAI SSE 流转换为 Anthropic SSE 流。
// 实现 http.ResponseWriter 接口，可以作为中间层插入响应链。
type OpenAIToAnthropicStreamConverter struct {
	writer      http.ResponseWriter
	logger      *zap.Logger
	reqID       string
	messageID   string
	sentStart   bool
	contentIdx  int
}

// NewOpenAIToAnthropicStreamConverter 创建流转换器。
func NewOpenAIToAnthropicStreamConverter(w http.ResponseWriter, logger *zap.Logger, reqID string) *OpenAIToAnthropicStreamConverter {
	return &OpenAIToAnthropicStreamConverter{
		writer:    w,
		logger:    logger,
		reqID:     reqID,
		messageID: "msg_" + reqID[:8],
	}
}

// Header 实现 http.ResponseWriter 接口
func (c *OpenAIToAnthropicStreamConverter) Header() http.Header {
	return c.writer.Header()
}

// WriteHeader 实现 http.ResponseWriter 接口
func (c *OpenAIToAnthropicStreamConverter) WriteHeader(statusCode int) {
	c.writer.WriteHeader(statusCode)
}

// Write 实现 http.ResponseWriter 接口，接收 OpenAI SSE chunk 并转换为 Anthropic 格式输出。
func (c *OpenAIToAnthropicStreamConverter) Write(chunk []byte) (int, error) {
	lines := bytes.Split(chunk, []byte("\n"))

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// 解析 SSE data 行
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

		// [DONE] 标记
		if string(payload) == "[DONE]" {
			c.sendMessageStop()
			continue
		}

		// 解析 OpenAI chunk
		var openaiChunk map[string]interface{}
		if err := json.Unmarshal(payload, &openaiChunk); err != nil {
			continue
		}

		// 发送 message_start（首次）
		if !c.sentStart {
			c.sendMessageStart()
			c.sentStart = true
		}

		// 提取 delta content
		if choices, ok := openaiChunk["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if content, ok := delta["content"].(string); ok && content != "" {
						c.sendContentDelta(content)
					}
				}
			}
		}

		// 提取 usage（最后一个 chunk）
		if usage, ok := openaiChunk["usage"].(map[string]interface{}); ok {
			c.sendMessageDelta(usage)
		}
	}

	return len(chunk), nil
}

// sendMessageStart 发送 message_start 事件
func (c *OpenAIToAnthropicStreamConverter) sendMessageStart() {
	event := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":    c.messageID,
			"type":  "message",
			"role":  "assistant",
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
	c.writeEvent("message_start", event)
	c.logger.Debug("stream conversion: message_start sent",
		zap.String("request_id", c.reqID),
		zap.String("message_id", c.messageID),
	)
}

// sendContentDelta 发送 content_block_delta 事件
func (c *OpenAIToAnthropicStreamConverter) sendContentDelta(text string) {
	if c.contentIdx == 0 {
		// 首次发送 content_block_start
		startEvent := map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}
		c.writeEvent("content_block_start", startEvent)
		c.contentIdx++
	}

	deltaEvent := map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": text,
		},
	}
	c.writeEvent("content_block_delta", deltaEvent)
}

// sendMessageDelta 发送 message_delta 事件（含 usage）
func (c *OpenAIToAnthropicStreamConverter) sendMessageDelta(usage map[string]interface{}) {
	// 先发送 content_block_stop
	if c.contentIdx > 0 {
		stopEvent := map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		}
		c.writeEvent("content_block_stop", stopEvent)
	}

	// 发送 message_delta
	outputTokens := int(usage["completion_tokens"].(float64))
	deltaEvent := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason": "end_turn",
		},
		"usage": map[string]interface{}{
			"output_tokens": outputTokens,
		},
	}
	c.writeEvent("message_delta", deltaEvent)
	c.logger.Debug("stream conversion: message_delta sent with usage",
		zap.String("request_id", c.reqID),
		zap.Int("output_tokens", outputTokens),
	)
}

// sendMessageStop 发送 message_stop 事件
func (c *OpenAIToAnthropicStreamConverter) sendMessageStop() {
	event := map[string]interface{}{
		"type": "message_stop",
	}
	c.writeEvent("message_stop", event)
	c.logger.Debug("stream conversion: message_stop sent, stream complete",
		zap.String("request_id", c.reqID),
	)
}

// writeEvent 写入 SSE 事件
func (c *OpenAIToAnthropicStreamConverter) writeEvent(eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		c.logger.Warn("failed to marshal SSE event",
			zap.String("request_id", c.reqID),
			zap.String("event_type", eventType),
			zap.Error(err),
		)
		return
	}

	fmt.Fprintf(c.writer, "event: %s\n", eventType)
	fmt.Fprintf(c.writer, "data: %s\n\n", jsonData)

	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
