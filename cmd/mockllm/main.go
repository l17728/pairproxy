// mockllm — 轻量级 Anthropic API mock 服务端，用于本地链路测试。
//
// 行为：接收 POST /v1/messages，将请求中最后一条 user 消息的内容
// 原文回显为响应（streaming 或 non-streaming），不做任何变换。
//
// 典型部署链路：
//
//	mockagent → cproxy(:8080) → sproxy(:9000) → mockllm(:11434)
//
// sproxy.yaml 中配置：
//
//	llm:
//	  targets:
//	    - url: "http://localhost:11434"
//	      api_key: "mock-key"
//	      provider: "anthropic"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

var (
	flagAddr    = flag.String("addr", ":11434", "监听地址")
	flagDelay   = flag.Duration("delay", 0, "每个 SSE chunk 发送前的人工延迟（模拟真实 LLM 延迟）")
	flagChunks  = flag.Int("chunks", 1, "将回显内容分成几个 SSE chunk 发送（>=1）")
	flagVerbose = flag.Bool("v", false, "详细日志")
)

// ---------------------------------------------------------------------------
// 请求解析
// ---------------------------------------------------------------------------

type messagesReq struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []msgItem `json:"messages"`
	Stream    bool      `json:"stream"`
}

type msgItem struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string 或 []contentBlock
}

// extractUserContent 从最后一条 user 消息中提取纯文本内容。
func extractUserContent(msgs []msgItem) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		switch v := msgs[i].Content.(type) {
		case string:
			return v
		case []any:
			// content block 格式：[{"type":"text","text":"..."}]
			for _, item := range v {
				if block, ok := item.(map[string]any); ok {
					if text, ok := block["text"].(string); ok {
						return text
					}
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// HTTP 处理
// ---------------------------------------------------------------------------

func handleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"read_error"}`, http.StatusBadRequest)
		return
	}

	var req messagesReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}

	content := extractUserContent(req.Messages)
	msgID := fmt.Sprintf("msg_%016x", rand.Uint64())

	if *flagVerbose {
		log.Printf("← %s stream=%v model=%s content=%q",
			r.RemoteAddr, req.Stream, req.Model, trunc(content, 80))
	}

	if req.Stream {
		serveSSE(w, msgID, content, req.Model)
	} else {
		serveJSON(w, msgID, content, req.Model)
	}
}

// ---------------------------------------------------------------------------
// SSE 回显
// ---------------------------------------------------------------------------

func serveSSE(w http.ResponseWriter, msgID, content, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	f, ok := w.(http.Flusher)
	if !ok {
		return
	}

	inputTokens := estimateTokens(content)

	// message_start
	sseEvent(w, f, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant",
			"content": []any{}, "model": model, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})

	// content_block_start
	sseEvent(w, f, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	// ping
	sseEvent(w, f, "ping", map[string]any{"type": "ping"})

	// content_block_delta（按 --chunks 分片）
	chunks := splitIntoChunks(content, max(*flagChunks, 1))
	for _, chunk := range chunks {
		if *flagDelay > 0 {
			time.Sleep(*flagDelay)
		}
		sseEvent(w, f, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": chunk},
		})
	}

	// content_block_stop
	sseEvent(w, f, "content_block_stop", map[string]any{
		"type": "content_block_stop", "index": 0,
	})

	// message_delta
	sseEvent(w, f, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": estimateTokens(content)},
	})

	// message_stop
	sseEvent(w, f, "message_stop", map[string]any{"type": "message_stop"})

	if *flagVerbose {
		log.Printf("→ %s msgID=%s chunks=%d content=%q",
			"streaming", msgID, len(chunks), trunc(content, 80))
	}
}

func sseEvent(w io.Writer, f http.Flusher, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	f.Flush()
}

// ---------------------------------------------------------------------------
// JSON 回显
// ---------------------------------------------------------------------------

func serveJSON(w http.ResponseWriter, msgID, content, model string) {
	resp := map[string]any{
		"id": msgID, "type": "message", "role": "assistant",
		"content": []map[string]any{{"type": "text", "text": content}},
		"model":   model, "stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  estimateTokens(content),
			"output_tokens": estimateTokens(content),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck

	if *flagVerbose {
		log.Printf("→ json msgID=%s content=%q", msgID, trunc(content, 80))
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// splitIntoChunks 将字符串尽量均分为 n 段（按 rune 分割）。
func splitIntoChunks(s string, n int) []string {
	runes := []rune(s)
	if n <= 1 || len(runes) == 0 {
		return []string{s}
	}
	size := (len(runes) + n - 1) / n
	var result []string
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		result = append(result, string(runes[i:end]))
	}
	return result
}

// estimateTokens 粗略估算 token 数（按空格分词，最少 1）。
func estimateTokens(s string) int {
	n := len(strings.Fields(s)) + 1
	if n < 1 {
		return 1
	}
	return n
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	flag.Usage = func() {
		fmt.Println(`mockllm — Anthropic API echo server for proxy chain testing

Usage:
  mockllm [flags]

Flags:`)
		flag.PrintDefaults()
		fmt.Println(`
sproxy.yaml example:
  llm:
    targets:
      - url: "http://localhost:11434"
        api_key: "mock-key"
        provider: "anthropic"`)
	}
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", handleMessages)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","service":"mockllm"}`)
	})

	log.Printf("mockllm listening on %s  (delay=%v chunks=%d verbose=%v)",
		*flagAddr, *flagDelay, *flagChunks, *flagVerbose)
	if err := http.ListenAndServe(*flagAddr, mux); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
