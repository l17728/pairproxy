package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/config"
)

// ModelRouterClient 调用 MaaS Router API，根据候选模型列表为请求选取最优模型。
// 实现基于 router-api.yaml 规范，仅在分组有多绑定（≥2 个 target）时被调用。
type ModelRouterClient struct {
	url    string
	client *http.Client
	logger *zap.Logger
}

// NewModelRouterClient 创建 ModelRouterClient。
func NewModelRouterClient(cfg config.ModelRouterConfig, logger *zap.Logger) *ModelRouterClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &ModelRouterClient{
		url:    cfg.URL,
		client: &http.Client{Timeout: timeout},
		logger: logger.Named("model_router"),
	}
}

// routerResponse 对应 ModelRouterResponse schema。
type routerResponse struct {
	XSpanID       string      `json:"x_span_id"`
	SessionID     string      `json:"session_id"`
	ModelRankings []modelRank `json:"model_rankings"`
}

type modelRank struct {
	Rank  int     `json:"rank"`
	Model string  `json:"model"`
	Score float64 `json:"score"`
}

// Route 调用 MaaS Router API 并返回排名第一的模型名。
//
//   - reqID: 请求追踪 ID（X-Span-Id）
//   - username: 用户名（X-Domain-Id + X-User-Alias）
//   - sessionID: 会话 ID（注入到请求体的 session_id 字段）
//   - bodyBytes: 原始请求 JSON（OpenAI/Anthropic 格式）
//   - requestedModel: 客户端请求的模型名（保留，不改为 "auto"）
//   - candidateModels: 所有候选模型名（来自绑定 targets 的 supported_models）
//   - dl: 可选的 debug 文件 logger；非 nil 时将完整 HTTP 请求（方法、URL、headers、body）写入 debug 文件
//
// 失败时返回 ("", err)；调用方应 passthrough（保留原始模型）。
func (c *ModelRouterClient) Route(
	ctx context.Context,
	reqID string,
	username string,
	sessionID string,
	bodyBytes []byte,
	requestedModel string,
	candidateModels []string,
	dl *zap.Logger,
) (selectedModel string, rawResponse string, err error) {
	if c.url == "" {
		return "", "", fmt.Errorf("model router URL not configured")
	}

	// 解析原始请求 body（OpenAI/Anthropic 格式）
	var bodyFields map[string]interface{}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &bodyFields); err != nil {
			c.logger.Warn("model_router: failed to parse request body, falling back",
				zap.String("req_id", reqID),
				zap.Error(err),
			)
			return "", "", fmt.Errorf("parse request body: %w", err)
		}
	} else {
		bodyFields = make(map[string]interface{})
	}

	// 注入/覆盖 session_id（保留原始 model，不改为 "auto"）
	bodyFields["session_id"] = sessionID
	if requestedModel != "" {
		bodyFields["model"] = requestedModel
	}
	// 注入 candidate_models（Router 增强字段）
	if len(candidateModels) > 0 {
		bodyFields["candidate_models"] = candidateModels
	}

	reqBody, err := json.Marshal(bodyFields)
	if err != nil {
		return "", "", fmt.Errorf("marshal router request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("create router request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Span-Id", reqID)
	httpReq.Header.Set("X-Domain-Id", username)
	httpReq.Header.Set("X-User-Alias", username)

	if dl != nil {
		dl.Debug("→ model_router HTTP request",
			zap.String("req_id", reqID),
			zap.String("method", httpReq.Method),
			zap.String("url", httpReq.URL.String()),
			zap.String("header_content_type", httpReq.Header.Get("Content-Type")),
			zap.String("header_x_span_id", httpReq.Header.Get("X-Span-Id")),
			zap.String("header_x_domain_id", httpReq.Header.Get("X-Domain-Id")),
			zap.String("header_x_user_alias", httpReq.Header.Get("X-User-Alias")),
			zap.ByteString("body", reqBody),
		)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("router request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("router returned non-200: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read router response: %w", err)
	}

	var result routerResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("parse router response: %w", err)
	}

	if len(result.ModelRankings) == 0 {
		return "", "", fmt.Errorf("router returned empty model_rankings")
	}

	// 取 rank=1 的模型（已按 rank 排序）
	best := result.ModelRankings[0]
	for _, r := range result.ModelRankings {
		if r.Rank < best.Rank {
			best = r
		}
	}

	c.logger.Info("model_router: selected model",
		zap.String("req_id", reqID),
		zap.String("selected_model", best.Model),
		zap.Int("rank", best.Rank),
	)
	return best.Model, string(respBody), nil
}

// extractSessionID 从请求体或请求头中提取会话 ID。
// 按以下优先级：
//  1. 请求体 JSON 字段 "session_id"
//  2. 请求头 "X-Claude-Code-Session-Id"（Claude Code 标准头）
//  3. 请求头 "X-Hermes-Session-Id"（Hermes Agent 标准头）
//  4. 自动生成 "auto-session-{uuid}"
func extractSessionID(r *http.Request, bodyBytes []byte) string {
	// 1. 请求体中的 session_id
	if len(bodyBytes) > 0 {
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(bodyBytes, &body); err == nil && body.SessionID != "" {
			return body.SessionID
		}
	}

	// 2. 请求头 X-Claude-Code-Session-Id
	if sid := r.Header.Get("X-Claude-Code-Session-Id"); sid != "" {
		return sid
	}

	// 3. 请求头 X-Hermes-Session-Id
	if sid := r.Header.Get("X-Hermes-Session-Id"); sid != "" {
		return sid
	}

	// 4. 自动生成
	return "auto-session-" + uuid.NewString()
}

// extractClientIP 从请求中提取真实客户端 IP。
// 优先级：X-Forwarded-For 首项 > X-Real-IP > RemoteAddr。
func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// expandCandidateModels 从 targets 中收集候选模型名。
// 对 targetIDs 中的每个 target，将其 SupportedModels 展开（去重）。
// 跳过含通配符 "*" 的模式（无法作为具体模型名传给 Router）。
// 若某 target 无 SupportedModels 配置，跳过（表示不限制）。
func expandCandidateModels(balancerTargets []lbTarget, targetIDs []string) []string {
	idSet := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		idSet[id] = true
	}

	seen := make(map[string]bool)
	var result []string
	for _, t := range balancerTargets {
		if !idSet[t.id] {
			continue
		}
		for _, m := range t.supportedModels {
			if strings.Contains(m, "*") {
				continue // 跳过通配符模式
			}
			if !seen[m] {
				seen[m] = true
				result = append(result, m)
			}
		}
	}
	return result
}

// lbTarget 是 expandCandidateModels 内部使用的最小化 target 结构，
// 避免直接依赖 lb 包（测试友好）。
type lbTarget struct {
	id              string
	supportedModels []string
	provider        string // "anthropic" | "openai" | "ollama"；空字符串表示未知
}

// groupProviderFromTargets 从 balancerTargets 中找到 groupTargetIDs 对应的 provider。
// 同组多绑定 target 的 provider 必须一致（设计约束），取第一个非空值即可。
// 若所有目标均无 provider 信息，返回空字符串（调用方按 conversionNone 处理）。
func groupProviderFromTargets(balancerTargets []lbTarget, groupTargetIDs []string) string {
	idSet := make(map[string]bool, len(groupTargetIDs))
	for _, id := range groupTargetIDs {
		idSet[id] = true
	}
	for _, t := range balancerTargets {
		if idSet[t.id] && t.provider != "" {
			return t.provider
		}
	}
	return ""
}

// resolveModelToTarget 根据 Router 返回的模型名，在 targetIDs 对应的 balancer targets 中
// 找到第一个支持该模型的 target ID 并返回。
// 若无匹配，返回 targetIDs[0]（兜底）。
func resolveModelToTarget(selectedModel string, balancerTargets []lbTarget, targetIDs []string) string {
	idSet := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		idSet[id] = true
	}

	for _, t := range balancerTargets {
		if !idSet[t.id] {
			continue
		}
		if len(t.supportedModels) == 0 {
			// 未配置 supported_models = 支持所有模型
			return t.id
		}
		if matchModel(selectedModel, t.supportedModels) {
			return t.id
		}
	}

	// 兜底：返回第一个 target
	if len(targetIDs) > 0 {
		return targetIDs[0]
	}
	return ""
}
