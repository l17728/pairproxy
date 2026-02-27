// Package alert 提供 webhook 告警通知功能。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// 告警事件类型常量
const (
	EventQuotaExceeded = "quota_exceeded" // token 配额超限
	EventRateLimited   = "rate_limited"   // 请求频率超限
	EventNodeDown      = "node_down"      // 集群节点下线
	EventNodeRecovered = "node_recovered" // 集群节点恢复
)

// Event 告警事件
type Event struct {
	Kind    string            `json:"kind"`              // 事件类型（见上方常量）
	At      time.Time         `json:"at"`                // 事件发生时间
	Message string            `json:"message"`           // 可读描述
	Labels  map[string]string `json:"labels,omitempty"`  // 附加标签（user_id 等）
}

// Notifier 向 webhook URL 异步发送告警事件。
// webhookURL 为空时所有操作均为 no-op。
type Notifier struct {
	webhookURL string
	client     *http.Client
	logger     *zap.Logger
}

// NewNotifier 创建 Notifier。
// webhookURL 为空时返回静默（no-op）的 Notifier。
func NewNotifier(logger *zap.Logger, webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 5 * time.Second},
		logger:     logger.Named("alert"),
	}
}

// Notify 异步发送告警（非阻塞）。
// 若 webhookURL 为空，则不做任何事。
func (n *Notifier) Notify(evt Event) {
	if n.webhookURL == "" {
		return
	}
	if evt.At.IsZero() {
		evt.At = time.Now()
	}
	go n.send(evt)
}

func (n *Notifier) send(evt Event) {
	body, err := json.Marshal(evt)
	if err != nil {
		n.logger.Error("alert: marshal failed", zap.Error(err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Error("alert: create request failed",
			zap.String("kind", evt.Kind),
			zap.Error(err),
		)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PairProxy/1.0")

	resp, err := n.client.Do(req)
	if err != nil {
		n.logger.Warn("alert: webhook send failed",
			zap.String("kind", evt.Kind),
			zap.String("url", n.webhookURL),
			zap.Error(err),
		)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		n.logger.Warn("alert: webhook returned error status",
			zap.String("kind", evt.Kind),
			zap.Int("status", resp.StatusCode),
		)
		return
	}
	n.logger.Info("alert sent",
		zap.String("kind", evt.Kind),
		zap.Int("status", resp.StatusCode),
	)
}
