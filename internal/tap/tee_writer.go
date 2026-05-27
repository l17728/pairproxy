package tap

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/tokenizer"
)

// TeeResponseWriter 包装 http.ResponseWriter，在不缓冲的情况下同时：
//  1. 将字节原样转发给原始 Writer（客户端）
//  2. 将字节 Feed 给 ResponseParser 解析 token 用量（支持 Anthropic / OpenAI / Ollama）
//
// 当 streaming 响应结束时，异步调用 UsageWriter.Record()。
// 对于非 streaming 响应，调用方需手动调用 RecordNonStreaming()。
type TeeResponseWriter struct {
	http.ResponseWriter // 原始 writer（透传 Header() 等方法）

	logger            *zap.Logger
	parser            ResponseParser
	writer            *db.UsageWriter
	record            db.UsageRecord // 预填充的 UsageRecord 模板（requestID、userID 等）
	statusCode        int
	isStreaming       bool
	streamingRecorded bool // onComplete 回调已触发（message_stop 已收到）

	// 失败响应 token 估算所需的请求上下文
	requestBody []byte // 原始请求体，仅在响应失败且 input_tokens=0 时使用
	requestPath string // 请求路径，用于判断 Anthropic/OpenAI 格式

	// debug 支持
	startTime     time.Time // 请求开始时间，用于计算 TTFB
	firstByteAt   time.Time // 第一个字节到达时间
	firstByteOnce sync.Once
	onChunk       func(p []byte) // debug 回调：每个 streaming chunk 调用，nil = 不调试
}

// NewTeeResponseWriter 创建 TeeResponseWriter。
// record 应预填充 RequestID、UserID、Model、UpstreamURL、SourceNode 等字段；
// InputTokens/OutputTokens/StatusCode/DurationMs 将由 Tee 在流结束时填写。
// provider 指定 LLM provider（"anthropic"、"openai"、"ollama"），空字符串默认 Anthropic。
// startTime 为请求开始时间，用于计算 TTFB；onChunk 为 debug chunk 回调（nil = 不启用）。
func NewTeeResponseWriter(
	w http.ResponseWriter,
	logger *zap.Logger,
	usageWriter *db.UsageWriter,
	record db.UsageRecord,
	provider string,
	startTime time.Time,
	onChunk func([]byte),
	requestBody []byte,
	requestPath string,
) *TeeResponseWriter {
	tw := &TeeResponseWriter{
		ResponseWriter: w,
		logger:         logger.Named("tee_writer"),
		writer:         usageWriter,
		record:         record,
		statusCode:     http.StatusOK,
		startTime:      startTime,
		onChunk:        onChunk,
		requestBody:    requestBody,
		requestPath:    requestPath,
	}

	// 按 provider 创建解析器，注册 SSE 解析完成回调
	tw.parser = NewResponseParser(provider, func(inputTokens, outputTokens int) {
		tw.streamingRecorded = true
		r := tw.record
		r.InputTokens = inputTokens
		r.OutputTokens = outputTokens
		r.IsStreaming = true
		if !tw.startTime.IsZero() {
			r.DurationMs = time.Since(tw.startTime).Milliseconds()
		}
		// TTFT: first byte time ≈ first token time for SSE streams
		if !tw.startTime.IsZero() && !tw.firstByteAt.IsZero() {
			r.TtftMs = tw.firstByteAt.Sub(tw.startTime).Milliseconds()
		}
		// TPOT: decode phase duration divided by output token count
		if outputTokens > 0 {
			if decodeMs := float64(r.DurationMs - r.TtftMs); decodeMs > 0 {
				r.TpotMs = decodeMs / float64(outputTokens)
			}
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}

		// SSE error 事件：修正状态码并填充 ErrorBody。
		// HTTP 层状态码是 200（SSE 头已发出），但流内发生了错误，需要用 502 标记。
		if tw.parser.HasError() {
			r.StatusCode = http.StatusBadGateway
			if r.InputTokens == 0 {
				r.InputTokens = tokenizer.EstimateInputTokens(tw.requestBody, tw.requestPath)
			}
			if errData := tw.parser.SSEErrorData(); errData != "" {
				r.ErrorBody = errData
			}
			tw.logger.Warn("SSE error event received, recording as 502",
				zap.String("request_id", record.RequestID),
				zap.String("user_id", record.UserID),
				zap.String("error_data", tw.parser.SSEErrorData()),
			)
		} else {
			r.StatusCode = tw.statusCode
			tw.logger.Debug("streaming token usage captured",
				zap.String("request_id", record.RequestID),
				zap.String("user_id", record.UserID),
				zap.String("provider", provider),
				zap.Int("input_tokens", inputTokens),
				zap.Int("output_tokens", outputTokens),
			)
		}

		usageWriter.Record(r)
	})

	return tw
}

// WriteHeader 记录状态码并透传。
func (tw *TeeResponseWriter) WriteHeader(statusCode int) {
	tw.statusCode = statusCode
	tw.ResponseWriter.WriteHeader(statusCode)
}

// Write 同时写入原始 writer 并 Feed 给 SSE 解析器。
// 实现 http.ResponseWriter.Write。
func (tw *TeeResponseWriter) Write(p []byte) (int, error) {
	// 记录第一个字节到达时间（TTFB）
	tw.firstByteOnce.Do(func() { tw.firstByteAt = time.Now() })

	// 先写给客户端（不阻塞流传输）
	n, err := tw.ResponseWriter.Write(p)
	if err != nil {
		tw.logger.Warn("failed to write to client",
			zap.String("request_id", tw.record.RequestID),
			zap.Int("bytes_attempted", len(p)),
			zap.Int("bytes_written", n),
			zap.Error(err),
		)
	}

	// 判断是否 streaming（响应第一次写入时检测 Content-Type）
	if !tw.isStreaming {
		ct := tw.ResponseWriter.Header().Get("Content-Type")
		tw.isStreaming = strings.Contains(ct, "text/event-stream")
	}

	// 只有 streaming 响应才 Feed 给 SSE 解析器
	if tw.isStreaming && n > 0 {
		tw.parser.Feed(p[:n])
		// debug 回调：每条 SSE chunk 实时通知
		if tw.onChunk != nil {
			tw.onChunk(p[:n])
		}
	}

	return n, err
}

// Flush 透传 Flush 调用（SSE 流必须立即刷新）。
// 实现 http.Flusher 接口。
func (tw *TeeResponseWriter) Flush() {
	if f, ok := tw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RecordNonStreaming 用于非 streaming 响应：解析完整 body，记录 usage。
// 调用方（sproxy.go 的 ModifyResponse 钩子）在读取完 body 后调用此方法。
// 使用与当前 provider 匹配的解析器（Anthropic / OpenAI / Ollama）。
func (tw *TeeResponseWriter) RecordNonStreaming(body []byte, statusCode int, durationMs int64) {
	in, out := tw.parser.ParseNonStreaming(body)
	tw.logger.Debug("non-streaming token usage captured",
		zap.String("request_id", tw.record.RequestID),
		zap.String("user_id", tw.record.UserID),
		zap.Int("input_tokens", in),
		zap.Int("output_tokens", out),
		zap.Int("status_code", statusCode),
		zap.Int64("duration_ms", durationMs),
	)
	r := tw.record
	r.InputTokens = in
	// 仅在失败响应且上游未返回 token 数时，按需估算 input tokens
	if r.InputTokens == 0 && statusCode >= 400 {
		r.InputTokens = tokenizer.EstimateInputTokens(tw.requestBody, tw.requestPath)
	}
	r.OutputTokens = out
	r.StatusCode = statusCode
	r.IsStreaming = false
	r.DurationMs = durationMs
	// Non-streaming: TTFT not applicable; TPOT = total duration / output tokens
	if out > 0 && durationMs > 0 {
		r.TpotMs = float64(durationMs) / float64(out)
	}
	if statusCode >= 400 && len(body) > 0 {
		const maxErrorBody = 1024
		if len(body) > maxErrorBody {
			r.ErrorBody = string(body[:maxErrorBody])
		} else {
			r.ErrorBody = string(body)
		}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	tw.writer.Record(r)
}

// FlushPartialTokens 在 streaming 响应被中断（连接断开、上游提前关闭等）时，
// 将已解析的 input_tokens 兜底写入 UsageWriter。
// 仅在以下所有条件同时满足时写入：
//  1. 响应确实是 streaming（Content-Type: text/event-stream）
//  2. onComplete 回调尚未触发（message_stop 未收到）
//  3. 解析器已捕获到 input_tokens > 0（message_start 已收到）
//
// 调用方（serveProxy）在 proxy.ServeHTTP 返回后调用此方法，确保部分 token 不丢失。
func (tw *TeeResponseWriter) FlushPartialTokens(statusCode int, durationMs int64) {
	if !tw.isStreaming || tw.streamingRecorded {
		return
	}
	inputTokens := tw.parser.InputTokens()
	if inputTokens == 0 {
		return
	}
	tw.logger.Warn("streaming interrupted before message_stop, flushing partial tokens",
		zap.String("request_id", tw.record.RequestID),
		zap.String("user_id", tw.record.UserID),
		zap.Int("input_tokens", inputTokens),
		zap.Int("output_tokens", tw.parser.OutputTokens()),
		zap.Int("status_code", statusCode),
	)
	r := tw.record
	r.InputTokens = inputTokens
	r.OutputTokens = tw.parser.OutputTokens()
	r.StatusCode = statusCode
	r.IsStreaming = true
	r.DurationMs = durationMs
	if !tw.startTime.IsZero() && !tw.firstByteAt.IsZero() {
		r.TtftMs = tw.firstByteAt.Sub(tw.startTime).Milliseconds()
	}
	if r.OutputTokens > 0 {
		if decodeMs := float64(durationMs - r.TtftMs); decodeMs > 0 {
			r.TpotMs = decodeMs / float64(r.OutputTokens)
		}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	tw.writer.Record(r)
	tw.streamingRecorded = true // 防止重复写入
}

// TTFBMs 返回首字节时延（毫秒）。在 Write() 被调用后有效，否则返回 0。
func (tw *TeeResponseWriter) TTFBMs() int64 {
	if tw.firstByteAt.IsZero() || tw.startTime.IsZero() {
		return 0
	}
	return tw.firstByteAt.Sub(tw.startTime).Milliseconds()
}

// StatusCode 返回记录的 HTTP 状态码（WriteHeader 调用后有效）。
func (tw *TeeResponseWriter) StatusCode() int {
	return tw.statusCode
}

// UpdateModel 补充 usage record 中的模型字段（在 Director 之后获取 body 时调用）。
func (tw *TeeResponseWriter) UpdateModel(model string) {
	tw.record.Model = model
}
