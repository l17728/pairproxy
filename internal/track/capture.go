package track

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 对话记录 JSON 格式
// ---------------------------------------------------------------------------

// Message 代表对话中的单条消息（用户/助手/系统）。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"` // 纯文本内容（已从 content block 展开）
}

// ConversationRecord 是写入磁盘的对话记录 JSON 结构。
type ConversationRecord struct {
	RequestID    string    `json:"request_id"`
	Username     string    `json:"username"`
	Timestamp    time.Time `json:"timestamp"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model,omitempty"`
	Messages     []Message `json:"messages"`              // 用户请求中的消息列表
	Response     string    `json:"response"`              // 助手回复（合并后的文本）
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
}

// ---------------------------------------------------------------------------
// CaptureSession：单次请求的对话内容捕获会话
// ---------------------------------------------------------------------------

// CaptureSession 捕获一次代理请求的输入消息和输出响应，在请求结束时写入磁盘。
// 零值不可用，必须通过 NewCaptureSession 创建。
// 线程安全：FeedChunk / SetNonStreamingResponse / Flush 可在不同 goroutine 调用。
type CaptureSession struct {
	dir      string // 该用户的对话目录（<track_dir>/conversations/<username>）
	record   ConversationRecord
	provider string

	mu      sync.Mutex
	textBuf strings.Builder // 流式响应文本累积
	flushed bool            // 已写入磁盘，防止重复写入
}

// NewCaptureSession 创建捕获会话。
//   - convDir: 该用户的对话记录目录（由 Tracker.UserConvDir 提供）
//   - reqID: 请求 ID
//   - username: 用户名
//   - requestBody: 原始请求 JSON body（用于提取 messages）
//   - provider: "anthropic" | "openai" | "ollama" | ""
func NewCaptureSession(convDir, reqID, username string, requestBody []byte, provider string) *CaptureSession {
	cs := &CaptureSession{
		dir:      convDir,
		provider: provider,
		record: ConversationRecord{
			RequestID: reqID,
			Username:  username,
			Timestamp: time.Now().UTC(),
			Provider:  provider,
		},
	}
	cs.record.Messages = extractMessages(requestBody)
	cs.record.Model = extractModel(requestBody)
	return cs
}

// FeedChunk 处理一个 SSE chunk（流式响应时由 onChunk 回调调用）。
// 从 chunk 中提取文本增量并累积。当检测到流结束信号时自动 Flush。
func (cs *CaptureSession) FeedChunk(chunk []byte) {
	text, done := extractSSEText(chunk, cs.provider)

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if text != "" {
		cs.textBuf.WriteString(text)
	}
	if done && !cs.flushed {
		cs.record.Response = cs.textBuf.String()
		cs.doFlush()
	}
}

// SetNonStreamingResponse 设置非流式响应内容（在 ModifyResponse 中调用）。
// 调用后需手动调用 Flush。token 数将从响应 body 中自动解析。
func (cs *CaptureSession) SetNonStreamingResponse(body []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.record.Response = extractNonStreamingText(body, cs.provider)
	cs.record.InputTokens, cs.record.OutputTokens = extractNonStreamingTokens(body, cs.provider)
}

// Flush 将对话记录写入磁盘。幂等，多次调用只写一次。
// 非流式响应需要在 SetNonStreamingResponse 之后显式调用。
func (cs *CaptureSession) Flush() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.flushed {
		return
	}
	// 仅当 Response 未被 SetNonStreamingResponse 设置时，才从 textBuf 填充
	// （流式场景下 FeedChunk 已在 message_stop 时触发 doFlush，此处保底处理未完成的流）
	if cs.record.Response == "" && cs.textBuf.Len() > 0 {
		cs.record.Response = cs.textBuf.String()
	}
	cs.doFlush()
}

// doFlush 在持有 mu 的情况下写入磁盘（内部方法）。
func (cs *CaptureSession) doFlush() {
	cs.flushed = true

	data, err := json.MarshalIndent(cs.record, "", "  ")
	if err != nil {
		log.Printf("[track] failed to marshal conversation record: %v", err)
		return
	}

	// 文件名：<timestamp>-<reqID>.json（用 UTC RFC3339 保证排序友好）
	ts := cs.record.Timestamp.Format("2006-01-02T15-04-05Z")
	filename := fmt.Sprintf("%s-%s.json", ts, cs.record.RequestID)
	path := filepath.Join(cs.dir, filename)

	// 确保目录存在（用户目录可能在 Enable 之后还未 Flush 过）
	if err := os.MkdirAll(cs.dir, 0o777); err != nil {
		log.Printf("[track] failed to create conversation dir %q: %v", cs.dir, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[track] failed to write conversation file %q: %v", path, err)
		return
	}
	log.Printf("[track] conversation saved: %s", path)
}

// ---------------------------------------------------------------------------
// SSE 文本提取
// ---------------------------------------------------------------------------

// extractSSEText 从一个 SSE chunk（可能包含多行）中提取助手文本增量。
// 返回 (text, done)：text 为本 chunk 提取到的文本；done 为流结束信号。
func extractSSEText(chunk []byte, provider string) (text string, done bool) {
	lines := bytes.Split(chunk, []byte("\n"))
	var sb strings.Builder

	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 {
			continue
		}
		if string(payload) == "[DONE]" {
			done = true
			continue
		}

		switch provider {
		case "openai", "ollama":
			t, isDone := extractOpenAIChunkText(payload)
			sb.WriteString(t)
			if isDone {
				done = true
			}
		default: // anthropic
			t, isDone := extractAnthropicChunkText(payload)
			sb.WriteString(t)
			if isDone {
				done = true
			}
		}
	}
	return sb.String(), done
}

// extractAnthropicChunkText 从 Anthropic SSE data payload 提取文本增量。
func extractAnthropicChunkText(payload []byte) (text string, done bool) {
	var event struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", false
	}
	switch event.Type {
	case "content_block_delta":
		if event.Delta != nil && event.Delta.Type == "text_delta" {
			return event.Delta.Text, false
		}
	case "message_stop":
		return "", true
	}
	return "", false
}

// extractOpenAIChunkText 从 OpenAI SSE data payload 提取文本增量。
func extractOpenAIChunkText(payload []byte) (text string, done bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return "", false
	}
	var sb strings.Builder
	for _, c := range chunk.Choices {
		if c.Delta.Content != "" {
			sb.WriteString(c.Delta.Content)
		}
	}
	return sb.String(), false
}

// ---------------------------------------------------------------------------
// 请求/响应内容提取
// ---------------------------------------------------------------------------

// extractMessages 从请求 body 提取消息列表（Anthropic 和 OpenAI 格式兼容）。
func extractMessages(body []byte) []Message {
	if len(body) == 0 {
		return nil
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"` // string 或 []block
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	messages := make([]Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		content := contentToString(m.Content)
		messages = append(messages, Message{Role: m.Role, Content: content})
	}
	return messages
}

// extractModel 从请求 body 提取模型名称。
func extractModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck
	return req.Model
}

// extractNonStreamingText 从非流式响应 body 提取助手文本。
func extractNonStreamingText(body []byte, provider string) string {
	if len(body) == 0 {
		return ""
	}
	switch provider {
	case "openai", "ollama":
		var resp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && len(resp.Choices) > 0 {
			return resp.Choices[0].Message.Content
		}
	default: // anthropic
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &resp); err == nil {
			var sb strings.Builder
			for _, block := range resp.Content {
				if block.Type == "text" {
					sb.WriteString(block.Text)
				}
			}
			return sb.String()
		}
	}
	return ""
}

// extractNonStreamingTokens 从非流式响应 body 提取 token 数量。
func extractNonStreamingTokens(body []byte, provider string) (inputTokens, outputTokens int) {
	if len(body) == 0 {
		return 0, 0
	}
	switch provider {
	case "openai", "ollama":
		var resp struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		}
	default: // anthropic
		var resp struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			return resp.Usage.InputTokens, resp.Usage.OutputTokens
		}
	}
	return 0, 0
}

// contentToString 将 message content 字段（string 或 content block 数组）展开为纯文本。
func contentToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 尝试解析为字符串
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// 尝试解析为 content block 数组（Anthropic 格式）
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
