package corpus

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Collector 单次代理请求的语料采集器。
// 线程安全：FeedChunk / SetNonStreamingResponse / Finish 可在不同 goroutine 调用。
type Collector struct {
	writer   *Writer
	logger   *zap.Logger
	record   Record
	provider string

	mu           sync.Mutex
	textBuf      strings.Builder // 流式响应文本累积
	modelFound   bool            // model_actual 已提取
	finished     bool            // 已提交到 writer
	inputTokens  int             // 从 SSE 事件解析
	outputTokens int             // 从 SSE 事件解析

	// 质量过滤
	minOutputTokens int
	excludeGroups   map[string]struct{}
}

// NewCollector 创建单请求采集器。
func NewCollector(
	w *Writer,
	instance, reqID, user, group, modelRequested, target, provider string,
	requestBody []byte,
	startTime time.Time,
) *Collector {
	c := &Collector{
		writer:          w,
		logger:          w.logger,
		provider:        provider,
		minOutputTokens: w.MinOutputTokens,
	}

	// 构建排除分组 set
	if len(w.ExcludeGroups) > 0 {
		c.excludeGroups = make(map[string]struct{}, len(w.ExcludeGroups))
		for _, g := range w.ExcludeGroups {
			c.excludeGroups[g] = struct{}{}
		}
	}

	// 生成记录 ID
	var rnd [2]byte
	_, _ = rand.Read(rnd[:])
	id := fmt.Sprintf("cr_%d_%s", startTime.Unix(), hex.EncodeToString(rnd[:]))

	c.record = Record{
		ID:             id,
		Timestamp:      startTime.UTC(),
		Instance:       instance,
		User:           user,
		Group:          group,
		ModelRequested: modelRequested,
		Target:         target,
		Provider:       provider,
	}

	// 从请求 body 提取输入 messages
	c.record.Messages = extractMessages(requestBody)

	c.logger.Debug("corpus collector created",
		zap.String("id", id),
		zap.String("user", user),
		zap.String("model", modelRequested),
		zap.String("provider", provider),
	)
	return c
}

// FeedChunk 处理一个 SSE chunk（流式响应）。
// 提取文本增量和 model_actual / token 信息。
func (c *Collector) FeedChunk(chunk []byte) {
	lines := bytes.Split(chunk, []byte("\n"))

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || string(payload) == "[DONE]" {
			continue
		}

		switch c.provider {
		case "openai", "ollama":
			c.feedOpenAIChunk(payload)
		default: // anthropic
			c.feedAnthropicChunk(payload)
		}
	}
}

func (c *Collector) feedAnthropicChunk(payload []byte) {
	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if !c.modelFound && event.Message.Model != "" {
				c.record.ModelActual = event.Message.Model
				c.modelFound = true
			}
			if event.Message.Usage != nil {
				c.inputTokens = event.Message.Usage.InputTokens +
					event.Message.Usage.CacheReadInputTokens +
					event.Message.Usage.CacheCreationInputTokens
			}
		}
	case "content_block_delta":
		if event.Delta != nil && event.Delta.Type == "text_delta" {
			c.textBuf.WriteString(event.Delta.Text)
		}
	case "message_delta":
		if event.Usage != nil {
			c.outputTokens = event.Usage.OutputTokens
		}
	}
}

func (c *Collector) feedOpenAIChunk(payload []byte) {
	var chunk struct {
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return
	}

	if !c.modelFound && chunk.Model != "" {
		c.record.ModelActual = chunk.Model
		c.modelFound = true
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != "" {
			c.textBuf.WriteString(ch.Delta.Content)
		}
	}
	if chunk.Usage != nil {
		c.inputTokens = chunk.Usage.PromptTokens
		c.outputTokens = chunk.Usage.CompletionTokens
	}
}

// SetNonStreamingResponse 处理非流式完整响应 body。
func (c *Collector) SetNonStreamingResponse(body []byte) {
	if len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.provider {
	case "openai", "ollama":
		c.parseNonStreamingOpenAI(body)
	default:
		c.parseNonStreamingAnthropic(body)
	}
}

func (c *Collector) parseNonStreamingAnthropic(body []byte) {
	var resp struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	if !c.modelFound && resp.Model != "" {
		c.record.ModelActual = resp.Model
		c.modelFound = true
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	c.textBuf.WriteString(sb.String())
	if resp.Usage != nil {
		c.inputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
		c.outputTokens = resp.Usage.OutputTokens
	}
}

func (c *Collector) parseNonStreamingOpenAI(body []byte) {
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	if !c.modelFound && resp.Model != "" {
		c.record.ModelActual = resp.Model
		c.modelFound = true
	}
	if len(resp.Choices) > 0 {
		c.textBuf.WriteString(resp.Choices[0].Message.Content)
	}
	if resp.Usage != nil {
		c.inputTokens = resp.Usage.PromptTokens
		c.outputTokens = resp.Usage.CompletionTokens
	}
}

// Finish 应用质量过滤后提交记录到 Writer channel。幂等。
func (c *Collector) Finish(statusCode int, durationMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.finished {
		return
	}
	c.finished = true

	// 质量过滤：错误响应
	if statusCode >= 400 {
		c.logger.Debug("corpus record filtered: error status",
			zap.String("id", c.record.ID),
			zap.Int("status", statusCode),
		)
		return
	}
	// 质量过滤：排除分组
	if _, excluded := c.excludeGroups[c.record.Group]; excluded {
		c.logger.Debug("corpus record filtered: excluded group",
			zap.String("id", c.record.ID),
			zap.String("group", c.record.Group),
		)
		return
	}
	// 质量过滤：输出 token 不足
	if c.minOutputTokens > 0 && c.outputTokens < c.minOutputTokens {
		c.logger.Debug("corpus record filtered: insufficient output tokens",
			zap.String("id", c.record.ID),
			zap.Int("output_tokens", c.outputTokens),
			zap.Int("min_required", c.minOutputTokens),
		)
		return
	}

	// 组装最终记录
	assistantText := c.textBuf.String()
	if assistantText == "" {
		c.logger.Debug("corpus record filtered: empty assistant text",
			zap.String("id", c.record.ID),
		)
		return // 无有效输出
	}

	c.record.Messages = append(c.record.Messages, Message{
		Role:    "assistant",
		Content: assistantText,
	})
	c.record.InputTokens = c.inputTokens
	c.record.OutputTokens = c.outputTokens
	c.record.DurationMs = durationMs

	c.writer.Submit(c.record)
	c.logger.Info("corpus record submitted",
		zap.String("id", c.record.ID),
		zap.String("user", c.record.User),
		zap.String("model_requested", c.record.ModelRequested),
		zap.String("model_actual", c.record.ModelActual),
		zap.Int("output_tokens", c.outputTokens),
	)
}

// ---------------------------------------------------------------------------
// 请求内容提取（与 track/capture.go 相同逻辑，独立实现避免包间耦合）
// ---------------------------------------------------------------------------

func extractMessages(body []byte) []Message {
	if len(body) == 0 {
		return nil
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	msgs := make([]Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, Message{Role: m.Role, Content: contentToString(m.Content)})
	}
	return msgs
}

func contentToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}
