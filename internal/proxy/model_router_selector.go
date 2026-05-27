package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/config"
)

// ---------------------------------------------------------------------------
// ModelSelectorQuerier：model_router_selector 对 DB 的最小查询接口
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ModelRouterSelector：带缓存和多模态感知的模型路由选择器
// ---------------------------------------------------------------------------

// ModelRouterSelector 封装 ModelRouterClient，在其之上增加：
//  1. 多模态请求检测 → 从 DB 查询多模态模型列表与 candidateModels 求交集
//  2. Redis session 历史缓存 → 避免重复调用 Router API
//  3. 规模感知决策 → 最近模型为 big 时直接复用
type ModelRouterSelector struct {
	client   *ModelRouterClient
	redis    *redis.Client // nil = 无缓存，每次均调用 Route()
	querier  ModelSelectorQuerier
	historyN int
	redisTTL time.Duration
	logger   *zap.Logger
}

// ModelSelectorQuerier 是 ModelRouterSelector 对模型信息的查询接口。
type ModelSelectorQuerier interface {
	// ListMultimodalNames 返回所有多模态模型名列表（is_multimodal=true）。
	ListMultimodalNames() ([]string, error)
	// ListNonMultimodalNames 返回所有非多模态模型名列表（is_multimodal=false）。
	ListNonMultimodalNames() ([]string, error)
	// GetScaleByName 按模型名返回规模字符串（"big"|"middle"|"small"|""）。
	GetScaleByName(modelName string) (string, error)
}

// modelSelectorQuerierFuncs 是 ModelSelectorQuerier 的函数式实现，用于 main 层桥接。
type modelSelectorQuerierFuncs struct {
	listMultimodalFn    func() ([]string, error)
	listNonMultimodalFn func() ([]string, error)
	getScaleFn          func(string) (string, error)
}

func (f *modelSelectorQuerierFuncs) ListMultimodalNames() ([]string, error) {
	return f.listMultimodalFn()
}
func (f *modelSelectorQuerierFuncs) ListNonMultimodalNames() ([]string, error) {
	return f.listNonMultimodalFn()
}
func (f *modelSelectorQuerierFuncs) GetScaleByName(name string) (string, error) {
	return f.getScaleFn(name)
}

// ModelSelectorQuerierFunc 从三个函数创建 ModelSelectorQuerier，供 main 层注入使用。
func ModelSelectorQuerierFunc(
	listMultimodal func() ([]string, error),
	listNonMultimodal func() ([]string, error),
	getScale func(string) (string, error),
) ModelSelectorQuerier {
	return &modelSelectorQuerierFuncs{
		listMultimodalFn:    listMultimodal,
		listNonMultimodalFn: listNonMultimodal,
		getScaleFn:          getScale,
	}
}

// NewModelRouterSelector 创建 ModelRouterSelector。
//   - client:   底层 HTTP Router 客户端
//   - rdb:      Redis 客户端（nil = 禁用缓存）
//   - querier:  模型信息查询接口
//   - cfg:      model_router 配置（取 SessionHistoryN / Redis.TTL）
//   - logger:   日志
func NewModelRouterSelector(
	client *ModelRouterClient,
	rdb *redis.Client,
	querier ModelSelectorQuerier,
	cfg config.ModelRouterConfig,
	logger *zap.Logger,
) *ModelRouterSelector {
	n := cfg.SessionHistoryN
	if n <= 0 {
		n = 5
	}
	ttl := cfg.Redis.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &ModelRouterSelector{
		client:   client,
		redis:    rdb,
		querier:  querier,
		historyN: n,
		redisTTL: ttl,
		logger:   logger.Named("model_router_selector"),
	}
}

// SelectModelResult 封装 SelectModel 的决策结果，供调用方记录路由元数据。
type SelectModelResult struct {
	Model              string
	RouterResultStatus int    // 0=未调用 Router API, 1=调用成功, 2=调用失败
	RouterRawResponse  string // Router API 原始响应体 JSON（未调用时为空）
	CacheHitScene      int    // 0=无缓存/未命中, 1=缓存满复用, 2=big模型复用, 3=未满非big调API
}

// SelectModel 根据请求内容和 session 历史选取最优模型名。
//
// 决策流程：
//  1. 多模态检测：请求含 image_url/video_url → 取多模态候选集与 candidateModels 交集
//  2. session_id 以 "auto" 开头 或 Redis 未命中 → 调 Route()
//  3. Redis 历史 >= N → 直接使用最近一次（不调 Route）
//  4. Redis 历史 < N：最近模型为 big → 直接返回；否则调 Route()
//
// 调用 Route() 后，若 len(history) < N，将结果追加写入 Redis。
// 失败时返回 (SelectModelResult{}, err)，调用方应 passthrough（保留原始模型）。
func (s *ModelRouterSelector) SelectModel(
	ctx context.Context,
	reqID string,
	username string,
	sessionID string,
	bodyBytes []byte,
	requestedModel string,
	candidateModels []string,
	dl *zap.Logger,
) (SelectModelResult, error) {
	// 入口日志：记录本次路由决策的关键输入，便于全链路追踪
	s.logger.Debug("model_router_selector: SelectModel called",
		zap.String("req_id", reqID),
		zap.String("username", username),
		zap.String("session_id", sessionID),
		zap.String("requested_model", requestedModel),
		zap.Strings("candidate_models", candidateModels),
		zap.Bool("redis_enabled", s.redis != nil),
		zap.Int("history_n", s.historyN),
	)

	// ── Step 1: 多模态 / 非多模态检测，过滤候选集 ────────────────────────────
	if isMultimodalRequest(bodyBytes) {
		return s.handleMultimodal(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
	}
	// 非多模态请求：取 DB 中非多模态模型与 candidateModels 的交集
	candidateModels = s.filterNonMultimodalCandidates(ctx, reqID, sessionID, candidateModels)

	// ── Step 2: session_id 以 "auto" 开头 → 直接调 Route ──────────────────
	if strings.HasPrefix(sessionID, "auto") {
		s.logger.Info("model_router_selector: auto-generated session, bypassing cache",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
		)
		model, raw, err := s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
		if err != nil {
			return SelectModelResult{}, err
		}
		s.writeRedis(ctx, sessionID, model)
		return SelectModelResult{Model: model, RouterResultStatus: 1, RouterRawResponse: raw, CacheHitScene: 0}, nil
	}

	// ── Step 3: 查询 Redis ─────────────────────────────────────────────────
	if s.redis == nil {
		// 无缓存配置：直接调 Route
		s.logger.Debug("model_router_selector: Redis not configured, calling Route directly",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
		)
		model, raw, err := s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
		if err != nil {
			return SelectModelResult{}, err
		}
		return SelectModelResult{Model: model, RouterResultStatus: 1, RouterRawResponse: raw, CacheHitScene: 0}, nil
	}

	history, err := s.loadHistory(ctx, sessionID)
	if err != nil {
		// Redis 故障：退化为调 Route
		s.logger.Warn("model_router_selector: Redis load failed, falling back to Route",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		model, raw, err := s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
		if err != nil {
			return SelectModelResult{}, err
		}
		return SelectModelResult{Model: model, RouterResultStatus: 1, RouterRawResponse: raw, CacheHitScene: 0}, nil
	}

	if history == nil {
		// session 不在缓存中（首次请求）→ 调 Route
		s.logger.Info("model_router_selector: session not in Redis (first request), calling Route",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
		)
		model, raw, err := s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
		if err != nil {
			return SelectModelResult{}, err
		}
		s.writeRedis(ctx, sessionID, model)
		return SelectModelResult{Model: model, RouterResultStatus: 1, RouterRawResponse: raw, CacheHitScene: 0}, nil
	}

	// ── Step 4: 历史 >= N → 直接返回最近一次（不更新 Redis） ───────────────
	if len(history) >= s.historyN {
		last := history[len(history)-1]
		s.logger.Info("model_router_selector: history full, reusing last model",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Int("history_len", len(history)),
			zap.Int("history_n", s.historyN),
			zap.String("model", last),
		)
		return SelectModelResult{Model: last, RouterResultStatus: 0, CacheHitScene: 1}, nil
	}

	// ── Step 5: 历史 < N → 检查最近模型规模 ───────────────────────────────
	last := history[len(history)-1]
	scale, scaleErr := s.querier.GetScaleByName(last)
	if scaleErr != nil {
		s.logger.Warn("model_router_selector: GetScaleByName failed, calling Route",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.String("model", last),
			zap.Error(scaleErr),
		)
	}

	if scaleErr == nil && scale == "big" {
		s.logger.Info("model_router_selector: last model is big, reusing (history not full)",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.String("model", last),
			zap.Int("history_len", len(history)),
			zap.Int("history_n", s.historyN),
		)
		return SelectModelResult{Model: last, RouterResultStatus: 0, CacheHitScene: 2}, nil
	}

	// 最近模型非 big（或规模查询失败）→ 调 Route
	s.logger.Info("model_router_selector: last model is not big, calling Route",
		zap.String("req_id", reqID),
		zap.String("session_id", sessionID),
		zap.String("last_model", last),
		zap.String("scale", scale),
		zap.Int("history_len", len(history)),
		zap.Int("history_n", s.historyN),
	)
	model, raw, err := s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, candidateModels, dl)
	if err != nil {
		return SelectModelResult{}, err
	}
	s.writeRedis(ctx, sessionID, model)
	return SelectModelResult{Model: model, RouterResultStatus: 1, RouterRawResponse: raw, CacheHitScene: 3}, nil
}

// handleMultimodal 处理多模态请求：取多模态模型与 candidateModels 的交集作为候选。
func (s *ModelRouterSelector) handleMultimodal(
	ctx context.Context,
	reqID, username, sessionID string,
	bodyBytes []byte,
	requestedModel string,
	candidateModels []string,
	dl *zap.Logger,
) (SelectModelResult, error) {
	// 多模态检测命中，记录关键上下文
	s.logger.Info("model_router_selector: multimodal request detected",
		zap.String("req_id", reqID),
		zap.String("session_id", sessionID),
		zap.Strings("candidate_models", candidateModels),
	)

	multiNames, err := s.querier.ListMultimodalNames()
	if err != nil {
		s.logger.Warn("model_router_selector: ListMultimodalNames failed, using all candidates",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		multiNames = nil
	} else {
		s.logger.Debug("model_router_selector: multimodal model list from DB",
			zap.String("req_id", reqID),
			zap.Strings("multimodal_models", multiNames),
		)
	}

	effective := intersectModels(multiNames, candidateModels)
	if len(effective) == 0 {
		// 交集为空：兜底使用全部候选
		s.logger.Warn("model_router_selector: multimodal intersection empty, using all candidates as fallback",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Strings("multimodal_models", multiNames),
			zap.Strings("candidate_models", candidateModels),
		)
		effective = candidateModels
	} else {
		s.logger.Info("model_router_selector: multimodal effective candidates after intersection",
			zap.String("req_id", reqID),
			zap.Strings("effective_candidates", effective),
		)
	}

	var model string
	var routerResultStatus int
	var routerRawResponse string
	if len(effective) == 1 {
		// 唯一候选，直接选定，不调 Route
		model = effective[0]
		routerResultStatus = 0
		s.logger.Info("model_router_selector: multimodal single candidate, selected directly",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.String("model", model),
		)
	} else {
		// 多个候选 → 调 Route，传入过滤后的候选集
		s.logger.Info("model_router_selector: multimodal multiple candidates, calling Route",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Strings("effective_candidates", effective),
		)
		model, routerRawResponse, err = s.client.Route(ctx, reqID, username, sessionID, bodyBytes, requestedModel, effective, dl)
		if err != nil {
			return SelectModelResult{}, fmt.Errorf("multimodal route: %w", err)
		}
		routerResultStatus = 1
		s.logger.Info("model_router_selector: multimodal Route selected model",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.String("model", model),
		)
	}

	s.writeRedis(ctx, sessionID, model)
	return SelectModelResult{Model: model, RouterResultStatus: routerResultStatus, RouterRawResponse: routerRawResponse, CacheHitScene: 0}, nil
}

// filterNonMultimodalCandidates 取 DB 非多模态模型列表与 candidateModels 的交集。
// 若 DB 查询失败或交集为空，返回原始 candidateModels（兜底不中断路由）。
func (s *ModelRouterSelector) filterNonMultimodalCandidates(
	ctx context.Context,
	reqID, sessionID string,
	candidateModels []string,
) []string {
	nonMultiNames, err := s.querier.ListNonMultimodalNames()
	if err != nil {
		s.logger.Warn("model_router_selector: ListNonMultimodalNames failed, using all candidates",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		return candidateModels
	}

	s.logger.Debug("model_router_selector: non-multimodal model list from DB",
		zap.String("req_id", reqID),
		zap.Strings("non_multimodal_models", nonMultiNames),
	)

	effective := intersectModels(nonMultiNames, candidateModels)
	if len(effective) == 0 {
		// 交集为空（DB 表未维护或所有候选均为多模态）：兜底使用全部候选
		s.logger.Warn("model_router_selector: non-multimodal intersection empty, using all candidates as fallback",
			zap.String("req_id", reqID),
			zap.String("session_id", sessionID),
			zap.Strings("non_multimodal_models", nonMultiNames),
			zap.Strings("candidate_models", candidateModels),
		)
		return candidateModels
	}

	s.logger.Info("model_router_selector: non-multimodal effective candidates after intersection",
		zap.String("req_id", reqID),
		zap.Strings("effective_candidates", effective),
	)
	return effective
}

// ---------------------------------------------------------------------------
// Redis 辅助
// ---------------------------------------------------------------------------

const redisKeyPrefix = "model_router:"

func redisKey(sessionID string) string {
	return redisKeyPrefix + sessionID
}

// loadHistory 从 Redis 加载 session 模型历史列表。
// key 不存在时返回 (nil, nil)；其他错误返回 (nil, err)。
func (s *ModelRouterSelector) loadHistory(ctx context.Context, sessionID string) ([]string, error) {
	key := redisKey(sessionID)
	val, err := s.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get %q: %w", key, err)
	}
	var history []string
	if err := json.Unmarshal([]byte(val), &history); err != nil {
		return nil, fmt.Errorf("redis unmarshal history for %q: %w", key, err)
	}
	return history, nil
}

// writeRedis 将 model 追加到 session 历史，仅当当前长度 < historyN 时写入。
// 写入失败只打 warn，不影响主流程。
func (s *ModelRouterSelector) writeRedis(ctx context.Context, sessionID, model string) {
	if s.redis == nil || model == "" {
		return
	}

	history, err := s.loadHistory(ctx, sessionID)
	if err != nil {
		s.logger.Warn("model_router_selector: Redis load before write failed",
			zap.String("session_id", sessionID),
			zap.String("model", model),
			zap.Error(err),
		)
		return
	}

	// 已满 N 条：不再追加（保留历史原样）
	if len(history) >= s.historyN {
		s.logger.Debug("model_router_selector: Redis history already full, skipping write",
			zap.String("session_id", sessionID),
			zap.Int("history_len", len(history)),
			zap.Int("history_n", s.historyN),
		)
		return
	}

	history = append(history, model)
	data, err := json.Marshal(history)
	if err != nil {
		s.logger.Warn("model_router_selector: marshal history failed",
			zap.String("session_id", sessionID),
			zap.String("model", model),
			zap.Error(err),
		)
		return
	}

	key := redisKey(sessionID)
	if err := s.redis.Set(ctx, key, data, s.redisTTL).Err(); err != nil {
		s.logger.Warn("model_router_selector: Redis write failed",
			zap.String("session_id", sessionID),
			zap.String("model", model),
			zap.Error(err),
		)
		return
	}
	s.logger.Debug("model_router_selector: Redis history updated",
		zap.String("session_id", sessionID),
		zap.String("model", model),
		zap.Int("new_history_len", len(history)),
		zap.Duration("ttl", s.redisTTL),
	)
}

// ---------------------------------------------------------------------------
// 多模态检测
// ---------------------------------------------------------------------------

// isMultimodalRequest 检查请求体的 messages 中是否含有 image_url 或 video_url 类型的 content block。
// 兼容 Anthropic 和 OpenAI 格式（两者均使用 messages 数组 + content 块数组）。
func isMultimodalRequest(bodyBytes []byte) bool {
	if len(bodyBytes) == 0 {
		return false
	}
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return false
	}
	for _, msg := range req.Messages {
		if containsMediaBlock(msg.Content) {
			return true
		}
	}
	return false
}

// containsMediaBlock 检查单条 message 的 content 字段（可能是 string 或 block 数组）
// 是否含有 image_url 或 video_url 类型的块。
func containsMediaBlock(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	// content 为字符串时，不含多媒体
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return false
	}
	// content 为 block 数组
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "image_url" || b.Type == "video_url" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 集合操作
// ---------------------------------------------------------------------------

// intersectModels 返回 multiNames 和 candidateModels 的交集（保持 candidateModels 顺序）。
// multiNames 为 nil 或空时返回 nil。
func intersectModels(multiNames, candidateModels []string) []string {
	if len(multiNames) == 0 {
		return nil
	}
	set := make(map[string]bool, len(multiNames))
	for _, m := range multiNames {
		set[m] = true
	}
	var result []string
	for _, c := range candidateModels {
		if set[c] {
			result = append(result, c)
		}
	}
	return result
}
