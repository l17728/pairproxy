package tap

import (
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// TeeResponseWriter 包装 http.ResponseWriter，在不缓冲的情况下同时：
//  1. 将字节原样转发给原始 Writer（客户端）
//  2. 将字节 Feed 给 ResponseParser 解析 token 用量（支持 Anthropic / OpenAI / Ollama）
//
// 当 streaming 响应结束时，异步调用 UsageWriter.Record()。
// 对于非 streaming 响应，调用方需手动调用 RecordNonStreaming()。
type TeeResponseWriter struct {
	http.ResponseWriter // 原始 writer（透传 Header() 等方法）

	logger      *zap.Logger
	parser      ResponseParser
	writer      *db.UsageWriter
	record      db.UsageRecord // 预填充的 UsageRecord 模板（requestID、userID 等）
	statusCode  int
	isStreaming bool
}

// NewTeeResponseWriter 创建 TeeResponseWriter。
// record 应预填充 RequestID、UserID、Model、UpstreamURL、SourceNode 等字段；
// InputTokens/OutputTokens/StatusCode/DurationMs 将由 Tee 在流结束时填写。
// provider 指定 LLM provider（"anthropic"、"openai"、"ollama"），空字符串默认 Anthropic。
func NewTeeResponseWriter(
	w http.ResponseWriter,
	logger *zap.Logger,
	usageWriter *db.UsageWriter,
	record db.UsageRecord,
	provider string,
) *TeeResponseWriter {
	tw := &TeeResponseWriter{
		ResponseWriter: w,
		logger:         logger.Named("tee_writer"),
		writer:         usageWriter,
		record:         record,
		statusCode:     http.StatusOK,
	}

	// 按 provider 创建解析器，注册 SSE 解析完成回调
	tw.parser = NewResponseParser(provider, func(inputTokens, outputTokens int) {
		tw.logger.Debug("streaming token usage captured",
			zap.String("request_id", record.RequestID),
			zap.String("user_id", record.UserID),
			zap.String("provider", provider),
			zap.Int("input_tokens", inputTokens),
			zap.Int("output_tokens", outputTokens),
		)
		r := tw.record
		r.InputTokens = inputTokens
		r.OutputTokens = outputTokens
		r.StatusCode = tw.statusCode
		r.IsStreaming = true
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now()
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
	r.OutputTokens = out
	r.StatusCode = statusCode
	r.IsStreaming = false
	r.DurationMs = durationMs
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	tw.writer.Record(r)
}

// StatusCode 返回记录的 HTTP 状态码（WriteHeader 调用后有效）。
func (tw *TeeResponseWriter) StatusCode() int {
	return tw.statusCode
}

// UpdateModel 补充 usage record 中的模型字段（在 Director 之后获取 body 时调用）。
func (tw *TeeResponseWriter) UpdateModel(model string) {
	tw.record.Model = model
}
