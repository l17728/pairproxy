package dashboard

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// modelPageRow 模型聚合列表页的一行数据。
type modelPageRow struct {
	ModelID        string
	AliasesDisplay []string
	TargetURL      string
	TargetName     string
	UpstreamName   string
	IsDefault      bool
	Source         string
}

// modelsPageData /dashboard/models 页面的模板数据。
type modelsPageData struct {
	baseData
	Models       []modelPageRow
	DefaultModel string
}

// handleModelsPage 渲染 /dashboard/models 页面。
func (h *Handler) handleModelsPage(w http.ResponseWriter, r *http.Request) {
	var rows []modelPageRow

	if h.llmTargetModelRepo != nil {
		entries, err := h.llmTargetModelRepo.ListAll()
		if err != nil {
			h.logger.Error("failed to list model entries for dashboard", zap.Error(err))
		} else {
			// 构建 target URL → Name 映射
			targetNames := make(map[string]string)
			if h.llmTargetRepo != nil {
				if targets, listErr := h.llmTargetRepo.ListAll(); listErr == nil {
					for _, t := range targets {
						name := t.Name
						if name == "" {
							name = t.URL
						}
						targetNames[t.URL] = name
					}
				}
			}

			rows = make([]modelPageRow, 0, len(entries))
			for _, e := range entries {
				name := targetNames[e.TargetURL]
				if name == "" {
					name = e.TargetURL
				}
				upstreamDisplay := e.UpstreamName
				if upstreamDisplay == e.ModelID {
					upstreamDisplay = "" // 相同则不显示，模板会显示"（同模型 ID）"
				}
				rows = append(rows, modelPageRow{
					ModelID:        e.ModelID,
					AliasesDisplay: e.Aliases(),
					TargetURL:      e.TargetURL,
					TargetName:     name,
					UpstreamName:   upstreamDisplay,
					IsDefault:      e.IsDefault,
					Source:         e.Source,
				})
			}
		}
	}

	data := modelsPageData{
		Models:       rows,
		DefaultModel: h.defaultModel,
	}

	h.renderPage(w, "models.html", data)
}

// SetLLMTargetModelRepo 设置模型条目仓库（用于模型路由页面）。
func (h *Handler) SetLLMTargetModelRepo(repo *db.LLMTargetModelRepo) {
	h.llmTargetModelRepo = repo
}

// SetDefaultModel 设置全局默认模型（用于 models 页面展示）。
func (h *Handler) SetDefaultModel(model string) {
	h.defaultModel = model
}
