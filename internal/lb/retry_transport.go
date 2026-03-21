package lb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"go.uber.org/zap"
)

// LLMTargetInfo 携带 RetryTransport 执行单次请求所需的最小 target 信息。
type LLMTargetInfo struct {
	URL    string // 完整 URL，如 "https://api.anthropic.com"
	APIKey string // Bearer token，用于 Authorization 头
}

// RetryTransport 是带重试的 LLM 上游 http.RoundTripper。
//
// 在连接错误或上游返回 5xx 时，自动切换到下一个健康 target 并重试。
// 不重试：4xx 响应（客户端错误无法通过换 target 解决）、context 取消/超时。
// 若 RetryOnStatus 非空，其中列出的状态码也会触发 try-next（如 429 配额耗尽）。
// body 缓冲：因 LLM 请求体通常为小型 JSON，重试前缓冲 body 以便重放。
type RetryTransport struct {
	// Inner 底层 transport（通常为 http.DefaultTransport 或带超时的 transport）
	Inner http.RoundTripper

	// MaxRetries 最大额外重试次数（不含首次尝试）。0 = 不重试。
	MaxRetries int

	// RetryOnStatus 除 5xx/连接错误外，触发 try-next 的额外 HTTP 状态码列表。
	// 典型用法：[]int{429}，使配额耗尽时自动切换到下一个 target。
	// 空列表（默认）= 仅重试 5xx 和连接错误，行为与旧版本完全一致。
	RetryOnStatus []int

	// PickNext 根据请求路径和已尝试的 URL 列表，返回下一个 target。
	// 无可用 target 时返回 error（通常是 ErrNoHealthyTarget）。
	PickNext func(path string, tried []string) (*LLMTargetInfo, error)

	// OnSuccess 请求成功时回调（用于被动健康计数重置）
	OnSuccess func(targetURL string)

	// OnFailure 请求失败时回调（用于被动熔断计数）
	OnFailure func(targetURL string)

	// Logger 结构化日志
	Logger *zap.Logger
}

// RoundTrip 实现 http.RoundTripper 接口。
// 首次尝试使用 req.URL 指定的 target；失败后通过 PickNext 选取备用 target 并克隆请求重试。
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 1. 缓冲 body 以便重试时重放
	var bodyBuf []byte
	if req.Body != nil {
		var err error
		bodyBuf, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("retry_transport: read request body: %w", err)
		}
	}

	tried := []string{}
	currentURL := req.URL.Scheme + "://" + req.URL.Host

	for attempt := 0; ; attempt++ {
		// 恢复 body
		if len(bodyBuf) > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBuf))
			req.ContentLength = int64(len(bodyBuf))
		}

		resp, err := t.Inner.RoundTrip(req)

		// 不重试：context 取消/超时
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resp, err
		}

		// 成功：2xx / 3xx，以及不在 RetryOnStatus 中的 4xx
		if err == nil && resp.StatusCode < 500 && !t.isRetriableStatus(resp.StatusCode) {
			if t.OnSuccess != nil {
				t.OnSuccess(currentURL)
			}
			return resp, nil
		}

		// 失败：记录被动熔断
		if t.OnFailure != nil {
			t.OnFailure(currentURL)
		}
		tried = append(tried, currentURL)

		// 耗尽 5xx body，关闭连接
		if resp != nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
		}

		if attempt >= t.MaxRetries {
			// 已达最大重试次数，返回最后一次的 error
			if err != nil {
				return nil, fmt.Errorf("llm request failed after %d retries (target=%s): %w", attempt+1, currentURL, err)
			}
			// 最后一次为失败状态码：返回 error 让 ReverseProxy 触发 ErrorHandler
			var lastStatus int
			if resp != nil {
				lastStatus = resp.StatusCode
			}
			return nil, fmt.Errorf("all %d LLM targets exhausted (last target=%s, last status=%d)", attempt+1, currentURL, lastStatus)
		}

		// 选下一个 target
		if t.PickNext == nil {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("llm upstream returned 5xx and no PickNext configured")
		}
		next, pickErr := t.PickNext(req.URL.Path, tried)
		if pickErr != nil {
			if err != nil {
				return nil, fmt.Errorf("llm request failed (no more targets): %w", err)
			}
			return nil, fmt.Errorf("llm 5xx and no more targets: %w", pickErr)
		}

		// 克隆请求，更新 URL + Authorization（不同 target 可能有不同 API Key）
		nextURL, parseErr := url.Parse(next.URL)
		if parseErr != nil {
			return nil, fmt.Errorf("retry_transport: parse next target URL %q: %w", next.URL, parseErr)
		}
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = nextURL.Scheme
		cloned.URL.Host = nextURL.Host
		cloned.Host = nextURL.Host
		if next.APIKey != "" {
			cloned.Header.Set("Authorization", "Bearer "+next.APIKey)
		}

		reason := "connection error"
		if err == nil && resp != nil {
			reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		t.Logger.Warn("llm request failed, retrying with next target",
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", t.MaxRetries),
			zap.String("failed_target", currentURL),
			zap.String("next_target", next.URL),
			zap.String("reason", reason),
		)

		req = cloned
		currentURL = next.URL
	}
}

// isRetriableStatus 检查 HTTP 状态码是否在 RetryOnStatus 列表中。
func (t *RetryTransport) isRetriableStatus(code int) bool {
	for _, s := range t.RetryOnStatus {
		if s == code {
			return true
		}
	}
	return false
}
