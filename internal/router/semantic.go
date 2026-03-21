package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/corpus"
)

// ctxKeySemanticClassifierType 用于防止分类器子请求递归触发语义路由
type ctxKeySemanticClassifierType struct{}

var ctxKeySemanticClassifier = ctxKeySemanticClassifierType{}

// WithClassifierContext 在 context 中标记当前请求为分类器子请求，
// 使 SemanticRouter.Route() 对其跳过语义路由（防递归）。
func WithClassifierContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySemanticClassifier, true)
}

// IsClassifierContext 报告 ctx 是否属于分类器子请求。
func IsClassifierContext(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySemanticClassifier).(bool)
	return v
}

// RouteRule 一条语义路由规则：自然语言 description → 候选 target URL 集合。
type RouteRule struct {
	ID          string
	Name        string
	Description string   // 送给分类器 LLM 的自然语言描述
	TargetURLs  []string // 匹配后使用的候选 target URL 列表
	Priority    int      // 越大越优先（降序排列后送入 prompt）
	IsActive    bool
}

// ClassifierTarget 抽象分类器 LLM 端点选取。
// 实现者（SProxyClassifierTarget）复用现有 LB 选取一个健康 target。
type ClassifierTarget interface {
	Pick(ctx context.Context) (url, apiKey string, err error)
}

// SemanticRouter 根据请求 messages 的语义意图缩窄候选 target 池。
// 仅对无显式 LLM 绑定的请求（LB 路径）生效。
type SemanticRouter struct {
	logger     *zap.Logger
	rules      []RouteRule
	classifier ClassifierTarget
	timeout    time.Duration // 分类器调用超时，默认 3s
	model      string        // 分类器模型名
	httpClient *http.Client
}

// NewSemanticRouter 创建 SemanticRouter。
// timeout <= 0 时使用默认值 3s；model 为空时使用 "claude-haiku-3-5"。
func NewSemanticRouter(
	logger *zap.Logger,
	rules []RouteRule,
	classifier ClassifierTarget,
	timeout time.Duration,
	model string,
) *SemanticRouter {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if model == "" {
		model = "claude-haiku-3-5"
	}
	return &SemanticRouter{
		logger:     logger.Named("semantic_router"),
		rules:      rules,
		classifier: classifier,
		timeout:    timeout,
		model:      model,
		httpClient: &http.Client{},
	}
}

// SetRules 热更新规则集（DB 写入后调用，线程安全要求由调用方保证——
// 实际上管理操作并发极低，直接赋值即可）。
func (sr *SemanticRouter) SetRules(rules []RouteRule) {
	sr.rules = rules
	sr.logger.Info("semantic router rules updated", zap.Int("count", len(rules)))
}

// Route 检查 messages 语义意图，返回缩窄后的候选 URL 列表。
// 下列情况返回 nil（调用方使用完整 LB 候选池）：
//   - ctx 是分类器子请求（防递归）
//   - 无激活规则
//   - 分类器调用失败或超时
//   - 分类器响应无法匹配任何规则
//   - 匹配规则的 TargetURLs 为空
func (sr *SemanticRouter) Route(ctx context.Context, messages []corpus.Message) []string {
	// 1. 防递归：分类器子请求直接跳过
	if IsClassifierContext(ctx) {
		sr.logger.Debug("semantic router: skipping (classifier sub-request)")
		return nil
	}

	// 2. 过滤出激活规则，按 priority 降序
	active := sr.activeRules()
	if len(active) == 0 {
		sr.logger.Debug("semantic router: no active rules, skipping")
		return nil
	}

	// 3. 构建分类 prompt
	prompt := sr.buildPrompt(messages, active)

	// 4. 带超时调用分类器
	classifyCtx, cancel := context.WithTimeout(WithClassifierContext(ctx), sr.timeout)
	defer cancel()

	matchedIdx, err := sr.classify(classifyCtx, prompt, len(active))
	if err != nil {
		sr.logger.Warn("semantic router: classifier failed, fallback to full pool",
			zap.Error(err))
		return nil
	}

	if matchedIdx < 0 {
		sr.logger.Debug("semantic router: no rule matched (-1), fallback to full pool")
		return nil
	}

	matched := active[matchedIdx]
	if len(matched.TargetURLs) == 0 {
		sr.logger.Debug("semantic router: matched rule has no target URLs, fallback to full pool",
			zap.String("rule", matched.Name))
		return nil
	}

	sr.logger.Info("semantic router: rule matched",
		zap.String("rule_name", matched.Name),
		zap.String("rule_id", matched.ID),
		zap.Int("candidates", len(matched.TargetURLs)))
	return matched.TargetURLs
}

// activeRules 返回 IsActive=true 的规则，按 Priority 降序排列。
func (sr *SemanticRouter) activeRules() []RouteRule {
	var active []RouteRule
	for _, r := range sr.rules {
		if r.IsActive {
			active = append(active, r)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].Priority > active[j].Priority
	})
	return active
}

// maxPromptMessages 送入 prompt 的最大消息条数（取最近 N 条）
const maxPromptMessages = 5

// buildPrompt 构建分类器 prompt。
// messages 只取最后 maxPromptMessages 条，避免 prompt 过长。
func (sr *SemanticRouter) buildPrompt(messages []corpus.Message, rules []RouteRule) string {
	var sb strings.Builder
	sb.WriteString("You are a routing classifier. Given the conversation below, select the BEST\n")
	sb.WriteString("matching rule by responding with ONLY a single integer (0-based index), or -1\n")
	sb.WriteString("if no rule applies. Do not explain your answer.\n\n")

	sb.WriteString("Rules:\n")
	for i, r := range rules {
		fmt.Fprintf(&sb, "[%d] %s: %s\n", i, r.Name, r.Description)
	}

	sb.WriteString("\nConversation (most recent messages):\n")
	start := 0
	if len(messages) > maxPromptMessages {
		start = len(messages) - maxPromptMessages
	}
	for _, m := range messages[start:] {
		content := m.Content
		// 截断超长消息内容
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, content)
	}

	sb.WriteString("\nYour answer (integer only):")
	return sb.String()
}

// classify 调用分类器 LLM，返回匹配的规则索引（0-based）或 -1（无匹配）。
func (sr *SemanticRouter) classify(ctx context.Context, prompt string, ruleCount int) (int, error) {
	targetURL, apiKey, err := sr.classifier.Pick(ctx)
	if err != nil {
		return -1, fmt.Errorf("pick classifier target: %w", err)
	}

	// 构建最简 Anthropic /v1/messages 请求
	reqBody := map[string]interface{}{
		"model":      sr.model,
		"max_tokens": 10,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return -1, fmt.Errorf("marshal classifier request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		targetURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return -1, fmt.Errorf("build classifier http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := sr.httpClient.Do(httpReq)
	if err != nil {
		return -1, fmt.Errorf("classifier http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("classifier returned HTTP %d", resp.StatusCode)
	}

	return sr.parseClassifierResponse(resp.Body, ruleCount)
}

// parseClassifierResponse 从 Anthropic /v1/messages 响应中解析整数索引。
func (sr *SemanticRouter) parseClassifierResponse(body io.Reader, ruleCount int) (int, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return -1, fmt.Errorf("decode classifier response: %w", err)
	}

	for _, c := range result.Content {
		if c.Type != "text" {
			continue
		}
		text := strings.TrimSpace(c.Text)
		idx, err := strconv.Atoi(text)
		if err != nil {
			return -1, fmt.Errorf("parse classifier index %q: not an integer", text)
		}
		if idx == -1 {
			return -1, nil
		}
		if idx < 0 || idx >= ruleCount {
			return -1, fmt.Errorf("classifier returned out-of-range index %d (rule count: %d)", idx, ruleCount)
		}
		return idx, nil
	}
	return -1, fmt.Errorf("classifier response contains no text content")
}
