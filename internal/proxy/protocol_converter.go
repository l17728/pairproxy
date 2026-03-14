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

// ErrThinkingNotSupported は将来の拡張のために保留。現在は使用されていない。
// thinking パラメータは OpenAI/Ollama 向け変換時に静默剥离される。
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

// conversionDirection 表示协议转换方向。
type conversionDirection int

const (
	conversionNone conversionDirection = iota // 无需转换
	conversionAtoO                            // Anthropic 客户端 → OpenAI/Ollama 目标（已有实现）
	conversionOtoA                            // OpenAI 客户端 → Anthropic 目标（新增）
)

// detectConversionDirection 根据请求路径和目标 provider 判断协议转换方向。
func detectConversionDirection(requestPath, targetProvider string) conversionDirection {
	if strings.HasPrefix(requestPath, "/v1/messages") &&
		(targetProvider == "openai" || targetProvider == "ollama") {
		return conversionAtoO
	}
	if strings.HasPrefix(requestPath, "/v1/chat/completions") &&
		targetProvider == "anthropic" {
		return conversionOtoA
	}
	return conversionNone
}

// shouldConvertProtocol は sproxy.go との互換のため一時的に残す。Task 5 で削除する。
func shouldConvertProtocol(path, provider string) bool {
	return detectConversionDirection(path, provider) == conversionAtoO
}

// mapModelName 将 Anthropic 模型名映射到目标提供商的本地模型名。
// 优先精确匹配，其次通配符 "*"，均未命中则返回原名。
// mapping 为 nil 或空时直接返回原名。
func mapModelName(model string, mapping map[string]string) string {
	if len(mapping) == 0 {
		return model
	}
	if mapped, ok := mapping[model]; ok {
		return mapped
	}
	if wildcard, ok := mapping["*"]; ok {
		return wildcard
	}
	return model
}

// ─── 请求转换：Anthropic → OpenAI ──────────────────────────────────────────

// convertAnthropicToOpenAIRequest 将 Anthropic Messages API 请求转换为 OpenAI Chat Completions 格式。
// 支持：文本消息、system 字段、工具调用（tool_use/tool_result）、tools 定义、tool_choice。
// modelMapping 可选，非 nil 时将 Anthropic 模型名转换为目标提供商的本地模型名。
// 返回转换后的 body 和新的请求路径。
func convertAnthropicToOpenAIRequest(body []byte, logger *zap.Logger, reqID string, modelMapping map[string]string) ([]byte, string, error) {
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

	// thinking 参数处理：OpenAI/Ollama 不支持扩展思考，静默剥离后继续转换
	if thinking, exists := anthropicReq["thinking"]; exists && thinking != nil {
		delete(anthropicReq, "thinking")
		logger.Debug("thinking parameter stripped for OpenAI/Ollama target",
			zap.String("request_id", reqID),
		)
	}

	// 1. 基础字段
	if model, ok := anthropicReq["model"].(string); ok {
		mapped := mapModelName(model, modelMapping)
		openaiReq["model"] = mapped
		if mapped != model {
			logger.Debug("model name mapped for protocol conversion",
				zap.String("request_id", reqID),
				zap.String("original", model),
				zap.String("mapped", mapped),
			)
		}
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
// requestedModel 为原始 Anthropic 请求中的模型名（如 "claude-opus-4-6"）；
// 非空时覆盖 OpenAI 响应中的 model 字段（避免返回 Ollama 本地模型名）。
func convertOpenAIToAnthropicResponse(body []byte, logger *zap.Logger, reqID string, requestedModel string) ([]byte, error) {
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

	// model 字段：优先使用原始 Anthropic 模型名（避免返回 Ollama 本地模型名给客户端）
	responseModel := openaiResp["model"]
	if requestedModel != "" {
		responseModel = requestedModel
	}
	anthropicResp := map[string]interface{}{
		"id":            convertMessageID(openaiResp["id"]),
		"type":          "message",
		"role":          "assistant",
		"model":         responseModel,
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
	case "content_filter":
		return "end_turn"
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
	argChunks []string // 待发射的 arguments 片段
}

// OpenAIToAnthropicStreamConverter 将 OpenAI SSE 流转换为 Anthropic SSE 流。
//
// 设计：采用"逐步发射"策略——边接收 OpenAI SSE chunk，边即时发射 Anthropic SSE 事件。
// message_start 在收到第一个内容 delta 时发射（input_tokens 置 0，因尚未可知），
// content_block_start/delta 随数据到达逐步发射，
// message_delta（携带准确 output_tokens）和 message_stop 在收到 [DONE] 时发射。
//
// 注：input_tokens 在 message_start 中为 0（占位）；准确的 input/output tokens 由
// TeeResponseWriter 直接解析 OpenAI SSE 获取，不影响服务端计费。
//
// 实现 http.ResponseWriter 接口，可作为中间层插入响应链。
type OpenAIToAnthropicStreamConverter struct {
	writer http.ResponseWriter
	logger *zap.Logger
	reqID  string
	model  string // 原始 Anthropic 模型名（如 "claude-opus-4-6"），用于 message_start.message.model

	// 渐进发射状态
	messageID        string
	messageStartSent bool
	nextBlockIdx     int

	// 文本块状态
	textBlockIdx  int
	textBlockOpen bool

	// 工具调用块状态
	toolBuffers    map[int]*toolCallBuffer
	toolBlockIdxMap map[int]int
	toolOrder      []int
	openToolBlocks map[int]bool

	// 流末信息（在收到 [DONE] 时使用）
	finishReason     string
	promptTokens     int
	completionTokens int
	cachedTokens     int

	done bool
}

// NewOpenAIToAnthropicStreamConverter 创建流转换器（渐进发射模式）。
// model 为原始 Anthropic 请求中的模型名（可为空，空时不注入 message_start 的 model 字段）。
func NewOpenAIToAnthropicStreamConverter(w http.ResponseWriter, logger *zap.Logger, reqID string, model string) *OpenAIToAnthropicStreamConverter {
	msgID := "msg_"
	if len(reqID) >= 8 {
		msgID += reqID[:8]
	} else {
		msgID += reqID
	}
	return &OpenAIToAnthropicStreamConverter{
		writer:          w,
		logger:          logger,
		reqID:           reqID,
		model:           model,
		messageID:       msgID,
		textBlockIdx:    -1,
		toolBuffers:     make(map[int]*toolCallBuffer),
		toolBlockIdxMap: make(map[int]int),
		openToolBlocks:  make(map[int]bool),
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

// Write 接收 OpenAI SSE chunk，逐步发射对应 Anthropic SSE 事件。
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

		if string(payload) == "[DONE]" {
			c.handleDone()
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
		c.processChunk(openaiChunk)
	}
	return len(chunk), nil
}

// processChunk 解析单个 OpenAI chunk 并立即发射对应 Anthropic 事件。
func (c *OpenAIToAnthropicStreamConverter) processChunk(chunk map[string]interface{}) {
	// 从首块提取 message ID（在 message_start 发射之前）
	if !c.messageStartSent {
		if id, ok := chunk["id"].(string); ok && id != "" {
			if after, found := strings.CutPrefix(id, "chatcmpl-"); found {
				c.messageID = "msg_" + after
			} else {
				c.messageID = "msg_" + id
			}
		}
	}

	// 提取 usage（通常在最后一个有效 chunk 中）
	if usage, ok := chunk["usage"].(map[string]interface{}); ok {
		c.promptTokens = floatToInt(usage["prompt_tokens"])
		c.completionTokens = floatToInt(usage["completion_tokens"])
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			c.cachedTokens = floatToInt(details["cached_tokens"])
		}
	}

	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return
	}

	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		c.finishReason = fr
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return
	}

	// 文本内容 delta
	if content, ok := delta["content"].(string); ok && content != "" {
		c.ensureMessageStart()
		if !c.textBlockOpen {
			c.textBlockIdx = c.nextBlockIdx
			c.nextBlockIdx++
			c.writeEvent("content_block_start", map[string]interface{}{
				"type":  "content_block_start",
				"index": c.textBlockIdx,
				"content_block": map[string]interface{}{
					"type": "text",
					"text": "",
				},
			})
			c.textBlockOpen = true
		}
		c.writeEvent("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": c.textBlockIdx,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": content,
			},
		})
	}

	// 工具调用 delta
	if rawToolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		c.ensureMessageStart()
		// 文本块如仍开放，在工具调用开始前关闭
		if c.textBlockOpen {
			c.writeEvent("content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": c.textBlockIdx,
			})
			c.textBlockOpen = false
		}
		for _, rawTC := range rawToolCalls {
			tcMap, ok := rawTC.(map[string]interface{})
			if !ok {
				continue
			}
			c.processToolCallDelta(tcMap)
		}
	}
}

// processToolCallDelta 处理单个工具调用 delta，维护状态并逐步发射事件。
func (c *OpenAIToAnthropicStreamConverter) processToolCallDelta(tcMap map[string]interface{}) {
	idx := floatToInt(tcMap["index"])

	// 初始化该工具调用的缓冲
	if _, exists := c.toolBuffers[idx]; !exists {
		c.toolBuffers[idx] = &toolCallBuffer{}
		c.toolOrder = append(c.toolOrder, idx)
		c.toolBlockIdxMap[idx] = c.nextBlockIdx
		c.nextBlockIdx++
	}
	buf := c.toolBuffers[idx]
	blockIdx := c.toolBlockIdxMap[idx]

	if id, ok := tcMap["id"].(string); ok && id != "" {
		buf.id = id
	}
	if funcMap, ok := tcMap["function"].(map[string]interface{}); ok {
		if name, ok := funcMap["name"].(string); ok && name != "" {
			buf.name = name
		}
		if args, ok := funcMap["arguments"].(string); ok && args != "" {
			buf.argChunks = append(buf.argChunks, args)
		}
	}

	if !c.openToolBlocks[idx] {
		// 未开放 → 若已有 id 和 name，立即开放并发射缓冲的 arg chunks
		if buf.id != "" && buf.name != "" {
			c.writeEvent("content_block_start", map[string]interface{}{
				"type":  "content_block_start",
				"index": blockIdx,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    buf.id,
					"name":  buf.name,
					"input": map[string]interface{}{},
				},
			})
			c.openToolBlocks[idx] = true
			for _, argChunk := range buf.argChunks {
				if argChunk == "" {
					continue
				}
				c.writeEvent("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": blockIdx,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": argChunk,
					},
				})
			}
			buf.argChunks = nil
		}
		// 尚无 id/name → 仅缓冲，等待下一个 chunk
	} else {
		// 已开放 → 直接发射新到达的 arg chunks
		for _, argChunk := range buf.argChunks {
			if argChunk == "" {
				continue
			}
			c.writeEvent("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": argChunk,
				},
			})
		}
		buf.argChunks = nil
	}
}

// ensureMessageStart 确保 message_start 事件已发射（懒发射，首次内容到达时触发）。
// 在渐进模式下，message_start 的 input_tokens 为 0（占位），
// 服务端计费由 TeeResponseWriter 直接解析 OpenAI SSE 完成，不受此值影响。
func (c *OpenAIToAnthropicStreamConverter) ensureMessageStart() {
	if c.messageStartSent {
		return
	}
	msgObj := map[string]interface{}{
		"id":            c.messageID,
		"type":          "message",
		"role":          "assistant",
		"content":       []interface{}{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":                0,
			"output_tokens":               0,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
		},
	}
	if c.model != "" {
		msgObj["model"] = c.model
	}
	c.writeEvent("message_start", map[string]interface{}{
		"type":    "message_start",
		"message": msgObj,
	})
	c.messageStartSent = true
	c.logger.Debug("stream conversion: message_start sent (progressive)",
		zap.String("request_id", c.reqID),
		zap.String("message_id", c.messageID),
	)
}

// handleDone 在收到 [DONE] 后完成 Anthropic 事件序列的发射。
// 关闭所有开放内容块，并发射 message_delta（含准确 output_tokens）和 message_stop。
func (c *OpenAIToAnthropicStreamConverter) handleDone() {
	// 空响应（无任何内容 delta）也确保 message_start 已发射
	c.ensureMessageStart()

	// 关闭文本块
	if c.textBlockOpen {
		c.writeEvent("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": c.textBlockIdx,
		})
		c.textBlockOpen = false
	}

	// 关闭所有工具调用块（含未来得及开放的块）
	for _, idx := range c.toolOrder {
		blockIdx := c.toolBlockIdxMap[idx]
		buf := c.toolBuffers[idx]
		if !c.openToolBlocks[idx] {
			// 尚未开放 → 补发 content_block_start（用已有信息）
			c.writeEvent("content_block_start", map[string]interface{}{
				"type":  "content_block_start",
				"index": blockIdx,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    buf.id,
					"name":  buf.name,
					"input": map[string]interface{}{},
				},
			})
			for _, argChunk := range buf.argChunks {
				if argChunk == "" {
					continue
				}
				c.writeEvent("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": blockIdx,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": argChunk,
					},
				})
			}
		}
		c.writeEvent("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": blockIdx,
		})
	}

	// message_delta：准确 output_tokens 和 input_tokens（在 [DONE] 时已知）
	stopReason := convertFinishReason(c.finishReason)
	if stopReason == "" {
		stopReason = "end_turn"
	}
	inputTokens := c.promptTokens - c.cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	c.writeEvent("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens":               c.completionTokens,
			"input_tokens":                inputTokens,
			"cache_read_input_tokens":     c.cachedTokens,
			"cache_creation_input_tokens": 0,
		},
	})

	// message_stop
	c.writeEvent("message_stop", map[string]interface{}{
		"type": "message_stop",
	})

	c.logger.Debug("stream conversion: completed (progressive)",
		zap.String("request_id", c.reqID),
		zap.String("stop_reason", stopReason),
		zap.Int("output_tokens", c.completionTokens),
		zap.Int("input_tokens_actual", inputTokens),
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

// ─── リクエスト変換：OpenAI → Anthropic ───────────────────────────────────────

// convertOpenAIToAnthropicRequest はOpenAI Chat Completions リクエストを Anthropic Messages API 形式に変換する。
// Returns (converted body, "/v1/messages", nil) on success.
// Returns (nil, "/v1/messages", error) on JSON parse failure — caller must return HTTP 400.
func convertOpenAIToAnthropicRequest(body []byte, logger *zap.Logger, reqID string, modelMapping map[string]string) ([]byte, string, error) {
	const newPath = "/v1/messages"
	if len(body) == 0 {
		return body, newPath, nil
	}

	var openaiReq map[string]interface{}
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		logger.Warn("failed to parse OpenAI request for OtoA conversion",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return nil, newPath, err
	}

	anthropicReq := make(map[string]interface{})

	// 1. model (with mapping)
	if model, ok := openaiReq["model"].(string); ok {
		anthropicReq["model"] = mapModelName(model, modelMapping)
	}

	// 2. Pass-through scalars
	for _, key := range []string{"max_tokens", "temperature", "top_p", "stream"} {
		if v, ok := openaiReq[key]; ok {
			anthropicReq[key] = v
		}
	}

	// 3. stop → stop_sequences (normalize to array)
	if stop, ok := openaiReq["stop"]; ok {
		switch s := stop.(type) {
		case string:
			anthropicReq["stop_sequences"] = []string{s}
		case []interface{}:
			anthropicReq["stop_sequences"] = s
		}
	}

	// 4. tools → Anthropic format
	if tools, ok := openaiReq["tools"].([]interface{}); ok {
		anthropicTools := make([]map[string]interface{}, 0, len(tools))
		for _, t := range tools {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]interface{})
			if !ok {
				continue
			}
			at := map[string]interface{}{
				"name": fn["name"],
			}
			if desc, ok := fn["description"]; ok {
				at["description"] = desc
			}
			if params, ok := fn["parameters"]; ok {
				at["input_schema"] = params
			}
			anthropicTools = append(anthropicTools, at)
		}
		if len(anthropicTools) > 0 {
			anthropicTools2 := make([]interface{}, len(anthropicTools))
			for i, at := range anthropicTools {
				anthropicTools2[i] = at
			}
			anthropicReq["tools"] = anthropicTools2
		}
	}

	// 5. tool_choice
	if tc, ok := openaiReq["tool_choice"]; ok {
		anthropicReq["tool_choice"] = convertOpenAIToolChoice(tc)
	}

	// 6. messages: extract system, convert roles
	rawMessages, _ := openaiReq["messages"].([]interface{})
	var systemParts []string
	var anthropicMessages []interface{}

	i := 0
	for i < len(rawMessages) {
		msg, ok := rawMessages[i].(map[string]interface{})
		if !ok {
			i++
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system":
			if text, ok := msg["content"].(string); ok {
				systemParts = append(systemParts, text)
			}
			i++
		case "user":
			anthropicMessages = append(anthropicMessages, convertOpenAIUserMessage(msg))
			i++
		case "assistant":
			anthropicMessages = append(anthropicMessages, convertOpenAIAssistantMessage(msg))
			i++
			// Collect consecutive tool messages that follow
			var toolResults []interface{}
			for i < len(rawMessages) {
				next, ok := rawMessages[i].(map[string]interface{})
				if !ok || next["role"] != "tool" {
					break
				}
				toolResults = append(toolResults, map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": next["tool_call_id"],
					"content":     extractToolResultContent(next["content"]),
				})
				i++
			}
			if len(toolResults) > 0 {
				anthropicMessages = append(anthropicMessages, map[string]interface{}{
					"role":    "user",
					"content": toolResults,
				})
			}
		default:
			i++
		}
	}

	if len(systemParts) > 0 {
		anthropicReq["system"] = strings.Join(systemParts, "\n\n")
	}
	if len(anthropicMessages) > 0 {
		anthropicReq["messages"] = anthropicMessages
	}

	converted, err := json.Marshal(anthropicReq)
	if err != nil {
		logger.Warn("failed to marshal Anthropic request",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return nil, newPath, err
	}
	logger.Debug("OpenAI request converted to Anthropic format",
		zap.String("request_id", reqID),
		zap.Int("original_size", len(body)),
		zap.Int("converted_size", len(converted)),
	)
	return converted, newPath, nil
}

// convertOpenAIToolChoice はOpenAI tool_choice 値を Anthropic 形式に変換する。
func convertOpenAIToolChoice(tc interface{}) map[string]interface{} {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]interface{}{"type": "auto"}
		case "none":
			return map[string]interface{}{"type": "none"}
		case "required":
			return map[string]interface{}{"type": "any"}
		}
	case map[string]interface{}:
		if fn, ok := v["function"].(map[string]interface{}); ok {
			return map[string]interface{}{
				"type": "tool",
				"name": fn["name"],
			}
		}
	}
	return map[string]interface{}{"type": "auto"}
}

// convertOpenAIUserMessage は user ロールのメッセージを変換する。
func convertOpenAIUserMessage(msg map[string]interface{}) map[string]interface{} {
	content := msg["content"]
	switch c := content.(type) {
	case string:
		return map[string]interface{}{"role": "user", "content": c}
	case []interface{}:
		anthropicContent := make([]interface{}, 0, len(c))
		for _, item := range c {
			im, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch im["type"] {
			case "text":
				anthropicContent = append(anthropicContent, map[string]interface{}{
					"type": "text",
					"text": im["text"],
				})
			case "image_url":
				if iu, ok := im["image_url"].(map[string]interface{}); ok {
					if block := convertOpenAIImageURL(iu); block != nil {
						anthropicContent = append(anthropicContent, block)
					}
				}
			}
		}
		return map[string]interface{}{"role": "user", "content": anthropicContent}
	default:
		return map[string]interface{}{"role": "user", "content": ""}
	}
}

// convertOpenAIImageURL は OpenAI image_url 項目を Anthropic image ブロックに変換する。
func convertOpenAIImageURL(iu map[string]interface{}) map[string]interface{} {
	rawURL, _ := iu["url"].(string)
	if strings.HasPrefix(rawURL, "data:") {
		// data:TYPE;base64,DATA
		rest := strings.TrimPrefix(rawURL, "data:")
		parts := strings.SplitN(rest, ";base64,", 2)
		if len(parts) != 2 {
			return nil
		}
		return map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": parts[0],
				"data":       parts[1],
			},
		}
	}
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type": "url",
			"url":  rawURL,
		},
	}
}

// convertOpenAIAssistantMessage は assistant ロールのメッセージを変換する。
func convertOpenAIAssistantMessage(msg map[string]interface{}) map[string]interface{} {
	var blocks []interface{}

	if text, ok := msg["content"].(string); ok && text != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
	}

	if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
		for _, tc := range toolCalls {
			tm, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]interface{})
			if !ok {
				continue
			}
			// arguments is a JSON string → parse to object
			var input interface{} = map[string]interface{}{}
			if args, ok := fn["arguments"].(string); ok {
				if err := json.Unmarshal([]byte(args), &input); err != nil {
					input = map[string]interface{}{}
				}
			}
			blocks = append(blocks, map[string]interface{}{
				"type":  "tool_use",
				"id":    tm["id"],
				"name":  fn["name"],
				"input": input,
			})
		}
	}

	if len(blocks) == 0 {
		blocks = []interface{}{map[string]interface{}{"type": "text", "text": ""}}
	}

	return map[string]interface{}{"role": "assistant", "content": blocks}
}
