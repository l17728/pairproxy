package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// ErrPrefillNotSupported は末尾 assistant メッセージ（prefill）が検出された場合に返されるエラー。
// OpenAI/Ollama エンドポイントは prefill をサポートしないため、HTTP 400 を返す。
var ErrPrefillNotSupported = errors.New("OpenAI/Ollama endpoints do not support assistant prefill")

// ErrThinkingNotSupported は Anthropic 拡張思考（thinking パラメータ）が検出された場合に返されるエラー。
// OpenAI/Ollama エンドポイントは thinking をサポートしないため、HTTP 400 を返す。
var ErrThinkingNotSupported = errors.New("Extended thinking (thinking parameter) is not supported for OpenAI/Ollama targets")

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

// ─── 请求转换：Anthropic → OpenAI ──────────────────────────────────────────

// convertAnthropicToOpenAIRequest 将 Anthropic Messages API 请求转换为 OpenAI Chat Completions 格式。
// 支持：文本消息、system 字段、工具调用（tool_use/tool_result）、tools 定义、tool_choice。
// 返回转换后的 body 和新的请求路径。
func convertAnthropicToOpenAIRequest(body []byte, logger *zap.Logger, reqID string) ([]byte, string, error) {
	const newPath = "/v1/chat/completions"
	if len(body) == 0 {
		return body, newPath, nil
	}

	var anthropicReq map[string]interface{}
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		logger.Warn("failed to parse Anthropic request for conversion, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, newPath, err
	}

	openaiReq := make(map[string]interface{})

	// thinking 参数检测：OpenAI/Ollama 不支持扩展思考，直接返回 400
	if thinking, exists := anthropicReq["thinking"]; exists && thinking != nil {
		logger.Warn("thinking parameter rejected for OpenAI/Ollama target",
			zap.String("request_id", reqID),
		)
		return body, newPath, ErrThinkingNotSupported
	}

	// 1. 基础字段
	if model, ok := anthropicReq["model"].(string); ok {
		openaiReq["model"] = model
	}
	if maxTokens, ok := anthropicReq["max_tokens"].(float64); ok {
		openaiReq["max_tokens"] = int(maxTokens)
	}
	// 透传采样参数（top_k 丢弃，OpenAI 不支持）
	for _, field := range []string{"temperature", "top_p"} {
		if val, ok := anthropicReq[field]; ok {
			openaiReq[field] = val
		}
	}
	if stopSeqs, ok := anthropicReq["stop_sequences"]; ok {
		openaiReq["stop"] = stopSeqs
	}

	// 2. 流式控制
	if stream, ok := anthropicReq["stream"].(bool); ok {
		openaiReq["stream"] = stream
		if stream {
			openaiReq["stream_options"] = map[string]interface{}{"include_usage": true}
		}
	}

	// 3. messages 数组
	var openaiMessages []map[string]interface{}

	// 3.1 system 字段 → messages[0] role=system
	if system, ok := anthropicReq["system"]; ok && system != nil {
		if systemText := extractTextContent(system); systemText != "" {
			openaiMessages = append(openaiMessages, map[string]interface{}{
				"role":    "system",
				"content": systemText,
			})
		}
	}

	// 3.2 转换 messages 数组（可能因 tool_result 拆分为多条）
	if messages, ok := anthropicReq["messages"].([]interface{}); ok {
		for _, msg := range messages {
			msgMap, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := msgMap["role"].(string)
			expanded := processAnthropicMessage(role, msgMap["content"])
			openaiMessages = append(openaiMessages, expanded...)
		}
	}
	openaiReq["messages"] = openaiMessages

	// Prefill 检测：若最后一条消息 role 为 assistant 且无 tool_calls，则为 prefill。
	// OpenAI/Ollama 不支持 prefill（以纯文本抢占 assistant 开头），拒绝请求。
	// 注意：assistant 消息含 tool_calls 时是合法的对话历史，不视为 prefill。
	if len(openaiMessages) > 0 {
		last := openaiMessages[len(openaiMessages)-1]
		if last["role"] == "assistant" {
			_, hasToolCalls := last["tool_calls"]
			if !hasToolCalls {
				logger.Warn("prefill detected: trailing assistant message rejected",
					zap.String("request_id", reqID),
					zap.Int("message_count", len(openaiMessages)),
				)
				return body, newPath, ErrPrefillNotSupported
			}
		}
	}

	// 4. tools 定义：input_schema → parameters，包裹 {type: function, function: {...}}
	if tools, ok := anthropicReq["tools"].([]interface{}); ok && len(tools) > 0 {
		if converted := convertAnthropicTools(tools); len(converted) > 0 {
			openaiReq["tools"] = converted
		}
	}

	// 5. tool_choice 转换
	if tc, ok := anthropicReq["tool_choice"]; ok && tc != nil {
		if converted := convertAnthropicToolChoice(tc); converted != nil {
			openaiReq["tool_choice"] = converted
		}
		// disable_parallel_tool_use → parallel_tool_calls: false
		if tcMap, ok := tc.(map[string]interface{}); ok {
			if disable, _ := tcMap["disable_parallel_tool_use"].(bool); disable {
				openaiReq["parallel_tool_calls"] = false
			}
		}
	}

	converted, err := json.Marshal(openaiReq)
	if err != nil {
		logger.Warn("failed to marshal converted OpenAI request, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, newPath, err
	}

	hasSystem := len(openaiMessages) > 0 && openaiMessages[0]["role"] == "system"
	_, hasTools := openaiReq["tools"]
	logger.Info("converted Anthropic request to OpenAI format",
		zap.String("request_id", reqID),
		zap.Int("original_size", len(body)),
		zap.Int("converted_size", len(converted)),
		zap.Int("message_count", len(openaiMessages)),
		zap.Bool("has_system", hasSystem),
		zap.Bool("has_tools", hasTools),
		zap.Bool("is_streaming", openaiReq["stream"] != nil && openaiReq["stream"].(bool)),
	)
	return converted, newPath, nil
}

// processAnthropicMessage 按 role 分发消息转换，返回一条或多条 OpenAI 消息。
// user 消息中的 tool_result 会被拆分为独立的 role=tool 消息。
func processAnthropicMessage(role string, content interface{}) []map[string]interface{} {
	switch role {
	case "assistant":
		return processAssistantContent(content)
	case "user":
		return processUserContent(content)
	default:
		return []map[string]interface{}{{"role": role, "content": extractTextContent(content)}}
	}
}

// processAssistantContent 处理 assistant 消息，将 tool_use 块转换为 OpenAI tool_calls 数组。
func processAssistantContent(content interface{}) []map[string]interface{} {
	switch v := content.(type) {
	case string:
		return []map[string]interface{}{{"role": "assistant", "content": v}}
	case []interface{}:
		var textParts []string
		var toolCalls []map[string]interface{}

		for _, block := range v {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			switch blockType {
			case "text":
				if text, ok := blockMap["text"].(string); ok && text != "" {
					textParts = append(textParts, text)
				}
			case "tool_use":
				id, _ := blockMap["id"].(string)
				name, _ := blockMap["name"].(string)
				// input（对象）→ arguments（JSON 字符串）
				argsBytes, _ := json.Marshal(blockMap["input"])
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   id,
					"type": "function",
					"function": map[string]interface{}{
						"name":      name,
						"arguments": string(argsBytes),
					},
				})
				// thinking/redacted_thinking/image/document 等均忽略
			}
		}

		msg := map[string]interface{}{"role": "assistant"}
		if len(textParts) > 0 {
			msg["content"] = strings.Join(textParts, "\n")
		} else {
			msg["content"] = nil // 纯工具调用时 content 为 null
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return []map[string]interface{}{msg}
	default:
		return []map[string]interface{}{{"role": "assistant", "content": fmt.Sprintf("%v", v)}}
	}
}

// processUserContent 处理 user 消息。
// 若 content 为纯字符串或纯文本块，直接映射为 user 消息。
// 若包含 tool_result 块，将其拆分为独立的 role=tool 消息，剩余文本保留为 user 消息。
func processUserContent(content interface{}) []map[string]interface{} {
	switch v := content.(type) {
	case string:
		return []map[string]interface{}{{"role": "user", "content": v}}
	case []interface{}:
		var toolMsgs []map[string]interface{}
		var openaiContentItems []interface{} // ordered content items for multimodal
		hasImage := false

		for _, block := range v {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			switch blockType {
			case "tool_result":
				toolUseID, _ := blockMap["tool_use_id"].(string)
				resultContent := extractToolResultContent(blockMap["content"])
				toolMsgs = append(toolMsgs, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": toolUseID,
					"content":      resultContent,
				})
			case "text":
				if text, ok := blockMap["text"].(string); ok && text != "" {
					openaiContentItems = append(openaiContentItems, map[string]interface{}{
						"type": "text",
						"text": text,
					})
				}
			case "image":
				hasImage = true
				if imgBlock := convertAnthropicImageBlock(blockMap); imgBlock != nil {
					openaiContentItems = append(openaiContentItems, imgBlock)
				}
			}
		}

		// tool 消息在前，user 文本消息在后
		var result []map[string]interface{}
		result = append(result, toolMsgs...)

		if len(openaiContentItems) > 0 {
			var userContent interface{}
			if hasImage {
				// 多模态：OpenAI array format
				userContent = openaiContentItems
			} else {
				// 纯文本：拼接为字符串（向后兼容）
				var parts []string
				for _, item := range openaiContentItems {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["text"].(string); ok {
							parts = append(parts, t)
						}
					}
				}
				userContent = strings.Join(parts, "\n")
			}
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": userContent,
			})
		}

		if len(result) == 0 {
			result = []map[string]interface{}{{"role": "user", "content": ""}}
		}
		return result
	default:
		return []map[string]interface{}{{"role": "user", "content": fmt.Sprintf("%v", v)}}
	}
}

// extractToolResultContent 从 tool_result 的 content 字段提取字符串内容。
// content 可能是 string 或 []contentBlock。
// convertMessageID 将 OpenAI 响应 id（如 chatcmpl-xxx）转换为 Anthropic 格式（msg_xxx）。
// 规范 §3.1/3.2：chatcmpl- 前缀替换为 msg_；其他格式直接加 msg_ 前缀。
func convertMessageID(id interface{}) string {
	s, _ := id.(string)
	if after, found := strings.CutPrefix(s, "chatcmpl-"); found {
		return "msg_" + after
	}
	return "msg_" + s
}

func extractToolResultContent(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		return extractTextContent(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// convertAnthropicImageBlock 将 Anthropic image 内容块转换为 OpenAI image_url 格式。
// 返回 nil 表示不支持的 source 类型（调用方应忽略该块）。
func convertAnthropicImageBlock(block map[string]interface{}) map[string]interface{} {
	source, ok := block["source"].(map[string]interface{})
	if !ok {
		return nil
	}
	sourceType, _ := source["type"].(string)
	switch sourceType {
	case "base64":
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		return map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]interface{}{
				"url":    fmt.Sprintf("data:%s;base64,%s", mediaType, data),
				"detail": "auto",
			},
		}
	case "url":
		url, _ := source["url"].(string)
		return map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]interface{}{
				"url":    url,
				"detail": "auto",
			},
		}
	default:
		return nil
	}
}

// convertAnthropicTools 将 Anthropic tools 数组转换为 OpenAI 格式。
// 关键变化：input_schema → parameters；包裹在 {type:"function", function:{...}}。
func convertAnthropicTools(tools []interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		funcDef := map[string]interface{}{}
		if name, ok := toolMap["name"].(string); ok {
			funcDef["name"] = name
		}
		if desc, ok := toolMap["description"].(string); ok {
			funcDef["description"] = desc
		}
		// input_schema → parameters（字段重命名，内容不变）
		if schema, ok := toolMap["input_schema"]; ok {
			funcDef["parameters"] = schema
		}
		// cache_control 丢弃（OpenAI 无此概念）
		result = append(result, map[string]interface{}{
			"type":     "function",
			"function": funcDef,
		})
	}
	return result
}

// convertAnthropicToolChoice 将 Anthropic tool_choice 转换为 OpenAI 格式。
//
//	{"type":"auto"}          → "auto"
//	{"type":"any"}           → "required"
//	{"type":"none"}          → "none"
//	{"type":"tool","name":X} → {"type":"function","function":{"name":X}}
func convertAnthropicToolChoice(tc interface{}) interface{} {
	tcMap, ok := tc.(map[string]interface{})
	if !ok {
		return nil
	}
	tcType, _ := tcMap["type"].(string)
	switch tcType {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		name, _ := tcMap["name"].(string)
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": name,
			},
		}
	default:
		return nil
	}
}

// extractTextContent 从 Anthropic content 字段提取纯文本。
// content 可能是：
//   - string: 直接返回
//   - []interface{}: 提取所有 type="text" 的 text 字段并换行拼接
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
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ─── 响应转换：OpenAI → Anthropic（非流式）──────────────────────────────────

// convertOpenAIToAnthropicResponse 将 OpenAI 非流式响应转换为 Anthropic 格式。
// 支持文本响应和工具调用响应（tool_calls → tool_use 内容块）。
func convertOpenAIToAnthropicResponse(body []byte, logger *zap.Logger, reqID string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var openaiResp map[string]interface{}
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		logger.Warn("failed to parse OpenAI response for conversion, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body, err
	}

	anthropicResp := map[string]interface{}{
		"id":            convertMessageID(openaiResp["id"]),
		"type":          "message",
		"role":          "assistant",
		"model":         openaiResp["model"],
		"stop_sequence": nil,
	}

	// 提取 choices[0]
	var stopReason string
	var contentBlocks []map[string]interface{}

	if choices, ok := openaiResp["choices"].([]interface{}); ok && len(choices) > 0 {
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			choice = map[string]interface{}{}
		}

		// finish_reason → stop_reason
		if finishReason, ok := choice["finish_reason"].(string); ok {
			stopReason = convertFinishReason(finishReason)
		}

		if message, ok := choice["message"].(map[string]interface{}); ok {
			// 文本内容
			if contentStr, ok := message["content"].(string); ok && contentStr != "" {
				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type": "text",
					"text": contentStr,
				})
			}

			// 工具调用：tool_calls → tool_use 内容块
			if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]interface{})
					if !ok {
						continue
					}
					id, _ := tcMap["id"].(string)
					funcMap, _ := tcMap["function"].(map[string]interface{})
					name, _ := funcMap["name"].(string)
					argsStr, _ := funcMap["arguments"].(string)

					// arguments（JSON 字符串）→ input（解析后的对象）
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
						input = map[string]interface{}{}
					}
					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type":  "tool_use",
						"id":    id,
						"name":  name,
						"input": input,
					})
				}
			}
		}
	}

	if len(contentBlocks) == 0 {
		contentBlocks = []map[string]interface{}{{"type": "text", "text": ""}}
	}
	anthropicResp["content"] = contentBlocks
	anthropicResp["stop_reason"] = stopReason

	// usage 转换：prompt_tokens 按缓存拆分
	if usage, ok := openaiResp["usage"].(map[string]interface{}); ok {
		promptTokens := floatToInt(usage["prompt_tokens"])
		completionTokens := floatToInt(usage["completion_tokens"])
		cachedTokens := 0
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			cachedTokens = floatToInt(details["cached_tokens"])
		}
		anthropicResp["usage"] = map[string]interface{}{
			"input_tokens":                promptTokens - cachedTokens,
			"output_tokens":               completionTokens,
			"cache_read_input_tokens":     cachedTokens,
			"cache_creation_input_tokens": 0,
		}
	}

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
		zap.String("stop_reason", stopReason),
		zap.Int("content_blocks", len(contentBlocks)),
	)
	return converted, nil
}

// convertFinishReason 将 OpenAI finish_reason 转换为 Anthropic stop_reason。
func convertFinishReason(openaiReason string) string {
	switch openaiReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return openaiReason
	}
}

// floatToInt 从 JSON 反序列化的 float64 转 int（安全版）。
func floatToInt(v interface{}) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// ─── 响应转换：OpenAI → Anthropic（流式）────────────────────────────────────

// toolCallBuffer 缓冲单个工具调用在流式传输中的各字段片段。
type toolCallBuffer struct {
	id        string
	name      string
	argChunks []string // 按到达顺序存储的 arguments 片段（保留流式分片）
}

// OpenAIToAnthropicStreamConverter 将 OpenAI SSE 流转换为 Anthropic SSE 流。
//
// 设计：采用"全量缓冲后发射"策略——缓冲所有 OpenAI SSE chunk 直到收到 [DONE]，
// 再按顺序重新发射完整的 Anthropic SSE 事件序列。
//
// 这样做的目的：OpenAI 的 token 用量（prompt_tokens）只在流的最后一个 chunk 中出现，
// 而 Anthropic 的 message_start（流的第一个事件）就需要携带 input_tokens。
// 缓冲策略确保 message_start 中的 input_tokens 始终是准确值，对计费至关重要。
//
// 实现 http.ResponseWriter 接口，可作为中间层插入响应链。
type OpenAIToAnthropicStreamConverter struct {
	writer http.ResponseWriter
	logger *zap.Logger
	reqID  string

	// 缓冲状态（在 Write 调用中逐步填充）
	messageID  string
	textChunks []string                // 按到达顺序的文本 delta 片段
	toolCalls  map[int]*toolCallBuffer // tool_call index → 缓冲
	toolOrder  []int                   // 工具调用的插入顺序（保证输出顺序）
	finishReason string

	// 来自 usage chunk 的 token 统计
	promptTokens     int
	completionTokens int
	cachedTokens     int

	done bool // 是否已处理 [DONE] 并发射完毕
}

// NewOpenAIToAnthropicStreamConverter 创建流转换器。
func NewOpenAIToAnthropicStreamConverter(w http.ResponseWriter, logger *zap.Logger, reqID string) *OpenAIToAnthropicStreamConverter {
	return &OpenAIToAnthropicStreamConverter{
		writer:    w,
		logger:    logger,
		reqID:     reqID,
		messageID: "msg_" + reqID[:8],
		toolCalls: make(map[int]*toolCallBuffer),
	}
}

// Header 实现 http.ResponseWriter 接口。
func (c *OpenAIToAnthropicStreamConverter) Header() http.Header {
	return c.writer.Header()
}

// WriteHeader 实现 http.ResponseWriter 接口。
func (c *OpenAIToAnthropicStreamConverter) WriteHeader(statusCode int) {
	c.writer.WriteHeader(statusCode)
}

// Write 接收 OpenAI SSE chunk，缓冲所有数据。收到 [DONE] 后触发 flush()。
func (c *OpenAIToAnthropicStreamConverter) Write(chunk []byte) (int, error) {
	if c.done {
		return len(chunk), nil
	}

	lines := bytes.Split(chunk, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

		// [DONE] 流结束标志：触发发射阶段
		if string(payload) == "[DONE]" {
			c.flush()
			c.done = true
			return len(chunk), nil
		}

		if len(payload) == 0 {
			continue
		}

		var openaiChunk map[string]interface{}
		if err := json.Unmarshal(payload, &openaiChunk); err != nil {
			continue // 解析失败静默忽略
		}
		c.bufferChunk(openaiChunk)
	}
	return len(chunk), nil
}

// bufferChunk 解析单个 OpenAI chunk，提取并缓冲各字段。
func (c *OpenAIToAnthropicStreamConverter) bufferChunk(chunk map[string]interface{}) {
	// 提取 message ID（首个 chunk 中）
	if c.messageID == "msg_"+c.reqID[:8] {
		if id, ok := chunk["id"].(string); ok && id != "" {
			// 去掉 chatcmpl- 前缀，替换为 msg_ 前缀
			if after, found := strings.CutPrefix(id, "chatcmpl-"); found {
				c.messageID = "msg_" + after
			}
		}
	}

	// 解析 usage（可能在 finish_reason chunk 或独立的 usage chunk 中）
	if usage, ok := chunk["usage"].(map[string]interface{}); ok {
		c.promptTokens = floatToInt(usage["prompt_tokens"])
		c.completionTokens = floatToInt(usage["completion_tokens"])
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			c.cachedTokens = floatToInt(details["cached_tokens"])
		}
	}

	// 解析 choices[0].delta
	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return
	}

	// finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		c.finishReason = fr
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return
	}

	// 文本内容 delta
	if content, ok := delta["content"].(string); ok && content != "" {
		c.textChunks = append(c.textChunks, content)
	}

	// 工具调用 delta（可能含多个并行工具，用 index 区分）
	if rawToolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		for _, rawTC := range rawToolCalls {
			tcMap, ok := rawTC.(map[string]interface{})
			if !ok {
				continue
			}
			idx := floatToInt(tcMap["index"])

			// 首次出现该 index：初始化缓冲
			if _, exists := c.toolCalls[idx]; !exists {
				c.toolCalls[idx] = &toolCallBuffer{}
				c.toolOrder = append(c.toolOrder, idx)
			}
			tc := c.toolCalls[idx]

			if id, ok := tcMap["id"].(string); ok && id != "" {
				tc.id = id
			}
			if funcMap, ok := tcMap["function"].(map[string]interface{}); ok {
				if name, ok := funcMap["name"].(string); ok && name != "" {
					tc.name = name
				}
				if args, ok := funcMap["arguments"].(string); ok {
					tc.argChunks = append(tc.argChunks, args)
				}
			}
		}
	}
}

// flush 在收到 [DONE] 后，按顺序发射完整的 Anthropic SSE 事件序列。
func (c *OpenAIToAnthropicStreamConverter) flush() {
	inputTokens := c.promptTokens - c.cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	// 1. message_start（携带准确的 input_tokens）
	c.writeEvent("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            c.messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":                inputTokens,
				"output_tokens":               1, // 占位值，真实值在 message_delta
				"cache_read_input_tokens":     c.cachedTokens,
				"cache_creation_input_tokens": 0,
			},
		},
	})
	c.logger.Debug("stream conversion: message_start sent",
		zap.String("request_id", c.reqID),
		zap.String("message_id", c.messageID),
		zap.Int("input_tokens", inputTokens),
	)

	contentIdx := 0

	// 2. 文本内容块（若有）
	if len(c.textChunks) > 0 {
		c.writeEvent("content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": contentIdx,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})
		for _, chunk := range c.textChunks {
			c.writeEvent("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentIdx,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": chunk,
				},
			})
		}
		c.writeEvent("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentIdx,
		})
		contentIdx++
	}

	// 3. 工具调用内容块（按插入顺序）
	for _, idx := range c.toolOrder {
		tc := c.toolCalls[idx]
		c.writeEvent("content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": contentIdx,
			"content_block": map[string]interface{}{
				"type":  "tool_use",
				"id":    tc.id,
				"name":  tc.name,
				"input": map[string]interface{}{}, // 占位，真实内容在 input_json_delta
			},
		})
		for _, argChunk := range tc.argChunks {
			if argChunk == "" {
				continue
			}
			c.writeEvent("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentIdx,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": argChunk,
				},
			})
		}
		c.writeEvent("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentIdx,
		})
		contentIdx++
	}

	// 4. message_delta（stop_reason + 准确 output_tokens）
	stopReason := convertFinishReason(c.finishReason)
	if stopReason == "" {
		stopReason = "end_turn"
	}
	c.writeEvent("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": c.completionTokens,
		},
	})
	c.logger.Debug("stream conversion: message_delta sent",
		zap.String("request_id", c.reqID),
		zap.String("stop_reason", stopReason),
		zap.Int("output_tokens", c.completionTokens),
	)

	// 5. message_stop
	c.writeEvent("message_stop", map[string]interface{}{
		"type": "message_stop",
	})
	c.logger.Debug("stream conversion: message_stop sent, stream complete",
		zap.String("request_id", c.reqID),
	)
}

// writeEvent 向下游写入一个 Anthropic SSE 事件。
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

// ─── 错误响应处理 ─────────────────────────────────────────────────────────────

// convertOpenAIErrorResponse 将 OpenAI 格式错误响应体转换为 Anthropic 格式。
// 若 body 不是 OpenAI 错误格式（无 error.message），原样返回。
// HTTP 状态码由调用方保留不变。
func convertOpenAIErrorResponse(body []byte, logger *zap.Logger, reqID string) []byte {
	var openaiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &openaiErr); err != nil || openaiErr.Error.Message == "" {
		return body // 不是 OpenAI 错误格式，原样返回
	}

	errType := openaiErr.Error.Type
	if errType == "" {
		errType = "api_error"
	}
	// error.type 映射（按设计文档 §5.1）
	switch errType {
	case "insufficient_quota":
		errType = "rate_limit_error"
	case "server_error":
		errType = "api_error"
	}

	resp := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errType,
			"message": openaiErr.Error.Message,
		},
	}
	converted, err := json.Marshal(resp)
	if err != nil {
		logger.Warn("failed to marshal Anthropic error response",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body
	}
	logger.Debug("converted OpenAI error to Anthropic format",
		zap.String("request_id", reqID),
		zap.String("error_type", errType),
	)
	return converted
}

// writeAnthropicError は Anthropic 形式のエラー JSON を HTTP レスポンスに書き込む。
// 協議転換レイヤーが自分でエラーを返す（上流に転送しない）場合に使用する。
func writeAnthropicError(w http.ResponseWriter, statusCode int, errorType, message string) {
	resp := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errorType,
			"message": message,
		},
	}
	body, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(body) //nolint:errcheck
}
