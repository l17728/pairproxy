// payload_test.go — 验证 mockllm 在 2 的指数大小 payload 下的回显正确性。
//
// 覆盖的 payload 长度（字符数）：1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024
// 模式：streaming（单 chunk / 多 chunk）× non-streaming
//
// 每个测试用例：
//   - 生成精确 n 字符的随机字符串作为 user message content
//   - 通过 httptest.Server 调用 handleMessages
//   - 断言回显内容与发送内容完全相同（字节级别）
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// pow2Sizes 返回从 1 到 1024 的 11 个 2 的幂次 payload 长度（字符数）。
func pow2Sizes() []int {
	sizes := make([]int, 11) // 2^0 … 2^10
	for i := range sizes {
		sizes[i] = 1 << i
	}
	return sizes
}

// genPayload 生成恰好 n 个字符的随机 ASCII 字符串（小写 hex 字符集）。
func genPayload(n int) string {
	raw := make([]byte, (n+1)/2)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)[:n]
}

// postMessages 向 url 发送 POST /v1/messages，返回回显文本。
func postMessages(t *testing.T, url, content string, stream bool) string {
	t.Helper()
	bodyBytes, err := json.Marshal(map[string]any{
		"model":      "test-model",
		"max_tokens": 4096,
		"messages":   []map[string]any{{"role": "user", "content": content}},
		"stream":     stream,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(url+"/v1/messages", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}

	if stream {
		return extractSSEText(t, resp.Body)
	}
	return extractJSONText(t, resp.Body)
}

// extractSSEText 从 Anthropic SSE 流中拼接所有 content_block_delta.text。
func extractSSEText(t *testing.T, r io.Reader) string {
	t.Helper()
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4<<20), 4<<20) // 4 MB，足以处理最大 payload

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // 跳过非 JSON 行（ping 等）
		}
		if ev["type"] == "content_block_delta" {
			if delta, ok := ev["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("SSE scanner error: %v", err)
	}
	return sb.String()
}

// extractJSONText 从非流式响应中拼接所有 text 块。
func extractJSONText(t *testing.T, r io.Reader) string {
	t.Helper()
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	var sb strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// withMockLLM 启动 mockllm httptest.Server，执行 fn，结束后关闭。
// 在 fn 内可通过 srv.URL 向服务器发送请求。
func withMockLLM(t *testing.T, fn func(url string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handleMessages))
	t.Cleanup(srv.Close)
	fn(srv.URL)
}

// ---------------------------------------------------------------------------
// TestPayloadPowersOf2_NonStreaming
// ---------------------------------------------------------------------------

// TestPayloadPowersOf2_NonStreaming 验证 non-streaming 模式下，
// 11 个 2 的幂次大小（1~1024 字符）的 payload 能被完整回显。
func TestPayloadPowersOf2_NonStreaming(t *testing.T) {
	// 重置全局 flag（防止其他测试修改后的副作用）
	*flagChunks = 1
	*flagDelay = 0
	*flagVerbose = false

	withMockLLM(t, func(url string) {
		for _, n := range pow2Sizes() {
			n := n
			t.Run(fmt.Sprintf("chars=%d", n), func(t *testing.T) {
				payload := genPayload(n)
				got := postMessages(t, url, payload, false)
				if got != payload {
					t.Errorf("payload mismatch (len %d):\n  sent: %q\n   got: %q",
						n, trunc(payload, 64), trunc(got, 64))
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// TestPayloadPowersOf2_Streaming
// ---------------------------------------------------------------------------

// TestPayloadPowersOf2_Streaming 验证 streaming（SSE）模式下，
// 11 个 2 的幂次大小的 payload 能被 SSE 解析器完整重组。
func TestPayloadPowersOf2_Streaming(t *testing.T) {
	*flagChunks = 1
	*flagDelay = 0
	*flagVerbose = false

	withMockLLM(t, func(url string) {
		for _, n := range pow2Sizes() {
			n := n
			t.Run(fmt.Sprintf("chars=%d", n), func(t *testing.T) {
				payload := genPayload(n)
				got := postMessages(t, url, payload, true)
				if got != payload {
					t.Errorf("payload mismatch (len %d):\n  sent: %q\n   got: %q",
						n, trunc(payload, 64), trunc(got, 64))
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// TestPayloadPowersOf2_StreamingMultiChunk
// ---------------------------------------------------------------------------

// TestPayloadPowersOf2_StreamingMultiChunk 验证 SSE 多 chunk 分片传输：
// 当服务端将 payload 拆分成 N 片 SSE 事件时，客户端拼接后仍与原文完全一致。
// 覆盖 chunk 数 = 1、4、8（测试边界与均分逻辑）。
func TestPayloadPowersOf2_StreamingMultiChunk(t *testing.T) {
	*flagDelay = 0
	*flagVerbose = false

	chunkCounts := []int{1, 4, 8}

	for _, chunks := range chunkCounts {
		chunks := chunks
		t.Run(fmt.Sprintf("chunks=%d", chunks), func(t *testing.T) {
			// 每组 chunk 数共用一个 httptest.Server（避免端口耗尽）
			*flagChunks = chunks
			withMockLLM(t, func(url string) {
				for _, n := range pow2Sizes() {
					n := n
					t.Run(fmt.Sprintf("chars=%d", n), func(t *testing.T) {
						payload := genPayload(n)
						got := postMessages(t, url, payload, true)
						if got != payload {
							t.Errorf("payload mismatch (len %d, chunks %d):\n  sent: %q\n   got: %q",
								n, chunks, trunc(payload, 64), trunc(got, 64))
						}
					})
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// TestPayloadPowersOf2_StreamingWithDelay
// ---------------------------------------------------------------------------

// TestPayloadPowersOf2_StreamingWithDelay 验证带人工延迟（模拟真实 LLM 吐字速度）
// 时各大小 payload 的完整性。延迟设为 1ms，避免测试过慢的同时仍能覆盖时序路径。
func TestPayloadPowersOf2_StreamingWithDelay(t *testing.T) {
	*flagChunks = 4
	*flagDelay = 1 * time.Millisecond
	*flagVerbose = false
	t.Cleanup(func() { *flagDelay = 0 }) // 恢复全局状态

	withMockLLM(t, func(url string) {
		for _, n := range pow2Sizes() {
			n := n
			t.Run(fmt.Sprintf("chars=%d", n), func(t *testing.T) {
				payload := genPayload(n)
				got := postMessages(t, url, payload, true)
				if got != payload {
					t.Errorf("payload mismatch (len %d with delay):\n  sent: %q\n   got: %q",
						n, trunc(payload, 64), trunc(got, 64))
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// TestPayloadPowersOf2_RoundTrip（流式 + 非流式交替）
// ---------------------------------------------------------------------------

// TestPayloadPowersOf2_RoundTrip 对每个 2 的幂次大小，同时测试 streaming 和
// non-streaming 两种模式，断言两者均能完整回显相同 payload（同一 payload 发两次）。
func TestPayloadPowersOf2_RoundTrip(t *testing.T) {
	*flagChunks = 1
	*flagDelay = 0
	*flagVerbose = false

	withMockLLM(t, func(url string) {
		for _, n := range pow2Sizes() {
			n := n
			t.Run(fmt.Sprintf("chars=%d", n), func(t *testing.T) {
				payload := genPayload(n)

				gotStream := postMessages(t, url, payload, true)
				if gotStream != payload {
					t.Errorf("streaming mismatch (len %d): sent=%q got=%q",
						n, trunc(payload, 64), trunc(gotStream, 64))
				}

				gotJSON := postMessages(t, url, payload, false)
				if gotJSON != payload {
					t.Errorf("non-streaming mismatch (len %d): sent=%q got=%q",
						n, trunc(payload, 64), trunc(gotJSON, 64))
				}

				// 两种模式的回显必须一致
				if gotStream != gotJSON {
					t.Errorf("streaming vs non-streaming diverge (len %d):\n  stream: %q\n    json: %q",
						n, trunc(gotStream, 64), trunc(gotJSON, 64))
				}
			})
		}
	})
}
