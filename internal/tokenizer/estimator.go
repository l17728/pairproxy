package tokenizer

import (
	"encoding/json"
	"strings"
	"sync"
	"unicode"
)

// cl100k_base 是 GLM-4/5 词表的基础编码，与 GPT-4 共享。
// GLM-5 在此基础上扩展了中文 token，但 cl100k_base 对中文的误差约 ±5-10%，
// 对英文几乎完全准确，足以用于失败请求的 input token 估算。

var (
	enc     tiktokenEncoder
	encOnce sync.Once
	encErr  error
)

// tiktokenEncoder 接口抽象底层编码器，便于测试注入。
type tiktokenEncoder interface {
	Encode(text string, allowedSpecial, disallowedSpecial []string) []int
}

func getEncoder() (tiktokenEncoder, error) {
	encOnce.Do(func() {
		enc, encErr = loadCL100K()
	})
	return enc, encErr
}

// EstimateInputTokens 从请求体中提取文本内容并用 cl100k_base 计算 token 数。
// path 用于判断请求格式（/v1/messages → Anthropic，/v1/chat/completions → OpenAI）。
// 若 tiktoken 初始化失败（离线环境、首次启动未缓存），自动降级为字符数估算。
func EstimateInputTokens(body []byte, path string) int {
	if len(body) == 0 {
		return 0
	}

	var texts []string
	if strings.Contains(path, "/messages") {
		texts = extractAnthropicTexts(body)
	} else {
		texts = extractOpenAITexts(body)
	}
	if len(texts) == 0 {
		return 0
	}

	e, err := getEncoder()
	if err != nil {
		return charBasedEstimate(texts)
	}

	total := 0
	for _, t := range texts {
		total += len(e.Encode(t, nil, nil))
	}
	// 每条 message 固定开销 4 token（角色标记 + 分隔符），回复起始 +3
	total += len(texts)*4 + 3
	return total
}

// ── Anthropic 格式解析 (/v1/messages) ────────────────────────────────────────

type anthropicRequest struct {
	System   json.RawMessage  `json:"system"`
	Messages []anthropicMsg   `json:"messages"`
}

type anthropicMsg struct {
	Content json.RawMessage `json:"content"`
}

func extractAnthropicTexts(body []byte) []string {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	var texts []string
	if s := extractRawText(req.System); s != "" {
		texts = append(texts, s)
	}
	for _, m := range req.Messages {
		if t := extractRawText(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	return texts
}

// ── OpenAI 格式解析 (/v1/chat/completions) ───────────────────────────────────

type openAIRequest struct {
	Messages []openAIMsg `json:"messages"`
}

type openAIMsg struct {
	Content json.RawMessage `json:"content"`
}

func extractOpenAITexts(body []byte) []string {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	var texts []string
	for _, m := range req.Messages {
		if t := extractRawText(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	return texts
}

// ── content 字段提取（string 或 []part 两种形式）────────────────────────────

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractRawText 将 content 字段（可能是字符串或 part 数组）转为纯文本。
// 图片等非文本 part 跳过。
func extractRawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 尝试直接作为字符串解析
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// 尝试作为 content part 数组解析
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// ── 降级：字符数估算 ─────────────────────────────────────────────────────────

// charBasedEstimate 在 tiktoken 不可用时的兜底估算。
// CJK 字符在 cl100k_base 中通常 1 字符 = 1 token；
// ASCII 约 4 字符 = 1 token；其他 Unicode 约 2 字符 = 1 token。
func charBasedEstimate(texts []string) int {
	var cjk, ascii, other int
	for _, t := range texts {
		for _, r := range t {
			switch {
			case isCJK(r):
				cjk++
			case r <= unicode.MaxASCII:
				ascii++
			default:
				other++
			}
		}
	}
	return cjk + ascii/4 + other/2 + 1
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一汉字
		(r >= 0x3400 && r <= 0x4DBF) || // CJK 扩展 A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK 扩展 B
		(r >= 0xF900 && r <= 0xFAFF) || // CJK 兼容汉字
		(r >= 0x2E80 && r <= 0x2EFF) // CJK 部首
}
