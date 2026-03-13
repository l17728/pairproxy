package api

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// ModelsHandler 处理 GET /v1/models 请求，返回 OpenAI 兼容的模型列表。
// 认证由调用方（mux 层）负责；此 handler 仅负责聚合模型数据并响应。
type ModelsHandler struct {
	logger             *zap.Logger
	modelRepo          *db.LLMTargetModelRepo
	targetStatusFn     func() []interface{} // 可选，获取 target 状态（用于 owned_by 字段）
	llmTargetRepo      *db.LLMTargetRepo
}

// NewModelsHandler 创建 ModelsHandler。
func NewModelsHandler(logger *zap.Logger, modelRepo *db.LLMTargetModelRepo, llmTargetRepo *db.LLMTargetRepo) *ModelsHandler {
	return &ModelsHandler{
		logger:        logger.Named("models_handler"),
		modelRepo:     modelRepo,
		llmTargetRepo: llmTargetRepo,
	}
}

// openAIModel OpenAI /v1/models 响应中的单个模型条目。
type openAIModel struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	OwnedBy string   `json:"owned_by"`
	Targets []string `json:"targets,omitempty"` // 扩展字段：提供该模型的 target 名称列表
	Aliases []string `json:"aliases,omitempty"` // 扩展字段：别名列表
}

// openAIModelList OpenAI /v1/models 响应格式。
type openAIModelList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

// ServeHTTP 处理 GET /v1/models 请求。
func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entries, err := h.modelRepo.ListAll()
	if err != nil {
		h.logger.Error("failed to list model entries", zap.Error(err))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal_error", "message": "failed to retrieve model list"})
		return
	}

	// 查询 target 名称映射（URL → Name）
	targetNames := h.buildTargetNameMap()

	// 聚合：同一 model_id 可能出现在多个 target
	type modelAgg struct {
		targets []string
		aliases []string
		ownedBy string
		created int64
	}
	agg := make(map[string]*modelAgg)
	for _, e := range entries {
		m, ok := agg[e.ModelID]
		if !ok {
			m = &modelAgg{created: time.Now().Unix()}
			agg[e.ModelID] = m
		}
		// target 名称
		name := targetNames[e.TargetURL]
		if name == "" {
			name = e.TargetURL
		}
		// 去重 targets
		found := false
		for _, t := range m.targets {
			if t == name {
				found = true
				break
			}
		}
		if !found {
			m.targets = append(m.targets, name)
		}
		// 合并别名（去重）
		for _, alias := range e.Aliases() {
			dup := false
			for _, a := range m.aliases {
				if a == alias {
					dup = true
					break
				}
			}
			if !dup {
				m.aliases = append(m.aliases, alias)
			}
		}
		// owned_by 取 target provider（第一次）
		if m.ownedBy == "" {
			m.ownedBy = h.providerForTarget(e.TargetURL)
		}
	}

	// 构建响应
	data := make([]openAIModel, 0, len(agg))
	for id, m := range agg {
		data = append(data, openAIModel{
			ID:      id,
			Object:  "model",
			Created: m.created,
			OwnedBy: m.ownedBy,
			Targets: m.targets,
			Aliases: m.aliases,
		})
	}

	// 排序（按 ID 字母序）
	sortOpenAIModels(data)

	resp := openAIModelList{
		Object: "list",
		Data:   data,
	}

	h.logger.Debug("GET /v1/models",
		zap.Int("count", len(data)),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// buildTargetNameMap 查询 llm_targets 表，返回 URL→Name 映射。
func (h *ModelsHandler) buildTargetNameMap() map[string]string {
	result := make(map[string]string)
	if h.llmTargetRepo == nil {
		return result
	}
	targets, err := h.llmTargetRepo.ListAll()
	if err != nil {
		h.logger.Warn("failed to list llm_targets for name mapping", zap.Error(err))
		return result
	}
	for _, t := range targets {
		name := t.Name
		if name == "" {
			name = t.URL
		}
		result[t.URL] = name
	}
	return result
}

// providerForTarget 根据 target URL 查询 provider。
func (h *ModelsHandler) providerForTarget(targetURL string) string {
	if h.llmTargetRepo == nil {
		return "unknown"
	}
	targets, err := h.llmTargetRepo.ListAll()
	if err != nil {
		return "unknown"
	}
	for _, t := range targets {
		if t.URL == targetURL {
			if t.Provider != "" {
				return t.Provider
			}
			return "anthropic"
		}
	}
	return "unknown"
}

// sortOpenAIModels 按 ID 字母序排序（简单插入排序，列表通常很小）。
func sortOpenAIModels(models []openAIModel) {
	for i := 1; i < len(models); i++ {
		key := models[i]
		j := i - 1
		for j >= 0 && models[j].ID > key.ID {
			models[j+1] = models[j]
			j--
		}
		models[j+1] = key
	}
}
