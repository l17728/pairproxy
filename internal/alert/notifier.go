// Package alert 提供 webhook 告警通知功能。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"text/template"
	"time"

	"go.uber.org/zap"
)

// 告警事件类型常量
const (
	EventQuotaExceeded = "quota_exceeded" // token 配额超限
	EventRateLimited   = "rate_limited"   // 请求频率超限
	EventNodeDown      = "node_down"      // 集群节点下线
	EventNodeRecovered = "node_recovered" // 集群节点恢复
	EventHighLoad      = "high_load"      // 活跃请求数上穿告警阈值
	EventLoadRecovered = "load_recovered" // 活跃请求数恢复到阈值以下
)

// Event 告警事件
type Event struct {
	Kind    string            `json:"kind"`             // 事件类型（见上方常量）
	At      time.Time         `json:"at"`               // 事件发生时间
	Message string            `json:"message"`          // 可读描述
	Labels  map[string]string `json:"labels,omitempty"` // 附加标签（user_id 等）
}

// webhookTarget 内部表示一个已解析的 webhook 目标（含编译后的模板）。
type webhookTarget struct {
	url      string
	events   map[string]bool  // nil 表示所有事件
	tmpl     *template.Template // nil 表示使用默认 JSON 序列化
}

// matches 检查该 target 是否需要处理 kind 事件。
func (t *webhookTarget) matches(kind string) bool {
	if len(t.events) == 0 {
		return true
	}
	return t.events[kind]
}

// Notifier 向一组 webhook URL 异步广播告警事件。
// 若无任何目标，所有操作均为 no-op。
type Notifier struct {
	targets []webhookTarget
	client  *http.Client
	logger  *zap.Logger
}

// NewNotifier 创建仅有单一 URL 的 Notifier（向后兼容）。
// webhookURL 为空时返回静默（no-op）的 Notifier。
func NewNotifier(logger *zap.Logger, webhookURL string) *Notifier {
	n := &Notifier{
		client: &http.Client{Timeout: 5 * time.Second},
		logger: logger.Named("alert"),
	}
	if webhookURL != "" {
		n.targets = []webhookTarget{{url: webhookURL}}
	}
	return n
}

// WebhookTargetConfig 是外部传入的目标配置（与 config.WebhookTarget 结构一致，
// 在此处重新定义以避免 alert 包循环依赖 config 包）。
type WebhookTargetConfig struct {
	URL      string
	Events   []string
	Template string
}

// NewNotifierMulti 根据多个 WebhookTargetConfig 创建 Notifier。
// 若 targets 为空，返回 no-op Notifier。
// 若同时提供了旧式 legacyURL，将其追加为最后一个无过滤目标（向后兼容）。
func NewNotifierMulti(logger *zap.Logger, targets []WebhookTargetConfig, legacyURL string) *Notifier {
	n := &Notifier{
		client: &http.Client{Timeout: 5 * time.Second},
		logger: logger.Named("alert"),
	}
	for _, cfg := range targets {
		if cfg.URL == "" {
			continue
		}
		wt := webhookTarget{url: cfg.URL}
		if len(cfg.Events) > 0 {
			wt.events = make(map[string]bool, len(cfg.Events))
			for _, ev := range cfg.Events {
				wt.events[strings.TrimSpace(ev)] = true
			}
		}
		if cfg.Template != "" {
			tmpl, err := template.New("webhook").Parse(cfg.Template)
			if err != nil {
				logger.Warn("alert: invalid webhook template, using default JSON",
					zap.String("url", cfg.URL),
					zap.Error(err),
				)
			} else {
				wt.tmpl = tmpl
			}
		}
		n.targets = append(n.targets, wt)
	}
	// 向后兼容：若旧 alert_webhook 非空且不在 targets 中重复，追加为无过滤目标
	if legacyURL != "" {
		dup := false
		for _, t := range n.targets {
			if t.url == legacyURL {
				dup = true
				break
			}
		}
		if !dup {
			n.targets = append(n.targets, webhookTarget{url: legacyURL})
		}
	}
	return n
}

// Notify 异步广播告警事件到所有匹配的 webhook 目标（非阻塞）。
func (n *Notifier) Notify(evt Event) {
	if len(n.targets) == 0 {
		return
	}
	if evt.At.IsZero() {
		evt.At = time.Now()
	}
	for i := range n.targets {
		t := n.targets[i]
		if !t.matches(evt.Kind) {
			continue
		}
		go n.send(t, evt)
	}
}

func (n *Notifier) send(t webhookTarget, evt Event) {
	var body []byte
	var err error

	if t.tmpl != nil {
		var buf bytes.Buffer
		if err = t.tmpl.Execute(&buf, evt); err != nil {
			n.logger.Error("alert: template render failed",
				zap.String("url", t.url),
				zap.Error(err),
			)
			return
		}
		body = buf.Bytes()
	} else {
		body, err = json.Marshal(evt)
		if err != nil {
			n.logger.Error("alert: marshal failed", zap.Error(err))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		n.logger.Error("alert: create request failed",
			zap.String("kind", evt.Kind),
			zap.String("url", t.url),
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
			zap.String("url", t.url),
			zap.Error(err),
		)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		n.logger.Warn("alert: webhook returned error status",
			zap.String("kind", evt.Kind),
			zap.String("url", t.url),
			zap.Int("status", resp.StatusCode),
		)
		return
	}
	n.logger.Info("alert sent",
		zap.String("kind", evt.Kind),
		zap.String("url", t.url),
		zap.Int("status", resp.StatusCode),
	)
}
