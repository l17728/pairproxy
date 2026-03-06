package proxy

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"
)

// injectOpenAIStreamOptions 对 OpenAI 流式请求注入 stream_options.include_usage: true。
// 仅当 path 为 /v1/chat/completions 且 "stream": true 时生效。
// 注入失败时静默降级，返回原 body（不中断请求）。
func injectOpenAIStreamOptions(path string, body []byte, logger *zap.Logger, reqID string) []byte {
	// 1. 仅处理 OpenAI chat completions 路径
	if !strings.HasPrefix(path, "/v1/chat/completions") {
		return body
	}

	// 2. 空 body 直接返回
	if len(body) == 0 {
		return body
	}

	// 3. 解析 JSON，检查是否为流式请求
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		logger.Warn("failed to parse request body for stream_options injection, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body
	}

	// 4. 检查 stream 字段
	stream, ok := req["stream"].(bool)
	if !ok || !stream {
		// 非流式请求，无需注入
		return body
	}

	// 5. 检查是否已有 stream_options.include_usage: true（幂等）
	if streamOpts, ok := req["stream_options"].(map[string]interface{}); ok {
		if includeUsage, ok := streamOpts["include_usage"].(bool); ok && includeUsage {
			// 已经设置为 true，无需重复注入
			logger.Debug("stream_options.include_usage already true, skipping injection",
				zap.String("request_id", reqID),
			)
			return body
		}
	}

	// 6. 注入 stream_options
	if req["stream_options"] == nil {
		req["stream_options"] = make(map[string]interface{})
	}
	streamOpts, ok := req["stream_options"].(map[string]interface{})
	if !ok {
		// stream_options 类型异常，降级
		logger.Warn("stream_options field has unexpected type, skipping injection",
			zap.String("request_id", reqID),
		)
		return body
	}
	streamOpts["include_usage"] = true

	// 7. 重新序列化
	modified, err := json.Marshal(req)
	if err != nil {
		logger.Warn("failed to marshal modified request body, forwarding original",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		return body
	}

	logger.Debug("injected stream_options.include_usage for OpenAI streaming request",
		zap.String("request_id", reqID),
		zap.Int("original_size", len(body)),
		zap.Int("modified_size", len(modified)),
	)

	return modified
}
