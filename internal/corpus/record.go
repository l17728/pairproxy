package corpus

import "time"

// Message 是 OpenAI messages 格式的单条消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Record 是写入 JSONL 文件的单条语料记录。
type Record struct {
	ID             string    `json:"id"`                        // "cr_<unix>_<4hex>"
	Timestamp      time.Time `json:"ts"`                        // UTC
	Instance       string    `json:"instance"`                  // sproxy 实例标识
	User           string    `json:"user"`                      // 用户名
	Group          string    `json:"group,omitempty"`            // 分组名
	ModelRequested string    `json:"model_requested"`           // 客户端请求的模型
	ModelActual    string    `json:"model_actual,omitempty"`    // LLM 实际使用的模型
	Target         string    `json:"target"`                    // 后端 LLM URL
	Provider       string    `json:"provider"`                  // "anthropic" | "openai" | "ollama"
	Messages       []Message `json:"messages"`                  // 完整对话（含 assistant 回复）
	InputTokens    int       `json:"input_tokens"`              // 输入 token 数
	OutputTokens   int       `json:"output_tokens"`             // 输出 token 数
	DurationMs     int64     `json:"duration_ms"`               // 请求耗时（毫秒）
}
