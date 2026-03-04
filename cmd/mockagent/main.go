// mockagent — 模拟 AI 客户端（Claude Code），用于本地链路测试。
//
// 行为：向 cproxy 随机发送字符串消息，接收响应后与发送内容对比。
// 相同 → PASS；不同 → FAIL；连接失败 → ERROR。
//
// 典型部署链路：
//
//	mockagent → cproxy(:8080) → sproxy(:9000) → mockllm(:11434)
//
// 前提条件：
//  1. cproxy 已完成登录（运行过 `cproxy login --server http://localhost:9000`）
//  2. sproxy 已配置 mockllm 为 LLM 目标（见 mockllm --help）
//
// 使用示例：
//
//	mockagent --count 20 --concurrency 4 --stream
//	mockagent --url http://localhost:8080 --count 5 --no-stream --v
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	flagURL         = flag.String("url", "http://localhost:8080", "cproxy 地址")
	flagCount       = flag.Int("count", 10, "发送请求总数")
	flagConcurrency = flag.Int("concurrency", 1, "并发数")
	flagStream      = flag.Bool("stream", true, "使用 SSE 流式响应（false = JSON 非流式）")
	flagModel       = flag.String("model", "claude-3-5-sonnet-20241022", "模型名称（sproxy 路由使用）")
	flagLen         = flag.Int("len", 64, "随机 payload 长度（字节，hex 编码后为 2x）")
	flagVerbose     = flag.Bool("v", false, "每条请求打印详情")
	flagTimeout     = flag.Duration("timeout", 30*time.Second, "单次请求超时")
	flagStopOnFail  = flag.Bool("stop-on-fail", false, "遇到 FAIL/ERROR 立即停止")
)

// ---------------------------------------------------------------------------
// 随机字符串
// ---------------------------------------------------------------------------

// randomPayload 生成 n 字节的加密随机数据，以 hex 字符串返回。
func randomPayload(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read: %v", err))
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// HTTP 请求
// ---------------------------------------------------------------------------

var httpClient *http.Client

func doRequest(content string) (received string, duration time.Duration, err error) {
	start := time.Now()

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":      *flagModel,
		"max_tokens": 1024,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"stream": *flagStream,
	})

	req, err := http.NewRequest(http.MethodPost, *flagURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// cproxy 会剥离此 Authorization 头，替换为用户 JWT；此处任意值均可。
	req.Header.Set("Authorization", "Bearer mock-api-key")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Since(start), fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	duration = time.Since(start)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", duration, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if *flagStream {
		received, err = parseSSE(resp.Body)
	} else {
		received, err = parseJSON(resp.Body)
	}
	return received, duration, err
}

// ---------------------------------------------------------------------------
// SSE 解析
// ---------------------------------------------------------------------------

// parseSSE 从 Anthropic SSE 流中提取所有 content_block_delta.text 并拼接。
func parseSSE(r io.Reader) (string, error) {
	var sb strings.Builder

	// 增大 scanner buffer 以应对长内容（默认 64KB 可能不够）。
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // 忽略非 JSON 行（如 ping）
		}

		if event["type"] == "content_block_delta" {
			if delta, ok := event["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
	}

	return sb.String(), scanner.Err()
}

// ---------------------------------------------------------------------------
// JSON 解析
// ---------------------------------------------------------------------------

// parseJSON 从非流式 Anthropic JSON 响应中提取文本内容。
func parseJSON(r io.Reader) (string, error) {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return "", fmt.Errorf("decode JSON response: %w", err)
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String(), nil
}

// ---------------------------------------------------------------------------
// 测试循环
// ---------------------------------------------------------------------------

type result struct {
	n        int
	sent     string
	received string
	dur      time.Duration
	err      error
}

func (r result) pass() bool  { return r.err == nil && r.sent == r.received }
func (r result) label() string {
	switch {
	case r.err != nil:
		return "ERROR"
	case r.sent == r.received:
		return "PASS "
	default:
		return "FAIL "
	}
}

func main() {
	flag.Usage = func() {
		fmt.Println(`mockagent — AI client mock for proxy chain testing

Usage:
  mockagent [flags]

Flags:`)
		flag.PrintDefaults()
	}
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	httpClient = &http.Client{Timeout: *flagTimeout}

	mode := "streaming"
	if !*flagStream {
		mode = "non-streaming"
	}
	fmt.Printf("mockagent: %s × %d (concurrency=%d) → %s\n",
		mode, *flagCount, *flagConcurrency, *flagURL)
	fmt.Println(strings.Repeat("─", 64))

	var (
		total   atomic.Int64
		passed  atomic.Int64
		failed  atomic.Int64
		errored atomic.Int64
	)

	sem  := make(chan struct{}, *flagConcurrency)
	stop := make(chan struct{})
	var once sync.Once
	signalStop := func() { once.Do(func() { close(stop) }) }

	var wg sync.WaitGroup
	startAll := time.Now()

	for i := 0; i < *flagCount; i++ {
		// 检查是否已触发停止
		select {
		case <-stop:
			break
		default:
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(n int) {
			defer wg.Done()
			defer func() { <-sem }()

			payload := randomPayload(*flagLen)
			received, dur, err := doRequest(payload)

			r := result{n: n + 1, sent: payload, received: received, dur: dur, err: err}
			total.Add(1)

			switch {
			case err != nil:
				errored.Add(1)
				failed.Add(1)
				log.Printf("[%4d] %s  err=%v  (%v)", r.n, r.label(), err, dur.Truncate(time.Millisecond))
			case r.pass():
				passed.Add(1)
				if *flagVerbose {
					log.Printf("[%4d] %s  payload=%s  (%v)", r.n, r.label(), trunc(payload, 16), dur.Truncate(time.Millisecond))
				}
			default:
				failed.Add(1)
				log.Printf("[%4d] %s  sent=%s  received=%s  (%v)",
					r.n, r.label(),
					trunc(payload, 32), trunc(received, 32),
					dur.Truncate(time.Millisecond))
			}

			if *flagStopOnFail && !r.pass() {
				signalStop()
			}
		}(i)
	}

	wg.Wait()

	elapsed := time.Since(startAll)
	fmt.Println(strings.Repeat("─", 64))
	fmt.Printf("Total: %d  Pass: %d  Fail: %d  Error: %d  Time: %v\n",
		total.Load(), passed.Load(), failed.Load(), errored.Load(),
		elapsed.Truncate(time.Millisecond))

	if passed.Load() == total.Load() {
		fmt.Println("✓ All checks passed — proxy chain is working correctly.")
	} else {
		fmt.Printf("✗ %d check(s) failed — proxy chain has issues.\n", failed.Load())
		os.Exit(1)
	}
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
